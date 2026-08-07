// family_service.go 家庭绑定与家长/监护人模式用例（完整设计文档 4.21 / API 文档 7.10）。
// 依赖 users.role='parent' 枚举（Todo 26 0006_audit.sql）与 family_bindings/parent_settings 表（0005）。
// 不新增迁移、不新增错误码。
package service

import (
	"context"
	"strings"
	"time"

	"lumo/internal/domain"
	"lumo/internal/repository"
)

// FamilyService 实现家庭绑定与家长视图用例。
type FamilyService struct{ s *Services }

// familyInviteTTLHours 邀请码有效期（4.21 G1：24 小时）。
const familyInviteTTLHours = 24

// familyMaxParents 每位学生最多绑定家长数（4.21 G1）。
const familyMaxParents = 2

// FamilyBinding 是家庭绑定 DTO。
type FamilyBinding struct {
	ID                string  `json:"id"`
	StudentUserID     string  `json:"student_user_id"`
	ParentUserID      string  `json:"parent_user_id"`
	ParentDisplayName string  `json:"parent_display_name"`
	Status            string  `json:"status"`
	BoundAt           *string `json:"bound_at"`
	RevokedAt         *string `json:"revoked_at"`
	CreatedAt         string  `json:"created_at"`
}

// FamilyInvite 是家庭邀请码 DTO（24h 有效）。
type FamilyInvite struct {
	BindingID string `json:"binding_id"`
	Code      string `json:"code"`
	Status    string `json:"status"`
	ExpiresAt string `json:"expires_at"`
	CreatedAt string `json:"created_at"`
}

// FamilyOverview 是学生端家庭面板（邀请码 + 当前绑定列表）。
type FamilyOverview struct {
	Invite        *FamilyInvite    `json:"invite"`
	ActiveParents int              `json:"active_parents"`
	Bindings      []*FamilyBinding `json:"bindings"`
}

// ParentSettings 是家长限制设置 DTO。
type ParentSettings struct {
	ParentUserID  string `json:"parent_user_id"`
	StudentUserID string `json:"student_user_id"`
	DailyLimitMin int    `json:"daily_limit_min"`
	AIDisabled    bool   `json:"ai_disabled"`
	ReportEnabled bool   `json:"report_enabled"`
	UpdatedAt     string `json:"updated_at"`
}

// FamilyStudent 是家长视图中的学生信息。
type FamilyStudent struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
}

// FamilyMinutes 是学习时长聚合。
type FamilyMinutes struct {
	Today int `json:"today"`
	Week  int `json:"week"`
}

// FamilyViewItem 是家长视图单学生聚合（G2：时长/打卡/完成率/正确率/薄弱知识点）。
// G4 隐私边界：只暴露聚合指标，不含题目答案明细、错题正文、AI 会话原文、长期记忆、笔记、闪卡内容。
type FamilyViewItem struct {
	BindingID     string          `json:"binding_id"`
	Student       FamilyStudent   `json:"student"`
	StudyMinutes  FamilyMinutes   `json:"study_minutes"`
	StreakDays    int             `json:"streak_days"`
	TotalCheckins int             `json:"total_checkins"`
	TaskSummary   TaskSummary     `json:"task_summary"`
	Accuracy      AccuracySummary `json:"accuracy"`
	WeakKnowledge []WeakKnowledge `json:"weak_knowledge"`
	Settings      ParentSettings  `json:"settings"`
}

