package repository

import (
	"context"
	"database/sql"

	"lumo/internal/domain"
)

const timerSessionCols = `id, workspace_id, user_id, mode, planned_minutes, actual_seconds, task_id, status, interrupt_reason, started_at, ended_at, created_at, updated_at`

func scanTimerSession(row interface{ Scan(...any) error }) (*domain.TimerSession, error) {
	var t domain.TimerSession
	if err := row.Scan(&t.ID, &t.WorkspaceID, &t.UserID, &t.Mode, &t.PlannedMinutes, &t.ActualSeconds,
		&t.TaskID, &t.Status, &t.InterruptReason, &t.StartedAt, &t.EndedAt, &t.CreatedAt, &t.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	return &t, nil
}

// CreateTimerSession 插入专注会话（活动态：ended_at 为空、status 用 DDL 默认占位，结束/归档时覆写终态）。
func (r *Repo) CreateTimerSession(ctx context.Context, t *domain.TimerSession) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO timer_sessions (id, workspace_id, user_id, mode, planned_minutes, actual_seconds, task_id, started_at)
		VALUES (?, ?, ?, ?, ?, 0, ?, ?)`,
		t.ID, t.WorkspaceID, t.UserID, t.Mode, t.PlannedMinutes, t.TaskID, t.StartedAt)
	return normalizeErr(err)
}

// GetActiveTimerSession 返回用户当前活动会话（ended_at IS NULL）；无则 nil,nil。
func (r *Repo) GetActiveTimerSession(ctx context.Context, userID string) (*domain.TimerSession, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+timerSessionCols+` FROM timer_sessions
		WHERE user_id = ? AND ended_at IS NULL
		ORDER BY created_at DESC LIMIT 1`, userID)
	return scanTimerSession(row)
}

// GetTimerSession 按工作区隔离返回会话；不存在返回 nil,nil。
func (r *Repo) GetTimerSession(ctx context.Context, wsID, id string) (*domain.TimerSession, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+timerSessionCols+` FROM timer_sessions
		WHERE id = ? AND workspace_id = ?`, id, wsID)
	return scanTimerSession(row)
}

// EndTimerSession 原子结束会话：仅当 ended_at IS NULL 时更新，保证单次归档（返回是否生效）。
// 活动态不存在时返回 false，调用方读回既有终态做幂等处理。
func (r *Repo) EndTimerSession(ctx context.Context, id, endedAt string, actualSeconds int, status, interruptReason string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE timer_sessions
		SET ended_at = ?, actual_seconds = ?, status = ?, interrupt_reason = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ? AND ended_at IS NULL`,
		endedAt, actualSeconds, status, interruptReason, id)
	if err != nil {
		return false, normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListTimerSessionsInRange 列出用户 [start,end]（YYYY-MM-DD，按 started_at 日期含边界）内已结束会话。
// start/end 为 nil 时不过滤（全部时间）。
func (r *Repo) ListTimerSessionsInRange(ctx context.Context, userID string, start, end *string) ([]*domain.TimerSession, error) {
	return r.listTimerSessions(ctx, userID, start, end)
}

// ListTimerSessions 列出用户全部已结束会话（完整性兜底）。
func (r *Repo) ListTimerSessions(ctx context.Context, userID string) ([]*domain.TimerSession, error) {
	return r.listTimerSessions(ctx, userID, nil, nil)
}

func (r *Repo) listTimerSessions(ctx context.Context, userID string, start, end *string) ([]*domain.TimerSession, error) {
	q := `SELECT ` + timerSessionCols + ` FROM timer_sessions
		WHERE user_id = ? AND ended_at IS NOT NULL`
	args := []any{userID}
	if start != nil {
		q += ` AND substr(started_at, 1, 10) >= ?`
		args = append(args, *start)
	}
	if end != nil {
		q += ` AND substr(started_at, 1, 10) <= ?`
		args = append(args, *end)
	}
	q += ` ORDER BY started_at`
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*domain.TimerSession
	for rows.Next() {
		t, err := scanTimerSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, normalizeErr(rows.Err())
}

// TimerStatsRow 是专注统计聚合结果（由 SQL 聚合一次性算出）。
type TimerStatsRow struct {
	TotalSessions       int
	TotalSeconds        int
	CompletedSessions   int
	InterruptedSessions int
	AbandonedSessions   int
}

// AggregateTimerStats 按日期范围聚合用户专注事实（prefer SQL 聚合，避免 Go 循环）。
// 只统计已结束会话（ended_at IS NOT NULL）；start/end 为 nil 时不过滤。
func (r *Repo) AggregateTimerStats(ctx context.Context, userID string, start, end *string) (*TimerStatsRow, error) {
	q := `
		SELECT COUNT(*) AS total_sessions,
			COALESCE(SUM(actual_seconds), 0) AS total_seconds,
			COALESCE(SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END), 0) AS completed_sessions,
			COALESCE(SUM(CASE WHEN status = 'interrupted' THEN 1 ELSE 0 END), 0) AS interrupted_sessions,
			COALESCE(SUM(CASE WHEN status = 'abandoned' THEN 1 ELSE 0 END), 0) AS abandoned_sessions
		FROM timer_sessions
		WHERE user_id = ? AND ended_at IS NOT NULL`
	args := []any{userID}
	if start != nil {
		q += ` AND substr(started_at, 1, 10) >= ?`
		args = append(args, *start)
	}
	if end != nil {
		q += ` AND substr(started_at, 1, 10) <= ?`
		args = append(args, *end)
	}
	var s TimerStatsRow
	err := r.db.QueryRowContext(ctx, q, args...).
		Scan(&s.TotalSessions, &s.TotalSeconds, &s.CompletedSessions, &s.InterruptedSessions, &s.AbandonedSessions)
	if err != nil {
		return nil, normalizeErr(err)
	}
	return &s, nil
}
