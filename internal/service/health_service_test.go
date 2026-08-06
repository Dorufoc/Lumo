package service

import (
	"testing"
	"time"

	"lumo/internal/domain"
)

// ---- 辅助 ----

// healthFixedNow 固定时钟：返回 setNow 闭包捕获的当前时间（测试推进时间用）。
func healthFixedNow() (*time.Time, func() time.Time) {
	t := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	return &t, func() time.Time { return t }
}

// seedHealthSession 直写一条已结束专注会话（start/end 为 RFC3339 UTC）。
func seedHealthSession(t *testing.T, s *Services, wsID, userID, start, end string) {
	t.Helper()
	startPtr := start
	sess := &domain.TimerSession{
		ID: NewID(), WorkspaceID: wsID, UserID: userID,
		Mode: domain.TimerModeFree, PlannedMinutes: 0, StartedAt: &startPtr,
	}
	if err := s.Repo.CreateTimerSession(ctx(), sess); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.Repo.EndTimerSession(ctx(), sess.ID, end, 0, domain.TimerStatusAbandoned, ""); err != nil {
		t.Fatalf("end session: %v", err)
	}
}

// ---- 场景 1：HealthSettingsUpdate 创建 + 更新（upsert 回读，created_at/updated_at 非空） ----

func TestHealthSettingsUpdateCreateReadBack(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)

	got, err := s.Health.HealthSettingsUpdate(ctx(), HealthSettingsUpdateReq{
		WorkspaceID: ws.ID, UserID: userID,
		SedentaryEnabled: true, EyeEnabled: true, NightMode: domain.NightModeAuto,
		BlueLightFilter: false, StatsEnabled: true,
	})
	if err != nil {
		t.Fatalf("update create: %v", err)
	}
	if !got.SedentaryEnabled || !got.EyeEnabled || got.NightMode != domain.NightModeAuto {
		t.Fatalf("unexpected settings after create: %+v", got)
	}
	if got.BlueLightFilter || !got.StatsEnabled {
		t.Fatalf("unexpected toggles after create: %+v", got)
	}
	if got.UpdatedAt == "" || got.WorkspaceID != ws.ID || got.UserID != userID {
		t.Fatalf("expected workspace/user ids + updated_at, got %+v", got)
	}

	// 更新：换值 + 关闭统计 → 回读新值，updated_at 更新，仍一行
	got, err = s.Health.HealthSettingsUpdate(ctx(), HealthSettingsUpdateReq{
		WorkspaceID: ws.ID, UserID: userID,
		SedentaryEnabled: false, EyeEnabled: false, NightMode: domain.NightModeDark,
		BlueLightFilter: true, StatsEnabled: false,
	})
	if err != nil {
		t.Fatalf("update v2: %v", err)
	}
	if got.SedentaryEnabled || got.EyeEnabled || got.NightMode != domain.NightModeDark {
		t.Fatalf("unexpected settings after update: %+v", got)
	}
	if !got.BlueLightFilter || got.StatsEnabled {
		t.Fatalf("unexpected toggles after update: %+v", got)
	}

	var n int
	if err := s.Repo.DB().QueryRowContext(ctx(),
		`SELECT COUNT(*) FROM health_settings WHERE workspace_id = ? AND user_id = ?`, ws.ID, userID).Scan(&n); err != nil {
		t.Fatalf("count health_settings: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 health_settings row, got %d", n)
	}
}

// ---- 场景 2：HealthSettingsUpdate 校验（非法 night_mode / 缺 user_id / 工作区不存在） ----

func TestHealthSettingsUpdateValidation(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)

	cases := []struct {
		name string
		req  HealthSettingsUpdateReq
		code domain.ErrorCode
	}{
		{"非法 night_mode", HealthSettingsUpdateReq{WorkspaceID: ws.ID, UserID: userID, NightMode: "sepia"}, domain.CodeInvalidArgument},
		{"缺 user_id", HealthSettingsUpdateReq{WorkspaceID: ws.ID, NightMode: domain.NightModeAuto}, domain.CodeInvalidArgument},
		{"工作区不存在", HealthSettingsUpdateReq{WorkspaceID: "no-such-workspace", UserID: userID, NightMode: domain.NightModeAuto}, domain.CodeNotFound},
	}
	for _, c := range cases {
		_, err := s.Health.HealthSettingsUpdate(ctx(), c.req)
		if err == nil {
			t.Fatalf("%s: expected error", c.name)
		}
		if domain.AsError(err).Code != c.code {
			t.Fatalf("%s: expected %s, got %s", c.name, c.code, domain.AsError(err).Code)
		}
	}
}

