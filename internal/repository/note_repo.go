package repository

import (
	"context"
	"database/sql"
	"strings"

	"lumo/internal/domain"
)

// NoteRow 是笔记行（6.2.1 notes）。
type NoteRow struct {
	ID           string
	WorkspaceID  string
	UserID       string
	Kind         string
	Title        string
	BodyMD       string
	SourceRef    *string
	KnowledgeIDs string // JSON 数组文本（json_valid CHECK）
	Tags         string // JSON 数组文本（json_valid CHECK）
	CreatedAt    string
	UpdatedAt    string
	DeletedAt    *string
	Version      int
}

// AnnotationRow 是标注行（6.2.1 note_annotations）。
type AnnotationRow struct {
	ID             string
	NoteID         string
	DocumentID     *string
	AnchorHash     string
	OffsetStart    int
	OffsetEnd      int
	HighlightColor string
	CreatedAt      string
}

const noteCols = `id, workspace_id, user_id, kind, title, body_md, source_ref, knowledge_ids, tags, ` +
	`created_at, updated_at, deleted_at, version`

func scanNote(row interface{ Scan(...any) error }) (*NoteRow, error) {
	var n NoteRow
	if err := row.Scan(&n.ID, &n.WorkspaceID, &n.UserID, &n.Kind, &n.Title, &n.BodyMD,
		&n.SourceRef, &n.KnowledgeIDs, &n.Tags, &n.CreatedAt, &n.UpdatedAt, &n.DeletedAt, &n.Version); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	return &n, nil
}

// CreateNote 创建笔记（notes_fts 由 0005 触发器自动同步，无需手动索引）。
func (r *Repo) CreateNote(ctx context.Context, n *NoteRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO notes (id, workspace_id, user_id, kind, title, body_md, source_ref, knowledge_ids, tags, version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
		n.ID, n.WorkspaceID, n.UserID, n.Kind, n.Title, n.BodyMD, n.SourceRef, n.KnowledgeIDs, n.Tags)
	return normalizeErr(err)
}

// GetNote 返回笔记原始行（含软删除行）；不存在时返回 nil,nil。服务层负责状态校验。
func (r *Repo) GetNote(ctx context.Context, wsID, id string) (*NoteRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+noteCols+` FROM notes
		WHERE id = ? AND workspace_id = ?`, id, wsID)
	return scanNote(row)
}

// UpdateNote 乐观锁更新可编辑字段；版本不匹配返回 CONFLICT。
func (r *Repo) UpdateNote(ctx context.Context, wsID, id string, version int, n *NoteRow) (*NoteRow, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE notes SET kind = ?, title = ?, body_md = ?, source_ref = ?, knowledge_ids = ?, tags = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), version = version + 1
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL AND version = ?`,
		n.Kind, n.Title, n.BodyMD, n.SourceRef, n.KnowledgeIDs, n.Tags, id, wsID, version)
	if err != nil {
		return nil, normalizeErr(err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		cur, err := r.GetNote(ctx, wsID, id)
		if err != nil {
			return nil, err
		}
		if cur == nil || cur.DeletedAt != nil {
			return nil, NotFoundErr("笔记", id)
		}
		return nil, domain.Conflict("笔记已被修改，请刷新后重试")
	}
	return r.GetNote(ctx, wsID, id)
}

// SoftDeleteNote 乐观锁软删除笔记；版本不匹配返回 CONFLICT，返回 deleted_at。
func (r *Repo) SoftDeleteNote(ctx context.Context, wsID, id string, version int) (string, error) {
	deletedAt := domain.NowUTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE notes SET deleted_at = ?, updated_at = ?, version = version + 1
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL AND version = ?`,
		deletedAt, deletedAt, id, wsID, version)
	if err != nil {
		return "", normalizeErr(err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		cur, err := r.GetNote(ctx, wsID, id)
		if err != nil {
			return "", err
		}
		if cur == nil || cur.DeletedAt != nil {
			return "", NotFoundErr("笔记", id)
		}
		return "", domain.Conflict("笔记已被修改，请刷新后重试")
	}
	return deletedAt, nil
}

// ListNotes 分页列出未删除笔记，支持 kind / knowledge_id / tag 过滤（JSON 数组用 json_each 命中）。
func (r *Repo) ListNotes(ctx context.Context, wsID, kind, knowledgeID, tag, cursor string, limit int) ([]*NoteRow, string, bool, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	conds := []string{"workspace_id = ?", "deleted_at IS NULL"}
	args := []any{wsID}
	if kind != "" {
		conds = append(conds, "kind = ?")
		args = append(args, kind)
	}
	if knowledgeID != "" {
		conds = append(conds, "EXISTS (SELECT 1 FROM json_each(knowledge_ids) WHERE json_each.value = ?)")
		args = append(args, knowledgeID)
	}
	if tag != "" {
		conds = append(conds, "EXISTS (SELECT 1 FROM json_each(tags) WHERE json_each.value = ?)")
		args = append(args, tag)
	}
	if cursor != "" {
		conds = append(conds, "id < ?")
		args = append(args, cursor)
	}
	query := `SELECT ` + noteCols + ` FROM notes WHERE ` + strings.Join(conds, " AND ") +
		` ORDER BY id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", false, normalizeErr(err)
	}
	defer rows.Close()
	var out []*NoteRow
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, "", false, err
		}
		out = append(out, n)
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

// GetNotesByIDs 按业务主键批量获取笔记（FTS 命中回查，保持调用方传入顺序）。
func (r *Repo) GetNotesByIDs(ctx context.Context, wsID string, ids []string) ([]*NoteRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	args = append(args, wsID)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+noteCols+` FROM notes
		WHERE workspace_id = ? AND deleted_at IS NULL AND id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	byID := make(map[string]*NoteRow, len(ids))
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, err
		}
		if n != nil {
			byID[n.ID] = n
		}
	}
	if err := rows.Err(); err != nil {
		return nil, normalizeErr(err)
	}
	out := make([]*NoteRow, 0, len(ids))
	for _, id := range ids {
		if n, ok := byID[id]; ok {
			out = append(out, n)
		}
	}
	return out, nil
}

// CreateAnnotation 创建标注。
func (r *Repo) CreateAnnotation(ctx context.Context, a *AnnotationRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO note_annotations (id, note_id, document_id, anchor_hash, offset_start, offset_end, highlight_color)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.NoteID, a.DocumentID, a.AnchorHash, a.OffsetStart, a.OffsetEnd, a.HighlightColor)
	return normalizeErr(err)
}
