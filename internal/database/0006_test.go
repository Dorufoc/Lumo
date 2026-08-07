package database

// 0006 迁移回归测试（TDD：先于 0006_audit.sql 编写，未实现时必须失败）。
// 覆盖：audit_events 扩展列（actor_role/before_json/after_json）+ users 重建
// （role 新增 parent 枚举 + disabled_at 列）+ 升级路径（旧库用户数据保留、FK 仍有效）。

import (
	"context"
	"database/sql"
	"testing"
)

// TestMigrateV0006Fresh 全新库迁移：audit_events 扩展列、users 重建（parent 枚举 + disabled_at）、
// users 索引恢复、重建后 FK 引用有效且 foreign_keys 已恢复为 ON。
func TestMigrateV0006Fresh(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate fresh: %v", err)
	}

	// audit_events 扩展列必须存在。
	aeCols := tableColumns(t, db, "audit_events")
	for _, col := range []string{"actor_role", "before_json", "after_json"} {
		if !aeCols[col] {
			t.Errorf("audit_events missing extended column %s", col)
		}
	}

	// users 重建：disabled_at 列存在。
	uCols := tableColumns(t, db, "users")
	if !uCols["disabled_at"] {
		t.Errorf("users missing disabled_at column after 0006 rebuild")
	}

	ws := "w0006"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, owner_type) VALUES (?, 'ws', 'local')`, ws); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}

	// users.role CHECK 已更新：parent 枚举可插入。
	if _, err := db.Exec(`INSERT INTO users (id, workspace_id, display_name, role) VALUES ('up1', ?, '家长', 'parent')`, ws); err != nil {
		t.Errorf("users.role CHECK rejects parent enum: %v", err)
	}
	// 非法枚举仍被拒绝。
	if _, err := db.Exec(`INSERT INTO users (id, workspace_id, display_name, role) VALUES ('ub1', ?, '非法', 'superuser')`, ws); err == nil {
		t.Errorf("users.role CHECK must reject unknown enum")
	}

	// users 索引在重建后必须恢复。
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='idx_users_workspace'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("idx_users_workspace must be rebuilt after users RENAME")
	}

	// 重建后 FK 引用有效：learning_goals 引用 parent 用户。
	if _, err := db.Exec(`INSERT INTO learning_goals (id, workspace_id, user_id, name, daily_minutes) VALUES ('lg1', ?, 'up1', '目标', 30)`, ws); err != nil {
		t.Errorf("FK to rebuilt users broken: %v", err)
	}
	// FK 已恢复为 ON：引用不存在用户的写入必须被拒绝。
	if _, err := db.Exec(`INSERT INTO learning_goals (id, workspace_id, user_id, name, daily_minutes) VALUES ('lg2', ?, 'nosuchuser', '目标', 30)`, ws); err == nil {
		t.Errorf("foreign_keys must be restored to ON after migrate")
	}
}

// TestMigrateV0006Upgrade 升级路径：旧库(0001–0004)含数据 → 应用 0006 → 用户数据保留、
// disabled_at 默认 NULL、引用 users 的历史行仍有效。
func TestMigrateV0006Upgrade(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	// 先应用 0001–0004（模拟既有旧库，含 users 与引用 users 的历史数据）。
	applyMigrationsUpTo(t, db, "0004")
	seedRows(t, db)

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate upgrade: %v", err)
	}

	// 旧用户数据保留且角色未变。
	var role string
	if err := db.QueryRow(`SELECT role FROM users WHERE id='u1'`).Scan(&role); err != nil {
		t.Fatalf("user row lost after 0006 rebuild: %v", err)
	}
	if role != "student" {
		t.Errorf("user role changed: %s", role)
	}

	// disabled_at 默认 NULL。
	var disabled sql.NullString
	if err := db.QueryRow(`SELECT disabled_at FROM users WHERE id='u1'`).Scan(&disabled); err != nil {
		t.Fatal(err)
	}
	if disabled.Valid {
		t.Errorf("historical user disabled_at should be NULL, got %q", disabled.String)
	}

	// 引用重建后 users 的历史行（review_cards.user_id）仍有效。
	var cnt int
	if err := db.QueryRow(`SELECT count(*) FROM review_cards WHERE user_id='u1'`).Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 1 {
		t.Errorf("review_cards referencing rebuilt users broken: %d", cnt)
	}
}

// TestMigrateRestoresForeignKeys 回归：迁移完成后 foreign_keys 必须恢复为 ON、
// defer_foreign_keys 必须恢复为 OFF（db.go 固定连接 PRAGMA 机制）。
func TestMigrateRestoresForeignKeys(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var fk, deferFK int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`PRAGMA defer_foreign_keys`).Scan(&deferFK); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys must be restored to ON after migrate, got %d", fk)
	}
	if deferFK != 0 {
		t.Errorf("defer_foreign_keys must be restored to OFF after migrate, got %d", deferFK)
	}
}
