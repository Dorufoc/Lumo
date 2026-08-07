package repository

import (
	"context"
	"database/sql"
)

// FamilyBindingRow 是 family_bindings 表行（0005，DDL 冻结）。
// 注意：pending 态（学生发起邀请、家长未绑定）时 parent_user_id 暂时填学生本人 ID 作为占位
// （列 NOT NULL REFERENCES users(id)，满足 FK；激活时覆盖为真实家长）。
type FamilyBindingRow struct {
	ID            string
	StudentUserID string
	ParentUserID  string
	InviteCode    string
	Status        string
	BoundAt       *string
	RevokedAt     *string
	CreatedAt     string
}

const familyBindingCols = `id, student_user_id, parent_user_id, invite_code, status, bound_at, revoked_at, created_at`

// ParentSettingsRow 是 parent_settings 表行（0005，DDL 冻结，主键 parent_user_id+student_user_id）。
type ParentSettingsRow struct {
	ParentUserID  string
	StudentUserID string
	DailyLimitMin int
	AIDisabled    bool
	ReportEnabled bool
	CreatedAt     string
	UpdatedAt     string
}

const parentSettingsCols = `parent_user_id, student_user_id, daily_limit_min, ai_disabled, report_enabled, created_at, updated_at`

// CreateFamilyBinding 写入一条 family_bindings（初始 status=pending）。
func (r *Repo) CreateFamilyBinding(ctx context.Context, row *FamilyBindingRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO family_bindings (id, student_user_id, parent_user_id, invite_code, status)
		VALUES (?, ?, ?, ?, 'pending')`,
		row.ID, row.StudentUserID, row.ParentUserID, row.InviteCode)
	return normalizeErr(err)
}

// GetFamilyBindingByID 按 ID 获取绑定行；不存在返回 nil,nil。
func (r *Repo) GetFamilyBindingByID(ctx context.Context, id string) (*FamilyBindingRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+familyBindingCols+` FROM family_bindings WHERE id = ?`, id)
	return scanFamilyBinding(row)
}

// GetFamilyBindingByCode 按邀请码获取 pending 绑定行；不存在返回 nil,nil。
func (r *Repo) GetFamilyBindingByCode(ctx context.Context, code string) (*FamilyBindingRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+familyBindingCols+` FROM family_bindings WHERE invite_code = ? AND status = 'pending'`, code)
	return scanFamilyBinding(row)
}

// GetLatestPendingBindingByStudent 返回学生最近一条 pending 绑定（邀请码行）；无则 nil。
func (r *Repo) GetLatestPendingBindingByStudent(ctx context.Context, studentUserID string) (*FamilyBindingRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+familyBindingCols+` FROM family_bindings
		WHERE student_user_id = ? AND status = 'pending'
		ORDER BY created_at DESC LIMIT 1`, studentUserID)
	return scanFamilyBinding(row)
}

// ListFamilyBindingsByStudent 列出学生全部绑定（倒序，含 pending/active/revoked）。
func (r *Repo) ListFamilyBindingsByStudent(ctx context.Context, studentUserID string) ([]*FamilyBindingRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+familyBindingCols+` FROM family_bindings
		WHERE student_user_id = ? ORDER BY created_at DESC`, studentUserID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*FamilyBindingRow
	for rows.Next() {
		b, err := scanFamilyBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, normalizeErr(rows.Err())
}

// ListActiveBindingsForParent 列出家长的全部 active 绑定（家长端视图）。
func (r *Repo) ListActiveBindingsForParent(ctx context.Context, parentUserID string) ([]*FamilyBindingRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+familyBindingCols+` FROM family_bindings
		WHERE parent_user_id = ? AND status = 'active' ORDER BY bound_at DESC`, parentUserID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*FamilyBindingRow
	for rows.Next() {
		b, err := scanFamilyBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, normalizeErr(rows.Err())
}

// GetActiveFamilyBinding 查询 (student, parent) 是否已有 active 绑定；无则 nil。
func (r *Repo) GetActiveFamilyBinding(ctx context.Context, studentUserID, parentUserID string) (*FamilyBindingRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+familyBindingCols+` FROM family_bindings
		WHERE student_user_id = ? AND parent_user_id = ? AND status = 'active'`, studentUserID, parentUserID)
	return scanFamilyBinding(row)
}

// CountActiveFamilyBindingsForStudent 统计学生当前已生效的家长绑定数（4.21 G1：上限 2）。
func (r *Repo) CountActiveFamilyBindingsForStudent(ctx context.Context, studentUserID string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
		SELECT count(*) FROM family_bindings
		WHERE student_user_id = ? AND status = 'active'`, studentUserID).Scan(&n)
	if err != nil {
		return 0, normalizeErr(err)
	}
	return n, nil
}

// ActivateFamilyBinding 将 pending 绑定激活为 active（WHERE status='pending' 原子迁移；
// 0 行 → 状态已变化，调用方区分不存在/已处理）。
func (r *Repo) ActivateFamilyBinding(ctx context.Context, id, parentUserID, boundAt string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE family_bindings SET status = 'active', parent_user_id = ?, bound_at = ?
		WHERE id = ? AND status = 'pending'`, parentUserID, boundAt, id)
	if err != nil {
		return false, normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// RegenerateFamilyInvite 复用 pending 行重新生成邀请码并重置 24h 有效期（created_at 锚点）。
func (r *Repo) RegenerateFamilyInvite(ctx context.Context, id, code string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE family_bindings SET invite_code = ?,
			created_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ? AND status = 'pending'`, code, id)
	return normalizeErr(err)
}

