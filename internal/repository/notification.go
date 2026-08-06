package repository

import (
	"context"
	"strings"

	"lumo/internal/domain"
)

// NotificationRow 是 notifications 表行（4.14 通知 / 6.2.1）。
type NotificationRow struct {
	ID           string
	UserID       string
	Kind         string
	TitleKey     string
	BodyArgsJSON string
	RefType      *string
	RefID        *string
	ReadAt       *string
	CreatedAt    string
}

// CreateNotification 写入一条通知（用户级领域事件持久化）。
func (r *Repo) CreateNotification(ctx context.Context, n *NotificationRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO notifications (id, user_id, kind, title_key, body_args_json, ref_type, ref_id)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		n.ID, n.UserID, n.Kind, n.TitleKey, n.BodyArgsJSON, n.RefType, n.RefID)
	return normalizeErr(err)
}

const notificationCols = `id, user_id, kind, title_key, body_args_json, ref_type, ref_id, read_at, created_at`

// ListNotifications 按 user_id 分页列出通知（newest-first）。
// unreadOnly 仅返回未读；cursor 为上一页返回的游标（created_at|id，即该页最后一行）。
// limit<=0 使用默认 20；返回多取一行用于判定 has_more 与下一页游标。
func (r *Repo) ListNotifications(ctx context.Context, userID string, unreadOnly bool, cursor string, limit int) ([]*NotificationRow, string, bool, error) {
	if limit <= 0 {
		limit = 20
	}
	query := `SELECT ` + notificationCols + ` FROM notifications WHERE user_id = ?`
	args := []any{userID}
	if unreadOnly {
		query += ` AND read_at IS NULL`
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

	out := make([]*NotificationRow, 0, limit)
	var nextCursor string
	var hasMore bool
	for rows.Next() {
		var n NotificationRow
		if err := rows.Scan(&n.ID, &n.UserID, &n.Kind, &n.TitleKey, &n.BodyArgsJSON,
			&n.RefType, &n.RefID, &n.ReadAt, &n.CreatedAt); err != nil {
			return nil, "", false, normalizeErr(err)
		}
		if len(out) == limit {
			// 第 limit+1 行：存在即说明还有更多（该行由下一页经游标取回）
			hasMore = true
			continue
		}
		out = append(out, &n)
	}
	if err := rows.Err(); err != nil {
		return nil, "", false, normalizeErr(err)
	}
	// 游标 = 本页最后一行（排他语义：下一页取 created_at<cur OR (=且 id<cur)）。
	if len(out) > 0 {
		last := out[len(out)-1]
		nextCursor = last.CreatedAt + "|" + last.ID
	}
	return out, nextCursor, hasMore, nil
}

// MarkNotificationsRead 批量标记已读（仅标记属于该用户的通知），返回实际更新行数。
// 空 ids 直接返回 0。
func (r *Repo) MarkNotificationsRead(ctx context.Context, userID string, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	args = append(args, userID)
	for _, id := range ids {
		args = append(args, id)
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE notifications SET read_at = ? WHERE user_id = ? AND id IN (`+placeholders+`) AND read_at IS NULL`,
		append([]any{domain.NowUTC()}, args...)...)
	if err != nil {
		return 0, normalizeErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, normalizeErr(err)
	}
	return int(n), nil
}
