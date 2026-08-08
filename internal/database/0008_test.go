package database

// 0008 迁移回归测试（TDD：先于 0008_org.sql 编写，未实现时必须失败）。
// 覆盖：users 重建后 role CHECK 含全部 5 枚举（student/teacher/admin/parent/org_admin）
// 且保留 0006 新增的 disabled_at 列（禁止丢列）；workspaces 增加 org 元数据列
// （org_name/org_admin_user_id）；升级路径（0001–0007 旧库用户/工作区数据保留、
// 引用 users 的历史行仍有效、重建后 FK 仍有效、非法枚举仍被拒绝）。

import (
	"context"
	"database/sql"
	"testing"
)

// TestMigrateV0008Fresh 全新库迁移：users role CHECK 接受全部 5 枚举并拒绝未知枚举；
// disabled_at 列保留；workspaces 含 org 元数据列；重建后 FK 引用有效。
func TestMigrateV0008Fresh(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate fresh: %v", err)
	}

	// users 重建：disabled_at 列必须保留（0006 列，禁止丢列）。
	uCols := tableColumns(t, db, "users")
	for _, col := range []string{"id", "workspace_id", "display_name", "role", "preferences_json", "created_at", "updated_at", "deleted_at", "disabled_at", "version"} {
		if !uCols[col] {
			t.Errorf("users missing column %s after 0008 rebuild", col)
		}
	}

	ws := "w0008"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, owner_type) VALUES (?, 'ws', 'local')`, ws); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}

	// 全部 5 枚举必须可插入。
	for _, role := range []string{"student", "teacher", "admin", "parent", "org_admin"} {
		if _, err := db.Exec(`INSERT INTO users (id, workspace_id, display_name, role) VALUES (?, ?, 'u', ?)`, "u_"+role, ws, role); err != nil {
			t.Errorf("users.role CHECK must accept %q: %v", role, err)
		}
	}
	// 未知枚举仍被拒绝。
	if _, err := db.Exec(`INSERT INTO users (id, workspace_id, display_name, role) VALUES ('ub1', ?, '非法', 'superuser')`, ws); err == nil {
		t.Errorf("users.role CHECK must reject unknown enum after 0008")
	}

	// workspaces 增加 org 元数据列。
	wCols := tableColumns(t, db, "workspaces")
	for _, col := range []string{"org_name", "org_admin_user_id"} {
		if !wCols[col] {
			t.Errorf("workspaces missing org column %s after 0008", col)
		}
	}

	// 重建后 FK 引用有效：learning_goals 引用 org_admin 用户。
	if _, err := db.Exec(`INSERT INTO learning_goals (id, workspace_id, user_id, name, daily_minutes) VALUES ('lg1', ?, 'u_org_admin', '目标', 30)`, ws); err != nil {
		t.Errorf("FK to rebuilt users broken: %v", err)
	}
	// FK 已恢复为 ON：引用不存在用户的写入必须被拒绝。
	if _, err := db.Exec(`INSERT INTO learning_goals (id, workspace_id, user_id, name, daily_minutes) VALUES ('lg2', ?, 'nosuchuser', '目标', 30)`, ws); err == nil {
		t.Errorf("foreign_keys must be restored to ON after migrate")
	}
}

// TestMigrateV0008Upgrade 升级路径：旧库(0001–0007)含数据 → 应用 0008 → 用户数据保留、
// disabled_at 保留、角色未变、org 列存在且默认 NULL、引用 users 的历史行仍有效。
func TestMigrateV0008Upgrade(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	// 先应用 0001–0007（模拟既有库，含 users 全列与引用 users 的历史数据）。
	applyMigrationsUpTo(t, db, "0007")
	seedRows(t, db)
	// 0006 的 parent 用户与 disabled_at 数据（0008 不得丢列/丢数据）。
	if _, err := db.Exec(`INSERT INTO users (id, workspace_id, display_name, role, disabled_at) VALUES ('up1', 'w1', '家长', 'parent', '2026-02-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert parent user: %v", err)
	}

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate upgrade: %v", err)
	}

	// 旧用户数据保留且角色未变。
	var role string
	if err := db.QueryRow(`SELECT role FROM users WHERE id='u1'`).Scan(&role); err != nil {
		t.Fatalf("user row lost after 0008 rebuild: %v", err)
	}
	if role != "student" {
		t.Errorf("user role changed: %s", role)
	}

	// disabled_at 保留：parent 用户的禁用时间未丢，历史学生用户仍为 NULL。
	var disabled sql.NullString
	if err := db.QueryRow(`SELECT disabled_at FROM users WHERE id='up1'`).Scan(&disabled); err != nil {
		t.Fatal(err)
	}
	if !disabled.Valid || disabled.String != "2026-02-01T00:00:00Z" {
		t.Errorf("parent user disabled_at lost after 0008 rebuild: %+v", disabled)
	}
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

	// org 列存在且旧数据默认 NULL。
	var orgName, orgAdmin sql.NullString
	if err := db.QueryRow(`SELECT org_name, org_admin_user_id FROM workspaces WHERE id='w1'`).Scan(&orgName, &orgAdmin); err != nil {
		t.Fatalf("read workspaces org cols: %v", err)
	}
	if orgName.Valid || orgAdmin.Valid {
		t.Errorf("historical workspace org cols should be NULL, got %q/%q", orgName.String, orgAdmin.String)
	}

	// 升级后 org_admin 可插入、未知枚举被拒。
	if _, err := db.Exec(`INSERT INTO users (id, workspace_id, display_name, role) VALUES ('uoa1', 'w1', 'org', 'org_admin')`); err != nil {
		t.Errorf("org_admin enum must be accepted after 0008 upgrade: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, workspace_id, display_name, role) VALUES ('ub2', 'w1', '非法', 'superuser')`); err == nil {
		t.Errorf("unknown enum must be rejected after 0008 upgrade")
	}
}
