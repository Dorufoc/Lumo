package repository

import (
	"context"
	"database/sql"
	"encoding/json"

	"lumo/internal/domain"
)

// GoalRow 是 learning_goals 表行。
type GoalRow struct {
	ID                 string
	WorkspaceID        string
	UserID             string
	Name               string
	Subject            string
	ExamAt             *string
	TargetScore        *float64
	DailyMinutes       int
	AvailableWeekdays  json.RawMessage
	KnowledgeIDs       json.RawMessage
	Status             string
	CreatedAt          string
	UpdatedAt          string
	DeletedAt          *string
	Version            int
}

// PlanTaskRow 是 plan_tasks 表行。
type PlanTaskRow struct {
	ID             string
	WorkspaceID    string
	UserID         string
	GoalID         *string
	TaskType       string
	DueAt          string
	DurationMin    int
	Priority       int
	Status         string
	ReasonCodes    json.RawMessage
	GeneratedReason string
	CreatedAt      string
	UpdatedAt      string
	DeletedAt      *string
	Version        int
}

const goalCols = `id, workspace_id, user_id, name, subject, exam_at, target_score, daily_minutes,
	available_weekdays_json, knowledge_ids_json, status, created_at, updated_at, deleted_at, version`

func scanGoal(row interface{ Scan(...any) error }) (*GoalRow, error) {
	var g GoalRow
	var weekdays, knowledge string
	if err := row.Scan(&g.ID, &g.WorkspaceID, &g.UserID, &g.Name, &g.Subject, &g.ExamAt, &g.TargetScore,
		&g.DailyMinutes, &weekdays, &knowledge, &g.Status, &g.CreatedAt, &g.UpdatedAt, &g.DeletedAt, &g.Version); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	g.AvailableWeekdays = json.RawMessage(weekdays)
	g.KnowledgeIDs = json.RawMessage(knowledge)
	return &g, nil
}

const planTaskCols = `id, workspace_id, user_id, goal_id, task_type, due_at, duration_min, priority,
	status, reason_codes_json, generated_reason, created_at, updated_at, deleted_at, version`

func scanPlanTask(row interface{ Scan(...any) error }) (*PlanTaskRow, error) {
	var p PlanTaskRow
	var reasons string
	if err := row.Scan(&p.ID, &p.WorkspaceID, &p.UserID, &p.GoalID, &p.TaskType, &p.DueAt,
		&p.DurationMin, &p.Priority, &p.Status, &reasons, &p.GeneratedReason,
		&p.CreatedAt, &p.UpdatedAt, &p.DeletedAt, &p.Version); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	p.ReasonCodes = json.RawMessage(reasons)
	return &p, nil
}

// CreateGoal 创建学习目标。
func (r *Repo) CreateGoal(ctx context.Context, g *GoalRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO learning_goals (id, workspace_id, user_id, name, subject, exam_at, target_score,
			daily_minutes, available_weekdays_json, knowledge_ids_json, status, version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'draft', 1)`,
		g.ID, g.WorkspaceID, g.UserID, g.Name, g.Subject, g.ExamAt, g.TargetScore,
		g.DailyMinutes, string(g.AvailableWeekdays), string(g.KnowledgeIDs))
	return normalizeErr(err)
}

// GetGoal 获取目标。
func (r *Repo) GetGoal(ctx context.Context, wsID, id string) (*GoalRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+goalCols+` FROM learning_goals
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`, id, wsID)
	return scanGoal(row)
}

// ListGoals 列出目标（按状态过滤可选）。
func (r *Repo) ListGoals(ctx context.Context, wsID, userID, status string) ([]*GoalRow, error) {
	query := `SELECT ` + goalCols + ` FROM learning_goals
		WHERE workspace_id = ? AND deleted_at IS NULL`
	args := []any{wsID}
	if userID != "" {
		query += ` AND user_id = ?`
		args = append(args, userID)
	}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*GoalRow
	for rows.Next() {
		g, err := scanGoal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// UpdateGoal 乐观锁更新目标可编辑字段。
func (r *Repo) UpdateGoal(ctx context.Context, wsID, id string, version int, g *GoalRow) (*GoalRow, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE learning_goals SET name = ?, subject = ?, exam_at = ?, target_score = ?,
			daily_minutes = ?, available_weekdays_json = ?, knowledge_ids_json = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), version = version + 1
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL AND version = ?`,
		g.Name, g.Subject, g.ExamAt, g.TargetScore, g.DailyMinutes,
		string(g.AvailableWeekdays), string(g.KnowledgeIDs), id, wsID, version)
	if err != nil {
		return nil, normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		cur, err := r.GetGoal(ctx, wsID, id)
		if err != nil {
			return nil, err
		}
		if cur == nil {
			return nil, NotFoundErr("学习目标", id)
		}
		return nil, domain.Conflict("学习目标已被修改，请刷新后重试")
	}
	return r.GetGoal(ctx, wsID, id)
}

