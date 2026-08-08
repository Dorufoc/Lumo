package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"lumo/internal/domain"
)

// SyncOpRow 是 sync_operations 表行。
type SyncOpRow struct {
	OperationID string
	DeviceID    string
	WorkspaceID string
	EntityType  string
	EntityID    string
	BaseVersion int
	Operation   string
	Payload     json.RawMessage
	State       string
	ServerSeq   *int64
	RetryCount  int
	CreatedAt   string
	UpdatedAt   string
}

// CreateSyncOp 记录变更操作（幂等：operation_id 唯一）。
func (r *Repo) CreateSyncOp(ctx context.Context, op *SyncOpRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO sync_operations (operation_id, device_id, workspace_id, entity_type, entity_id,
			base_version, operation, payload_json, state, retry_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'pending', 0)
		ON CONFLICT(operation_id) DO NOTHING`,
		op.OperationID, op.DeviceID, op.WorkspaceID, op.EntityType, op.EntityID,
		op.BaseVersion, op.Operation, string(op.Payload))
	return normalizeErr(err)
}

// ListPendingSyncOps 列出待推送操作。
func (r *Repo) ListPendingSyncOps(ctx context.Context, wsID string, limit int) ([]*SyncOpRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT operation_id, device_id, workspace_id, entity_type, entity_id, base_version,
			operation, payload_json, state, server_sequence, retry_count, created_at, updated_at
		FROM sync_operations
		WHERE workspace_id = ? AND state IN ('pending', 'pushing')
		ORDER BY created_at LIMIT ?`, wsID, limit)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*SyncOpRow
	for rows.Next() {
		op, err := scanSyncOp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// CountPendingSyncOps 统计待推送数量。
func (r *Repo) CountPendingSyncOps(ctx context.Context, wsID string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
		SELECT count(*) FROM sync_operations
		WHERE workspace_id = ? AND state IN ('pending', 'pushing')`, wsID).Scan(&n)
	return n, normalizeErr(err)
}

// UpdateSyncOpState 更新推送状态。
func (r *Repo) UpdateSyncOpState(ctx context.Context, opID, state string, serverSeq *int64) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE sync_operations SET state = ?, server_sequence = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE operation_id = ?`, state, serverSeq, opID)
	return normalizeErr(err)
}

// ListSyncOps 列出工作区全部操作（含状态）。
func (r *Repo) ListSyncOps(ctx context.Context, wsID string, limit int) ([]*SyncOpRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT operation_id, device_id, workspace_id, entity_type, entity_id, base_version,
			operation, payload_json, state, server_sequence, retry_count, created_at, updated_at
		FROM sync_operations WHERE workspace_id = ? ORDER BY created_at DESC LIMIT ?`, wsID, limit)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*SyncOpRow
	for rows.Next() {
		op, err := scanSyncOp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

func scanSyncOp(row interface{ Scan(...any) error }) (*SyncOpRow, error) {
	var op SyncOpRow
	var payload string
	var serverSeq *int64
	if err := row.Scan(&op.OperationID, &op.DeviceID, &op.WorkspaceID, &op.EntityType, &op.EntityID,
		&op.BaseVersion, &op.Operation, &payload, &op.State, &serverSeq, &op.RetryCount,
		&op.CreatedAt, &op.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	op.Payload = json.RawMessage(payload)
	op.ServerSeq = serverSeq
	return &op, nil
}

// RecordSyncOp 便捷记录（device 固定为本地模拟设备）。
func (r *Repo) RecordSyncOp(ctx context.Context, wsID, entityType, entityID, operation string, baseVersion int, payload any) error {
	op := &SyncOpRow{
		OperationID: newIDLocal(), DeviceID: "local-device", WorkspaceID: wsID,
		EntityType: entityType, EntityID: entityID, BaseVersion: baseVersion,
		Operation: operation, Payload: json.RawMessage(MarshalJSON(payload)),
	}
	return r.CreateSyncOp(ctx, op)
}

// ---------- 设备管理（Todo 40：桌面-移动协同；本地 devices 表为 status 权威源）----------

// DeviceRow 是 devices 表行映射（本地 status 权威源）。
type DeviceRow struct {
	ID          string
	WorkspaceID string
	DeviceName  string
	Platform    string
	LastSeenAt  string
	Status      string // active | revoked
	CreatedAt   string
}

// UpsertDevice 注册/刷新设备（幂等，语义镜像 cloud-server RegisterDevice）：
//   - 已存在：刷新最近活跃与名称/平台，返回 already_registered；
//   - 已撤销设备不自动恢复（撤销以服务端时间为准）；
//   - 不存在：插入 active。
func (r *Repo) UpsertDevice(ctx context.Context, wsID string, d DeviceRow) (string, error) {
	var status string
	err := r.db.QueryRowContext(ctx, `SELECT status FROM devices WHERE id = ?`, d.ID).Scan(&status)
	switch {
	case err == nil:
		// 已存在：刷新最近活跃与名称/平台，返回 already_registered（不恢复已撤销设备）
		_, uerr := r.db.ExecContext(ctx,
			`UPDATE devices SET last_seen_at = ?, device_name = ?, platform = ? WHERE id = ?`,
			d.LastSeenAt, d.DeviceName, d.Platform, d.ID)
		if uerr != nil {
			return "", normalizeErr(uerr)
		}
		return "already_registered", nil
	case errors.Is(err, sql.ErrNoRows):
		_, ierr := r.db.ExecContext(ctx,
			`INSERT INTO devices (id, workspace_id, device_name, platform, last_seen_at, status, created_at)
			 VALUES (?, ?, ?, ?, ?, 'active', ?)`,
			d.ID, wsID, d.DeviceName, d.Platform, d.LastSeenAt, d.CreatedAt)
		if ierr != nil {
			return "", normalizeErr(ierr)
		}
		return "registered", nil
	default:
		return "", normalizeErr(err)
	}
}

// GetDeviceStatus 查询设备状态；不存在返回空字符串（不视为错误）。
func (r *Repo) GetDeviceStatus(ctx context.Context, deviceID string) (string, error) {
	var status string
	err := r.db.QueryRowContext(ctx, `SELECT status FROM devices WHERE id = ?`, deviceID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", normalizeErr(err)
	}
	return status, nil
}

// SetDeviceStatus 更新设备状态（device:revoked 传播 / 本地停用用）。
func (r *Repo) SetDeviceStatus(ctx context.Context, deviceID, status string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE devices SET status = ? WHERE id = ?`, status, deviceID)
	return normalizeErr(err)
}

// ListDevices 列出工作区全部设备（按创建时间升序）。
func (r *Repo) ListDevices(ctx context.Context, wsID string) ([]*DeviceRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, workspace_id, device_name, platform, last_seen_at, status, created_at
		FROM devices WHERE workspace_id = ? ORDER BY created_at ASC, id ASC`, wsID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*DeviceRow
	for rows.Next() {
		var d DeviceRow
		if err := rows.Scan(&d.ID, &d.WorkspaceID, &d.DeviceName, &d.Platform, &d.LastSeenAt, &d.Status, &d.CreatedAt); err != nil {
			return nil, normalizeErr(err)
		}
		out = append(out, &d)
	}
	return out, rows.Err()
}

var _ = strings.Join
var _ = domain.NowUTC
