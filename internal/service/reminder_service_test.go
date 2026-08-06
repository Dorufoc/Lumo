package service

import (
	"testing"
	"time"

	"lumo/internal/domain"
)

// ---- 辅助 ----

// reminderFixedNow 固定时钟：返回 setNow 闭包捕获的当前时间（测试推进时间用）。
// 命名独立于 checkin/focus 的 helper，避免同包符号冲突。
func reminderFixedNow() (*time.Time, func() time.Time) {
	t := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	return &t, func() time.Time { return t }
}

// reminderNotificationCount 统计用户通知条数（事实断言：每次触发一条）。
func reminderNotificationCount(t *testing.T, s *Services, userID string) int {
	t.Helper()
	var n int
	if err := s.Repo.DB().QueryRowContext(ctx(), `SELECT COUNT(*) FROM notifications WHERE user_id = ?`, userID).Scan(&n); err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	return n
}

// reminderRowCount 统计用户某 kind 的提醒行数（(user_id, kind) 唯一断言）。
func reminderRowCount(t *testing.T, s *Services, userID, kind string) int {
	t.Helper()
	var n int
	if err := s.Repo.DB().QueryRowContext(ctx(), `SELECT COUNT(*) FROM reminders WHERE user_id = ? AND kind = ?`, userID, kind).Scan(&n); err != nil {
		t.Fatalf("count reminders: %v", err)
	}
	return n
}

const (
	reminderRuleInterval60 = `{"type":"interval","minutes":60,"repeat":true}`
	reminderRuleInterval30 = `{"type":"interval","minutes":30,"repeat":true}`
	reminderRuleOneShot30  = `{"type":"interval","minutes":30,"repeat":false}`
)

// ---- 场景 1：ReminderUpsert 创建 + 更新（(user_id, kind) 唯一）+ 参数校验 ----

func TestReminderUpsertCreateUpdatePerUserKind(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	now, clock := reminderFixedNow()
	s.Reminder.Now = clock

	created, err := s.Reminder.ReminderUpsert(ctx(), ReminderUpsertReq{
		WorkspaceID: ws.ID, UserID: userID, Kind: domain.ReminderKindReview,
		RuleJSON: reminderRuleInterval60, Enabled: true,
	})
	if err != nil {
		t.Fatalf("upsert create: %v", err)
	}
	wantNext := now.Add(60 * time.Minute).UTC().Format(time.RFC3339)
	if created.Kind != domain.ReminderKindReview || !created.Enabled || created.NextTriggerAt != wantNext {
		t.Fatalf("unexpected created reminder: %+v (want next=%s)", created, wantNext)
	}

	// 同 (user, kind) 更新：换规则 + 关闭 → 仍只有一行，next_trigger_at 重算
	updated, err := s.Reminder.ReminderUpsert(ctx(), ReminderUpsertReq{
		WorkspaceID: ws.ID, UserID: userID, Kind: domain.ReminderKindReview,
		RuleJSON: reminderRuleInterval30, Enabled: false,
	})
	if err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	if updated.Enabled {
		t.Fatalf("expected disabled after update: %+v", updated)
	}
	wantNext30 := now.Add(30 * time.Minute).UTC().Format(time.RFC3339)
	if updated.NextTriggerAt != wantNext30 {
		t.Fatalf("expected next=%s, got %s", wantNext30, updated.NextTriggerAt)
	}
	if reminderRowCount(t, s, userID, domain.ReminderKindReview) != 1 {
		t.Fatalf("expected exactly 1 review reminder row")
	}

	// 不同 kind → 第二行
	if _, err := s.Reminder.ReminderUpsert(ctx(), ReminderUpsertReq{
		WorkspaceID: ws.ID, UserID: userID, Kind: domain.ReminderKindGoal,
		RuleJSON: reminderRuleInterval60, Enabled: true,
	}); err != nil {
		t.Fatalf("upsert goal: %v", err)
	}
	if reminderRowCount(t, s, userID, domain.ReminderKindGoal) != 1 {
		t.Fatalf("expected exactly 1 goal reminder row")
	}
	if reminderRowCount(t, s, userID, domain.ReminderKindReview) != 1 {
		t.Fatalf("review row count changed after goal upsert")
	}
}

