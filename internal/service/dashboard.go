package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"lumo/internal/domain"
)

// Dashboard 是首页统计聚合。
type Dashboard struct {
	TodayTasks      TaskSummary       `json:"today_tasks"`
	DueReviews      int               `json:"due_reviews"`
	StreakDays      int               `json:"streak_days"`
	RecentAccuracy  AccuracySummary   `json:"recent_accuracy"`
	WeakKnowledge   []WeakKnowledge   `json:"weak_knowledge"`
	AIAdvice        string            `json:"ai_advice"`
	HasEmptyLibrary bool              `json:"has_empty_library"`
}

// TaskSummary 是今日任务统计。
type TaskSummary struct {
	Total     int `json:"total"`
	Completed int `json:"completed"`
	Pending   int `json:"pending"`
}

// AccuracySummary 是近期正确率。
type AccuracySummary struct {
	Correct int     `json:"correct"`
	Total   int     `json:"total"`
	Rate    float64 `json:"rate"`
}

// WeakKnowledge 是薄弱知识点。
type WeakKnowledge struct {
	KnowledgeID string `json:"knowledge_id"`
	Name        string `json:"name"`
	WrongCount  int    `json:"wrong_count"`
}

// DashboardService 实现统计聚合。
type DashboardService struct{ s *Services }

// DashboardGetReq 获取统计请求。
type DashboardGetReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
}

// DashboardGet 聚合首页数据（今日任务/到期复习/连续天数/正确率/薄弱知识点/AI 建议）。
func (d *DashboardService) DashboardGet(ctx context.Context, req DashboardGetReq) (*Dashboard, error) {
	if err := d.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	db := d.s.Repo.DB()
	out := &Dashboard{}
	now := time.Now().UTC()
	dayStart := now.Truncate(24 * time.Hour)
	dayEnd := dayStart.AddDate(0, 0, 1)

	// 今日任务
	var total, completed int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*), COALESCE(sum(CASE WHEN status = 'completed' THEN 1 ELSE 0 END), 0)
		FROM plan_tasks
		WHERE workspace_id = ? AND user_id = ? AND deleted_at IS NULL
		  AND due_at >= ? AND due_at < ?`,
		req.WorkspaceID, req.UserID, dayStart.Format(time.RFC3339), dayEnd.Format(time.RFC3339),
	).Scan(&total, &completed); err != nil {
		return nil, dbErr(err)
	}
	out.TodayTasks = TaskSummary{Total: total, Completed: completed, Pending: total - completed}

	// 到期复习
	var due int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM review_cards
		WHERE workspace_id = ? AND user_id = ? AND status = 'active' AND due_at <= ?`,
		req.WorkspaceID, req.UserID, now.Format(time.RFC3339)).Scan(&due); err != nil {
		return nil, dbErr(err)
	}
	out.DueReviews = due

	// 连续学习天数（提交答案或完成复习的天数）
	streak, err := d.streakDays(ctx, req.WorkspaceID, req.UserID, now)
	if err != nil {
		return nil, err
	}
	out.StreakDays = streak

	// 近期正确率（最近 7 天客观题）
	var correct, graded int
	if err := db.QueryRowContext(ctx, `
		SELECT COALESCE(sum(CASE WHEN g.score >= g.max_score THEN 1 ELSE 0 END), 0),
		       COALESCE(count(*), 0)
		FROM grading_results g
		JOIN submissions s ON g.submission_id = s.id
		JOIN practice_sessions p ON s.session_id = p.id
		WHERE p.workspace_id = ? AND p.user_id = ? AND g.status = 'completed'
		  AND g.method = 'rule'
		  AND s.submitted_at >= ?`,
		req.WorkspaceID, req.UserID, now.AddDate(0, 0, -6).Format(time.RFC3339),
	).Scan(&correct, &graded); err != nil {
		return nil, dbErr(err)
	}
	rate := 0.0
	if graded > 0 {
		rate = float64(correct) / float64(graded)
	}
	out.RecentAccuracy = AccuracySummary{Correct: correct, Total: graded, Rate: rate}

	// 薄弱知识点
	rows, err := db.QueryContext(ctx, `
		SELECT k.id, k.name, count(*) AS cnt
		FROM wrong_answers w
		JOIN question_knowledge qk ON qk.question_version_id = w.question_version_id
		JOIN knowledge_nodes k ON k.id = qk.knowledge_id
		WHERE w.workspace_id = ? AND w.user_id = ? AND w.deleted_at IS NULL AND w.status = 'active'
		GROUP BY k.id, k.name
		ORDER BY cnt DESC
		LIMIT 5`, req.WorkspaceID, req.UserID)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var wk WeakKnowledge
		if err := rows.Scan(&wk.KnowledgeID, &wk.Name, &wk.WrongCount); err != nil {
			return nil, dbErr(err)
		}
		out.WeakKnowledge = append(out.WeakKnowledge, wk)
	}
	if err := rows.Err(); err != nil {
		return nil, dbErr(err)
	}

	// 题库为空提示
	var qCount int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM questions
		WHERE workspace_id = ? AND deleted_at IS NULL AND status = 'published'`,
		req.WorkspaceID).Scan(&qCount); err != nil {
		return nil, dbErr(err)
	}
	out.HasEmptyLibrary = qCount == 0

	out.AIAdvice = d.buildAdvice(out, qCount)
	return out, nil
}

// streakDays 计算连续学习天数：从最近学习日向前连续计数。
func (d *DashboardService) streakDays(ctx context.Context, wsID, userID string, now time.Time) (int, error) {
	db := d.s.Repo.DB()
	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT date(s.submitted_at)
		FROM submissions s JOIN practice_sessions p ON s.session_id = p.id
		WHERE p.workspace_id = ? AND p.user_id = ? AND s.status = 'submitted'
		UNION
		SELECT DISTINCT date(e.created_at)
		FROM review_events e JOIN review_cards c ON e.review_card_id = c.id
		WHERE c.workspace_id = ? AND c.user_id = ?`, wsID, userID, wsID, userID)
	if err != nil {
		return 0, dbErr(err)
	}
	defer rows.Close()
	days := map[string]bool{}
	for rows.Next() {
		var day string
		if err := rows.Scan(&day); err != nil {
			return 0, dbErr(err)
		}
		days[day] = true
	}
	if err := rows.Err(); err != nil {
		return 0, dbErr(err)
	}
	if len(days) == 0 {
		return 0, nil
	}
	// 从最近学习日向前连续计数（今天未学习不中断历史）
	cur := now.Truncate(24 * time.Hour)
	for !days[cur.Format("2006-01-02")] {
		cur = cur.AddDate(0, 0, -1)
	}
	streak := 0
	for days[cur.Format("2006-01-02")] {
		streak++
		cur = cur.AddDate(0, 0, -1)
	}
	return streak, nil
}

