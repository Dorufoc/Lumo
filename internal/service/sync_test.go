package service

import (
	"context"
	"encoding/json"
	"testing"

	"lumo/internal/domain"
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
