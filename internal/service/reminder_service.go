package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"lumo/internal/agent"
	"lumo/internal/domain"
	"lumo/internal/repository"
)

// ReminderService 实现提醒与通知用例（API 设计文档 7.7 / 完整设计文档 4.14）。
// Now 可注入时钟：测试中推进时间触发调度判定；RunOnce/RunScheduler 供调度器使用。
type ReminderService struct {
	s   *Services
	Now func() time.Time
}

// Reminder 是提醒 DTO（reminders 表行）。
type Reminder struct {
	ID            string `json:"id"`
	WorkspaceID   string `json:"workspace_id"`
	UserID        string `json:"user_id"`
	Kind          string `json:"kind"`
	RuleJSON      string `json:"rule_json"`
	Enabled       bool   `json:"enabled"`
	NextTriggerAt string `json:"next_trigger_at"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

// TestResult 是 ReminderTestSend 返回（确定性测试钩子，不依赖真实时钟）。
type TestResult struct {
	OK   bool   `json:"ok"`
	Kind string `json:"kind"`
}

// Notification 是通知 DTO（notifications 表行，body_args 已解析为 map）。
type Notification struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind"`
	TitleKey  string         `json:"title_key"`
	BodyArgs  map[string]any `json:"body_args"`
	RefType   *string        `json:"ref_type"`
	RefID     *string        `json:"ref_id"`
	ReadAt    *string        `json:"read_at"`
	CreatedAt string         `json:"created_at"`
}

// NotificationPage 是通知列表分页响应。
type NotificationPage struct {
	Items      []*Notification `json:"items"`
	NextCursor string          `json:"next_cursor"`
	HasMore    bool            `json:"has_more"`
}

// MarkReadResult 是 NotificationMarkRead 返回（API 文档 7.7 BatchResult 契约：{updated}）。
// 命名避免与 flashcard 模块的 BatchResult（success_count/error_count）符号冲突；
// JSON 字段 updated 即传输契约。
type MarkReadResult struct {
	Updated int `json:"updated"`
}

// ReminderUpsertReq 提醒新增/更新请求。
type ReminderUpsertReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	Kind        string `json:"kind"`
	RuleJSON    string `json:"rule_json"`
	Enabled     bool   `json:"enabled"`
}

// ReminderTestSendReq 测试发送请求。
type ReminderTestSendReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	Kind        string `json:"kind"`
}

// NotificationListReq 通知列表请求。
type NotificationListReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	UnreadOnly  bool   `json:"unread_only"`
	Cursor      string `json:"cursor"`
	Limit       int    `json:"limit"`
}

// NotificationMarkReadReq 通知标记已读请求。
type NotificationMarkReadReq struct {
	WorkspaceID string   `json:"workspace_id"`
	UserID      string   `json:"user_id"`
	IDs         []string `json:"ids"`
}

