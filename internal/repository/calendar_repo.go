package repository

import (
	"context"
	"database/sql"

	"lumo/internal/domain"
)

// CalendarEventRow 是 calendar_events 表行（0005_student.sql 4.16）。
type CalendarEventRow struct {
	ID          string
	WorkspaceID string
	UserID      string
	Kind        string
	RefID       *string
	EventDate   string
	StartTime   *string
	DurationMin int
	Title       string
	Note        string
	CreatedAt   string
	UpdatedAt   string
}

const calendarEventCols = `id, workspace_id, user_id, kind, ref_id, event_date, start_time, duration_min, title, note, created_at, updated_at`

func scanCalendarEvent(row interface{ Scan(...any) error }) (*CalendarEventRow, error) {
	var e CalendarEventRow
	if err := row.Scan(&e.ID, &e.WorkspaceID, &e.UserID, &e.Kind, &e.RefID, &e.EventDate,
		&e.StartTime, &e.DurationMin, &e.Title, &e.Note, &e.CreatedAt, &e.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	return &e, nil
}

// CreateCalendarEvent 插入个人日历事件（kind 由服务层校验为 personal）。
func (r *Repo) CreateCalendarEvent(ctx context.Context, e *CalendarEventRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO calendar_events (id, workspace_id, user_id, kind, ref_id, event_date, start_time, duration_min, title, note)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.WorkspaceID, e.UserID, e.Kind, e.RefID, e.EventDate, e.StartTime, e.DurationMin, e.Title, e.Note)
	return normalizeErr(err)
}

// UpdateCalendarEvent 更新个人日历事件（按 id+workspace 定位，无版本列）。
func (r *Repo) UpdateCalendarEvent(ctx context.Context, wsID, id string, e *CalendarEventRow) (*CalendarEventRow, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE calendar_events SET event_date = ?, start_time = ?, duration_min = ?, title = ?, note = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ? AND workspace_id = ?`,
		e.EventDate, e.StartTime, e.DurationMin, e.Title, e.Note, id, wsID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, NotFoundErr("日历事件", id)
	}
	return r.GetCalendarEvent(ctx, wsID, id)
}

// GetCalendarEvent 获取日历事件（按工作区隔离）。
func (r *Repo) GetCalendarEvent(ctx context.Context, wsID, id string) (*CalendarEventRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+calendarEventCols+` FROM calendar_events
		WHERE id = ? AND workspace_id = ?`, id, wsID)
	return scanCalendarEvent(row)
}

// ListCalendarEventsInMonth 列出某月（YYYY-MM）内个人日历事件。
func (r *Repo) ListCalendarEventsInMonth(ctx context.Context, wsID, userID, month string) ([]*CalendarEventRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+calendarEventCols+` FROM calendar_events
		WHERE workspace_id = ? AND user_id = ? AND substr(event_date, 1, 7) = ?
		ORDER BY event_date, start_time`, wsID, userID, month)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*CalendarEventRow
	for rows.Next() {
		e, err := scanCalendarEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, normalizeErr(rows.Err())
}

// MilestoneRow 是 goal_milestones 表行（0005_student.sql 4.16）。
type MilestoneRow struct {
	ID           string
	GoalID       string
	Title        string
	DueAt        string
	CriteriaJSON string
	Status       string
	AchievedAt   *string
	CreatedAt    string
	UpdatedAt    string
}

const milestoneCols = `id, goal_id, title, due_at, criteria_json, status, achieved_at, created_at, updated_at`

func scanMilestone(row interface{ Scan(...any) error }) (*MilestoneRow, error) {
	var m MilestoneRow
	if err := row.Scan(&m.ID, &m.GoalID, &m.Title, &m.DueAt, &m.CriteriaJSON,
		&m.Status, &m.AchievedAt, &m.CreatedAt, &m.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	return &m, nil
}

// CreateMilestone 创建目标里程碑（status 默认 pending）。
func (r *Repo) CreateMilestone(ctx context.Context, m *MilestoneRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO goal_milestones (id, goal_id, title, due_at, criteria_json, status)
		VALUES (?, ?, ?, ?, ?, 'pending')`,
		m.ID, m.GoalID, m.Title, m.DueAt, m.CriteriaJSON)
	return normalizeErr(err)
}

// GetMilestone 获取里程碑；goal_milestones 无 workspace_id，经 learning_goals 关联隔离工作区。
func (r *Repo) GetMilestone(ctx context.Context, wsID, id string) (*MilestoneRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT m.id, m.goal_id, m.title, m.due_at, m.criteria_json,
			m.status, m.achieved_at, m.created_at, m.updated_at
		FROM goal_milestones m
		JOIN learning_goals g ON g.id = m.goal_id
		WHERE m.id = ? AND g.workspace_id = ? AND g.deleted_at IS NULL`, id, wsID)
	return scanMilestone(row)
}

// UpdateMilestoneResult 判定里程碑：写状态与达成时间（无版本列，直接覆写）。
func (r *Repo) UpdateMilestoneResult(ctx context.Context, id, status string, achievedAt *string) (*MilestoneRow, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE goal_milestones SET status = ?, achieved_at = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ?`,
		status, achievedAt, id)
	if err != nil {
		return nil, normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, NotFoundErr("里程碑", id)
	}
	row := r.db.QueryRowContext(ctx, `
		SELECT `+milestoneCols+` FROM goal_milestones WHERE id = ?`, id)
	return scanMilestone(row)
}

