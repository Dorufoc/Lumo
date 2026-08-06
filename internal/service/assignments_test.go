package service

import (
	"encoding/json"
	"testing"
	"time"

	"lumo/internal/domain"
)

// ---- 辅助 ----

// addClassStudent 教师把学生加入班级。
func addClassStudent(t *testing.T, s *Services, wsID, teacherID, classID, studentID string) {
	t.Helper()
	if _, err := s.Classes.ClassMemberAdd(ctx(), ClassMemberAddReq{
		WorkspaceID: wsID, UserID: teacherID, ClassID: classID,
		StudentUserID: studentID, IdempotencyKey: "cma-" + NewID(),
	}); err != nil {
		t.Fatalf("add member: %v", err)
	}
}

// publishPaper 创建并发布一张试卷（含给定题目）。
func publishPaper(t *testing.T, s *Services, wsID, userID, title string, qvIDs []string, durationMin int) *ExamPaper {
	t.Helper()
	paper, err := s.Exam.ExamPaperCreate(ctx(), ExamPaperCreateReq{
		WorkspaceID: wsID, UserID: userID, Title: title,
		ConfigJSON: examPaperConfig(durationMin, []map[string]any{
			examSection("第一部分", 1, qvIDs, 10),
		}),
		IdempotencyKey: "epc-" + NewID(),
	})
	if err != nil {
		t.Fatalf("create paper: %v", err)
	}
	paper, err = s.Exam.ExamPaperPublish(ctx(), ExamPaperPublishReq{
		WorkspaceID: wsID, PaperID: paper.ID, Version: paper.Version,
	})
	if err != nil {
		t.Fatalf("publish paper: %v", err)
	}
	return paper
}

// createAssignment 教师为班级创建作业（草稿态）。
func createAssignment(t *testing.T, s *Services, wsID, teacherID, classID, paperID, title, dueAt string) *Assignment {
	t.Helper()
	a, err := s.Assignments.AssignmentCreate(ctx(), AssignmentCreateReq{
		WorkspaceID: wsID, UserID: teacherID, ClassID: classID, PaperID: paperID,
		Title: title, DueAt: dueAt, GradingRule: domain.GradingRuleAuto,
		IdempotencyKey: "ac-" + NewID(),
	})
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}
	return a
}

// assignmentRowGradeJSON 直读 assignment_submissions.grade_json。
func assignmentRowGradeJSON(t *testing.T, s *Services, id string) string {
	t.Helper()
	var g string
	if err := s.Repo.DB().QueryRowContext(ctx(),
		`SELECT grade_json FROM assignment_submissions WHERE id = ?`, id).Scan(&g); err != nil {
		t.Fatalf("read grade_json: %v", err)
	}
	return g
}

// assignmentRowSubmissionID 直读 assignment_submissions.submission_id（可为 NULL）。
func assignmentRowSubmissionID(t *testing.T, s *Services, id string) *string {
	t.Helper()
	var sid *string
	if err := s.Repo.DB().QueryRowContext(ctx(),
		`SELECT submission_id FROM assignment_submissions WHERE id = ?`, id).Scan(&sid); err != nil {
		t.Fatalf("read submission_id: %v", err)
	}
	return sid
}

// ---- AssignmentCreate ----

