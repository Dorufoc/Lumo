package repository

import (
	"context"
	"database/sql"

	"lumo/internal/domain"
)

// KnowledgeNodeRow 是 knowledge_nodes 表行。
type KnowledgeNodeRow struct {
	ID          string
	WorkspaceID string
	ParentID    *string
	Name        string
	NodePath    string
	Level       int
	CreatedAt   string
	UpdatedAt   string
	DeletedAt   *string
	Version     int
}

const knowledgeCols = `id, workspace_id, parent_id, name, node_path, level, created_at, updated_at, deleted_at, version`

func scanKnowledge(row interface{ Scan(...any) error }) (*KnowledgeNodeRow, error) {
	var n KnowledgeNodeRow
	if err := row.Scan(&n.ID, &n.WorkspaceID, &n.ParentID, &n.Name, &n.NodePath, &n.Level,
		&n.CreatedAt, &n.UpdatedAt, &n.DeletedAt, &n.Version); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	return &n, nil
}

// CreateKnowledgeNode 创建知识点。
func (r *Repo) CreateKnowledgeNode(ctx context.Context, n *KnowledgeNodeRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO knowledge_nodes (id, workspace_id, parent_id, name, node_path, level, version)
		VALUES (?, ?, ?, ?, ?, ?, 1)`,
		n.ID, n.WorkspaceID, n.ParentID, n.Name, n.NodePath, n.Level)
	return normalizeErr(err)
}

// GetKnowledgeNode 获取知识点。
func (r *Repo) GetKnowledgeNode(ctx context.Context, wsID, id string) (*KnowledgeNodeRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+knowledgeCols+` FROM knowledge_nodes
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`, id, wsID)
	return scanKnowledge(row)
}

// ListKnowledgeNodes 列出工作区全部知识点（按路径排序，service 组装树）。
func (r *Repo) ListKnowledgeNodes(ctx context.Context, wsID string) ([]*KnowledgeNodeRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+knowledgeCols+` FROM knowledge_nodes
		WHERE workspace_id = ? AND deleted_at IS NULL ORDER BY node_path`, wsID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*KnowledgeNodeRow
	for rows.Next() {
		n, err := scanKnowledge(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// UpdateKnowledgeNode 乐观锁更新名称。
func (r *Repo) UpdateKnowledgeNode(ctx context.Context, wsID, id string, version int, name string) (*KnowledgeNodeRow, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE knowledge_nodes SET name = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), version = version + 1
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL AND version = ?`,
		name, id, wsID, version)
	if err != nil {
		return nil, normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		cur, err := r.GetKnowledgeNode(ctx, wsID, id)
		if err != nil {
			return nil, err
		}
		if cur == nil {
			return nil, NotFoundErr("知识点", id)
		}
		return nil, domain.Conflict("知识点已被修改，请刷新后重试")
	}
	return r.GetKnowledgeNode(ctx, wsID, id)
}

// SoftDeleteKnowledgeNode 软删除知识点（有子节点或题目引用时拒绝）。
func (r *Repo) SoftDeleteKnowledgeNode(ctx context.Context, wsID, id string, version int) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return normalizeErr(err)
	}
	defer tx.Rollback()

	var children int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM knowledge_nodes
		WHERE parent_id = ? AND workspace_id = ? AND deleted_at IS NULL`, id, wsID).Scan(&children); err != nil {
		return normalizeErr(err)
	}
	if children > 0 {
		return domain.InvalidState("知识点存在子节点，不能删除")
	}
	var refs int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM question_knowledge qk
		JOIN question_versions qv ON qk.question_version_id = qv.id
		JOIN questions q ON qv.question_id = q.id
		WHERE qk.knowledge_id = ? AND q.workspace_id = ? AND q.deleted_at IS NULL`, id, wsID).Scan(&refs); err != nil {
		return normalizeErr(err)
	}
	if refs > 0 {
		return domain.InvalidState("知识点被 %d 道题引用，请先解除关联", refs)
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE knowledge_nodes SET deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), version = version + 1
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL AND version = ?`,
		id, wsID, version)
	if err != nil {
		return normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return domain.Conflict("知识点已被修改，请刷新后重试")
	}
	return tx.Commit()
}

// SetQuestionKnowledge 重建题目-知识点关联（先删后插）。
func (r *Repo) SetQuestionKnowledge(ctx context.Context, questionVersionID string, knowledgeIDs []string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return normalizeErr(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM question_knowledge WHERE question_version_id = ?`, questionVersionID); err != nil {
		return normalizeErr(err)
	}
	for _, kid := range knowledgeIDs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO question_knowledge (question_version_id, knowledge_id, weight) VALUES (?, ?, 1)`,
			questionVersionID, kid); err != nil {
			return normalizeErr(err)
		}
	}
	return tx.Commit()
}
