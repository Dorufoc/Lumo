package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// openTemp 创建临时数据库。
func openTemp(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestMigrateFresh(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate fresh: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if count < 2 {
		t.Fatalf("expected >=2 migrations, got %d", count)
	}

	// 关键表应存在
	for _, table := range []string{
		"workspaces", "users", "learning_goals", "plan_tasks", "questions",
		"question_versions", "question_knowledge", "import_batches", "import_batch_items",
		"practice_sessions", "submissions", "grading_results", "wrong_answers",
		"review_cards", "review_events", "agent_sessions", "agent_messages", "agent_memory",
		"documents", "document_chunks", "provider_calls", "audit_events", "sync_operations",
		"settings", "knowledge_nodes",
	} {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil {
			t.Fatalf("check table %s: %v", table, err)
		}
		if n != 1 {
			t.Errorf("table %s missing", table)
		}
	}

	// 触发器应存在
	for _, trg := range []string{"trg_questions_current_version_insert", "trg_question_versions_immutable_update"} {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name=?`, trg).Scan(&n); err != nil {
			t.Fatalf("check trigger %s: %v", trg, err)
		}
		if n != 1 {
			t.Errorf("trigger %s missing", trg)
		}
	}
}

func TestMigrateIdempotent(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count < 2 {
		t.Fatalf("migrations should not duplicate, count=%d", count)
	}
}

func TestHealth(t *testing.T) {
	db := openTemp(t)
	if err := Health(context.Background(), db); err != nil {
		t.Fatalf("health: %v", err)
	}
}

// TestForeignKeysEnabled 验证外键约束已开启（迁移基础要求）。
func TestForeignKeysEnabled(t *testing.T) {
	db := openTemp(t)
	var v int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != 1 {
		t.Fatal("foreign_keys pragma not enabled")
	}
}
