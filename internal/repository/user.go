package repository

import (
	"context"
	"database/sql"
	"encoding/json"

	"lumo/internal/domain"
)

// UserRow 是 users 表的行映射。
type UserRow struct {
	ID            string
	WorkspaceID   string
	DisplayName   string
	Role          string
	Preferences   json.RawMessage
	CreatedAt     string
	UpdatedAt     string
	DeletedAt     *string
	Version       int
}

const userCols = `id, workspace_id, display_name, role, preferences_json, created_at, updated_at, deleted_at, version`

func scanUser(row interface{ Scan(...any) error }) (*UserRow, error) {
	var u UserRow
	var prefs string
	if err := row.Scan(&u.ID, &u.WorkspaceID, &u.DisplayName, &u.Role, &prefs,
		&u.CreatedAt, &u.UpdatedAt, &u.DeletedAt, &u.Version); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	u.Preferences = json.RawMessage(prefs)
	return &u, nil
}

// CreateUser 创建用户。
func (r *Repo) CreateUser(ctx context.Context, u *UserRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO users (id, workspace_id, display_name, role, preferences_json, version)
		VALUES (?, ?, ?, ?, ?, 1)`,
		u.ID, u.WorkspaceID, u.DisplayName, u.Role, string(u.Preferences))
	return normalizeErr(err)
}

// GetUser 按 ID 获取用户（必须属于 workspace）。
func (r *Repo) GetUser(ctx context.Context, workspaceID, id string) (*UserRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+userCols+` FROM users
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`, id, workspaceID)
	return scanUser(row)
}

// ListUsers 列出工作区全部用户。
func (r *Repo) ListUsers(ctx context.Context, workspaceID string) ([]*UserRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+userCols+` FROM users
		WHERE workspace_id = ? AND deleted_at IS NULL ORDER BY created_at`, workspaceID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*UserRow
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpdateUserProfile 乐观锁更新显示名与偏好，返回更新后的行。
func (r *Repo) UpdateUserProfile(ctx context.Context, workspaceID, id string, version int, displayName string, preferences json.RawMessage) (*UserRow, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE users SET display_name = ?, preferences_json = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), version = version + 1
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL AND version = ?`,
		displayName, string(preferences), id, workspaceID, version)
	if err != nil {
		return nil, normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// 区分不存在与版本冲突
		u, err := r.GetUser(ctx, workspaceID, id)
		if err != nil {
			return nil, err
		}
		if u == nil {
			return nil, NotFoundErr("用户", id)
		}
		return nil, domain.Conflict("用户资料已被修改，请刷新后重试")
	}
	return r.GetUser(ctx, workspaceID, id)
}
