package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"lumo/internal/domain"
)

// TestProviderConfigureLocalKeyless 校验本地 Provider 免密钥配置：
// kind=local 无需 api_key 即可启用，且 secret 不入库。
func TestProviderConfigureLocalKeyless(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	ctx := context.Background()

	status, err := s.ProviderConfigure(ctx, ProviderConfigureReq{
		WorkspaceID: ws.ID, Provider: "llm", Kind: "local", Enabled: true,
	})
	if err != nil {
		t.Fatalf("configure local llm: %v", err)
	}
	st, ok := status["llm"]
	if !ok || !st.Configured {
		t.Fatalf("llm should be configured after local configure: %+v", status)
	}

	// 配置读取：无 api_key 也返回 ok=true。
	cfg, ok := s.providerConfig("llm")
	if !ok {
		t.Fatal("providerConfig should see local as configured without api_key")
	}
	if cfg["kind"] != "local" {
		t.Fatalf("expected kind local, got %v", cfg["kind"])
	}
	if cfg["base_url"] != "" {
		t.Fatalf("expected empty base_url (default), got %v", cfg["base_url"])
	}

	// 读取 secrets 文件：不应包含 api_key 字段。
	b, err := os.ReadFile(s.Cfg.SecretsPath)
	if err != nil {
		t.Fatalf("read secrets: %v", err)
	}
	if strings.Contains(string(b), "api_key") {
		t.Fatalf("local provider secret should not be stored: %s", string(b))
	}
}

// TestProviderConfigureLocalPersistsBaseURL 校验本地端点可配且仅存端点不存密钥。
func TestProviderConfigureLocalPersistsBaseURL(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	ctx := context.Background()

	if _, err := s.ProviderConfigure(ctx, ProviderConfigureReq{
		WorkspaceID: ws.ID, Provider: "llm", Kind: "local",
		BaseURL: "http://127.0.0.1:11434/v1", Model: "qwen2.5", Enabled: true,
	}); err != nil {
		t.Fatalf("configure local llm: %v", err)
	}
	cfg, ok := s.providerConfig("llm")
	if !ok {
		t.Fatal("providerConfig should see local as configured")
	}
	if cfg["base_url"] != "http://127.0.0.1:11434/v1" {
		t.Fatalf("base_url mismatch: %v", cfg["base_url"])
	}
	if cfg["model"] != "qwen2.5" {
		t.Fatalf("model mismatch: %v", cfg["model"])
	}
}

// TestProviderConfigureLocalIgnoresKey 校验本地 Provider 即使传入 api_key 也不入库。
func TestProviderConfigureLocalIgnoresKey(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	ctx := context.Background()

	if _, err := s.ProviderConfigure(ctx, ProviderConfigureReq{
		WorkspaceID: ws.ID, Provider: "llm", Kind: "local",
		BaseURL: "http://127.0.0.1:11434/v1", APIKey: "sk-should-not-persist", Enabled: true,
	}); err != nil {
		t.Fatalf("configure local llm: %v", err)
	}
	b, err := os.ReadFile(s.Cfg.SecretsPath)
	if err != nil {
		t.Fatalf("read secrets: %v", err)
	}
	if strings.Contains(string(b), "sk-should-not-persist") {
		t.Fatalf("local provider api_key should never be stored: %s", string(b))
	}
}

// TestProviderTestLocalReachable 校验本地端点可达时 ProviderTest 返回 OK:true。
func TestProviderTestLocalReachable(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	ctx := context.Background()

	// 模拟本地 Ollama 兼容端点。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	defer srv.Close()

	if _, err := s.ProviderConfigure(ctx, ProviderConfigureReq{
		WorkspaceID: ws.ID, Provider: "llm", Kind: "local", BaseURL: srv.URL, Enabled: true,
	}); err != nil {
		t.Fatalf("configure local llm: %v", err)
	}
	h, err := s.ProviderTest(ctx, ProviderTestReq{WorkspaceID: ws.ID, Provider: "llm"})
	if err != nil {
		t.Fatalf("provider test: %v", err)
	}
	if !h.OK {
		t.Fatalf("expected OK for reachable local endpoint: %+v", h)
	}
}

// TestProviderTestLocalUnreachable 校验本地端点不可达时 ProviderTest 返回 OK:false 且 Error 非空。
func TestProviderTestLocalUnreachable(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	ctx := context.Background()

	// 端点不可达（立即关闭的连接）。
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	if _, err := s.ProviderConfigure(ctx, ProviderConfigureReq{
		WorkspaceID: ws.ID, Provider: "llm", Kind: "local", BaseURL: deadURL, Enabled: true,
	}); err != nil {
		t.Fatalf("configure local llm: %v", err)
	}
	h, err := s.ProviderTest(ctx, ProviderTestReq{WorkspaceID: ws.ID, Provider: "llm"})
	if err != nil {
		t.Fatalf("provider test should return health not error: %v", err)
	}
	if h.OK {
		t.Fatal("expected OK:false for unreachable local endpoint")
	}
	if h.Error == "" {
		t.Fatal("expected non-empty Error for unreachable local endpoint")
	}
}

// TestProviderConfigureLocalEmbedding 校验 embedding-local 同样免密钥。
func TestProviderConfigureLocalEmbedding(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	ctx := context.Background()

	status, err := s.ProviderConfigure(ctx, ProviderConfigureReq{
		WorkspaceID: ws.ID, Provider: "embedding", Kind: "local", Enabled: true,
	})
	if err != nil {
		t.Fatalf("configure local embedding: %v", err)
	}
	if st, ok := status["embedding"]; !ok || !st.Configured {
		t.Fatalf("embedding should be configured: %+v", status)
	}
	if _, ok := s.providerConfig("embedding"); !ok {
		t.Fatal("providerConfig should see embedding local as configured")
	}
}

// TestProviderConfigureBadKindRejected 校验非法 kind 仍被拒绝（封闭错误码）。
func TestProviderConfigureBadKindRejected(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	_, err := s.ProviderConfigure(context.Background(), ProviderConfigureReq{
		WorkspaceID: ws.ID, Provider: "llm", Kind: "bogus", Enabled: true,
	})
	e := domain.AsError(err)
	if e == nil || e.Code != domain.CodeInvalidArgument {
		t.Fatalf("expected INVALID_ARG for bogus kind, got %v", err)
	}
}

// TestProviderStatusLocalConfigured 校验 SettingsGet 的 provider_status 反映 local 已配置。
func TestProviderStatusLocalConfigured(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	ctx := context.Background()
	if _, err := s.ProviderConfigure(ctx, ProviderConfigureReq{
		WorkspaceID: ws.ID, Provider: "llm", Kind: "local", Enabled: true,
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	got, err := s.Settings.SettingsGet(ctx, SettingsGetReq{WorkspaceID: ws.ID})
	if err != nil {
		t.Fatalf("settings get: %v", err)
	}
	if st, ok := got.ProviderStatus["llm"]; !ok || !st.Configured {
		t.Fatalf("provider_status should mark llm configured: %+v", got.ProviderStatus)
	}
	_ = json.Valid
}
