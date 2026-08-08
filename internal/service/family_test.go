package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"lumo/internal/agent"
	"lumo/internal/domain"
	"lumo/internal/repository"
)

// createParent 直接经仓储创建家长角色用户（UserCreate 不开放 parent 角色，须绕过）。
func createParent(t *testing.T, s *Services, wsID, displayName string) string {
	t.Helper()
	u := &repository.UserRow{
		ID: NewID(), WorkspaceID: wsID, DisplayName: displayName,
		Role: "parent", Preferences: json.RawMessage("{}"),
	}
	if err := s.Repo.CreateUser(context.Background(), u); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	return u.ID
}

// bindParent 完整走一遍邀请→绑定流程，返回 binding。
func bindParent(t *testing.T, s *Services, wsID, studentID, parentID string) *FamilyBinding {
	t.Helper()
	inv, err := s.Family.FamilyInviteCreate(context.Background(), FamilyInviteCreateReq{
		WorkspaceID: wsID, UserID: studentID, IdempotencyKey: "inv-" + NewID(),
	})
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}
	b, err := s.Family.FamilyBind(context.Background(), FamilyBindReq{
		WorkspaceID: wsID, UserID: parentID, InviteCode: inv.Code,
	})
	if err != nil {
		t.Fatalf("bind parent: %v", err)
	}
	return b
}

func TestFamilyInviteCreateAndGet(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, studentID := createWorkspace(t, s)

	inv, err := s.Family.FamilyInviteCreate(ctx, FamilyInviteCreateReq{
		WorkspaceID: ws.ID, UserID: studentID, IdempotencyKey: "inv-create-0001",
	})
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}
	if len(inv.Code) != 8 || inv.Status != "pending" {
		t.Fatalf("unexpected invite: %+v", inv)
	}
	exp, err := time.Parse(time.RFC3339, inv.ExpiresAt)
	if err != nil {
		t.Fatalf("bad expires_at: %v", err)
	}
	if time.Until(exp) < 23*time.Hour || time.Until(exp) > 25*time.Hour {
		t.Fatalf("expires_at 应约 24h 后，实际 %v", time.Until(exp))
	}

	// FamilyInviteGet 概览
	ov, err := s.Family.FamilyInviteGet(ctx, FamilyInviteGetReq{WorkspaceID: ws.ID, UserID: studentID})
	if err != nil {
		t.Fatalf("invite get: %v", err)
	}
	if ov.Invite == nil || ov.Invite.Code != inv.Code {
		t.Fatalf("overview 应包含未过期邀请码，got %+v", ov.Invite)
	}
	if ov.ActiveParents != 0 || len(ov.Bindings) != 0 {
		t.Fatalf("未绑定时 active_parents 应为 0，got %+v", ov)
	}
}

func TestFamilyInviteExpired(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, studentID := createWorkspace(t, s)

	inv, err := s.Family.FamilyInviteCreate(ctx, FamilyInviteCreateReq{
		WorkspaceID: ws.ID, UserID: studentID, IdempotencyKey: "inv-expired-0001",
	})
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}
	// 把 created_at 拨回 25 小时前模拟过期。
	if _, err := s.Repo.DB().ExecContext(ctx,
		`UPDATE family_bindings SET created_at = ? WHERE id = ?`,
		time.Now().UTC().Add(-25*time.Hour).Format(time.RFC3339), inv.BindingID); err != nil {
		t.Fatalf("backdate invite: %v", err)
	}
	// 重新读取绑定行，用库中的过期 created_at 判定。
	row, err := s.Repo.GetFamilyBindingByID(ctx, inv.BindingID)
	if err != nil {
		t.Fatalf("reload binding: %v", err)
	}
	if !familyInviteExpired(row.CreatedAt) {
		t.Fatalf("familyInviteExpired 应按 created_at 判定过期")
	}
	// 概览不再展示过期码
	ov, err := s.Family.FamilyInviteGet(ctx, FamilyInviteGetReq{WorkspaceID: ws.ID, UserID: studentID})
	if err != nil {
		t.Fatalf("invite get: %v", err)
	}
	if ov.Invite != nil {
		t.Fatalf("过期邀请码不应展示，got %+v", ov.Invite)
	}
}

