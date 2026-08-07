package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"lumo/internal/agent"
)

// mockCloud 模拟 cloud-server：记录请求头并返回可配置的逐项结果。
type mockCloud struct {
	ts        *httptest.Server
	conflicts map[string]bool // "*" 表示全部冲突
	pushCount atomic.Int64
	lastAuth  atomic.Value // string
	lastDev   atomic.Value // string
	lastBody  atomic.Value // string
}

func newMockCloud(t *testing.T, conflicts map[string]bool) *mockCloud {
	t.Helper()
	m := &mockCloud{conflicts: conflicts}
	m.ts = httptest.NewServer(http.HandlerFunc(m.handle))
	t.Cleanup(m.ts.Close)
	return m
}

func (m *mockCloud) handle(w http.ResponseWriter, r *http.Request) {
	m.lastAuth.Store(r.Header.Get("Authorization"))
	m.lastDev.Store(r.Header.Get("X-Device-ID"))
	if r.URL.Path == "/v1/devices" && r.Method == http.MethodPost {
		_, _ = w.Write([]byte(`{"device_id":"local-web","status":"registered","server_time":"2026-08-08T00:00:00Z","workspace":"","cursor":0}`))
		return
	}
	if r.URL.Path == "/v1/sync/push" && r.Method == http.MethodPost {
		m.pushCount.Add(1)
		b, _ := io.ReadAll(r.Body)
		m.lastBody.Store(string(b))
		var body struct {
			WorkspaceID string `json:"workspace_id"`
			Operations  []struct {
				OperationID string `json:"operation_id"`
			} `json:"operations"`
		}
		_ = json.Unmarshal(b, &body)
		var items []map[string]any
		for _, op := range body.Operations {
			conflict := m.conflicts["*"] || m.conflicts[op.OperationID]
			if conflict {
				items = append(items, map[string]any{
					"operation_id": op.OperationID, "result": "conflict",
					"conflict_copy": map[string]any{"conflict_of": op.OperationID, "server_version": 2, "local_payload": map[string]any{}},
				})
			} else {
				items = append(items, map[string]any{
					"operation_id": op.OperationID, "result": "accepted", "server_sequence": 1, "server_version": 1,
				})
			}
		}
		resp, _ := json.Marshal(map[string]any{"workspace_id": body.WorkspaceID, "items": items, "server_time": "2026-08-08T00:00:00Z"})
		_, _ = w.Write(resp)
		return
	}
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND","message":"not found"}}`))
}

func TestSyncCloudPushHappy(t *testing.T) {
	mock := newMockCloud(t, nil)
	s, cfg := newTestServices(t)
	cfg.CloudServerToken = "test-token"
	cfg.CloudServerURL = mock.ts.URL
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)

	// 记录本地 pending 操作
	if err := s.Sync.RecordReviewSyncOp(ctx, ws.ID, "card-1", 1, map[string]any{"rating": "good"}); err != nil {
		t.Fatal(err)
	}

	res, err := s.Sync.SyncCloudPush(ctx, SyncCloudPushReq{WorkspaceID: ws.ID, UserID: userID})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || res.Items[0].Result != "accepted" {
		t.Fatalf("unexpected push result: %+v", res.Items)
	}
	// 请求头携带 Bearer token 与 X-Device-ID
	if got := mock.lastAuth.Load().(string); got != "Bearer test-token" {
		t.Fatalf("Authorization = %q，期望 Bearer test-token", got)
	}
	if got := mock.lastDev.Load().(string); got != "local-web" {
		t.Fatalf("X-Device-ID = %q", got)
	}
	// 请求体含 workspace_id 与 operations
	body := mock.lastBody.Load().(string)
	if !strings.Contains(body, `"workspace_id"`) || !strings.Contains(body, `"operations"`) {
		t.Fatalf("请求体异常: %s", body)
	}
	// 本地状态回写 accepted → pending 清空
	status, _ := s.Sync.SyncStatusGet(ctx, SyncStatusGetReq{WorkspaceID: ws.ID})
	if status.Pending != 0 {
		t.Fatalf("期望 pending 清空，got %d", status.Pending)
	}
}

