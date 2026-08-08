-- 0008_org: 组织版角色扩展 + workspaces org 元数据
-- 说明：users.role 在既有 student/teacher/admin/parent（0006 添加）基础上新增
--      org_admin 枚举；重建流程与 0006 一致（建新表-拷数据-删旧表-改名），
--      由 db.go 固定连接关闭外键检查（foreign_keys=OFF + defer_foreign_keys=ON）
--      并恢复。重建复制 users 全部既有列（含 0006 的 disabled_at），禁止丢列。
--      workspaces 增加 org 元数据列（机构名称与机构管理员），均为可空，历史行不受影响。

-- 1. workspaces 增加 org 元数据列（可空，历史工作区不受影响）。
ALTER TABLE workspaces ADD COLUMN org_name TEXT;
ALTER TABLE workspaces ADD COLUMN org_admin_user_id TEXT;

-- 2. users 重建：role 新增 org_admin 枚举；全列复制（含 disabled_at）。
CREATE TABLE users_new (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  display_name TEXT NOT NULL CHECK (length(trim(display_name)) BETWEEN 1 AND 80),
  role TEXT NOT NULL DEFAULT 'student' CHECK (role IN ('student', 'teacher', 'admin', 'parent', 'org_admin')),
  preferences_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  deleted_at TEXT,
  disabled_at TEXT,
  version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0)
);

INSERT INTO users_new (id, workspace_id, display_name, role, preferences_json, created_at, updated_at, deleted_at, disabled_at, version)
  SELECT id, workspace_id, display_name, role, preferences_json, created_at, updated_at, deleted_at, disabled_at, version FROM users;

DROP TABLE users;
ALTER TABLE users_new RENAME TO users;

-- 3. 重建 users 索引（DROP 旧表时随表删除，RENAME 后需重建）。
CREATE INDEX IF NOT EXISTS idx_users_workspace ON users(workspace_id, deleted_at);
