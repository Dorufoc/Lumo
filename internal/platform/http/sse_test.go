package http

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lumo/internal/agent"
	"lumo/internal/database"
	"lumo/internal/repository"
)

// ---- Todo 4: GET /api/v1/events 支持 session_id | user_id（user_id 优先） ----

// fakeEventBus 同时满足 SSEBus（Subscribe）与 UserSSEBus（SubscribeUser），
// 记录每次订阅的主语，供测试断言实际路由到哪条总线。
type fakeEventBus struct {
	subj  chan string // Subscribe(session_id) 收到的主语
	subjU chan string // SubscribeUser(user_id) 收到的主语
	ch    chan agent.Event
}

func newFakeEventBus() *fakeEventBus {
	return &fakeEventBus{
		subj:  make(chan string, 1),
		subjU: make(chan string, 1),
		ch:    make(chan agent.Event, 8),
	}
}

func (f *fakeEventBus) Subscribe(subject string) (<-chan agent.Event, func()) {
	f.subj <- subject
	return f.ch, func() {}
}

func (f *fakeEventBus) SubscribeUser(subject string) (<-chan agent.Event, func()) {
	f.subjU <- subject
	return f.ch, func() {}
}

// assertNoSessionSubscribe 断言 session 级 Subscribe 从未被调用。
func assertNoSessionSubscribe(t *testing.T, bus *fakeEventBus) {
	t.Helper()
	select {
	case got := <-bus.subj:
		t.Fatalf("Subscribe(session) called with %q, want none", got)
	default:
	}
}

// assertNoUserSubscribe 断言用户级 SubscribeUser 从未被调用。
func assertNoUserSubscribe(t *testing.T, bus *fakeEventBus) {
	t.Helper()
	select {
	case got := <-bus.subjU:
		t.Fatalf("SubscribeUser called with %q, want none", got)
	default:
	}
}

// signalRecorder 在每次 Write 时发出信号，避免测试与 handler goroutine 并发读 Body。
type signalRecorder struct {
	*httptest.ResponseRecorder
	wrote chan struct{}
}

func newSignalRecorder() *signalRecorder {
	return &signalRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		wrote:            make(chan struct{}, 16),
	}
}

func (r *signalRecorder) Write(p []byte) (int, error) {
	n, err := r.ResponseRecorder.Write(p)
	select {
	case r.wrote <- struct{}{}:
	default:
	}
	return n, err
}

// TestEventsRouteRegisteredOnce: /api/v1/events 必须恰好注册一次。
// ServeMux 对重复 pattern 注册会 panic —— 若第二次注册未 panic，
// 说明路由未被 NewServer 注册（即没有恰好一次注册），测试失败。
func TestEventsRouteRegisteredOnce(t *testing.T) {
	s := NewServer()
	defer func() {
		if rec := recover(); rec == nil {
			t.Fatal("duplicate /api/v1/events registration did not panic; route is not registered exactly once")
		}
	}()
	s.mux.HandleFunc("/api/v1/events", func(w http.ResponseWriter, r *http.Request) {})
}

