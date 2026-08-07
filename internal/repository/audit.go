package repository

import (
	"context"
	"database/sql"
	"encoding/json"
)

// AuditEvent 是审计事件。
type AuditEvent struct {
	ID          string
	WorkspaceID string
	ActorID     *string
	ActorRole   *string
	Action      string
	EntityType  string
	EntityID    *string
	RequestID   *string
	Payload     json.RawMessage
	BeforeJSON  json.RawMessage
	AfterJSON   json.RawMessage
}

// AppendAudit 写入审计事件。
func (r *Repo) AppendAudit(ctx context.Context, e *AuditEvent) error {
	return r.appendAudit(ctx, r.db, e)
}

// AppendAuditTx 在事务内写入审计事件（管理写操作审计门禁用，失败随事务回滚）。
func (r *Repo) AppendAuditTx(ctx context.Context, tx *sql.Tx, e *AuditEvent) error {
	return r.appendAudit(ctx, tx, e)
}

// appendAudit 执行审计写入（db 或事务共用）。
func (r *Repo) appendAudit(ctx context.Context, q queryer, e *AuditEvent) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO audit_events (id, workspace_id, actor_id, actor_role, action, entity_type, entity_id, request_id, payload_json, before_json, after_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.WorkspaceID, e.ActorID, e.ActorRole, e.Action, e.EntityType, e.EntityID,
		e.RequestID, string(e.Payload), string(e.BeforeJSON), string(e.AfterJSON))
	return normalizeErr(err)
}
