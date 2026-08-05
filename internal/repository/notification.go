package repository

import "context"

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