// FamilyInviteCreateReq 生成家庭邀请码请求（学生发起）。
type FamilyInviteCreateReq struct {
	WorkspaceID    string `json:"workspace_id"`
	UserID         string `json:"user_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

// FamilyInviteGetReq 查询学生家庭面板请求。
type FamilyInviteGetReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
}

// FamilyBindReq 家长绑定请求。
type FamilyBindReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	InviteCode  string `json:"invite_code"`
}

// FamilyUnbindReq 解除绑定请求（任一方）。
type FamilyUnbindReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	BindingID   string `json:"binding_id"`
	Version     int    `json:"version"`
}

// ParentSettingsUpdateReq 家长设置使用限制请求。
type ParentSettingsUpdateReq struct {
	WorkspaceID   string `json:"workspace_id"`
	UserID        string `json:"user_id"`
	StudentUserID string `json:"student_user_id"`
	DailyLimitMin int    `json:"daily_limit_min"`
	AIDisabled    bool   `json:"ai_disabled"`
	ReportEnabled bool   `json:"report_enabled"`
}

// FamilyViewReq 家长视图请求（student_user_id 可选：空=全部已绑定学生）。
type FamilyViewReq struct {
	WorkspaceID   string `json:"workspace_id"`
	UserID        string `json:"user_id"`
	StudentUserID string `json:"student_user_id"`
}

// FamilyInviteCreate 生成/刷新家庭邀请码（学生发起；复用 pending 行保幂等；24h 有效）。
// 幂等：同一学生存在有效 pending 邀请码时重新生成新码（RegenerateFamilyInvite 重置 created_at）。
func (f *FamilyService) FamilyInviteCreate(ctx context.Context, req FamilyInviteCreateReq) (*FamilyInvite, error) {
	if err := f.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if err := f.s.assertUserActive(ctx, req.UserID); err != nil {
		return nil, err
	}
	return withIdempotency(f.s, ctx, req.WorkspaceID, req.IdempotencyKey, "FamilyInviteCreate",
		func() (*FamilyInvite, error) { return f.doCreateInvite(ctx, req.WorkspaceID, req.UserID) })
}

func (f *FamilyService) doCreateInvite(ctx context.Context, wsID, userID string) (*FamilyInvite, error) {
	active, err := f.s.Repo.CountActiveFamilyBindingsForStudent(ctx, userID)
	if err != nil {
		return nil, err
	}
	if active >= familyMaxParents {
		return nil, domain.InvalidState("已达家长绑定上限（%d 位），请先解除后重试", familyMaxParents)
	}
	code, err := newInviteCode()
	if err != nil {
		return nil, err
	}
	pending, err := f.s.Repo.GetLatestPendingBindingByStudent(ctx, userID)
	if err != nil {
		return nil, err
	}
	if pending != nil {
		if err := f.s.Repo.RegenerateFamilyInvite(ctx, pending.ID, code); err != nil {
			return nil, err
		}
		pending, err = f.s.Repo.GetFamilyBindingByID(ctx, pending.ID)
		if err != nil {
			return nil, err
		}
	} else {
		row := &repository.FamilyBindingRow{
			ID: NewID(), StudentUserID: userID, ParentUserID: userID, // 占位满足 FK；激活时覆盖为真实家长
			InviteCode: code,
		}
		if err := f.s.Repo.CreateFamilyBinding(ctx, row); err != nil {
			return nil, err
		}
		pending, err = f.s.Repo.GetFamilyBindingByID(ctx, row.ID)
		if err != nil {
			return nil, err
		}
	}
	if pending == nil {
		return nil, domain.Conflict("邀请码生成失败，请重试")
	}
	f.s.audit(ctx, wsID, "family.invite_create", "family_binding", pending.ID,
		map[string]any{"student_user_id": userID})
	return familyInviteFromRow(pending), nil
}

// FamilyInviteGet 返回学生家庭面板（当前邀请码 + 绑定列表）。
func (f *FamilyService) FamilyInviteGet(ctx context.Context, req FamilyInviteGetReq) (*FamilyOverview, error) {
	if err := f.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	overview := &FamilyOverview{Bindings: []*FamilyBinding{}}
	pending, err := f.s.Repo.GetLatestPendingBindingByStudent(ctx, req.UserID)
	if err != nil {
		return nil, err
	}
	if pending != nil && !familyInviteExpired(pending.CreatedAt) {
		overview.Invite = familyInviteFromRow(pending)
	}
	rows, err := f.s.Repo.ListFamilyBindingsByStudent(ctx, req.UserID)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.Status != "active" {
			continue
		}
		b := familyBindingFromRow(r)
		if u, err := f.s.Repo.GetUser(ctx, req.WorkspaceID, r.ParentUserID); err == nil && u != nil {
			b.ParentDisplayName = u.DisplayName
		}
		overview.Bindings = append(overview.Bindings, b)
	}
	overview.ActiveParents = len(overview.Bindings)
	return overview, nil
}

// FamilyBind 家长通过邀请码绑定学生（G1：每位学生最多 2 位家长；重复绑定 → FAMILY_BOUND）。
func (f *FamilyService) FamilyBind(ctx context.Context, req FamilyBindReq) (*FamilyBinding, error) {
	if err := f.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if strings.TrimSpace(req.InviteCode) == "" {
		return nil, domain.InvalidArg("invite_code 必填")
	}
	if err := f.s.assertUserActive(ctx, req.UserID); err != nil {
		return nil, err
	}
	// 调用者必须是家长角色。
	actor, err := f.s.Repo.GetUser(ctx, req.WorkspaceID, req.UserID)
	if err != nil {
		return nil, err
	}
	if actor == nil {
		return nil, domain.Forbidden("用户不存在")
	}
	if actor.Role != "parent" {
		f.s.audit(ctx, req.WorkspaceID, "family.bind", "family_binding", "",
			map[string]any{"forbidden": true, "role": actor.Role, "user_id": req.UserID})
		return nil, domain.Forbidden("仅家长角色可绑定学生")
	}

	pending, err := f.s.Repo.GetFamilyBindingByCode(ctx, strings.TrimSpace(req.InviteCode))
	if err != nil {
		return nil, err
	}
	if pending == nil {
		return nil, domain.NotFound("邀请码无效或已失效")
	}
	if familyInviteExpired(pending.CreatedAt) {
		return nil, domain.InvalidState("邀请码已过期（24 小时），请让学生重新生成")
	}
	if pending.StudentUserID == req.UserID {
		return nil, domain.InvalidArg("不能绑定自己")
	}
	// 重复绑定（同一学生+同一家长已有 active 绑定）→ FAMILY_BOUND。
	existing, err := f.s.Repo.GetActiveFamilyBinding(ctx, pending.StudentUserID, req.UserID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, domain.FamilyBound("你已绑定该学生，请勿重复绑定")
	}
	active, err := f.s.Repo.CountActiveFamilyBindingsForStudent(ctx, pending.StudentUserID)
	if err != nil {
		return nil, err
	}
	if active >= familyMaxParents {
		return nil, domain.InvalidState("该学生已绑定 %d 位家长，达到上限", familyMaxParents)
	}
	// 激活（原子迁移：仅 pending 可激活，防并发重复绑定）。
	boundAt := Now()
	ok, err := f.s.Repo.ActivateFamilyBinding(ctx, pending.ID, req.UserID, boundAt)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, domain.Conflict("该邀请码已被使用，请使用新邀请码")
	}
	// 默认限制设置（无时长上限、AI 开启、周报开启）。
	if _, err := f.s.Repo.UpsertParentSettings(ctx, f.s.Repo.DB(), &repository.ParentSettingsRow{
		ParentUserID: req.UserID, StudentUserID: pending.StudentUserID,
		DailyLimitMin: 0, AIDisabled: false, ReportEnabled: true,
	}); err != nil {
		return nil, err
	}
	f.s.audit(ctx, req.WorkspaceID, "family.bind", "family_binding", pending.ID,
		map[string]any{"student_user_id": pending.StudentUserID, "parent_user_id": req.UserID})
	return familyBindingFromRow(&repository.FamilyBindingRow{
		ID: pending.ID, StudentUserID: pending.StudentUserID, ParentUserID: req.UserID,
		Status: "active", BoundAt: &boundAt, CreatedAt: pending.CreatedAt,
	}), nil
}

// FamilyUnbind 解除绑定（G6：任一方可解除；解除后家长侧立即失效）。
func (f *FamilyService) FamilyUnbind(ctx context.Context, req FamilyUnbindReq) (*DeleteResult, error) {
	if err := f.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if req.BindingID == "" {
		return nil, domain.InvalidArg("binding_id 必填")
	}
	if err := f.s.assertUserActive(ctx, req.UserID); err != nil {
		return nil, err
	}
	b, err := f.s.Repo.GetFamilyBindingByID(ctx, req.BindingID)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, domain.NotFound("绑定不存在")
	}
	if b.StudentUserID != req.UserID && b.ParentUserID != req.UserID {
		f.s.audit(ctx, req.WorkspaceID, "family.unbind", "family_binding", b.ID,
			map[string]any{"forbidden": true, "user_id": req.UserID})
		return nil, domain.Forbidden("无权解除该绑定")
	}
	if b.Status != "active" {
		return nil, domain.InvalidState("该绑定已解除")
	}
	revokedAt := Now()
	ok, err := f.s.Repo.RevokeFamilyBinding(ctx, b.ID, revokedAt)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, domain.Conflict("绑定状态已变化，请刷新后重试")
	}
	f.s.audit(ctx, req.WorkspaceID, "family.unbind", "family_binding", b.ID,
		map[string]any{"student_user_id": b.StudentUserID, "parent_user_id": b.ParentUserID})
	return &DeleteResult{Deleted: true, DeletedAt: revokedAt}, nil
}

// ParentSettingsUpdate 家长更新使用限制（G3：每日时长 / AI 开关 / 周报开关，即时生效）。
func (f *FamilyService) ParentSettingsUpdate(ctx context.Context, req ParentSettingsUpdateReq) (*ParentSettings, error) {
	if err := f.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if err := f.s.assertUserActive(ctx, req.UserID); err != nil {
		return nil, err
	}
	if req.StudentUserID == "" {
		return nil, domain.InvalidArg("student_user_id 必填")
	}
	if req.DailyLimitMin < 0 || req.DailyLimitMin > 1440 {
		return nil, domain.InvalidArg("daily_limit_min 须为 0-1440（0=不限）")
	}
	actor, err := f.s.Repo.GetUser(ctx, req.WorkspaceID, req.UserID)
	if err != nil {
		return nil, err
	}
	if actor == nil {
		return nil, domain.Forbidden("用户不存在")
	}
	if actor.Role != "parent" {
		f.s.audit(ctx, req.WorkspaceID, "family.settings_update", "parent_settings", "",
			map[string]any{"forbidden": true, "role": actor.Role, "user_id": req.UserID})
		return nil, domain.Forbidden("仅家长角色可设置使用限制")
	}
	// 必须与目标学生存在 active 绑定。
	b, err := f.s.Repo.GetActiveFamilyBinding(ctx, req.StudentUserID, req.UserID)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, domain.Forbidden("尚未绑定该学生，无法设置使用限制")
	}
	row, err := f.s.Repo.UpsertParentSettings(ctx, f.s.Repo.DB(), &repository.ParentSettingsRow{
		ParentUserID: req.UserID, StudentUserID: req.StudentUserID,
		DailyLimitMin: req.DailyLimitMin, AIDisabled: req.AIDisabled, ReportEnabled: req.ReportEnabled,
	})
	if err != nil {
		return nil, err
	}
	f.s.audit(ctx, req.WorkspaceID, "family.settings_update", "parent_settings", req.StudentUserID,
		map[string]any{"parent_user_id": req.UserID, "daily_limit_min": req.DailyLimitMin,
			"ai_disabled": req.AIDisabled, "report_enabled": req.ReportEnabled})
	return parentSettingsFromRow(row), nil
}

// FamilyViewGet 家长视图（G2：仅聚合指标；G4：不暴露私有明细）。
func (f *FamilyService) FamilyViewGet(ctx context.Context, req FamilyViewReq) ([]*FamilyViewItem, error) {
	if err := f.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	actor, err := f.s.Repo.GetUser(ctx, req.WorkspaceID, req.UserID)
	if err != nil {
		return nil, err
	}
	if actor == nil {
		return nil, domain.Forbidden("用户不存在")
	}
	if actor.Role != "parent" {
		f.s.audit(ctx, req.WorkspaceID, "family.view", "family_binding", "",
			map[string]any{"forbidden": true, "role": actor.Role, "user_id": req.UserID})
		return nil, domain.Forbidden("仅家长角色可查看家庭视图")
	}
	bindings, err := f.s.Repo.ListActiveBindingsForParent(ctx, req.UserID)
	if err != nil {
		return nil, err
	}
	out := make([]*FamilyViewItem, 0, len(bindings))
	for _, b := range bindings {
		if req.StudentUserID != "" && b.StudentUserID != req.StudentUserID {
			continue
		}
		item, err := f.buildFamilyViewItem(ctx, req.WorkspaceID, req.UserID, b)
		if err != nil {
			return nil, err
		}
		if item != nil {
			out = append(out, item)
		}
	}
	return out, nil
}

// buildFamilyViewItem 聚合单个学生的家长视图指标。
func (f *FamilyService) buildFamilyViewItem(ctx context.Context, wsID, parentID string, b *repository.FamilyBindingRow) (*FamilyViewItem, error) {
	item := &FamilyViewItem{BindingID: b.ID}
	studentID := b.StudentUserID
	stu, err := f.s.Repo.GetUser(ctx, wsID, studentID)
	if err != nil {
		return nil, err
	}
	if stu == nil {
		return nil, nil
	}
	item.Student = FamilyStudent{UserID: stu.ID, DisplayName: stu.DisplayName}
	settings, err := f.s.Repo.GetParentSettings(ctx, parentID, studentID)
	if err != nil {
		return nil, err
	}
	if settings != nil {
		item.Settings = *parentSettingsFromRow(settings)
	}

	now := time.Now().UTC()
	dayStart := now.Truncate(24 * time.Hour).Format(time.RFC3339)
	todayMin, err := f.s.Repo.CountTodayStudyMinutes(ctx, studentID, dayStart)
	if err != nil {
		return nil, err
	}
	weekStart := now.AddDate(0, 0, -6).Truncate(24 * time.Hour).Format(time.RFC3339)
	weekMin, err := f.s.Repo.CountStudyMinutesBetween(ctx, studentID, weekStart, dayStart)
	if err != nil {
		return nil, err
	}
	item.StudyMinutes = FamilyMinutes{Today: todayMin, Week: weekMin + todayMin}

	// 打卡与连续天数。
	dates, err := f.s.Repo.ListCheckinDates(ctx, studentID)
	if err != nil {
		return nil, err
	}
	item.TotalCheckins = len(dates)
	if streak, _, err := domain.ComputeStreak(dates, domain.CheckinDate(now)); err == nil {
		item.StreakDays = streak
	}

	db := f.s.Repo.DB()
	// 今日任务完成率。
	dayEnd := now.Truncate(24*time.Hour).AddDate(0, 0, 1).Format(time.RFC3339)
	var total, completed int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*), COALESCE(sum(CASE WHEN status = 'completed' THEN 1 ELSE 0 END), 0)
		FROM plan_tasks
		WHERE workspace_id = ? AND user_id = ? AND deleted_at IS NULL
		  AND due_at >= ? AND due_at < ?`,
		wsID, studentID, dayStart, dayEnd,
	).Scan(&total, &completed); err != nil {
		return nil, dbErr(err)
	}
	item.TaskSummary = TaskSummary{Total: total, Completed: completed, Pending: total - completed}

	// 近 7 天客观题正确率。
	var correct, graded int
	if err := db.QueryRowContext(ctx, `
		SELECT COALESCE(sum(CASE WHEN g.score >= g.max_score THEN 1 ELSE 0 END), 0),
		       COALESCE(count(*), 0)
		FROM grading_results g
		JOIN submissions s ON g.submission_id = s.id
		JOIN practice_sessions p ON s.session_id = p.id
		WHERE p.workspace_id = ? AND p.user_id = ? AND g.status = 'completed'
		  AND g.method = 'rule' AND s.submitted_at >= ?`,
		wsID, studentID, now.AddDate(0, 0, -6).Format(time.RFC3339),
	).Scan(&correct, &graded); err != nil {
		return nil, dbErr(err)
	}
	rate := 0.0
	if graded > 0 {
		rate = float64(correct) / float64(graded)
	}
	item.Accuracy = AccuracySummary{Correct: correct, Total: graded, Rate: rate}

	// 薄弱知识点（仅知识点名 + 错题数聚合，不暴露题目/答案正文）。
	rows, err := db.QueryContext(ctx, `
		SELECT k.id, k.name, count(*) AS cnt
		FROM wrong_answers w
		JOIN question_knowledge qk ON qk.question_version_id = w.question_version_id
		JOIN knowledge_nodes k ON k.id = qk.knowledge_id
		WHERE w.workspace_id = ? AND w.user_id = ? AND w.deleted_at IS NULL AND w.status = 'active'
		GROUP BY k.id, k.name
		ORDER BY cnt DESC
		LIMIT 5`, wsID, studentID)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var wk WeakKnowledge
		if err := rows.Scan(&wk.KnowledgeID, &wk.Name, &wk.WrongCount); err != nil {
			return nil, dbErr(err)
		}
		item.WeakKnowledge = append(item.WeakKnowledge, wk)
	}
	if err := rows.Err(); err != nil {
		return nil, dbErr(err)
	}
	return item, nil
}

