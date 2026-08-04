package repository

import (
	"context"
	"database/sql"
	"encoding/json"

	"lumo/internal/domain"
)

// ImportBatchRow 是 import_batches 表行。
type ImportBatchRow struct {
	ID            string
	WorkspaceID   string
	IdempotencyKey string
	FileName      string
	FileHash      string
	Format        string
	Status        string
	TotalCount    int
	ValidCount    int
	ErrorCount    int
	CreatedAt     string
	UpdatedAt     string
}

// ImportItemRow 是 import_batch_items 表行。
type ImportItemRow struct {
	ID         string
	BatchID    string
	ItemNo     int
	Payload    json.RawMessage
	Status     string
	ErrorJSON  *string
	QuestionID *string
}

const importBatchCols = `id, workspace_id, idempotency_key, file_name, file_hash, format, status, total_count, valid_count, error_count, created_at, updated_at`

func scanImportBatch(row interface{ Scan(...any) error }) (*ImportBatchRow, error) {
	var b ImportBatchRow
	if err := row.Scan(&b.ID, &b.WorkspaceID, &b.IdempotencyKey, &b.FileName, &b.FileHash,
		&b.Format, &b.Status, &b.TotalCount, &b.ValidCount, &b.ErrorCount, &b.CreatedAt, &b.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	return &b, nil
}

// CreateImportBatch 创建批次。
func (r *Repo) CreateImportBatch(ctx context.Context, b *ImportBatchRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO import_batches (id, workspace_id, idempotency_key, file_name, file_hash, format, status,
			total_count, valid_count, error_count)
		VALUES (?, ?, ?, ?, ?, ?, 'validating', ?, 0, 0)`,
		b.ID, b.WorkspaceID, b.IdempotencyKey, b.FileName, b.FileHash, b.Format, b.TotalCount)
	return normalizeErr(err)
}

// GetImportBatch 获取批次。
func (r *Repo) GetImportBatch(ctx context.Context, wsID, id string) (*ImportBatchRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+importBatchCols+` FROM import_batches WHERE id = ? AND workspace_id = ?`, id, wsID)
	return scanImportBatch(row)
}

// GetImportBatchByHash 按文件哈希获取批次。
func (r *Repo) GetImportBatchByHash(ctx context.Context, wsID, hash string) (*ImportBatchRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+importBatchCols+` FROM import_batches WHERE workspace_id = ? AND file_hash = ?`, wsID, hash)
	return scanImportBatch(row)
}

// GetImportBatchByKey 按幂等键获取批次。
func (r *Repo) GetImportBatchByKey(ctx context.Context, wsID, key string) (*ImportBatchRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+importBatchCols+` FROM import_batches WHERE workspace_id = ? AND idempotency_key = ?`, wsID, key)
	return scanImportBatch(row)
}

// SetImportBatchReady 将批次标记为 ready 并更新计数。
func (r *Repo) SetImportBatchReady(ctx context.Context, id string, total, valid, errCount int) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE import_batches SET status = 'ready', total_count = ?, valid_count = ?, error_count = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ? AND status = 'validating'`, total, valid, errCount, id)
	if err != nil {
		return normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return domain.InvalidState("批次状态不允许该操作")
	}
	return nil
}

// SetImportBatchCommitted 标记批次已提交。
func (r *Repo) SetImportBatchCommitted(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE import_batches SET status = 'committed',
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ? AND status = 'ready'`, id)
	if err != nil {
		return normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return domain.InvalidState("批次状态不允许提交（仅 ready 可提交）")
	}
	return nil
}

// CreateImportItems 批量插入条目。
func (r *Repo) CreateImportItems(ctx context.Context, items []*ImportItemRow) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return normalizeErr(err)
	}
	defer tx.Rollback()
	for _, it := range items {
		var errJSON any
		if it.ErrorJSON != nil {
			errJSON = *it.ErrorJSON
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO import_batch_items (id, batch_id, item_no, payload_json, status, error_json)
			VALUES (?, ?, ?, ?, ?, ?)`,
			it.ID, it.BatchID, it.ItemNo, string(it.Payload), it.Status, errJSON); err != nil {
			return normalizeErr(err)
		}
	}
	return tx.Commit()
}

// ListImportItems 列出批次条目。
func (r *Repo) ListImportItems(ctx context.Context, batchID string) ([]*ImportItemRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, batch_id, item_no, payload_json, status, error_json, question_id
		FROM import_batch_items WHERE batch_id = ? ORDER BY item_no`, batchID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*ImportItemRow
	for rows.Next() {
		var it ImportItemRow
		var payload string
		if err := rows.Scan(&it.ID, &it.BatchID, &it.ItemNo, &payload, &it.Status, &it.ErrorJSON, &it.QuestionID); err != nil {
			return nil, normalizeErr(err)
		}
		it.Payload = json.RawMessage(payload)
		out = append(out, &it)
	}
	return out, rows.Err()
}

// MarkImportItemImported 标记条目已导入。
func (r *Repo) MarkImportItemImported(ctx context.Context, id, questionID string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE import_batch_items SET status = 'imported', question_id = ? WHERE id = ?`,
		questionID, id)
	return normalizeErr(err)
}

// MarkImportItemInvalid 标记条目无效（重复等）。
func (r *Repo) MarkImportItemInvalid(ctx context.Context, id, reason string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE import_batch_items SET status = 'invalid', error_json = ? WHERE id = ?`,
		`{"reason":"`+reason+`"}`, id)
	return normalizeErr(err)
}
