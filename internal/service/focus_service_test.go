package service

import (
	"testing"
	"time"

	"lumo/internal/domain"
)

// ---- 辅助 ----

// focusFixedNow 固定时钟：返回 setNow 闭包捕获的当前时间（测试推进时间用）。
func focusFixedNow() (*time.Time, func() time.Time) {
	t := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	return &t, func() time.Time { return t }
}

// focusUsageCount 统计用户 focus 类型 usage_events 条数（事实断言：每结束一个会话一条）。
func focusUsageCount(t *testing.T, s *Services, userID string) int {
	t.Helper()
	n, err := s.Repo.CountUsageEventsByType(ctx(), userID, "focus")
	if err != nil {
		t.Fatalf("count focus usage_events: %v", err)
	}
	return n
}

// ---- 场景 1：番茄钟 happy path（25 分钟 → completed，统计正确，幂等键重放） ----

func TestTimerStartEndPomodoroCompleted(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	now, clock := focusFixedNow()
	s.Focus.Now = clock

	key := "fm-" + NewID()
	started, err := s.Focus.TimerStart(ctx(), TimerStartReq{
		WorkspaceID: ws.ID, UserID: userID, Mode: domain.TimerModePomodoro,
		PlannedMinutes: 25, IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("timer start: %v", err)
	}
	if started.Mode != domain.TimerModePomodoro || started.PlannedMinutes != 25 || started.StartedAt == nil {
		t.Fatalf("unexpected session: %+v", started)
	}
	if focusUsageCount(t, s, userID) != 0 {
		t.Fatalf("expected 0 usage_events while active, got %d", focusUsageCount(t, s, userID))
	}

	// 相同幂等键重放：返回同一会话、不新建
	replay, err := s.Focus.TimerStart(ctx(), TimerStartReq{
		WorkspaceID: ws.ID, UserID: userID, Mode: domain.TimerModePomodoro,
		PlannedMinutes: 25, IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("timer start replay: %v", err)
	}
	if replay.ID != started.ID {
		t.Fatalf("idempotency violated: %s != %s", replay.ID, started.ID)
	}

	*now = now.Add(25 * time.Minute)
	ended, err := s.Focus.TimerEnd(ctx(), TimerEndReq{
		WorkspaceID: ws.ID, UserID: userID, SessionID: started.ID,
	})
	if err != nil {
		t.Fatalf("timer end: %v", err)
	}
	if ended.Status != domain.TimerStatusCompleted {
		t.Fatalf("expected completed, got %s", ended.Status)
	}
	if ended.ActualSeconds != 1500 {
		t.Fatalf("expected actual_seconds=1500, got %d", ended.ActualSeconds)
	}

	// 重复结束：幂等返回已有终态，不重复写 usage_events
	if _, err := s.Focus.TimerEnd(ctx(), TimerEndReq{
		WorkspaceID: ws.ID, UserID: userID, SessionID: started.ID,
	}); err != nil {
		t.Fatalf("repeat end should not error: %v", err)
	}

	stats, err := s.Focus.TimerStats(ctx(), TimerStatsReq{WorkspaceID: ws.ID, UserID: userID})
	if err != nil {
		t.Fatalf("timer stats: %v", err)
	}
	if stats.TotalSessions != 1 || stats.TotalSeconds != 1500 {
		t.Fatalf("unexpected totals: %+v", stats)
	}
	if stats.CompletedSessions != 1 || stats.InterruptedSessions != 0 || stats.AbandonedSessions != 0 {
		t.Fatalf("unexpected status counts: %+v", stats)
	}
	if stats.InterruptionRate != 0 {
		t.Fatalf("expected interruption_rate=0, got %f", stats.InterruptionRate)
	}
	if focusUsageCount(t, s, userID) != 1 {
		t.Fatalf("expected 1 usage_events, got %d", focusUsageCount(t, s, userID))
	}
}

// ---- 场景 2：单活动计时（第二个未结束的 TimerStart → INVALID_STATE） ----

func TestTimerStartSingleActive(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	_, clock := focusFixedNow()
	s.Focus.Now = clock

	if _, err := s.Focus.TimerStart(ctx(), TimerStartReq{
		WorkspaceID: ws.ID, UserID: userID, Mode: domain.TimerModePomodoro,
		PlannedMinutes: 25, IdempotencyKey: "fm-" + NewID(),
	}); err != nil {
		t.Fatalf("first start: %v", err)
	}

	_, err := s.Focus.TimerStart(ctx(), TimerStartReq{
		WorkspaceID: ws.ID, UserID: userID, Mode: domain.TimerModePomodoro,
		PlannedMinutes: 25, IdempotencyKey: "fm-" + NewID(),
	})
	if err == nil {
		t.Fatal("expected INVALID_STATE on second start while active")
	}
	if domain.AsError(err).Code != domain.CodeInvalidState {
		t.Fatalf("expected INVALID_STATE, got %s", domain.AsError(err).Code)
	}
}

// ---- 场景 3：惰性超时回收（过期的活动会话在下次 TimerStart 自动归档后放行） ----

func TestTimerStartLazyTimeoutRecovery(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	now, clock := focusFixedNow()
	s.Focus.Now = clock

	first, err := s.Focus.TimerStart(ctx(), TimerStartReq{
		WorkspaceID: ws.ID, UserID: userID, Mode: domain.TimerModePomodoro,
		PlannedMinutes: 25, IdempotencyKey: "fm-" + NewID(),
	})
	if err != nil {
		t.Fatalf("first start: %v", err)
	}

	// 推进 30 分钟（已超 25 分钟计划）→ 下次 TimerStart 先归档旧会话再新建
	*now = now.Add(30 * time.Minute)
	second, err := s.Focus.TimerStart(ctx(), TimerStartReq{
		WorkspaceID: ws.ID, UserID: userID, Mode: domain.TimerModePomodoro,
		PlannedMinutes: 25, IdempotencyKey: "fm-" + NewID(),
	})
	if err != nil {
		t.Fatalf("second start should recover stale session: %v", err)
	}
	if second.ID == first.ID {
		t.Fatal("expected a NEW session after recovery")
	}

	// 旧会话已被归档为 completed（时长已到）；重复结束返回既有终态
	old, err := s.Focus.TimerEnd(ctx(), TimerEndReq{WorkspaceID: ws.ID, UserID: userID, SessionID: first.ID})
	if err != nil {
		t.Fatalf("end recovered old session: %v", err)
	}
	if old.Status != domain.TimerStatusCompleted {
		t.Fatalf("expected old session completed, got %s", old.Status)
	}
	if old.ActualSeconds != 1800 {
		t.Fatalf("expected old session actual_seconds=1800, got %d", old.ActualSeconds)
	}

	// 新会话保持活动态
	active, err := s.Repo.GetActiveTimerSession(ctx(), userID)
	if err != nil {
		t.Fatalf("get active: %v", err)
	}
	if active == nil || active.ID != second.ID {
		t.Fatalf("expected active = second session, got %+v", active)
	}
	// 归档的旧会话写入一条 usage_events
	if focusUsageCount(t, s, userID) != 1 {
		t.Fatalf("expected 1 usage_events for recovered stale session, got %d", focusUsageCount(t, s, userID))
	}
}

// ---- 场景 4：中断（提前结束并带原因 → interrupted，计入中断率） ----

func TestTimerEndInterrupt(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	now, clock := focusFixedNow()
	s.Focus.Now = clock

	started, err := s.Focus.TimerStart(ctx(), TimerStartReq{
		WorkspaceID: ws.ID, UserID: userID, Mode: domain.TimerModePomodoro,
		PlannedMinutes: 25, IdempotencyKey: "fm-" + NewID(),
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	*now = now.Add(5 * time.Minute)
	ended, err := s.Focus.TimerEnd(ctx(), TimerEndReq{
		WorkspaceID: ws.ID, UserID: userID, SessionID: started.ID, InterruptReason: "rest",
	})
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	if ended.Status != domain.TimerStatusInterrupted {
		t.Fatalf("expected interrupted, got %s", ended.Status)
	}
	if ended.InterruptReason == nil || *ended.InterruptReason != "rest" {
		t.Fatalf("expected interrupt_reason=rest, got %+v", ended.InterruptReason)
	}

	stats, err := s.Focus.TimerStats(ctx(), TimerStatsReq{WorkspaceID: ws.ID, UserID: userID})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.InterruptedSessions != 1 || stats.TotalSessions != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if stats.InterruptionRate != 1 {
		t.Fatalf("expected interruption_rate=1, got %f", stats.InterruptionRate)
	}
}

// ---- 场景 5：放弃（提前结束且无原因 → abandoned，计入中断率） ----

func TestTimerEndAbandoned(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	now, clock := focusFixedNow()
	s.Focus.Now = clock

	started, err := s.Focus.TimerStart(ctx(), TimerStartReq{
		WorkspaceID: ws.ID, UserID: userID, Mode: domain.TimerModePomodoro,
		PlannedMinutes: 25, IdempotencyKey: "fm-" + NewID(),
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	*now = now.Add(5 * time.Minute)
	ended, err := s.Focus.TimerEnd(ctx(), TimerEndReq{
		WorkspaceID: ws.ID, UserID: userID, SessionID: started.ID,
	})
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	if ended.Status != domain.TimerStatusAbandoned {
		t.Fatalf("expected abandoned, got %s", ended.Status)
	}

	stats, err := s.Focus.TimerStats(ctx(), TimerStatsReq{WorkspaceID: ws.ID, UserID: userID})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.AbandonedSessions != 1 || stats.TotalSessions != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if stats.InterruptionRate != 1 {
		t.Fatalf("expected interruption_rate=1, got %f", stats.InterruptionRate)
	}
}

// ---- 场景 6：usage_events 每会话一条 ----

func TestTimerUsageEventsPerSession(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	now, clock := focusFixedNow()
	s.Focus.Now = clock

	for i := 0; i < 2; i++ {
		started, err := s.Focus.TimerStart(ctx(), TimerStartReq{
			WorkspaceID: ws.ID, UserID: userID, Mode: domain.TimerModePomodoro,
			PlannedMinutes: 25, IdempotencyKey: "fm-" + NewID(),
		})
		if err != nil {
			t.Fatalf("start %d: %v", i+1, err)
		}
		*now = now.Add(25 * time.Minute)
		if _, err := s.Focus.TimerEnd(ctx(), TimerEndReq{
			WorkspaceID: ws.ID, UserID: userID, SessionID: started.ID,
		}); err != nil {
			t.Fatalf("end %d: %v", i+1, err)
		}
	}
	if n := focusUsageCount(t, s, userID); n != 2 {
		t.Fatalf("expected 2 focus usage_events, got %d", n)
	}
	stats, err := s.Focus.TimerStats(ctx(), TimerStatsReq{WorkspaceID: ws.ID, UserID: userID})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.TotalSessions != 2 || stats.TotalSeconds != 3000 {
		t.Fatalf("unexpected stats after 2 sessions: %+v", stats)
	}
}

// ---- 场景 7：参数校验 ----

func TestTimerStartValidation(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	_, clock := focusFixedNow()
	s.Focus.Now = clock

	cases := []struct {
		name  string
		req   TimerStartReq
		code  domain.ErrorCode
	}{
		{"非法模式", TimerStartReq{WorkspaceID: ws.ID, UserID: userID, Mode: "deep", PlannedMinutes: 25, IdempotencyKey: "fm-1"}, domain.CodeInvalidArgument},
		{"负数时长", TimerStartReq{WorkspaceID: ws.ID, UserID: userID, Mode: domain.TimerModePomodoro, PlannedMinutes: -1, IdempotencyKey: "fm-2"}, domain.CodeInvalidArgument},
		{"番茄钟 0 分钟", TimerStartReq{WorkspaceID: ws.ID, UserID: userID, Mode: domain.TimerModePomodoro, PlannedMinutes: 0, IdempotencyKey: "fm-3"}, domain.CodeInvalidArgument},
		{"番茄钟超上限", TimerStartReq{WorkspaceID: ws.ID, UserID: userID, Mode: domain.TimerModePomodoro, PlannedMinutes: 121, IdempotencyKey: "fm-4"}, domain.CodeInvalidArgument},
		{"缺幂等键", TimerStartReq{WorkspaceID: ws.ID, UserID: userID, Mode: domain.TimerModePomodoro, PlannedMinutes: 25}, domain.CodeInvalidArgument},
		{"缺用户", TimerStartReq{WorkspaceID: ws.ID, Mode: domain.TimerModePomodoro, PlannedMinutes: 25, IdempotencyKey: "fm-5"}, domain.CodeInvalidArgument},
	}
	for _, c := range cases {
		_, err := s.Focus.TimerStart(ctx(), c.req)
		if err == nil {
			t.Fatalf("%s: expected error", c.name)
		}
		if domain.AsError(err).Code != c.code {
			t.Fatalf("%s: expected %s, got %s", c.name, c.code, domain.AsError(err).Code)
		}
	}
}

// ---- 场景 8：自由计时（planned_minutes=0 不限时 + 负数拒绝 + 日期范围统计） ----

func TestTimerFreeModeAndStatsRange(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	now, clock := focusFixedNow()
	s.Focus.Now = clock

	// 自由计时 0 分钟（不限时）可开始；负数拒绝
	if _, err := s.Focus.TimerStart(ctx(), TimerStartReq{
		WorkspaceID: ws.ID, UserID: userID, Mode: domain.TimerModeFree,
		PlannedMinutes: 0, IdempotencyKey: "fm-" + NewID(),
	}); err != nil {
		t.Fatalf("free untimed start: %v", err)
	}
	if _, err := s.Focus.TimerStart(ctx(), TimerStartReq{
		WorkspaceID: ws.ID, UserID: userID, Mode: domain.TimerModeFree,
		PlannedMinutes: -3, IdempotencyKey: "fm-" + NewID(),
	}); err == nil {
		t.Fatal("expected INVALID_ARGUMENT for negative free minutes")
	}

	// 第 1 天会话
	first, err := s.Repo.GetActiveTimerSession(ctx(), userID)
	if err != nil {
		t.Fatalf("get active: %v", err)
	}
	*now = now.Add(10 * time.Minute)
	if _, err := s.Focus.TimerEnd(ctx(), TimerEndReq{
		WorkspaceID: ws.ID, UserID: userID, SessionID: first.ID, InterruptReason: "rest",
	}); err != nil {
		t.Fatalf("end free session: %v", err)
	}

	// 第 2 天会话
	*now = now.AddDate(0, 0, 1)
	second, err := s.Focus.TimerStart(ctx(), TimerStartReq{
		WorkspaceID: ws.ID, UserID: userID, Mode: domain.TimerModePomodoro,
		PlannedMinutes: 25, IdempotencyKey: "fm-" + NewID(),
	})
	if err != nil {
		t.Fatalf("day2 start: %v", err)
	}
	*now = now.Add(25 * time.Minute)
	if _, err := s.Focus.TimerEnd(ctx(), TimerEndReq{
		WorkspaceID: ws.ID, UserID: userID, SessionID: second.ID,
	}); err != nil {
		t.Fatalf("day2 end: %v", err)
	}

	// 全部时间：2 次
	all, err := s.Focus.TimerStats(ctx(), TimerStatsReq{WorkspaceID: ws.ID, UserID: userID})
	if err != nil {
		t.Fatalf("stats all: %v", err)
	}
	if all.TotalSessions != 2 {
		t.Fatalf("expected 2 sessions all-time, got %d", all.TotalSessions)
	}

	// 仅第 1 天：1 次（免费会话 600s 中断）
	day1, err := s.Focus.TimerStats(ctx(), TimerStatsReq{
		WorkspaceID: ws.ID, UserID: userID, StartDate: "2026-08-06", EndDate: "2026-08-06",
	})
	if err != nil {
		t.Fatalf("stats day1: %v", err)
	}
	if day1.TotalSessions != 1 || day1.TotalSeconds != 600 || day1.InterruptedSessions != 1 {
		t.Fatalf("unexpected day1 stats: %+v", day1)
	}
	if day1.InterruptionRate != 1 {
		t.Fatalf("expected day1 rate=1, got %f", day1.InterruptionRate)
	}

	// 非法日期 → INVALID_ARGUMENT
	if _, err := s.Focus.TimerStats(ctx(), TimerStatsReq{
		WorkspaceID: ws.ID, UserID: userID, StartDate: "2026/08/06",
	}); err == nil {
		t.Fatal("expected INVALID_ARGUMENT for bad start_date")
	} else if domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("expected INVALID_ARGUMENT, got %s", domain.AsError(err).Code)
	}
}
