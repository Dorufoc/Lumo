// cloud_client.go —— 客户端 cloud-server HTTP 客户端（Todo 34）。
// 与 cmd/cloud-server 二进制按 API 设计文档第 4 章契约通信：token 来源与 cloud-server
// 同源（环境变量 CLOUD_SERVER_TOKEN，经 config.Config.CloudServerToken 暴露）。
// 未配置 token 时 SyncCloudPush 返回 FEATURE_DISABLED，客户端回退 in-process SyncService。
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"lumo/internal/agent"
	"lumo/internal/domain"
)

// cloudTokenConfigured 判断客户端是否配置了云同步访问令牌。
func (s *Services) cloudTokenConfigured() bool {
	return s.Cfg.CloudServerToken != ""
}

// CloudServerStatus 描述云同步服务端配置状态（前端设置页展示用，不返回 token 本身）。
type CloudServerStatus struct {
	Configured bool   `json:"configured"` // 是否配置 CLOUD_SERVER_TOKEN
	Mode       string `json:"mode"`       // inprocess | cloud（未配置时强制回退 inprocess）
}

// CloudClient 是 cloud-server 的 HTTP 客户端（API 文档第 4 章契约）。
type CloudClient struct {
	BaseURL  string
	Token    string
	DeviceID string
	HTTP     *http.Client
}

// NewCloudClient 构造 cloud-server 客户端。
func NewCloudClient(baseURL, token, deviceID string) *CloudClient {
	return &CloudClient{
		BaseURL: baseURL, Token: token, DeviceID: deviceID,
		HTTP: &http.Client{Timeout: 15 * time.Second},
	}
}

// cloudErr 是 cloud-server 返回的错误信封。
type cloudErr struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// do 发送 JSON 请求并解析响应（或错误信封为领域错误）。
func (c *CloudClient) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("X-Device-ID", c.DeviceID)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return domain.WrapError(domain.CodeInternal, "无法连接云同步服务", err)
	}
	defer resp.Body.Close()
	rb, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		var env struct {
			Error cloudErr `json:"error"`
		}
		_ = json.Unmarshal(rb, &env)
		if env.Error.Message == "" {
			env.Error.Message = fmt.Sprintf("云同步服务返回 %d", resp.StatusCode)
		}
		return cloudErrorToDomain(env.Error)
	}
	if out != nil {
		return json.Unmarshal(rb, out)
	}
	return nil
}

// cloudErrorToDomain 将 cloud-server 错误码映射回领域错误（复用既有 24 个稳定错误码）。
func cloudErrorToDomain(ce cloudErr) error {
	switch ce.Code {
	case "UNAUTHORIZED":
		return domain.Unauthorized("%s", ce.Message)
	case "INVALID_ARGUMENT":
		return domain.InvalidArg("%s", ce.Message)
	case "CONFLICT":
		return domain.Conflict("%s", ce.Message)
	case "NOT_FOUND":
		return domain.NotFound("%s", ce.Message)
	case "SERVICE_UNAVAILABLE", "FEATURE_DISABLED":
		return domain.FeatureDisabled("%s", ce.Message)
	default:
		return domain.NewError(domain.CodeInternal, ce.Message)
	}
}

