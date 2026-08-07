// Package repository 实现 SQLite 持久化（internal/service 依赖本包的接口与实现）。
package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"lumo/internal/domain"
)

// Repo 聚合全部仓储，持有数据库连接。
type Repo struct {
	db *sql.DB
}

// New 构造仓储。
func New(db *sql.DB) *Repo { return &Repo{db: db} }

// DB 暴露底层连接（迁移、备份等基础设施使用）。
func (r *Repo) DB() *sql.DB { return r.db }

// queryer 统一 *sql.DB 与 *sql.Tx（服务层事务与直接执行共用）。
type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// WithTx 在事务中执行 fn，任一步骤失败整体回滚。
func (r *Repo) WithTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return normalizeErr(err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return normalizeErr(tx.Commit())
}

// exec 执行写语句并返回错误归一化。
func (r *Repo) exec(ctx context.Context, query string, args ...any) error {
	_, err := r.db.ExecContext(ctx, query, args...)
	return normalizeErr(err)
}

// normalizeErr 将 SQLite 错误映射为领域错误。
func normalizeErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "UNIQUE constraint failed"):
		return domain.Conflict("资源已存在（唯一约束冲突）")
	case strings.Contains(msg, "FOREIGN KEY constraint failed"):
		return domain.Conflict("关联资源不存在或状态不允许")
	case strings.Contains(msg, "CHECK constraint failed"):
		return domain.InvalidArg("数据不满足约束: %s", msg)
	case strings.Contains(msg, "database is locked"):
		return domain.WrapError(domain.CodeDatabaseUnavailable, "数据库繁忙，请稍后重试", err)
	default:
		return err
	}
}

// MarshalJSON 序列化任意值，失败时返回空对象。
func MarshalJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// NotFoundErr 构造 NOT_FOUND 领域错误。
func NotFoundErr(entity string, id string) *domain.Error {
	return domain.NotFound("%s 不存在或已被删除", fmt.Sprintf("%s(%s)", entity, id))
}