func TestSyncCloudPushNotConfigured(t *testing.T) {
	s, cfg := newTestServices(t)
	cfg.CloudServerToken = "" // 未配置 CLOUD_SERVER_TOKEN
	cfg.CloudServerURL = "http://127.0.0.1:1"
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)
	if err := s.Sync.RecordReviewSyncOp(ctx, ws.ID, "card-1", 1, map[string]any{"rating": "good"}); err != nil {
		t.Fatal(err)
	}
	_, err := s.Sync.SyncCloudPush(ctx, SyncCloudPushReq{WorkspaceID: ws.ID})
	if err == nil {
		t.Fatal("未配置 token 应返回错误")
	}
	if !strings.Contains(err.Error(), "CLOUD_SERVER_TOKEN") {
		t.Fatalf("错误信息应提示未配置 token: %v", err)
	}
	if !strings.Contains(err.Error(), "FEATURE_DISABLED") {
		t.Fatalf("错误应为 FEATURE_DISABLED: %v", err)
	}
}

func TestSyncCloudPushConflictPublishesEvent(t *testing.T) {
	mock := newMockCloud(t, map[string]bool{"*": true}) // 全部返回 conflict
	s, cfg := newTestServices(t)
	cfg.CloudServerToken = "test-token"
	cfg.CloudServerURL = mock.ts.URL
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)

	ch, cancel := s.UserEvents.SubscribeUser(userID)
	defer cancel()

	if err := s.Sync.RecordReviewSyncOp(ctx, ws.ID, "card-1", 1, map[string]any{"rating": "good"}); err != nil {
		t.Fatal(err)
	}
	res, err := s.Sync.SyncCloudPush(ctx, SyncCloudPushReq{WorkspaceID: ws.ID, UserID: userID})
	if err != nil {
		t.Fatal(err)
	}
	if res.Items[0].Result != "conflict" {
		t.Fatalf("期望 conflict: %+v", res.Items)
	}
	// 本地状态回写 conflict
	ops, err := s.Repo.ListSyncOps(ctx, ws.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].State != "conflict" {
		t.Fatalf("本地状态应回写 conflict: %+v", ops)
	}
	// 发布 sync:extended 用户事件（payload 含 entity_type/conflict_count）
	select {
	case ev := <-ch:
		if ev.Name != agent.EventSyncExtended {
			t.Fatalf("事件名 = %s，期望 %s", ev.Name, agent.EventSyncExtended)
		}
		if ev.Payload["entity_type"] != "review_card" {
			t.Fatalf("entity_type = %v", ev.Payload["entity_type"])
		}
		if ev.Payload["conflict_count"] != 1 {
			t.Fatalf("conflict_count = %v，期望 1", ev.Payload["conflict_count"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未收到 sync:extended 事件")
	}
}

func TestCloudClientRejectsWorkspaceDeleting(t *testing.T) {
	mock := newMockCloud(t, nil)
	// 覆写 push 返回 409 CONFLICT（模拟删除期）
	old := mock.ts.Config.Handler
	mock.ts.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/sync/push" {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"CONFLICT","message":"工作区处于删除期，拒绝新的写操作"}}`))
			return
		}
		old.ServeHTTP(w, r)
	})
	s, cfg := newTestServices(t)
	cfg.CloudServerToken = "test-token"
	cfg.CloudServerURL = mock.ts.URL
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)
	if err := s.Sync.RecordReviewSyncOp(ctx, ws.ID, "card-1", 1, map[string]any{"rating": "good"}); err != nil {
		t.Fatal(err)
	}
	_, err := s.Sync.SyncCloudPush(ctx, SyncCloudPushReq{WorkspaceID: ws.ID})
	if err == nil {
		t.Fatal("删除期 push 应返回错误")
	}
	if !strings.Contains(err.Error(), "CONFLICT") {
		t.Fatalf("错误应为 CONFLICT: %v", err)
	}
}
