package repository

import (
	"context"
	"database/sql"
	"strings"
)

// ReportRow 是 reports 表行（4.12 学习报告 / 6.2.1）。
type ReportRow struct {
	ID          string
	WorkspaceID string
	UserID      string
	Period      string
	PeriodStart string
	PeriodEnd   string
	PayloadJSON string
	Status      string
	CreatedAt   string
	UpdatedAt   string
}

const reportCols = `id, workspace_id, user_id, period, period_start, period_end, payload_json, status, created_at, updated_at`

// CreateReport 插入一条报告（初始 status='generating'）。
func (r *Repo) CreateReport(ctx context.Context, row *ReportRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO reports (id, workspace_id, user_id, period, period_start, period_end, payload_json, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.WorkspaceID, row.UserID, row.Period,
		row.PeriodStart, row.PeriodEnd, row.PayloadJSON, row.Status)
	return normalizeErr(err)
}

// GetReport 按工作区 + 报告 id 查询；不存在返回 nil,nil。
func (r *Repo) GetReport(ctx context.Context, workspaceID, reportID string) (*ReportRow, error) {
	var row ReportRow
	err := r.db.QueryRowContext(ctx,
		`SELECT `+reportCols+` FROM reports WHERE workspace_id = ? AND id = ? LIMIT 1`,
		workspaceID, reportID).
		Scan(&row.ID, &row.WorkspaceID, &row.UserID, &row.Period,
			&row.PeriodStart, &row.PeriodEnd, &row.PayloadJSON, &row.Status,
			&row.CreatedAt, &row.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, normalizeErr(err)
	}
	return &row, nil
}

// UpdateReportResult 报告生成完成后回写 payload 与终态（ready|failed），按 id 更新。
func (r *Repo) UpdateReportResult(ctx context.Context, id, status, payloadJSON, updatedAt string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE reports SET status = ?, payload_json = ?, updated_at = ?
		WHERE id = ?`,
		status, payloadJSON, updatedAt, id)
	return normalizeErr(err)
}

// ListReports 分页列出报告（newest-first；period 为空不过滤周期）。
// cursor 为上一页返回的游标（created_at|id）；limit<=0 使用默认 20；
// 返回多取一行用于判定 has_more 与下一页游标（同 ListNotifications 模式）。
func (r *Repo) ListReports(ctx context.Context, workspaceID, userID, period, cursor string, limit int) ([]*ReportRow, string, bool, error) {
	if limit <= 0 {
		limit = 20
	}
	query := `SELECT ` + reportCols + ` FROM reports WHERE workspace_id = ? AND user_id = ?`
	args := []any{workspaceID, userID}
	if period != "" {
		query += ` AND period = ?`
		args = append(args, period)
	}
	if cursor != "" {
		createdAt, id, ok := strings.Cut(cursor, "|")
		if ok && createdAt != "" && id != "" {
			query += ` AND (created_at < ? OR (created_at = ? AND id < ?))`
			args = append(args, createdAt, createdAt, id)
		}
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", false, normalizeErr(err)
	}
	defer rows.Close()

	out := make([]*ReportRow, 0, limit)
	var nextCursor string
	var hasMore bool
	for rows.Next() {
		var row ReportRow
		if err := rows.Scan(&row.ID, &row.WorkspaceID, &row.UserID, &row.Period,
			&row.PeriodStart, &row.PeriodEnd, &row.PayloadJSON, &row.Status,
			&row.CreatedAt, &row.UpdatedAt); err != nil {
			return nil, "", false, normalizeErr(err)
		}
		if len(out) == limit {
			hasMore = true
			continue
		}
		out = append(out, &row)
	}
	if err := rows.Err(); err != nil {
		return nil, "", false, normalizeErr(err)
	}
	if len(out) > 0 {
		last := out[len(out)-1]
		nextCursor = last.CreatedAt + "|" + last.ID
	}
	return out, nextCursor, hasMore, nil
}

// GetReportCache 读取报告聚合缓存（period_key 唯一）；不存在返回 "",false,nil。
func (r *Repo) GetReportCache(ctx context.Context, periodKey string) (string, bool, error) {
	var payload string
	err := r.db.QueryRowContext(ctx,
		`SELECT payload_json FROM report_cache WHERE period_key = ?`, periodKey).Scan(&payload)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, normalizeErr(err)
	}
	return payload, true, nil
}

// PutReportCache 写入报告聚合缓存（upsert，同周期覆盖）。
func (r *Repo) PutReportCache(ctx context.Context, periodKey, payloadJSON string) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO report_cache (period_key, payload_json, computed_at)
		VALUES (?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		ON CONFLICT(period_key) DO UPDATE SET
			payload_json = excluded.payload_json,
			computed_at = excluded.computed_at`,
		periodKey, payloadJSON)
	return normalizeErr(err)
}
