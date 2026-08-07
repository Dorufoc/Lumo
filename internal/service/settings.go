package service

import (
	"context"
	"encoding/json"
	"os"

	"lumo/internal/domain"
)

// Settings 是设置 DTO：非敏感配置 + Provider 配置状态。
type Settings struct {
	WorkspaceID    string                    `json:"workspace_id"`
	Settings       json.RawMessage           `json:"settings"`
	ProviderStatus map[string]ProviderStatus `json:"provider_status"`
	Version        int                       `json:"version"`
}

// ProviderStatus 描述 Provider 配置状态（不返回密钥）。
type ProviderStatus struct {
	Configured bool   `json:"configured"`
	Model      string `json:"model,omitempty"`
}

// SettingsService 实现设置用例。
type SettingsService struct{ s *Services }

// SettingsGetReq 获取设置请求。
type SettingsGetReq struct {
	WorkspaceID string `json:"workspace_id"`
}

// SettingsGet 获取工作区设置与 Provider 配置状态。
func (sv *SettingsService) SettingsGet(ctx context.Context, req SettingsGetReq) (*Settings, error) {
	if err := sv.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	row, err := sv.s.Repo.GetSettings(ctx, req.WorkspaceID)
	if err != nil {
		return nil, err
	}
	status, err := sv.s.providerStatus()
	if err != nil {
		return nil, err
	}
	return &Settings{
		WorkspaceID:    req.WorkspaceID,
		Settings:       row.Settings,
		ProviderStatus: status,
		Version:        row.Version,
	}, nil
}

// SettingsUpdateReq 更新设置请求（不接受密钥回读字段）。
type SettingsUpdateReq struct {
	WorkspaceID string          `json:"workspace_id"`
	Version     int             `json:"version"`
	Settings    json.RawMessage `json:"settings"`
}

// SettingsUpdate 更新设置（乐观锁；密钥仅通过 ProviderConfigure 写入）。
func (sv *SettingsService) SettingsUpdate(ctx context.Context, req SettingsUpdateReq) (*Settings, error) {
	if err := sv.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if len(req.Settings) == 0 {
		return nil, domain.InvalidArg("settings 必填")
	}
	if !json.Valid(req.Settings) {
		return nil, domain.InvalidArg("settings 不是合法 JSON")
	}
	row, err := sv.s.Repo.UpdateSettings(ctx, req.WorkspaceID, req.Version, req.Settings)
	if err != nil {
		return nil, err
	}
	sv.s.audit(ctx, req.WorkspaceID, "settings.update", "settings", req.WorkspaceID, nil)
	status, err := sv.s.providerStatus()
	if err != nil {
		return nil, err
	}
	return &Settings{
		WorkspaceID:    req.WorkspaceID,
		Settings:       row.Settings,
		ProviderStatus: status,
		Version:        row.Version,
	}, nil
}

// providerStatus 从 secrets 文件读取 Provider 配置状态（只读，不返回密钥）。
func (s *Services) providerStatus() (map[string]ProviderStatus, error) {
	status := map[string]ProviderStatus{}
	b, err := readSecretsFile(s.Cfg.SecretsPath)
	if err != nil {
		return nil, err
	}
	for _, name := range []string{"llm", "embedding", "tts", "asr"} {
		v, ok := b["provider_"+name]
		if !ok {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(v), &m) != nil {
			continue
		}
		st := ProviderStatus{Configured: m["api_key"] != nil && m["api_key"] != ""}
		if kind, ok := m["kind"].(string); ok && kind == "mock" {
			st.Configured = true // mock 无需密钥即视为已配置
		}
		if model, ok := m["model"].(string); ok {
			st.Model = model
		}
		status[name] = st
	}
	return status, nil
}

// providerConfig 返回某个 Provider 的配置（含密钥，仅内部使用）。
func (s *Services) providerConfig(name string) (map[string]any, bool) {
	b, err := readSecretsFile(s.Cfg.SecretsPath)
	if err != nil {
		return nil, false
	}
	v, ok := b["provider_"+name]
	if !ok {
		return nil, false
	}
	var m map[string]any
	if json.Unmarshal([]byte(v), &m) != nil {
		return nil, false
	}
	if m["api_key"] == nil || m["api_key"] == "" {
		return nil, false
	}
	return m, true
}

// ProviderConfigOf 公开 Provider 配置读取（app 层注入 LLM 工厂使用）。
func (s *Services) ProviderConfigOf(name string) (map[string]any, bool) {
	return s.providerConfig(name)
}

// saveProviderConfig 写入 Provider 配置（合并式，保留其他字段）。
func (s *Services) saveProviderConfig(name string, m map[string]any) error {
	b, err := readSecretsFile(s.Cfg.SecretsPath)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(m)
	b["provider_"+name] = string(raw)
	return writeSecretsFile(s.Cfg.SecretsPath, b)
}

// clearProviderConfig 删除 Provider 配置。
func (s *Services) clearProviderConfig(name string) error {
	b, err := readSecretsFile(s.Cfg.SecretsPath)
	if err != nil {
		return err
	}
	delete(b, "provider_"+name)
	return writeSecretsFile(s.Cfg.SecretsPath, b)
}

// readSecretsFile 读取 secrets 文件（不存在时返回空 map）。
func readSecretsFile(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	var m map[string]string
	if len(b) > 0 {
		_ = json.Unmarshal(b, &m)
	}
	if m == nil {
		m = map[string]string{}
	}
	return m, nil
}

// writeSecretsFile 写入 secrets 文件（0600）。
func writeSecretsFile(path string, m map[string]string) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
