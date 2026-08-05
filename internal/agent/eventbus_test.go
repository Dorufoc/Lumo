package agent

import (
	"context"
	"encoding/json"
	"testing"
)

// ---- Todo 4: UserEventBus 用户级事件总线 + notifications 持久化 ----

// TestUserEventBusReminderDeliversAndPersists: 发布 reminder:triggered(userA)
// → userA 订阅者（经 user_id）收到事件 + notifications 表有记录；
// 未订阅的 userB 收不到。
func TestUserEventBusReminderDeliversAndPersists(t *testing.T) {
	s := newTestAgent(t, false)
	_, userA := setupWorkspace(t, s)
	_, userB := setupWorkspace(t, s)

	chA, unsubA := s.UserEvents.SubscribeUser(userA)
	defer unsubA()
	chB, unsubB := s.UserEvents.SubscribeUser(userB)
	defer unsubB()

	err := s.UserEvents.Publish(userA, Event{
		Name: EventReminderTriggered,
		Payload: map[string]any{
			"request_id": "req-rem-1",
			"kind":       "goal",
			"ref_type":   "question",
			"ref_id":     "q-100",
		},
	})
	if err != nil {
		t.Fatalf("publish reminder:triggered: %v", err)
	}

	// userA 收到事件
	select {
	case ev := <-chA:
		if ev.Name != EventReminderTriggered {
			t.Fatalf("userA got event %q, want %q", ev.Name, EventReminderTriggered)
		}
	default:
		t.Fatal("userA subscriber did not receive reminder:triggered")
	}

	// userB 收不到
	select {
	case ev := <-chB:
		t.Fatalf("userB should NOT receive event, got %q", ev.Name)
	default:
	}

	// notifications 表有记录（6.2.1: kind/title_key/body_args_json/ref_type/ref_id）
	var kind, titleKey, bodyArgs, refType, refID string
	if err := s.Repo.DB().QueryRow(
		`SELECT kind, title_key, body_args_json, ref_type, ref_id
		 FROM notifications WHERE user_id = ?`, userA,
	).Scan(&kind, &titleKey, &bodyArgs, &refType, &refID); err != nil {
		t.Fatalf("notifications row missing for userA: %v", err)
	}
	if kind != EventReminderTriggered {
		t.Errorf("notifications.kind = %q, want %q", kind, EventReminderTriggered)
	}
	if titleKey != "notification.reminder_triggered.title" {
		t.Errorf("title_key = %q, want notification.reminder_triggered.title", titleKey)
	}
	if refType != "question" || refID != "q-100" {
		t.Errorf("ref = %q/%q, want question/q-100", refType, refID)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(bodyArgs), &args); err != nil {
		t.Fatalf("body_args_json is not valid JSON: %v", err)
	}
	if args["kind"] != "goal" || args["ref_id"] != "q-100" {
		t.Errorf("body_args wrong: %v", args)
	}
	if args["occurred_at"] == nil || args["occurred_at"] == "" {
		t.Error("payload missing occurred_at (API §3 事件规范)")
	}
	if _, ok := args["sequence_no"]; !ok {
		t.Error("payload missing sequence_no (API §3 事件规范)")
	}

	// userB 无通知记录
	var cnt int
	if err := s.Repo.DB().QueryRow(
		`SELECT COUNT(*) FROM notifications WHERE user_id = ?`, userB).Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 0 {
		t.Errorf("userB must have no notification rows, got %d", cnt)
	}
}

