-- 0007_webhook_dead: webhook_deliveries 重建，status CHECK 增加 'dead' 死信枚举
-- 说明：0005 的 webhook_deliveries.status CHECK 仅含 ('pending','sent','failed')，
--      但 Todo 31 死信语义要求 attempt ≥ 5 → status='dead'，且失败投递进入
--      'pending_retry' 等待重试（重试调度器扫描该状态）。SQLite 修改 CHECK
--      约束必须重建表，按 0006 先例（db.go 固定连接关闭外键检查后
--      建新表-拷数据-删旧表-改名）。0005 未对 webhook_deliveries 建索引，
--      且无其他表引用它，故无需重建索引；webhook_subscriptions 引用保持原状。
--      另新增 payload_json：重试调度（attempt 2-5）需要重发原始载荷，0005
--      无此列，重建时补上（新列可空，旧行迁移后为 NULL，仅在重试时读取）。

CREATE TABLE webhook_deliveries_new (
  id TEXT PRIMARY KEY,
  subscription_id TEXT NOT NULL REFERENCES webhook_subscriptions(id) ON DELETE RESTRICT,
  event_id TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'pending_retry', 'sent', 'failed', 'dead')),
  attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
  next_retry_at TEXT,
  last_error TEXT,
  payload_json TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

INSERT INTO webhook_deliveries_new (id, subscription_id, event_id, status, attempt, next_retry_at, last_error, created_at)
  SELECT id, subscription_id, event_id, status, attempt, next_retry_at, last_error, created_at FROM webhook_deliveries;

DROP TABLE webhook_deliveries;
ALTER TABLE webhook_deliveries_new RENAME TO webhook_deliveries;
