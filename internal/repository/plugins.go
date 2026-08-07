package repository

import (
	"context"
	"database/sql"
)

// PluginRow 是 plugins 表行（0005，DDL 冻结；插件全局共享，无 workspace_id）。
type PluginRow struct {
	ID              string
	Name            string
	Version         string
	ManifestJSON    string
	Enabled         bool
	PermissionsJSON string
	InstalledAt     string
	UpdatedAt       string
}

const pluginCols = `id, name, version, manifest_json, enabled, permissions_json, installed_at, updated_at`

// CreatePlugin 写入一个插件行（初始 enabled=0，权限待确认）。
func (r *Repo) CreatePlugin(ctx context.Context, row *PluginRow) error {
	enabled := 0
	if row.Enabled {
		enabled = 1
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO plugins (id, name, version, manifest_json, enabled, permissions_json)
		VALUES (?, ?, ?, ?, ?, ?)`,
		row.ID, row.Name, row.Version, row.ManifestJSON, enabled, row.PermissionsJSON)
	return normalizeErr(err)
}

// GetPluginByID 按 ID 获取插件行；不存在返回 nil,nil。
func (r *Repo) GetPluginByID(ctx context.Context, id string) (*PluginRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+pluginCols+` FROM plugins WHERE id = ?`, id)
	return scanPlugin(row)
}

// GetPluginByName 按名称获取插件行（安装去重）；不存在返回 nil,nil。
func (r *Repo) GetPluginByName(ctx context.Context, name string) (*PluginRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+pluginCols+` FROM plugins WHERE name = ?`, name)
	return scanPlugin(row)
}

// ListPlugins 列出全部插件（全局，按安装时间倒序）。
func (r *Repo) ListPlugins(ctx context.Context) ([]*PluginRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+pluginCols+` FROM plugins ORDER BY installed_at DESC`)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*PluginRow
	for rows.Next() {
		p, err := scanPlugin(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, normalizeErr(rows.Err())
}

// SetPluginEnabled 更新插件启用状态；返回是否命中行（false=插件不存在）。
func (r *Repo) SetPluginEnabled(ctx context.Context, id string, enabled bool) (bool, error) {
	v := 0
	if enabled {
		v = 1
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE plugins SET enabled = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ?`, v, id)
	if err != nil {
		return false, normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SetPluginPermissions 更新已确认的权限列表（用户弹窗同意后落库 permissions_json）。
func (r *Repo) SetPluginPermissions(ctx context.Context, id, permissionsJSON string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE plugins SET permissions_json = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ?`, permissionsJSON, id)
	return normalizeErr(err)
}

// DeletePlugin 删除插件行；返回是否命中行（false=插件不存在）。
func (r *Repo) DeletePlugin(ctx context.Context, id string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM plugins WHERE id = ?`, id)
	if err != nil {
		return false, normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ---- 扫描 ----

func scanPlugin(row interface{ Scan(...any) error }) (*PluginRow, error) {
	var p PluginRow
	var enabled int
	err := row.Scan(&p.ID, &p.Name, &p.Version, &p.ManifestJSON, &enabled,
		&p.PermissionsJSON, &p.InstalledAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, normalizeErr(err)
	}
	p.Enabled = enabled == 1
	return &p, nil
}