// ---- 场景 3：HealthSettingsUpdate 同步久坐提醒（开启→创建 enabled；关闭→禁用） ----

func TestHealthSettingsUpdateSyncsSedentaryReminder(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	_, clock := healthFixedNow()
	s.Health.Now = clock
	s.Reminder.Now = clock

	// 开启久坐提醒 → 自动 upsert kind=health 提醒（enabled=1，interval 45）
	if _, err := s.Health.HealthSettingsUpdate(ctx(), HealthSettingsUpdateReq{
		WorkspaceID: ws.ID, UserID: userID, SedentaryEnabled: true,
		NightMode: domain.NightModeAuto,
	}); err != nil {
		t.Fatalf("enable sedentary: %v", err)
	}
	rem, err := s.Repo.GetReminder(ctx(), userID, domain.ReminderKindHealth)
	if err != nil {
		t.Fatalf("get health reminder: %v", err)
	}
	if rem == nil || rem.Enabled != 1 {
		t.Fatalf("expected enabled health reminder, got %+v", rem)
	}
	if rem.RuleJSON != healthRuleInterval45 {
		t.Fatalf("expected rule %s, got %s", healthRuleInterval45, rem.RuleJSON)
	}
	if rem.NextTriggerAt == "" {
		t.Fatal("expected next_trigger_at set")
	}

	// 关闭久坐提醒 → 同一行 enabled=0
	if _, err := s.Health.HealthSettingsUpdate(ctx(), HealthSettingsUpdateReq{
		WorkspaceID: ws.ID, UserID: userID, SedentaryEnabled: false,
		NightMode: domain.NightModeAuto,
	}); err != nil {
		t.Fatalf("disable sedentary: %v", err)
	}
	rem, err = s.Repo.GetReminder(ctx(), userID, domain.ReminderKindHealth)
	if err != nil {
		t.Fatalf("get health reminder after disable: %v", err)
	}
	if rem == nil || rem.Enabled != 0 {
		t.Fatalf("expected disabled health reminder, got %+v", rem)
	}
	if reminderRowCount(t, s, userID, domain.ReminderKindHealth) != 1 {
		t.Fatalf("expected exactly 1 health reminder row")
	}
}

// ---- 场景 4：HealthStatsGet 统计（久坐次数 + 休息完成率，仅开启时采集） ----

func TestHealthStatsGetCountsAndRate(t *testing.T) {
	cases := []struct {
		name          string
		sessions      [][2]string // start/end RFC3339
		wantCount     int
		wantRate      float64
	}{
		{
			"单窗口 50 分钟（已休息）",
			[][2]string{{"2026-08-06T09:00:00Z", "2026-08-06T09:50:00Z"}},
			1, 1.0,
		},
		{
			"两段连续 50 分钟（跨阈值后追加 → 未休息）",
			[][2]string{{"2026-08-06T09:00:00Z", "2026-08-06T09:50:00Z"}, {"2026-08-06T09:55:00Z", "2026-08-06T10:25:00Z"}},
			1, 0.0,
		},
		{
			"两个独立久坐窗口均达标（均已休息）",
			[][2]string{{"2026-08-06T09:00:00Z", "2026-08-06T09:50:00Z"}, {"2026-08-06T10:20:00Z", "2026-08-06T11:10:00Z"}},
			2, 1.0,
		},
		{
			"窗口未达 45 分钟：不计次",
			[][2]string{{"2026-08-06T09:00:00Z", "2026-08-06T09:30:00Z"}},
			0, 0.0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s2, _ := newTestServices(t)
			ws2, userID2 := createWorkspace(t, s2)
			for _, se := range c.sessions {
				seedHealthSession(t, s2, ws2.ID, userID2, se[0], se[1])
			}
			stats, err := s2.Health.HealthStatsGet(ctx(), HealthStatsGetReq{WorkspaceID: ws2.ID, UserID: userID2})
			if err != nil {
				t.Fatalf("stats: %v", err)
			}
			if !stats.StatsEnabled {
				t.Fatal("expected stats_enabled=true by DDL default")
			}
			if stats.SedentaryCount != c.wantCount {
				t.Fatalf("expected sedentary_count=%d, got %d", c.wantCount, stats.SedentaryCount)
			}
			if stats.RestCompletionRate != c.wantRate {
				t.Fatalf("expected rest_completion_rate=%v, got %v", c.wantRate, stats.RestCompletionRate)
			}
		})
	}
}

