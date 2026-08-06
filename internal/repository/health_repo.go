package repository

import (
	"context"
	"database/sql"
)

// HealthSettingsRow 是 health_settings 表行（4.17 / 0005_student.sql）。
// 复合主键 (workspace_id, user_id)；布尔列以 0/1 存储。
type HealthSettingsRow struct {
	WorkspaceID      string
	UserID           string
	SedentaryEnabled int
	EyeEnabled       int
	NightMode        string
	BlueLightFilter  int
	StatsEnabled     int
	CreatedAt        string
	UpdatedAt        string
}

const healthSettingsCols = `workspace_id, user_id, sedentary_enabled, eye_enabled, night_mode, blue_light_filter, stats_enabled, created_at, updated_at`

// GetHealthSettings 按 (workspace_id, user_id) 查询健康设置；不存在返回 nil,nil。
func (r *Repo) GetHealthSettings(ctx context.Context, wsID, userID string) (*HealthSettingsRow, error) {
	var row HealthSettingsRow
	err := r.db.QueryRowContext(ctx,
		`SELECT `+healthSettingsCols+` FROM health_settings WHERE workspace_id = ? AND user_id = ?`,
		wsID, userID).
		Scan(&row.WorkspaceID, &row.UserID, &row.SedentaryEnabled, &row.EyeEnabled,
			&row.NightMode, &row.BlueLightFilter, &row.StatsEnabled, &row.CreatedAt, &row.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, normalizeErr(err)
	}
	return &row, nil
}

// UpsertHealthSettings 插入或更新健康设置（复合主键 upsert 语义）。
// 更新时 created_at 保持不变，updated_at 使用调用方传入时间（与 reminders 等保持一致）。
func (r *Repo) UpsertHealthSettings(ctx context.Context, row *HealthSettingsRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO health_settings (workspace_id, user_id, sedentary_enabled, eye_enabled, night_mode, blue_light_filter, stats_enabled, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (workspace_id, user_id) DO UPDATE SET
			sedentary_enabled = excluded.sedentary_enabled,
			eye_enabled = excluded.eye_enabled,
			night_mode = excluded.night_mode,
			blue_light_filter = excluded.blue_light_filter,
			stats_enabled = excluded.stats_enabled,
			updated_at = excluded.updated_at`,
		row.WorkspaceID, row.UserID, row.SedentaryEnabled, row.EyeEnabled,
		row.NightMode, row.BlueLightFilter, row.StatsEnabled, row.UpdatedAt)
	return normalizeErr(err)
}