// familyInviteExpired 判断邀请码是否过期（created_at + 24h）。
func familyInviteExpired(createdAt string) bool {
	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return true
	}
	return time.Now().UTC().Sub(t) > familyInviteTTLHours*time.Hour
}

func familyInviteFromRow(r *repository.FamilyBindingRow) *FamilyInvite {
	expires := r.CreatedAt
	if t, err := time.Parse(time.RFC3339, r.CreatedAt); err == nil {
		expires = t.Add(familyInviteTTLHours * time.Hour).Format(time.RFC3339)
	}
	return &FamilyInvite{
		BindingID: r.ID, Code: r.InviteCode, Status: r.Status,
		ExpiresAt: expires, CreatedAt: r.CreatedAt,
	}
}

func familyBindingFromRow(r *repository.FamilyBindingRow) *FamilyBinding {
	return &FamilyBinding{
		ID: r.ID, StudentUserID: r.StudentUserID, ParentUserID: r.ParentUserID,
		Status: r.Status, BoundAt: r.BoundAt, RevokedAt: r.RevokedAt, CreatedAt: r.CreatedAt,
	}
}

func parentSettingsFromRow(r *repository.ParentSettingsRow) *ParentSettings {
	return &ParentSettings{
		ParentUserID: r.ParentUserID, StudentUserID: r.StudentUserID,
		DailyLimitMin: r.DailyLimitMin, AIDisabled: r.AIDisabled,
		ReportEnabled: r.ReportEnabled, UpdatedAt: r.UpdatedAt,
	}
}
