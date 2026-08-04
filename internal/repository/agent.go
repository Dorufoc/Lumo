package repository

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/google/uuid"

	"lumo/internal/domain"
)

// AgentSessionRow 是 agent_sessions 表行。
type AgentSessionRow struct {
	ID            string
	WorkspaceID   string
	UserID        string
	Agent         string
	Status        string
	RequestID     *string
	ContextVer    *string
	CreatedAt     string
	UpdatedAt     string
}

// AgentMessageRow 是 agent_messages 表行。
type AgentMessageRow struct {
	ID            string
	SessionID     string
	Role          string
	ContentRef    *string
	SequenceNo    int
	CreatedAt     string
}

// AgentMemoryRow 是 agent_memory 表行。
type AgentMemoryRow struct {
	ID          string
	WorkspaceID string
	UserID      string
	MemoryType  string
	Summary     string
	SourceRef   *string
	Consent     bool
	ExpiresAt   *string
	CreatedAt   string
	UpdatedAt   string
	DeletedAt   *string
	Version     int
}

const agentSessionCols = `id, workspace_id, user_id, agent, status, request_id, context_version, created_at, updated_at`

func scanAgentSession(row interface{ Scan(...any) error }) (*AgentSessionRow, error) {
	var s AgentSessionRow
	if err := row.Scan(&s.ID, &s.WorkspaceID, &s.UserID, &s.Agent, &s.Status,
		&s.RequestID, &s.ContextVer, &s.CreatedAt, &s.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	return &s, nil
}

// CreateAgentSession 创建 AI 会话。
func (r *Repo) CreateAgentSession(ctx context.Context, s *AgentSessionRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO agent_sessions (id, workspace_id, user_id, agent, status, request_id, context_version)
		VALUES (?, ?, ?, ?, 'created', ?, ?)`,
		s.ID, s.WorkspaceID, s.UserID, s.Agent, s.RequestID, s.ContextVer)
	return normalizeErr(err)
}

// GetAgentSession 获取会话。
func (r *Repo) GetAgentSession(ctx context.Context, wsID, id string) (*AgentSessionRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+agentSessionCols+` FROM agent_sessions
		WHERE id = ? AND workspace_id = ?`, id, wsID)
	return scanAgentSession(row)
}

// UpdateAgentSession 更新会话状态与 request_id。
func (r *Repo) UpdateAgentSession(ctx context.Context, id, status string, requestID *string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE agent_sessions SET status = ?, request_id = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ?`, status, requestID, id)
	return normalizeErr(err)
}

// AppendAgentMessage 追加消息（sequence_no 自动）。
func (r *Repo) AppendAgentMessage(ctx context.Context, sessionID, role, content string) (*AgentMessageRow, error) {
	var seq int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(sequence_no), -1) + 1 FROM agent_messages WHERE session_id = ?`, sessionID).Scan(&seq); err != nil {
		return nil, normalizeErr(err)
	}
	id := newIDLocal()
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO agent_messages (id, session_id, role, content_summary, sequence_no)
		VALUES (?, ?, ?, ?, ?)`, id, sessionID, role, truncateString(content, 2000), seq)
	if err != nil {
		return nil, normalizeErr(err)
	}
	return &AgentMessageRow{ID: id, SessionID: sessionID, Role: role, SequenceNo: seq}, nil
}

// ListAgentMessages 列出会话消息。
func (r *Repo) ListAgentMessages(ctx context.Context, sessionID string, limit int) ([]*AgentMessageRow, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, session_id, role, content_summary, sequence_no, created_at
		FROM agent_messages WHERE session_id = ? ORDER BY sequence_no DESC LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*AgentMessageRow
	for rows.Next() {
		var m AgentMessageRow
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.ContentRef, &m.SequenceNo, &m.CreatedAt); err != nil {
			return nil, normalizeErr(err)
		}
		out = append(out, &m)
	}
	// 反转为升序
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// ListAgentMemory 列出长期记忆。
func (r *Repo) ListAgentMemory(ctx context.Context, wsID, userID string) ([]*AgentMemoryRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, workspace_id, user_id, memory_type, summary, source_ref, consent, expires_at, created_at, updated_at, version
		FROM agent_memory
		WHERE workspace_id = ? AND user_id = ? AND deleted_at IS NULL
		ORDER BY created_at DESC`, wsID, userID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*AgentMemoryRow
	for rows.Next() {
		var m AgentMemoryRow
		var consent int
		if err := rows.Scan(&m.ID, &m.WorkspaceID, &m.UserID, &m.MemoryType, &m.Summary,
			&m.SourceRef, &consent, &m.ExpiresAt, &m.CreatedAt, &m.UpdatedAt, &m.Version); err != nil {
			return nil, normalizeErr(err)
		}
		m.Consent = consent == 1
		out = append(out, &m)
	}
	return out, rows.Err()
}

// GetAgentMemory 获取记忆。
func (r *Repo) GetAgentMemory(ctx context.Context, wsID, id string) (*AgentMemoryRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, workspace_id, user_id, memory_type, summary, source_ref, consent, expires_at, created_at, updated_at, version
		FROM agent_memory WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`, id, wsID)
	var m AgentMemoryRow
	var consent int
	if err := row.Scan(&m.ID, &m.WorkspaceID, &m.UserID, &m.MemoryType, &m.Summary,
		&m.SourceRef, &consent, &m.ExpiresAt, &m.CreatedAt, &m.UpdatedAt, &m.Version); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	m.Consent = consent == 1
	return &m, nil
}

// SaveAgentMemory 保存记忆（consent 用户授权）。
func (r *Repo) SaveAgentMemory(ctx context.Context, m *AgentMemoryRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO agent_memory (id, workspace_id, user_id, memory_type, summary, source_ref, consent, expires_at, version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1)`,
		m.ID, m.WorkspaceID, m.UserID, m.MemoryType, m.Summary, m.SourceRef,
		boolToInt(m.Consent), m.ExpiresAt)
	return normalizeErr(err)
}

// SoftDeleteAgentMemory 软删除记忆（乐观锁）。
func (r *Repo) SoftDeleteAgentMemory(ctx context.Context, wsID, id string, version int) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE agent_memory SET deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), version = version + 1
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL AND version = ?`, id, wsID, version)
	if err != nil {
		return normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return domain.NotFound("记忆不存在或已被修改")
	}
	return nil
}

func newIDLocal() string { return uuid.NewString() }

func truncateString(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

var _ = json.Valid