// ---- 场景 5：stats_enabled=0 → 返回禁用态（stats_enabled=false + 零值），非报错 ----

func TestHealthStatsGetDisabledState(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	_, clock := healthFixedNow()
	s.Health.Now = clock

	if _, err := s.Health.HealthSettingsUpdate(ctx(), HealthSettingsUpdateReq{
		WorkspaceID: ws.ID, UserID: userID, StatsEnabled: false,
		NightMode: domain.NightModeAuto,
	}); err != nil {
		t.Fatalf("disable stats: %v", err)
	}
	// 即使有久坐会话也不统计
	seedHealthSession(t, s, ws.ID, userID, "2026-08-06T09:00:00Z", "2026-08-06T09:50:00Z")

	stats, err := s.Health.HealthStatsGet(ctx(), HealthStatsGetReq{WorkspaceID: ws.ID, UserID: userID})
	if err != nil {
		t.Fatalf("stats disabled should not error: %v", err)
	}
	if stats.StatsEnabled {
		t.Fatal("expected stats_enabled=false")
	}
	if stats.SedentaryCount != 0 || stats.RestCompletionRate != 0 {
		t.Fatalf("expected zeroed stats, got %+v", stats)
	}
}

// ---- 场景 6：HealthStatsGet 日期范围过滤 ----

func TestHealthStatsGetDateRange(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)

	// 8/6 一条 50 分钟；8/7 一条 50 分钟（不同窗口，均已休息）
	seedHealthSession(t, s, ws.ID, userID, "2026-08-06T09:00:00Z", "2026-08-06T09:50:00Z")
	seedHealthSession(t, s, ws.ID, userID, "2026-08-07T09:00:00Z", "2026-08-07T09:50:00Z")

	stats, err := s.Health.HealthStatsGet(ctx(), HealthStatsGetReq{
		WorkspaceID: ws.ID, UserID: userID, StartDate: "2026-08-07", EndDate: "2026-08-07",
	})
	if err != nil {
		t.Fatalf("stats range: %v", err)
	}
	if stats.SedentaryCount != 1 {
		t.Fatalf("expected 1 sedentary in range, got %d", stats.SedentaryCount)
	}

	// 非法日期 → INVALID_ARGUMENT
	if _, err := s.Health.HealthStatsGet(ctx(), HealthStatsGetReq{
		WorkspaceID: ws.ID, UserID: userID, StartDate: "2026/08/07",
	}); err == nil {
		t.Fatal("expected INVALID_ARGUMENT for bad start_date")
	} else if domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("expected INVALID_ARGUMENT, got %s", domain.AsError(err).Code)
	}
}

// ---- 场景 7：TimerEnd 评估钩子：连续 ≥45 分钟 → 拉前 health 提醒 next_trigger_at ----

