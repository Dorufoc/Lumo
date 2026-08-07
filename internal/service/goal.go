package service

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"lumo/internal/domain"
	"lumo/internal/repository"
)

// LearningGoal 是学习目标 DTO。
type LearningGoal struct {
	ID                string   `json:"id"`
	WorkspaceID       string   `json:"workspace_id"`
	UserID            string   `json:"user_id"`
	Name              string   `json:"name"`
	Subject           string   `json:"subject"`
	ExamAt            *string  `json:"exam_at"`
	TargetScore       *float64 `json:"target_score"`
	DailyMinutes      int      `json:"daily_minutes"`
	AvailableWeekdays []int    `json:"available_weekdays"`
	KnowledgeIDs      []string `json:"knowledge_ids"`
	Status            string   `json:"status"`
	Version           int      `json:"version"`
	CreatedAt         string   `json:"created_at"`
	UpdatedAt         string   `json:"updated_at"`
}

// PlanTask 是计划任务 DTO。
type PlanTask struct {
	ID              string   `json:"id"`
	WorkspaceID     string   `json:"workspace_id"`
	UserID          string   `json:"user_id"`
	GoalID          *string  `json:"goal_id"`
	TaskType        string   `json:"task_type"`
	DueAt           string   `json:"due_at"`
	DurationMin     int      `json:"duration_min"`
	Priority        int      `json:"priority"`
	Status          string   `json:"status"`
	ReasonCodes     []string `json:"reason_codes"`
	GeneratedReason string   `json:"generated_reason"`
	Version         int      `json:"version"`
	CreatedAt       string   `json:"created_at"`
	UpdatedAt       string   `json:"updated_at"`
}

// GoalService 实现学习目标与计划用例。
type GoalService struct{ s *Services }

// GoalCreateReq 创建目标请求。
type GoalCreateReq struct {
	WorkspaceID       string   `json:"workspace_id"`
	UserID            string   `json:"user_id"`
	Name              string   `json:"name"`
	Subject           string   `json:"subject"`
	ExamAt            *string  `json:"exam_at"`
	TargetScore       *float64 `json:"target_score"`
	DailyMinutes      int      `json:"daily_minutes"`
	AvailableWeekdays []int    `json:"available_weekdays"`
	KnowledgeIDs      []string `json:"knowledge_ids"`
	IdempotencyKey    string   `json:"idempotency_key"`
}

// GoalCreate 创建目标。
func (g *GoalService) GoalCreate(ctx context.Context, req GoalCreateReq) (*LearningGoal, error) {
	if err := g.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if err := g.s.assertUserActive(ctx, req.UserID); err != nil {
		return nil, err
	}
	if req.Name == "" || len(req.Name) > 160 {
		return nil, domain.InvalidArg("目标名称长度须为 1-160")
	}
	if req.DailyMinutes < 1 || req.DailyMinutes > 1440 {
		return nil, domain.InvalidArg("daily_minutes 须在 1-1440")
	}
	if req.ExamAt != nil {
		if t, err := domain.ParseTime(*req.ExamAt); err != nil || t.Before(time.Now().UTC()) {
			return nil, domain.InvalidArg("exam_at 必须是未来的合法时间")
		}
	}
	if len(req.AvailableWeekdays) == 0 {
		req.AvailableWeekdays = []int{1, 2, 3, 4, 5}
	}
	for _, wd := range req.AvailableWeekdays {
		if wd < 1 || wd > 7 {
			return nil, domain.InvalidArg("available_weekdays 取值 1-7")
		}
	}
	if req.Subject == "" {
		req.Subject = "general"
	}

	return withIdempotency(g.s, ctx, req.WorkspaceID, req.IdempotencyKey, "GoalCreate", func() (*LearningGoal, error) {
		row := &repository.GoalRow{
			ID: NewID(), WorkspaceID: req.WorkspaceID, UserID: req.UserID,
			Name: req.Name, Subject: req.Subject, ExamAt: req.ExamAt, TargetScore: req.TargetScore,
			DailyMinutes:      req.DailyMinutes,
			AvailableWeekdays: mustJSON(req.AvailableWeekdays),
			KnowledgeIDs:      mustJSON(req.KnowledgeIDs),
		}
		if err := g.s.Repo.CreateGoal(ctx, row); err != nil {
			return nil, err
		}
		g.s.audit(ctx, req.WorkspaceID, "goal.create", "learning_goal", row.ID,
			map[string]any{"name": req.Name})
		return g.goalByID(ctx, req.WorkspaceID, row.ID)
	})
}

// GoalListReq 目标列表请求。
type GoalListReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	Status      string `json:"status"`
}

