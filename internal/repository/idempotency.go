package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// ErrIdempotencyMiss 表示幂等键未命中。
var ErrIdempotencyMiss = errors.New("idempotency key miss")

// placeholderResponse 是占位响应（请求处理中）。
const placeholderResponse = "{}"

// ClaimIdempotency 原子占位：INSERT ... ON CONFLICT DO NOTHING。
// 返回 claimed=true 表示本请求获得执行权；false 表示键已存在。
func (r *Repo) ClaimIdempotency(ctx context.Context, workspaceID, key, method string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO idempotency_keys (workspace_id, idempotency_key, method, response_json)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(workspace_id, idempotency_key) DO NOTHING`,
		workspaceID, key, method, placeholderResponse)
	if err != nil {
		return false, normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// CompleteIdempotency 写入最终响应（仅占位可写，防止覆盖）。
func (r *Repo) CompleteIdempotency(ctx context.Context, workspaceID, key string, response json.RawMessage) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE idempotency_keys SET response_json = ?
		WHERE workspace_id = ? AND idempotency_key = ? AND response_json = ?`,
		string(response), workspaceID, key, placeholderResponse)
	return normalizeErr(err)
}

// ReleaseIdempotency 释放占位（fn 失败时允许重试）。
func (r *Repo) ReleaseIdempotency(ctx context.Context, workspaceID, key string) error {
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM idempotency_keys
		WHERE workspace_id = ? AND idempotency_key = ? AND response_json = ?`,
		workspaceID, key, placeholderResponse)
	return normalizeErr(err)
}

// GetIdempotentResponse 查询幂等键对应的历史响应；未命中或仍在处理中返回 ErrIdempotencyMiss。
func (r *Repo) GetIdempotentResponse(ctx context.Context, workspaceID, key string) (json.RawMessage, error) {
	var raw string
	err := r.db.QueryRowContext(ctx, `
		SELECT response_json FROM idempotency_keys
		WHERE workspace_id = ? AND idempotency_key = ?`, workspaceID, key).Scan(&raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrIdempotencyMiss
		}
		return nil, normalizeErr(err)
	}
	if raw == placeholderResponse {
		return nil, ErrIdempotencyMiss
	}
	return json.RawMessage(raw), nil
}
