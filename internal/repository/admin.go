// admin.go 管理端仓储：内容审核队列、Provider 策略、功能开关、用户禁用、审计查询、用量计数。
package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"lumo/internal/domain"
)

// ReviewQueueItemRow 是 review_queue_items 表行。
type ReviewQueueItemRow struct {
	ID         string
	RefType    string
	RefID      string
	Status     string
	ReviewerID *string
	Reason     string
	ReviewedAt *string
	CreatedAt  string
	UpdatedAt  string
}

const reviewQueueCols = `id, ref_type, ref_id, status, reviewer_id, reason, reviewed_at, created_at, updated_at`

func scanReviewQueueItem(row interface{ Scan(...any) error }) (*ReviewQueueItemRow, error) {
	var it ReviewQueueItemRow
	if err := row.Scan(&it.ID, &it.RefType, &it.RefID, &it.Status, &it.ReviewerID,
		&it.Reason, &it.ReviewedAt, &it.CreatedAt, &it.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	return &it, nil
}

// ListReviewQueueItems 列出审核队列（status 为空表示全部；审核队列全局共享）。
func (r *Repo) ListReviewQueueItems(ctx context.Context, status string) ([]*ReviewQueueItemRow, error) {
	query := `SELECT ` + reviewQueueCols + ` FROM review_queue_items`
	var args []any
	if status != "" {
		query += ` WHERE status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*ReviewQueueItemRow
	for rows.Next() {
		it, err := scanReviewQueueItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// GetReviewQueueItem 获取单个审核条目。
func (r *Repo) GetReviewQueueItem(ctx context.Context, id string) (*ReviewQueueItemRow, error) {
	return getReviewQueueItemBy(ctx, r.db, id)
}

// CreateReviewQueueItem 创建审核条目（ref_type + ref_id；status 默认 pending）。
// q 可传 *sql.Tx 使调用方与业务写入同事务。
func (r *Repo) CreateReviewQueueItem(ctx context.Context, q queryer, refType, refID string) (*ReviewQueueItemRow, error) {
	id := newIDLocal()
	if _, err := q.ExecContext(ctx, `
		INSERT INTO review_queue_items (id, ref_type, ref_id, status)
		VALUES (?, ?, ?, 'pending')`, id, refType, refID); err != nil {
		return nil, normalizeErr(err)
	}
	return getReviewQueueItemBy(ctx, q, id)
}

// ListReviewQueueItemsByRef 按 ref_type + ref_id 列出审核条目（倒序）。
func (r *Repo) ListReviewQueueItemsByRef(ctx context.Context, refType, refID string) ([]*ReviewQueueItemRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+reviewQueueCols+` FROM review_queue_items
		WHERE ref_type = ? AND ref_id = ?
		ORDER BY created_at DESC`, refType, refID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*ReviewQueueItemRow
	for rows.Next() {
		it, err := scanReviewQueueItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// GetPendingReviewItemByRef 获取 ref 的 pending 审核条目；无返回 nil。
func (r *Repo) GetPendingReviewItemByRef(ctx context.Context, refType, refID string) (*ReviewQueueItemRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+reviewQueueCols+` FROM review_queue_items
		WHERE ref_type = ? AND ref_id = ? AND status = 'pending'
		ORDER BY created_at ASC LIMIT 1`, refType, refID)
	return scanReviewQueueItem(row)
}

func getReviewQueueItemBy(ctx context.Context, q queryer, id string) (*ReviewQueueItemRow, error) {
	row := q.QueryRowContext(ctx, `
		SELECT `+reviewQueueCols+` FROM review_queue_items WHERE id = ?`, id)
	return scanReviewQueueItem(row)
}

// DecideReviewQueueItem 决策审核条目（仅 pending 可迁移）；0 行受影响时区分不存在与已决。
func (r *Repo) DecideReviewQueueItem(ctx context.Context, q queryer, id, decision, reason string) (*ReviewQueueItemRow, error) {
	res, err := q.ExecContext(ctx, `
		UPDATE review_queue_items SET status = ?, reason = ?,
			reviewed_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ? AND status = 'pending'`, decision, reason, id)
	if err != nil {
		return nil, normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		cur, err := getReviewQueueItemBy(ctx, q, id)
		if err != nil {
			return nil, err
		}
		if cur == nil {
			return nil, NotFoundErr("审核条目", id)
		}
		return nil, domain.InvalidState("审核条目已处理，不能重复决策")
	}
	return getReviewQueueItemBy(ctx, q, id)
}

// ProviderPolicyRow 是 provider_policies 表行。
type ProviderPolicyRow struct {
	ID            string
	Provider      string
	Model         string
	Allowed       bool
	DailyQuota    *int
	MonthlyBudget *int
	UpdatedBy     *string
	CreatedAt     string
	UpdatedAt     string
}

const providerPolicyCols = `id, provider, model, allowed, daily_quota, monthly_budget, updated_by, created_at, updated_at`

func scanProviderPolicy(row interface{ Scan(...any) error }) (*ProviderPolicyRow, error) {
	var p ProviderPolicyRow
	if err := row.Scan(&p.ID, &p.Provider, &p.Model, &p.Allowed, &p.DailyQuota,
		&p.MonthlyBudget, &p.UpdatedBy, &p.CreatedAt, &p.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	return &p, nil
}

// GetProviderPolicy 获取 (provider, model) 策略；不存在返回 nil。
func (r *Repo) GetProviderPolicy(ctx context.Context, provider, model string) (*ProviderPolicyRow, error) {
	return getProviderPolicyBy(ctx, r.db, provider, model)
}

func getProviderPolicyBy(ctx context.Context, q queryer, provider, model string) (*ProviderPolicyRow, error) {
	row := q.QueryRowContext(ctx, `
		SELECT `+providerPolicyCols+` FROM provider_policies
		WHERE provider = ? AND model = ?`, provider, model)
	return scanProviderPolicy(row)
}

// UpsertProviderPolicy 写入或更新策略（无唯一约束：先更新，0 行受影响则插入）。
func (r *Repo) UpsertProviderPolicy(ctx context.Context, q queryer, p *ProviderPolicyRow) (*ProviderPolicyRow, error) {
	res, err := q.ExecContext(ctx, `
		UPDATE provider_policies SET allowed = ?, daily_quota = ?, monthly_budget = ?, updated_by = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE provider = ? AND model = ?`,
		p.Allowed, p.DailyQuota, p.MonthlyBudget, p.UpdatedBy, p.Provider, p.Model)
	if err != nil {
		return nil, normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		if _, err := q.ExecContext(ctx, `
			INSERT INTO provider_policies (id, provider, model, allowed, daily_quota, monthly_budget, updated_by)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			p.ID, p.Provider, p.Model, p.Allowed, p.DailyQuota, p.MonthlyBudget, p.UpdatedBy); err != nil {
			return nil, normalizeErr(err)
		}
	}
	return getProviderPolicyBy(ctx, q, p.Provider, p.Model)
}

