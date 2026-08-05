package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"lumo/internal/domain"
)

// DocumentRow 是 documents 表行。
type DocumentRow struct {
	ID            string
	WorkspaceID   string
	RelativePath  string
	FileName      string
	MimeType      string
	ByteSize      int64
	SHA256        string
	Encrypted     bool
	Status        string
	FailureReason *string
	CreatedAt     string
	UpdatedAt     string
	DeletedAt     *string
	Version       int
}

// DocumentChunkRow 是 document_chunks 表行。
type DocumentChunkRow struct {
	ID               string
	DocumentID       string
	TextRef          string
	SectionName      *string
	PageNo           *int
	ParagraphNo      *int
	StartOffset      int
	EndOffset        int
	EmbeddingVersion string
	VectorRef        *string
	CreatedAt        string
}

const documentCols = `id, workspace_id, relative_path, file_name, mime_type, byte_size, sha256, encrypted,
	status, failure_reason, created_at, updated_at, deleted_at, version`

func scanDocument(row interface{ Scan(...any) error }) (*DocumentRow, error) {
	var d DocumentRow
	var encrypted int
	if err := row.Scan(&d.ID, &d.WorkspaceID, &d.RelativePath, &d.FileName, &d.MimeType, &d.ByteSize,
		&d.SHA256, &encrypted, &d.Status, &d.FailureReason, &d.CreatedAt, &d.UpdatedAt, &d.DeletedAt, &d.Version); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	d.Encrypted = encrypted == 1
	return &d, nil
}

// CreateDocument 创建文档记录。
func (r *Repo) CreateDocument(ctx context.Context, d *DocumentRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO documents (id, workspace_id, relative_path, file_name, mime_type, byte_size,
			sha256, encrypted, status, failure_reason, version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, 1)`,
		d.ID, d.WorkspaceID, d.RelativePath, d.FileName, d.MimeType, d.ByteSize,
		d.SHA256, boolToInt(d.Encrypted), d.FailureReason)
	return normalizeErr(err)
}

// GetDocument 获取文档。
func (r *Repo) GetDocument(ctx context.Context, wsID, id string) (*DocumentRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+documentCols+` FROM documents
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`, id, wsID)
	return scanDocument(row)
}

// GetDocumentByHash 按哈希查重。
func (r *Repo) GetDocumentByHash(ctx context.Context, wsID, hash string) (*DocumentRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+documentCols+` FROM documents
		WHERE workspace_id = ? AND sha256 = ? AND deleted_at IS NULL`, wsID, hash)
	return scanDocument(row)
}

// ListDocuments 分页列出文档。
func (r *Repo) ListDocuments(ctx context.Context, wsID, status, cursor string, limit int) ([]*DocumentRow, string, bool, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	conds := []string{"workspace_id = ?", "deleted_at IS NULL"}
	args := []any{wsID}
	if status != "" {
		conds = append(conds, "status = ?")
		args = append(args, status)
	}
	if cursor != "" {
		conds = append(conds, "id < ?")
		args = append(args, cursor)
	}
	query := `SELECT ` + documentCols + ` FROM documents WHERE ` + strings.Join(conds, " AND ") +
		` ORDER BY id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", false, normalizeErr(err)
	}
	defer rows.Close()
	var out []*DocumentRow
	for rows.Next() {
		d, err := scanDocument(rows)
		if err != nil {
			return nil, "", false, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, "", false, normalizeErr(err)
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	next := ""
	if hasMore && len(out) > 0 {
		next = out[len(out)-1].ID
	}
	return out, next, hasMore, nil
}

// UpdateDocumentStatus 更新文档处理状态。
func (r *Repo) UpdateDocumentStatus(ctx context.Context, id, status string, failure *string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE documents SET status = ?, failure_reason = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ?`, status, failure, id)
	return normalizeErr(err)
}

// SoftDeleteDocument 软删除文档（级联删除 chunks 由 service 处理）。
func (r *Repo) SoftDeleteDocument(ctx context.Context, wsID, id string, version int) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE documents SET deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), version = version + 1
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL AND version = ?`, id, wsID, version)
	if err != nil {
		return normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return domain.NotFound("文档不存在或已被修改")
	}
	return nil
}

// ReplaceDocumentChunks 重建文档分块（先删后插）。
// ReplaceDocumentIndex 重建文档分块与全文索引（同一事务：先删后插，保证 chunk 与 FTS 一致）。
// documents_fts 无触发器，正文经分块后在此写入（body_cjk 由 SpaceCJK 逐字空格化）。
func (r *Repo) ReplaceDocumentIndex(ctx context.Context, docID, workspaceID string, chunks []*DocumentChunkRow) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return normalizeErr(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM document_chunks WHERE document_id = ?`, docID); err != nil {
		return normalizeErr(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM documents_fts WHERE document_id = ?`, docID); err != nil {
		return normalizeErr(err)
	}
	for _, c := range chunks {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO document_chunks (id, document_id, text_ref, section_name, paragraph_no,
				start_offset, end_offset, embedding_version, vector_ref)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ID, c.DocumentID, c.TextRef, c.SectionName, c.ParagraphNo,
			c.StartOffset, c.EndOffset, c.EmbeddingVersion, c.VectorRef); err != nil {
			return normalizeErr(err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO documents_fts (chunk_id, document_id, workspace_id, body, body_cjk)
			VALUES (?, ?, ?, ?, ?)`,
			c.ID, c.DocumentID, workspaceID, c.TextRef, SpaceCJK(c.TextRef)); err != nil {
			return normalizeErr(err)
		}
	}
	return tx.Commit()
}

// DeleteDocumentFTS 删除文档的全部全文索引行（软删除时清理，避免已删除文档被检索到）。
func (r *Repo) DeleteDocumentFTS(ctx context.Context, docID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM documents_fts WHERE document_id = ?`, docID)
	return normalizeErr(err)
}

// ChunkRow 是检索用分块行。
type ChunkRow struct {
	ID        string
	DocID     string
	TextRef   string
	Section   *string
	Embedding string // 版本
	Vector    *string
}

// ListDocumentChunks 列出文档分块。
func (r *Repo) ListDocumentChunks(ctx context.Context, wsID string, docIDs []string) ([]*ChunkRow, error) {
	query := `SELECT c.id, c.document_id, c.text_ref, c.section_name, c.embedding_version, c.vector_ref
		FROM document_chunks c JOIN documents d ON c.document_id = d.id
		WHERE d.workspace_id = ? AND d.deleted_at IS NULL`
	args := []any{wsID}
	if len(docIDs) > 0 {
		placeholders := make([]string, len(docIDs))
		for i, id := range docIDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		query += ` AND c.document_id IN (` + strings.Join(placeholders, ",") + `)`
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*ChunkRow
	for rows.Next() {
		var c ChunkRow
		var vector *string
		if err := rows.Scan(&c.ID, &c.DocID, &c.TextRef, &c.Section, &c.Embedding, &vector); err != nil {
			return nil, normalizeErr(err)
		}
		if vector != nil && *vector != "" {
			c.Vector = vector
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

var _ = json.Marshal
