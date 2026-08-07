-- 0006_audit: 审计事件扩展 + users 重建（新增 parent 角色与 disabled_at 禁用列）
-- 说明：users 表结构变更需要"建新表-拷数据-删旧表-改名"重建流程，
--      迁移期间由 db.go 固定连接关闭外键检查（foreign_keys=OFF + defer_foreign_keys=ON），
--      迁移完成后恢复。重建后必须重建 users 相关索引。

-- 1. audit_events 扩展列：操作者角色、操作前后快照（均允许为空，历史行不受影响）。
ALTER TABLE audit_events ADD COLUMN actor_role TEXT;
ALTER TABLE audit_events ADD COLUMN before_json TEXT;
ALTER TABLE audit_events ADD COLUMN after_json TEXT;

-- 2. users 重建：role 新增 parent 枚举；新增 disabled_at（禁用时间，NULL=未禁用）。
CREATE TABLE users_new (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  display_name TEXT NOT NULL CHECK (length(trim(display_name)) BETWEEN 1 AND 80),
  role TEXT NOT NULL DEFAULT 'student' CHECK (role IN ('student', 'teacher', 'admin', 'parent')),
  preferences_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  deleted_at TEXT,
  disabled_at TEXT,
  version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0)
);

INSERT INTO users_new (id, workspace_id, display_name, role, preferences_json, created_at, updated_at, deleted_at, version)
  SELECT id, workspace_id, display_name, role, preferences_json, created_at, updated_at, deleted_at, version FROM users;

DROP TABLE users;
ALTER TABLE users_new RENAME TO users;

-- 3. 重建 users 索引（DROP 旧表时随表删除，RENAME 后需重建）。
CREATE INDEX IF NOT EXISTS idx_users_workspace ON users(workspace_id, deleted_at);