// RegisterDevice 注册设备（幂等；重复注册返回 already_registered 非错误）。
func (c *CloudClient) RegisterDevice(ctx context.Context, req SyncDeviceRegisterReq) (*DeviceStatus, error) {
	var out DeviceStatus
	if err := c.do(ctx, http.MethodPost, "/v1/devices", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Push 推送变更（逐项 accepted/duplicate/conflict/rejected）。
func (c *CloudClient) Push(ctx context.Context, wsID string, ops []SyncOpDTO) (*PushResult, error) {
	var out PushResult
	if err := c.do(ctx, http.MethodPost, "/v1/sync/push", map[string]any{
		"workspace_id": wsID, "operations": ops,
	}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Pull 按游标拉取变更。
func (c *CloudClient) Pull(ctx context.Context, wsID string, cursor int64, limit int) (*PullResult, error) {
	var out PullResult
	path := fmt.Sprintf("/v1/sync/pull?workspace_id=%s&cursor=%d&limit=%d", wsID, cursor, limit)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SyncCloudPushReq 推送本地队列到 cloud-server 请求。
type SyncCloudPushReq struct {
	WorkspaceID string `json:"workspace_id"`
	// UserID 可选：冲突时用于发布 sync:extended 用户事件（空则跳过发布）。
	UserID string `json:"user_id,omitempty"`
}

// SyncCloudPush 将本地 pending 队列推送到 cloud-server（替代 in-process 模拟服务端）。
// 未配置 CLOUD_SERVER_TOKEN 时返回 FEATURE_DISABLED（设置页提示并回退 in-process）。
// 冲突发生时发布 EventSyncExtended（sync:extended，payload 含 entity_type/conflict_count）。
// 逐项回写本地状态语义与 SyncPushLocal 完全一致。
func (y *SyncService) SyncCloudPush(ctx context.Context, req SyncCloudPushReq) (*PushResult, error) {
	if err := y.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if !y.s.cloudTokenConfigured() {
		return nil, domain.FeatureDisabled("未配置 CLOUD_SERVER_TOKEN，云同步不可用，已回退本地同步")
	}
	ops, err := y.s.Repo.ListPendingSyncOps(ctx, req.WorkspaceID, 500)
	if err != nil {
		return nil, err
	}
	// 设备身份：与 in-process 注册一致的本地固定设备 ID。
	deviceID := "local-web"
	client := NewCloudClient(y.s.Cfg.CloudServerURL, y.s.Cfg.CloudServerToken, deviceID)
	if _, err := client.RegisterDevice(ctx, SyncDeviceRegisterReq{
		DeviceID: deviceID, DeviceName: "本地浏览器", Platform: "web", AppVersion: "2.0.0",
	}); err != nil {
		return nil, err
	}
	if len(ops) == 0 {
		return &PushResult{WorkspaceID: req.WorkspaceID, ServerTime: Now()}, nil
	}
	entityTypes := make(map[string]string, len(ops))
	dtos := make([]SyncOpDTO, 0, len(ops))
	for _, op := range ops {
		entityTypes[op.OperationID] = op.EntityType
		dtos = append(dtos, SyncOpDTO{
			OperationID: op.OperationID, EntityType: op.EntityType, EntityID: op.EntityID,
			BaseVersion: op.BaseVersion, Operation: op.Operation,
			Payload: op.Payload, CreatedAt: op.CreatedAt,
		})
	}
	res, err := client.Push(ctx, req.WorkspaceID, dtos)
	if err != nil {
		return nil, err
	}
	// 回写本地状态（与 SyncPushLocal 一致：accepted/duplicate → accepted，conflict → conflict，其余 rejected）。
	conflictCount := 0
	var conflictEntityType string
	for _, item := range res.Items {
		state := "rejected"
		switch item.Result {
		case "accepted", "duplicate":
			state = "accepted"
		case "conflict":
			state = "conflict"
			conflictCount++
			if conflictEntityType == "" {
				conflictEntityType = entityTypes[item.OperationID]
			}
		}
		_ = y.s.Repo.UpdateSyncOpState(ctx, item.OperationID, state, item.ServerSeq)
	}
	// 冲突/扩展同步 → 发布 sync:extended 用户事件（仅在有 user_id 时）。
	if conflictCount > 0 && req.UserID != "" {
		_ = y.s.UserEvents.Publish(req.UserID, agent.Event{
			Name: agent.EventSyncExtended,
			Payload: map[string]any{
				"entity_type": conflictEntityType, "conflict_count": conflictCount,
			},
		})
	}
	y.s.audit(ctx, req.WorkspaceID, "sync.push", "workspace", req.WorkspaceID,
		map[string]any{"mode": "cloud", "total": len(res.Items), "conflicts": conflictCount})
	return res, nil
}

// cloudServerStatus 计算云同步状态（配置 + 有效模式）。
// 模式取自工作区设置 sync_mode；未配置 token 时强制回退 inprocess。
func (s *Services) cloudServerStatus(settingsJSON json.RawMessage) CloudServerStatus {
	configured := s.cloudTokenConfigured()
	mode := "inprocess"
	var raw map[string]any
	if json.Unmarshal(settingsJSON, &raw) == nil {
		if v, ok := raw["sync_mode"].(string); ok && v == "cloud" {
			mode = "cloud"
		}
	}
	if !configured {
		mode = "inprocess"
	}
	return CloudServerStatus{Configured: configured, Mode: mode}
}