func TestAssignmentCreateValidation(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	teacherID := createTeacher(t, s, ws.ID)
	studentID := createStudent(t, s, ws.ID)
	cls := createClass(t, s, ws.ID, teacherID)

	// 需要已发布试卷
	q := publishedQuestion(t, s, ws.ID, scPayload("作业题", "A"))
	draftPaper, err := s.Exam.ExamPaperCreate(ctx(), ExamPaperCreateReq{
		WorkspaceID: ws.ID, UserID: teacherID, Title: "未发布卷",
		ConfigJSON: examPaperConfig(30, []map[string]any{
			examSection("第一部分", 1, []string{q.CurrentVersion.ID}, 10),
		}),
		IdempotencyKey: "epc-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	published := publishPaper(t, s, ws.ID, teacherID, "已发布卷", []string{q.CurrentVersion.ID}, 30)

	cases := []struct {
		name string
		req  AssignmentCreateReq
		code domain.ErrorCode
	}{
		{"学生创建被拒", AssignmentCreateReq{WorkspaceID: ws.ID, UserID: studentID, ClassID: cls.ID, PaperID: published.ID, Title: "x", DueAt: "2026-08-10T00:00:00Z", IdempotencyKey: "ac-" + NewID()}, domain.CodeForbidden},
		{"非班级创建者教师被拒", AssignmentCreateReq{WorkspaceID: ws.ID, UserID: createTeacher(t, s, ws.ID), ClassID: cls.ID, PaperID: published.ID, Title: "x", DueAt: "2026-08-10T00:00:00Z", IdempotencyKey: "ac-" + NewID()}, domain.CodeForbidden},
		{"空标题", AssignmentCreateReq{WorkspaceID: ws.ID, UserID: teacherID, ClassID: cls.ID, PaperID: published.ID, Title: "", DueAt: "2026-08-10T00:00:00Z", IdempotencyKey: "ac-" + NewID()}, domain.CodeInvalidArgument},
		{"标题超长", AssignmentCreateReq{WorkspaceID: ws.ID, UserID: teacherID, ClassID: cls.ID, PaperID: published.ID, Title: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", DueAt: "2026-08-10T00:00:00Z", IdempotencyKey: "ac-" + NewID()}, domain.CodeInvalidArgument},
		{"非法判分方式", AssignmentCreateReq{WorkspaceID: ws.ID, UserID: teacherID, ClassID: cls.ID, PaperID: published.ID, Title: "x", DueAt: "2026-08-10T00:00:00Z", GradingRule: "bogus", IdempotencyKey: "ac-" + NewID()}, domain.CodeInvalidArgument},
		{"非法截止时间", AssignmentCreateReq{WorkspaceID: ws.ID, UserID: teacherID, ClassID: cls.ID, PaperID: published.ID, Title: "x", DueAt: "not-a-time", IdempotencyKey: "ac-" + NewID()}, domain.CodeInvalidArgument},
		{"未发布试卷被拒", AssignmentCreateReq{WorkspaceID: ws.ID, UserID: teacherID, ClassID: cls.ID, PaperID: draftPaper.ID, Title: "x", DueAt: "2026-08-10T00:00:00Z", IdempotencyKey: "ac-" + NewID()}, domain.CodeInvalidState},
		{"缺幂等键", AssignmentCreateReq{WorkspaceID: ws.ID, UserID: teacherID, ClassID: cls.ID, PaperID: published.ID, Title: "x", DueAt: "2026-08-10T00:00:00Z"}, domain.CodeInvalidArgument},
		{"班级不存在", AssignmentCreateReq{WorkspaceID: ws.ID, UserID: teacherID, ClassID: "no-class-1", PaperID: published.ID, Title: "x", DueAt: "2026-08-10T00:00:00Z", IdempotencyKey: "ac-" + NewID()}, domain.CodeNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Assignments.AssignmentCreate(ctx(), tc.req)
			de := domain.AsError(err)
			if de == nil || de.Code != tc.code {
				t.Fatalf("expected %s, got %v", tc.code, err)
			}
		})
	}
}

func TestAssignmentCreateHappyAndIdempotency(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	teacherID := createTeacher(t, s, ws.ID)
	cls := createClass(t, s, ws.ID, teacherID)
	q := publishedQuestion(t, s, ws.ID, scPayload("作业题", "A"))
	paper := publishPaper(t, s, ws.ID, teacherID, "已发布卷", []string{q.CurrentVersion.ID}, 30)

	key := "ac-" + NewID()
	req := AssignmentCreateReq{
		WorkspaceID: ws.ID, UserID: teacherID, ClassID: cls.ID, PaperID: paper.ID,
		Title: "第五章作业", DueAt: "2026-08-10T00:00:00Z",
		GradingRule: domain.GradingRuleAuto, IdempotencyKey: key,
	}
	a, err := s.Assignments.AssignmentCreate(ctx(), req)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if a.Status != domain.AssignmentStatusDraft || a.Version != 1 || a.ClassID != cls.ID || a.PaperID != paper.ID {
		t.Fatalf("unexpected assignment: %+v", a)
	}
	// 同一幂等键重放 → 同一作业
	b, err := s.Assignments.AssignmentCreate(ctx(), req)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if a.ID != b.ID {
		t.Fatalf("idempotency violated: %s != %s", a.ID, b.ID)
	}
	// 审计只记一次
	if n := classAuditCount(t, s, "assignment.create"); n != 1 {
		t.Fatalf("expected 1 audit, got %d", n)
	}
}

// ---- AssignmentPublish ----

func TestAssignmentPublishOptimisticLock(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	teacherID := createTeacher(t, s, ws.ID)
	cls := createClass(t, s, ws.ID, teacherID)
	q := publishedQuestion(t, s, ws.ID, scPayload("作业题", "A"))
	paper := publishPaper(t, s, ws.ID, teacherID, "已发布卷", []string{q.CurrentVersion.ID}, 30)
	asg := createAssignment(t, s, ws.ID, teacherID, cls.ID, paper.ID, "第五章作业", "2026-08-10T00:00:00Z")

	// 发布成功：draft→published，version 1→2
	pub, err := s.Assignments.AssignmentPublish(ctx(), AssignmentPublishReq{
		WorkspaceID: ws.ID, UserID: teacherID, AssignmentID: asg.ID, Version: asg.Version,
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if pub.Status != domain.AssignmentStatusPublished || pub.Version != 2 {
		t.Fatalf("unexpected published: %+v", pub)
	}

	// 陈旧版本 → CONFLICT
	_, err = s.Assignments.AssignmentPublish(ctx(), AssignmentPublishReq{
		WorkspaceID: ws.ID, UserID: teacherID, AssignmentID: asg.ID, Version: asg.Version,
	})
	if de := domain.AsError(err); de == nil || de.Code != domain.CodeConflict {
		t.Fatalf("expected CONFLICT on stale version, got %v", err)
	}

	// 重复发布（当前版本）→ INVALID_STATE
	_, err = s.Assignments.AssignmentPublish(ctx(), AssignmentPublishReq{
		WorkspaceID: ws.ID, UserID: teacherID, AssignmentID: asg.ID, Version: pub.Version,
	})
	if de := domain.AsError(err); de == nil || de.Code != domain.CodeInvalidState {
		t.Fatalf("expected INVALID_STATE on republish, got %v", err)
	}
}

func TestAssignmentPublishPermission(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	teacherID := createTeacher(t, s, ws.ID)
	studentID := createStudent(t, s, ws.ID)
	otherTeacher := createTeacher(t, s, ws.ID)
	cls := createClass(t, s, ws.ID, teacherID)
	q := publishedQuestion(t, s, ws.ID, scPayload("作业题", "A"))
	paper := publishPaper(t, s, ws.ID, teacherID, "已发布卷", []string{q.CurrentVersion.ID}, 30)
	asg := createAssignment(t, s, ws.ID, teacherID, cls.ID, paper.ID, "第五章作业", "2026-08-10T00:00:00Z")

	// 学生发布 → FORBIDDEN
	_, err := s.Assignments.AssignmentPublish(ctx(), AssignmentPublishReq{
		WorkspaceID: ws.ID, UserID: studentID, AssignmentID: asg.ID, Version: asg.Version,
	})
	isForbidden(t, err)
	// 非班级创建者教师 → FORBIDDEN
	_, err = s.Assignments.AssignmentPublish(ctx(), AssignmentPublishReq{
		WorkspaceID: ws.ID, UserID: otherTeacher, AssignmentID: asg.ID, Version: asg.Version,
	})
	isForbidden(t, err)
}

// ---- AssignmentSubmit ----

// baseSubmitSetup 返回一套完整前置：工作区/教师/学生/班级/已发布作业 + 一道已发布题目的版本 ID。
func baseSubmitSetup(t *testing.T, s *Services) (ws *Workspace, teacherID, studentID, classID, paperID, assignmentID, qvid string) {
	t.Helper()
	ws, _ = createWorkspace(t, s)
	teacherID = createTeacher(t, s, ws.ID)
	studentID = createStudent(t, s, ws.ID)
	cls := createClass(t, s, ws.ID, teacherID)
	addClassStudent(t, s, ws.ID, teacherID, cls.ID, studentID)
	q := publishedQuestion(t, s, ws.ID, scPayload("作业题", "A"))
	qvid = q.CurrentVersion.ID
	paper := publishPaper(t, s, ws.ID, teacherID, "已发布卷", []string{qvid}, 30)
	asg := createAssignment(t, s, ws.ID, teacherID, cls.ID, paper.ID, "第五章作业", "2026-08-10T00:00:00Z")
	pub, err := s.Assignments.AssignmentPublish(ctx(), AssignmentPublishReq{
		WorkspaceID: ws.ID, UserID: teacherID, AssignmentID: asg.ID, Version: asg.Version,
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	return ws, teacherID, studentID, cls.ID, paper.ID, pub.ID, qvid
}

func TestAssignmentSubmitHappy(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _, studentID, _, _, assignmentID, qvid := baseSubmitSetup(t, s)

	sub, err := s.Assignments.AssignmentSubmit(ctx(), AssignmentSubmitReq{
		WorkspaceID: ws.ID, UserID: studentID, AssignmentID: assignmentID,
		Answers:        []AssignmentAnswer{{QuestionVersionID: qvid, Answer: json.RawMessage(`"A"`)}},
		IdempotencyKey: "as-" + NewID(),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	// grade_json 空态
	if g := assignmentRowGradeJSON(t, s, sub.ID); g != "{}" {
		t.Fatalf("expected empty grade_json, got %s", g)
	}
	// submission_id 非空（FK 有效）
	sid := assignmentRowSubmissionID(t, s, sub.ID)
	if sid == nil || *sid == "" {
		t.Fatal("submission_id should be set after submit")
	}
	// 关联的 submissions 行状态为 submitted
	var status string
	if err := s.Repo.DB().QueryRowContext(ctx(),
		`SELECT status FROM submissions WHERE id = ?`, *sid).Scan(&status); err != nil {
		t.Fatalf("read submission: %v", err)
	}
	if status != "submitted" {
		t.Fatalf("expected submitted submission, got %s", status)
	}
}

func TestAssignmentSubmitValidation(t *testing.T) {
	s, _ := newTestServices(t)
	ws, teacherID, studentID, classID, paperID, assignmentID, qvid := baseSubmitSetup(t, s)

	// 非成员学生 → FORBIDDEN
	outsider := createStudent(t, s, ws.ID)
	_, err := s.Assignments.AssignmentSubmit(ctx(), AssignmentSubmitReq{
		WorkspaceID: ws.ID, UserID: outsider, AssignmentID: assignmentID,
		Answers:        []AssignmentAnswer{{QuestionVersionID: qvid, Answer: json.RawMessage(`"A"`)}},
		IdempotencyKey: "as-" + NewID(),
	})
	isForbidden(t, err)

	// 教师提交 → FORBIDDEN（非成员）
	_, err = s.Assignments.AssignmentSubmit(ctx(), AssignmentSubmitReq{
		WorkspaceID: ws.ID, UserID: teacherID, AssignmentID: assignmentID,
		Answers:        []AssignmentAnswer{{QuestionVersionID: qvid, Answer: json.RawMessage(`"A"`)}},
		IdempotencyKey: "as-" + NewID(),
	})
	isForbidden(t, err)

	// 缺幂等键
	_, err = s.Assignments.AssignmentSubmit(ctx(), AssignmentSubmitReq{
		WorkspaceID: ws.ID, UserID: studentID, AssignmentID: assignmentID,
		Answers: []AssignmentAnswer{{QuestionVersionID: qvid, Answer: json.RawMessage(`"A"`)}},
	})
	if de := domain.AsError(err); de == nil || de.Code != domain.CodeInvalidArgument {
		t.Fatalf("expected INVALID_ARGUMENT for missing idem key, got %v", err)
	}

	// 作业不存在 → NOT_FOUND
	_, err = s.Assignments.AssignmentSubmit(ctx(), AssignmentSubmitReq{
		WorkspaceID: ws.ID, UserID: studentID, AssignmentID: "no-assignment-1",
		Answers:        []AssignmentAnswer{{QuestionVersionID: qvid, Answer: json.RawMessage(`"A"`)}},
		IdempotencyKey: "as-" + NewID(),
	})
	if de := domain.AsError(err); de == nil || de.Code != domain.CodeNotFound {
		t.Fatalf("expected NOT_FOUND, got %v", err)
	}

	// 未发布作业 → INVALID_STATE
	draftAsg := createAssignment(t, s, ws.ID, teacherID, classID, paperID, "未发布作业", "2026-08-10T00:00:00Z")
	_, err = s.Assignments.AssignmentSubmit(ctx(), AssignmentSubmitReq{
		WorkspaceID: ws.ID, UserID: studentID, AssignmentID: draftAsg.ID,
		Answers:        []AssignmentAnswer{{QuestionVersionID: qvid, Answer: json.RawMessage(`"A"`)}},
		IdempotencyKey: "as-" + NewID(),
	})
	if de := domain.AsError(err); de == nil || de.Code != domain.CodeInvalidState {
		t.Fatalf("expected INVALID_STATE for unpublished assignment, got %v", err)
	}
}

func TestAssignmentSubmitAfterDue(t *testing.T) {
	s, _ := newTestServices(t)
	ws, teacherID, studentID, classID, _, _, _ := baseSubmitSetup(t, s)

	// 截止时间在过去（复用与 baseSubmitSetup 不同的题目避免内容去重冲突）
	t0 := examFixedTime("2026-08-06T10:00:00Z")
	s.Assignments.Now = func() time.Time { return t0 }
	q := publishedQuestion(t, s, ws.ID, scPayload("截止作业题", "A"))
	paper := publishPaper(t, s, ws.ID, teacherID, "已发布卷", []string{q.CurrentVersion.ID}, 30)
	asg := createAssignment(t, s, ws.ID, teacherID, classID, paper.ID, "已截止作业", "2026-08-01T00:00:00Z")
	pub, err := s.Assignments.AssignmentPublish(ctx(), AssignmentPublishReq{
		WorkspaceID: ws.ID, UserID: teacherID, AssignmentID: asg.ID, Version: asg.Version,
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	_, err = s.Assignments.AssignmentSubmit(ctx(), AssignmentSubmitReq{
		WorkspaceID: ws.ID, UserID: studentID, AssignmentID: pub.ID,
		Answers:        []AssignmentAnswer{{QuestionVersionID: q.CurrentVersion.ID, Answer: json.RawMessage(`"A"`)}},
		IdempotencyKey: "as-" + NewID(),
	})
	if de := domain.AsError(err); de == nil || de.Code != domain.CodeInvalidArgument {
		t.Fatalf("expected INVALID_ARGUMENT after due_at, got %v", err)
	}
}

func TestAssignmentSubmitDoubleSubmitConflict(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _, studentID, _, _, assignmentID, qvid := baseSubmitSetup(t, s)

	key := "as-" + NewID()
	req := AssignmentSubmitReq{
		WorkspaceID: ws.ID, UserID: studentID, AssignmentID: assignmentID,
		Answers:        []AssignmentAnswer{{QuestionVersionID: qvid, Answer: json.RawMessage(`"A"`)}},
		IdempotencyKey: key,
	}
	first, err := s.Assignments.AssignmentSubmit(ctx(), req)
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}
	// 同一幂等键重放 → 同一条提交记录
	replay, err := s.Assignments.AssignmentSubmit(ctx(), req)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if first.ID != replay.ID {
		t.Fatalf("idempotency violated: %s != %s", first.ID, replay.ID)
	}
	// 不同幂等键重复提交 → CONFLICT
	_, err = s.Assignments.AssignmentSubmit(ctx(), AssignmentSubmitReq{
		WorkspaceID: ws.ID, UserID: studentID, AssignmentID: assignmentID,
		Answers:        []AssignmentAnswer{{QuestionVersionID: qvid, Answer: json.RawMessage(`"A"`)}},
		IdempotencyKey: "as2-" + NewID(),
	})
	if de := domain.AsError(err); de == nil || de.Code != domain.CodeConflict {
		t.Fatalf("expected CONFLICT on duplicate submit, got %v", err)
	}
	// 审计只记一次提交
	if n := classAuditCount(t, s, "assignment.submit"); n != 1 {
		t.Fatalf("expected 1 submit audit, got %d", n)
	}
}

// ---- AssignmentList ----

func TestAssignmentListVisibility(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	teacherID := createTeacher(t, s, ws.ID)
	studentID := createStudent(t, s, ws.ID)
	otherTeacher := createTeacher(t, s, ws.ID)
	cls := createClass(t, s, ws.ID, teacherID)
	otherCls := createClass(t, s, ws.ID, otherTeacher)

	q := publishedQuestion(t, s, ws.ID, scPayload("作业题", "A"))
	paper := publishPaper(t, s, ws.ID, teacherID, "已发布卷", []string{q.CurrentVersion.ID}, 30)
	asg := createAssignment(t, s, ws.ID, teacherID, cls.ID, paper.ID, "第五章作业", "2026-08-10T00:00:00Z")
	// 其他教师班级的作业（学生不可见）
	createAssignment(t, s, ws.ID, otherTeacher, otherCls.ID, paper.ID, "他人作业", "2026-08-10T00:00:00Z")

	// 学生未加入任何班级 → 空
	list, err := s.Assignments.AssignmentList(ctx(), AssignmentListReq{WorkspaceID: ws.ID, UserID: studentID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("student should see no assignments before joining, got %d", len(list))
	}

	// 教师可见自己班级作业
	teacherList, err := s.Assignments.AssignmentList(ctx(), AssignmentListReq{WorkspaceID: ws.ID, UserID: teacherID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(teacherList) != 1 || teacherList[0].ID != asg.ID {
		t.Fatalf("teacher sees %+v, want own assignment", teacherList)
	}

	// 学生加入后可见
	addClassStudent(t, s, ws.ID, teacherID, cls.ID, studentID)
	studentList, err := s.Assignments.AssignmentList(ctx(), AssignmentListReq{WorkspaceID: ws.ID, UserID: studentID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(studentList) != 1 || studentList[0].ID != asg.ID {
		t.Fatalf("student sees %+v, want joined assignment", studentList)
	}
}

// ---- AssignmentSubmissionList ----

func TestAssignmentSubmissionListTeacherView(t *testing.T) {
	s, _ := newTestServices(t)
	ws, teacherID, studentID, _, _, assignmentID, qvid := baseSubmitSetup(t, s)

	// 学生提交后教师可见名单
	if _, err := s.Assignments.AssignmentSubmit(ctx(), AssignmentSubmitReq{
		WorkspaceID: ws.ID, UserID: studentID, AssignmentID: assignmentID,
		Answers:        []AssignmentAnswer{{QuestionVersionID: qvid, Answer: json.RawMessage(`"A"`)}},
		IdempotencyKey: "as-" + NewID(),
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	list, err := s.Assignments.AssignmentSubmissionList(ctx(), AssignmentSubmissionListReq{
		WorkspaceID: ws.ID, UserID: teacherID, AssignmentID: assignmentID,
	})
	if err != nil {
		t.Fatalf("list submissions: %v", err)
	}
	if len(list) != 1 || list[0].StudentUserID != studentID || list[0].DisplayName == "" {
		t.Fatalf("unexpected submission list: %+v", list)
	}

	// 学生无权看名单 → FORBIDDEN
	_, err = s.Assignments.AssignmentSubmissionList(ctx(), AssignmentSubmissionListReq{
		WorkspaceID: ws.ID, UserID: studentID, AssignmentID: assignmentID,
	})
	isForbidden(t, err)
}
