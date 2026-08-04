package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"lumo/internal/domain"
)

// DeviceInfo 是设备注册信息。
type DeviceInfo struct {
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name"`
	Platform   string `json:"platform"`
	AppVersion string `json:"app_version"`
	RegisteredAt string `json:"registered_at"`
}

// DeviceStatus 是注册结果。
type DeviceStatus struct {
	DeviceID   string `json:"device_id"`
	Status     string `json:"status"` // registered | already_registered
	ServerTime string `json:"server_time"`
	Workspace  string `json:"workspace"` // 工作区游标（简化为 0）
	Cursor     int64  `json:"cursor"`
}

// SyncOpDTO 是同步操作 DTO。
type SyncOpDTO struct {
	OperationID  string          `json:"operation_id"`
	EntityType   string          `json:"entity_type"`
	EntityID     string          `json:"entity_id"`
	BaseVersion  int             `json:"base_version"`
	Operation    string          `json:"operation"`
	Payload      json.RawMessage `json:"payload"`
	CreatedAt    string          `json:"created_at"`
}

// PushItemResult 是逐项推送结果。
type PushItemResult struct {
	OperationID   string          `json:"operation_id"`
	Result        string          `json:"result"` // accepted | duplicate | conflict | rejected
	ServerSeq     *int64          `json:"server_sequence,omitempty"`
	ServerVersion *int            `json:"server_version,omitempty"`
	ConflictCopy  json.RawMessage `json:"conflict_copy,omitempty"`
}

// PushResult 是推送结果。
type PushResult struct {
	WorkspaceID string           `json:"workspace_id"`
	Items       []PushItemResult `json:"items"`
	ServerTime  string           `json:"server_time"`
}

// PullResult 是拉取结果。
type PullResult struct {
	Operations  []SyncOpDTO `json:"operations"`
	NextCursor  int64       `json:"next_cursor"`
	HasMore     bool        `json:"has_more"`
	ServerTime  string      `json:"server_time"`
}

// SyncStatus 是同步状态。
type SyncStatus struct {
	WorkspaceID string `json:"workspace_id"`
	Pending     int    `json:"pending_count"`
	State       string `json:"state"` // idle | syncing | error
	LastError   string `json:"last_error"`
	DeviceID    string `json:"device_id"`
}

// SyncService 实现本地同步骨架：变更记录 + 模拟服务端（文件存储）。
type SyncService struct {
	s *Services

	mu sync.Mutex
}

// serverDir 返回模拟服务端数据目录。
func (y *SyncService) serverDir() string {
	return filepath.Join(y.s.Cfg.DataDir, "sim-server")
}

// ---------- 模拟服务端存储 ----------

type simServerState struct {
	Devices    map[string]DeviceInfo            `json:"devices"`
	Workspaces map[string]*simWorkspaceState    `json:"workspaces"`
}

type simWorkspaceState struct {
	Operations []simServerOp `json:"operations"`
}

type simServerOp struct {
	OperationID  string          `json:"operation_id"`
	EntityType   string          `json:"entity_type"`
	EntityID     string          `json:"entity_id"`
	BaseVersion  int             `json:"base_version"`
	Operation    string          `json:"operation"`
	Payload      json.RawMessage `json:"payload"`
	ServerSeq    int64           `json:"server_sequence"`
	ServerVersion int            `json:"server_version"`
	CreatedAt    string          `json:"created_at"`
}

func (y *SyncService) loadServer() (*simServerState, error) {
	dir := y.serverDir()
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return &simServerState{Devices: map[string]DeviceInfo{}, Workspaces: map[string]*simWorkspaceState{}}, nil
		}
		return nil, err
	}
	var st simServerState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	if st.Devices == nil {
		st.Devices = map[string]DeviceInfo{}
	}
	if st.Workspaces == nil {
		st.Workspaces = map[string]*simWorkspaceState{}
	}
	return &st, nil
}