// buildAdvice 基于规则生成一条建议（AI 未配置时的确定性降级）。
func (d *DashboardService) buildAdvice(dash *Dashboard, questionCount int) string {
	if dash.HasEmptyLibrary {
		return "题库还是空的：先去「题库与资料」导入一份题库，或创建一道题目，就可以开始练习了。"
	}
	var parts []string
	if dash.DueReviews > 0 {
		parts = append(parts, fmt.Sprintf("有 %d 张复习卡到期，建议先完成错题复习（间隔记忆效果最佳）。", dash.DueReviews))
	}
	if dash.TodayTasks.Pending > 0 {
		parts = append(parts, fmt.Sprintf("今日还有 %d 项计划任务未完成。", dash.TodayTasks.Pending))
	}
	if len(dash.WeakKnowledge) > 0 {
		names := make([]string, 0, 3)
		for _, wk := range dash.WeakKnowledge {
			names = append(names, wk.Name)
		}
		parts = append(parts, "近期薄弱知识点："+strings.Join(names, "、")+"，建议针对练习。")
	}
	if dash.RecentAccuracy.Total >= 5 && dash.RecentAccuracy.Rate < 0.6 {
		parts = append(parts, fmt.Sprintf("近期正确率 %.0f%%，可适当降低难度巩固基础。", dash.RecentAccuracy.Rate*100))
	}
	if len(parts) == 0 {
		parts = append(parts, "状态很好：保持每日练习节奏，可在设置中配置 AI 获得个性化建议。")
	}
	return strings.Join(parts, " ")
}

// 编译期断言：确保 DashboardService 类型正确。
var _ = domain.CodeInvalidArgument