// FeatureFlagRow 是 feature_flags 表行。
type FeatureFlagRow struct {
	ID             string
	Key            string
	Enabled        bool
	RolloutPercent int
	UpdatedBy      *string
	CreatedAt      string
	UpdatedAt      string
}

const featureFlagCols = `id, key, enabled, rollout_percent, updated_by, created_at, updated_at`

func scanFeatureFlag(row interface{ Scan(...any) error }) (*FeatureFlagRow, error) {
	var f FeatureFlagRow
	if err := row.Scan(&f.ID, &f.Key, &f.Enabled, &f.RolloutPercent, &f.UpdatedBy,
		&f.CreatedAt, &f.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	return &f, nil
}

// GetFeatureFlag 获取功能开关；不存在返回 nil。
func (r *Repo) GetFeatureFlag(ctx context.Context, key string) (*FeatureFlagRow, error) {
	return getFeatureFlagBy(ctx, r.db, key)
}

func getFeatureFlagBy(ctx context.Context, q queryer, key string) (*FeatureFlagRow, error) {
	row := q.QueryRowContext(ctx, `
		SELECT `+featureFlagCols+` FROM feature_flags WHERE key = ?`, key)
	return scanFeatureFlag(row)
}

// UpsertFeatureFlag 写入或更新功能开关（无唯一约束：先更新，0 行受影响则插入）。
func (r *Repo) UpsertFeatureFlag(ctx context.Context, q queryer, f *FeatureFlagRow) (*FeatureFlagRow, error) {
	res, err := q.ExecContext(ctx, `
		UPDATE feature_flags SET enabled = ?, rollout_percent = ?, updated_by = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE key = ?`,
		f.Enabled, f.RolloutPercent, f.UpdatedBy, f.Key)
	if err != nil {
		return nil, normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		if _, err := q.ExecContext(ctx, `
			INSERT INTO feature_flags (id, key, enabled, rollout_percent, updated_by)
			VALUES (?, ?, ?, ?, ?)`,
			f.ID, f.Key, f.Enabled, f.RolloutPercent, f.UpdatedBy); err != nil {
			return nil, normalizeErr(err)
		}
	}
	return getFeatureFlagBy(ctx, q, f.Key)
}

// SetUserDisabled 设置用户禁用时间（NULL 由 GetUserDisabledAt 表达未禁用）。
func (r *Repo) SetUserDisabled(ctx context.Context, q queryer, userID, disabledAt string) error {
	res, err := q.ExecContext(ctx, `
		UPDATE users SET disabled_at = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), version = version + 1
		WHERE id = ? AND deleted_at IS NULL`, disabledAt, userID)
	if err != nil {
		return normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return NotFoundErr("用户", userID)
	}
	return nil
}

// GetUserDisabledAt 返回用户禁用时间；不存在或未禁用返回 nil,nil（视为活跃）。
func (r *Repo) GetUserDisabledAt(ctx context.Context, userID string) (*string, error) {
	var disabledAt *string
	err := r.db.QueryRowContext(ctx, `
		SELECT disabled_at FROM users WHERE id = ? AND deleted_at IS NULL`, userID).Scan(&disabledAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, normalizeErr(err)
	}
	return disabledAt, nil
}

// AuditEntryRow 是审计事件行（含管理端快照列）。
type AuditEntryRow struct {
	ID          string
	WorkspaceID string
	ActorID     *string
	ActorRole   *string
	Action      string
	EntityType  string
	EntityID    *string
	Payload     json.RawMessage
	BeforeJSON  json.RawMessage
	AfterJSON   json.RawMessage
	CreatedAt   string
}

const auditCols = `id, workspace_id, actor_id, actor_role, action, entity_type, entity_id,
	payload_json, before_json, after_json, created_at`

func scanAuditEntry(row interface{ Scan(...any) error }) (*AuditEntryRow, error) {
	var e AuditEntryRow
	var payload, before, after string
	if err := row.Scan(&e.ID, &e.WorkspaceID, &e.ActorID, &e.ActorRole, &e.Action, &e.EntityType,
		&e.EntityID, &payload, &before, &after, &e.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	e.Payload = json.RawMessage(payload)
	e.BeforeJSON = json.RawMessage(before)
	e.AfterJSON = json.RawMessage(after)
	return &e, nil
}

// ListAuditEvents 列出审计事件（按 action / actor_id 过滤；倒序）。
func (r *Repo) ListAuditEvents(ctx context.Context, wsID, action, actorID string) ([]*AuditEntryRow, error) {
	conds := []string{"workspace_id = ?"}
	args := []any{wsID}
	if action != "" {
		conds = append(conds, "action = ?")
		args = append(args, action)
	}
	if actorID != "" {
		conds = append(conds, "actor_id = ?")
		args = append(args, actorID)
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+auditCols+` FROM audit_events
		WHERE `+strings.Join(conds, " AND ")+`
		ORDER BY created_at DESC LIMIT 200`, args...)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*AuditEntryRow
	for rows.Next() {
		e, err := scanAuditEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AppendUsageEvent 记录一次学习/使用事件（Agent 配额计数用，event_type=agent.chat）。
func (r *Repo) AppendUsageEvent(ctx context.Context, wsID, userID, eventType string, payload any) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO usage_events (id, workspace_id, user_id, event_type, payload_json)
		VALUES (?, ?, ?, ?, ?)`, newIDLocal(), wsID, userID, eventType, MarshalJSON(payload))
	return normalizeErr(err)
}

// CountUsageEvents 统计某事件在 since（含）之后的次数（按 payload 中 provider/model 过滤）。
func (r *Repo) CountUsageEvents(ctx context.Context, wsID, eventType, provider, model, since string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM usage_events
		WHERE workspace_id = ? AND event_type = ?
		  AND occurred_at >= ?
		  AND json_extract(payload_json, '$.provider') = ?
		  AND json_extract(payload_json, '$.model') = ?`,
		wsID, eventType, since, provider, model).Scan(&n)
	if err != nil {
		return 0, normalizeErr(err)
	}
	return n, nil
}