func TestFamilyBindSuccessAndOverview(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, studentID := createWorkspace(t, s)
	parentID := createParent(t, s, ws.ID, "家长甲")

	b := bindParent(t, s, ws.ID, studentID, parentID)
	if b.Status != "active" || b.StudentUserID != studentID || b.ParentUserID != parentID {
		t.Fatalf("unexpected binding: %+v", b)
	}
	if b.BoundAt == nil {
		t.Fatalf("active 绑定应记录 bound_at")
	}

	ov, err := s.Family.FamilyInviteGet(ctx, FamilyInviteGetReq{WorkspaceID: ws.ID, UserID: studentID})
	if err != nil {
		t.Fatalf("invite get: %v", err)
	}
	if ov.ActiveParents != 1 || len(ov.Bindings) != 1 {
		t.Fatalf("绑定后应展示 1 位家长，got %+v", ov)
	}
	if ov.Bindings[0].ParentDisplayName != "家长甲" {
		t.Fatalf("家长展示名未回填，got %+v", ov.Bindings[0])
	}
}

func TestFamilyBindDuplicateIsFamilyBound(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, studentID := createWorkspace(t, s)
	parentID := createParent(t, s, ws.ID, "家长甲")

	bindParent(t, s, ws.ID, studentID, parentID)
	inv, err := s.Family.FamilyInviteCreate(ctx, FamilyInviteCreateReq{
		WorkspaceID: ws.ID, UserID: studentID, IdempotencyKey: "inv-dup-0002",
	})
	if err != nil {
		t.Fatalf("second invite: %v", err)
	}
	_, err = s.Family.FamilyBind(ctx, FamilyBindReq{
		WorkspaceID: ws.ID, UserID: parentID, InviteCode: inv.Code,
	})
	de := domain.AsError(err)
	if de == nil || de.Code != domain.CodeFamilyBound {
		t.Fatalf("重复绑定应返回 FAMILY_BOUND，got %v", err)
	}
}

func TestFamilyBindRequiresParentRole(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, studentID := createWorkspace(t, s)
	teacherID := createTeacher(t, s, ws.ID)

	inv, err := s.Family.FamilyInviteCreate(ctx, FamilyInviteCreateReq{
		WorkspaceID: ws.ID, UserID: studentID, IdempotencyKey: "inv-role-0003",
	})
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}
	_, err = s.Family.FamilyBind(ctx, FamilyBindReq{
		WorkspaceID: ws.ID, UserID: teacherID, InviteCode: inv.Code,
	})
	de := domain.AsError(err)
	if de == nil || de.Code != domain.CodeForbidden {
		t.Fatalf("非家长角色绑定应 FORBIDDEN，got %v", err)
	}
}

func TestFamilyBindSelfRejected(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)
	// 家长给自己生成邀请（占位语义）后绑自己 → InvalidArg。
	parentID := createParent(t, s, ws.ID, "家长乙")
	inv, err := s.Family.FamilyInviteCreate(ctx, FamilyInviteCreateReq{
		WorkspaceID: ws.ID, UserID: parentID, IdempotencyKey: "inv-self",
	})
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}
	_, err = s.Family.FamilyBind(ctx, FamilyBindReq{
		WorkspaceID: ws.ID, UserID: parentID, InviteCode: inv.Code,
	})
	de := domain.AsError(err)
	if de == nil || de.Code != domain.CodeInvalidArgument {
		t.Fatalf("绑定自己应 INVALID_ARGUMENT，got %v", err)
	}
}

func TestFamilyBindMaxParents(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, studentID := createWorkspace(t, s)
	p1 := createParent(t, s, ws.ID, "家长一")
	p2 := createParent(t, s, ws.ID, "家长二")

	bindParent(t, s, ws.ID, studentID, p1)
	// 第 2 位家长绑定。
	inv, err := s.Family.FamilyInviteCreate(ctx, FamilyInviteCreateReq{
		WorkspaceID: ws.ID, UserID: studentID, IdempotencyKey: "inv-max-0004",
	})
	if err != nil {
		t.Fatalf("second invite: %v", err)
	}
	if _, err := s.Family.FamilyBind(ctx, FamilyBindReq{
		WorkspaceID: ws.ID, UserID: p2, InviteCode: inv.Code,
	}); err != nil {
		t.Fatalf("第 2 位家长应绑定成功: %v", err)
	}
	// 第 3 位 → 生成新邀请时即达上限（INVALID_STATE）。
	_, err = s.Family.FamilyInviteCreate(ctx, FamilyInviteCreateReq{
		WorkspaceID: ws.ID, UserID: studentID, IdempotencyKey: "inv-max-0005",
	})
	de := domain.AsError(err)
	if de == nil || de.Code != domain.CodeInvalidState {
		t.Fatalf("第 3 位家长应触发上限 INVALID_STATE，got %v", err)
	}
}

