package repository

import (
	"context"
	"encoding/json"
)

// AuditEvent 是审计事件。
type AuditEvent struct {
	ID          string
	WorkspaceID string
	ActorID     *string
	Action      string
	EntityType  string
	EntityID    *string
	RequestID   *string
	Payload     json.RawMessage
}

// AppendAudit 写入审计事件。
func (r *Repo) AppendAudit(ctx context.Context, e *AuditEvent) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO audit_events (id, workspace_id, actor_id, action, entity_type, entity_id, request_id, payload_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.WorkspaceID, e.ActorID, e.Action, e.EntityType, e.EntityID, e.RequestID, string(e.Payload))
	return normalizeErr(err)
}