// TestSSEUserIDSubscription: ?user_id=U-1 → 走 UserSSEBus，事件写入 SSE 流。
func TestSSEUserIDSubscription(t *testing.T) {
	bus := newFakeEventBus()
	s := NewServer()
	s.RegisterSSE(bus)
	s.RegisterUserSSE(bus)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events?user_id=U-1", nil).WithContext(ctx)
	w := newSignalRecorder()
	done := make(chan struct{})
	go func() { s.sse(w, req); close(done) }()

	select {
	case got := <-bus.subjU:
		if got != "U-1" {
			t.Fatalf("SubscribeUser called with %q, want U-1", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SubscribeUser not called within timeout")
	}
	assertNoSessionSubscribe(t, bus)

	bus.ch <- agent.Event{Name: agent.EventReminderTriggered, Payload: map[string]any{"ref_id": "q-1"}}
	select {
	case <-w.wrote:
	case <-time.After(2 * time.Second):
		t.Fatal("SSE handler did not write event")
	}
	cancel()
	<-done
	if !strings.Contains(w.Body.String(), "reminder:triggered") {
		t.Fatalf("SSE body missing reminder:triggered: %q", w.Body.String())
	}
}

// TestSSESessionIDRegression: 既有 session_id 流保持可用（回归保护）。
func TestSSESessionIDRegression(t *testing.T) {
	bus := newFakeEventBus()
	s := NewServer()
	s.RegisterSSE(bus)
	s.RegisterUserSSE(bus)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events?session_id=S-1", nil).WithContext(ctx)
	w := newSignalRecorder()
	done := make(chan struct{})
	go func() { s.sse(w, req); close(done) }()

	select {
	case got := <-bus.subj:
		if got != "S-1" {
			t.Fatalf("Subscribe(session) called with %q, want S-1", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe (session) not called within timeout")
	}
	assertNoUserSubscribe(t, bus)

	bus.ch <- agent.Event{Name: "agent:completed", Payload: map[string]any{"message_id": "m-1"}}
	select {
	case <-w.wrote:
	case <-time.After(2 * time.Second):
		t.Fatal("SSE handler did not write event")
	}
	cancel()
	<-done
	if !strings.Contains(w.Body.String(), "agent:completed") {
		t.Fatalf("SSE body missing agent:completed: %q", w.Body.String())
	}
}

// TestSSEUserIDPriority: user_id 与 session_id 并存时 user_id 优先。
func TestSSEUserIDPriority(t *testing.T) {
	bus := newFakeEventBus()
	s := NewServer()
	s.RegisterSSE(bus)
	s.RegisterUserSSE(bus)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events?user_id=U-9&session_id=S-9", nil).WithContext(ctx)
	w := newSignalRecorder()
	done := make(chan struct{})
	go func() { s.sse(w, req); close(done) }()

	select {
	case got := <-bus.subjU:
		if got != "U-9" {
			t.Fatalf("SubscribeUser called with %q, want U-9 (user_id takes priority)", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SubscribeUser not called within timeout")
	}
	assertNoSessionSubscribe(t, bus)

	cancel()
	<-done
}

// TestSSEMissingParamsBadRequest: 无 session_id 且无 user_id → 400。
func TestSSEMissingParamsBadRequest(t *testing.T) {
	s := NewServer()
	s.RegisterSSE(newFakeEventBus())
	s.RegisterUserSSE(newFakeEventBus())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
	w := httptest.NewRecorder()
	s.sse(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "必填") {
		t.Fatalf("400 body should mention required param: %q", w.Body.String())
	}
}

// TestSSESessionBusUnregistered: session_id 但 session 总线未注册 → 503（既有行为）。
func TestSSESessionBusUnregistered(t *testing.T) {
	s := NewServer()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events?session_id=S-1", nil)
	w := httptest.NewRecorder()
	s.sse(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

// TestSSEUserBusUnregistered: user_id 但用户总线未注册 → 503。
func TestSSEUserBusUnregistered(t *testing.T) {
	s := NewServer()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events?user_id=U-1", nil)
	w := httptest.NewRecorder()
	s.sse(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

// TestSSEUserEventIntegration: 端到端冒烟 —— 真实 UserEventBus 接入真实 mux，
// 通过 HTTP 层 SSE 订阅 user_id 收到 grading:updated（含 grading_id/status/score），
// 且 notifications 表落库。
func TestSSEUserEventIntegration(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "sse-e2e.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	repo := repository.New(db)
	// notifications.user_id 有 FK，需先建真实用户
	ws, user := "ws-e2e", "user-e2e"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, owner_type) VALUES (?, '测试', 'local')`, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, workspace_id, display_name) VALUES (?, ?, '学生')`, user, ws); err != nil {
		t.Fatal(err)
	}

	bus := agent.NewUserEventBus(repo)
	s := NewServer()
	s.RegisterUserSSE(bus)

	srv := httptest.NewServer(s.Mux())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events?user_id="+user, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SSE status = %d, want 200", resp.StatusCode)
	}

	if err := bus.Publish(user, agent.Event{
		Name: agent.EventGradingUpdated,
		Payload: map[string]any{
			"request_id": "req-e2e", "grading_id": "g-9", "status": "completed", "score": 88,
		},
	}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	got := ""
	for time.Now().Before(deadline) {
		buf := make([]byte, 4096)
		n, _ := resp.Body.Read(buf)
		got += string(buf[:n])
		if strings.Contains(got, "grading:updated") {
			break
		}
	}
	if !strings.Contains(got, "grading:updated") {
		t.Fatalf("SSE stream missing grading:updated: %q", got)
	}
	for _, want := range []string{`"grading_id":"g-9"`, `"status":"completed"`, `"score":88`, `"sequence_no":1`, "occurred_at"} {
		if !strings.Contains(got, want) {
			t.Fatalf("SSE payload missing %s: %q", want, got)
		}
	}

	// notifications 表已落库
	var kind string
	var refID sql.NullString
	if err := db.QueryRow(
		`SELECT kind, ref_id FROM notifications WHERE user_id = ?`, user).Scan(&kind, &refID); err != nil {
		t.Fatalf("notifications row missing: %v", err)
	}
	if kind != agent.EventGradingUpdated {
		t.Errorf("notifications.kind = %q, want %q", kind, agent.EventGradingUpdated)
	}
	if refID.Valid {
		t.Errorf("grading:updated must not set ref_id (no ref in payload), got %q", refID.String)
	}
}