// MilestoneFacts 是里程碑判定的服务端事实聚合（4.16 C3：题量/正确率、任务完成数）。
type MilestoneFacts struct {
	SubmissionCount int      // 用户已完成练习提交数（submissions.status='submitted'）
	Accuracy        *float64 // 已判分提交的平均得分率（score/max_score），无判分数据为 nil
	CompletedTasks  int      // 目标下已完成计划任务数
}

// GetMilestoneFacts 聚合里程碑判定所需事实（practice：提交数+平均正确率；tasks：已完成任务数）。
func (r *Repo) GetMilestoneFacts(ctx context.Context, wsID, userID, goalID string) (*MilestoneFacts, error) {
	f := &MilestoneFacts{}
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM submissions s
		JOIN practice_sessions p ON p.id = s.session_id
		WHERE p.workspace_id = ? AND p.user_id = ? AND p.mode = 'practice' AND s.status = 'submitted'`,
		wsID, userID).Scan(&f.SubmissionCount); err != nil {
		return nil, normalizeErr(err)
	}
	var acc sql.NullFloat64
	if err := r.db.QueryRowContext(ctx, `
		SELECT AVG(g.score / g.max_score) FROM grading_results g
		JOIN submissions s ON s.id = g.submission_id
		JOIN practice_sessions p ON p.id = s.session_id
		WHERE p.workspace_id = ? AND p.user_id = ? AND p.mode = 'practice'
		  AND s.status = 'submitted' AND g.status = 'completed' AND g.score IS NOT NULL AND g.max_score > 0`,
		wsID, userID).Scan(&acc); err != nil {
		return nil, normalizeErr(err)
	}
	if acc.Valid {
		f.Accuracy = &acc.Float64
	}
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM plan_tasks
		WHERE goal_id = ? AND status = 'completed' AND deleted_at IS NULL`, goalID).Scan(&f.CompletedTasks); err != nil {
		return nil, normalizeErr(err)
	}
	return f, nil
}

// CalendarEntryRow 是日历月视图投影行（4.16 C1：任务/复习/考试/打卡/专注/个人事件）。
type CalendarEntryRow struct {
	Kind        string
	RefID       string
	Title       string
	EventDate   string
	StartTime   *string
	DurationMin int
}

// ListCalendarMonthEntries 聚合某月（YYYY-MM）全部日历投影，按日期排序。
// 数据全部来自现有业务对象投影，不维护"日历事件主数据"（4.16 规则）。
func (r *Repo) ListCalendarMonthEntries(ctx context.Context, wsID, userID, month string) ([]*CalendarEntryRow, error) {
	query := `
		SELECT 'task' AS kind, pt.id AS ref_id, COALESCE(NULLIF(pt.generated_reason, ''), pt.task_type) AS title,
			substr(pt.due_at, 1, 10) AS event_date, NULL AS start_time, pt.duration_min
		FROM plan_tasks pt
		WHERE pt.workspace_id = ? AND pt.user_id = ? AND pt.deleted_at IS NULL AND substr(pt.due_at, 1, 7) = ?
		UNION ALL
		SELECT 'review', rc.id, '', substr(rc.due_at, 1, 10), NULL, 0
		FROM review_cards rc
		WHERE rc.workspace_id = ? AND rc.user_id = ? AND rc.status = 'active' AND substr(rc.due_at, 1, 7) = ?
		UNION ALL
		SELECT 'exam', e.id, p.title, substr(e.started_at, 1, 10), NULL, 0
		FROM exams e JOIN exam_papers p ON p.id = e.paper_id
		WHERE p.workspace_id = ? AND e.user_id = ? AND e.started_at IS NOT NULL AND substr(e.started_at, 1, 7) = ?
		UNION ALL
		SELECT 'checkin', c.id, '', c.date, NULL, c.minutes
		FROM checkins c
		WHERE c.user_id = ? AND substr(c.date, 1, 7) = ?
		UNION ALL
		SELECT 'focus', t.id, '', substr(t.started_at, 1, 10), NULL, t.actual_seconds / 60
		FROM timer_sessions t
		WHERE t.workspace_id = ? AND t.user_id = ? AND t.ended_at IS NOT NULL AND substr(t.started_at, 1, 7) = ?
		UNION ALL
		SELECT ce.kind, ce.id, ce.title, ce.event_date, ce.start_time, ce.duration_min
		FROM calendar_events ce
		WHERE ce.workspace_id = ? AND ce.user_id = ? AND substr(ce.event_date, 1, 7) = ?
		ORDER BY event_date, start_time`
	rows, err := r.db.QueryContext(ctx, query, wsID, userID, month, wsID, userID, month,
		wsID, userID, month, userID, month, wsID, userID, month, wsID, userID, month)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*CalendarEntryRow
	for rows.Next() {
		var e CalendarEntryRow
		if err := rows.Scan(&e.Kind, &e.RefID, &e.Title, &e.EventDate, &e.StartTime, &e.DurationMin); err != nil {
			return nil, normalizeErr(err)
		}
		out = append(out, &e)
	}
	return out, normalizeErr(rows.Err())
}

// AssertCalendarEventKind 供服务层使用：校验 kind 枚举（避免仓库依赖领域常量顺序变更）。
func (r *Repo) AssertCalendarEventKind(kind string) bool {
	switch kind {
	case domain.CalendarKindTask, domain.CalendarKindReview, domain.CalendarKindExam,
		domain.CalendarKindCheckin, domain.CalendarKindFocus, domain.CalendarKindPersonal:
		return true
	}
	return false
}