func (y *SyncService) saveServer(st *simServerState) error {
	if err := os.MkdirAll(y.serverDir(), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	return os.WriteFile(filepath.Join(y.serverDir(), "state.json"), b, 0o600)
}

// ---------- API ----------

// SyncDeviceRegisterReq 设备注册请求。
type SyncDeviceRegisterReq struct {
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name"`
	Platform   string `json:"platform"`
	AppVersion string `json:"app_version"`
}

// SyncDeviceRegister 注册设备（本地模拟服务端）。
func (y *SyncService) SyncDeviceRegister(ctx context.Context, req SyncDeviceRegisterReq) (*DeviceStatus, error) {
	if req.DeviceID == "" {
		return nil, domain.InvalidArg("device_id 必填")
	}
	y.mu.Lock()
	defer y.mu.Unlock()
	st, err := y.loadServer()
	if err != nil {
		return nil, err
	}
	status := "registered"
	if _, exists := st.Devices[req.DeviceID]; exists {
		status = "already_registered"
	}
	st.Devices[req.DeviceID] = DeviceInfo{
		DeviceID: req.DeviceID, DeviceName: req.DeviceName,
		Platform: req.Platform, AppVersion: req.AppVersion,
		RegisteredAt: Now(),
	}
	if err := y.saveServer(st); err != nil {
		return nil, err
	}
	return &DeviceStatus{
		DeviceID: req.DeviceID, Status: status,
		ServerTime: Now(), Workspace: "", Cursor: 0,
	}, nil
}

// SyncPushReq 推送变更请求。
type SyncPushReq struct {
	WorkspaceID string       `json:"workspace_id"`
	Operations  []SyncOpDTO  `json:"operations"`
}

// SyncPush 推送本地变更到模拟服务端（逐项幂等/冲突）。
func (y *SyncService) SyncPush(ctx context.Context, req SyncPushReq) (*PushResult, error) {
	if err := y.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	y.mu.Lock()
	defer y.mu.Unlock()
	st, err := y.loadServer()
	if err != nil {
		return nil, err
	}
	wsState := st.Workspaces[req.WorkspaceID]
	if wsState == nil {
		wsState = &simWorkspaceState{}
		st.Workspaces[req.WorkspaceID] = wsState
	}
	byOpID := map[string]simServerOp{}
	entityVersions := map[string]int{}
	for _, op := range wsState.Operations {
		byOpID[op.OperationID] = op
		if v, ok := entityVersions[op.EntityType+":"+op.EntityID]; !ok || op.ServerVersion > v {
			entityVersions[op.EntityType+":"+op.EntityID] = op.ServerVersion
		}
	}
	result := &PushResult{WorkspaceID: req.WorkspaceID, ServerTime: Now()}
	for _, op := range req.Operations {
		item := PushItemResult{OperationID: op.OperationID}
		if _, dup := byOpID[op.OperationID]; dup {
			item.Result = "duplicate"
			result.Items = append(result.Items, item)
			continue
		}
		current := entityVersions[op.EntityType+":"+op.EntityID]
		// 冲突：客户端基于比服务端更旧的版本修改（BaseVersion < 当前服务端版本）。
		if current > 0 && op.BaseVersion < current {
			item.Result = "conflict"
			copyPayload, _ := json.Marshal(map[string]any{
				"conflict_of": op.OperationID, "server_version": current,
				"local_payload": json.RawMessage(op.Payload),
			})
			item.ConflictCopy = copyPayload
			result.Items = append(result.Items, item)
			continue
		}
		nextSeq := int64(len(wsState.Operations) + 1)
		nextVer := current + 1
		if op.BaseVersion >= nextVer {
			nextVer = op.BaseVersion + 1
		}
		serverOp := simServerOp{
			OperationID: op.OperationID, EntityType: op.EntityType, EntityID: op.EntityID,
			BaseVersion: op.BaseVersion, Operation: op.Operation, Payload: op.Payload,
			ServerSeq: nextSeq, ServerVersion: nextVer, CreatedAt: Now(),
		}
		wsState.Operations = append(wsState.Operations, serverOp)
		entityVersions[op.EntityType+":"+op.EntityID] = nextVer
		item.Result = "accepted"
		item.ServerSeq = &nextSeq
		item.ServerVersion = &nextVer
		result.Items = append(result.Items, item)
	}
	if err := y.saveServer(st); err != nil {
		return nil, err
	}
	return result, nil
}

// SyncPullReq 拉取变更请求。
type SyncPullReq struct {
	WorkspaceID string `json:"workspace_id"`
	Cursor      int64  `json:"cursor"`
	Limit       int    `json:"limit"`
}

// SyncPull 按游标拉取变更（客户端先本地应用再保存游标）。
func (y *SyncService) SyncPull(ctx context.Context, req SyncPullReq) (*PullResult, error) {
	if err := y.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	y.mu.Lock()
	defer y.mu.Unlock()
	st, err := y.loadServer()
	if err != nil {
		return nil, err
	}
	wsState := st.Workspaces[req.WorkspaceID]
	limit := req.Limit
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	result := &PullResult{ServerTime: Now()}
	if wsState == nil {
		result.NextCursor = req.Cursor
		return result, nil
	}
	var ops []simServerOp
	for _, op := range wsState.Operations {
		if op.ServerSeq > req.Cursor {
			ops = append(ops, op)
		}
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].ServerSeq < ops[j].ServerSeq })
	hasMore := len(ops) > limit
	if hasMore {
		ops = ops[:limit]
	}
	for _, op := range ops {
		result.Operations = append(result.Operations, SyncOpDTO{
			OperationID: op.OperationID, EntityType: op.EntityType, EntityID: op.EntityID,
			BaseVersion: op.BaseVersion, Operation: op.Operation,
			Payload: op.Payload, CreatedAt: op.CreatedAt,
		})
		result.NextCursor = op.ServerSeq
	}
	result.HasMore = hasMore
	return result, nil
}

