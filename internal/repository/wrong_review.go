package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"lumo/internal/domain"
)

// WrongAnswerRow 是 wrong_answers 表行。
type WrongAnswerRow struct {
	ID                string
	WorkspaceID       string
	UserID            string
	SubmissionID      string
	QuestionVersionID string
	Answer            json.RawMessage
	Cause             string
	Status            string
	LatestGradingID   *string
	CreatedAt         string
	UpdatedAt         string
	DeletedAt         *string
	Version           int
}

// ReviewCardRow 是 review_cards 表行。
type ReviewCardRow struct {
	ID            string
	WorkspaceID   string
	UserID        string
	WrongAnswerID string
	Repetition    int
	IntervalDays  int
	EaseFactor    float64
	DueAt         string
	Status        string
	CreatedAt     string
	UpdatedAt     string
	Version       int
}

// ReviewEventRow 是 review_events 表行。
type ReviewEventRow struct {
	ID           string
	ReviewCardID string
	Rating       string
	Previous     json.RawMessage
	Current      json.RawMessage
	CreatedAt    string
}

const wrongCols = `id, workspace_id, user_id, submission_id, question_version_id, answer_json, cause, status,
	latest_grading_id, created_at, updated_at, deleted_at, version`

func scanWrong(row interface{ Scan(...any) error }) (*WrongAnswerRow, error) {
	var w WrongAnswerRow
	var answer string
	if err := row.Scan(&w.ID, &w.WorkspaceID, &w.UserID, &w.SubmissionID, &w.QuestionVersionID,
		&answer, &w.Cause, &w.Status, &w.LatestGradingID, &w.CreatedAt, &w.UpdatedAt, &w.DeletedAt, &w.Version); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	w.Answer = json.RawMessage(answer)
	return &w, nil
}

const cardCols = `id, workspace_id, user_id, wrong_answer_id, repetition, interval_days, ease_factor, due_at, status, created_at, updated_at, version`

func scanCard(row interface{ Scan(...any) error }) (*ReviewCardRow, error) {
	var c ReviewCardRow
	if err := row.Scan(&c.ID, &c.WorkspaceID, &c.UserID, &c.WrongAnswerID, &c.Repetition,
		&c.IntervalDays, &c.EaseFactor, &c.DueAt, &c.Status, &c.CreatedAt, &c.UpdatedAt, &c.Version); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	return &c, nil
}

// CreateWrongAnswer 创建错题记录（同一 submission 唯一）。
func (r *Repo) CreateWrongAnswer(ctx context.Context, w *WrongAnswerRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO wrong_answers (id, workspace_id, user_id, submission_id, question_version_id,
			answer_json, cause, status, latest_grading_id, version)
		VALUES (?, ?, ?, ?, ?, ?, 'unknown', 'active', ?, 1)`,
		w.ID, w.WorkspaceID, w.UserID, w.SubmissionID, w.QuestionVersionID,
		string(w.Answer), w.LatestGradingID)
	return normalizeErr(err)
}

// WrongFilter 是错题过滤条件。
type WrongFilter struct {
	Status      string
	Cause       string
	KnowledgeID string
	Cursor      string
	Limit       int
}

// ListWrongAnswers 分页列出错题。
func (r *Repo) ListWrongAnswers(ctx context.Context, wsID, userID string, f WrongFilter) ([]*WrongAnswerRow, string, bool, error) {
	limit := f.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	conds := []string{"w.workspace_id = ?", "w.user_id = ?", "w.deleted_at IS NULL"}
	args := []any{wsID, userID}
	if f.Status != "" {
		conds = append(conds, "w.status = ?")
		args = append(args, f.Status)
	}
	if f.Cause != "" {
		conds = append(conds, "w.cause = ?")
		args = append(args, f.Cause)
	}
	join := ""
	if f.KnowledgeID != "" {
		join = ` JOIN question_knowledge qk ON qk.question_version_id = w.question_version_id`
		conds = append(conds, "qk.knowledge_id = ?")
		args = append(args, f.KnowledgeID)
	}
	if f.Cursor != "" {
		conds = append(conds, "w.id < ?")
		args = append(args, f.Cursor)
	}
	query := `SELECT ` + wrongCols + ` FROM wrong_answers w` + join +
		` WHERE ` + strings.Join(conds, " AND ") +
		` ORDER BY w.id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", false, normalizeErr(err)
	}
	defer rows.Close()
	var out []*WrongAnswerRow
	for rows.Next() {
		w, err := scanWrong(rows)
		if err != nil {
			return nil, "", false, err
		}
		out = append(out, w)
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
		next = out[len(out)-1].ID
	}
	return out, next, hasMore, nil
}

// GetWrongAnswer 获取错题。
func (r *Repo) GetWrongAnswer(ctx context.Context, wsID, id string) (*WrongAnswerRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+wrongCols+` FROM wrong_answers
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`, id, wsID)
	return scanWrong(row)
}

