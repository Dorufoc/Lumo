package service

import (
	"testing"
	"time"

	"lumo/internal/domain"
)

// ---- 辅助 ----

// checkinFixedNow 固定时钟：返回 setNow 闭包捕获的当前时间（测试推进日期用）。
func checkinFixedNow() (*time.Time, func() time.Time) {
	t := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	return &t, func() time.Time { return t }
}

// usageCheckinCount 统计用户 checkin 类型 usage_events 条数（事实断言）。
func usageCheckinCount(t *testing.T, s *Services, userID string) int {
	t.Helper()
	n, err := s.Repo.CountUsageEventsByType(ctx(), userID, "checkin")
	if err != nil {
		t.Fatalf("count usage_events: %v", err)
	}
	return n
}

// userAchievementCount 统计用户某成就（按 code）解锁行数（幂等断言）。
func userAchievementCount(t *testing.T, s *Services, userID, code string) int {
	t.Helper()
	var n int
	err := s.Repo.DB().QueryRowContext(ctx(), `
		SELECT COUNT(*) FROM user_achievements ua
		JOIN achievement_defs ad ON ad.id = ua.achievement_id
		WHERE ua.user_id = ? AND ad.code = ?`, userID, code).Scan(&n)
	if err != nil {
		t.Fatalf("count user_achievements: %v", err)
	}
	return n
}

// checkinOfCode 从 AchievementList 中按 code 取成就视图。
func checkinOfCode(t *testing.T, s *Services, wsID, userID, code string) *AchievementView {
	t.Helper()
	list, err := s.Checkin.AchievementList(ctx(), AchievementListReq{WorkspaceID: wsID, UserID: userID})
	if err != nil {
		t.Fatalf("achievement list: %v", err)
	}
	for _, v := range list {
		if v.Code == code {
			return v
		}
	}
	t.Fatalf("achievement %q not found in defs", code)
	return nil
}

// ---- 场景 1：每日打卡幂等（同日第二次返回原记录，不重复计数） ----

func TestCheckinCreateIdempotent(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	now, clock := checkinFixedNow()
	s.Checkin.Now = clock

	first, err := s.Checkin.CheckinCreate(ctx(), CheckinCreateReq{
		WorkspaceID: ws.ID, UserID: userID, IdempotencyKey: "ck-" + NewID(),
	})
	if err != nil {
		t.Fatalf("first checkin: %v", err)
	}
	if first.Kind != domain.CheckinKindNormal || first.Date != domain.CheckinDate(*now) {
		t.Fatalf("unexpected checkin: %+v", first)
	}
	if usageCheckinCount(t, s, userID) != 1 {
		t.Fatalf("expected 1 usage_events after first checkin, got %d", usageCheckinCount(t, s, userID))
	}

	// 同日第二次打卡（不同幂等键）：返回原记录、不报错、不重复计数
	again, err := s.Checkin.CheckinCreate(ctx(), CheckinCreateReq{
		WorkspaceID: ws.ID, UserID: userID, IdempotencyKey: "ck-" + NewID(),
	})
	if err != nil {
		t.Fatalf("repeat checkin should not error: %v", err)
	}
	if again.ID != first.ID || again.Date != first.Date {
		t.Fatalf("expected same record, got %+v vs %+v", again, first)
	}
	if usageCheckinCount(t, s, userID) != 1 {
		t.Fatalf("expected usage_events count still 1, got %d", usageCheckinCount(t, s, userID))
	}
	// total 计数不重复
	streak, err := s.Checkin.StreakGet(ctx(), StreakGetReq{WorkspaceID: ws.ID, UserID: userID})
	if err != nil {
		t.Fatalf("streak get: %v", err)
	}
	if streak.TotalCheckins != 1 {
		t.Fatalf("expected total_checkins=1, got %d", streak.TotalCheckins)
	}
	// 首次打卡成就解锁
	if v := checkinOfCode(t, s, ws.ID, userID, "first_checkin"); !v.IsUnlocked {
		t.Fatal("expected first_checkin unlocked after first checkin")
	}
}

// ---- 场景 2：补签每月限 3 次 ----

func TestCheckinMakeupQuota(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	_, clock := checkinFixedNow() // 今天 = 2026-08-06
	s.Checkin.Now = clock

	// 3 次补签（同月 8 月）成功
	for _, d := range []string{"2026-08-01", "2026-08-02", "2026-08-03"} {
		if _, err := s.Checkin.CheckinMakeup(ctx(), CheckinMakeupReq{
			WorkspaceID: ws.ID, UserID: userID, Date: d, IdempotencyKey: "ckm-" + NewID(),
		}); err != nil {
			t.Fatalf("makeup %s: %v", d, err)
		}
	}
	if usageCheckinCount(t, s, userID) != 3 {
		t.Fatalf("expected 3 usage_events after 3 makeups, got %d", usageCheckinCount(t, s, userID))
	}

	// 第 4 次 → QUOTA_EXCEEDED
	_, err := s.Checkin.CheckinMakeup(ctx(), CheckinMakeupReq{
		WorkspaceID: ws.ID, UserID: userID, Date: "2026-08-04", IdempotencyKey: "ckm-" + NewID(),
	})
	if err == nil {
		t.Fatal("expected error on 4th makeup in month")
	} else if domain.AsError(err).Code != domain.CodeQuotaExceeded {
		t.Fatalf("expected QUOTA_EXCEEDED, got %s", domain.AsError(err).Code)
	}

	// 未来日期补签 → INVALID_ARGUMENT
	if _, err := s.Checkin.CheckinMakeup(ctx(), CheckinMakeupReq{
		WorkspaceID: ws.ID, UserID: userID, Date: "2026-08-06", IdempotencyKey: "ckm-" + NewID(),
	}); err == nil {
		t.Fatal("expected error on future/today makeup")
	} else if domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("expected INVALID_ARGUMENT, got %s", domain.AsError(err).Code)
	}

	// 同一日期重复补签：返回原记录，不消耗配额（8 月配额仍 3）
	if _, err := s.Checkin.CheckinMakeup(ctx(), CheckinMakeupReq{
		WorkspaceID: ws.ID, UserID: userID, Date: "2026-08-02", IdempotencyKey: "ckm-" + NewID(),
	}); err != nil {
		t.Fatalf("duplicate makeup should return existing: %v", err)
	}
	if usageCheckinCount(t, s, userID) != 3 {
		t.Fatalf("duplicate makeup must not write fact, got %d events", usageCheckinCount(t, s, userID))
	}
}

