package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"lumo/internal/domain"
	"lumo/internal/repository"
)

// AssignmentsService 作业用例（完整设计文档 4.22 / API 文档 7.11）。
type AssignmentsService struct {
	s   *Services
	Now func() time.Time
}

// Assignment 是作业 DTO。
type Assignment struct {
	ID          string `json:"id"`
	ClassID     string `json:"class_id"`
	PaperID     string `json:"paper_id"`
	Title       string `json:"title"`
	DueAt       string `json:"due_at"`
	GradingRule string `json:"grading_rule"`
	Status      string `json:"status"`
	Version     int    `json:"version"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// AssignmentCreateReq 创建作业请求（教师，须已发布试卷）。
type AssignmentCreateReq struct {
	WorkspaceID    string `json:"workspace_id"`
	UserID         string `json:"user_id"`
	ClassID        string `json:"class_id"`
	PaperID        string `json:"paper_id"`
	Title          string `json:"title"`
	DueAt          string `json:"due_at"`
	GradingRule    string `json:"grading_rule"`
	IdempotencyKey string `json:"idempotency_key"`
}

// AssignmentPublishReq 发布作业请求（教师，乐观锁）。
type AssignmentPublishReq struct {
	WorkspaceID  string `json:"workspace_id"`
	UserID       string `json:"user_id"`
	AssignmentID string `json:"assignment_id"`
	Version      int    `json:"version"`
}

// AssignmentAnswer 是作业单题作答。
type AssignmentAnswer struct {
	QuestionVersionID string          `json:"question_version_id"`
	Answer            json.RawMessage `json:"answer"`
}

// AssignmentSubmitReq 提交作业请求（班级 active 学生）。
type AssignmentSubmitReq struct {
	WorkspaceID    string             `json:"workspace_id"`
	UserID         string             `json:"user_id"`
	AssignmentID   string             `json:"assignment_id"`
	Answers        []AssignmentAnswer `json:"answers"`
	IdempotencyKey string             `json:"idempotency_key"`
}

// AssignmentListReq 作业列表请求。
type AssignmentListReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
}

// AssignmentSubmissionListReq 作业提交名单请求（教师）。
type AssignmentSubmissionListReq struct {
	WorkspaceID  string `json:"workspace_id"`
	UserID       string `json:"user_id"`
	AssignmentID string `json:"assignment_id"`
}

// AssignmentSubmission 是作业提交记录 DTO。
type AssignmentSubmission struct {
	ID            string          `json:"id"`
	AssignmentID  string          `json:"assignment_id"`
	StudentUserID string          `json:"student_user_id"`
	DisplayName   string          `json:"display_name"`
	SubmissionID  *string         `json:"submission_id"`
	GradeJSON     json.RawMessage `json:"grade_json"`
	GradedAt      *string         `json:"graded_at"`
	CreatedAt     string          `json:"created_at"`
}

// AssignmentCreate 创建作业：校验教师权限、作业字段、试卷已发布，落库草稿态。
func (a *AssignmentsService) AssignmentCreate(ctx context.Context, req AssignmentCreateReq) (*Assignment, error) {
	if err := a.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.IdempotencyKey == "" {
		return nil, domain.InvalidArg("idempotency_key 必填")
	}
	return withIdempotency(a.s, ctx, req.WorkspaceID, req.IdempotencyKey, "AssignmentCreate",
		func() (*Assignment, error) { return a.doCreate(ctx, req) })
}

func (a *AssignmentsService) doCreate(ctx context.Context, req AssignmentCreateReq) (*Assignment, error) {
	if _, err := a.s.Classes.assertEditableClass(ctx, req.WorkspaceID, req.UserID, "assignment.create", req.ClassID); err != nil {
		return nil, err
	}
	title := strings.TrimSpace(req.Title)
	if !domain.ValidAssignmentTitle(title) {
		return nil, domain.InvalidArg("作业标题长度须为 1-200")
	}
	if req.GradingRule == "" {
		req.GradingRule = domain.GradingRuleAuto
	}
	if !domain.ValidGradingRule(req.GradingRule) {
		return nil, domain.InvalidArg("grading_rule 须为 auto/teacher/hybrid")
	}
	if !domain.ValidDueAt(req.DueAt) {
		return nil, domain.InvalidArg("due_at 须为合法 RFC3339 时间戳")
	}
	paper, err := a.s.Repo.GetExamPaper(ctx, req.WorkspaceID, req.PaperID)
	if err != nil {
		return nil, err
	}
	if paper == nil {
		return nil, domain.NotFound("试卷不存在")
	}
	if paper.Status != domain.ExamPaperStatusPublished {
		return nil, domain.InvalidState("仅已发布试卷可布置作业")
	}
	row := &repository.AssignmentRow{
		ID: NewID(), ClassID: req.ClassID, PaperID: req.PaperID,
		Title: title, DueAt: req.DueAt, GradingRule: req.GradingRule,
	}
	if err := a.s.Repo.CreateAssignment(ctx, row); err != nil {
		return nil, err
	}
	// 重新读取以带回 DB 侧默认值（status/version/时间戳）
	created, err := a.s.Repo.GetAssignment(ctx, req.WorkspaceID, row.ID)
	if err != nil {
		return nil, err
	}
	a.s.audit(ctx, req.WorkspaceID, "assignment.create", "assignment", row.ID,
		map[string]any{"class_id": req.ClassID, "paper_id": req.PaperID, "title": title})
	return a.assignmentFromRow(created), nil
}

// AssignmentPublish 发布作业：draft→published，乐观锁（version 不匹配 → CONFLICT）。
func (a *AssignmentsService) AssignmentPublish(ctx context.Context, req AssignmentPublishReq) (*Assignment, error) {
	if err := a.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	asg, err := a.s.Repo.GetAssignment(ctx, req.WorkspaceID, req.AssignmentID)
	if err != nil {
		return nil, err
	}
	if asg == nil {
		return nil, domain.NotFound("作业不存在")
	}
	if _, err := a.s.Classes.assertEditableClass(ctx, req.WorkspaceID, req.UserID, "assignment.publish", asg.ClassID); err != nil {
		return nil, err
	}
	// 先查版本：陈旧版本 → CONFLICT（乐观锁优先于状态校验，与测试契约一致）
	if asg.Version != req.Version {
		return nil, domain.Conflict("作业已被修改，请刷新后重试")
	}
	if asg.Status != domain.AssignmentStatusDraft {
		return nil, domain.InvalidState("仅草稿作业可发布")
	}
	updated, err := a.s.Repo.UpdateAssignmentStatus(ctx, req.WorkspaceID, req.AssignmentID, req.Version, domain.AssignmentStatusPublished)
	if err != nil {
		return nil, err
	}
	a.s.audit(ctx, req.WorkspaceID, "assignment.publish", "assignment", req.AssignmentID,
		map[string]any{"version": updated.Version})
	return a.assignmentFromRow(updated), nil
}

// AssignmentSubmit 提交作业：校验成员身份、截止时间与作业状态；作答落 practice 会话。
// 答案存储策略：为作业创建 mode='practice' 会话 + 每题 submissions 行（status='submitted'），
// assignment_submissions.submission_id 指向首条 submissions 行，保证 FK 有效且答案可恢复。
func (a *AssignmentsService) AssignmentSubmit(ctx context.Context, req AssignmentSubmitReq) (*AssignmentSubmission, error) {
	if err := a.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.IdempotencyKey == "" {
		return nil, domain.InvalidArg("idempotency_key 必填")
	}
	return withIdempotency(a.s, ctx, req.WorkspaceID, req.IdempotencyKey, "AssignmentSubmit",
		func() (*AssignmentSubmission, error) { return a.doSubmit(ctx, req) })
}

func (a *AssignmentsService) doSubmit(ctx context.Context, req AssignmentSubmitReq) (*AssignmentSubmission, error) {
	asg, err := a.s.Repo.GetAssignment(ctx, req.WorkspaceID, req.AssignmentID)
	if err != nil {
		return nil, err
	}
	if asg == nil {
		return nil, domain.NotFound("作业不存在")
	}
	if asg.Status != domain.AssignmentStatusPublished {
		return nil, domain.InvalidState("作业未发布或已关闭，不可提交")
	}
	// 仅 active 成员可提交（教师不在此列）
	member, err := a.s.Repo.GetClassMember(ctx, asg.ClassID, req.UserID)
	if err != nil {
		return nil, err
	}
	if member == nil || member.Status != domain.ClassMemberStatusActive {
		return nil, domain.Forbidden("仅班级成员可提交作业")
	}
	// 截止时间检查（无新错误码，复用 INVALID_ARGUMENT）
	if due, err := domain.ParseTime(asg.DueAt); err != nil {
		return nil, domain.InvalidArg("作业截止时间无效")
	} else if a.Now().After(due) {
		return nil, domain.InvalidArg("已过作业截止时间，无法提交")
	}
	// 判重：同一作业学生仅允许一次提交
	if existing, err := a.s.Repo.GetAssignmentSubmission(ctx, req.AssignmentID, req.UserID); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, domain.Conflict("作业已提交，请勿重复提交")
	}
	// 锁定题目快照（与考试同一套快照机制）
	sections, err := a.s.Repo.GetExamPaperSections(ctx, asg.PaperID)
	if err != nil {
		return nil, err
	}
	snapshot, err := a.s.Exam.buildSnapshot(ctx, sections)
	if err != nil {
		return nil, err
	}
	validQV := map[string]bool{}
	for _, q := range snapshot {
		validQV[q.QuestionVersionID] = true
	}
	for _, ans := range req.Answers {
		if !validQV[ans.QuestionVersionID] {
			return nil, domain.InvalidArg("答案包含本作业之外的题目：%s", ans.QuestionVersionID)
		}
		if len(ans.Answer) == 0 || !json.Valid(ans.Answer) {
			return nil, domain.InvalidArg("题目 %s 的答案格式无效", ans.QuestionVersionID)
		}
	}

	// 会话即提交载体：创建 practice 会话 + 每题提交行
	now := a.Now().UTC().Format(time.RFC3339)
	sessionID := NewID()
	session := &repository.PracticeSessionRow{
		ID: sessionID, WorkspaceID: req.WorkspaceID, UserID: req.UserID,
		Mode: "practice", QuestionSnapshot: mustJSON(snapshot), StartedAt: &now,
	}
	if err := a.s.Repo.CreateSession(ctx, session); err != nil {
		return nil, err
	}
	if err := a.s.Repo.MarkSessionStarted(ctx, sessionID); err != nil {
		return nil, err
	}
	// 重新读取会话以取得 DB 侧真实 version（SubmitSession 乐观锁需要）
	started, err := a.s.Repo.GetSession(ctx, req.WorkspaceID, sessionID)
	if err != nil {
		return nil, err
	}
	if started == nil {
		return nil, domain.NotFound("练习会话不存在")
	}
	var firstSubID string
	for i, ans := range req.Answers {
		subID := NewID()
		if firstSubID == "" {
			firstSubID = subID
		}
		draft := &repository.SubmissionRow{
			ID: subID, SessionID: sessionID, QuestionVersionID: ans.QuestionVersionID,
			Answer: ans.Answer, ClientSequence: i + 1,
		}
		if _, err := a.s.Repo.UpsertDraft(ctx, draft); err != nil {
			return nil, err
		}
		if err := a.s.Repo.MarkSubmissionSubmitted(ctx, subID); err != nil {
			return nil, err
		}
	}
	if err := a.s.Repo.SubmitSession(ctx, sessionID, started.Version); err != nil {
		return nil, err
	}

	sub := &repository.AssignmentSubmissionRow{
		ID: NewID(), AssignmentID: req.AssignmentID, StudentUserID: req.UserID,
		SubmissionID: &firstSubID,
	}
	if err := a.s.Repo.CreateAssignmentSubmission(ctx, sub); err != nil {
		return nil, err
	}
	// 重新读取以带回 DB 侧默认值（grade_json/graded_at/created_at）
	saved, err := a.s.Repo.GetAssignmentSubmission(ctx, req.AssignmentID, req.UserID)
	if err != nil {
		return nil, err
	}
	if saved == nil {
		return nil, domain.NotFound("作业提交记录不存在")
	}
	a.s.audit(ctx, req.WorkspaceID, "assignment.submit", "assignment_submission", sub.ID,
		map[string]any{"assignment_id": req.AssignmentID, "answers": len(req.Answers)})
	return a.submissionFromRow(ctx, req.WorkspaceID, saved), nil
}

// AssignmentList 列出当前用户可见作业（教师=创建班级；学生=加入班级）。
func (a *AssignmentsService) AssignmentList(ctx context.Context, req AssignmentListReq) ([]*Assignment, error) {
	if err := a.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	rows, err := a.s.Repo.ListAssignmentsForUser(ctx, req.WorkspaceID, req.UserID)
	if err != nil {
		return nil, err
	}
	out := make([]*Assignment, 0, len(rows))
	for _, r := range rows {
		out = append(out, a.assignmentFromRow(r))
	}
	return out, nil
}

// AssignmentSubmissionList 列出作业提交名单（教师：班级创建者）。
func (a *AssignmentsService) AssignmentSubmissionList(ctx context.Context, req AssignmentSubmissionListReq) ([]*AssignmentSubmission, error) {
	if err := a.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	asg, err := a.s.Repo.GetAssignment(ctx, req.WorkspaceID, req.AssignmentID)
	if err != nil {
		return nil, err
	}
	if asg == nil {
		return nil, domain.NotFound("作业不存在")
	}
	if _, err := a.s.Classes.assertEditableClass(ctx, req.WorkspaceID, req.UserID, "assignment.submission_list", asg.ClassID); err != nil {
		return nil, err
	}
	rows, err := a.s.Repo.ListAssignmentSubmissions(ctx, req.AssignmentID)
	if err != nil {
		return nil, err
	}
	out := make([]*AssignmentSubmission, 0, len(rows))
	for _, r := range rows {
		out = append(out, a.submissionFromRow(ctx, req.WorkspaceID, r))
	}
	return out, nil
}

func (a *AssignmentsService) assignmentFromRow(r *repository.AssignmentRow) *Assignment {
	return &Assignment{
		ID: r.ID, ClassID: r.ClassID, PaperID: r.PaperID, Title: r.Title,
		DueAt: r.DueAt, GradingRule: r.GradingRule, Status: r.Status,
		Version: r.Version, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func (a *AssignmentsService) submissionFromRow(ctx context.Context, wsID string, r *repository.AssignmentSubmissionRow) *AssignmentSubmission {
	s := &AssignmentSubmission{
		ID: r.ID, AssignmentID: r.AssignmentID, StudentUserID: r.StudentUserID,
		SubmissionID: r.SubmissionID, GradeJSON: json.RawMessage(r.GradeJSON),
		GradedAt: r.GradedAt, CreatedAt: r.CreatedAt,
	}
	if u, err := a.s.Repo.GetUser(ctx, wsID, r.StudentUserID); err == nil && u != nil {
		s.DisplayName = u.DisplayName
	}
	return s
}