// UpdateWrongCause 乐观锁更新错因。
func (r *Repo) UpdateWrongCause(ctx context.Context, wsID, id string, version int, cause string) (*WrongAnswerRow, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE wrong_answers SET cause = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), version = version + 1
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL AND version = ?`,
		cause, id, wsID, version)
	if err != nil {
		return nil, normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		cur, err := r.GetWrongAnswer(ctx, wsID, id)
		if err != nil {
			return nil, err
		}
		if cur == nil {
			return nil, NotFoundErr("错题", id)
		}
		return nil, domain.Conflict("错题记录已被修改，请刷新后重试")
	}
	return r.GetWrongAnswer(ctx, wsID, id)
}

// CreateReviewCard 创建复习卡。
func (r *Repo) CreateReviewCard(ctx context.Context, c *ReviewCardRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO review_cards (id, workspace_id, user_id, wrong_answer_id, repetition, interval_days,
			ease_factor, due_at, status, version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'active', 1)`,
		c.ID, c.WorkspaceID, c.UserID, c.WrongAnswerID, c.Repetition, c.IntervalDays,
		c.EaseFactor, c.DueAt)
	return normalizeErr(err)
}

// GetReviewCard 获取复习卡。
func (r *Repo) GetReviewCard(ctx context.Context, wsID, id string) (*ReviewCardRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+cardCols+` FROM review_cards
		WHERE id = ? AND workspace_id = ?`, id, wsID)
	return scanCard(row)
}

// ListDueReviewCards 列出到期复习卡（due_at <= dueBefore，按到期时间排序）。
func (r *Repo) ListDueReviewCards(ctx context.Context, wsID, userID, dueBefore string, limit int) ([]*ReviewCardRow, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+cardCols+` FROM review_cards
		WHERE workspace_id = ? AND user_id = ? AND status = 'active' AND due_at <= ?
		ORDER BY due_at, created_at LIMIT ?`, wsID, userID, dueBefore, limit)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*ReviewCardRow
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CountDueReviewCards 统计到期复习卡数量。
func (r *Repo) CountDueReviewCards(ctx context.Context, wsID, userID, dueBefore string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
		SELECT count(*) FROM review_cards
		WHERE workspace_id = ? AND user_id = ? AND status = 'active' AND due_at <= ?`,
		wsID, userID, dueBefore).Scan(&n)
	return n, normalizeErr(err)
}

// UpdateReviewCard 更新复习卡（SM-2 结果）。
func (r *Repo) UpdateReviewCard(ctx context.Context, id string, repetition, intervalDays int, easeFactor float64, dueAt string, version int) (*ReviewCardRow, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE review_cards SET repetition = ?, interval_days = ?, ease_factor = ?, due_at = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), version = version + 1
		WHERE id = ? AND status = 'active' AND version = ?`,
		repetition, intervalDays, easeFactor, dueAt, id, version)
	if err != nil {
		return nil, normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, domain.InvalidState("复习卡不存在、已结束或已被修改")
	}
	// 回读（带 workspace 无关查询）
	var c ReviewCardRow
	if err := r.db.QueryRowContext(ctx, `
		SELECT `+cardCols+` FROM review_cards WHERE id = ?`, id).Scan(
		&c.ID, &c.WorkspaceID, &c.UserID, &c.WrongAnswerID, &c.Repetition, &c.IntervalDays,
		&c.EaseFactor, &c.DueAt, &c.Status, &c.CreatedAt, &c.UpdatedAt, &c.Version); err != nil {
		return nil, normalizeErr(err)
	}
	return &c, nil
}

// CreateReviewEvent 追加复习事件（只追加）。
func (r *Repo) CreateReviewEvent(ctx context.Context, e *ReviewEventRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO review_events (id, review_card_id, rating, previous_json, current_json)
		VALUES (?, ?, ?, ?, ?)`,
		e.ID, e.ReviewCardID, e.Rating, string(e.Previous), string(e.Current))
	return normalizeErr(err)
}

// ListReviewEvents 列出复习事件。
func (r *Repo) ListReviewEvents(ctx context.Context, cardID string, limit int) ([]*ReviewEventRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, review_card_id, rating, previous_json, current_json, created_at
		FROM review_events WHERE review_card_id = ? ORDER BY created_at DESC LIMIT ?`, cardID, limit)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*ReviewEventRow
	for rows.Next() {
		var e ReviewEventRow
		var prev, cur string
		if err := rows.Scan(&e.ID, &e.ReviewCardID, &e.Rating, &prev, &cur, &e.CreatedAt); err != nil {
			return nil, normalizeErr(err)
		}
		e.Previous = json.RawMessage(prev)
		e.Current = json.RawMessage(cur)
		out = append(out, &e)
	}
	return out, rows.Err()
}
