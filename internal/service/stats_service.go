package service

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"lumo/internal/domain"
)

// StatsService 实现教师统计（API 设计文档 7.11 / 完整设计文档 4.22 C6）。
// 全部数值由服务端从 classes/assignment_submissions 等表聚合，只读接口，不允许前端传入数值。
type StatsService struct{ s *Services }

// ClassStatsReq 请求班级统计（教师/班级创建者）。
type ClassStatsReq struct {
	WorkspaceID  string `json:"workspace_id"`
	UserID       string `json:"user_id"`
	ClassID      string `json:"class_id"`
	AssignmentID string `json:"assignment_id,omitempty"` // 可选：按作业过滤
}

// ClassStats 是班级统计响应（完成率/均分/正确率/薄弱知识点 Top）。
type ClassStats struct {
	ClassID         string                     `json:"class_id"`
	AssignmentID    string                     `json:"assignment_id,omitempty"`
	StudentTotal    int                        `json:"student_total"`    // 班级活跃学生数
	AssignmentTotal int                        `json:"assignment_total"` // 统计范围作业数
	SubmissionTotal int                        `json:"submission_total"` // 已提交份数
	GradedTotal     int                        `json:"graded_total"`     // 已判分份数
	CompletionRate  float64                    `json:"completion_rate"`  // 完成率 = 提交份数/(学生数×作业数)
	AvgScore        float64                    `json:"avg_score"`        // 均分 = 已判分提交 overall 均值
	MaxScore        float64                    `json:"max_score"`        // 满分 = 已判分提交满分均值
	Accuracy        float64                    `json:"accuracy"`         // 正确率 = 得分/满分（客观题）
	WeakTop         []domain.WeakKnowledgeItem `json:"weak_top"`         // 薄弱知识点 Top5
}

// gradeBody 解析 grade_json 的总体与明细项。
type gradeBody struct {
	Overall float64 `json:"overall"`
	Items   []struct {
		QuestionVersionID string  `json:"question_version_id"`
		MaxScore          float64 `json:"max_score"`
		Score             float64 `json:"score"`
		Status            string  `json:"status"`
	} `json:"items"`
}

