package service

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"lumo/internal/agent"
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
	// Submission 是当前用户的提交记录（学生可见得分；教师为 nil）。
	Submission *AssignmentSubmission `json:"submission,omitempty"`
	// Appeal 是当前学生对该作业提交的申诉（学生视角；无申诉为 nil）。
	Appeal *Appeal `json:"appeal,omitempty"`
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
	// SessionID 提交所在练习会话（由 submission_id 反查），教师批阅时据此取作答。
	SessionID *string `json:"session_id,omitempty"`
	// Message 预批提示（EssayGrader 降级等），非预批调用为空。
	Message string `json:"message,omitempty"`
}

// AssignmentGradeReq 批阅作业请求（教师，班级创建者）。
// Version 为当前 grade_json.version 乐观锁版本；PreGrade=true 时仅 EssayGrader 预批预览（不落库）。
type AssignmentGradeReq struct {
	WorkspaceID  string          `json:"workspace_id"`
	UserID       string          `json:"user_id"`
	SubmissionID string          `json:"submission_id"`
	GradeJSON    json.RawMessage `json:"grade_json,omitempty"`
	Version      int             `json:"version"`
	PreGrade     bool            `json:"pre_grade,omitempty"`
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
	// auto 规则：客观题提交即自动判分（grade_json 空态 '{}' 视为 version=0，写入后 version=1）
	if asg.GradingRule == domain.GradingRuleAuto {
		gradeJSON, err := a.autoGradeOnSubmit(ctx, req.Answers, snapshot)
		if err != nil {
			return nil, err
		}
		if err := a.s.Repo.UpdateAssignmentGrade(ctx, saved.ID, gradeJSON, 0); err != nil {
			return nil, err
		}
		// 重新读取以带回 graded_at 与最新 grade_json
		saved, err = a.s.Repo.GetAssignmentSubmission(ctx, req.AssignmentID, req.UserID)
		if err != nil {
			return nil, err
		}
	}
	a.s.audit(ctx, req.WorkspaceID, "assignment.submit", "assignment_submission", sub.ID,
		map[string]any{"assignment_id": req.AssignmentID, "answers": len(req.Answers)})
	return a.submissionFromRow(ctx, req.WorkspaceID, saved), nil
}

