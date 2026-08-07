// knowledge_graph.go 知识图谱仓储：知识关系、掌握度快照与证据样本聚合。
// 注意：knowledge_relations 无 workspace_id 列，工作区隔离经 JOIN 两个端点 knowledge_nodes 实现；
// mastery_snapshots 无 UNIQUE(user_id, knowledge_id)，"upsert" = 同事务内先删后插。
package repository

import (
	"context"
	"database/sql"
	"strings"
)

// KnowledgeRelationRow 是 knowledge_relations 表行。
type KnowledgeRelationRow struct {
	ID              string
	FromKnowledgeID string
	ToKnowledgeID   string
	RelType         string
	Source          string
	CreatedAt       string
}

// MasterySnapshotRow 是 mastery_snapshots 表行。
type MasterySnapshotRow struct {
	ID           string
	UserID       string
	KnowledgeID  string
	MasteryScore float64
	SampleSize   int
	ComputedAt   string
}

// MasterySnapshotListItem 是快照列表行（附带知识点名称；知识节点被删时名称为空）。
type MasterySnapshotListItem struct {
	MasterySnapshotRow
	KnowledgeName string
}

// MasterySampleRow 是一条掌握度证据样本（判分或复习事件），Value 已归一化到 [0,1]。
type MasterySampleRow struct {
	Type        string // grading | review
	KnowledgeID string
	Value       float64
	Weight      float64
	OccurredAt  string
}

const masterySnapshotCols = `id, user_id, knowledge_id, mastery_score, sample_size, computed_at`

