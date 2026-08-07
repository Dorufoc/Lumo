// admin_service.go 管理端用例：内容审核、Provider 策略、功能开关、用户禁用、审计查询。
// 所有管理写操作都带审计门禁：审计写入失败则整体回滚（事务内 appendAudit）。
package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"lumo/internal/domain"
	"lumo/internal/repository"
)

// AdminService 实现管理端用例。
type AdminService struct{ s *Services }

// ---- 内容审核 ----

// AdminReviewListReq 审核队列请求。
type AdminReviewListReq struct {
	WorkspaceID string `json:"workspace_id"`
	Status      string `json:"status"` // 空=全部；pending|approved|rejected|taken_down
}

// ReviewQueueItem 是审核条目 DTO。
type ReviewQueueItem struct {
	ID         string  `json:"id"`
	RefType    string  `json:"ref_type"`
	RefID      string  `json:"ref_id"`
	Status     string  `json:"status"`
	Reason     string  `json:"reason"`
	ReviewedAt *string `json:"reviewed_at"`
}

// ReviewQueuePage 是审核队列页。
type ReviewQueuePage struct {
	Total int                `json:"total"`
	Items []*ReviewQueueItem `json:"items"`
}

// AdminReviewList 列出审核队列。
func (a *AdminService) AdminReviewList(ctx context.Context, req AdminReviewListReq) (*ReviewQueuePage, error) {
	if err := a.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.Status != "" && req.Status != "pending" && req.Status != "approved" &&
		req.Status != "rejected" && req.Status != "taken_down" {
		return nil, domain.InvalidArg("status 仅允许 pending/approved/rejected/taken_down")
	}
	rows, err := a.s.Repo.ListReviewQueueItems(ctx, req.Status)
	if err != nil {
		return nil, err
	}
	items := make([]*ReviewQueueItem, 0, len(rows))
	for _, r := range rows {
		items = append(items, reviewItemFromRow(r))
	}
	return &ReviewQueuePage{Total: len(items), Items: items}, nil
}

// AdminReviewDecideReq 审核决策请求。
type AdminReviewDecideReq struct {
	WorkspaceID string `json:"workspace_id"`
	ItemID      string `json:"item_id"`
	Decision    string `json:"decision"` // approved | rejected | taken_down
	Reason      string `json:"reason"`
}

