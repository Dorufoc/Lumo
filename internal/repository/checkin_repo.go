package repository

import (
	"context"
	"database/sql"
)

// CheckinRow 是打卡行（0005 checkins，user_id+date 唯一）。
type CheckinRow struct {
	ID        string
	UserID    string
	Date      string
	Kind      string
	Minutes   int
	CreatedAt string
}

// AchievementDefRow 是成就模板行（0005 achievement_defs）。
type AchievementDefRow struct {
	ID              string
	Code            string
	TitleKey        string
	DescriptionKey  string
	TriggerRuleJSON string
	Icon            string
	Version         int
}

// UserAchievementRow 是用户成就行（0005 user_achievements）。
type UserAchievementRow struct {
	ID             string
	UserID         string
	AchievementID  string
	AwardedAt      string
	EventRef       *string
	IdempotencyKey *string
}

// StreakSnapshotRow 是连续天数聚合投影行（0005 streak_snapshots）。
type StreakSnapshotRow struct {
	UserID        string
	Date          string
	Streak        int
	TotalCheckins int
	ComputedAt    string
}

// UsageEventRow 是只追加使用事件行（0005 usage_events）。
type UsageEventRow struct {
	ID          string
	WorkspaceID string
	UserID      string
	EventType   string
	PayloadJSON string
	OccurredAt  string
}

const checkinCols = `id, user_id, date, kind, minutes, created_at`

func scanCheckin(row interface{ Scan(...any) error }) (*CheckinRow, error) {
	var c CheckinRow
	if err := row.Scan(&c.ID, &c.UserID, &c.Date, &c.Kind, &c.Minutes, &c.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	return &c, nil
}

// GetCheckin 返回 (user_id, date) 的打卡行；不存在返回 nil,nil。
func (r *Repo) GetCheckin(ctx context.Context, userID, date string) (*CheckinRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+checkinCols+` FROM checkins WHERE user_id = ? AND date = ?`, userID, date)
	return scanCheckin(row)
}

// CreateCheckin 插入打卡行；user_id+date 已存在时幂等忽略并返回 false。
func (r *Repo) CreateCheckin(ctx context.Context, c *CheckinRow) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO checkins (id, user_id, date, kind, minutes)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(user_id, date) DO NOTHING`,
		c.ID, c.UserID, c.Date, c.Kind, c.Minutes)
	if err != nil {
		return false, normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// CountCheckins 统计用户打卡总数（含补签）。
func (r *Repo) CountCheckins(ctx context.Context, userID string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM checkins WHERE user_id = ?`, userID).Scan(&n)
	if err != nil {
		return 0, normalizeErr(err)
	}
	return n, nil
}

// CountMakeupInMonth 统计用户某月（YYYY-MM）补签次数（4.11 A2 配额）。
func (r *Repo) CountMakeupInMonth(ctx context.Context, userID, month string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM checkins
		WHERE user_id = ? AND kind = 'makeup' AND substr(date, 1, 7) = ?`, userID, month).Scan(&n)
	if err != nil {
		return 0, normalizeErr(err)
	}
	return n, nil
}

// ListCheckinDates 返回用户全部打卡日期（升序，供 streak 聚合计算）。
func (r *Repo) ListCheckinDates(ctx context.Context, userID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT date FROM checkins WHERE user_id = ? ORDER BY date`, userID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, normalizeErr(err)
		}
		out = append(out, d)
	}
	return out, normalizeErr(rows.Err())
}

