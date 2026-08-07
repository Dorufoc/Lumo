// content_request.go 求题请求仓储（完整设计文档 4.20 P2：content_requests 表）。
package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"lumo/internal/domain"
)

// ContentRequestRow 是 content_requests 表行。
type ContentRequestRow struct {
	ID           string
	WorkspaceID  string
	UserID       string
	KnowledgeIDs string // JSON 数组文本
	Description  string
	Status       string
	CreatedAt    string
	UpdatedAt    string
}

const contentRequestCols = `id, workspace_id, user_id, knowledge_ids, description, status, created_at, updated_at`

func scanContentRequest(row interface{ Scan(...any) error }) (*ContentRequestRow, error) {
	var r ContentRequestRow
	if err := row.Scan(&r.ID, &r.WorkspaceID, &r.UserID, &r.KnowledgeIDs, &r.Description,
		&r.Status, &r.CreatedAt, &r.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	return &r, nil
}

// CreateContentRequest 插入求题请求（status 默认 open）。
func (r *Repo) CreateContentRequest(ctx context.Context, row *ContentRequestRow) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO content_requests (id, workspace_id, user_id, knowledge_ids, description, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.WorkspaceID, row.UserID, row.KnowledgeIDs, row.Description,
		row.Status, now, now)
	return normalizeErr(err)
}

// GetContentRequest 获取单条请求（按 workspace_id 隔离）。
func (r *Repo) GetContentRequest(ctx context.Context, wsID, id string) (*ContentRequestRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+contentRequestCols+` FROM content_requests
		WHERE id = ? AND workspace_id = ?`, id, wsID)
	return scanContentRequest(row)
}

// ListContentRequests 列出请求（user_id 为空 = 全部；按创建时间倒序）。
func (r *Repo) ListContentRequests(ctx context.Context, wsID, userID string) ([]*ContentRequestRow, error) {
	query := `SELECT ` + contentRequestCols + ` FROM content_requests WHERE workspace_id = ?`
	args := []any{wsID}
	if userID != "" {
		query += ` AND user_id = ?`
		args = append(args, userID)
	}
	query += ` ORDER BY created_at DESC`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*ContentRequestRow
	for rows.Next() {
		it, err := scanContentRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// UpdateContentRequestStatus 迁移请求状态（仅允许 pending→目标 之外的 from 守卫：
// 用 WHERE status = ? 原子守卫旧状态，0 行受影响时区分不存在与非法迁移）。
// 返回迁移后的行；非法迁移返回 INVALID_STATE。
func (r *Repo) UpdateContentRequestStatus(ctx context.Context, q queryer, id, from, to string) (*ContentRequestRow, error) {
	res, err := q.ExecContext(ctx, `
		UPDATE content_requests SET status = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ? AND status = ?`, to, id, from)
	if err != nil {
		return nil, normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		cur, err := getContentRequestBy(ctx, q, id)
		if err != nil {
			return nil, err
		}
		if cur == nil {
			return nil, NotFoundErr("求题请求", id)
		}
		return nil, domain.InvalidState("求题请求状态不允许迁移")
	}
	return getContentRequestBy(ctx, q, id)
}

func getContentRequestBy(ctx context.Context, q queryer, id string) (*ContentRequestRow, error) {
	row := q.QueryRowContext(ctx, `
		SELECT `+contentRequestCols+` FROM content_requests WHERE id = ?`, id)
	return scanContentRequest(row)
}

// UnmarshalKnowledgeIDs 解析 knowledge_ids JSON 数组。
func (r *ContentRequestRow) UnmarshalKnowledgeIDs() []string {
	var ids []string
	if r.KnowledgeIDs != "" && json.Unmarshal([]byte(r.KnowledgeIDs), &ids) == nil {
		return ids
	}
	return nil
}
