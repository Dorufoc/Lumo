package service

import (
	"context"
	"encoding/json"
	"strings"

	"lumo/internal/domain"
	"lumo/internal/repository"
)

// CalendarEntry 是日历月视图单日投影条目（4.16 C1：任务/复习/考试/打卡/专注/个人事件）。
type CalendarEntry struct {
	Kind        string  `json:"kind"`
	RefID       string  `json:"ref_id"`
	Title       string  `json:"title"`
	EventDate   string  `json:"event_date"`
	StartTime   *string `json:"start_time"`
	DurationMin int     `json:"duration_min"`
}

// CalendarMonth 是日历月视图投影响应。
type CalendarMonth struct {
	Month   string           `json:"month"`
	Entries []*CalendarEntry `json:"entries"`
}

// CalendarEvent 是个人日历事件 DTO（4.16：personal 事件可独立增删改）。
type CalendarEvent struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	UserID      string  `json:"user_id"`
	Kind        string  `json:"kind"`
	RefID       *string `json:"ref_id"`
	EventDate   string  `json:"event_date"`
	StartTime   *string `json:"start_time"`
	DurationMin int     `json:"duration_min"`
	Title       string  `json:"title"`
	Note        string  `json:"note"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// Milestone 是目标里程碑 DTO（4.16 C3：绑定到期日与验收条件）。
type Milestone struct {
	ID         string          `json:"id"`
	GoalID     string          `json:"goal_id"`
	Title      string          `json:"title"`
	DueAt      string          `json:"due_at"`
	Criteria   json.RawMessage `json:"criteria_json"`
	Status     string          `json:"status"`
	AchievedAt *string         `json:"achieved_at"`
	CreatedAt  string          `json:"created_at"`
	UpdatedAt  string          `json:"updated_at"`
}

// CalendarService 实现学习日历与目标里程碑用例（API 文档 7.9 / 完整设计文档 4.16）。
type CalendarService struct{ s *Services }

// CalendarGetMonthReq 月度日历请求。
type CalendarGetMonthReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	Month       string `json:"month"` // YYYY-MM
}

// CalendarEventUpsertReq 个人事件新增/更新请求（event_id 为空=新增）。
type CalendarEventUpsertReq struct {
	WorkspaceID    string  `json:"workspace_id"`
	UserID         string  `json:"user_id"`
	EventID        string  `json:"event_id"`
	Kind           string  `json:"kind"` // 仅允许 personal
	RefID          *string `json:"ref_id"`
	EventDate      string  `json:"event_date"` // YYYY-MM-DD
	StartTime      *string `json:"start_time"` // HH:MM，可选
	DurationMin    int     `json:"duration_min"`
	Title          string  `json:"title"`
	Note           string  `json:"note"`
	IdempotencyKey string  `json:"idempotency_key"`
}

// MilestoneCreateReq 里程碑创建请求。
type MilestoneCreateReq struct {
	WorkspaceID    string          `json:"workspace_id"`
	UserID         string          `json:"user_id"`
	GoalID         string          `json:"goal_id"`
	Title          string          `json:"title"`
	DueAt          string          `json:"due_at"`
	Criteria       json.RawMessage `json:"criteria_json"`
	IdempotencyKey string          `json:"idempotency_key"`
}

// MilestoneEvaluateReq 里程碑判定请求（服务端按验收条件计算达成）。
type MilestoneEvaluateReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	MilestoneID string `json:"milestone_id"`
}

// CalendarGetMonth 返回某月日历投影（任务/复习/考试/打卡/专注/个人事件）。
func (c *CalendarService) CalendarGetMonth(ctx context.Context, req CalendarGetMonthReq) (*CalendarMonth, error) {
	if err := c.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if !domain.ValidMonth(req.Month) {
		return nil, domain.InvalidArg("month 格式须为 YYYY-MM")
	}
	rows, err := c.s.Repo.ListCalendarMonthEntries(ctx, req.WorkspaceID, req.UserID, req.Month)
	if err != nil {
		return nil, err
	}
	entries := make([]*CalendarEntry, 0, len(rows))
	for _, r := range rows {
		entries = append(entries, &CalendarEntry{
			Kind: r.Kind, RefID: r.RefID, Title: r.Title,
			EventDate: r.EventDate, StartTime: r.StartTime, DurationMin: r.DurationMin,
		})
	}
	return &CalendarMonth{Month: req.Month, Entries: entries}, nil
}

// CalendarEventUpsert 新增/更新个人日历事件；kind 仅允许 personal（业务事件由投影生成）。
func (c *CalendarService) CalendarEventUpsert(ctx context.Context, req CalendarEventUpsertReq) (*CalendarEvent, error) {
	if err := c.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if req.Kind != domain.CalendarKindPersonal {
		return nil, domain.InvalidArg("日历事件仅允许手动维护 personal 类型，业务事件由系统投影生成")
	}
	if !domain.ValidDate(req.EventDate) {
		return nil, domain.InvalidArg("event_date 格式须为 YYYY-MM-DD")
	}
	if req.StartTime != nil && *req.StartTime != "" && !domain.ValidStartTime(*req.StartTime) {
		return nil, domain.InvalidArg("start_time 格式须为 HH:MM")
	}
	if req.DurationMin < 0 {
		return nil, domain.InvalidArg("duration_min 不能为负")
	}
	if strings.TrimSpace(req.Title) == "" {
		return nil, domain.InvalidArg("title 必填")
	}
	if len(req.Title) > 200 {
		return nil, domain.InvalidArg("title 长度须在 200 以内")
	}
	return withIdempotency(c.s, ctx, req.WorkspaceID, req.IdempotencyKey, "CalendarEventUpsert",
		func() (*CalendarEvent, error) {
			row := &repository.CalendarEventRow{
				WorkspaceID: req.WorkspaceID, UserID: req.UserID, Kind: domain.CalendarKindPersonal,
				RefID: req.RefID, EventDate: req.EventDate, StartTime: req.StartTime,
				DurationMin: req.DurationMin, Title: req.Title, Note: req.Note,
			}
			if req.EventID == "" {
				row.ID = NewID()
				if err := c.s.Repo.CreateCalendarEvent(ctx, row); err != nil {
					return nil, err
				}
				fresh, err := c.s.Repo.GetCalendarEvent(ctx, req.WorkspaceID, row.ID)
				if err != nil {
					return nil, err
				}
				if fresh == nil {
					return nil, domain.Conflict("日历事件写入冲突，请重试")
				}
				c.s.audit(ctx, req.WorkspaceID, "calendar_event.create", "calendar_event", row.ID,
					map[string]any{"event_date": req.EventDate, "title": req.Title})
				return calendarEventFromRow(fresh), nil
			}
			existing, err := c.s.Repo.GetCalendarEvent(ctx, req.WorkspaceID, req.EventID)
			if err != nil {
				return nil, err
			}
			if existing == nil {
				return nil, domain.NotFound("日历事件不存在")
			}
			row.ID = req.EventID
			updated, err := c.s.Repo.UpdateCalendarEvent(ctx, req.WorkspaceID, req.EventID, row)
			if err != nil {
				return nil, err
			}
			c.s.audit(ctx, req.WorkspaceID, "calendar_event.update", "calendar_event", req.EventID,
				map[string]any{"event_date": req.EventDate, "title": req.Title})
			return calendarEventFromRow(updated), nil
		})
}

// MilestoneCreate 创建目标里程碑（status 默认 pending，4.16 C3）。
func (c *CalendarService) MilestoneCreate(ctx context.Context, req MilestoneCreateReq) (*Milestone, error) {
	if err := c.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if strings.TrimSpace(req.Title) == "" || len(req.Title) > 200 {
		return nil, domain.InvalidArg("里程碑标题长度须为 1-200")
	}
	if !domain.ValidMilestoneDueAt(req.DueAt) {
		return nil, domain.InvalidArg("due_at 须为 YYYY-MM-DD 或 RFC3339 时间戳")
	}
	if _, err := domain.ParseMilestoneCriteria(req.Criteria); err != nil {
		return nil, err
	}
	goal, err := c.s.Repo.GetGoal(ctx, req.WorkspaceID, req.GoalID)
	if err != nil {
		return nil, err
	}
	if goal == nil {
		return nil, domain.NotFound("学习目标不存在")
	}
	return withIdempotency(c.s, ctx, req.WorkspaceID, req.IdempotencyKey, "MilestoneCreate",
		func() (*Milestone, error) {
			row := &repository.MilestoneRow{
				ID: NewID(), GoalID: req.GoalID, Title: strings.TrimSpace(req.Title),
				DueAt: req.DueAt, CriteriaJSON: string(req.Criteria),
			}
			if err := c.s.Repo.CreateMilestone(ctx, row); err != nil {
				return nil, err
			}
			c.s.audit(ctx, req.WorkspaceID, "milestone.create", "goal_milestone", row.ID,
				map[string]any{"goal_id": req.GoalID, "due_at": req.DueAt})
			return c.milestoneByID(ctx, req.WorkspaceID, row.ID)
		})
}

// MilestoneEvaluate 服务端按验收条件判定里程碑达成（题量达标且正确率达标；任务完成数达标）。
// 已判定（非 pending）幂等返回既有结果，不重复判定。
func (c *CalendarService) MilestoneEvaluate(ctx context.Context, req MilestoneEvaluateReq) (*Milestone, error) {
	if err := c.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	cur, err := c.s.Repo.GetMilestone(ctx, req.WorkspaceID, req.MilestoneID)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, domain.NotFound("里程碑不存在")
	}
	if cur.Status != domain.MilestoneStatusPending {
		return milestoneFromRow(cur), nil
	}
	criteria, err := domain.ParseMilestoneCriteria(json.RawMessage(cur.CriteriaJSON))
	if err != nil {
		return nil, err
	}
	goal, err := c.s.Repo.GetGoal(ctx, req.WorkspaceID, cur.GoalID)
	if err != nil {
		return nil, err
	}
	if goal == nil {
		return nil, domain.NotFound("学习目标不存在")
	}
	facts, err := c.s.Repo.GetMilestoneFacts(ctx, req.WorkspaceID, goal.UserID, cur.GoalID)
	if err != nil {
		return nil, err
	}
	achieved := c.checkMilestoneMet(criteria, facts)
	status := domain.MilestoneStatusNotMet
	var achievedAt *string
	if achieved {
		status = domain.MilestoneStatusAchieved
		at := Now()
		achievedAt = &at
	}
	updated, err := c.s.Repo.UpdateMilestoneResult(ctx, cur.ID, status, achievedAt)
	if err != nil {
		return nil, err
	}
	c.s.audit(ctx, req.WorkspaceID, "milestone.evaluate", "goal_milestone", cur.ID,
		map[string]any{"status": status, "submission_count": facts.SubmissionCount,
			"accuracy": facts.Accuracy, "completed_tasks": facts.CompletedTasks})
	return milestoneFromRow(updated), nil
}

// checkMilestoneMet 判定验收条件是否达成（4.16 C3）。
func (c *CalendarService) checkMilestoneMet(criteria *domain.MilestoneCriteria, facts *repository.MilestoneFacts) bool {
	switch criteria.Type {
	case domain.MilestoneCriteriaPractice:
		if facts.SubmissionCount < criteria.Count {
			return false
		}
		if criteria.MinAccuracy != nil {
			if facts.Accuracy == nil || *facts.Accuracy < *criteria.MinAccuracy {
				return false
			}
		}
		return true
	case domain.MilestoneCriteriaTasks:
		return facts.CompletedTasks >= criteria.Count
	}
	return false
}

// milestoneByID 读取里程碑并映射 DTO；不存在返回 NOT_FOUND。
func (c *CalendarService) milestoneByID(ctx context.Context, wsID, id string) (*Milestone, error) {
	row, err := c.s.Repo.GetMilestone(ctx, wsID, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, domain.NotFound("里程碑不存在")
	}
	return milestoneFromRow(row), nil
}

func calendarEventFromRow(r *repository.CalendarEventRow) *CalendarEvent {
	return &CalendarEvent{
		ID: r.ID, WorkspaceID: r.WorkspaceID, UserID: r.UserID, Kind: r.Kind,
		RefID: r.RefID, EventDate: r.EventDate, StartTime: r.StartTime,
		DurationMin: r.DurationMin, Title: r.Title, Note: r.Note,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func milestoneFromRow(r *repository.MilestoneRow) *Milestone {
	return &Milestone{
		ID: r.ID, GoalID: r.GoalID, Title: r.Title, DueAt: r.DueAt,
		Criteria: json.RawMessage(r.CriteriaJSON), Status: r.Status,
		AchievedAt: r.AchievedAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}