// autoGradeOnSubmit auto 规则判分：客观题（单选/多选/填空）规则引擎判分，主观题标 pending 待教师复核。
// 返回 grade_json 文本（version=1）。判分异常（如答案格式非法）按 0 分处理，不阻断提交。
func (a *AssignmentsService) autoGradeOnSubmit(ctx context.Context, answers []AssignmentAnswer, snapshot []*PracticeQuestion) (string, error) {
	maxByQVID := map[string]float64{}
	for _, q := range snapshot {
		maxByQVID[q.QuestionVersionID] = q.MaxScore
	}
	items := make([]map[string]any, 0, len(answers))
	overall := 0.0
	for _, ans := range answers {
		item := map[string]any{
			"question_version_id": ans.QuestionVersionID,
			"max_score":           maxByQVID[ans.QuestionVersionID],
			"score":               0,
			"status":              "pending",
			"comment":             "待教师复核",
		}
		v, err := a.s.Repo.GetQuestionVersion(ctx, ans.QuestionVersionID)
		if err != nil {
			return "", err
		}
		if v != nil {
			payload, err := parseQuestionPayload(v.Payload)
			if err != nil {
				return "", err
			}
			item["type"] = payload.Type
			switch payload.Type {
			case "single_choice", "multiple_choice", "fill_blank":
				gr, gerr := GradeObjective(payload, ans.Answer, maxByQVID[ans.QuestionVersionID])
				score := 0.0
				reason := "答案格式非法或未作答"
				if gerr == nil {
					score = gr.Score
					reason = gr.Reason
				}
				item["score"] = score
				item["status"] = "graded"
				item["comment"] = reason
				overall += score
			}
		}
		items = append(items, item)
	}
	g := map[string]any{"version": 1, "items": items, "overall": overall, "comment": ""}
	b, err := json.Marshal(g)
	if err != nil {
		return "", err
	}
	return string(b), nil
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
		asg := a.assignmentFromRow(r)
		// 学生附带本人提交（可见批阅状态/得分）；教师无提交记录，保持 nil
		if s, err := a.s.Repo.GetAssignmentSubmission(ctx, r.ID, req.UserID); err != nil {
			return nil, err
		} else if s != nil {
			asg.Submission = a.submissionFromRow(ctx, req.WorkspaceID, s)
			// 学生视角附带其申诉（教师端由 AppealList 单独列出）；grading_id 即提交 id
			if ap, err := a.s.Repo.GetAppealByGrading(ctx, s.ID); err != nil {
				return nil, err
			} else if ap != nil {
				asg.Appeal = a.s.Appeals.appealFromRow(ap)
			}
		}
		out = append(out, asg)
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

// AssignmentGrade 批阅作业（教师，班级创建者；乐观锁版本号写入）。
// 非预批调用：校验提交归属作业、教师权限、grade_json 合法性；以 req.Version 为乐观锁
// 原子更新（0 rows 时仓库区分 NOT_FOUND / CONFLICT），成功后版本 +1 并回读最新记录。
func (a *AssignmentsService) AssignmentGrade(ctx context.Context, req AssignmentGradeReq) (*AssignmentSubmission, error) {
	if err := a.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	sub, err := a.s.Repo.GetAssignmentSubmissionByID(ctx, req.SubmissionID)
	if err != nil {
		return nil, err
	}
	if sub == nil {
		return nil, domain.NotFound("作业提交不存在")
	}
	asg, err := a.s.Repo.GetAssignment(ctx, req.WorkspaceID, sub.AssignmentID)
	if err != nil {
		return nil, err
	}
	if asg == nil {
		return nil, domain.NotFound("作业不存在")
	}
	// 仅班级创建者可批阅（学生/非创建者教师 → FORBIDDEN）
	if _, err := a.s.Classes.assertEditableClass(ctx, req.WorkspaceID, req.UserID, "assignment.grade", asg.ClassID); err != nil {
		return nil, err
	}
	// 预批（EssayGrader）：AI 评分预览，不落库、不改版本
	if req.PreGrade {
		return a.preGrade(ctx, req.WorkspaceID, req.UserID, sub)
	}
	if len(req.GradeJSON) == 0 || !json.Valid(req.GradeJSON) {
		return nil, domain.InvalidArg("grade_json 必填且须为合法 JSON")
	}
	// 注入版本号：仅接受不含 version 的提交，由服务端写入 req.Version+1
	var g map[string]any
	if err := json.Unmarshal(req.GradeJSON, &g); err != nil {
		return nil, domain.InvalidArg("grade_json 须为合法 JSON 对象")
	}
	if _, ok := g["version"]; ok {
		return nil, domain.InvalidArg("grade_json 不应包含 version 字段（服务端维护）")
	}
	g["version"] = req.Version + 1
	next, err := json.Marshal(g)
	if err != nil {
		return nil, domain.InvalidArg("grade_json 序列化失败")
	}
	if err := a.s.Repo.UpdateAssignmentGrade(ctx, sub.ID, string(next), req.Version); err != nil {
		return nil, err
	}
	// 回读最新记录（graded_at / grade_json 由 DB 侧写入）
	saved, err := a.s.Repo.GetAssignmentSubmissionByID(ctx, sub.ID)
	if err != nil {
		return nil, err
	}
	if saved == nil {
		return nil, domain.NotFound("作业提交记录不存在")
	}
	a.s.audit(ctx, req.WorkspaceID, "assignment.grade", "assignment_submission", sub.ID,
		map[string]any{"assignment_id": sub.AssignmentID, "version": req.Version + 1})
	// 复议联动：已接受的申诉在教师重新批阅后 → resolved（改分完成）。
	if ap, err := a.s.Repo.GetAppealByGrading(ctx, sub.ID); err == nil && ap != nil && ap.Status == domain.AppealStatusAccepted {
		if err := a.s.Repo.UpdateAppealStatus(ctx, ap.ID, domain.AppealStatusAccepted, domain.AppealStatusResolved, ap.TeacherNote); err == nil {
			if saved, err := a.s.Repo.GetAppeal(ctx, ap.ID); err == nil && saved != nil {
				_ = a.s.UserEvents.Publish(ap.StudentUserID, agent.Event{
					Name: agent.EventGradingAppeal,
					Payload: map[string]any{
						"appeal_id": saved.ID, "grading_id": saved.GradingID, "status": saved.Status,
						"ref_type": "grading_appeal", "ref_id": saved.ID,
					},
				})
			}
		}
	}
	return a.submissionFromRow(ctx, req.WorkspaceID, saved), nil
}

// preGrade EssayGrader 预批预览：对主观题（简答/代码）调用 AI 评分，返回提示而不落库。
// 未配置 Provider → 降级 Message 提示（非阻断）；返回当前提交（版本不变）。
func (a *AssignmentsService) preGrade(ctx context.Context, wsID, userID string, sub *repository.AssignmentSubmissionRow) (*AssignmentSubmission, error) {
	out := a.submissionFromRow(ctx, wsID, sub)
	if sub.SubmissionID == nil {
		out.Message = "该提交未包含作答内容，无法预批"
		return out, nil
	}
	firstSub, err := a.s.Repo.GetSubmission(ctx, *sub.SubmissionID)
	if err != nil {
		return nil, err
	}
	if firstSub == nil {
		out.Message = "该提交未包含作答内容，无法预批"
		return out, nil
	}
	// 依据首条 submissions 行还原会话，取出全部作答
	rows, err := a.s.Repo.ListSubmissions(ctx, firstSub.SessionID)
	if err != nil {
		return nil, err
	}
	var msgParts []string
	for _, r := range rows {
		v, err := a.s.Repo.GetQuestionVersion(ctx, r.QuestionVersionID)
		if err != nil {
			return nil, err
		}
		if v == nil {
			continue
		}
		payload, err := parseQuestionPayload(v.Payload)
		if err != nil {
			return nil, err
		}
		// 仅主观题走 EssayGrader；客观题已可规则判分
		if payload.Type != "short_answer" && payload.Type != "code" {
			continue
		}
		var essay string
		if err := json.Unmarshal(r.Answer, &essay); err != nil {
			essay = string(r.Answer)
		}
		rubric := ""
		if len(payload.Rubric) > 0 {
			if b, err := json.Marshal(payload.Rubric); err == nil {
				rubric = string(b)
			}
		}
		res, err := a.s.AgentTasks.AgentEssayGrade(ctx, AgentEssayGradeReq{
			WorkspaceID: wsID, UserID: userID,
			Stem: payload.Stem, Rubric: rubric, Essay: essay,
			MaxScore: gradingMaxScore(payload, 10), IdempotencyKey: "pg-" + NewID(),
		})
		if err != nil {
			msgParts = append(msgParts, "题目 "+payload.Type+" 预批失败："+err.Error())
			continue
		}
		if res.Degraded {
			msgParts = append(msgParts, res.Message)
		} else if res.Result != nil {
			msgParts = append(msgParts, "AI 预批得分 "+strconv.FormatFloat(res.Result.OverallScore, 'f', 1, 64)+
				"/"+strconv.FormatFloat(gradingMaxScore(payload, 10), 'f', 1, 64))
		}
	}
	if len(msgParts) == 0 {
		out.Message = "该提交无主观题，无需预批"
	} else {
		out.Message = strings.Join(msgParts, "；")
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
	if r.SubmissionID != nil {
		if first, err := a.s.Repo.GetSubmission(ctx, *r.SubmissionID); err == nil && first != nil {
			s.SessionID = &first.SessionID
		}
	}
	if u, err := a.s.Repo.GetUser(ctx, wsID, r.StudentUserID); err == nil && u != nil {
		s.DisplayName = u.DisplayName
	}
	return s
}
