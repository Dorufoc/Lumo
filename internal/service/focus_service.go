package service

import (
	"context"
	"math"
	"time"

	"lumo/internal/domain"
	"lumo/internal/repository"
)

// FocusService 实现专注计时用例（API 文档 7.6 / 完整设计文档 4.13）。
// Now 可注入时钟：测试中推进时间触发完成判定 / 惰性超时回收。
type FocusService struct {
	s   *Services
	Now func() time.Time
}

// TimerStartReq 开始专注计时请求（API 文档 7.6 TimerStart）。
type TimerStartReq struct {
	WorkspaceID    string `json:"workspace_id"`
	UserID         string `json:"user_id"`
	Mode           string `json:"mode"`
	PlannedMinutes int    `json:"planned_minutes"`
	TaskID         string `json:"task_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

// TimerEndReq 结束专注计时请求（API 文档 7.6 TimerEnd）。
type TimerEndReq struct {
	WorkspaceID     string `json:"workspace_id"`
	UserID          string `json:"user_id"`
	SessionID       string `json:"session_id"`
	InterruptReason string `json:"interrupt_reason"`
}

// TimerStatsReq 专注统计请求（start_date/end_date 为 YYYY-MM-DD，均留空 = 全部时间）。
type TimerStatsReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	StartDate   string `json:"start_date"`
	EndDate     string `json:"end_date"`
}

// TimerStats 是专注统计聚合（4.13 T5：总时长/轮次/中断率，供 Dashboard/Reports/Calendar 引用）。
type TimerStats struct {
	TotalSessions       int     `json:"total_sessions"`
	TotalSeconds        int     `json:"total_seconds"`
	CompletedSessions   int     `json:"completed_sessions"`
	InterruptedSessions int     `json:"interrupted_sessions"`
	AbandonedSessions   int     `json:"abandoned_sessions"`
	InterruptionRate    float64 `json:"interruption_rate"`
}

// TimerStart 开始一次专注计时（单活动约束：已有未结束计时 → INVALID_STATE，见 doStart）。
func (f *FocusService) TimerStart(ctx context.Context, req TimerStartReq) (*domain.TimerSession, error) {
	if err := f.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if !domain.ValidTimerMode(req.Mode) {
		return nil, domain.InvalidArg("mode 须为 pomodoro|free")
	}
	if err := domain.ValidateTimerPlannedMinutes(req.Mode, req.PlannedMinutes); err != nil {
		return nil, err
	}
	if req.IdempotencyKey == "" {
		return nil, domain.InvalidArg("idempotency_key 必填")
	}
	return withIdempotency(f.s, ctx, req.WorkspaceID, req.IdempotencyKey, "TimerStart",
		func() (*domain.TimerSession, error) {
			return f.doStart(ctx, req.WorkspaceID, req.UserID, req.Mode, req.PlannedMinutes, req.TaskID)
		})
}

// doStart 执行开始：先处理单活动约束（惰性超时回收），再写入会话。
func (f *FocusService) doStart(ctx context.Context, wsID, userID, mode string, plannedMinutes int, taskID string) (*domain.TimerSession, error) {
	now := f.Now()
	active, err := f.s.Repo.GetActiveTimerSession(ctx, userID)
	if err != nil {
		return nil, err
	}
	if active != nil {
		// 单活动计时约束（4.13：同一用户同一时间只允许一个活动计时；跨设备以最早开始者为准）。
		// 惰性超时回收（设计 4.13「应用退出时未结束的计时」）：活动会话的计划时长已到
		// （now - started_at ≥ planned），说明上次计时早已走完却被残留——先按状态规则自动归档
		// （写 ended_at + actual_seconds + status + usage_events），再放行新计时；
		// 否则拒绝（INVALID_STATE）并附活动会话详情供前端恢复。
		if f.timeoutElapsed(active, now) {
			if err := f.finalizeStale(ctx, wsID, active, now); err != nil {
				return nil, err
			}
		} else {
			e := domain.InvalidState("已有进行中的专注计时（session_id=%s），请先结束再开始", active.ID)
			e.Details = map[string]any{"session_id": active.ID}
			if active.StartedAt != nil {
				e.Details["started_at"] = *active.StartedAt
			}
			return nil, e
		}
	}
	id := NewID()
	var taskIDPtr *string
	if taskID != "" {
		taskIDPtr = &taskID
	}
	startedAt := now.UTC().Format(time.RFC3339)
	session := &domain.TimerSession{
		ID: id, WorkspaceID: wsID, UserID: userID,
		Mode: mode, PlannedMinutes: plannedMinutes, ActualSeconds: 0,
		TaskID: taskIDPtr, StartedAt: &startedAt,
	}
	if err := f.s.Repo.CreateTimerSession(ctx, session); err != nil {
		return nil, err
	}
	fresh, err := f.s.Repo.GetTimerSession(ctx, wsID, id)
	if err != nil {
		return nil, err
	}
	if fresh == nil {
		return nil, domain.Conflict("专注计时写入冲突，请重试")
	}
	f.s.audit(ctx, wsID, "focus.start", "timer_session", id,
		map[string]any{"mode": mode, "planned_minutes": plannedMinutes, "task_id": taskID})
	return fresh, nil
}

// TimerEnd 结束一次专注计时：写 ended_at + actual_seconds + 终态 + usage_events。
// 已结束会话幂等返回既有终态（不重复写事件、不改变状态）——设计决策：结束天然幂等，
// 重复调用无需 CONFLICT（返回现有结果对前端更友好）。
func (f *FocusService) TimerEnd(ctx context.Context, req TimerEndReq) (*domain.TimerSession, error) {
	if err := f.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if req.SessionID == "" {
		return nil, domain.InvalidArg("session_id 必填")
	}
	session, err := f.s.Repo.GetTimerSession(ctx, req.WorkspaceID, req.SessionID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, domain.NotFound("专注计时不存在")
	}
	if session.UserID != req.UserID {
		return nil, domain.NotFound("专注计时不存在")
	}
	if session.EndedAt != nil {
		return session, nil
	}
	now := f.Now()
	elapsed, err := elapsedSeconds(session, now)
	if err != nil {
		return nil, err
	}
	status := domain.TimerStatusFor(session.PlannedMinutes, elapsed, req.InterruptReason)
	endedAt := now.UTC().Format(time.RFC3339)
	ok, err := f.s.Repo.EndTimerSession(ctx, session.ID, endedAt, elapsed, status, req.InterruptReason)
	if err != nil {
		return nil, err
	}
	if !ok {
		// 并发结束竞争：读回既有终态返回。
		return f.s.Repo.GetTimerSession(ctx, req.WorkspaceID, session.ID)
	}
	if err := f.writeFocusUsage(ctx, req.WorkspaceID, session, elapsed, status, req.InterruptReason); err != nil {
		return nil, err
	}
	f.s.audit(ctx, req.WorkspaceID, "focus.end", "timer_session", session.ID,
		map[string]any{"mode": session.Mode, "planned_minutes": session.PlannedMinutes,
			"actual_seconds": elapsed, "status": status, "interrupt_reason": req.InterruptReason})
	return f.s.Repo.GetTimerSession(ctx, req.WorkspaceID, session.ID)
}

// TimerStats 聚合专注统计（日期范围含边界，均留空 = 全部时间）。
func (f *FocusService) TimerStats(ctx context.Context, req TimerStatsReq) (*TimerStats, error) {
	if err := f.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	var start, end *string
	if req.StartDate != "" {
		if !domain.ValidDate(req.StartDate) {
			return nil, domain.InvalidArg("start_date 格式须为 YYYY-MM-DD")
		}
		start = &req.StartDate
	}
	if req.EndDate != "" {
		if !domain.ValidDate(req.EndDate) {
			return nil, domain.InvalidArg("end_date 格式须为 YYYY-MM-DD")
		}
		end = &req.EndDate
	}
	agg, err := f.s.Repo.AggregateTimerStats(ctx, req.UserID, start, end)
	if err != nil {
		return nil, err
	}
	rate := 0.0
	if agg.TotalSessions > 0 {
		rate = float64(agg.InterruptedSessions+agg.AbandonedSessions) / float64(agg.TotalSessions)
	}
	rate = math.Round(rate*10000) / 10000
	return &TimerStats{
		TotalSessions:       agg.TotalSessions,
		TotalSeconds:        agg.TotalSeconds,
		CompletedSessions:   agg.CompletedSessions,
		InterruptedSessions: agg.InterruptedSessions,
		AbandonedSessions:   agg.AbandonedSessions,
		InterruptionRate:    rate,
	}, nil
}

// timeoutElapsed 判断活动会话是否已超出计划时长（now - started_at ≥ planned）。
func (f *FocusService) timeoutElapsed(active *domain.TimerSession, now time.Time) bool {
	if active.PlannedMinutes <= 0 || active.StartedAt == nil {
		return false
	}
	started, err := time.Parse(time.RFC3339, *active.StartedAt)
	if err != nil {
		return false
	}
	return int(now.Sub(started).Seconds()) >= active.PlannedMinutes*60
}

// finalizeStale 惰性归档一个已逾时的活动会话：写 ended_at/actual_seconds/终态 + usage_events + 审计。
// 终态按 TimerStatusFor 判定（已达计划时长 → completed）；幂等：并发下 EndTimerSession 已归档则跳过。
func (f *FocusService) finalizeStale(ctx context.Context, wsID string, active *domain.TimerSession, now time.Time) error {
	elapsed, err := elapsedSeconds(active, now)
	if err != nil {
		return err
	}
	status := domain.TimerStatusFor(active.PlannedMinutes, elapsed, "")
	endedAt := now.UTC().Format(time.RFC3339)
	ok, err := f.s.Repo.EndTimerSession(ctx, active.ID, endedAt, elapsed, status, "")
	if err != nil {
		return err
	}
	if !ok {
		return nil // 并发下已被其他请求归档
	}
	if err := f.writeFocusUsage(ctx, wsID, active, elapsed, status, ""); err != nil {
		return err
	}
	f.s.audit(ctx, wsID, "focus.timeout", "timer_session", active.ID,
		map[string]any{"mode": active.Mode, "planned_minutes": active.PlannedMinutes,
			"actual_seconds": elapsed, "status": status})
	return nil
}

// elapsedSeconds 计算会话已进行的秒数（now - started_at，非负）。
func elapsedSeconds(s *domain.TimerSession, now time.Time) (int, error) {
	if s.StartedAt == nil {
		return 0, domain.NewError(domain.CodeInternal, "专注计时缺少 started_at")
	}
	started, err := time.Parse(time.RFC3339, *s.StartedAt)
	if err != nil {
		return 0, domain.NewError(domain.CodeInternal, "专注计时 started_at 非法")
	}
	elapsed := int(now.Sub(started).Seconds())
	if elapsed < 0 {
		elapsed = 0
	}
	return elapsed, nil
}

// writeFocusUsage 每次结束/归档的专注会话写入一条 usage_events（event_type='focus'，只追加防篡改）。
func (f *FocusService) writeFocusUsage(ctx context.Context, wsID string, s *domain.TimerSession, actualSeconds int, status, interruptReason string) error {
	return f.s.Repo.CreateUsageEvent(ctx, &repository.UsageEventRow{
		ID: NewID(), WorkspaceID: wsID, UserID: s.UserID,
		EventType: "focus",
		PayloadJSON: repository.MarshalJSON(map[string]any{
			"session_id": s.ID, "mode": s.Mode, "planned_minutes": s.PlannedMinutes,
			"actual_seconds": actualSeconds, "status": status, "interrupt_reason": interruptReason,
		}),
		OccurredAt: Now(),
	})
}
