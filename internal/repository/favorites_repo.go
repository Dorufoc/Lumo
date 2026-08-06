package repository

import (
	"context"
	"database/sql"
	"strings"

	"lumo/internal/domain"
)

// FavoriteRow 是收藏行（4.15 / 0005_student.sql favorites 表）。
type FavoriteRow struct {
	ID          string
	WorkspaceID string
	UserID      string
	RefType     string
	RefID       string
	GroupName   string
	Note        string
	CreatedAt   string
	UpdatedAt   string
	Version     int
}

const favoriteCols = `id, workspace_id, user_id, ref_type, ref_id, group_name, note, created_at, updated_at, version`

func scanFavorite(row interface{ Scan(...any) error }) (*FavoriteRow, error) {
	var f FavoriteRow
	if err := row.Scan(&f.ID, &f.WorkspaceID, &f.UserID, &f.RefType, &f.RefID,
		&f.GroupName, &f.Note, &f.CreatedAt, &f.UpdatedAt, &f.Version); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	return &f, nil
}

// CreateFavorite 创建收藏（UNIQUE(user_id, ref_type, ref_id) 冲突由 normalizeErr 映射 CONFLICT）。
func (r *Repo) CreateFavorite(ctx context.Context, f *FavoriteRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO favorites (id, workspace_id, user_id, ref_type, ref_id, group_name, note, version)
		VALUES (?, ?, ?, ?, ?, ?, ?, 1)`,
		f.ID, f.WorkspaceID, f.UserID, f.RefType, f.RefID, f.GroupName, f.Note)
	return normalizeErr(err)
}

// GetFavorite 按唯一键查询收藏；不存在返回 nil,nil。
func (r *Repo) GetFavorite(ctx context.Context, userID, refType, refID string) (*FavoriteRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+favoriteCols+` FROM favorites
		WHERE user_id = ? AND ref_type = ? AND ref_id = ?`, userID, refType, refID)
	return scanFavorite(row)
}

// UpdateFavorite 乐观锁更新分组/备注；版本不匹配返回 CONFLICT。
func (r *Repo) UpdateFavorite(ctx context.Context, userID, refType, refID string, version int, f *FavoriteRow) (*FavoriteRow, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE favorites SET group_name = ?, note = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), version = version + 1
		WHERE user_id = ? AND ref_type = ? AND ref_id = ? AND version = ?`,
		f.GroupName, f.Note, userID, refType, refID, version)
	if err != nil {
		return nil, normalizeErr(err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		cur, err := r.GetFavorite(ctx, userID, refType, refID)
		if err != nil {
			return nil, err
		}
		if cur == nil {
			return nil, domain.NotFound("收藏不存在或已被取消")
		}
		return nil, domain.Conflict("收藏已被修改，请刷新后重试")
	}
	return r.GetFavorite(ctx, userID, refType, refID)
}

// DeleteFavorite 乐观锁取消收藏（版本不匹配返回 CONFLICT，不存在返回 NOT_FOUND）。
func (r *Repo) DeleteFavorite(ctx context.Context, userID, refType, refID string, version int) error {
	res, err := r.db.ExecContext(ctx, `
		DELETE FROM favorites
		WHERE user_id = ? AND ref_type = ? AND ref_id = ? AND version = ?`,
		userID, refType, refID, version)
	if err != nil {
		return normalizeErr(err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		cur, err := r.GetFavorite(ctx, userID, refType, refID)
		if err != nil {
			return err
		}
		if cur == nil {
			return domain.NotFound("收藏不存在或已被取消")
		}
		return domain.Conflict("收藏已被修改，请刷新后重试")
	}
	return nil
}

// ListFavorites 分页列出收藏（newest-first），支持 group_name / ref_type / keyword 过滤。
// cursor 为上一页返回的 created_at|id（与 notifications 一致）；返回多取一行判定 has_more。
func (r *Repo) ListFavorites(ctx context.Context, userID, groupName, refType, keyword, cursor string, limit int) ([]*FavoriteRow, string, bool, error) {
	if limit <= 0 {
		limit = 50
	}
	conds := []string{"user_id = ?"}
	args := []any{userID}
	if groupName != "" {
		conds = append(conds, "group_name = ?")
		args = append(args, groupName)
	}
	if refType != "" {
		conds = append(conds, "ref_type = ?")
		args = append(args, refType)
	}
	if kw := strings.TrimSpace(keyword); kw != "" {
		conds = append(conds, `(note LIKE ? OR group_name LIKE ? OR ref_id LIKE ?)`)
		like := "%" + kw + "%"
		args = append(args, like, like, like)
	}
	if cursor != "" {
		createdAt, id, ok := strings.Cut(cursor, "|")
		if ok && createdAt != "" && id != "" {
			conds = append(conds, `(created_at < ? OR (created_at = ? AND id < ?))`)
			args = append(args, createdAt, createdAt, id)
		}
	}
	query := `SELECT ` + favoriteCols + ` FROM favorites WHERE ` + strings.Join(conds, " AND ") +
		` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", false, normalizeErr(err)
	}
	defer rows.Close()

	out := make([]*FavoriteRow, 0, limit)
	var nextCursor string
	var hasMore bool
	for rows.Next() {
		f, err := scanFavorite(rows)
		if err != nil {
			return nil, "", false, err
		}
		if len(out) == limit {
			hasMore = true
			continue
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, "", false, normalizeErr(err)
	}
	if len(out) > 0 {
		last := out[len(out)-1]
		nextCursor = last.CreatedAt + "|" + last.ID
	}
	return out, nextCursor, hasMore, nil
}

// ReadLaterRow 是稍后读行（4.15 / 0005_student.sql read_later 表）。
type ReadLaterRow struct {
	ID          string
	WorkspaceID string
	UserID      string
	DocumentID  string
	Status      string
	CreatedAt   string
	UpdatedAt   string
}

const readLaterCols = `id, workspace_id, user_id, document_id, status, created_at, updated_at`

func scanReadLater(row interface{ Scan(...any) error }) (*ReadLaterRow, error) {
	var x ReadLaterRow
	if err := row.Scan(&x.ID, &x.WorkspaceID, &x.UserID, &x.DocumentID,
		&x.Status, &x.CreatedAt, &x.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	return &x, nil
}

// CreateReadLater 入队稍后读（重复入队由服务层自然幂等拦截；此处依赖唯一键由调用方保证）。
func (r *Repo) CreateReadLater(ctx context.Context, x *ReadLaterRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO read_later (id, workspace_id, user_id, document_id, status)
		VALUES (?, ?, ?, ?, ?)`,
		x.ID, x.WorkspaceID, x.UserID, x.DocumentID, x.Status)
	return normalizeErr(err)
}