func TestReminderUpsertValidation(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	_, clock := reminderFixedNow()
	s.Reminder.Now = clock

	cases := []struct {
		name string
		req  ReminderUpsertReq
		code domain.ErrorCode
	}{
		{"非法 kind", ReminderUpsertReq{WorkspaceID: ws.ID, UserID: userID, Kind: "sleep", RuleJSON: reminderRuleInterval60, Enabled: true}, domain.CodeInvalidArgument},
		{"非法 JSON", ReminderUpsertReq{WorkspaceID: ws.ID, UserID: userID, Kind: domain.ReminderKindReview, RuleJSON: `not-json`, Enabled: true}, domain.CodeInvalidArgument},
		{"未知规则类型", ReminderUpsertReq{WorkspaceID: ws.ID, UserID: userID, Kind: domain.ReminderKindReview, RuleJSON: `{"type":"cron","minutes":60,"repeat":true}`, Enabled: true}, domain.CodeInvalidArgument},
		{"interval minutes 为 0", ReminderUpsertReq{WorkspaceID: ws.ID, UserID: userID, Kind: domain.ReminderKindReview, RuleJSON: `{"type":"interval","minutes":0,"repeat":true}`, Enabled: true}, domain.CodeInvalidArgument},
		{"缺 user_id", ReminderUpsertReq{WorkspaceID: ws.ID, Kind: domain.ReminderKindReview, RuleJSON: reminderRuleInterval60, Enabled: true}, domain.CodeInvalidArgument},
	}
	for _, c := range cases {
		_, err := s.Reminder.ReminderUpsert(ctx(), c.req)
		if err == nil {
			t.Fatalf("%s: expected error", c.name)
		}
		if domain.AsError(err).Code != c.code {
			t.Fatalf("%s: expected %s, got %s", c.name, c.code, domain.AsError(err).Code)
		}
	}
}

// ---- 场景 2：RunOnce 触发到期提醒 → 恰好一条通知 + next_trigger_at 前移；重复扫描不重复触发 ----