// RevokeFamilyBinding 解除绑定（active→revoked；WHERE status='active' 原子迁移）。
func (r *Repo) RevokeFamilyBinding(ctx context.Context, id, revokedAt string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE family_bindings SET status = 'revoked', revoked_at = ?
		WHERE id = ? AND status = 'active'`, revokedAt, id)
	if err != nil {
		return false, normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// GetParentSettings 获取 (parent, student) 限制设置；不存在返回 nil,nil。
func (r *Repo) GetParentSettings(ctx context.Context, parentUserID, studentUserID string) (*ParentSettingsRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+parentSettingsCols+` FROM parent_settings
		WHERE parent_user_id = ? AND student_user_id = ?`, parentUserID, studentUserID)
	return scanParentSettings(row)
}

// UpsertParentSettings 写入/更新家长限制设置（主键 upsert，保留 created_at）。
func (r *Repo) UpsertParentSettings(ctx context.Context, q queryer, row *ParentSettingsRow) (*ParentSettingsRow, error) {
	_, err := q.ExecContext(ctx, `
		INSERT INTO parent_settings (parent_user_id, student_user_id, daily_limit_min, ai_disabled, report_enabled)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(parent_user_id, student_user_id) DO UPDATE SET
			daily_limit_min = excluded.daily_limit_min,
			ai_disabled = excluded.ai_disabled,
			report_enabled = excluded.report_enabled,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		row.ParentUserID, row.StudentUserID, row.DailyLimitMin, row.AIDisabled, row.ReportEnabled)
	if err != nil {
		return nil, normalizeErr(err)
	}
	return r.GetParentSettings(ctx, row.ParentUserID, row.StudentUserID)
}

// GetStudentAIDisabled 判断学生是否被任一 active 绑定的家长关闭 AI（Tutor/RAG）。
func (r *Repo) GetStudentAIDisabled(ctx context.Context, studentUserID string) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
		SELECT count(*) FROM family_bindings fb
		JOIN parent_settings ps ON ps.parent_user_id = fb.parent_user_id
			AND ps.student_user_id = fb.student_user_id
		WHERE fb.student_user_id = ? AND fb.status = 'active' AND ps.ai_disabled = 1`, studentUserID).Scan(&n)
	if err != nil {
		return false, normalizeErr(err)
	}
	return n > 0, nil
}

// GetStudentDailyLimitMin 返回学生被任一 active 绑定的家长设置的最严每日时长上限（分钟）；
// 取各 active 绑定 daily_limit_min>0 中的最小值（最严格）；无任何限制返回 0。
func (r *Repo) GetStudentDailyLimitMin(ctx context.Context, studentUserID string) (int, error) {
	var min sql.NullInt64
	err := r.db.QueryRowContext(ctx, `
		SELECT MIN(ps.daily_limit_min) FROM family_bindings fb
		JOIN parent_settings ps ON ps.parent_user_id = fb.parent_user_id
			AND ps.student_user_id = fb.student_user_id
		WHERE fb.student_user_id = ? AND fb.status = 'active' AND ps.daily_limit_min > 0`, studentUserID).Scan(&min)
	if err != nil {
		return 0, normalizeErr(err)
	}
	if !min.Valid {
		return 0, nil
	}
	return int(min.Int64), nil
}

// CountTodayStudyMinutes 统计学生当日已学习分钟。
// 口径（practice_sessions 无时长列，0001 DDL 冻结）：当日 status='completed' 的专注计时
// 实际秒数（4.13，折整为分钟）+ 当日打卡上报分钟（4.11）。两个事实源均为实际可测学习时长。
func (r *Repo) CountTodayStudyMinutes(ctx context.Context, userID, dayStart string) (int, error) {
	dayEnd := ""
	if len(dayStart) >= 19 {
		dayEnd = dayStart[:10] + "T23:59:59Z"
	}
	return r.CountStudyMinutesBetween(ctx, userID, dayStart, dayEnd)
}

// CountStudyMinutesBetween 统计 [from, to) 区间学习分钟（timer 秒折整 + checkins 分钟）。
func (r *Repo) CountStudyMinutesBetween(ctx context.Context, userID, from, to string) (int, error) {
	var secs int64
	err := r.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(actual_seconds), 0) FROM timer_sessions
		WHERE user_id = ? AND status = 'completed' AND ended_at IS NOT NULL AND ended_at >= ? AND ended_at < ?`,
		userID, from, to).Scan(&secs)
	if err != nil {
		return 0, normalizeErr(err)
	}
	fromDay, toDay := "", ""
	if len(from) >= 10 {
		fromDay = from[:10]
	}
	if len(to) >= 10 {
		toDay = to[:10]
	}
	var minutes int
	err = r.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(minutes), 0) FROM checkins
		WHERE user_id = ? AND date >= ? AND date < ?`, userID, fromDay, toDay).Scan(&minutes)
	if err != nil {
		return 0, normalizeErr(err)
	}
	return int(secs/60) + minutes, nil
}

// ---- 扫描 ----

func scanFamilyBinding(row interface{ Scan(...any) error }) (*FamilyBindingRow, error) {
	var b FamilyBindingRow
	err := row.Scan(&b.ID, &b.StudentUserID, &b.ParentUserID, &b.InviteCode,
		&b.Status, &b.BoundAt, &b.RevokedAt, &b.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, normalizeErr(err)
	}
	return &b, nil
}

func scanParentSettings(row interface{ Scan(...any) error }) (*ParentSettingsRow, error) {
	var p ParentSettingsRow
	err := row.Scan(&p.ParentUserID, &p.StudentUserID, &p.DailyLimitMin,
		&p.AIDisabled, &p.ReportEnabled, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, normalizeErr(err)
	}
	return &p, nil
}