// TestUserEventBusGradingUpdatedPayload: 发布 grading:updated →
// 订阅者收到含 grading_id/status/score 的载荷（API ch3 L222 + 7.16 L506）。
func TestUserEventBusGradingUpdatedPayload(t *testing.T) {
	s := newTestAgent(t, false)
	_, userA := setupWorkspace(t, s)
	ch, unsub := s.UserEvents.SubscribeUser(userA)
	defer unsub()

	err := s.UserEvents.Publish(userA, Event{
		Name: EventGradingUpdated,
		Payload: map[string]any{
			"request_id": "req-grade-1",
			"grading_id": "grading-42",
			"status":     "completed",
			"score":      92.5,
		},
	})
	if err != nil {
		t.Fatalf("publish grading:updated: %v", err)
	}
	select {
	case ev := <-ch:
		if ev.Name != EventGradingUpdated {
			t.Fatalf("got event %q, want %q", ev.Name, EventGradingUpdated)
		}
		p := ev.Payload
		if p["grading_id"] != "grading-42" {
			t.Errorf("grading_id = %v, want grading-42", p["grading_id"])
		}
		if p["status"] != "completed" {
			t.Errorf("status = %v, want completed", p["status"])
		}
		if p["score"] != 92.5 {
			t.Errorf("score = %v, want 92.5", p["score"])
		}
	default:
		t.Fatal("subscriber did not receive grading:updated")
	}
}

// TestUserEventBusUnknownEvent: 未注册事件类型 → 返回错误、不 panic、
// notifications 不落库、不广播给订阅者。
func TestUserEventBusUnknownEvent(t *testing.T) {
	s := newTestAgent(t, false)
	_, userA := setupWorkspace(t, s)
	ch, unsub := s.UserEvents.SubscribeUser(userA)
	defer unsub()

	err := s.UserEvents.Publish(userA, Event{
		Name:    "no:such:event",
		Payload: map[string]any{"x": 1},
	})
	if err == nil {
		t.Fatal("expected error for unregistered event type")
	}
	select {
	case ev := <-ch:
		t.Fatalf("unknown event must not be delivered, got %q", ev.Name)
	default:
	}
	var cnt int
	if err := s.Repo.DB().QueryRow(`SELECT COUNT(*) FROM notifications`).Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 0 {
		t.Fatalf("unknown event must not persist, got %d notification rows", cnt)
	}
}

// TestUserSessionBusesCoexist: 用户级事件只投递到 UserEventBus 订阅者；
// session 级 Bus 订阅者收不到 —— 两条总线并存互不影响。
func TestUserSessionBusesCoexist(t *testing.T) {
	s := newTestAgent(t, false)
	ws, userA := setupWorkspace(t, s)

	// session 级订阅（Agent 流式通道）
	if _, err := s.AgentChatCreate(context.Background(), AgentChatCreateReq{
		WorkspaceID: ws, UserID: userA, Agent: "tutor",
	}); err != nil {
		t.Fatal(err)
	}
	sessCh, unsubS := s.Events.Subscribe("session-not-involved")
	defer unsubS()
	userCh, unsubU := s.UserEvents.SubscribeUser(userA)
	defer unsubU()

	if err := s.UserEvents.Publish(userA, Event{
		Name:    EventFlashcardDue,
		Payload: map[string]any{"due_count": 3},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-userCh:
		if ev.Name != EventFlashcardDue {
			t.Fatalf("user channel got %q, want %q", ev.Name, EventFlashcardDue)
		}
	default:
		t.Fatal("user channel missing event")
	}
	select {
	case ev := <-sessCh:
		t.Fatalf("session bus must NOT receive user event, got %q", ev.Name)
	default:
	}
}

// TestUserEventBusSequenceMonotonic: 同一 user 的 sequence_no 严格递增。
func TestUserEventBusSequenceMonotonic(t *testing.T) {
	s := newTestAgent(t, false)
	_, userA := setupWorkspace(t, s)
	ch, unsub := s.UserEvents.SubscribeUser(userA)
	defer unsub()

	for i := 0; i < 3; i++ {
		if err := s.UserEvents.Publish(userA, Event{
			Name:    EventSyncExtended,
			Payload: map[string]any{"entity_type": "note", "conflict_count": i},
		}); err != nil {
			t.Fatal(err)
		}
		select {
		case ev := <-ch:
			seq, ok := ev.Payload["sequence_no"].(int64)
			if !ok {
				t.Fatalf("sequence_no type = %T, want int64", ev.Payload["sequence_no"])
			}
			if seq != int64(i+1) {
				t.Fatalf("sequence_no = %d, want %d (strictly increasing)", seq, i+1)
			}
		default:
			t.Fatal("event not received")
		}
	}
}
