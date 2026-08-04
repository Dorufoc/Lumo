package repository

import (
	"context"
	"database/sql"
	"encoding/json"

	"lumo/internal/domain"
)

// SettingsRow 是 settings 表的行映射。
type SettingsRow struct {
	WorkspaceID string
	Settings    json.RawMessage
	Version     int
}

// GetSettings 获取工作区设置；不存在时返回空设置（version=0）。
func (r *Repo) GetSettings(ctx context.Context, workspaceID string) (*SettingsRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT workspace_id, settings_json, version FROM settings WHERE workspace_id = ?`, workspaceID)
	var s SettingsRow
	var raw string
	if err := row.Scan(&s.WorkspaceID, &raw, &s.Version); err != nil {
		if err == sql.ErrNoRows {
			return &SettingsRow{WorkspaceID: workspaceID, Settings: json.RawMessage("{}"), Version: 0}, nil
		}
		return nil, normalizeErr(err)
	}
	s.Settings = json.RawMessage(raw)
	return &s, nil
}

// UpdateSettings 乐观锁更新设置；不存在时插入。
func (r *Repo) UpdateSettings(ctx context.Context, workspaceID string, version int, settings json.RawMessage) (*SettingsRow, error) {
	if version == 0 {
		_, err := r.db.ExecContext(ctx, `
			INSERT INTO settings (workspace_id, settings_json, version) VALUES (?, ?, 1)
			ON CONFLICT(workspace_id) DO UPDATE SET
				settings_json = excluded.settings_json,
				updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
				version = settings.version + 1
			WHERE settings.version = ?`,
			workspaceID, string(settings), version)
		if err != nil {
			return nil, normalizeErr(err)
		}
	} else {
		res, err := r.db.ExecContext(ctx, `
			UPDATE settings SET settings_json = ?,
				updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), version = version + 1
			WHERE workspace_id = ? AND version = ?`,
			string(settings), workspaceID, version)
		if err != nil {
			return nil, normalizeErr(err)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			cur, err := r.GetSettings(ctx, workspaceID)
			if err != nil {
				return nil, err
			}
			if cur.Version == 0 {
				return nil, domain.InvalidArg("设置不存在，请使用 version=0 创建")
			}
			return nil, domain.Conflict("设置已被修改，请刷新后重试")
		}
	}
	return r.GetSettings(ctx, workspaceID)
}