// AdminReviewDecide 决策审核条目（仅 pending 可迁移；写审计快照）。
func (a *AdminService) AdminReviewDecide(ctx context.Context, req AdminReviewDecideReq) (*ReviewQueueItem, error) {
	if err := a.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.Decision != "approved" && req.Decision != "rejected" && req.Decision != "taken_down" {
		return nil, domain.InvalidArg("decision 仅允许 approved/rejected/taken_down")
	}
	before, err := a.s.Repo.GetReviewQueueItem(ctx, req.ItemID)
	if err != nil {
		return nil, err
	}
	if before == nil {
		return nil, domain.NotFound("审核条目不存在")
	}
	beforeJSON := mustJSON(map[string]any{
		"id": before.ID, "ref_type": before.RefType, "ref_id": before.RefID,
		"status": before.Status, "reason": before.Reason,
	})
	var result *ReviewQueueItem
	err = a.s.Repo.WithTx(ctx, func(tx *sql.Tx) error {
		updated, err := a.s.Repo.DecideReviewQueueItem(ctx, tx, req.ItemID, req.Decision, req.Reason)
		if err != nil {
			return err
		}
		result = reviewItemFromRow(updated)
		afterJSON := mustJSON(map[string]any{
			"id": updated.ID, "ref_type": updated.RefType, "ref_id": updated.RefID,
			"status": updated.Status, "reason": updated.Reason, "reviewed_at": updated.ReviewedAt,
		})
		return a.s.Repo.AppendAuditTx(ctx, tx, &repository.AuditEvent{
			ID: NewID(), WorkspaceID: req.WorkspaceID,
			ActorRole:  strPtr("admin"),
			Action:     "admin.review.decide",
			EntityType: "review_queue_item", EntityID: &req.ItemID,
			Payload:    mustJSON(map[string]any{"decision": req.Decision, "reason": req.Reason}),
			BeforeJSON: beforeJSON, AfterJSON: afterJSON,
		})
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ---- Provider 策略 ----

// AdminProviderPolicySetReq 设置 Provider 策略请求。
type AdminProviderPolicySetReq struct {
	WorkspaceID   string `json:"workspace_id"`
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	Allowed       bool   `json:"allowed"`
	DailyQuota    *int   `json:"daily_quota"`
	MonthlyBudget *int   `json:"monthly_budget"`
}

// ProviderPolicy 是策略 DTO。
type ProviderPolicy struct {
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	Allowed       bool   `json:"allowed"`
	DailyQuota    *int   `json:"daily_quota"`
	MonthlyBudget *int   `json:"monthly_budget"`
}

// AdminProviderPolicySet 写入或更新 Provider 策略（审计门禁）。
func (a *AdminService) AdminProviderPolicySet(ctx context.Context, req AdminProviderPolicySetReq) (*ProviderPolicy, error) {
	if err := a.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.Provider == "" {
		return nil, domain.InvalidArg("provider 必填")
	}
	if req.Model == "" {
		return nil, domain.InvalidArg("model 必填")
	}
	if req.DailyQuota != nil && *req.DailyQuota < 0 {
		return nil, domain.InvalidArg("daily_quota 不能为负数")
	}
	if req.MonthlyBudget != nil && *req.MonthlyBudget < 0 {
		return nil, domain.InvalidArg("monthly_budget 不能为负数")
	}
	row := &repository.ProviderPolicyRow{
		ID: NewID(), Provider: req.Provider, Model: req.Model,
		Allowed: req.Allowed, DailyQuota: req.DailyQuota, MonthlyBudget: req.MonthlyBudget,
	}
	var result *ProviderPolicy
	err := a.s.Repo.WithTx(ctx, func(tx *sql.Tx) error {
		got, err := a.s.Repo.UpsertProviderPolicy(ctx, tx, row)
		if err != nil {
			return err
		}
		result = providerPolicyFromRow(got)
		return a.s.Repo.AppendAuditTx(ctx, tx, &repository.AuditEvent{
			ID: NewID(), WorkspaceID: req.WorkspaceID,
			ActorRole:  strPtr("admin"),
			Action:     "admin.provider_policy.set",
			EntityType: "provider_policy",
			EntityID:   strPtr(req.Provider + "/" + req.Model),
			Payload:    mustJSON(map[string]any{"allowed": req.Allowed, "daily_quota": req.DailyQuota, "monthly_budget": req.MonthlyBudget}),
		})
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ---- 功能开关 ----

// AdminFeatureFlagSetReq 设置功能开关请求。
type AdminFeatureFlagSetReq struct {
	WorkspaceID    string `json:"workspace_id"`
	Key            string `json:"key"`
	Enabled        bool   `json:"enabled"`
	RolloutPercent int    `json:"rollout_percent"`
}

// FeatureFlag 是功能开关 DTO。
type FeatureFlag struct {
	Key            string `json:"key"`
	Enabled        bool   `json:"enabled"`
	RolloutPercent int    `json:"rollout_percent"`
}

// AdminFeatureFlagSet 写入或更新功能开关（审计门禁）。
func (a *AdminService) AdminFeatureFlagSet(ctx context.Context, req AdminFeatureFlagSetReq) (*FeatureFlag, error) {
	if err := a.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.Key == "" {
		return nil, domain.InvalidArg("key 必填")
	}
	if req.RolloutPercent < 0 || req.RolloutPercent > 100 {
		return nil, domain.InvalidArg("rollout_percent 须在 0-100")
	}
	row := &repository.FeatureFlagRow{
		ID: NewID(), Key: req.Key, Enabled: req.Enabled, RolloutPercent: req.RolloutPercent,
	}
	var result *FeatureFlag
	err := a.s.Repo.WithTx(ctx, func(tx *sql.Tx) error {
		got, err := a.s.Repo.UpsertFeatureFlag(ctx, tx, row)
		if err != nil {
			return err
		}
		result = &FeatureFlag{Key: got.Key, Enabled: got.Enabled, RolloutPercent: got.RolloutPercent}
		return a.s.Repo.AppendAuditTx(ctx, tx, &repository.AuditEvent{
			ID: NewID(), WorkspaceID: req.WorkspaceID,
			ActorRole:  strPtr("admin"),
			Action:     "admin.feature_flag.set",
			EntityType: "feature_flag", EntityID: &req.Key,
			Payload: mustJSON(map[string]any{"enabled": req.Enabled, "rollout_percent": req.RolloutPercent}),
		})
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ---- 用户禁用 ----

// AdminUserDisableReq 禁用用户请求。
type AdminUserDisableReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	Reason      string `json:"reason"`
}

// UserStatus 是用户状态 DTO。
type UserStatus struct {
	Disabled   bool    `json:"disabled"`
	DisabledAt *string `json:"disabled_at"`
}

// AdminUserDisable 禁用用户（写 disabled_at；审计门禁）。
func (a *AdminService) AdminUserDisable(ctx context.Context, req AdminUserDisableReq) (*UserStatus, error) {
	if err := a.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	u, err := a.s.Repo.GetUser(ctx, req.WorkspaceID, req.UserID)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, domain.NotFound("用户不存在")
	}
	disabledAt := Now()
	err = a.s.Repo.WithTx(ctx, func(tx *sql.Tx) error {
		if err := a.s.Repo.SetUserDisabled(ctx, tx, req.UserID, disabledAt); err != nil {
			return err
		}
		return a.s.Repo.AppendAuditTx(ctx, tx, &repository.AuditEvent{
			ID: NewID(), WorkspaceID: req.WorkspaceID,
			ActorID: &req.UserID, ActorRole: strPtr("admin"),
			Action:     "admin.user.disable",
			EntityType: "user", EntityID: &req.UserID,
			Payload:    mustJSON(map[string]any{"reason": req.Reason}),
			BeforeJSON: mustJSON(map[string]any{"disabled": false}),
			AfterJSON:  mustJSON(map[string]any{"disabled": true, "disabled_at": disabledAt, "reason": req.Reason}),
		})
	})
	if err != nil {
		return nil, err
	}
	return &UserStatus{Disabled: true, DisabledAt: &disabledAt}, nil
}

// ---- 审计查询 ----

// AdminAuditListReq 审计列表请求。
type AdminAuditListReq struct {
	WorkspaceID string `json:"workspace_id"`
	ActorID     string `json:"actor_id"`
	Action      string `json:"action"`
}

// AuditEntry 是审计条目 DTO。
type AuditEntry struct {
	ID         string          `json:"id"`
	ActorID    *string         `json:"actor_id"`
	ActorRole  *string         `json:"actor_role"`
	Action     string          `json:"action"`
	EntityType string          `json:"entity_type"`
	EntityID   *string         `json:"entity_id"`
	Payload    json.RawMessage `json:"payload"`
	BeforeJSON string          `json:"before_json"`
	AfterJSON  string          `json:"after_json"`
	CreatedAt  string          `json:"created_at"`
}

// AuditPage 是审计页。
type AuditPage struct {
	Total int           `json:"total"`
	Items []*AuditEntry `json:"items"`
}

// AdminAuditList 列出审计事件（action / actor_id 过滤）。
func (a *AdminService) AdminAuditList(ctx context.Context, req AdminAuditListReq) (*AuditPage, error) {
	if err := a.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	rows, err := a.s.Repo.ListAuditEvents(ctx, req.WorkspaceID, req.Action, req.ActorID)
	if err != nil {
		return nil, err
	}
	items := make([]*AuditEntry, 0, len(rows))
	for _, e := range rows {
		items = append(items, auditEntryFromRow(e))
	}
	return &AuditPage{Total: len(items), Items: items}, nil
}

// ---- 映射 ----

func reviewItemFromRow(r *repository.ReviewQueueItemRow) *ReviewQueueItem {
	return &ReviewQueueItem{
		ID: r.ID, RefType: r.RefType, RefID: r.RefID,
		Status: r.Status, Reason: r.Reason, ReviewedAt: r.ReviewedAt,
	}
}

func providerPolicyFromRow(r *repository.ProviderPolicyRow) *ProviderPolicy {
	return &ProviderPolicy{
		Provider: r.Provider, Model: r.Model, Allowed: r.Allowed,
		DailyQuota: r.DailyQuota, MonthlyBudget: r.MonthlyBudget,
	}
}

func auditEntryFromRow(e *repository.AuditEntryRow) *AuditEntry {
	return &AuditEntry{
		ID: e.ID, ActorID: e.ActorID, ActorRole: e.ActorRole,
		Action: e.Action, EntityType: e.EntityType, EntityID: e.EntityID,
		Payload: e.Payload, BeforeJSON: string(e.BeforeJSON), AfterJSON: string(e.AfterJSON),
		CreatedAt: e.CreatedAt,
	}
}

// startOfDayUTC 返回 UTC 当日零点（RFC3339，用于用量计数）。
func startOfDayUTC(now time.Time) string {
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
}

// startOfMonthUTC 返回 UTC 当月 1 日零点（RFC3339，用于月度预算计数）。
func startOfMonthUTC(now time.Time) string {
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
}