// ReminderUpsert 新增或更新提醒（(user_id, kind) 组合唯一，upsert 语义）。
// 校验：kind 枚举 + rule_json 合法 JSON + 规则可解析；enabled 控制是否参与调度。
// 每次 upsert 都按当前规则重算初始 next_trigger_at。
// 幂等：0005 迁移 reminders 无 UNIQUE(user_id, kind) 约束，故 check-then-insert
// （SQLite 单写者下安全，同 SeedAchievementDefs 的 code 去重策略）。
func (r *ReminderService) ReminderUpsert(ctx context.Context, req ReminderUpsertReq) (*Reminder, error) {
	if err := r.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if !domain.ValidReminderKind(req.Kind) {
		return nil, domain.InvalidArg("kind 须为 review|goal|exam|streak|health")
	}
	if !json.Valid([]byte(req.RuleJSON)) {
		return nil, domain.InvalidArg("rule_json 须为合法 JSON")
	}
	rule, err := domain.ParseReminderRule(json.RawMessage(req.RuleJSON))
	if err != nil {
		return nil, err
	}
	now := r.Now()
	next := rule.NextTriggerAt(now)
	enabled := 0
	if req.Enabled {
		enabled = 1
	}

	existing, err := r.s.Repo.GetReminder(ctx, req.UserID, req.Kind)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		row := &repository.ReminderRow{
			ID: existing.ID, WorkspaceID: existing.WorkspaceID, UserID: req.UserID,
			Kind: req.Kind, RuleJSON: req.RuleJSON, Enabled: enabled,
			NextTriggerAt: next, UpdatedAt: now.UTC().Format(time.RFC3339),
		}
		if err := r.s.Repo.UpdateReminder(ctx, row); err != nil {
			return nil, err
		}
		r.s.audit(ctx, req.WorkspaceID, "reminder.upsert", "reminder", existing.ID,
			map[string]any{"kind": req.Kind, "enabled": req.Enabled, "rule_json": req.RuleJSON})
		return r.readReminder(ctx, req.UserID, req.Kind)
	}

	row := &repository.ReminderRow{
		ID: NewID(), WorkspaceID: req.WorkspaceID, UserID: req.UserID,
		Kind: req.Kind, RuleJSON: req.RuleJSON, Enabled: enabled, NextTriggerAt: next,
	}
	if err := r.s.Repo.CreateReminder(ctx, row); err != nil {
		return nil, err
	}
	fresh, err := r.s.Repo.GetReminder(ctx, req.UserID, req.Kind)
	if err != nil {
		return nil, err
	}
	if fresh == nil {
		return nil, domain.Conflict("提醒写入冲突，请重试")
	}
	r.s.audit(ctx, req.WorkspaceID, "reminder.upsert", "reminder", fresh.ID,
		map[string]any{"kind": req.Kind, "enabled": req.Enabled, "rule_json": req.RuleJSON})
	return reminderFromRow(fresh), nil
}

// ReminderTestSend 确定性测试钩子：立即发布 reminder:triggered 事件
// （持久化通知 + 广播），不依赖真实时钟，供设置页验证通知通道（设计 4.14 测试发送）。
func (r *ReminderService) ReminderTestSend(ctx context.Context, req ReminderTestSendReq) (*TestResult, error) {
	if err := r.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if !domain.ValidReminderKind(req.Kind) {
		return nil, domain.InvalidArg("kind 须为 review|goal|exam|streak|health")
	}
	if err := r.s.UserEvents.Publish(req.UserID, agent.Event{
		Name:    agent.EventReminderTriggered,
		Payload: map[string]any{"kind": req.Kind, "test": true},
	}); err != nil {
		return nil, err
	}
	r.s.audit(ctx, req.WorkspaceID, "reminder.test_send", "reminder", "",
		map[string]any{"kind": req.Kind})
	return &TestResult{OK: true, Kind: req.Kind}, nil
}

// NotificationList 分页列出用户通知（newest-first；unread_only 仅未读）。
func (r *ReminderService) NotificationList(ctx context.Context, req NotificationListReq) (*NotificationPage, error) {
	if err := r.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if req.Limit < 0 {
		return nil, domain.InvalidArg("limit 不能为负")
	}
	rows, next, hasMore, err := r.s.Repo.ListNotifications(ctx, req.UserID, req.UnreadOnly, req.Cursor, req.Limit)
	if err != nil {
		return nil, err
	}
	items := make([]*Notification, 0, len(rows))
	for _, row := range rows {
		items = append(items, notificationFromRow(row))
	}
	return &NotificationPage{Items: items, NextCursor: next, HasMore: hasMore}, nil
}

// NotificationMarkRead 批量标记已读（仅标记属于该用户的通知），返回实际更新行数。
func (r *ReminderService) NotificationMarkRead(ctx context.Context, req NotificationMarkReadReq) (*MarkReadResult, error) {
	if err := r.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if len(req.IDs) == 0 {
		return nil, domain.InvalidArg("ids 不能为空")
	}
	n, err := r.s.Repo.MarkNotificationsRead(ctx, req.UserID, req.IDs)
	if err != nil {
		return nil, err
	}
	r.s.audit(ctx, req.WorkspaceID, "notification.mark_read", "notification", "",
		map[string]any{"count": n})
	return &MarkReadResult{Updated: n}, nil
}