// UpdateGoalStatus 乐观锁更新目标状态。
func (r *Repo) UpdateGoalStatus(ctx context.Context, wsID, id string, version int, status string) (*GoalRow, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE learning_goals SET status = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), version = version + 1
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL AND version = ?`,
		status, id, wsID, version)
	if err != nil {
		return nil, normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		cur, err := r.GetGoal(ctx, wsID, id)
		if err != nil {
			return nil, err
		}
		if cur == nil {
			return nil, NotFoundErr("学习目标", id)
		}
		return nil, domain.Conflict("学习目标已被修改，请刷新后重试")
	}
	return r.GetGoal(ctx, wsID, id)
}

// CreatePlanTask 创建计划任务。
func (r *Repo) CreatePlanTask(ctx context.Context, p *PlanTaskRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO plan_tasks (id, workspace_id, user_id, goal_id, task_type, due_at, duration_min,
			priority, status, reason_codes_json, generated_reason, version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'planned', ?, ?, 1)`,
		p.ID, p.WorkspaceID, p.UserID, p.GoalID, p.TaskType, p.DueAt, p.DurationMin,
		p.Priority, string(p.ReasonCodes), p.GeneratedReason)
	return normalizeErr(err)
}

// GetPlanTask 获取任务。
func (r *Repo) GetPlanTask(ctx context.Context, wsID, id string) (*PlanTaskRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+planTaskCols+` FROM plan_tasks
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`, id, wsID)
	return scanPlanTask(row)
}

// ListPlanTasksInRange 列出日期范围内任务。
func (r *Repo) ListPlanTasksInRange(ctx context.Context, wsID, userID, start, end string) ([]*PlanTaskRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+planTaskCols+` FROM plan_tasks
		WHERE workspace_id = ? AND user_id = ? AND deleted_at IS NULL
		  AND due_at >= ? AND due_at < ?
		ORDER BY due_at, priority DESC`, wsID, userID, start, end)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*PlanTaskRow
	for rows.Next() {
		p, err := scanPlanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListPlanTasksByGoal 列出目标全部任务。
func (r *Repo) ListPlanTasksByGoal(ctx context.Context, goalID string) ([]*PlanTaskRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+planTaskCols+` FROM plan_tasks
		WHERE goal_id = ? AND deleted_at IS NULL ORDER BY due_at`, goalID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*PlanTaskRow
	for rows.Next() {
		p, err := scanPlanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdatePlanTaskStatus 乐观锁更新任务状态。
func (r *Repo) UpdatePlanTaskStatus(ctx context.Context, wsID, id string, version int, status string) (*PlanTaskRow, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE plan_tasks SET status = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), version = version + 1
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL AND version = ?`,
		status, id, wsID, version)
	if err != nil {
		return nil, normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		cur, err := r.GetPlanTask(ctx, wsID, id)
		if err != nil {
			return nil, err
		}
		if cur == nil {
			return nil, NotFoundErr("计划任务", id)
		}
		return nil, domain.Conflict("计划任务已被修改，请刷新后重试")
	}
	return r.GetPlanTask(ctx, wsID, id)
}