func TestFamilyUnbindEitherPartyAndThirdParty(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, studentID := createWorkspace(t, s)
	parentID := createParent(t, s, ws.ID, "家长甲")
	b := bindParent(t, s, ws.ID, studentID, parentID)

	// 第三方（另一家长）解除 → FORBIDDEN。
	other := createParent(t, s, ws.ID, "无关家长")
	_, err := s.Family.FamilyUnbind(ctx, FamilyUnbindReq{
		WorkspaceID: ws.ID, UserID: other, BindingID: b.ID, Version: 1,
	})
	de := domain.AsError(err)
	if de == nil || de.Code != domain.CodeForbidden {
		t.Fatalf("第三方解除应 FORBIDDEN，got %v", err)
	}
	// 家长方可解除。
	res, err := s.Family.FamilyUnbind(ctx, FamilyUnbindReq{
		WorkspaceID: ws.ID, UserID: parentID, BindingID: b.ID, Version: 1,
	})
	if err != nil {
		t.Fatalf("家长解除: %v", err)
	}
	if !res.Deleted {
		t.Fatalf("应返回 deleted=true")
	}
	// 学生方也可解除（新绑定）。
	b2 := bindParent(t, s, ws.ID, studentID, parentID)
	if _, err := s.Family.FamilyUnbind(ctx, FamilyUnbindReq{
		WorkspaceID: ws.ID, UserID: studentID, BindingID: b2.ID, Version: 1,
	}); err != nil {
		t.Fatalf("学生解除: %v", err)
	}
}

func TestParentSettingsUpdateValidation(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, studentID := createWorkspace(t, s)
	parentID := createParent(t, s, ws.ID, "家长甲")
	bindParent(t, s, ws.ID, studentID, parentID)

	// 非法 daily_limit。
	_, err := s.Family.ParentSettingsUpdate(ctx, ParentSettingsUpdateReq{
		WorkspaceID: ws.ID, UserID: parentID, StudentUserID: studentID,
		DailyLimitMin: -5, AIDisabled: true, ReportEnabled: true,
	})
	de := domain.AsError(err)
	if de == nil || de.Code != domain.CodeInvalidArgument {
		t.Fatalf("非法 daily_limit_min 应 INVALID_ARGUMENT，got %v", err)
	}
	// 正常更新。
	ps, err := s.Family.ParentSettingsUpdate(ctx, ParentSettingsUpdateReq{
		WorkspaceID: ws.ID, UserID: parentID, StudentUserID: studentID,
		DailyLimitMin: 60, AIDisabled: true, ReportEnabled: false,
	})
	if err != nil {
		t.Fatalf("update settings: %v", err)
	}
	if ps.DailyLimitMin != 60 || !ps.AIDisabled || ps.ReportEnabled {
		t.Fatalf("settings 未生效: %+v", ps)
	}
	// 未绑定的家长 → FORBIDDEN。
	unbound := createParent(t, s, ws.ID, "未绑定家长")
	_, err = s.Family.ParentSettingsUpdate(ctx, ParentSettingsUpdateReq{
		WorkspaceID: ws.ID, UserID: unbound, StudentUserID: studentID,
		DailyLimitMin: 30, AIDisabled: false, ReportEnabled: true,
	})
	de = domain.AsError(err)
	if de == nil || de.Code != domain.CodeForbidden {
		t.Fatalf("未绑定家长设置应 FORBIDDEN，got %v", err)
	}
	// 非家长角色 → FORBIDDEN。
	teacherID := createTeacher(t, s, ws.ID)
	_, err = s.Family.ParentSettingsUpdate(ctx, ParentSettingsUpdateReq{
		WorkspaceID: ws.ID, UserID: teacherID, StudentUserID: studentID,
		DailyLimitMin: 30, AIDisabled: false, ReportEnabled: true,
	})
	de = domain.AsError(err)
	if de == nil || de.Code != domain.CodeForbidden {
		t.Fatalf("非家长角色设置应 FORBIDDEN，got %v", err)
	}
}

