// Package database 负责 SQLite 连接、PRAGMA 配置与版本化迁移。
package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"

	"lumo/internal/domain"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open 打开（或创建）SQLite 数据库并设置连接级 PRAGMA。
func Open(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// WAL + 外键 + 有限忙等待：写事务短小，避免长锁。
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA synchronous = NORMAL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("apply pragma %q: %w", pragma, err)
		}
	}
	return db, nil
}

// Migrate 按文件名顺序执行 migrations 目录中未应用的迁移。
// 每个迁移执行前计算校验和，执行后写入 schema_migrations。
func Migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
		checksum TEXT NOT NULL
	)`); err != nil {
		return err
	}

	applied := map[string]string{}
	rows, err := db.QueryContext(ctx, `SELECT version, checksum FROM schema_migrations`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v, c string
		if err := rows.Scan(&v, &c); err != nil {
			rows.Close()
			return err
		}
		applied[v] = c
	}
	rows.Close()

	names, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)

	// 迁移在固定连接上执行：部分迁移（如 0006 重建 users 表）需要临时关闭外键检查，
	// 而 PRAGMA foreign_keys 只能在事务外修改且作用于单条连接，因此借用 db.Conn
	// 独占一条连接；迁移结束（含出错）后恢复 PRAGMA 原状再归还连接池。
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	restorePragmas := func() {
		// 用独立的 background context 保证恢复逻辑不被调用方取消影响。
		_, _ = conn.ExecContext(context.Background(), `PRAGMA defer_foreign_keys = OFF`)
		_, _ = conn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`)
	}
	defer restorePragmas()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA defer_foreign_keys = ON`); err != nil {
		return err
	}

	for _, name := range names {
		version := strings.TrimSuffix(filepath.Base(name), ".sql")
		if _, ok := applied[version]; ok {
			continue
		}
		b, err := migrationsFS.ReadFile(name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		checksum := hex.EncodeToString(sum[:])
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(b)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, checksum) VALUES (?, ?)`, version, checksum); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Health 检查数据库可用性。
func Health(ctx context.Context, db *sql.DB) error {
	var one int
	if err := db.QueryRowContext(ctx, `SELECT 1`).Scan(&one); err != nil {
		return domain.WrapError(domain.CodeDatabaseUnavailable, "数据库不可用", err)
	}
	return nil
}