// ClassStats 聚合班级统计：班级维度完成率/均分/正确率，以及知识点薄弱 Top。
func (st *StatsService) ClassStats(ctx context.Context, req ClassStatsReq) (*ClassStats, error) {
	if err := st.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.ClassID == "" {
		return nil, domain.InvalidArg("class_id 必填")
	}
	if _, err := st.s.Classes.assertEditableClass(ctx, req.WorkspaceID, req.UserID, "stats.class", req.ClassID); err != nil {
		return nil, err
	}
	db := st.s.Repo.DB()
	out := &ClassStats{ClassID: req.ClassID, AssignmentID: req.AssignmentID}

	// 学生总数（活跃成员）
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM class_members
		WHERE class_id = ? AND status = 'active'`, req.ClassID).Scan(&out.StudentTotal); err != nil {
		return nil, dbErr(err)
	}

	// 统计范围作业数；指定 assignment_id 时校验其属于该班级
	args := []any{req.ClassID}
	where := `class_id = ?`
	if req.AssignmentID != "" {
		where += ` AND id = ?`
		args = append(args, req.AssignmentID)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM assignments WHERE `+where, args...).Scan(&out.AssignmentTotal); err != nil {
		return nil, dbErr(err)
	}
	if req.AssignmentID != "" && out.AssignmentTotal == 0 {
		return nil, domain.NotFound("作业不存在")
	}

	// 提交聚合
	subArgs := []any{req.ClassID}
	subWhere := `a.class_id = ?`
	if req.AssignmentID != "" {
		subWhere += ` AND s.assignment_id = ?`
		subArgs = append(subArgs, req.AssignmentID)
	}
	rows, err := db.QueryContext(ctx, `
		SELECT s.grade_json, s.graded_at
		FROM assignment_submissions s
		JOIN assignments a ON a.id = s.assignment_id
		WHERE `+subWhere, subArgs...)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()

	var overallSum, maxSum, scoreSum, maxScoreSum float64
	wrongByQVID := map[string]int{}
	qvids := map[string]bool{}
	for rows.Next() {
		var raw string
		var gradedAt *string
		if err := rows.Scan(&raw, &gradedAt); err != nil {
			return nil, dbErr(err)
		}
		out.SubmissionTotal++
		if gradedAt == nil {
			continue // 未判分不参与均分/正确率/薄弱统计
		}
		out.GradedTotal++
		var g gradeBody
		if err := json.Unmarshal([]byte(raw), &g); err != nil {
			continue // 判分 JSON 异常时跳过该提交，不阻断统计
		}
		overallSum += g.Overall
		var subMax float64
		for _, it := range g.Items {
			subMax += it.MaxScore
			if it.Status != "graded" || it.MaxScore <= 0 {
				continue
			}
			maxScoreSum += it.MaxScore
			scoreSum += it.Score
			if it.Score < it.MaxScore {
				wrongByQVID[it.QuestionVersionID]++
				qvids[it.QuestionVersionID] = true
			}
		}
		maxSum += subMax
	}
	if err := rows.Err(); err != nil {
		return nil, dbErr(err)
	}

	if out.GradedTotal > 0 {
		out.AvgScore = overallSum / float64(out.GradedTotal)
		out.MaxScore = maxSum / float64(out.GradedTotal)
	}
	if maxScoreSum > 0 {
		out.Accuracy = scoreSum / maxScoreSum
	}
	if denom := out.StudentTotal * out.AssignmentTotal; denom > 0 {
		out.CompletionRate = float64(out.SubmissionTotal) / float64(denom)
	}

	// 薄弱知识点 Top：question_version_id → knowledge 映射
	if len(qvids) > 0 {
		ids := make([]string, 0, len(qvids))
		for qvid := range qvids {
			ids = append(ids, qvid)
		}
		ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
		inArgs := make([]any, 0, len(ids))
		for _, id := range ids {
			inArgs = append(inArgs, id)
		}
		kmRows, err := db.QueryContext(ctx, `
			SELECT qk.question_version_id, k.id, k.name
			FROM question_knowledge qk
			JOIN knowledge_nodes k ON k.id = qk.knowledge_id
			WHERE qk.question_version_id IN (`+ph+`)`, inArgs...)
		if err != nil {
			return nil, dbErr(err)
		}
		knowByQVID := map[string]string{} // qvid -> knowledgeID
		nameByID := map[string]string{}
		for kmRows.Next() {
			var qvid, kid, name string
			if err := kmRows.Scan(&qvid, &kid, &name); err != nil {
				kmRows.Close()
				return nil, dbErr(err)
			}
			knowByQVID[qvid] = kid
			nameByID[kid] = name
		}
		kmRows.Close()
		if err := kmRows.Err(); err != nil {
			return nil, dbErr(err)
		}

		wrongByKID := map[string]int{}
		for qvid, cnt := range wrongByQVID {
			if kid, ok := knowByQVID[qvid]; ok {
				wrongByKID[kid] += cnt
			}
		}
		top := make([]domain.WeakKnowledgeItem, 0, len(wrongByKID))
		for kid, cnt := range wrongByKID {
			top = append(top, domain.WeakKnowledgeItem{
				KnowledgeID: kid,
				Name:        nameByID[kid],
				WrongCount:  cnt,
			})
		}
		sort.Slice(top, func(i, j int) bool {
			if top[i].WrongCount != top[j].WrongCount {
				return top[i].WrongCount > top[j].WrongCount
			}
			return top[i].Name < top[j].Name
		})
		if len(top) > 5 {
			top = top[:5]
		}
		out.WeakTop = top
	}

	st.s.audit(ctx, req.WorkspaceID, "stats.class", "class", req.ClassID,
		map[string]any{"assignment_id": req.AssignmentID, "student_total": out.StudentTotal})
	return out, nil
}
