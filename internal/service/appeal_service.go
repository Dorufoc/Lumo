package service

import (
	"context"
	"encoding/json"
	"strings"

	"lumo/internal/agent"
	"lumo/internal/domain"
	"lumo/internal/repository"
)

// AppealsService 申诉与复议业务：设计文档 4.22 C7 / API 文档 7.11。
// 状态机：pending→accepted|rejected（AppealResolve）；accepted + 改分完成 → resolved。
type AppealsService struct {
	s *Services
}

// Appeal 申诉 DTO。
type Appeal struct {
	ID            string `json:"id"`
	GradingID     string `json:"grading_id"`
	StudentUserID string `json:"student_user_id"`
	Reason        string `json:"reason"`
	Status        string `json:"status"`
	TeacherNote   string `json:"teacher_note"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

// AppealCreateReq 学生申诉：grading_id 即作业提交 id（assignment_submissions.id）。
type AppealCreateReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	GradingID   string `json:"grading_id"`
	Reason      string `json:"reason"`
}

// AppealResolveReq 教师处理申诉：decision ∈ accepted|rejected；
// decision=accepted 时可带 new_score 复议改分（grade_json version+1），改分完成 → resolved。
type AppealResolveReq struct {
	WorkspaceID string   `json:"workspace_id"`
	UserID      string   `json:"user_id"`
	AppealID    string   `json:"appeal_id"`
	Decision    string   `json:"decision"`
	NewScore    *float64 `json:"new_score,omitempty"`
	TeacherNote string   `json:"teacher_note,omitempty"`
}

// AppealListReq 教师复议视图：列出某作业全部申诉。
type AppealListReq struct {
	WorkspaceID  string `json:"workspace_id"`
	UserID       string `json:"user_id"`
	AssignmentID string `json:"assignment_id"`
}

// AppealCreate 学生提交申诉。
func (ap *AppealsService) AppealCreate(ctx context.Context, req AppealCreateReq) (*Appeal, error) {
	if err := ap.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 不能为空")
	}
	if req.GradingID == "" || !domain.ValidID(req.GradingID) {
		return nil, domain.InvalidArg("grading_id 无效")
	}
	if !domain.ValidAppealReason(req.Reason) {
		return nil, domain.InvalidArg("申诉理由长度应为 1-2000")
	}
	// grading_id = assignment_submissions.id
	sub, err := ap.s.Repo.GetAssignmentSubmissionByID(ctx, req.GradingID)
	if err != nil {
		return nil, err
	}
	if sub == nil {
		return nil, domain.NotFound("作业提交记录不存在")
	}
	// 只有提交者本人可以申诉
	if sub.StudentUserID != req.UserID {
		ap.s.audit(ctx, req.WorkspaceID, "appeal.create", "grading_appeal", req.GradingID,
			map[string]any{"forbidden": true, "reason": "not owner"})
		return nil, domain.Forbidden("只能申诉自己的作业")
	}
	// 必须先已批阅才能申诉
	if !assignmentSubmissionGraded(sub) {
		return nil, domain.InvalidState("作业尚未批阅，无法申诉")
	}
	// 同一作业提交只能申诉一次
	if existing, err := ap.s.Repo.GetAppealByGrading(ctx, sub.ID); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, domain.Conflict("该作业已申诉，请勿重复提交")
	}
	// 物化 synthetic grading_results 锚点行（grading_appeals.grading_id FK → grading_results(id)）
	// id = assignment_submissions.id，submission_id 指向作业提交关联的 practice submissions 锚点行。
	if sub.SubmissionID == nil {
		return nil, domain.InvalidState("作业缺少练习提交数据，无法申诉")
	}
	if err := ap.s.Repo.CreateGrading(ctx, &repository.GradingRow{
		ID:           sub.ID,
		SubmissionID: *sub.SubmissionID,
		Status:       "pending",
		Score:        nil,
		MaxScore:     appealAnchorMaxScore(sub.GradeJSON),
		Method:       "manual",
		Matched:      json.RawMessage("[]"),
		NeedsReview:  false,
	}); err != nil {
		return nil, err
	}
	appeal := &repository.AppealRow{
		ID: NewID(), GradingID: sub.ID, StudentUserID: req.UserID,
		Reason: strings.TrimSpace(req.Reason), Status: domain.AppealStatusPending,
	}
	if err := ap.s.Repo.CreateAppeal(ctx, appeal); err != nil {
		return nil, err
	}
	// 通知教师（班级 owner）
	if teacherID, err := ap.teacherForSubmission(ctx, req.WorkspaceID, sub); err != nil {
		return nil, err
	} else if teacherID != "" {
		_ = ap.s.UserEvents.Publish(teacherID, agent.Event{
			Name: agent.EventGradingAppeal,
			Payload: map[string]any{
				"appeal_id": appeal.ID, "grading_id": sub.ID, "status": domain.AppealStatusPending,
				"ref_type": "grading_appeal", "ref_id": appeal.ID,
			},
		})
	}
	ap.s.audit(ctx, req.WorkspaceID, "appeal.create", "grading_appeal", appeal.ID,
		map[string]any{"grading_id": sub.ID, "status": domain.AppealStatusPending})
	return ap.appealFromRow(appeal), nil
}

// AppealResolve 教师处理申诉：pending→accepted|rejected；accepted+new_score → 改分 + resolved。
func (ap *AppealsService) AppealResolve(ctx context.Context, req AppealResolveReq) (*Appeal, error) {
	if err := ap.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 不能为空")
	}
	if req.AppealID == "" || !domain.ValidID(req.AppealID) {
		return nil, domain.InvalidArg("appeal_id 无效")
	}
	if !domain.ValidAppealDecision(req.Decision) {
		return nil, domain.InvalidArg("decision 必须是 accepted 或 rejected")
	}
	if req.Decision == domain.AppealDecisionRejected && req.NewScore != nil {
		return nil, domain.InvalidArg("驳回申诉不能带新分数")
	}
	if req.NewScore != nil && *req.NewScore < 0 {
		return nil, domain.InvalidArg("新分数不能为负数")
	}
	appeal, err := ap.s.Repo.GetAppeal(ctx, req.AppealID)
	if err != nil {
		return nil, err
	}
	if appeal == nil {
		return nil, domain.NotFound("申诉不存在")
	}
	// 权限：教师 + 班级 owner（作业提交 → 作业 → 班级）
	sub, err := ap.s.Repo.GetAssignmentSubmissionByID(ctx, appeal.GradingID)
	if err != nil {
		return nil, err
	}
	if sub == nil {
		return nil, domain.NotFound("作业提交记录不存在")
	}
	asg, err := ap.s.Repo.GetAssignment(ctx, req.WorkspaceID, sub.AssignmentID)
	if err != nil {
		return nil, err
	}
	if asg == nil {
		return nil, domain.NotFound("作业不存在")
	}
	if _, err := ap.s.Classes.assertEditableClass(ctx, req.WorkspaceID, req.UserID, "appeal.resolve", asg.ClassID); err != nil {
		return nil, err
	}
	// 状态机：pending→accepted|rejected；accepted + 改分完成 → resolved。
	// 迁移前先校验合法性（rejected/resolved 为终态，非法迁移 → INVALID_STATE）。
	if req.Decision == domain.AppealDecisionRejected {
		if !domain.AppealCanTransition(appeal.Status, domain.AppealStatusRejected) {
			return nil, domain.InvalidState("申诉状态已变更，请刷新后重试")
		}
		if err := ap.s.Repo.UpdateAppealStatus(ctx, appeal.ID, appeal.Status, domain.AppealStatusRejected, req.TeacherNote); err != nil {
			return nil, err
		}
	} else {
		if !domain.AppealCanTransition(appeal.Status, domain.AppealStatusAccepted) {
			return nil, domain.InvalidState("申诉状态已变更，请刷新后重试")
		}
		if err := ap.s.Repo.UpdateAppealStatus(ctx, appeal.ID, appeal.Status, domain.AppealStatusAccepted, req.TeacherNote); err != nil {
			return nil, err
		}
		if req.NewScore != nil {
			// 复议改分：更新作业批阅 grade_json（version+1），改分完成 → resolved
			if err := ap.regradeWithNewScore(ctx, req.WorkspaceID, req.UserID, sub, *req.NewScore); err != nil {
				return nil, err
			}
			if err := ap.s.Repo.UpdateAppealStatus(ctx, appeal.ID, domain.AppealStatusAccepted, domain.AppealStatusResolved, req.TeacherNote); err != nil {
				return nil, err
			}
		}
	}
	saved, err := ap.s.Repo.GetAppeal(ctx, appeal.ID)
	if err != nil {
		return nil, err
	}
	if saved == nil {
		return nil, domain.NotFound("申诉不存在")
	}
	// 通知学生
	_ = ap.s.UserEvents.Publish(appeal.StudentUserID, agent.Event{
		Name: agent.EventGradingAppeal,
		Payload: map[string]any{
			"appeal_id": saved.ID, "grading_id": saved.GradingID, "status": saved.Status,
			"ref_type": "grading_appeal", "ref_id": saved.ID,
		},
	})
	ap.s.audit(ctx, req.WorkspaceID, "appeal.resolve", "grading_appeal", saved.ID,
		map[string]any{"grading_id": saved.GradingID, "status": saved.Status})
	return ap.appealFromRow(saved), nil
}

// AppealList 教师复议视图：列出某作业的全部申诉（教师 + 班级 owner）。
func (ap *AppealsService) AppealList(ctx context.Context, req AppealListReq) ([]*Appeal, error) {
	if err := ap.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 不能为空")
	}
	if req.AssignmentID == "" || !domain.ValidID(req.AssignmentID) {
		return nil, domain.InvalidArg("assignment_id 无效")
	}
	asg, err := ap.s.Repo.GetAssignment(ctx, req.WorkspaceID, req.AssignmentID)
	if err != nil {
		return nil, err
	}
	if asg == nil {
		return nil, domain.NotFound("作业不存在")
	}
	if _, err := ap.s.Classes.assertEditableClass(ctx, req.WorkspaceID, req.UserID, "appeal.list", asg.ClassID); err != nil {
		return nil, err
	}
	rows, err := ap.s.Repo.ListAppealsByAssignment(ctx, req.AssignmentID)
	if err != nil {
		return nil, err
	}
	out := make([]*Appeal, 0, len(rows))
	for _, r := range rows {
		out = append(out, ap.appealFromRow(r))
	}
	return out, nil
}

// regradeWithNewScore 复议改分：读当前 grade_json，version+1，overall=new_score，
// 保留 items/comment，走乐观锁 UpdateAssignmentGrade。
func (ap *AppealsService) regradeWithNewScore(ctx context.Context, wsID, userID string, sub *repository.AssignmentSubmissionRow, newScore float64) error {
	var g map[string]any
	curVersion := 0
	if err := json.Unmarshal([]byte(sub.GradeJSON), &g); err == nil {
		if v, ok := g["version"].(float64); ok {
			curVersion = int(v)
		}
	}
	if g == nil {
		g = map[string]any{}
	}
	g["version"] = curVersion + 1
	g["overall"] = newScore
	next, err := json.Marshal(g)
	if err != nil {
		return domain.InvalidArg("grade_json 序列化失败")
	}
	if err := ap.s.Repo.UpdateAssignmentGrade(ctx, sub.ID, string(next), curVersion); err != nil {
		return err
	}
	ap.s.audit(ctx, wsID, "assignment.grade", "assignment_submission", sub.ID,
		map[string]any{"via": "appeal.resolve", "version": curVersion + 1})
	return nil
}

// teacherForSubmission 作业提交 → 作业 → 班级 → owner（教师）。
func (ap *AppealsService) teacherForSubmission(ctx context.Context, wsID string, sub *repository.AssignmentSubmissionRow) (string, error) {
	asg, err := ap.s.Repo.GetAssignment(ctx, wsID, sub.AssignmentID)
	if err != nil {
		return "", err
	}
	if asg == nil {
		return "", nil
	}
	cls, err := ap.s.Repo.GetClass(ctx, wsID, asg.ClassID)
	if err != nil {
		return "", err
	}
	if cls == nil {
		return "", nil
	}
	return cls.OwnerUserID, nil
}

func (ap *AppealsService) appealFromRow(r *repository.AppealRow) *Appeal {
	return &Appeal{
		ID: r.ID, GradingID: r.GradingID, StudentUserID: r.StudentUserID,
		Reason: r.Reason, Status: r.Status, TeacherNote: r.TeacherNote,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// assignmentSubmissionGraded 判断作业提交是否已批阅：graded_at 非空或 grade_json 含 version≥1。
func assignmentSubmissionGraded(sub *repository.AssignmentSubmissionRow) bool {
	if sub.GradedAt != nil && *sub.GradedAt != "" {
		return true
	}
	var g map[string]any
	if err := json.Unmarshal([]byte(sub.GradeJSON), &g); err == nil {
		if v, ok := g["version"].(float64); ok && v >= 1 {
			return true
		}
	}
	return false
}

// appealAnchorMaxScore 从 grade_json 的 items[].max_score 求和（CHECK 要求 max_score>0；兜底 1）。
func appealAnchorMaxScore(gradeJSON string) float64 {
	var g struct {
		Items []struct {
			MaxScore float64 `json:"max_score"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(gradeJSON), &g); err == nil {
		total := 0.0
		for _, it := range g.Items {
			total += it.MaxScore
		}
		if total > 0 {
			return total
		}
	}
	return 1
}
