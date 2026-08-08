package service

import (
	"context"
	"encoding/json"
	"testing"

	"lumo/internal/domain"
	"lumo/internal/repository"
)

func TestSyncDeviceAndPush(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)

	// 设备注册（幂等）
	d1, err := s.Sync.SyncDeviceRegister(ctx, SyncDeviceRegisterReq{
		DeviceID: "device-1", DeviceName: "Windows Desktop", Platform: "windows", AppVersion: "2.0.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d1.Status != "registered" {
		t.Fatalf("unexpected status: %s", d1.Status)
	}
	d2, err := s.Sync.SyncDeviceRegister(ctx, SyncDeviceRegisterReq{
		DeviceID: "device-1", DeviceName: "Windows Desktop", Platform: "windows", AppVersion: "2.0.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d2.Status != "already_registered" {
		t.Fatalf("expected already_registered, got %s", d2.Status)
	}

	// 推送变更
	push, err := s.Sync.SyncPush(ctx, SyncPushReq{
		WorkspaceID: ws.ID,
		Operations: []SyncOpDTO{
			{OperationID: "op-001", EntityType: "review_card", EntityID: "card-1", BaseVersion: 1, Operation: "update", Payload: jsonRaw(`{"rating":"good"}`)},
			{OperationID: "op-002", EntityType: "review_card", EntityID: "card-2", BaseVersion: 1, Operation: "update", Payload: jsonRaw(`{"rating":"again"}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(push.Items) != 2 || push.Items[0].Result != "accepted" {
		t.Fatalf("unexpected push result: %+v", push.Items)
	}
	if push.Items[0].ServerSeq == nil || *push.Items[0].ServerSeq != 1 {
		t.Fatalf("expected server_sequence 1, got %v", push.Items[0].ServerSeq)
	}

	// 重复推送 → duplicate
	push2, err := s.Sync.SyncPush(ctx, SyncPushReq{
		WorkspaceID: ws.ID,
		Operations: []SyncOpDTO{
			{OperationID: "op-001", EntityType: "review_card", EntityID: "card-1", BaseVersion: 1, Operation: "update", Payload: jsonRaw(`{"rating":"good"}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if push2.Items[0].Result != "duplicate" {
		t.Fatalf("expected duplicate, got %s", push2.Items[0].Result)
	}

	// 版本冲突 → conflict
	push3, err := s.Sync.SyncPush(ctx, SyncPushReq{
		WorkspaceID: ws.ID,
		Operations: []SyncOpDTO{
			{OperationID: "op-003", EntityType: "review_card", EntityID: "card-1", BaseVersion: 1, Operation: "update", Payload: jsonRaw(`{"rating":"old"}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if push3.Items[0].Result != "conflict" {
		t.Fatalf("expected conflict, got %s", push3.Items[0].Result)
	}
	if push3.Items[0].ConflictCopy == nil {
		t.Fatal("conflict copy missing")
	}

	// 拉取
	pull, err := s.Sync.SyncPull(ctx, SyncPullReq{WorkspaceID: ws.ID, Cursor: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(pull.Operations) != 2 {
		t.Fatalf("expected 2 ops, got %d", len(pull.Operations))
	}
	if pull.NextCursor != 2 || pull.HasMore {
		t.Fatalf("unexpected pull: cursor=%d hasMore=%v", pull.NextCursor, pull.HasMore)
	}
	// 增量拉取
	pull2, err := s.Sync.SyncPull(ctx, SyncPullReq{WorkspaceID: ws.ID, Cursor: pull.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(pull2.Operations) != 0 {
		t.Fatalf("expected no ops after cursor, got %d", len(pull2.Operations))
	}
}

func TestSyncLocalQueue(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)

	// 复习卡变更埋点 → pending 队列
	if err := s.Sync.RecordReviewSyncOp(ctx, ws.ID, "card-x", 1, map[string]any{"rating": "good"}); err != nil {
		t.Fatal(err)
	}
	status, err := s.Sync.SyncStatusGet(ctx, SyncStatusGetReq{WorkspaceID: ws.ID})
	if err != nil {
		t.Fatal(err)
	}
	if status.Pending != 1 {
		t.Fatalf("expected 1 pending, got %d", status.Pending)
	}

	// 推送本地队列
	res, err := s.Sync.SyncPushLocal(ctx, SyncPushLocalReq{WorkspaceID: ws.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || res.Items[0].Result != "accepted" {
		t.Fatalf("unexpected push local: %+v", res.Items)
	}
	status, _ = s.Sync.SyncStatusGet(ctx, SyncStatusGetReq{WorkspaceID: ws.ID})
	if status.Pending != 0 {
		t.Fatalf("expected 0 pending after push, got %d", status.Pending)
	}
}

func TestSyncScoping(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	_, _ = createWorkspace(t, s)
	ws2, _ := createWorkspace(t, s)

	// ws2 拉取 ws 的数据应为空
	pull, err := s.Sync.SyncPull(ctx, SyncPullReq{WorkspaceID: ws2.ID, Cursor: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(pull.Operations) != 0 {
		t.Fatal("cross-workspace sync leak")
	}
	// 无效 workspace
	if _, err := s.Sync.SyncPush(ctx, SyncPushReq{WorkspaceID: "no-such"}); err == nil {
		t.Fatal("expected error for missing workspace")
	}
	_ = domain.CodeNotFound
}

func jsonRaw(s string) json.RawMessage { return json.RawMessage(s) }

// TestSyncDeviceListAndRevoke 覆盖设备列表 + 停用设备双通道失效（Todo 40）：
// 停用设备后，SyncPushLocal/SyncPull 入口返回 UNAUTHORIZED（本地 in-process 校验）。
func TestSyncDeviceListAndRevoke(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)

	// 注册三台设备（本地 devices 表）
	for _, d := range []SyncDeviceRegisterReq{
		{WorkspaceID: ws.ID, DeviceID: "device-a", DeviceName: "Windows Desktop", Platform: "windows", AppVersion: "2.0.0"},
		{WorkspaceID: ws.ID, DeviceID: "device-b", DeviceName: "Android Phone", Platform: "android", AppVersion: "2.0.0"},
		{WorkspaceID: ws.ID, DeviceID: "device-c", DeviceName: "iPad", Platform: "ios", AppVersion: "2.0.0"},
	} {
		if _, err := s.Sync.SyncDeviceRegister(ctx, d); err != nil {
			t.Fatal(err)
		}
	}

	// 设备列表：三台上线
	list, err := s.Sync.SyncDeviceList(ctx, SyncDeviceListReq{WorkspaceID: ws.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Devices) != 3 {
		t.Fatalf("expected 3 devices, got %d", len(list.Devices))
	}
	if list.Devices[0].Status != "active" || list.Devices[0].DeviceName != "Windows Desktop" {
		t.Fatalf("unexpected device list: %+v", list.Devices)
	}

	// 停用 device-b → devices.status=revoked
	rev, err := s.Sync.SyncDeviceRevoke(ctx, SyncDeviceRevokeReq{WorkspaceID: ws.ID, DeviceID: "device-b"})
	if err != nil {
		t.Fatal(err)
	}
	if rev.DeviceID != "device-b" || rev.Status != "revoked" {
		t.Fatalf("unexpected revoke result: %+v", rev)
	}

	// 列表反映 revoked
	list, _ = s.Sync.SyncDeviceList(ctx, SyncDeviceListReq{WorkspaceID: ws.ID})
	if len(list.Devices) != 3 {
		t.Fatalf("expected 3 devices after revoke, got %d", len(list.Devices))
	}
	byID := map[string]string{}
	for _, d := range list.Devices {
		byID[d.DeviceID] = d.Status
	}
	if byID["device-b"] != "revoked" {
		t.Fatalf("device-b should be revoked: %v", byID)
	}
	if byID["device-a"] != "active" || byID["device-c"] != "active" {
		t.Fatalf("device-a/c should stay active: %v", byID)
	}

	// 停用设备立即失效：SyncPushLocal/SyncPull 返回 UNAUTHORIZED（本地双通道校验）
	if _, err := s.Sync.SyncPushLocal(ctx, SyncPushLocalReq{WorkspaceID: ws.ID, DeviceID: "device-b"}); domain.AsError(err).Code != domain.CodeUnauthorized {
		t.Fatalf("SyncPushLocal with revoked device should be UNAUTHORIZED, got %v", err)
	}
	if _, err := s.Sync.SyncPull(ctx, SyncPullReq{WorkspaceID: ws.ID, DeviceID: "device-b", Cursor: 0}); domain.AsError(err).Code != domain.CodeUnauthorized {
		t.Fatalf("SyncPull with revoked device should be UNAUTHORIZED, got %v", err)
	}
	// 活跃设备不受影响
	if _, err := s.Sync.SyncPushLocal(ctx, SyncPushLocalReq{WorkspaceID: ws.ID, DeviceID: "device-a"}); err != nil {
		t.Fatalf("SyncPushLocal with active device should pass: %v", err)
	}
	if _, err := s.Sync.SyncPull(ctx, SyncPullReq{WorkspaceID: ws.ID, DeviceID: "device-a", Cursor: 0}); err != nil {
		t.Fatalf("SyncPull with active device should pass: %v", err)
	}
}

// TestSyncRevokePropagatesDeviceRevokedOp 覆盖撤销传播（Todo 40）：
// 本地停用设备时经 POST /v1/sync/push 发送 device:revoked 操作
// （entity_type=device, entity_id=设备 id, operation=update, payload.status=revoked）。
func TestSyncRevokePropagatesDeviceRevokedOp(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)

	if _, err := s.Sync.SyncDeviceRegister(ctx, SyncDeviceRegisterReq{WorkspaceID: ws.ID, DeviceID: "device-b", DeviceName: "Android Phone", Platform: "android", AppVersion: "2.0.0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sync.SyncDeviceRevoke(ctx, SyncDeviceRevokeReq{WorkspaceID: ws.ID, DeviceID: "device-b"}); err != nil {
		t.Fatal(err)
	}

	// pending 队列包含 device:revoked 操作（payload 含 status=revoked）
	ops, err := s.Repo.ListPendingSyncOps(ctx, ws.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	var found *repository.SyncOpRow
	for _, op := range ops {
		if op.EntityType == "device" && op.EntityID == "device-b" {
			found = op
			break
		}
	}
	if found == nil {
		t.Fatalf("expected device:revoked op in pending queue, ops=%+v", ops)
	}
	if found.Operation != "update" {
		t.Fatalf("expected operation=update, got %s", found.Operation)
	}
	var payload struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(found.Payload, &payload); err != nil || payload.Status != "revoked" {
		t.Fatalf("expected payload.status=revoked, got %s (err=%v)", string(found.Payload), err)
	}

	// 推送后：device:revoked 被模拟服务端接受；停用设备仍被本地拒绝
	res, err := s.Sync.SyncPushLocal(ctx, SyncPushLocalReq{WorkspaceID: ws.ID, DeviceID: "device-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || res.Items[0].Result != "accepted" {
		t.Fatalf("expected device:revoked accepted, got %+v", res.Items)
	}
}