// GoalList 列出目标。
func (g *GoalService) GoalList(ctx context.Context, req GoalListReq) ([]*LearningGoal, error) {
	if err := g.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	rows, err := g.s.Repo.ListGoals(ctx, req.WorkspaceID, req.UserID, req.Status)
	if err != nil {
		return nil, err
	}
	out := make([]*LearningGoal, 0, len(rows))
	for _, r := range rows {
		out = append(out, goalFromRow(r))
	}
	return out, nil
}

// GoalUpdateReq 更新目标请求。
type GoalUpdateReq struct {
	WorkspaceID       string   `json:"workspace_id"`
	GoalID            string   `json:"goal_id"`
	Version           int      `json:"version"`
	Name              *string  `json:"name"`
	Subject           *string  `json:"subject"`
	ExamAt            *string  `json:"exam_at"`
	TargetScore       *float64 `json:"target_score"`
	DailyMinutes      *int     `json:"daily_minutes"`
	AvailableWeekdays []int    `json:"available_weekdays"`
	KnowledgeIDs      []string `json:"knowledge_ids"`
}

// GoalUpdate 更新目标字段（乐观锁；nil 表示不修改）。
func (g *GoalService) GoalUpdate(ctx context.Context, req GoalUpdateReq) (*LearningGoal, error) {
	if err := g.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	cur, err := g.s.Repo.GetGoal(ctx, req.WorkspaceID, req.GoalID)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, domain.NotFound("学习目标不存在")
	}
	if err := g.s.assertUserActive(ctx, cur.UserID); err != nil {
		return nil, err
	}
	row := &repository.GoalRow{
		Name: cur.Name, Subject: cur.Subject, ExamAt: cur.ExamAt, TargetScore: cur.TargetScore,
		DailyMinutes:      cur.DailyMinutes,
		AvailableWeekdays: cur.AvailableWeekdays,
		KnowledgeIDs:      cur.KnowledgeIDs,
	}
	if req.Name != nil {
		row.Name = *req.Name
	}
	if req.Subject != nil {
		row.Subject = *req.Subject
	}
	if req.ExamAt != nil {
		row.ExamAt = req.ExamAt
	}
	if req.TargetScore != nil {
		row.TargetScore = req.TargetScore
	}
	if req.DailyMinutes != nil {
		row.DailyMinutes = *req.DailyMinutes
	}
	if len(req.AvailableWeekdays) > 0 {
		row.AvailableWeekdays = mustJSON(req.AvailableWeekdays)
	}
	if len(req.KnowledgeIDs) > 0 {
		row.KnowledgeIDs = mustJSON(req.KnowledgeIDs)
	}
	updated, err := g.s.Repo.UpdateGoal(ctx, req.WorkspaceID, req.GoalID, req.Version, row)
	if err != nil {
		return nil, err
	}
	g.s.audit(ctx, req.WorkspaceID, "goal.update", "learning_goal", req.GoalID, nil)
	return goalFromRow(updated), nil
}

// GoalTransitionReq 目标状态迁移请求。
type GoalTransitionReq struct {
	WorkspaceID string `json:"workspace_id"`
	GoalID      string `json:"goal_id"`
	Version     int    `json:"version"`
	Action      string `json:"action"`
}

// GoalTransition 状态机：draft→active→paused→completed/archived。
func (g *GoalService) GoalTransition(ctx context.Context, req GoalTransitionReq) (*LearningGoal, error) {
	if err := g.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	cur, err := g.s.Repo.GetGoal(ctx, req.WorkspaceID, req.GoalID)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, domain.NotFound("学习目标不存在")
	}
	if err := g.s.assertUserActive(ctx, cur.UserID); err != nil {
		return nil, err
	}
	if cur.Version != req.Version {
		return nil, domain.Conflict("学习目标已被修改，请刷新后重试")
	}
	var next string
	switch req.Action {
	case "activate":
		if cur.Status != "draft" && cur.Status != "paused" {
			return nil, domain.InvalidState("仅 draft/paused 状态可激活")
		}
		next = "active"
	case "pause":
		if cur.Status != "active" {
			return nil, domain.InvalidState("仅 active 状态可暂停")
		}
		next = "paused"
	case "complete":
		if cur.Status != "active" && cur.Status != "paused" {
			return nil, domain.InvalidState("仅 active/paused 状态可完成")
		}
		next = "completed"
	case "archive":
		if cur.Status == "archived" {
			return nil, domain.InvalidState("目标已归档")
		}
		next = "archived"
	default:
		return nil, domain.InvalidArg("action 仅允许 activate/pause/complete/archive")
	}
	updated, err := g.s.Repo.UpdateGoalStatus(ctx, req.WorkspaceID, req.GoalID, req.Version, next)
	if err != nil {
		return nil, err
	}
	g.s.audit(ctx, req.WorkspaceID, "goal.transition", "learning_goal", req.GoalID,
		map[string]any{"action": req.Action, "from": cur.Status, "to": next})
	return goalFromRow(updated), nil
}

