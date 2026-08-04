-- 0003_idempotency: 命令型接口幂等键记录表（创建/提交/导入等必须幂等）
-- workspace_id 使用 sentinel（如 "__new__"）表示工作区尚未创建的命令（WorkspaceCreate）。
CREATE TABLE IF NOT EXISTS idempotency_keys (
  workspace_id TEXT NOT NULL,
  idempotency_key TEXT NOT NULL,
  method TEXT NOT NULL,
  response_json TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  PRIMARY KEY (workspace_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS idx_idempotency_workspace ON idempotency_keys(workspace_id, created_at);