// SyncPushLocalReq 推送本地队列请求。
type SyncPushLocalReq struct {
	WorkspaceID string `json:"workspace_id"`
}

// SyncPushLocal 便捷推送：读取本地 pending 队列并推送到模拟服务端。
func (y *SyncService) SyncPushLocal(ctx context.Context, req SyncPushLocalReq) (*PushResult, error) {
	wsID := req.WorkspaceID
	if err := y.s.assertWorkspace(ctx, wsID); err != nil {
		return nil, err
	}
	ops, err := y.s.Repo.ListPendingSyncOps(ctx, wsID, 500)
	if err != nil {
		return nil, err
	}
	if len(ops) == 0 {
		return &PushResult{WorkspaceID: wsID, ServerTime: Now()}, nil
	}
	dtos := make([]SyncOpDTO, 0, len(ops))
	for _, op := range ops {
		dtos = append(dtos, SyncOpDTO{
			OperationID: op.OperationID, EntityType: op.EntityType, EntityID: op.EntityID,
			BaseVersion: op.BaseVersion, Operation: op.Operation,
			Payload: op.Payload, CreatedAt: op.CreatedAt,
		})
	}
	res, err := y.SyncPush(ctx, SyncPushReq{WorkspaceID: wsID, Operations: dtos})
	if err != nil {
		return nil, err
	}
	// 回写本地状态
	for _, item := range res.Items {
		state := "rejected"
		switch item.Result {
		case "accepted":
			state = "accepted"
		case "duplicate":
			state = "accepted"
		case "conflict":
			state = "conflict"
		}
		_ = y.s.Repo.UpdateSyncOpState(ctx, item.OperationID, state, item.ServerSeq)
	}
	y.s.audit(ctx, wsID, "sync.push", "workspace", wsID,
		map[string]any{"total": len(res.Items)})
	return res, nil
}

// SyncStatusGetReq 同步状态请求。
type SyncStatusGetReq struct {
	WorkspaceID string `json:"workspace_id"`
}

// SyncStatusGet 返回同步状态（pending 数量、设备）。
func (y *SyncService) SyncStatusGet(ctx context.Context, req SyncStatusGetReq) (*SyncStatus, error) {
	if err := y.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	pending, err := y.s.Repo.CountPendingSyncOps(ctx, req.WorkspaceID)
	if err != nil {
		return nil, err
	}
	return &SyncStatus{
		WorkspaceID: req.WorkspaceID, Pending: pending,
		State: "idle", LastError: "", DeviceID: "local-device",
	}, nil
}

// RecordReviewSyncOp 记录复习卡变更（示例业务埋点）。
func (y *SyncService) RecordReviewSyncOp(ctx context.Context, wsID, cardID string, version int, payload any) error {
	return y.s.Repo.RecordSyncOp(ctx, wsID, "review_card", cardID, "update", version, payload)
}

var _ = fmt.Sprintf
var _ = time.Now