// PlanGenerateReq 生成计划请求。
type PlanGenerateReq struct {
	WorkspaceID    string `json:"workspace_id"`
	GoalID         string `json:"goal_id"`
	RangeStart     string `json:"range_start"` // RFC 3339 日期（可选，默认今天）
	RangeEnd       string `json:"range_end"`
	IdempotencyKey string `json:"idempotency_key"`
}

// PlanGenerate 按确定性规则生成计划任务：每天按 daily_minutes 生成练习任务；
// 已有任务的日期自动跳过（可重复调用补缺）。
func (g *GoalService) PlanGenerate(ctx context.Context, req PlanGenerateReq) ([]*PlanTask, error) {
	if err := g.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	goal, err := g.s.Repo.GetGoal(ctx, req.WorkspaceID, req.GoalID)
	if err != nil {
		return nil, err
	}
	if goal == nil {
		return nil, domain.NotFound("学习目标不存在")
	}
	if err := g.s.assertUserActive(ctx, goal.UserID); err != nil {
		return nil, err
	}
	if goal.Status != "active" && goal.Status != "draft" {
		return nil, domain.InvalidState("仅 active/draft 目标可生成计划")
	}

	start := time.Now().UTC().Truncate(24 * time.Hour)
	if req.RangeStart != "" {
		t, err := domain.ParseTime(req.RangeStart)
		if err != nil {
			return nil, domain.InvalidArg("range_start 时间格式非法")
		}
		start = t.UTC().Truncate(24 * time.Hour)
	}
	end := start.AddDate(0, 0, 13)
	if req.RangeEnd != "" {
		t, err := domain.ParseTime(req.RangeEnd)
		if err != nil {
			return nil, domain.InvalidArg("range_end 时间格式非法")
		}
		end = t.UTC().Truncate(24 * time.Hour)
	}
	if end.Before(start) {
		return nil, domain.InvalidArg("range_end 不能早于 range_start")
	}

	return withIdempotency(g.s, ctx, req.WorkspaceID, req.IdempotencyKey, "PlanGenerate", func() ([]*PlanTask, error) {
		weekdaySet := map[int]bool{}
		var weekdays []int
		_ = json.Unmarshal(goal.AvailableWeekdays, &weekdays)
		for _, wd := range weekdays {
			weekdaySet[wd] = true
		}
		var created []*PlanTask
		for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
			// 约定 1=周一 … 7=周日；Go Weekday() 周日为 0。
			wd := int(day.Weekday())
			if wd == 0 {
				wd = 7
			}
			if !weekdaySet[wd] {
				continue
			}
			dayStart := day.Format(time.RFC3339)
			dayEnd := day.AddDate(0, 0, 1).Format(time.RFC3339)
			existing, err := g.s.Repo.ListPlanTasksInRange(ctx, req.WorkspaceID, goal.UserID, dayStart, dayEnd)
			if err != nil {
				return nil, err
			}
			if len(existing) > 0 {
				created = append(created, planTasksFromRows(existing)...)
				continue
			}
			// 练习任务：按每日分钟数切分（<=60 分钟一段）。
			minutes := goal.DailyMinutes
			for minutes > 0 {
				dur := minutes
				if dur > 60 {
					dur = 60
				}
				dueAt := dayStart
				if minutes != goal.DailyMinutes {
					// 第二段任务顺延到当天晚些时候（UTC 简化处理）
					dueAt = day.Add(2 * time.Hour).Format(time.RFC3339)
				}
				row := &repository.PlanTaskRow{
					ID: NewID(), WorkspaceID: req.WorkspaceID, UserID: goal.UserID,
					GoalID: &goal.ID, TaskType: "practice", DueAt: dueAt,
					DurationMin: dur, Priority: 50,
					ReasonCodes:     mustJSON([]string{"PLAN_INIT"}),
					GeneratedReason: "按每日 " + strconv.Itoa(goal.DailyMinutes) + " 分钟目标生成练习任务",
				}
				if err := g.s.Repo.CreatePlanTask(ctx, row); err != nil {
					return nil, err
				}
				row.Status = "planned"
				row.CreatedAt = Now()
				row.UpdatedAt = Now()
				row.Version = 1
				created = append(created, planTaskFromRow(row))
				minutes -= dur
			}
		}
		g.s.audit(ctx, req.WorkspaceID, "plan.generate", "learning_goal", goal.ID,
			map[string]any{"range_start": start.Format(time.RFC3339), "range_end": end.Format(time.RFC3339)})
		return created, nil
	})
}

