package repository

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"lumo/internal/database"
)

// TestVacuumInto 验证 SQLite VACUUM INTO 支持（备份一致性快照依赖）。
func TestVacuumInto(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO t (v) VALUES ('hello')`); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "snapshot.db")
	if _, err := db.Exec(`VACUUM INTO ?`, dst); err != nil {
		t.Fatalf("VACUUM INTO not supported: %v", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("snapshot missing: %v", err)
	}
	snap, err := sql.Open("sqlite", dst)
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	var v string
	if err := snap.QueryRow(`SELECT v FROM t`).Scan(&v); err != nil {
		t.Fatalf("snapshot query: %v", err)
	}
	if v != "hello" {
		t.Fatalf("unexpected value: %s", v)
	}
	// 快照文件应可被数据库包重新打开（完整性）
	if err := database.Health(context.Background(), snap); err != nil {
		t.Fatalf("snapshot health: %v", err)
	}
}