func scanMasterySnapshot(row interface{ Scan(...any) error }) (*MasterySnapshotRow, error) {
	var m MasterySnapshotRow
	if err := row.Scan(&m.ID, &m.UserID, &m.KnowledgeID, &m.MasteryScore, &m.SampleSize, &m.ComputedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	return &m, nil
}

// ListKnowledgeRelationsInWorkspace 列出工作区内两端知识点均存在且未删除的关系。
// knowledge_relations 无 workspace_id 列，工作区隔离经 JOIN knowledge_nodes（两次）。
func (r *Repo) ListKnowledgeRelationsInWorkspace(ctx context.Context, wsID string) ([]*KnowledgeRelationRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT r.id, r.from_knowledge_id, r.to_knowledge_id, r.rel_type, r.source, r.created_at
		FROM knowledge_relations r
		JOIN knowledge_nodes f ON f.id = r.from_knowledge_id AND f.deleted_at IS NULL
		JOIN knowledge_nodes t ON t.id = r.to_knowledge_id AND t.deleted_at IS NULL
		WHERE f.workspace_id = ? AND t.workspace_id = ?
		ORDER BY r.created_at`, wsID, wsID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*KnowledgeRelationRow
	for rows.Next() {
		var r KnowledgeRelationRow
		if err := rows.Scan(&r.ID, &r.FromKnowledgeID, &r.ToKnowledgeID, &r.RelType, &r.Source, &r.CreatedAt); err != nil {
			return nil, normalizeErr(err)
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// GetKnowledgeNodeByID 按 ID 获取未删除知识点（不限定工作区，MasteryExplain 校验用）。
func (r *Repo) GetKnowledgeNodeByID(ctx context.Context, id string) (*KnowledgeNodeRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+knowledgeCols+` FROM knowledge_nodes
		WHERE id = ? AND deleted_at IS NULL`, id)
	return scanKnowledge(row)
}

// GetLatestMasterySnapshot 获取 (user, knowledge) 最新快照；无则返回 nil。
func (r *Repo) GetLatestMasterySnapshot(ctx context.Context, userID, knowledgeID string) (*MasterySnapshotRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+masterySnapshotCols+` FROM mastery_snapshots
		WHERE user_id = ? AND knowledge_id = ?
		ORDER BY computed_at DESC, id DESC LIMIT 1`, userID, knowledgeID)
	return scanMasterySnapshot(row)
}

// UpsertMasterySnapshotTx 在事务内写一条 (user, knowledge) 快照：先删旧快照再插入。
// mastery_snapshots 无 UNIQUE(user_id, knowledge_id)，删除+插入必须在同一事务内保证恰好一条。
func (r *Repo) UpsertMasterySnapshotTx(ctx context.Context, q queryer, m *MasterySnapshotRow) error {
	if _, err := q.ExecContext(ctx, `
		DELETE FROM mastery_snapshots WHERE user_id = ? AND knowledge_id = ?`,
		m.UserID, m.KnowledgeID); err != nil {
		return normalizeErr(err)
	}
	_, err := q.ExecContext(ctx, `
		INSERT INTO mastery_snapshots (id, user_id, knowledge_id, mastery_score, sample_size, computed_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		m.ID, m.UserID, m.KnowledgeID, m.MasteryScore, m.SampleSize, m.ComputedAt)
	return normalizeErr(err)
}

// ListMasterySnapshots 分页列出快照（cursor = "computed_at|id"，按 computed_at DESC, id DESC）。
func (r *Repo) ListMasterySnapshots(ctx context.Context, userID, knowledgeID, cursor string, limit int) ([]*MasterySnapshotListItem, string, bool, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	conds := []string{"ms.user_id = ?"}
	args := []any{userID}
	if knowledgeID != "" {
		conds = append(conds, "ms.knowledge_id = ?")
		args = append(args, knowledgeID)
	}
	if cursor != "" {
		if parts := strings.SplitN(cursor, "|", 2); len(parts) == 2 {
			conds = append(conds, "(ms.computed_at < ? OR (ms.computed_at = ? AND ms.id < ?))")
			args = append(args, parts[0], parts[0], parts[1])
		}
	}
	query := `SELECT ms.id, ms.user_id, ms.knowledge_id, ms.mastery_score, ms.sample_size, ms.computed_at, k.name
		FROM mastery_snapshots ms
		LEFT JOIN knowledge_nodes k ON k.id = ms.knowledge_id
		WHERE ` + strings.Join(conds, " AND ") + `
		ORDER BY ms.computed_at DESC, ms.id DESC LIMIT ?`
	args = append(args, limit+1)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", false, normalizeErr(err)
	}
	defer rows.Close()
	var out []*MasterySnapshotListItem
	for rows.Next() {
		var it MasterySnapshotListItem
		var name sql.NullString
		if err := rows.Scan(&it.ID, &it.UserID, &it.KnowledgeID, &it.MasteryScore, &it.SampleSize,
			&it.ComputedAt, &name); err != nil {
			return nil, "", false, normalizeErr(err)
		}
		it.KnowledgeName = name.String
		out = append(out, &it)
	}
	if err := rows.Err(); err != nil {
		return nil, "", false, normalizeErr(err)
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	next := ""
	if hasMore && len(out) > 0 {
		last := out[len(out)-1]
		next = last.ComputedAt + "|" + last.ID
	}
	return out, next, hasMore, nil
}

// ListGradingMasterySamples 返回判分证据样本：判分 completed，正确(score>=max_score)=1.0、错误=0.0，
// 权重取 question_knowledge.weight（缺省 1）。一条判分按题目所有知识点标签各记一条样本。
func (r *Repo) ListGradingMasterySamples(ctx context.Context, userID string) ([]*MasterySampleRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT 'grading',
		       qk.knowledge_id,
		       CASE WHEN COALESCE(g.score, 0) >= g.max_score THEN 1.0 ELSE 0.0 END,
		       COALESCE(qk.weight, 1),
		       g.created_at
		FROM grading_results g
		JOIN submissions s ON g.submission_id = s.id
		JOIN practice_sessions ps ON s.session_id = ps.id
		JOIN question_knowledge qk ON qk.question_version_id = s.question_version_id
		WHERE ps.user_id = ? AND g.status = 'completed'`, userID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	return scanMasterySamples(rows)
}

// ListReviewMasterySamples 返回复习证据样本：评级 good=1.0 / hard=0.5 / again=0.0，权重 1。
// review_events.user_id 由 0005 ALTER 追加但服务层写路径未填充，故按 review_cards.user_id 过滤。
func (r *Repo) ListReviewMasterySamples(ctx context.Context, userID string) ([]*MasterySampleRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT 'review',
		       qk.knowledge_id,
		       CASE re.rating WHEN 'good' THEN 1.0 WHEN 'hard' THEN 0.5 ELSE 0.0 END,
		       1.0,
		       re.created_at
		FROM review_events re
		JOIN review_cards rc ON rc.id = re.review_card_id
		JOIN wrong_answers w ON w.id = rc.wrong_answer_id
		JOIN question_knowledge qk ON qk.question_version_id = w.question_version_id
		WHERE rc.user_id = ?`, userID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	return scanMasterySamples(rows)
}

// ListRecentMasterySamples 返回某知识点最近 limit 条证据样本（判分+复习，按发生时间倒序）。
// MasteryExplain K3 用：最近 10 次作答明细。
func (r *Repo) ListRecentMasterySamples(ctx context.Context, userID, knowledgeID string, limit int) ([]*MasterySampleRow, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT knowledge_id, value, weight, occurred_at, type FROM (
			SELECT qk.knowledge_id AS knowledge_id,
			       CASE WHEN COALESCE(g.score, 0) >= g.max_score THEN 1.0 ELSE 0.0 END AS value,
			       COALESCE(qk.weight, 1) AS weight,
			       g.created_at AS occurred_at,
			       'grading' AS type
			FROM grading_results g
			JOIN submissions s ON g.submission_id = s.id
			JOIN practice_sessions ps ON s.session_id = ps.id
			JOIN question_knowledge qk ON qk.question_version_id = s.question_version_id
			WHERE ps.user_id = ? AND qk.knowledge_id = ? AND g.status = 'completed'
			UNION ALL
			SELECT qk.knowledge_id,
			       CASE re.rating WHEN 'good' THEN 1.0 WHEN 'hard' THEN 0.5 ELSE 0.0 END,
			       1.0,
			       re.created_at,
			       'review'
			FROM review_events re
			JOIN review_cards rc ON rc.id = re.review_card_id
			JOIN wrong_answers w ON w.id = rc.wrong_answer_id
			JOIN question_knowledge qk ON qk.question_version_id = w.question_version_id
			WHERE rc.user_id = ? AND qk.knowledge_id = ?
		)
		ORDER BY occurred_at DESC
		LIMIT ?`, userID, knowledgeID, userID, knowledgeID, limit)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*MasterySampleRow
	for rows.Next() {
		var s MasterySampleRow
		if err := rows.Scan(&s.KnowledgeID, &s.Value, &s.Weight, &s.OccurredAt, &s.Type); err != nil {
			return nil, normalizeErr(err)
		}
		out = append(out, &s)
	}
	return out, rows.Err()
}

func scanMasterySamples(rows *sql.Rows) ([]*MasterySampleRow, error) {
	var out []*MasterySampleRow
	for rows.Next() {
		var s MasterySampleRow
		if err := rows.Scan(&s.Type, &s.KnowledgeID, &s.Value, &s.Weight, &s.OccurredAt); err != nil {
			return nil, normalizeErr(err)
		}
		out = append(out, &s)
	}
	return out, rows.Err()
}