// PlanListTodayReq 今日计划请求。
type PlanListTodayReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	Date        string `json:"date"` // RFC 3339 或日期，默认今天
}

// PlanListToday 列出当日计划任务。
func (g *GoalService) PlanListToday(ctx context.Context, req PlanListTodayReq) ([]*PlanTask, error) {
	if err := g.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	day := time.Now().UTC().Truncate(24 * time.Hour)
	if req.Date != "" {
		t, err := domain.ParseTime(req.Date)
		if err != nil {
			return nil, domain.InvalidArg("date 时间格式非法")
		}
		day = t.UTC().Truncate(24 * time.Hour)
	}
	rows, err := g.s.Repo.ListPlanTasksInRange(ctx, req.WorkspaceID, req.UserID,
		day.Format(time.RFC3339), day.AddDate(0, 0, 1).Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	return planTasksFromRows(rows), nil
}

// PlanTaskTransitionReq 任务状态迁移请求。
type PlanTaskTransitionReq struct {
	WorkspaceID string `json:"workspace_id"`
	TaskID      string `json:"task_id"`
	Version     int    `json:"version"`
	Action      string `json:"action"` // start | complete | skip | restore
	Reason      string `json:"reason"`
}

// PlanTaskTransition 任务状态机。
func (g *GoalService) PlanTaskTransition(ctx context.Context, req PlanTaskTransitionReq) (*PlanTask, error) {
	if err := g.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	cur, err := g.s.Repo.GetPlanTask(ctx, req.WorkspaceID, req.TaskID)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, domain.NotFound("计划任务不存在")
	}
	if err := g.s.assertUserActive(ctx, cur.UserID); err != nil {
		return nil, err
	}
	if cur.Version != req.Version {
		return nil, domain.Conflict("计划任务已被修改，请刷新后重试")
	}
	var next string
	switch req.Action {
	case "start":
		if cur.Status != "planned" && cur.Status != "available" {
			return nil, domain.InvalidState("仅 planned/available 任务可开始")
		}
		next = "in_progress"
	case "complete":
		if cur.Status != "in_progress" && cur.Status != "available" {
			return nil, domain.InvalidState("仅 in_progress/available 任务可完成")
		}
		next = "completed"
	case "skip":
		if cur.Status == "completed" || cur.Status == "skipped" {
			return nil, domain.InvalidState("任务已结束，不能跳过")
		}
		next = "skipped"
	case "restore":
		if cur.Status != "skipped" {
			return nil, domain.InvalidState("仅 skipped 任务可恢复")
		}
		next = "planned"
	default:
		return nil, domain.InvalidArg("action 仅允许 start/complete/skip/restore")
	}
	updated, err := g.s.Repo.UpdatePlanTaskStatus(ctx, req.WorkspaceID, req.TaskID, req.Version, next)
	if err != nil {
		return nil, err
	}
	g.s.audit(ctx, req.WorkspaceID, "plan_task.transition", "plan_task", req.TaskID,
		map[string]any{"action": req.Action, "from": cur.Status, "to": next, "reason": req.Reason})
	return planTaskFromRow(updated), nil
}

func (g *GoalService) goalByID(ctx context.Context, wsID, id string) (*LearningGoal, error) {
	row, err := g.s.Repo.GetGoal(ctx, wsID, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, domain.NotFound("学习目标不存在")
	}
	return goalFromRow(row), nil
}

func goalFromRow(r *repository.GoalRow) *LearningGoal {
	out := &LearningGoal{
		ID: r.ID, WorkspaceID: r.WorkspaceID, UserID: r.UserID, Name: r.Name,
		Subject: r.Subject, ExamAt: r.ExamAt, TargetScore: r.TargetScore,
		DailyMinutes: r.DailyMinutes, Status: r.Status,
		Version: r.Version, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	_ = json.Unmarshal(r.AvailableWeekdays, &out.AvailableWeekdays)
	_ = json.Unmarshal(r.KnowledgeIDs, &out.KnowledgeIDs)
	return out
}

func planTaskFromRow(r *repository.PlanTaskRow) *PlanTask {
	out := &PlanTask{
		ID: r.ID, WorkspaceID: r.WorkspaceID, UserID: r.UserID, GoalID: r.GoalID,
		TaskType: r.TaskType, DueAt: r.DueAt, DurationMin: r.DurationMin,
		Priority: r.Priority, Status: r.Status, GeneratedReason: r.GeneratedReason,
		Version: r.Version, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	_ = json.Unmarshal(r.ReasonCodes, &out.ReasonCodes)
	return out
}

func planTasksFromRows(rows []*repository.PlanTaskRow) []*PlanTask {
	out := make([]*PlanTask, 0, len(rows))
	for _, r := range rows {
		out = append(out, planTaskFromRow(r))
	}
	return out
}