func TestTimerEndEvaluatesSedentary(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	now, clock := healthFixedNow()
	s.Health.Now = clock
	s.Reminder.Now = clock
	s.Focus.Now = clock

	// 先有一段已结束会话（9:40-10:00，20 分钟），与即将结束的会话连续
	seedHealthSession(t, s, ws.ID, userID, "2026-08-06T09:40:00Z", "2026-08-06T10:00:00Z")

	// 开启久坐提醒：提醒 next_trigger_at = now+45（10:45）
	if _, err := s.Health.HealthSettingsUpdate(ctx(), HealthSettingsUpdateReq{
		WorkspaceID: ws.ID, UserID: userID, SedentaryEnabled: true,
		NightMode: domain.NightModeAuto,
	}); err != nil {
		t.Fatalf("enable sedentary: %v", err)
	}

	// 开始 60 分钟番茄钟，30 分钟后结束 → 会话 10:00-10:30
	started, err := s.Focus.TimerStart(ctx(), TimerStartReq{
		WorkspaceID: ws.ID, UserID: userID, Mode: domain.TimerModePomodoro,
		PlannedMinutes: 60, IdempotencyKey: "hm-" + NewID(),
	})
	if err != nil {
		t.Fatalf("timer start: %v", err)
	}
	*now = now.Add(30 * time.Minute)
	if _, err := s.Focus.TimerEnd(ctx(), TimerEndReq{
		WorkspaceID: ws.ID, UserID: userID, SessionID: started.ID,
	}); err != nil {
		t.Fatalf("timer end: %v", err)
	}

	// 连续窗口 = 20 + 30 = 50 ≥ 45 → next_trigger_at 拉前到结束时刻（10:30）
	rem, err := s.Repo.GetReminder(ctx(), userID, domain.ReminderKindHealth)
	if err != nil {
		t.Fatalf("get health reminder: %v", err)
	}
	if rem == nil {
		t.Fatal("expected health reminder")
	}
	want := "2026-08-06T10:30:00Z"
	if rem.NextTriggerAt != want {
		t.Fatalf("expected next_trigger_at=%s, got %s", want, rem.NextTriggerAt)
	}
}

// ---- 场景 8：TimerEnd 评估钩子：连续不足 45 分钟 → 不拉前 next_trigger_at ----

func TestTimerEndNoPullBelowThreshold(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	now, clock := healthFixedNow()
	s.Health.Now = clock
	s.Reminder.Now = clock
	s.Focus.Now = clock

	if _, err := s.Health.HealthSettingsUpdate(ctx(), HealthSettingsUpdateReq{
		WorkspaceID: ws.ID, UserID: userID, SedentaryEnabled: true,
		NightMode: domain.NightModeAuto,
	}); err != nil {
		t.Fatalf("enable sedentary: %v", err)
	}
	before, err := s.Repo.GetReminder(ctx(), userID, domain.ReminderKindHealth)
	if err != nil {
		t.Fatalf("get reminder: %v", err)
	}
	if before == nil {
		t.Fatal("expected health reminder")
	}

	// 10 分钟会话 → 连续窗口 10 < 45 → next_trigger_at 不变
	started, err := s.Focus.TimerStart(ctx(), TimerStartReq{
		WorkspaceID: ws.ID, UserID: userID, Mode: domain.TimerModePomodoro,
		PlannedMinutes: 60, IdempotencyKey: "hm-" + NewID(),
	})
	if err != nil {
		t.Fatalf("timer start: %v", err)
	}
	*now = now.Add(10 * time.Minute)
	if _, err := s.Focus.TimerEnd(ctx(), TimerEndReq{
		WorkspaceID: ws.ID, UserID: userID, SessionID: started.ID,
	}); err != nil {
		t.Fatalf("timer end: %v", err)
	}
	after, err := s.Repo.GetReminder(ctx(), userID, domain.ReminderKindHealth)
	if err != nil {
		t.Fatalf("get reminder: %v", err)
	}
	if after.NextTriggerAt != before.NextTriggerAt {
		t.Fatalf("next_trigger_at must not change below threshold: before=%s after=%s",
			before.NextTriggerAt, after.NextTriggerAt)
	}
}

// ---- 场景 9：ReminderTestSend(kind=health) 先评估再发布 ----

func TestReminderTestSendHealthEvaluatesThenPublishes(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	_, clock := healthFixedNow()
	s.Health.Now = clock
	s.Reminder.Now = clock

	// 无任何会话：评估为空 → 仍发布测试通知
	res, err := s.Reminder.ReminderTestSend(ctx(), ReminderTestSendReq{
		WorkspaceID: ws.ID, UserID: userID, Kind: domain.ReminderKindHealth,
	})
	if err != nil {
		t.Fatalf("test send health: %v", err)
	}
	if !res.OK || res.Kind != domain.ReminderKindHealth {
		t.Fatalf("unexpected test result: %+v", res)
	}
	if n := reminderNotificationCount(t, s, userID); n != 1 {
		t.Fatalf("expected 1 notification from test send, got %d", n)
	}
}
