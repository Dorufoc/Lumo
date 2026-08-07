package repository

import (
	"context"
	"database/sql"

	"lumo/internal/domain"
)

// WebhookSubscriptionRow 是 webhook_subscriptions 表行（0005，DDL 冻结）。
// SecretRef 指向 secrets.json 内的键名（密钥不落库）；EventTypesJSON 为事件白名单 JSON 数组。
type WebhookSubscriptionRow struct {
	ID             string
	WorkspaceID    string
	URL            string
	SecretRef      *string
	EventTypesJSON string
	Enabled        bool
	CreatedAt      string
	UpdatedAt      string
}

const webhookSubCols = `id, workspace_id, url, secret_ref, event_types, enabled, created_at, updated_at`

// WebhookDeliveryRow 是 webhook_deliveries 表行（0005 + 0007：status 增加 'dead'，
// 另增 payload_json 供重试重发原始载荷；0007 迁移新列，旧行迁移后为 NULL）。
// 投递状态机：pending → sent / pending_retry →（重试）→ sent / dead。
type WebhookDeliveryRow struct {
	ID             string
	SubscriptionID string
	EventID        string
	Status         string
	Attempt        int
	NextRetryAt    *string
	LastError      *string
	PayloadJSON    *string
	CreatedAt      string
}

const webhookDeliveryCols = `id, subscription_id, event_id, status, attempt, next_retry_at, last_error, payload_json, created_at`

// CreateWebhookSubscription 写入一条 Webhook 订阅。
func (r *Repo) CreateWebhookSubscription(ctx context.Context, row *WebhookSubscriptionRow) error {
	enabled := 0
	if row.Enabled {
		enabled = 1
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO webhook_subscriptions (id, workspace_id, url, secret_ref, event_types, enabled)
		VALUES (?, ?, ?, ?, ?, ?)`,
		row.ID, row.WorkspaceID, row.URL, row.SecretRef, row.EventTypesJSON, enabled)
	return normalizeErr(err)
}

// GetWebhookSubscriptionByID 按 ID 获取订阅行；不存在返回 nil,nil。
func (r *Repo) GetWebhookSubscriptionByID(ctx context.Context, id string) (*WebhookSubscriptionRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+webhookSubCols+` FROM webhook_subscriptions WHERE id = ?`, id)
	return scanWebhookSubscription(row)
}

// ListWebhookSubscriptionsByWorkspace 列出工作区全部订阅（新→旧）。
func (r *Repo) ListWebhookSubscriptionsByWorkspace(ctx context.Context, workspaceID string) ([]*WebhookSubscriptionRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+webhookSubCols+` FROM webhook_subscriptions
		WHERE workspace_id = ? ORDER BY created_at DESC`, workspaceID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*WebhookSubscriptionRow
	for rows.Next() {
		s, err := scanWebhookSubscription(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, normalizeErr(rows.Err())
}

// DeleteWebhookSubscription 删除订阅及其全部投递记录。
// 存在进行中投递（pending/pending_retry）时返回 Conflict：删除订阅会丢失重试语义，
// 需等投递收敛（sent/dead）后再删。终端状态（sent/failed/dead）投递随订阅一并清除
// （webhook_deliveries.subscription_id 为 ON DELETE RESTRICT，须先删投递再删订阅）。
func (r *Repo) DeleteWebhookSubscription(ctx context.Context, id string) (bool, error) {
	var inFlight int
	if err := r.db.QueryRowContext(ctx, `
		SELECT count(*) FROM webhook_deliveries
		WHERE subscription_id = ? AND status IN ('pending', 'pending_retry')`, id).Scan(&inFlight); err != nil {
		return false, normalizeErr(err)
	}
	if inFlight > 0 {
		return false, domain.Conflict("订阅存在进行中的投递，请等待投递收敛后再删除")
	}
	deleted := false
	err := r.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM webhook_deliveries WHERE subscription_id = ?`, id); err != nil {
			return normalizeErr(err)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM webhook_subscriptions WHERE id = ?`, id)
		if err != nil {
			return normalizeErr(err)
		}
		n, _ := res.RowsAffected()
		deleted = n > 0
		return nil
	})
	return deleted, err
}

// UpdateWebhookSubscriptionEnabled 切换订阅启用状态（Dispatcher 跳过禁用订阅）。
func (r *Repo) UpdateWebhookSubscriptionEnabled(ctx context.Context, id string, enabled bool) error {
	v := 0
	if enabled {
		v = 1
	}
	_, err := r.db.ExecContext(ctx, `UPDATE webhook_subscriptions SET enabled = ? WHERE id = ?`, v, id)
	return normalizeErr(err)
}

// CreateWebhookDelivery 写入一条投递记录（初始 status=pending, attempt=0）。
func (r *Repo) CreateWebhookDelivery(ctx context.Context, row *WebhookDeliveryRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO webhook_deliveries (id, subscription_id, event_id, status, attempt, next_retry_at, last_error, payload_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.SubscriptionID, row.EventID, row.Status, row.Attempt, row.NextRetryAt, row.LastError, row.PayloadJSON)
	return normalizeErr(err)
}

// ListWebhookDeliveriesBySubscription 列出订阅的全部投递记录（新→旧）。
func (r *Repo) ListWebhookDeliveriesBySubscription(ctx context.Context, subscriptionID string) ([]*WebhookDeliveryRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+webhookDeliveryCols+` FROM webhook_deliveries
		WHERE subscription_id = ? ORDER BY created_at DESC, id DESC`, subscriptionID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*WebhookDeliveryRow
	for rows.Next() {
		d, err := scanWebhookDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, normalizeErr(rows.Err())
}

// ListPendingRetryDeliveries 列出到期待重试的投递：status=pending_retry 且
// next_retry_at 非空且 <= now（重试运行器每 tick 扫描；按下次重试时间升序）。
func (r *Repo) ListPendingRetryDeliveries(ctx context.Context, now string) ([]*WebhookDeliveryRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+webhookDeliveryCols+` FROM webhook_deliveries
		WHERE status = 'pending_retry' AND next_retry_at IS NOT NULL AND next_retry_at <= ?
		ORDER BY next_retry_at ASC`, now)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*WebhookDeliveryRow
	for rows.Next() {
		d, err := scanWebhookDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, normalizeErr(rows.Err())
}

// UpdateWebhookDelivery 更新投递结果（status/attempt/next_retry_at/last_error）。
func (r *Repo) UpdateWebhookDelivery(ctx context.Context, row *WebhookDeliveryRow) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE webhook_deliveries
		SET status = ?, attempt = ?, next_retry_at = ?, last_error = ?
		WHERE id = ?`,
		row.Status, row.Attempt, row.NextRetryAt, row.LastError, row.ID)
	return normalizeErr(err)
}

// ---- 扫描 ----

func scanWebhookSubscription(row interface{ Scan(...any) error }) (*WebhookSubscriptionRow, error) {
	var s WebhookSubscriptionRow
	var enabled int
	err := row.Scan(&s.ID, &s.WorkspaceID, &s.URL, &s.SecretRef, &s.EventTypesJSON,
		&enabled, &s.CreatedAt, &s.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, normalizeErr(err)
	}
	s.Enabled = enabled == 1
	return &s, nil
}

func scanWebhookDelivery(row interface{ Scan(...any) error }) (*WebhookDeliveryRow, error) {
	var d WebhookDeliveryRow
	err := row.Scan(&d.ID, &d.SubscriptionID, &d.EventID, &d.Status, &d.Attempt,
		&d.NextRetryAt, &d.LastError, &d.PayloadJSON, &d.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, normalizeErr(err)
	}
	return &d, nil
}