// TestParentWriteForbiddenGoalCreate 家长写操作（GoalCreate）→ FORBIDDEN（4.21 G6 家长只读）。
func TestParentWriteForbiddenGoalCreate(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)
	parentID := createParent(t, s, ws.ID, "家长甲")

	_, err := s.Goal.GoalCreate(ctx, GoalCreateReq{
		WorkspaceID: ws.ID, UserID: parentID, Name: "家长创建目标",
	})
	de := domain.AsError(err)
	if de == nil || de.Code != domain.CodeForbidden {
		t.Fatalf("家长写操作应 FORBIDDEN，got %v", err)
	}
}

// TestEnforceParentAIDisabled 家长关闭 AI → EnforceParentAI 返回 FEATURE_DISABLED。
func TestEnforceParentAIDisabled(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, studentID := createWorkspace(t, s)
	parentID := createParent(t, s, ws.ID, "家长甲")
	bindParent(t, s, ws.ID, studentID, parentID)

	// 默认 AI 开启。
	if err := s.EnforceParentAI(ctx, studentID); err != nil {
		t.Fatalf("默认应放行 AI: %v", err)
	}
	if _, err := s.Family.ParentSettingsUpdate(ctx, ParentSettingsUpdateReq{
		WorkspaceID: ws.ID, UserID: parentID, StudentUserID: studentID,
		DailyLimitMin: 0, AIDisabled: true, ReportEnabled: true,
	}); err != nil {
		t.Fatalf("close AI: %v", err)
	}
	err := s.EnforceParentAI(ctx, studentID)
	de := domain.AsError(err)
	if de == nil || de.Code != domain.CodeFeatureDisabled {
		t.Fatalf("关闭 AI 后应 FEATURE_DISABLED，got %v", err)
	}
}

// TestAgentChatSendBlockedWhenAIDisabled Agent 发送入口经 BeforeChat 注入拦截。
func TestAgentChatSendBlockedWhenAIDisabled(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, studentID := createWorkspace(t, s)
	parentID := createParent(t, s, ws.ID, "家长甲")
	bindParent(t, s, ws.ID, studentID, parentID)
	if _, err := s.Family.ParentSettingsUpdate(ctx, ParentSettingsUpdateReq{
		WorkspaceID: ws.ID, UserID: parentID, StudentUserID: studentID,
		DailyLimitMin: 0, AIDisabled: true, ReportEnabled: true,
	}); err != nil {
		t.Fatalf("close AI: %v", err)
	}
	// 模拟 app 组装注入 BeforeChat。
	s.Agent.BeforeChat = func(ctx context.Context, session *repository.AgentSessionRow) error {
		return s.EnforceParentAI(ctx, session.UserID)
	}
	sess, err := s.Agent.AgentChatCreate(ctx, agent.AgentChatCreateReq{
		WorkspaceID: ws.ID, UserID: studentID, Agent: "tutor",
	})
	if err != nil {
		t.Fatalf("create agent session: %v", err)
	}
	_, err = s.Agent.AgentChatSend(ctx, agent.AgentChatSendReq{
		WorkspaceID: ws.ID, SessionID: sess.ID, Message: "帮我复习",
	})
	de := domain.AsError(err)
	if de == nil || de.Code != domain.CodeFeatureDisabled {
		t.Fatalf("关闭 AI 后 AgentChatSend 应 FEATURE_DISABLED，got %v", err)
	}
}

