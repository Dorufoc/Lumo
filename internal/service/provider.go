package service

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"lumo/internal/domain"
	"lumo/internal/provider"
)

// ProviderHealth 是 Provider 连通性测试结果。
type ProviderHealth struct {
	OK        bool   `json:"ok"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	LatencyMs int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

// ProviderConfigureReq 配置 Provider 请求。
type ProviderConfigureReq struct {
	WorkspaceID string `json:"workspace_id"`
	Provider    string `json:"provider"` // llm | embedding | tts | asr
	BaseURL     string `json:"base_url"`
	APIKey      string `json:"api_key"`
	Model       string `json:"model"`
	Kind        string `json:"kind"` // openai | mock | local
	Enabled     bool   `json:"enabled"`
}

// ProviderConfigure 写入 Provider 配置（密钥存本地 secrets 文件，不回读）。
func (s *Services) ProviderConfigure(ctx context.Context, req ProviderConfigureReq) (map[string]ProviderStatus, error) {
	if err := s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if !isSupportedProvider(req.Provider) {
		return nil, domain.InvalidArg("provider 仅允许 llm/embedding/tts/asr")
	}
	kind := req.Kind
	if kind == "" {
		kind = "openai"
	}
	if kind != "openai" && kind != "mock" && kind != "local" {
		return nil, domain.InvalidArg("kind 仅允许 openai/mock/local")
	}
	// local 为本地端点，免 api_key；仅 openai 需密钥。
	if req.Enabled && kind == "openai" && req.APIKey == "" {
		return nil, domain.InvalidArg("启用 openai Provider 需要 api_key")
	}
	if !req.Enabled {
		if err := s.clearProviderConfig(req.Provider); err != nil {
			return nil, err
		}
		s.audit(ctx, req.WorkspaceID, "provider.disable", "provider", req.Provider, nil)
		return s.providerStatus()
	}
	// 保留旧 key（api_key 为空时）
	cfg := map[string]any{"kind": kind, "base_url": req.BaseURL, "model": req.Model}
	if old, ok := s.providerConfig(req.Provider); ok {
		if req.APIKey == "" {
			if k, ok2 := old["api_key"]; ok2 {
				cfg["api_key"] = k
			}
		}
		if req.BaseURL == "" {
			if b, ok2 := old["base_url"]; ok2 {
				cfg["base_url"] = b
			}
		}
		if req.Model == "" {
			if m, ok2 := old["model"]; ok2 {
				cfg["model"] = m
			}
		}
	}
	if req.APIKey != "" {
		cfg["api_key"] = req.APIKey
	}
	if err := s.saveProviderConfig(req.Provider, cfg); err != nil {
		return nil, err
	}
	s.audit(ctx, req.WorkspaceID, "provider.configure", "provider", req.Provider, nil)
	return s.providerStatus()
}

// ProviderTestReq 测试 Provider 请求。
type ProviderTestReq struct {
	WorkspaceID string `json:"workspace_id"`
	Provider    string `json:"provider"` // llm | embedding | tts | asr
	Model       string `json:"model"`
}

// ProviderTest 发送最小探测请求验证连通性。
func (s *Services) ProviderTest(ctx context.Context, req ProviderTestReq) (*ProviderHealth, error) {
	if err := s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if !isSupportedProvider(req.Provider) {
		return nil, domain.InvalidArg("provider 仅允许 llm/embedding/tts/asr")
	}
	cfg, ok := s.providerConfig(req.Provider)
	if req.Provider == "tts" || req.Provider == "asr" {
		// tts/asr 的 mock Provider 无需 api_key 即视为已配置
		cfg, ok = s.speechProviderConfig(req.Provider)
	}
	if !ok {
		return nil, domain.InvalidState("Provider 未配置，请先配置")
	}
	kind, _ := cfg["kind"].(string)
	if kind == "" {
		kind = "openai"
	}
	if req.Model != "" {
		cfg["model"] = req.Model
	}
	start := time.Now()
	switch req.Provider {
	case "llm":
		p, err := provider.NewLLM(kind, cfg)
		if err != nil {
			return nil, domain.InvalidArg("%v", err)
		}
		_, err = p.Chat(ctx, provider.ChatRequest{Messages: []provider.Message{{Role: "user", Content: "ping"}}, MaxTokens: 8}, nil)
		if err != nil {
			return &ProviderHealth{OK: false, Provider: req.Provider, Model: modelOf(cfg), LatencyMs: time.Since(start).Milliseconds(), Error: err.Error()}, nil
		}
	case "embedding":
		// 注册名约定：kind 需加 "embedding-" 前缀（与 tts-/asr- 一致）。
		embedKind := "embedding-" + kind
		p, err := provider.NewEmbedding(embedKind, cfg)
		if err != nil {
			return nil, domain.InvalidArg("%v", err)
		}
		_, err = p.Embed(ctx, []string{"ping"})
		if err != nil {
			return &ProviderHealth{OK: false, Provider: req.Provider, Model: modelOf(cfg), LatencyMs: time.Since(start).Milliseconds(), Error: err.Error()}, nil
		}
	case "tts":
		p, err := provider.NewTTS("tts-"+kind, cfg)
		if err != nil {
			return nil, domain.InvalidArg("%v", err)
		}
		_, _, err = p.Synthesize(ctx, "ping", 1.0)
		if err != nil {
			return &ProviderHealth{OK: false, Provider: req.Provider, Model: modelOf(cfg), LatencyMs: time.Since(start).Milliseconds(), Error: err.Error()}, nil
		}
	case "asr":
		p, err := provider.NewASR("asr-"+kind, cfg)
		if err != nil {
			return nil, domain.InvalidArg("%v", err)
		}
		probe, _, _ := (&provider.MockTTS{}).Synthesize(ctx, "ping", 1.0)
		tmp, err := os.CreateTemp("", "lumo-asr-probe-*.wav")
		if err != nil {
			return nil, domain.InvalidState("创建探测音频失败: %v", err)
		}
		tmpPath := tmp.Name()
		if _, err := tmp.Write(probe); err != nil {
			tmp.Close()
			_ = os.Remove(tmpPath)
			return nil, domain.InvalidState("写入探测音频失败: %v", err)
		}
		tmp.Close()
		defer os.Remove(tmpPath)
		_, err = p.Transcribe(ctx, tmpPath)
		if err != nil {
			return &ProviderHealth{OK: false, Provider: req.Provider, Model: modelOf(cfg), LatencyMs: time.Since(start).Milliseconds(), Error: err.Error()}, nil
		}
	}
	return &ProviderHealth{OK: true, Provider: req.Provider, Model: modelOf(cfg), LatencyMs: time.Since(start).Milliseconds()}, nil
}

// ProviderClearReq 清除 Provider 请求。
type ProviderClearReq struct {
	WorkspaceID string `json:"workspace_id"`
	Provider    string `json:"provider"`
}

// ProviderClear 删除 Provider 配置。
func (s *Services) ProviderClear(ctx context.Context, req ProviderClearReq) (map[string]ProviderStatus, error) {
	if err := s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if !isSupportedProvider(req.Provider) {
		return nil, domain.InvalidArg("provider 仅允许 llm/embedding/tts/asr")
	}
	if err := s.clearProviderConfig(req.Provider); err != nil {
		return nil, err
	}
	s.audit(ctx, req.WorkspaceID, "provider.clear", "provider", req.Provider, nil)
	return s.providerStatus()
}

// isSupportedProvider 判断是否受支持的 Provider 类型（llm/embedding/tts/asr）。
func isSupportedProvider(p string) bool {
	switch p {
	case "llm", "embedding", "tts", "asr":
		return true
	}
	return false
}

func modelOf(cfg map[string]any) string {
	if m, ok := cfg["model"].(string); ok {
		return m
	}
	return ""
}

var _ = json.Valid
