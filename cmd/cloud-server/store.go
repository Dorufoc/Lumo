// store.go —— cloud-server 的独立 SQLite 持久化层。
// 使用 modernc.org/sqlite（纯 Go 驱动），表结构镜像客户端 devices/sync_operations
// schema，但完全独立：不依赖客户端本地库、不跑客户端迁移。
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// ErrWorkspaceDeleting 表示工作区处于延迟删除期，拒绝写操作。
var ErrWorkspaceDeleting = errors.New("workspace is being deleted")

// Store 是 cloud-server 的 SQLite 持久化层（单进程内加锁保证写事务串行）。
type Store struct {
	db *sql.DB
	mu sync.Mutex
}

// OpenStore 打开（或创建）cloud-server 独立数据库并建表。
func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// 连接级 PRAGMA：WAL + 有限忙等待（与 internal/database 同款配置）。
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA synchronous = NORMAL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("apply pragma %q: %w", pragma, err)
		}
	}
	st := &Store{db: db}
	if err := st.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return st, nil
}

// migrate 建表（CREATE TABLE IF NOT EXISTS），镜像客户端 schema 但不跑客户端迁移。
func (s *Store) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS devices (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL DEFAULT '',
	device_name TEXT NOT NULL DEFAULT '',
	platform TEXT NOT NULL DEFAULT '',
	app_version TEXT NOT NULL DEFAULT '',
	last_seen_at TEXT,
	status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','revoked')),
	created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE TABLE IF NOT EXISTS sync_operations (
	operation_id TEXT PRIMARY KEY,
	device_id TEXT NOT NULL DEFAULT '',
	workspace_id TEXT NOT NULL,
	entity_type TEXT NOT NULL,
	entity_id TEXT NOT NULL,
	base_version INTEGER NOT NULL DEFAULT 0 CHECK (base_version >= 0),
	operation TEXT NOT NULL CHECK (operation IN ('create','update','delete')),
	payload_json TEXT NOT NULL,
	state TEXT NOT NULL DEFAULT 'accepted',
	server_sequence INTEGER NOT NULL,
	server_version INTEGER NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_sync_ops_ws_seq ON sync_operations (workspace_id, server_sequence);
CREATE TABLE IF NOT EXISTS backups (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL,
	device_id TEXT NOT NULL DEFAULT '',
	name TEXT NOT NULL DEFAULT '',
	size_bytes INTEGER NOT NULL DEFAULT 0,
	sha256 TEXT NOT NULL DEFAULT '',
	meta_json TEXT NOT NULL DEFAULT '{}',
	created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_backups_ws ON backups (workspace_id);
CREATE TABLE IF NOT EXISTS workspaces (
	workspace_id TEXT PRIMARY KEY,
	soft_deleted_at TEXT,
	delete_deadline TEXT,
	updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
`
	_, err := s.db.Exec(schema)
	return err
}

// Close 关闭底层连接。
func (s *Store) Close() error { return s.db.Close() }

// RegisterDevice 注册设备；已存在返回 already_registered（幂等，非错误）。
// 已撤销设备再次注册不自动恢复（撤销以服务端时间为准，恢复逻辑由 Todo 40 负责）。
func (s *Store) RegisterDevice(ctx context.Context, d Device) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT status FROM devices WHERE id = ?`, d.DeviceID).Scan(&status)
	switch {
	case err == nil:
		// 已存在：刷新最近活跃与名称/平台，返回 already_registered
		_, uerr := s.db.ExecContext(ctx,
			`UPDATE devices SET last_seen_at = ?, device_name = ?, platform = ?, app_version = ? WHERE id = ?`,
			d.LastSeenAt, d.DeviceName, d.Platform, d.AppVersion, d.DeviceID)
		if uerr != nil {
			return "", uerr
		}
		return "already_registered", nil
	case errors.Is(err, sql.ErrNoRows):
		_, ierr := s.db.ExecContext(ctx,
			`INSERT INTO devices (id, workspace_id, device_name, platform, app_version, last_seen_at, status, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, 'active', ?)`,
			d.DeviceID, d.WorkspaceID, d.DeviceName, d.Platform, d.AppVersion, d.LastSeenAt, d.CreatedAt)
		if ierr != nil {
			return "", ierr
		}
		return "registered", nil
	default:
		return "", err
	}
}

// SetDeviceStatus 更新设备状态（device:revoked 传播用）。
func (s *Store) SetDeviceStatus(ctx context.Context, deviceID, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE devices SET status = ? WHERE id = ?`, status, deviceID)
	return err
}

// DeviceStatus 查询设备状态；不存在返回空字符串（auth 中间件用于拒绝 revoked 设备）。
func (s *Store) DeviceStatus(ctx context.Context, deviceID string) (string, error) {
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT status FROM devices WHERE id = ?`, deviceID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return status, nil
}

// PushOps 逐项处理推送，与 in-process SyncService.SyncPush 语义完全一致：
//   - duplicate：operation_id 已存在；
//   - conflict：客户端 BaseVersion 落后于服务端当前版本（current > 0 && BaseVersion < current），
//     冲突副本含 conflict_of / server_version / local_payload；
//   - rejected：字段非法（空 operation_id / 非法 operation 值 / 空实体 / base_version<0 / 非法 payload）；
//   - 其余 accepted，分配单调递增 server_sequence 与 server_version。
//
// 任一操作失败不回滚已接受操作。工作区处于延迟删除期时返回 ErrWorkspaceDeleting。
func (s *Store) PushOps(ctx context.Context, workspaceID, deviceID string, ops []Op, now string) ([]ItemResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	deleting, err := s.workspaceDeletingLocked(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	if deleting {
		return nil, ErrWorkspaceDeleting
	}

	// 加载既有操作（去重依据）与实体版本表（冲突判定依据）。
	byOpID := map[string]struct{}{}
	entityVersions := map[string]int{}
	rows, err := s.db.QueryContext(ctx,
		`SELECT operation_id, entity_type, entity_id, server_version FROM sync_operations WHERE workspace_id = ?`, workspaceID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var oid, et, eid string
		var ver int
		if err := rows.Scan(&oid, &et, &eid, &ver); err != nil {
			rows.Close()
			return nil, err
		}
		byOpID[oid] = struct{}{}
		k := et + ":" + eid
		if v, ok := entityVersions[k]; !ok || ver > v {
			entityVersions[k] = ver
		}
	}
	rows.Close()

	results := make([]ItemResult, 0, len(ops))
	for _, op := range ops {
		item := ItemResult{OperationID: op.OperationID}
		// 字段校验 → rejected（不落库、不影响其他操作）
		if !op.valid() {
			item.Result = "rejected"
			results = append(results, item)
			continue
		}
		if _, dup := byOpID[op.OperationID]; dup {
			item.Result = "duplicate"
			results = append(results, item)
			continue
		}
		current := entityVersions[op.EntityType+":"+op.EntityID]
		if current > 0 && op.BaseVersion < current {
			item.Result = "conflict"
			copyPayload, _ := json.Marshal(map[string]any{
				"conflict_of": op.OperationID, "server_version": current,
				"local_payload": json.RawMessage(op.Payload),
			})
			item.ConflictCopy = copyPayload
			results = append(results, item)
			continue
		}
		// accepted：分配单调递增 server_sequence（MAX+1，与 in-process 的 count+1 等价）
		var nextSeq int64
		if err := s.db.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(server_sequence), 0) FROM sync_operations WHERE workspace_id = ?`, workspaceID).Scan(&nextSeq); err != nil {
			return nil, err
		}
		nextSeq++
		nextVer := current + 1
		if op.BaseVersion >= nextVer {
			nextVer = op.BaseVersion + 1
		}
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO sync_operations (operation_id, device_id, workspace_id, entity_type, entity_id,
				base_version, operation, payload_json, state, server_sequence, server_version, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'accepted', ?, ?, ?, ?)`,
			op.OperationID, deviceID, workspaceID, op.EntityType, op.EntityID, op.BaseVersion, op.Operation,
			string(op.Payload), nextSeq, nextVer, now, now); err != nil {
			return nil, err
		}
		entityVersions[op.EntityType+":"+op.EntityID] = nextVer
		item.Result = "accepted"
		item.ServerSeq = &nextSeq
		item.ServerVersion = &nextVer
		// device 撤销传播：entity_type=device, operation=update, payload.status=revoked
		if op.EntityType == "device" && op.Operation == "update" {
			var p struct {
				Status string `json:"status"`
			}
			if json.Unmarshal(op.Payload, &p) == nil && p.Status == "revoked" {
				_ = s.SetDeviceStatus(ctx, op.EntityID, "revoked")
			}
		}
		results = append(results, item)
	}
	return results, nil
}

// PullOps 按游标拉取变更：返回 server_sequence > cursor 的操作（升序），
// limit 缺省 200、上限 200；next_cursor 为最后返回操作的 server_sequence。
func (s *Store) PullOps(ctx context.Context, workspaceID string, cursor int64, limit int) ([]Op, int64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT operation_id, entity_type, entity_id, base_version, operation, payload_json, created_at, server_sequence
		 FROM sync_operations WHERE workspace_id = ? AND server_sequence > ?
		 ORDER BY server_sequence ASC LIMIT ?`, workspaceID, cursor, limit+1)
	if err != nil {
		return nil, 0, false, err
	}
	next := cursor
	hasMore := false
	var ops []Op
	count := 0
	for rows.Next() {
		var o Op
		var payload string
		var seq int64
		if err := rows.Scan(&o.OperationID, &o.EntityType, &o.EntityID, &o.BaseVersion, &o.Operation,
			&payload, &o.CreatedAt, &seq); err != nil {
			rows.Close()
			return nil, 0, false, err
		}
		o.Payload = json.RawMessage(payload)
		count++
		if count > limit {
			hasMore = true
			break
		}
		ops = append(ops, o)
		next = seq
	}
	rows.Close()
	return ops, next, hasMore, nil
}

// CreateBackup 登记备份元数据（服务器不解密端到端加密内容，仅登记）。
// 工作区处于延迟删除期时返回 ErrWorkspaceDeleting。
func (s *Store) CreateBackup(ctx context.Context, b Backup) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	deleting, err := s.workspaceDeletingLocked(ctx, b.WorkspaceID)
	if err != nil {
		return err
	}
	if deleting {
		return ErrWorkspaceDeleting
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO backups (id, workspace_id, device_id, name, size_bytes, sha256, meta_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ID, b.WorkspaceID, b.DeviceID, b.Name, b.SizeBytes, b.SHA256, b.MetaJSON, b.CreatedAt)
	return err
}

// SoftDeleteWorkspace 标记工作区延迟删除：记录软删时间与可撤销截止时间（now+7 天）。
// 重复删除幂等，返回既有时间戳。
func (s *Store) SoftDeleteWorkspace(ctx context.Context, workspaceID, now string) (deletedAt, deadline string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var da, dl *string
	if err := s.db.QueryRowContext(ctx,
		`SELECT soft_deleted_at, delete_deadline FROM workspaces WHERE workspace_id = ? AND soft_deleted_at IS NOT NULL`,
		workspaceID).Scan(&da, &dl); err == nil && da != nil && dl != nil {
		return *da, *dl, nil
	}
	deletedAt = now
	deadline = addDays(now, 7)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO workspaces (workspace_id, soft_deleted_at, delete_deadline, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(workspace_id) DO UPDATE SET
			soft_deleted_at = excluded.soft_deleted_at,
			delete_deadline = excluded.delete_deadline,
			updated_at = excluded.updated_at`,
		workspaceID, deletedAt, deadline, now)
	if err != nil {
		return "", "", err
	}
	return deletedAt, deadline, nil
}

// workspaceDeletingLocked 判断工作区是否处于延迟删除期（调用方需持有锁）。
func (s *Store) workspaceDeletingLocked(ctx context.Context, workspaceID string) (bool, error) {
	var d *string
	err := s.db.QueryRowContext(ctx,
		`SELECT soft_deleted_at FROM workspaces WHERE workspace_id = ?`, workspaceID).Scan(&d)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return d != nil, nil
}

// addDays 解析 RFC 3339 时间并加 n 天。
func addDays(ts string, days int) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	return t.AddDate(0, 0, days).Format(time.RFC3339)
}