// TestPracticeStartDailyLimit 家长每日时长上限 → PracticeStart QUOTA_EXCEEDED。
func TestPracticeStartDailyLimit(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, studentID := createWorkspace(t, s)
	parentID := createParent(t, s, ws.ID, "家长甲")
	bindParent(t, s, ws.ID, studentID, parentID)
	if _, err := s.Family.ParentSettingsUpdate(ctx, ParentSettingsUpdateReq{
		WorkspaceID: ws.ID, UserID: parentID, StudentUserID: studentID,
		DailyLimitMin: 10, AIDisabled: false, ReportEnabled: true,
	}); err != nil {
		t.Fatalf("set limit: %v", err)
	}
	// 今日已完成 15 分钟（11 分钟已超限）。
	now := time.Now().UTC()
	if _, err := s.Repo.DB().ExecContext(ctx, `
		INSERT INTO timer_sessions (id, workspace_id, user_id, mode, planned_minutes, actual_seconds, status, started_at, ended_at)
		VALUES (?, ?, ?, 'pomodoro', 0, ?, 'completed', ?, ?)`,
		NewID(), ws.ID, studentID, 15*60,
		now.Add(-30*time.Minute).Format(time.RFC3339), now.Format(time.RFC3339)); err != nil {
		t.Fatalf("seed timer: %v", err)
	}
	_, err := s.Practice.PracticeStart(ctx, PracticeStartReq{
		WorkspaceID: ws.ID, UserID: studentID, Mode: "practice",
		QuestionIDs: []string{"q-nonexistent"},
	})
	de := domain.AsError(err)
	if de == nil || de.Code != domain.CodeQuotaExceeded {
		t.Fatalf("超限后 PracticeStart 应 QUOTA_EXCEEDED，got %v", err)
	}
	// 提高上限后应放行至题目校验（q-nonexistent → NOT_FOUND），证明限额门禁为严格小于。
	if _, err := s.Family.ParentSettingsUpdate(ctx, ParentSettingsUpdateReq{
		WorkspaceID: ws.ID, UserID: parentID, StudentUserID: studentID,
		DailyLimitMin: 60, AIDisabled: false, ReportEnabled: true,
	}); err != nil {
		t.Fatalf("raise limit: %v", err)
	}
	_, err = s.Practice.PracticeStart(ctx, PracticeStartReq{
		WorkspaceID: ws.ID, UserID: studentID, Mode: "practice",
		QuestionIDs: []string{"q-nonexistent"},
	})
	de = domain.AsError(err)
	if de == nil || de.Code != domain.CodeNotFound {
		t.Fatalf("未超限应放行至题目校验 NOT_FOUND，got %v", err)
	}
}

// TestFamilyViewGetAggregation 家长视图聚合且不含隐私明细字段（G4）。
func TestFamilyViewGetAggregation(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, studentID := createWorkspace(t, s)
	parentID := createParent(t, s, ws.ID, "家长甲")
	bindParent(t, s, ws.ID, studentID, parentID)
	if _, err := s.Family.ParentSettingsUpdate(ctx, ParentSettingsUpdateReq{
		WorkspaceID: ws.ID, UserID: parentID, StudentUserID: studentID,
		DailyLimitMin: 45, AIDisabled: false, ReportEnabled: true,
	}); err != nil {
		t.Fatalf("set settings: %v", err)
	}

	items, err := s.Family.FamilyViewGet(ctx, FamilyViewReq{WorkspaceID: ws.ID, UserID: parentID})
	if err != nil {
		t.Fatalf("family view: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("应返回 1 个学生聚合，got %d", len(items))
	}
	it := items[0]
	if it.Student.UserID != studentID || it.Student.DisplayName == "" {
		t.Fatalf("student 聚合异常: %+v", it.Student)
	}
	if it.Settings.DailyLimitMin != 45 || it.Settings.ParentUserID != parentID {
		t.Fatalf("settings 聚合异常: %+v", it.Settings)
	}
	// 无错题时 weak_knowledge 必须是空切片而非 null（避免前端 .length 白屏）。
	if it.WeakKnowledge == nil {
		t.Fatal("weak_knowledge should be non-nil empty slice, not null")
	}
	if len(it.WeakKnowledge) != 0 {
		t.Fatalf("expected empty weak knowledge, got %+v", it.WeakKnowledge)
	}
	// G4：JSON 输出不得包含答案/错题正文/AI 会话等隐私明细。
	raw, err := json.Marshal(it)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	js := string(raw)
	for _, key := range []string{`"answer"`, `"solution"`, `"wrong_text"`, `"content"`, `"session"`} {
		if strings.Contains(js, key) {
			t.Fatalf("家长视图泄漏隐私字段 %s: %s", key, js)
		}
	}
	// 学生端不可调用家长视图（非家长角色）。
	_, err = s.Family.FamilyViewGet(ctx, FamilyViewReq{WorkspaceID: ws.ID, UserID: studentID})
	de := domain.AsError(err)
	if de == nil || de.Code != domain.CodeForbidden {
		t.Fatalf("学生调用家长视图应 FORBIDDEN，got %v", err)
	}
}
