package repository

import (
	"context"
	"database/sql"
	"strings"

	"lumo/internal/domain"
)

// FlashcardRow 是闪卡行（6.2.1 flashcards）。
type FlashcardRow struct {
	ID             string
	WorkspaceID    string
	UserID         string
	Source         string
	SourceRef      *string
	Front          string
	Back           string
	CardType       string
	State          string
	Repetition     int
	IntervalDays   int
	EaseFactor     float64
	DueAt          string
	CreatedAt      string
	UpdatedAt      string
	DeletedAt      *string
	Version        int
}

// FlashcardReviewRow 是复习行为行（6.2.1 flashcard_reviews，只追加）。
type FlashcardReviewRow struct {
	ID         string
	FlashcardID string
	Rating     string
	ReviewedAt string
	NextDueAt  string
}

const flashcardCols = `id, workspace_id, user_id, source, source_ref, front, back, card_type, ` +
	`state, repetition, interval_days, ease_factor, due_at, created_at, updated_at, deleted_at, version`

func scanFlashcard(row interface{ Scan(...any) error }) (*FlashcardRow, error) {
	var f FlashcardRow
	if err := row.Scan(&f.ID, &f.WorkspaceID, &f.UserID, &f.Source, &f.SourceRef, &f.Front, &f.Back,
		&f.CardType, &f.State, &f.Repetition, &f.IntervalDays, &f.EaseFactor, &f.DueAt,
		&f.CreatedAt, &f.UpdatedAt, &f.DeletedAt, &f.Version); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	return &f, nil
}

// CreateFlashcard 创建闪卡（flashcards_fts 由触发器自动同步）。
func (r *Repo) CreateFlashcard(ctx context.Context, f *FlashcardRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO flashcards (id, workspace_id, user_id, source, source_ref, front, back, card_type,
			state, repetition, interval_days, ease_factor, due_at, version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
		f.ID, f.WorkspaceID, f.UserID, f.Source, f.SourceRef, f.Front, f.Back, f.CardType,
		f.State, f.Repetition, f.IntervalDays, f.EaseFactor, f.DueAt)
	return normalizeErr(err)
}

// GetFlashcard 返回原始行（含软删除行）；不存在时返回 nil,nil。服务层负责状态校验。
func (r *Repo) GetFlashcard(ctx context.Context, wsID, id string) (*FlashcardRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+flashcardCols+` FROM flashcards
		WHERE id = ? AND workspace_id = ?`, id, wsID)
	return scanFlashcard(row)
}

// ListDueFlashcards 列出到期闪卡（learning/review 且 due_at <= dueBefore，按到期时间排序）。
func (r *Repo) ListDueFlashcards(ctx context.Context, wsID, userID, dueBefore string, limit int) ([]*FlashcardRow, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+flashcardCols+` FROM flashcards
		WHERE workspace_id = ? AND user_id = ? AND deleted_at IS NULL
		  AND state IN ('learning', 'review') AND due_at <= ?
		ORDER BY due_at, id LIMIT ?`, wsID, userID, dueBefore, limit)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*FlashcardRow
	for rows.Next() {
		f, err := scanFlashcard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, normalizeErr(rows.Err())
}

// CountDueFlashcards 统计到期闪卡数（用于 flashcard:due 事件载荷）。
func (r *Repo) CountDueFlashcards(ctx context.Context, wsID, userID, dueBefore string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM flashcards
		WHERE workspace_id = ? AND user_id = ? AND deleted_at IS NULL
		  AND state IN ('learning', 'review') AND due_at <= ?`,
		wsID, userID, dueBefore).Scan(&n)
	if err != nil {
		return 0, normalizeErr(err)
	}
	return n, nil
}

// UpdateFlashcardSM2 乐观锁更新调度字段并递增版本。
func (r *Repo) UpdateFlashcardSM2(ctx context.Context, id string, repetition, intervalDays int,
	easeFactor float64, dueAt, state string, version int) (*FlashcardRow, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE flashcards SET repetition = ?, interval_days = ?, ease_factor = ?, due_at = ?,
			state = ?, updated_at = ?, version = version + 1
		WHERE id = ? AND version = ?`,
		repetition, intervalDays, easeFactor, dueAt, state, domain.NowUTC(), id, version)
	if err != nil {
		return nil, normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, domain.Conflict("闪卡已被其他会话更新，请刷新后重试")
	}
	return r.getFlashcardByID(ctx, id)
}

// getFlashcardByID 按 id 返回最新行（UpdateFlashcardSM2 配套读取）。
func (r *Repo) getFlashcardByID(ctx context.Context, id string) (*FlashcardRow, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+flashcardCols+` FROM flashcards WHERE id = ?`, id)
	return scanFlashcard(row)
}