func TestReminderRunOnceFiresDueAndAdvances(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	now, clock := reminderFixedNow()
	s.Reminder.Now = clock

	if _, err := s.Reminder.ReminderUpsert(ctx(), ReminderUpsertReq{
		WorkspaceID: ws.ID, UserID: userID, Kind: domain.ReminderKindReview,
		RuleJSON: reminderRuleInterval60, Enabled: true,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// 未到期（next=now+60min）：不触发
	if err := s.Reminder.RunOnce(ctx()); err != nil {
		t.Fatalf("run once (not due): %v", err)
	}
	if n := reminderNotificationCount(t, s, userID); n != 0 {
		t.Fatalf("expected 0 notifications before due, got %d", n)
	}

	// 推进 61 分钟 → 到期：RunOnce 触发一条，next_trigger_at 前移
	*now = now.Add(61 * time.Minute)
	if err := s.Reminder.RunOnce(ctx()); err != nil {
		t.Fatalf("run once (due): %v", err)
	}
	if n := reminderNotificationCount(t, s, userID); n != 1 {
		t.Fatalf("expected 1 notification after fire, got %d", n)
	}
	row, err := s.Repo.GetReminder(ctx(), userID, domain.ReminderKindReview)
	if err != nil {
		t.Fatalf("get reminder: %v", err)
	}
	wantNext := now.Add(60 * time.Minute).UTC().Format(time.RFC3339)
	if row.NextTriggerAt != wantNext {
		t.Fatalf("expected next_trigger_at=%s, got %s", wantNext, row.NextTriggerAt)
	}

	// 幂等：时钟未动，再次扫描不得重复触发
	if err := s.Reminder.RunOnce(ctx()); err != nil {
		t.Fatalf("run once (idempotent): %v", err)
	}
	if n := reminderNotificationCount(t, s, userID); n != 1 {
		t.Fatalf("idempotency violated: expected still 1 notification, got %d", n)
	}

	// 再推进 61 分钟 → 第二次到期：再次触发（累计 2 条）
	*now = now.Add(61 * time.Minute)
	if err := s.Reminder.RunOnce(ctx()); err != nil {
		t.Fatalf("run once (second cycle): %v", err)
	}
	if n := reminderNotificationCount(t, s, userID); n != 2 {
		t.Fatalf("expected 2 notifications after second cycle, got %d", n)
	}
}

// ---- 场景 3：一次性规则（repeat=false）→ 触发后 enabled 翻转为 0，不再触发 ----

func TestReminderRunOnceOneShotDisables(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	now, clock := reminderFixedNow()
	s.Reminder.Now = clock

	if _, err := s.Reminder.ReminderUpsert(ctx(), ReminderUpsertReq{
		WorkspaceID: ws.ID, UserID: userID, Kind: domain.ReminderKindStreak,
		RuleJSON: reminderRuleOneShot30, Enabled: true,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	*now = now.Add(31 * time.Minute) // 到期
	if err := s.Reminder.RunOnce(ctx()); err != nil {
		t.Fatalf("run once: %v", err)
	}
	if n := reminderNotificationCount(t, s, userID); n != 1 {
		t.Fatalf("expected 1 notification for one-shot, got %d", n)
	}
	row, err := s.Repo.GetReminder(ctx(), userID, domain.ReminderKindStreak)
	if err != nil {
		t.Fatalf("get reminder: %v", err)
	}
	if row.Enabled != 0 {
		t.Fatalf("expected one-shot reminder disabled after fire, enabled=%d", row.Enabled)
	}

	// 再推进很久：因 enabled=0 不会再次触发
	*now = now.Add(10 * time.Hour)
	if err := s.Reminder.RunOnce(ctx()); err != nil {
		t.Fatalf("run once (after disable): %v", err)
	}
	if n := reminderNotificationCount(t, s, userID); n != 1 {
		t.Fatalf("one-shot must not refire after disable, got %d", n)
	}
}

// ---- 场景 4：未到期提醒（next_trigger_at 在未来）不触发 ----

func TestReminderRunOnceNonDueNotFired(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	_, clock := reminderFixedNow()
	s.Reminder.Now = clock

	if _, err := s.Reminder.ReminderUpsert(ctx(), ReminderUpsertReq{
		WorkspaceID: ws.ID, UserID: userID, Kind: domain.ReminderKindExam,
		RuleJSON: reminderRuleInterval60, Enabled: true,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.Reminder.RunOnce(ctx()); err != nil {
		t.Fatalf("run once: %v", err)
	}
	if n := reminderNotificationCount(t, s, userID); n != 0 {
		t.Fatalf("non-due reminder must not fire, got %d notifications", n)
	}
}

// ---- 场景 5：ReminderTestSend 立即触发（不依赖时钟）----

func TestReminderTestSendFiresImmediately(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	_, clock := reminderFixedNow()
	s.Reminder.Now = clock

	res, err := s.Reminder.ReminderTestSend(ctx(), ReminderTestSendReq{
		WorkspaceID: ws.ID, UserID: userID, Kind: domain.ReminderKindReview,
	})
	if err != nil {
		t.Fatalf("test send: %v", err)
	}
	if !res.OK || res.Kind != domain.ReminderKindReview {
		t.Fatalf("unexpected test result: %+v", res)
	}
	if n := reminderNotificationCount(t, s, userID); n != 1 {
		t.Fatalf("expected 1 notification from test send, got %d", n)
	}

	// 通知 body_args 携带 kind + test 标记
	page, err := s.Reminder.NotificationList(ctx(), NotificationListReq{
		WorkspaceID: ws.ID, UserID: userID,
	})
	if err != nil {
		t.Fatalf("list notifications: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("expected 1 notification item, got %d", len(page.Items))
	}
	item := page.Items[0]
	if item.TitleKey != "notification.reminder_triggered.title" {
		t.Fatalf("expected title_key notification.reminder_triggered.title, got %s", item.TitleKey)
	}
	if item.BodyArgs["kind"] != domain.ReminderKindReview {
		t.Fatalf("expected body_args.kind=%s, got %v", domain.ReminderKindReview, item.BodyArgs["kind"])
	}
	if item.BodyArgs["test"] != true {
		t.Fatalf("expected body_args.test=true, got %v", item.BodyArgs["test"])
	}
}

// ---- 场景 6：NotificationList 分页 + unread_only ----

func TestNotificationListPaginationAndUnread(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	_, clock := reminderFixedNow()
	s.Reminder.Now = clock

	for _, kind := range []string{domain.ReminderKindReview, domain.ReminderKindGoal, domain.ReminderKindExam} {
		if _, err := s.Reminder.ReminderTestSend(ctx(), ReminderTestSendReq{
			WorkspaceID: ws.ID, UserID: userID, Kind: kind,
		}); err != nil {
			t.Fatalf("test send %s: %v", kind, err)
		}
	}
	if n := reminderNotificationCount(t, s, userID); n != 3 {
		t.Fatalf("expected 3 notifications, got %d", n)
	}

	// 分页：limit=2 → 第一页 2 条 + has_more + next_cursor
	page1, err := s.Reminder.NotificationList(ctx(), NotificationListReq{
		WorkspaceID: ws.ID, UserID: userID, Limit: 2,
	})
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1.Items) != 2 || !page1.HasMore || page1.NextCursor == "" {
		t.Fatalf("unexpected page1: %+v", page1)
	}

	// 第二页 → 1 条、has_more=false
	page2, err := s.Reminder.NotificationList(ctx(), NotificationListReq{
		WorkspaceID: ws.ID, UserID: userID, Limit: 2, Cursor: page1.NextCursor,
	})
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2.Items) != 1 || page2.HasMore {
		t.Fatalf("unexpected page2: %+v", page2)
	}
	// 两页无重复
	seen := map[string]bool{}
	for _, it := range append(page1.Items, page2.Items...) {
		if seen[it.ID] {
			t.Fatalf("duplicate notification across pages: %s", it.ID)
		}
		seen[it.ID] = true
	}

	// unread_only：全部未读 → 3 条
	unread, err := s.Reminder.NotificationList(ctx(), NotificationListReq{
		WorkspaceID: ws.ID, UserID: userID, UnreadOnly: true,
	})
	if err != nil {
		t.Fatalf("unread list: %v", err)
	}
	if len(unread.Items) != 3 {
		t.Fatalf("expected 3 unread, got %d", len(unread.Items))
	}

	// 标记 2 条已读
	ids := []string{page1.Items[0].ID, page2.Items[0].ID}
	res, err := s.Reminder.NotificationMarkRead(ctx(), NotificationMarkReadReq{
		WorkspaceID: ws.ID, UserID: userID, IDs: ids,
	})
	if err != nil {
		t.Fatalf("mark read: %v", err)
	}
	if res.Updated != 2 {
		t.Fatalf("expected 2 marked read, got %d", res.Updated)
	}
	unread2, err := s.Reminder.NotificationList(ctx(), NotificationListReq{
		WorkspaceID: ws.ID, UserID: userID, UnreadOnly: true,
	})
	if err != nil {
		t.Fatalf("unread list after mark: %v", err)
	}
	if len(unread2.Items) != 1 {
		t.Fatalf("expected 1 unread after mark, got %d", len(unread2.Items))
	}
}

// ---- 场景 7：NotificationMarkRead 计数 + 已读展示 ----

func TestNotificationMarkReadShowsReadAt(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	_, clock := reminderFixedNow()
	s.Reminder.Now = clock

	if _, err := s.Reminder.ReminderTestSend(ctx(), ReminderTestSendReq{
		WorkspaceID: ws.ID, UserID: userID, Kind: domain.ReminderKindHealth,
	}); err != nil {
		t.Fatalf("test send: %v", err)
	}
	if _, err := s.Reminder.ReminderTestSend(ctx(), ReminderTestSendReq{
		WorkspaceID: ws.ID, UserID: userID, Kind: domain.ReminderKindGoal,
	}); err != nil {
		t.Fatalf("test send 2: %v", err)
	}

	all, err := s.Reminder.NotificationList(ctx(), NotificationListReq{
		WorkspaceID: ws.ID, UserID: userID,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	target := all.Items[0].ID
	res, err := s.Reminder.NotificationMarkRead(ctx(), NotificationMarkReadReq{
		WorkspaceID: ws.ID, UserID: userID, IDs: []string{target},
	})
	if err != nil {
		t.Fatalf("mark read: %v", err)
	}
	if res.Updated != 1 {
		t.Fatalf("expected 1 updated, got %d", res.Updated)
	}

	// 已读行带 read_at；未读行 read_at 为空
	after, err := s.Reminder.NotificationList(ctx(), NotificationListReq{
		WorkspaceID: ws.ID, UserID: userID,
	})
	if err != nil {
		t.Fatalf("list after: %v", err)
	}
	for _, it := range after.Items {
		if it.ID == target {
			if it.ReadAt == nil || *it.ReadAt == "" {
				t.Fatalf("expected read_at set for %s", target)
			}
		} else if it.ReadAt != nil {
			t.Fatalf("unread notification %s has read_at %v", it.ID, *it.ReadAt)
		}
	}

	// 空 ids → INVALID_ARGUMENT
	if _, err := s.Reminder.NotificationMarkRead(ctx(), NotificationMarkReadReq{
		WorkspaceID: ws.ID, UserID: userID, IDs: []string{},
	}); err == nil {
		t.Fatal("expected INVALID_ARGUMENT for empty ids")
	} else if domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("expected INVALID_ARGUMENT, got %s", domain.AsError(err).Code)
	}
}
