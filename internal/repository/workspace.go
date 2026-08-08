package repository

import (
	"context"
	"database/sql"

	"lumo/internal/domain"
)

// WorkspaceRow 是 workspaces 表的行映射（0008 起含 org 元数据列）。
type WorkspaceRow struct {
	ID             string
	Name           string
	OwnerType      string
	OrgName        *string
	OrgAdminUserID *string
	CreatedAt      string
	UpdatedAt      string
	DeletedAt      *string
	Version        int
}

const workspaceCols = `id, name, owner_type, org_name, org_admin_user_id, created_at, updated_at, deleted_at, version`

func scanWorkspace(row interface{ Scan(...any) error }) (*WorkspaceRow, error) {
	var w WorkspaceRow
	if err := row.Scan(&w.ID, &w.Name, &w.OwnerType, &w.OrgName, &w.OrgAdminUserID,
		&w.CreatedAt, &w.UpdatedAt, &w.DeletedAt, &w.Version); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	return &w, nil
}

// CreateWorkspace 创建工作区。
func (r *Repo) CreateWorkspace(ctx context.Context, w *WorkspaceRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO workspaces (id, name, owner_type, version)
		VALUES (?, ?, ?, 1)`, w.ID, w.Name, w.OwnerType)
	return normalizeErr(err)
}

// UpdateWorkspaceOrg 更新工作区组织元数据（org_name/org_admin_user_id，空串清空）。
func (r *Repo) UpdateWorkspaceOrg(ctx context.Context, id string, orgName, orgAdminUserID *string) (*WorkspaceRow, error) {
	_, err := r.db.ExecContext(ctx, `
		UPDATE workspaces SET org_name = ?, org_admin_user_id = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), version = version + 1
		WHERE id = ? AND deleted_at IS NULL`, orgName, orgAdminUserID, id)
	if err != nil {
		return nil, normalizeErr(err)
	}
	return r.GetWorkspace(ctx, id)
}

// GetWorkspace 按 ID 获取未删除工作区。
func (r *Repo) GetWorkspace(ctx context.Context, id string) (*WorkspaceRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+workspaceCols+` FROM workspaces WHERE id = ? AND deleted_at IS NULL`, id)
	return scanWorkspace(row)
}

// ListWorkspaces 列出全部未删除工作区。
func (r *Repo) ListWorkspaces(ctx context.Context) ([]*WorkspaceRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+workspaceCols+` FROM workspaces WHERE deleted_at IS NULL ORDER BY created_at`)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*WorkspaceRow
	for rows.Next() {
		w, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// SoftDeleteWorkspace 软删除工作区并推进版本。
func (r *Repo) SoftDeleteWorkspace(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE workspaces SET deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), version = version + 1
		WHERE id = ? AND deleted_at IS NULL`, id)
	if err != nil {
		return normalizeErr(err)
	}
	// 校验确实删除了一行
	var n int
	if err := r.db.QueryRowContext(ctx,
		`SELECT changes()`).Scan(&n); err != nil {
		return normalizeErr(err)
	}
	return nil
}

// CountWorkspaces 统计工作区数量。
func (r *Repo) CountWorkspaces(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT count(*) FROM workspaces WHERE deleted_at IS NULL`).Scan(&n)
	return n, normalizeErr(err)
}

// AssertWorkspace 校验工作区存在（归属校验）。
func (r *Repo) AssertWorkspace(ctx context.Context, id string) error {
	w, err := r.GetWorkspace(ctx, id)
	if err != nil {
		return err
	}
	if w == nil {
		return domain.NotFound("工作区不存在或已被删除")
	}
	return nil
}
