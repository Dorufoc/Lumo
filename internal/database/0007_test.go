package database

// 0007 迁移回归测试（TDD：先于 0007_webhook_dead.sql 编写，未实现时必须失败）。
// 覆盖：webhook_deliveries 重建后 status CHECK 含 'dead' 死信枚举；
// 升级路径（0005 旧投递行数据保留、重建后 FK 仍有效、非法枚举仍被拒绝）。

import (
	"context"
	"testing"
)

// TestMigrateV0007Fresh 全新库迁移：webhook_deliveries.status CHECK 接受 'dead'，
// 同时拒绝未知枚举；其余列结构保持（0005 冻结）。
func TestMigrateV0007Fresh(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate fresh: %v", err)
	}

	cols := tableColumns(t, db, "webhook_deliveries")
	for _, col := range []string{"id", "subscription_id", "event_id", "status", "attempt", "next_retry_at", "last_error", "created_at"} {
		if !cols[col] {
			t.Errorf("webhook_deliveries missing column %s after 0007 rebuild", col)
		}
	}

	if _, err := db.Exec(`INSERT INTO workspaces (id, name, owner_type) VALUES ('w7', 'ws', 'local')`); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO webhook_subscriptions (id, workspace_id, url, event_types) VALUES ('whs1', 'w7', 'https://example.com/hook', '["report:ready"]')`); err != nil {
		t.Fatalf("insert subscription: %v", err)
	}

	// dead 枚举必须可插入（0007 核心：死信状态）。
	if _, err := db.Exec(`INSERT INTO webhook_deliveries (id, subscription_id, event_id, status, attempt) VALUES ('whd1', 'whs1', 'ev1', 'dead', 5)`); err != nil {
		t.Errorf("webhook_deliveries.status CHECK must accept 'dead': %v", err)
	}
	// 未知枚举仍被拒绝。
	if _, err := db.Exec(`INSERT INTO webhook_deliveries (id, subscription_id, event_id, status) VALUES ('whd2', 'whs1', 'ev2', 'bogus')`); err == nil {
		t.Errorf("webhook_deliveries.status CHECK must reject unknown enum")
	}
	// 重建后 FK 仍有效：引用不存在订阅的投递必须被拒绝。
	if _, err := db.Exec(`INSERT INTO webhook_deliveries (id, subscription_id, event_id, status) VALUES ('whd3', 'nosuch', 'ev3', 'pending')`); err == nil {
		t.Errorf("FK to webhook_subscriptions must be enforced after 0007 rebuild")
	}
}

// TestMigrateV0007Upgrade 升级路径：0005 旧库含投递行 → 应用 0007 → 行数据保留、
// status 原值不变、dead 新枚举可用。
func TestMigrateV0007Upgrade(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	// 先应用 0001–0005（webhook 两表由此创建），插入旧库投递数据。
	applyMigrationsUpTo(t, db, "0005")
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, owner_type) VALUES ('w1', 'ws', 'local')`); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO webhook_subscriptions (id, workspace_id, url, event_types, enabled) VALUES ('whs1', 'w1', 'https://example.com/hook', '["grading:updated"]', 1)`); err != nil {
		t.Fatalf("insert subscription: %v", err)
	}
	// 0005 的合法枚举（pending/sent/failed）各留一行历史数据。
	if _, err := db.Exec(`INSERT INTO webhook_deliveries (id, subscription_id, event_id, status, attempt) VALUES ('d1', 'whs1', 'e1', 'sent', 1)`); err != nil {
		t.Fatalf("insert sent delivery: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO webhook_deliveries (id, subscription_id, event_id, status, attempt) VALUES ('d2', 'whs1', 'e2', 'failed', 2)`); err != nil {
		t.Fatalf("insert failed delivery: %v", err)
	}

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate upgrade: %v", err)
	}

	// 历史行保留且 status 原值不变。
	var status string
	var attempt int
	if err := db.QueryRow(`SELECT status, attempt FROM webhook_deliveries WHERE id='d1'`).Scan(&status, &attempt); err != nil {
		t.Fatalf("delivery row lost after 0007 rebuild: %v", err)
	}
	if status != "sent" || attempt != 1 {
		t.Errorf("sent delivery changed: status=%q attempt=%d", status, attempt)
	}
	if err := db.QueryRow(`SELECT status FROM webhook_deliveries WHERE id='d2'`).Scan(&status); err != nil {
		t.Fatalf("failed delivery row lost: %v", err)
	}
	if status != "failed" {
		t.Errorf("failed delivery status changed: %q", status)
	}

	// 重建后 dead 可插入、未知枚举被拒。
	if _, err := db.Exec(`INSERT INTO webhook_deliveries (id, subscription_id, event_id, status, attempt) VALUES ('d3', 'whs1', 'e3', 'dead', 5)`); err != nil {
		t.Errorf("dead enum must be accepted after 0007: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO webhook_deliveries (id, subscription_id, event_id, status) VALUES ('d4', 'whs1', 'e4', 'bogus')`); err == nil {
		t.Errorf("unknown enum must be rejected after 0007")
	}
}
