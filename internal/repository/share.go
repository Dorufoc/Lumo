package repository

import (
	"context"
	"database/sql"
)

// ShareRow 是 shares 表行（0005，DDL 冻结）。
// 语义：share=一条分享链接；token 为对外凭据；expires_at NULL=永久；
// revoked_at 非空=已撤销（立即失效）；scan_result 记录分享时的安全扫描结论（"clean"）。
type ShareRow struct {
	ID          string
	WorkspaceID string
	UserID      string
	RefType     string
	RefID       string
	Token       string
	ExpiresAt   *string
	RevokedAt   *string
	ScanResult  *string
	CreatedAt   string
}

const shareCols = `id, workspace_id, user_id, ref_type, ref_id, token, expires_at, revoked_at, scan_result, created_at`

// ScanResultRow 是 content_scan_results 缓存行（0005，DDL 冻结）。
// 缓存键 = (ref_type, ref_id, content_hash)；同一内容重复扫描直接复用，不重复审计。
type ScanResultRow struct {
	ID          string
	RefType     string
	RefID       string
	ContentHash string
	ResultJSON  string // {"clean":bool,"findings":[...]}（json_valid CHECK）
	ScannedAt   string
}

// CreateShare 写入一条分享链接（初始 revoked_at 为 NULL）。
func (r *Repo) CreateShare(ctx context.Context, row *ShareRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO shares (id, workspace_id, user_id, ref_type, ref_id, token, expires_at, scan_result)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.WorkspaceID, row.UserID, row.RefType, row.RefID, row.Token,
		row.ExpiresAt, row.ScanResult)
	return normalizeErr(err)
}

// GetShareByID 按 ID 获取分享行；不存在返回 nil,nil。
func (r *Repo) GetShareByID(ctx context.Context, id string) (*ShareRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+shareCols+` FROM shares WHERE id = ?`, id)
	return scanShare(row)
}

// GetShareByToken 按令牌获取分享行（公开链接消费入口）；不存在返回 nil,nil。
func (r *Repo) GetShareByToken(ctx context.Context, token string) (*ShareRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+shareCols+` FROM shares WHERE token = ?`, token)
	return scanShare(row)
}

// ListSharesByWorkspace 列出工作区全部分享链接（新→旧）。
func (r *Repo) ListSharesByWorkspace(ctx context.Context, workspaceID string) ([]*ShareRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+shareCols+` FROM shares
		WHERE workspace_id = ? ORDER BY created_at DESC`, workspaceID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*ShareRow
	for rows.Next() {
		s, err := scanShare(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, normalizeErr(rows.Err())
}

// RevokeShare 撤销分享（revoked_at 写入；WHERE revoked_at IS NULL 保证单次撤销）。
// 返回是否真的写入了撤销时间（false=已撤销或不存在，调用方回查区分）。
func (r *Repo) RevokeShare(ctx context.Context, id, revokedAt string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE shares SET revoked_at = ?
		WHERE id = ? AND revoked_at IS NULL`, revokedAt, id)
	if err != nil {
		return false, normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// GetScanResult 查询内容安全扫描缓存（按 ref + 内容哈希）；无缓存返回 nil,nil。
func (r *Repo) GetScanResult(ctx context.Context, refType, refID, contentHash string) (*ScanResultRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, ref_type, ref_id, content_hash, result_json, scanned_at
		FROM content_scan_results
		WHERE ref_type = ? AND ref_id = ? AND content_hash = ?
		ORDER BY scanned_at DESC, id DESC LIMIT 1`, refType, refID, contentHash)
	return scanScanResult(row)
}

// UpsertScanResult 写入安全扫描结果缓存（按 (ref_type, ref_id, content_hash) 幂等：
// 0005 DDL 未建 UNIQUE 约束，先删同键旧行再插入，保证同内容只留一行）。
func (r *Repo) UpsertScanResult(ctx context.Context, row *ScanResultRow) error {
	return r.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM content_scan_results
			WHERE ref_type = ? AND ref_id = ? AND content_hash = ?`,
			row.RefType, row.RefID, row.ContentHash); err != nil {
			return normalizeErr(err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO content_scan_results (id, ref_type, ref_id, content_hash, result_json)
			VALUES (?, ?, ?, ?, ?)`,
			row.ID, row.RefType, row.RefID, row.ContentHash, row.ResultJSON); err != nil {
			return normalizeErr(err)
		}
		return nil
	})
}

// FlashcardPackExists 判断工作区内是否存在引用指定"闪卡包"的非删除闪卡。
// 0005 无独立 pack 表（DDL 冻结），pack 概念 = 按 (id 或 source_ref) 聚合的一组闪卡：
// ref_id 既可以是单张闪卡 ID，也可以是生成批次标识（source_ref，如某笔记/文档转出的整批卡）。
func (r *Repo) FlashcardPackExists(ctx context.Context, workspaceID, packRefID string) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
		SELECT count(*) FROM flashcards
		WHERE workspace_id = ? AND deleted_at IS NULL
		  AND (id = ? OR source_ref = ?)`, workspaceID, packRefID, packRefID).Scan(&n)
	if err != nil {
		return false, normalizeErr(err)
	}
	return n > 0, nil
}

// ListFlashcardsByPack 返回工作区内属于指定"闪卡包"的非删除闪卡（前端导出/内容扫描用）。
// 语义与 FlashcardPackExists 一致：id 或 source_ref 匹配即视为包成员。
func (r *Repo) ListFlashcardsByPack(ctx context.Context, workspaceID, packRefID string) ([]*FlashcardRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+flashcardCols+` FROM flashcards
		WHERE workspace_id = ? AND deleted_at IS NULL
		  AND (id = ? OR source_ref = ?)
		ORDER BY created_at, id`, workspaceID, packRefID, packRefID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*FlashcardRow
	for rows.Next() {
		f, err := scanFlashcard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, normalizeErr(rows.Err())
}

// ---- 扫描 ----

func scanShare(row interface{ Scan(...any) error }) (*ShareRow, error) {
	var s ShareRow
	err := row.Scan(&s.ID, &s.WorkspaceID, &s.UserID, &s.RefType, &s.RefID, &s.Token,
		&s.ExpiresAt, &s.RevokedAt, &s.ScanResult, &s.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, normalizeErr(err)
	}
	return &s, nil
}

func scanScanResult(row interface{ Scan(...any) error }) (*ScanResultRow, error) {
	var s ScanResultRow
	err := row.Scan(&s.ID, &s.RefType, &s.RefID, &s.ContentHash, &s.ResultJSON, &s.ScannedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, normalizeErr(err)
	}
	return &s, nil
}