// UpsertStreakSnapshot 写入/更新连续天数投影（PK user_id+date）。
func (r *Repo) UpsertStreakSnapshot(ctx context.Context, s *StreakSnapshotRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO streak_snapshots (user_id, date, streak, total_checkins, computed_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(user_id, date) DO UPDATE SET
			streak = excluded.streak,
			total_checkins = excluded.total_checkins,
			computed_at = excluded.computed_at`,
		s.UserID, s.Date, s.Streak, s.TotalCheckins, s.ComputedAt)
	return normalizeErr(err)
}

// GetStreakSnapshot 返回 (user_id, date) 快照；不存在返回 nil,nil。
func (r *Repo) GetStreakSnapshot(ctx context.Context, userID, date string) (*StreakSnapshotRow, error) {
	var s StreakSnapshotRow
	err := r.db.QueryRowContext(ctx, `
		SELECT user_id, date, streak, total_checkins, computed_at
		FROM streak_snapshots WHERE user_id = ? AND date = ?`, userID, date).
		Scan(&s.UserID, &s.Date, &s.Streak, &s.TotalCheckins, &s.ComputedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, normalizeErr(err)
	}
	return &s, nil
}

// SeedAchievementDefs 幂等写入固定成就模板（code 存在则跳过；0005 无 UNIQUE(code)，故 check-then-insert）。
func (r *Repo) SeedAchievementDefs(ctx context.Context, defs []*AchievementDefRow) error {
	for _, d := range defs {
		var existing string
		err := r.db.QueryRowContext(ctx, `SELECT id FROM achievement_defs WHERE code = ?`, d.Code).Scan(&existing)
		if err == sql.ErrNoRows {
			if _, err := r.db.ExecContext(ctx, `
				INSERT INTO achievement_defs (id, code, title_key, description_key, trigger_rule_json, icon, version)
				VALUES (?, ?, ?, ?, ?, ?, 1)`,
				d.ID, d.Code, d.TitleKey, d.DescriptionKey, d.TriggerRuleJSON, d.Icon); err != nil {
				return normalizeErr(err)
			}
			continue
		}
		if err != nil {
			return normalizeErr(err)
		}
	}
	return nil
}

const achievementDefCols = `id, code, title_key, description_key, trigger_rule_json, icon, version`

// ListAchievementDefs 列出全部成就模板（规则引擎 + AchievementList 共用）。
func (r *Repo) ListAchievementDefs(ctx context.Context) ([]*AchievementDefRow, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+achievementDefCols+` FROM achievement_defs ORDER BY version, code`)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*AchievementDefRow
	for rows.Next() {
		var d AchievementDefRow
		if err := rows.Scan(&d.ID, &d.Code, &d.TitleKey, &d.DescriptionKey, &d.TriggerRuleJSON, &d.Icon, &d.Version); err != nil {
			return nil, normalizeErr(err)
		}
		out = append(out, &d)
	}
	return out, normalizeErr(rows.Err())
}

// AwardAchievement 发放成就（user_id+achievement_id 已存在则跳过，返回 false）。
// user_achievements 无 UNIQUE(idempotency_key)，以自然键 (user_id, achievement_id) 去重；
// idempotency_key 列仍写入稳定键以便追溯（4.11：发放失败可重放、不重复发奖）。
func (r *Repo) AwardAchievement(ctx context.Context, a *UserAchievementRow) (bool, error) {
	var existing string
	err := r.db.QueryRowContext(ctx, `
		SELECT id FROM user_achievements WHERE user_id = ? AND achievement_id = ?`,
		a.UserID, a.AchievementID).Scan(&existing)
	if err == nil {
		return false, nil
	}
	if err != sql.ErrNoRows {
		return false, normalizeErr(err)
	}
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO user_achievements (id, user_id, achievement_id, awarded_at, event_ref, idempotency_key)
		VALUES (?, ?, ?, ?, ?, ?)`,
		a.ID, a.UserID, a.AchievementID, a.AwardedAt, a.EventRef, a.IdempotencyKey); err != nil {
		return false, normalizeErr(err)
	}
	return true, nil
}

// ListUserAchievements 列出用户已解锁成就（AchievementList 拼装用）。
func (r *Repo) ListUserAchievements(ctx context.Context, userID string) ([]*UserAchievementRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, user_id, achievement_id, awarded_at, event_ref, idempotency_key
		FROM user_achievements WHERE user_id = ? ORDER BY awarded_at`, userID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*UserAchievementRow
	for rows.Next() {
		var a UserAchievementRow
		if err := rows.Scan(&a.ID, &a.UserID, &a.AchievementID, &a.AwardedAt, &a.EventRef, &a.IdempotencyKey); err != nil {
			return nil, normalizeErr(err)
		}
		out = append(out, &a)
	}
	return out, normalizeErr(rows.Err())
}

// CreateUsageEvent 追加一条使用事件（只追加，4.11 事实防篡改）。
func (r *Repo) CreateUsageEvent(ctx context.Context, e *UsageEventRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO usage_events (id, workspace_id, user_id, event_type, payload_json, occurred_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		e.ID, e.WorkspaceID, e.UserID, e.EventType, e.PayloadJSON, e.OccurredAt)
	return normalizeErr(err)
}

// CountUsageEventsByType 统计用户某类型事件数（测试与事实断言用）。
func (r *Repo) CountUsageEventsByType(ctx context.Context, userID, eventType string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM usage_events WHERE user_id = ? AND event_type = ?`,
		userID, eventType).Scan(&n)
	if err != nil {
		return 0, normalizeErr(err)
	}
	return n, nil
}
