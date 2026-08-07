package service

import (
	"context"
	"strings"
	"testing"

	"lumo/internal/agent"
	"lumo/internal/domain"
)

// seedReviewItem 直接插入一条审核队列记录（表无 workspace_id，全局队列）。
func seedReviewItem(t *testing.T, s *Services, refType, refID, status string) string {
	t.Helper()
	id := NewID()
	_, err := s.Repo.DB().ExecContext(context.Background(), `
		INSERT INTO review_queue_items (id, ref_type, ref_id, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ','now'), strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
		id, refType, refID, status)
	if err != nil {
		t.Fatalf("seed review item: %v", err)
	}
	return id
}

func TestAdminReviewListAndDecide(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)

	pendingID := seedReviewItem(t, s, "question", "q1", "pending")
	seedReviewItem(t, s, "question", "q2", "approved")

	// 按状态过滤
	page, err := s.Admin.AdminReviewList(ctx, AdminReviewListReq{WorkspaceID: ws.ID, Status: "pending"})
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != pendingID {
		t.Fatalf("unexpected pending page: total=%d items=%d", page.Total, len(page.Items))
	}
	if page.Items[0].Status != "pending" || page.Items[0].RefType != "question" {
		t.Fatalf("unexpected item: %+v", page.Items[0])
	}

	// 无过滤列出全部
	all, err := s.Admin.AdminReviewList(ctx, AdminReviewListReq{WorkspaceID: ws.ID})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if all.Total != 2 {
		t.Fatalf("expected 2 items, got %d", all.Total)
	}

	// Decide pending → approved
	decided, err := s.Admin.AdminReviewDecide(ctx, AdminReviewDecideReq{
		WorkspaceID: ws.ID, ItemID: pendingID, Decision: "approved", Reason: "内容合规",
	})
	if err != nil {
		t.Fatalf("decide approved: %v", err)
	}
	if decided.Status != "approved" || decided.Reason != "内容合规" || decided.ReviewedAt == nil {
		t.Fatalf("unexpected decided item: %+v", decided)
	}

	// 已决条目再决定 → INVALID_STATE
	if _, err := s.Admin.AdminReviewDecide(ctx, AdminReviewDecideReq{
		WorkspaceID: ws.ID, ItemID: pendingID, Decision: "rejected",
	}); err == nil {
		t.Fatal("expected INVALID_STATE on re-decide")
	} else if domain.AsError(err).Code != domain.CodeInvalidState {
		t.Fatalf("expected INVALID_STATE, got %s", domain.AsError(err).Code)
	}

	// 不存在的条目 → NOT_FOUND
	if _, err := s.Admin.AdminReviewDecide(ctx, AdminReviewDecideReq{
		WorkspaceID: ws.ID, ItemID: "no-such-item", Decision: "approved",
	}); err == nil {
		t.Fatal("expected NOT_FOUND")
	} else if domain.AsError(err).Code != domain.CodeNotFound {
		t.Fatalf("expected NOT_FOUND, got %s", domain.AsError(err).Code)
	}

	// 非法 decision → INVALID_ARGUMENT
	if _, err := s.Admin.AdminReviewDecide(ctx, AdminReviewDecideReq{
		WorkspaceID: ws.ID, ItemID: pendingID, Decision: "nonsense",
	}); err == nil {
		t.Fatal("expected INVALID_ARGUMENT")
	} else if domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("expected INVALID_ARGUMENT, got %s", domain.AsError(err).Code)
	}

	// Decide 写审计（actor_role=admin，含 before/after 快照）
	auditPage, err := s.Admin.AdminAuditList(ctx, AdminAuditListReq{WorkspaceID: ws.ID, Action: "admin.review.decide"})
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(auditPage.Items) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(auditPage.Items))
	}
	e := auditPage.Items[0]
	if e.ActorRole == nil || *e.ActorRole != "admin" {
		t.Fatalf("expected actor_role=admin, got %v", e.ActorRole)
	}
	if len(e.BeforeJSON) == 0 || len(e.AfterJSON) == 0 {
		t.Fatalf("expected before/after snapshots, got before=%s after=%s", e.BeforeJSON, e.AfterJSON)
	}
}

func TestAdminProviderPolicySet(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)

	dq := 10
	mb := 500
	pol, err := s.Admin.AdminProviderPolicySet(ctx, AdminProviderPolicySetReq{
		WorkspaceID: ws.ID, Provider: "llm", Model: "gpt-4o-mini",
		Allowed: false, DailyQuota: &dq, MonthlyBudget: &mb,
	})
	if err != nil {
		t.Fatalf("set policy: %v", err)
	}
	if pol.Provider != "llm" || pol.Allowed || pol.DailyQuota == nil || *pol.DailyQuota != 10 {
		t.Fatalf("unexpected policy: %+v", pol)
	}
	if pol.MonthlyBudget == nil || *pol.MonthlyBudget != 500 {
		t.Fatalf("unexpected monthly budget: %+v", pol.MonthlyBudget)
	}

	// 再次设置 = 更新（不产生重复行）
	allowed := true
	dq2 := 20
	pol2, err := s.Admin.AdminProviderPolicySet(ctx, AdminProviderPolicySetReq{
		WorkspaceID: ws.ID, Provider: "llm", Model: "gpt-4o-mini",
		Allowed: true, DailyQuota: &dq2,
	})
	if err != nil {
		t.Fatalf("update policy: %v", err)
	}
	if !pol2.Allowed || *pol2.DailyQuota != 20 {
		t.Fatalf("unexpected updated policy: %+v", pol2)
	}
	_ = allowed

	// 非法配额 → INVALID_ARGUMENT
	neg := -1
	if _, err := s.Admin.AdminProviderPolicySet(ctx, AdminProviderPolicySetReq{
		WorkspaceID: ws.ID, Provider: "llm", Model: "x", DailyQuota: &neg,
	}); err == nil {
		t.Fatal("expected INVALID_ARGUMENT for negative quota")
	} else if domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("expected INVALID_ARGUMENT, got %s", domain.AsError(err).Code)
	}
}

func TestAdminFeatureFlagSet(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)

	flag, err := s.Admin.AdminFeatureFlagSet(ctx, AdminFeatureFlagSetReq{
		WorkspaceID: ws.ID, Key: "ai_tutor", Enabled: true, RolloutPercent: 50,
	})
	if err != nil {
		t.Fatalf("set flag: %v", err)
	}
	if !flag.Enabled || flag.RolloutPercent != 50 || flag.Key != "ai_tutor" {
		t.Fatalf("unexpected flag: %+v", flag)
	}

	flag2, err := s.Admin.AdminFeatureFlagSet(ctx, AdminFeatureFlagSetReq{
		WorkspaceID: ws.ID, Key: "ai_tutor", Enabled: false, RolloutPercent: 0,
	})
	if err != nil {
		t.Fatalf("update flag: %v", err)
	}
	if flag2.Enabled {
		t.Fatalf("expected flag disabled: %+v", flag2)
	}
	if flag2.RolloutPercent != 0 {
		t.Fatalf("expected rollout 0: %+v", flag2)
	}

	// 非法 rollout → INVALID_ARGUMENT
	if _, err := s.Admin.AdminFeatureFlagSet(ctx, AdminFeatureFlagSetReq{
		WorkspaceID: ws.ID, Key: "x", Enabled: true, RolloutPercent: 101,
	}); err == nil {
		t.Fatal("expected INVALID_ARGUMENT for rollout > 100")
	}
}

func TestAdminUserDisableBlocksCommands(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)

	// 禁用前：GoalCreate 正常
	goal, err := s.Goal.GoalCreate(ctx, GoalCreateReq{
		WorkspaceID: ws.ID, UserID: userID, Name: "目标", DailyMinutes: 30,
	})
	if err != nil {
		t.Fatalf("create goal before disable: %v", err)
	}

	// 禁用用户
	st, err := s.Admin.AdminUserDisable(ctx, AdminUserDisableReq{WorkspaceID: ws.ID, UserID: userID, Reason: "违规"})
	if err != nil {
		t.Fatalf("disable user: %v", err)
	}
	if !st.Disabled || st.DisabledAt == nil {
		t.Fatalf("unexpected status: %+v", st)
	}

	// 命令型方法（user_id 直传）→ UNAUTHORIZED
	if _, err := s.Goal.GoalCreate(ctx, GoalCreateReq{
		WorkspaceID: ws.ID, UserID: userID, Name: "目标2", DailyMinutes: 30,
	}); err == nil {
		t.Fatal("expected UNAUTHORIZED on GoalCreate after disable")
	} else if domain.AsError(err).Code != domain.CodeUnauthorized {
		t.Fatalf("expected UNAUTHORIZED, got %s", domain.AsError(err).Code)
	}

	// 命令型方法（由实体归属推导 user_id）→ UNAUTHORIZED
	name := "改名"
	if _, err := s.Goal.GoalUpdate(ctx, GoalUpdateReq{
		WorkspaceID: ws.ID, GoalID: goal.ID, Version: 1, Name: &name,
	}); err == nil {
		t.Fatal("expected UNAUTHORIZED on GoalUpdate (derived) after disable")
	} else if domain.AsError(err).Code != domain.CodeUnauthorized {
		t.Fatalf("expected UNAUTHORIZED, got %s", domain.AsError(err).Code)
	}

	// PracticeStart（user_id 直传）→ UNAUTHORIZED
	if _, err := s.Practice.PracticeStart(ctx, PracticeStartReq{
		WorkspaceID: ws.ID, UserID: userID, Mode: "practice",
		QuestionIDs: []string{"q1"}, IdempotencyKey: "idem-" + NewID(),
	}); err == nil {
		t.Fatal("expected UNAUTHORIZED on PracticeStart after disable")
	} else if domain.AsError(err).Code != domain.CodeUnauthorized {
		t.Fatalf("expected UNAUTHORIZED, got %s", domain.AsError(err).Code)
	}

	// 只读方法豁免：GoalList 仍可读
	if _, err := s.Goal.GoalList(ctx, GoalListReq{WorkspaceID: ws.ID, UserID: userID}); err != nil {
		t.Fatalf("read-only GoalList should still work: %v", err)
	}
}

func TestAdminAuditListFilters(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)

	s.audit(ctx, ws.ID, "goal.create", "learning_goal", NewID(), map[string]any{"name": "a"})
	s.audit(ctx, ws.ID, "practice.start", "practice_session", NewID(), map[string]any{"mode": "practice"})

	// 按 action 过滤
	page, err := s.Admin.AdminAuditList(ctx, AdminAuditListReq{WorkspaceID: ws.ID, Action: "goal.create"})
	if err != nil {
		t.Fatalf("list audit by action: %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Action != "goal.create" {
		t.Fatalf("unexpected audit page: total=%d items=%d", page.Total, len(page.Items))
	}

	// 按 actor_id 过滤（既有普通审计无 actor → 命中 0）
	page2, err := s.Admin.AdminAuditList(ctx, AdminAuditListReq{WorkspaceID: ws.ID, ActorID: userID})
	if err != nil {
		t.Fatalf("list audit by actor: %v", err)
	}
	if page2.Total != 0 || len(page2.Items) != 0 {
		t.Fatalf("expected 0 entries for actor, got %d", page2.Total)
	}
}

func TestAdminAuditGateRejectsWriteWhenAuditFails(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)

	// 破坏 audit_events 表：后续任何管理写操作的审计写入必然失败
	if _, err := s.Repo.DB().ExecContext(ctx, `DROP TABLE audit_events`); err != nil {
		t.Fatalf("drop audit_events: %v", err)
	}

	// AdminUserDisable → 审计失败 → 写操作被拒
	if _, err := s.Admin.AdminUserDisable(ctx, AdminUserDisableReq{WorkspaceID: ws.ID, UserID: userID, Reason: "x"}); err == nil {
		t.Fatal("expected error when audit write fails (gate)")
	}

	// 且用户未被禁用（写操作未落库）
	disabledAt, err := s.Repo.GetUserDisabledAt(ctx, userID)
	if err != nil {
		t.Fatalf("get disabled_at: %v", err)
	}
	if disabledAt != nil {
		t.Fatalf("expected user not disabled, got %v", *disabledAt)
	}

	// AdminProviderPolicySet → 同样被拒
	if _, err := s.Admin.AdminProviderPolicySet(ctx, AdminProviderPolicySetReq{
		WorkspaceID: ws.ID, Provider: "llm", Model: "m",
	}); err == nil {
		t.Fatal("expected error when audit write fails (gate)")
	}
}

func TestAgentProviderPolicyEnforcement(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)

	sess, err := s.Agent.AgentChatCreate(ctx, agent.AgentChatCreateReq{
		WorkspaceID: ws.ID, UserID: userID, Agent: "tutor",
	})
	if err != nil {
		t.Fatalf("create agent session: %v", err)
	}

	// 策略：禁止 → FEATURE_DISABLED
	if _, err := s.Admin.AdminProviderPolicySet(ctx, AdminProviderPolicySetReq{
		WorkspaceID: ws.ID, Provider: "llm", Model: "mock",
		Allowed: false,
	}); err != nil {
		t.Fatalf("set blocked policy: %v", err)
	}
	if _, err := s.Agent.AgentChatSend(ctx, agent.AgentChatSendReq{
		WorkspaceID: ws.ID, SessionID: sess.ID, Message: "你好",
	}); err == nil {
		t.Fatal("expected FEATURE_DISABLED when provider blocked")
	} else if domain.AsError(err).Code != domain.CodeFeatureDisabled {
		t.Fatalf("expected FEATURE_DISABLED, got %s", domain.AsError(err).Code)
	}

	// 策略：允许但 daily_quota=1 → 第一次成功，第二次 QUOTA_EXCEEDED
	dq := 1
	if _, err := s.Admin.AdminProviderPolicySet(ctx, AdminProviderPolicySetReq{
		WorkspaceID: ws.ID, Provider: "llm", Model: "mock",
		Allowed: true, DailyQuota: &dq,
	}); err != nil {
		t.Fatalf("set quota policy: %v", err)
	}
	if _, err := s.Agent.AgentChatSend(ctx, agent.AgentChatSendReq{
		WorkspaceID: ws.ID, SessionID: sess.ID, Message: "问题一",
	}); err != nil {
		t.Fatalf("first chat under quota should succeed: %v", err)
	}
	if _, err := s.Agent.AgentChatSend(ctx, agent.AgentChatSendReq{
		WorkspaceID: ws.ID, SessionID: sess.ID, Message: "问题二",
	}); err == nil {
		t.Fatal("expected QUOTA_EXCEEDED on second chat")
	} else if domain.AsError(err).Code != domain.CodeQuotaExceeded {
		t.Fatalf("expected QUOTA_EXCEEDED, got %s (msg=%s)", domain.AsError(err).Code, domain.AsError(err).Message)
	}
}

// TestAdminUserDisableUnknownUser 禁用不存在的用户 → NOT_FOUND。
func TestAdminUserDisableUnknownUser(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)

	if _, err := s.Admin.AdminUserDisable(ctx, AdminUserDisableReq{
		WorkspaceID: ws.ID, UserID: "no-such-user", Reason: "x",
	}); err == nil {
		t.Fatal("expected NOT_FOUND for unknown user")
	} else if domain.AsError(err).Code != domain.CodeNotFound {
		t.Fatalf("expected NOT_FOUND, got %s", domain.AsError(err).Code)
	}
}

// TestAssertUserActiveEmptySkips 空 user_id（未携带）时 assertUserActive 跳过校验。
func TestAssertUserActiveEmptySkips(t *testing.T) {
	s, _ := newTestServices(t)
	if err := s.assertUserActive(context.Background(), ""); err != nil {
		t.Fatalf("empty user_id should skip check: %v", err)
	}
	if err := s.assertUserActive(context.Background(), "no-such-user"); err != nil {
		t.Fatalf("unknown user should not block (not found treated as active): %v", err)
	}
}

// TestProviderPolicySetValidation 空 provider/model → INVALID_ARGUMENT。
func TestProviderPolicySetValidation(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)

	if _, err := s.Admin.AdminProviderPolicySet(ctx, AdminProviderPolicySetReq{
		WorkspaceID: ws.ID, Model: "m",
	}); err == nil {
		t.Fatal("expected INVALID_ARGUMENT for empty provider")
	} else if !strings.Contains(domain.AsError(err).Message, "provider") {
		t.Fatalf("unexpected message: %s", domain.AsError(err).Message)
	}
}