// RunOnce 执行一次到期扫描（调度核心，幂等）。
// 每条到期提醒（enabled=1 AND next_trigger_at<=now）：
//  1. 解析规则，计算新 next_trigger_at（repeat=true 前移；repeat=false 一次性 → enabled=0）；
//  2. 原子抢占 ClaimReminder（WHERE id=? AND enabled=1 AND next_trigger_at<=?，RowsAffected>0 才算抢占成功）；
//  3. 仅抢占成功者发布 reminder:triggered 事件（持久化通知 + 广播）。
//
// claim-before-fire：事件发布前先推进 next_trigger_at/关闭 enabled，
// 因此重复扫描 / 并发扫描不会重复触发同一提醒（调度幂等，验收 4.14）。
func (r *ReminderService) RunOnce(ctx context.Context) error {
	now := r.Now()
	nowStr := now.UTC().Format(time.RFC3339)
	due, err := r.s.Repo.ListDueReminders(ctx, nowStr)
	if err != nil {
		return err
	}
	for _, rem := range due {
		rule, err := domain.ParseReminderRule(json.RawMessage(rem.RuleJSON))
		if err != nil {
			// 非法规则：跳过该提醒，不阻断调度
			continue
		}
		newNext := rule.NextTriggerAt(now)
		newEnabled := 1
		if !rule.Repeat {
			newEnabled = 0
		}
		claimed, err := r.s.Repo.ClaimReminder(ctx, rem.ID, nowStr, newNext, newEnabled)
		if err != nil {
			return err
		}
		if !claimed {
			// 已被并发抢占或已不满足到期条件：跳过，避免重复触发
			continue
		}
		if err := r.s.UserEvents.Publish(rem.UserID, agent.Event{
			Name: agent.EventReminderTriggered,
			Payload: map[string]any{
				"kind": rem.Kind, "ref_type": "reminder", "ref_id": rem.ID,
			},
		}); err != nil {
			return err
		}
		r.s.audit(ctx, rem.WorkspaceID, "reminder.fire", "reminder", rem.ID,
			map[string]any{"kind": rem.Kind, "enabled": newEnabled, "next_trigger_at": newNext})
	}
	return nil
}

// RunScheduler 提醒调度循环：每 30 秒执行一次 RunOnce，直到 ctx 取消。
// 由 cmd/app/main.go 以进程级 context 启动（in-process goroutine）。
func (r *ReminderService) RunScheduler(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	slog.Info("提醒调度器已启动", "interval", "30s")
	for {
		select {
		case <-ctx.Done():
			slog.Info("提醒调度器已停止")
			return
		case <-ticker.C:
			if err := r.RunOnce(ctx); err != nil {
				slog.Error("提醒调度扫描失败", "error", err)
			}
		}
	}
}

// readReminder 按 (user_id, kind) 读回提醒（upsert 更新后返回新状态）。
func (r *ReminderService) readReminder(ctx context.Context, userID, kind string) (*Reminder, error) {
	row, err := r.s.Repo.GetReminder(ctx, userID, kind)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, domain.Conflict("提醒写入冲突，请重试")
	}
	return reminderFromRow(row), nil
}

func reminderFromRow(row *repository.ReminderRow) *Reminder {
	return &Reminder{
		ID: row.ID, WorkspaceID: row.WorkspaceID, UserID: row.UserID,
		Kind: row.Kind, RuleJSON: row.RuleJSON, Enabled: row.Enabled == 1,
		NextTriggerAt: row.NextTriggerAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func notificationFromRow(row *repository.NotificationRow) *Notification {
	out := &Notification{
		ID: row.ID, Kind: row.Kind, TitleKey: row.TitleKey,
		RefType: row.RefType, RefID: row.RefID, ReadAt: row.ReadAt, CreatedAt: row.CreatedAt,
		BodyArgs: map[string]any{},
	}
	if row.BodyArgsJSON != "" {
		if err := json.Unmarshal([]byte(row.BodyArgsJSON), &out.BodyArgs); err != nil {
			out.BodyArgs = map[string]any{}
		}
	}
	return out
}