// GetReadLater 按 id 查询稍后读条目；不存在返回 nil,nil。
func (r *Repo) GetReadLater(ctx context.Context, id string) (*ReadLaterRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+readLaterCols+` FROM read_later WHERE id = ?`, id)
	return scanReadLater(row)
}

// GetReadLaterByDocument 查用户是否已将该文档入队（自然幂等）。
func (r *Repo) GetReadLaterByDocument(ctx context.Context, userID, documentID string) (*ReadLaterRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+readLaterCols+` FROM read_later
		WHERE user_id = ? AND document_id = ? ORDER BY created_at DESC LIMIT 1`, userID, documentID)
	return scanReadLater(row)
}

// UpdateReadLaterStatus 更新稍后读状态；条目不存在返回 NOT_FOUND。
func (r *Repo) UpdateReadLaterStatus(ctx context.Context, id, status string) (*ReadLaterRow, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE read_later SET status = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ?`, status, id)
	if err != nil {
		return nil, normalizeErr(err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return nil, domain.NotFound("稍后读条目不存在")
	}
	return r.GetReadLater(ctx, id)
}

// CountReadLater 统计用户稍后读队列数量（上限 500/用户，见设计 4.15）。
func (r *Repo) CountReadLater(ctx context.Context, userID string) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM read_later WHERE user_id = ?`, userID).Scan(&n); err != nil {
		return 0, normalizeErr(err)
	}
	return n, nil
}

// DocumentSummaryRow 是文档摘要行（4.15 / 0005_student.sql document_summaries 表）。
type DocumentSummaryRow struct {
	ID            string
	DocumentID    string
	SummaryJSON   string
	Model         string
	PromptVersion *string
	Status        string
	CreatedAt     string
	UpdatedAt     string
}

const documentSummaryCols = `id, document_id, summary_json, model, prompt_version, status, created_at, updated_at`

func scanDocumentSummary(row interface{ Scan(...any) error }) (*DocumentSummaryRow, error) {
	var s DocumentSummaryRow
	if err := row.Scan(&s.ID, &s.DocumentID, &s.SummaryJSON, &s.Model,
		&s.PromptVersion, &s.Status, &s.CreatedAt, &s.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	return &s, nil
}

// CreateDocumentSummary 创建文档摘要（summary_json 由 JSON 序列化保证 json_valid）。
func (r *Repo) CreateDocumentSummary(ctx context.Context, s *DocumentSummaryRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO document_summaries (id, document_id, summary_json, model, prompt_version, status)
		VALUES (?, ?, ?, ?, ?, ?)`,
		s.ID, s.DocumentID, s.SummaryJSON, s.Model, s.PromptVersion, s.Status)
	return normalizeErr(err)
}

// GetDocumentSummary 按 id 查询文档摘要；不存在返回 nil,nil。
func (r *Repo) GetDocumentSummary(ctx context.Context, id string) (*DocumentSummaryRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+documentSummaryCols+` FROM document_summaries WHERE id = ?`, id)
	return scanDocumentSummary(row)
}
