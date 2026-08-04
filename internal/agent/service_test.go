package agent

import (
	"context"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"lumo/internal/database"
	"lumo/internal/provider"
	"lumo/internal/repository"
)

func newTestAgent(t *testing.T, mockLLM bool) *Service {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	repo := repository.New(db)
	s := New(repo)
	if mockLLM {
		s.LLMFactory = func() (provider.LLMProvider, error) {
			return provider.NewLLM("mock", map[string]any{})
		}
	}
	return s
}

func setupWorkspace(t *testing.T, s *Service) (string, string) {
	t.Helper()
	db := s.Repo.DB()
	ws := newID()
	user := newID()
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, owner_type) VALUES (?, '测试', 'local')`, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, workspace_id, display_name) VALUES (?, ?, '学生')`, user, ws); err != nil {
		t.Fatal(err)
	}
	return ws, user
}

func TestAgentChatFallback(t *testing.T) {
	s := newTestAgent(t, false) // 未配置 LLM → 降级
	ws, user := setupWorkspace(t, s)
	ctx := context.Background()

	session, err := s.AgentChatCreate(ctx, AgentChatCreateReq{
		WorkspaceID: ws, UserID: user, Agent: "tutor", Context: "讲解题目",
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.Agent != "tutor" {
		t.Fatalf("unexpected agent: %s", session.Agent)
	}

	// 订阅事件
	ch, unsub := s.Events.Subscribe(session.ID)
	defer unsub()

	req, err := s.AgentChatSend(ctx, AgentChatSendReq{
		WorkspaceID: ws, SessionID: session.ID, Message: "请讲解这道题",
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.RequestID == "" {
		t.Fatal("request_id missing")
	}
	// 等待完成事件
	seenDelta := false
	seenDone := false
	for ev := range ch {
		if ev.Name == "agent:delta" {
			seenDelta = true
			payload := ev.Payload
			if d, ok := payload["delta"].(string); !ok || d == "" {
				t.Fatalf("empty delta: %v", payload)
			}
		}
		if ev.Name == "agent:completed" {
			seenDone = true
			break
		}
	}
	if !seenDelta || !seenDone {
		t.Fatalf("missing events: delta=%v done=%v", seenDelta, seenDone)
	}

	got, err := s.AgentSessionGet(ctx, AgentSessionGetReq{WorkspaceID: ws, SessionID: session.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "completed" {
		t.Fatalf("expected completed, got %s", got.Status)
	}
	if len(got.Messages) < 2 {
		t.Fatalf("expected user+assistant messages, got %d", len(got.Messages))
	}
}

func TestAgentChatWithMockLLM(t *testing.T) {
	s := newTestAgent(t, true)
	ws, user := setupWorkspace(t, s)
	ctx := context.Background()

	session, err := s.AgentChatCreate(ctx, AgentChatCreateReq{
		WorkspaceID: ws, UserID: user, Agent: "router",
	})
	if err != nil {
		t.Fatal(err)
	}
	ch, unsub := s.Events.Subscribe(session.ID)
	defer unsub()

	if _, err := s.AgentChatSend(ctx, AgentChatSendReq{
		WorkspaceID: ws, SessionID: session.ID, Message: "帮我规划学习",
	}); err != nil {
		t.Fatal(err)
	}
	done := false
	for ev := range ch {
		if ev.Name == "agent:completed" {
			done = true
			break
		}
	}
	if !done {
		t.Fatal("stream did not complete")
	}
}

func TestAgentCancelAndMemory(t *testing.T) {
	s := newTestAgent(t, false)
	ws, user := setupWorkspace(t, s)
	ctx := context.Background()

	// 记忆保存/列表/删除
	mem := &repository.AgentMemoryRow{
		ID: newID(), WorkspaceID: ws, UserID: user,
		MemoryType: "preference", Summary: "喜欢详细讲解", Consent: true,
	}
	if err := s.Repo.SaveAgentMemory(ctx, mem); err != nil {
		t.Fatal(err)
	}
	mems, err := s.AgentMemoryList(ctx, AgentMemoryListReq{WorkspaceID: ws, UserID: user})
	if err != nil {
		t.Fatal(err)
	}
	if len(mems) != 1 || mems[0].Summary != "喜欢详细讲解" {
		t.Fatalf("unexpected memory list: %+v", mems)
	}
	if _, err := s.AgentMemoryDelete(ctx, AgentMemoryDeleteReq{
		WorkspaceID: ws, MemoryID: mem.ID, Version: mems[0].Version,
	}); err != nil {
		t.Fatal(err)
	}
	mems, _ = s.AgentMemoryList(ctx, AgentMemoryListReq{WorkspaceID: ws, UserID: user})
	if len(mems) != 0 {
		t.Fatalf("memory should be deleted, got %d", len(mems))
	}

	// 取消一个不存在的请求（应返回 cancelled=false 而非错误）
	session, err := s.AgentChatCreate(ctx, AgentChatCreateReq{
		WorkspaceID: ws, UserID: user, Agent: "tutor",
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.AgentChatCancel(ctx, AgentChatCancelReq{
		WorkspaceID: ws, SessionID: session.ID, RequestID: "nonexistent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Cancelled {
		t.Fatal("expected cancelled=false for unknown request")
	}
}

func TestGraderHandlerStructured(t *testing.T) {
	s := newTestAgent(t, true) // mock LLM
	ws, user := setupWorkspace(t, s)

	// 通过会话走 grader handler
	session, err := s.AgentChatCreate(context.Background(), AgentChatCreateReq{
		WorkspaceID: ws, UserID: user, Agent: "grader",
	})
	if err != nil {
		t.Fatal(err)
	}
	ch, unsub := s.Events.Subscribe(session.ID)
	defer unsub()

	if _, err := s.AgentChatSend(context.Background(), AgentChatSendReq{
		WorkspaceID: ws, SessionID: session.ID,
		Message: `{"stem":"简述牛顿定律","answer":"学生答案","standard":"参考答案","rubric":"","max_score":10}`,
	}); err != nil {
		t.Fatal(err)
	}
	var captured string
	for ev := range ch {
		if ev.Name == "agent:delta" {
			captured += ev.Payload["delta"].(string)
		}
		if ev.Name == "agent:completed" {
			break
		}
	}
	// mock LLM JSONMode 输出 {"ok":true,...} —— 不是严格 score 结构，但验证流完整
	if captured == "" {
		t.Fatal("no output captured")
	}
}