// CreateFlashcardReview 追加一条复习行为（只追加）。
func (r *Repo) CreateFlashcardReview(ctx context.Context, v *FlashcardReviewRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO flashcard_reviews (id, flashcard_id, rating, reviewed_at, next_due_at)
		VALUES (?, ?, ?, ?, ?)`,
		v.ID, v.FlashcardID, v.Rating, v.ReviewedAt, v.NextDueAt)
	return normalizeErr(err)
}

// LastFlashcardRatings 返回最近的 n 条评级（旧→新），用于连续 good 判定。
func (r *Repo) LastFlashcardRatings(ctx context.Context, flashcardID string, n int) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT rating FROM flashcard_reviews
		WHERE flashcard_id = ? ORDER BY reviewed_at DESC LIMIT ?`, flashcardID, n)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var rating string
		if err := rows.Scan(&rating); err != nil {
			return nil, normalizeErr(err)
		}
		out = append(out, rating)
	}
	if err := rows.Err(); err != nil {
		return nil, normalizeErr(err)
	}
	// 逆序还原为旧→新
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// BatchFlashcardAction 批量操作：archive（归档）/ delete（软删除）/ reset（重置学习）。
// 返回实际影响的行数；不存在的 id 被忽略。
func (r *Repo) BatchFlashcardAction(ctx context.Context, wsID, action string, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, wsID)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	where := ` WHERE workspace_id = ? AND id IN (` + strings.Join(placeholders, ",") + `) AND deleted_at IS NULL`
	var query string
	switch action {
	case "archive":
		query = `UPDATE flashcards SET state = 'archived', updated_at = ?, version = version + 1` + where
		args = append([]any{domain.NowUTC()}, args...)
	case "delete":
		query = `UPDATE flashcards SET deleted_at = ?, updated_at = ?, version = version + 1` + where
		args = append([]any{domain.NowUTC(), domain.NowUTC()}, args...)
	case "reset":
		query = `UPDATE flashcards SET state = 'learning', repetition = 0, interval_days = 0,
			ease_factor = 2.5, due_at = ?, updated_at = ?, version = version + 1` + where
		args = append([]any{domain.NowUTC(), domain.NowUTC()}, args...)
	default:
		return 0, domain.InvalidArg("action 仅允许 archive/delete/reset")
	}
	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// GetNoteForFlashcard 读取笔记正文（供 FlashcardGenerate note 来源使用）。
func (r *Repo) GetNoteForFlashcard(ctx context.Context, wsID, noteID string) (title, body string, found bool, err error) {
	err = r.db.QueryRowContext(ctx, `
		SELECT title, body_md FROM notes
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`, noteID, wsID).
		Scan(&title, &body)
	if err == sql.ErrNoRows {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, normalizeErr(err)
	}
	return title, body, true, nil
}

// GetFlashcardBySource 按来源快照查已生成闪卡（FlashcardGenerate 去重）。
func (r *Repo) GetFlashcardBySource(ctx context.Context, wsID, source, sourceRef string) (*FlashcardRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+flashcardCols+` FROM flashcards
		WHERE workspace_id = ? AND source = ? AND source_ref = ? AND deleted_at IS NULL`,
		wsID, source, sourceRef)
	return scanFlashcard(row)
}

// ListFlashcards 列出工作区内全部未删除闪卡（按创建时间排序，供 Anki 导出）。
func (r *Repo) ListFlashcards(ctx context.Context, wsID string) ([]*FlashcardRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+flashcardCols+` FROM flashcards
		WHERE workspace_id = ? AND deleted_at IS NULL ORDER BY created_at, id`, wsID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*FlashcardRow
	for rows.Next() {
		f, err := scanFlashcard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, normalizeErr(rows.Err())
}

// CreateFlashcardsBatch 在一个事务内批量插入闪卡（CSV 导入原子性）。
func (r *Repo) CreateFlashcardsBatch(ctx context.Context, cards []*FlashcardRow) error {
	if len(cards) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return normalizeErr(err)
	}
	defer tx.Rollback()
	for _, f := range cards {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO flashcards (id, workspace_id, user_id, source, source_ref, front, back, card_type,
				state, repetition, interval_days, ease_factor, due_at, version)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
			f.ID, f.WorkspaceID, f.UserID, f.Source, f.SourceRef, f.Front, f.Back, f.CardType,
			f.State, f.Repetition, f.IntervalDays, f.EaseFactor, f.DueAt); err != nil {
			return normalizeErr(err)
		}
	}
	return normalizeErr(tx.Commit())
}
