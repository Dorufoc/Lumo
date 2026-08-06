package repository

import (
	"context"
	"database/sql"
)

// ReminderRow 是 reminders 表行（4.14 提醒 / 6.2.1）。
type ReminderRow struct {
	ID            string
	WorkspaceID   string
	UserID        string
	Kind          string
	RuleJSON      string
	Enabled       int
	NextTriggerAt string
	CreatedAt     string
	UpdatedAt     string
}

const reminderCols = `id, workspace_id, user_id, kind, rule_json, enabled, next_trigger_at, created_at, updated_at`

// GetReminder 按 (user_id, kind) 查询提醒；不存在返回 nil,nil。
// 0005 迁移 reminders 表无 UNIQUE(user_id, kind) 约束，应用层保证该组合唯一
// （ReminderUpsert check-then-insert，SQLite 单写者下安全，同 SeedAchievementDefs）。
func (r *Repo) GetReminder(ctx context.Context, userID, kind string) (*ReminderRow, error) {
	var row ReminderRow
	err := r.db.QueryRowContext(ctx,
		`SELECT `+reminderCols+` FROM reminders WHERE user_id = ? AND kind = ? LIMIT 1`,
		userID, kind).
		Scan(&row.ID, &row.WorkspaceID, &row.UserID, &row.Kind, &row.RuleJSON,
			&row.Enabled, &row.NextTriggerAt, &row.CreatedAt, &row.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, normalizeErr(err)
	}
	return &row, nil
}

// CreateReminder 插入提醒。
func (r *Repo) CreateReminder(ctx context.Context, row *ReminderRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO reminders (id, workspace_id, user_id, kind, rule_json, enabled, next_trigger_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.WorkspaceID, row.UserID, row.Kind, row.RuleJSON, row.Enabled, row.NextTriggerAt)
	return normalizeErr(err)
}

// UpdateReminder 更新规则/启用/下次触发时间（按 id），updated_at 使用调用方时间。
func (r *Repo) UpdateReminder(ctx context.Context, row *ReminderRow) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE reminders SET rule_json = ?, enabled = ?, next_trigger_at = ?, updated_at = ?
		WHERE id = ?`,
		row.RuleJSON, row.Enabled, row.NextTriggerAt, row.UpdatedAt, row.ID)
	return normalizeErr(err)
}

// ListDueReminders 列出到期提醒（enabled=1 AND next_trigger_at<=?），按触发时间升序。
func (r *Repo) ListDueReminders(ctx context.Context, now string) ([]*ReminderRow, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+reminderCols+` FROM reminders WHERE enabled = 1 AND next_trigger_at <= ? ORDER BY next_trigger_at`,
		now)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*ReminderRow
	for rows.Next() {
		var row ReminderRow
		if err := rows.Scan(&row.ID, &row.WorkspaceID, &row.UserID, &row.Kind, &row.RuleJSON,
			&row.Enabled, &row.NextTriggerAt, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return nil, normalizeErr(err)
		}
		out = append(out, &row)
	}
	if err := rows.Err(); err != nil {
		return nil, normalizeErr(err)
	}
	return out, nil
}

// ClaimReminder 原子抢占一条到期提醒：仅当 id 匹配、仍启用且仍到期（next_trigger_at<=now）
// 时更新 next_trigger_at / enabled，返回是否抢占成功（RowsAffected>0）。
// 这是调度幂等的关键闸门：抢占发生在事件发布之前（claim-before-fire），
// 重复扫描 / 并发扫描中已被抢占的提醒不会再被发布。
func (r *Repo) ClaimReminder(ctx context.Context, id, now, newNext string, newEnabled int) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE reminders SET next_trigger_at = ?, enabled = ?, updated_at = ?
		WHERE id = ? AND enabled = 1 AND next_trigger_at <= ?`,
		newNext, newEnabled, now, id, now)
	if err != nil {
		return false, normalizeErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, normalizeErr(err)
	}
	return n > 0, nil
}