// ---- 场景 3：streak 聚合（连续 7 天 → streak=7） ----

func TestStreakComputation(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	now, clock := checkinFixedNow() // 2026-08-06 起
	s.Checkin.Now = clock

	for i := 0; i < 7; i++ {
		if _, err := s.Checkin.CheckinCreate(ctx(), CheckinCreateReq{
			WorkspaceID: ws.ID, UserID: userID, Minutes: 20, IdempotencyKey: "ck-" + NewID(),
		}); err != nil {
			t.Fatalf("day %d checkin: %v", i+1, err)
		}
		*now = now.AddDate(0, 0, 1)
	}
	// 7 次打卡事实
	if usageCheckinCount(t, s, userID) != 7 {
		t.Fatalf("expected 7 usage_events, got %d", usageCheckinCount(t, s, userID))
	}

	// StreakGet → streak=7, total=7
	streak, err := s.Checkin.StreakGet(ctx(), StreakGetReq{WorkspaceID: ws.ID, UserID: userID})
	if err != nil {
		t.Fatalf("streak get: %v", err)
	}
	if streak.Streak != 7 || streak.TotalCheckins != 7 {
		t.Fatalf("expected streak=7 total=7, got streak=%d total=%d", streak.Streak, streak.TotalCheckins)
	}
	// 断签：再前进 2 天不打卡 → streak 归零但 total 保留
	*now = now.AddDate(0, 0, 2)
	streak, err = s.Checkin.StreakGet(ctx(), StreakGetReq{WorkspaceID: ws.ID, UserID: userID})
	if err != nil {
		t.Fatalf("streak get after break: %v", err)
	}
	if streak.Streak != 0 {
		t.Fatalf("expected streak=0 after break, got %d", streak.Streak)
	}
	if streak.TotalCheckins != 7 {
		t.Fatalf("expected total retained=7, got %d", streak.TotalCheckins)
	}
}

// ---- 场景 4：成就规则引擎（连续 7 天 → streak_7；重复触发不重复发奖） ----

func TestAchievementStreakFires(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	now, clock := checkinFixedNow()
	s.Checkin.Now = clock

	for i := 0; i < 7; i++ {
		if _, err := s.Checkin.CheckinCreate(ctx(), CheckinCreateReq{
			WorkspaceID: ws.ID, UserID: userID, IdempotencyKey: "ck-" + NewID(),
		}); err != nil {
			t.Fatalf("day %d checkin: %v", i+1, err)
		}
		*now = now.AddDate(0, 0, 1)
	}

	// streak_7 解锁；streak_3 也已解锁；total_10 未解锁
	if v := checkinOfCode(t, s, ws.ID, userID, "streak_7"); !v.IsUnlocked || v.AwardedAt == nil {
		t.Fatal("expected streak_7 unlocked")
	}
	if v := checkinOfCode(t, s, ws.ID, userID, "streak_3"); !v.IsUnlocked {
		t.Fatal("expected streak_3 unlocked")
	}
	if v := checkinOfCode(t, s, ws.ID, userID, "total_10"); v.IsUnlocked {
		t.Fatal("total_10 must not be unlocked after 7 checkins")
	}
	if got := userAchievementCount(t, s, userID, "streak_7"); got != 1 {
		t.Fatalf("expected streak_7 awarded exactly once, got %d", got)
	}

	// 重复触发（同日再打卡）：不新增成就行
	if _, err := s.Checkin.CheckinCreate(ctx(), CheckinCreateReq{
		WorkspaceID: ws.ID, UserID: userID, IdempotencyKey: "ck-" + NewID(),
	}); err != nil {
		t.Fatalf("re-fire checkin: %v", err)
	}
	if got := userAchievementCount(t, s, userID, "streak_7"); got != 1 {
		t.Fatalf("re-fire must not duplicate award, got %d rows", got)
	}
}

// ---- 场景 5：总量阈值规则（累计 10 次 → total_10） ----

func TestAchievementTotalThreshold(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	now, clock := checkinFixedNow()
	s.Checkin.Now = clock

	for i := 0; i < 10; i++ {
		if _, err := s.Checkin.CheckinCreate(ctx(), CheckinCreateReq{
			WorkspaceID: ws.ID, UserID: userID, IdempotencyKey: "ck-" + NewID(),
		}); err != nil {
			t.Fatalf("day %d checkin: %v", i+1, err)
		}
		*now = now.AddDate(0, 0, 1)
	}
	if v := checkinOfCode(t, s, ws.ID, userID, "total_10"); !v.IsUnlocked {
		t.Fatal("expected total_10 unlocked after 10 checkins")
	}
	if got := userAchievementCount(t, s, userID, "total_10"); got != 1 {
		t.Fatalf("expected total_10 awarded exactly once, got %d", got)
	}
}
