package service

import (
	"encoding/json"
	"testing"
	"time"

	"lumo/internal/agent"
	"lumo/internal/domain"
)

// ---- Todo 24 申诉 Appeals ----

// appealRowStatus 直接读 grading_appeals.status。
func appealRowStatus(t *testing.T, s *Services, appealID string) string {
	t.Helper()
	var st string
	if err := s.Repo.DB().QueryRowContext(ctx(),
		`SELECT status FROM grading_appeals WHERE id = ?`, appealID).Scan(&st); err != nil {
		t.Fatalf("read appeal status: %v", err)
	}
	return st
}

// appealAnchorCount 统计某 grading_id 的 synthetic grading_results 锚点行数。
func appealAnchorCount(t *testing.T, s *Services, gradingID string) int {
	t.Helper()
	var n int
	if err := s.Repo.DB().QueryRowContext(ctx(),
		`SELECT COUNT(*) FROM grading_results WHERE id = ? AND status = 'pending' AND method = 'manual'`,
		gradingID).Scan(&n); err != nil {
		t.Fatalf("count anchor rows: %v", err)
	}
	return n
}

func TestAppealCreateHappy(t *testing.T) {
	s, _ := newTestServices(t)
	ws, teacherID, studentID, _, _, assignmentID, qvid := baseSubmitSetup(t, s)
	sub := submitAssignment(t, s, ws.ID, studentID, assignmentID, qvid, json.RawMessage(`"A"`))

	// 教师订阅事件，验证能收到 grading:appeal
	evCh, cancel := s.UserEvents.SubscribeUser(teacherID)
	defer cancel()

	got, err := s.Appeals.AppealCreate(ctx(), AppealCreateReq{
		WorkspaceID: ws.ID, UserID: studentID, GradingID: sub.ID, Reason: "对第一题得分有疑问",
	})
	if err != nil {
		t.Fatalf("appeal create: %v", err)
	}
	if got.Status != domain.AppealStatusPending {
		t.Fatalf("expected pending, got %s", got.Status)
	}
	if got.GradingID != sub.ID || got.StudentUserID != studentID {
		t.Fatalf("appeal fields wrong: %+v", got)
	}
	if got.Reason != "对第一题得分有疑问" {
		t.Fatalf("reason not preserved: %q", got.Reason)
	}
	// 锚点 grading_results 行已生成（FK 支撑）
	if n := appealAnchorCount(t, s, sub.ID); n != 1 {
		t.Fatalf("expected 1 anchor row, got %d", n)
	}
	// 教师收到 grading:appeal 事件
	select {
	case ev := <-evCh:
		if ev.Name != agent.EventGradingAppeal {
			t.Fatalf("expected grading:appeal event, got %s", ev.Name)
		}
		if ev.Payload["appeal_id"] != got.ID || ev.Payload["grading_id"] != sub.ID || ev.Payload["status"] != "pending" {
			t.Fatalf("event payload wrong: %+v", ev.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no grading:appeal event received")
	}
	// 教师通知已落库
	if n := reminderNotificationCount(t, s, teacherID); n < 1 {
		t.Fatalf("expected teacher notification, got %d", n)
	}
}

func TestAppealCreateValidation(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _, studentID, _, _, assignmentID, qvid := baseSubmitSetup(t, s)
	sub := submitAssignment(t, s, ws.ID, studentID, assignmentID, qvid, json.RawMessage(`"A"`))

	cases := []struct {
		name string
		req  AppealCreateReq
		code domain.ErrorCode
	}{
		{"empty reason", AppealCreateReq{WorkspaceID: ws.ID, UserID: studentID, GradingID: sub.ID, Reason: "  "}, domain.CodeInvalidArgument},
		{"long reason", AppealCreateReq{WorkspaceID: ws.ID, UserID: studentID, GradingID: sub.ID, Reason: string(make([]byte, 2001))}, domain.CodeInvalidArgument},
		{"empty grading_id", AppealCreateReq{WorkspaceID: ws.ID, UserID: studentID, GradingID: "", Reason: "有疑问"}, domain.CodeInvalidArgument},
		{"unknown grading_id", AppealCreateReq{WorkspaceID: ws.ID, UserID: studentID, GradingID: NewID(), Reason: "有疑问"}, domain.CodeNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Appeals.AppealCreate(ctx(), tc.req)
			de := domain.AsError(err)
			if de == nil || de.Code != tc.code {
				t.Fatalf("expected %s, got %v", tc.code, err)
			}
		})
	}
}

func TestAppealCreateNotGraded(t *testing.T) {
	s, _ := newTestServices(t)
	ws, teacherID, studentID, classID, paperID, _, qvid := baseSubmitSetup(t, s)
	asg := createAssignmentRule(t, s, ws.ID, teacherID, classID, paperID, "手工批改作业", "2026-08-10T00:00:00Z", domain.GradingRuleTeacher)
	asg = publishAssignment(t, s, ws.ID, teacherID, asg)
	sub := submitAssignment(t, s, ws.ID, studentID, asg.ID, qvid, json.RawMessage(`"A"`))

	_, err := s.Appeals.AppealCreate(ctx(), AppealCreateReq{
		WorkspaceID: ws.ID, UserID: studentID, GradingID: sub.ID, Reason: "有疑问",
	})
	de := domain.AsError(err)
	if de == nil || de.Code != domain.CodeInvalidState {
		t.Fatalf("expected INVALID_STATE, got %v", err)
	}
}

func TestAppealCreatePermission(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _, studentID, _, _, assignmentID, qvid := baseSubmitSetup(t, s)
	sub := submitAssignment(t, s, ws.ID, studentID, assignmentID, qvid, json.RawMessage(`"A"`))
	outsider := createStudent(t, s, ws.ID)

	_, err := s.Appeals.AppealCreate(ctx(), AppealCreateReq{
		WorkspaceID: ws.ID, UserID: outsider, GradingID: sub.ID, Reason: "有疑问",
	})
	isForbidden(t, err)
}

func TestAppealCreateDuplicateConflict(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _, studentID, _, _, assignmentID, qvid := baseSubmitSetup(t, s)
	sub := submitAssignment(t, s, ws.ID, studentID, assignmentID, qvid, json.RawMessage(`"A"`))
	if _, err := s.Appeals.AppealCreate(ctx(), AppealCreateReq{
		WorkspaceID: ws.ID, UserID: studentID, GradingID: sub.ID, Reason: "第一次申诉",
	}); err != nil {
		t.Fatalf("first appeal: %v", err)
	}
	_, err := s.Appeals.AppealCreate(ctx(), AppealCreateReq{
		WorkspaceID: ws.ID, UserID: studentID, GradingID: sub.ID, Reason: "重复申诉",
	})
	de := domain.AsError(err)
	if de == nil || de.Code != domain.CodeConflict {
		t.Fatalf("expected CONFLICT, got %v", err)
	}
}

// TestAppealResolveAcceptedNewScore 复议改分：accept + new_score → grade_json version+1，状态 resolved。
func TestAppealResolveAcceptedNewScore(t *testing.T) {
	s, _ := newTestServices(t)
	ws, teacherID, studentID, _, _, assignmentID, qvid := baseSubmitSetup(t, s)
	sub := submitAssignment(t, s, ws.ID, studentID, assignmentID, qvid, json.RawMessage(`"A"`))
	ap, err := s.Appeals.AppealCreate(ctx(), AppealCreateReq{
		WorkspaceID: ws.ID, UserID: studentID, GradingID: sub.ID, Reason: "对第一题得分有疑问",
	})
	if err != nil {
		t.Fatalf("appeal create: %v", err)
	}

	evCh, cancel := s.UserEvents.SubscribeUser(studentID)
	defer cancel()

	newScore := 8.0
	got, err := s.Appeals.AppealResolve(ctx(), AppealResolveReq{
		WorkspaceID: ws.ID, UserID: teacherID, AppealID: ap.ID,
		Decision: domain.AppealDecisionAccepted, NewScore: &newScore, TeacherNote: "同意复议，改为8分",
	})
	if err != nil {
		t.Fatalf("appeal resolve: %v", err)
	}
	if got.Status != domain.AppealStatusResolved {
		t.Fatalf("expected resolved, got %s", got.Status)
	}
	if got.TeacherNote != "同意复议，改为8分" {
		t.Fatalf("teacher_note not preserved: %q", got.TeacherNote)
	}
	// grade_json 从 version 1 → 2，overall=8
	raw := assignmentRowGradeJSON(t, s, sub.ID)
	if v := gradeJSONVersion(t, raw); v != 2 {
		t.Fatalf("expected grade_json version 2, got %d (raw=%s)", v, raw)
	}
	var g struct {
		Overall float64 `json:"overall"`
	}
	if err := json.Unmarshal([]byte(raw), &g); err != nil {
		t.Fatalf("parse grade_json: %v", err)
	}
	if g.Overall != newScore {
		t.Fatalf("expected overall %v, got %v", newScore, g.Overall)
	}
	// 学生收到 resolved 事件 + 通知
	select {
	case ev := <-evCh:
		if ev.Name != agent.EventGradingAppeal {
			t.Fatalf("expected grading:appeal event, got %s", ev.Name)
		}
		if ev.Payload["status"] != "resolved" || ev.Payload["appeal_id"] != ap.ID {
			t.Fatalf("event payload wrong: %+v", ev.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event to student")
	}
	if n := reminderNotificationCount(t, s, studentID); n < 1 {
		t.Fatalf("expected student notification, got %d", n)
	}
}

// TestAppealResolveAcceptedNoScore 仅 accept（不写分）→ 状态 accepted，grade_json 不变。
func TestAppealResolveAcceptedNoScore(t *testing.T) {
	s, _ := newTestServices(t)
	ws, teacherID, studentID, _, _, assignmentID, qvid := baseSubmitSetup(t, s)
	sub := submitAssignment(t, s, ws.ID, studentID, assignmentID, qvid, json.RawMessage(`"A"`))
	ap, err := s.Appeals.AppealCreate(ctx(), AppealCreateReq{
		WorkspaceID: ws.ID, UserID: studentID, GradingID: sub.ID, Reason: "有疑问",
	})
	if err != nil {
		t.Fatalf("appeal create: %v", err)
	}
	got, err := s.Appeals.AppealResolve(ctx(), AppealResolveReq{
		WorkspaceID: ws.ID, UserID: teacherID, AppealID: ap.ID,
		Decision: domain.AppealDecisionAccepted,
	})
	if err != nil {
		t.Fatalf("appeal resolve: %v", err)
	}
	if got.Status != domain.AppealStatusAccepted {
		t.Fatalf("expected accepted, got %s", got.Status)
	}
	if v := gradeJSONVersion(t, assignmentRowGradeJSON(t, s, sub.ID)); v != 1 {
		t.Fatalf("grade_json should stay version 1, got %d", v)
	}
}

func TestAppealResolveRejected(t *testing.T) {
	s, _ := newTestServices(t)
	ws, teacherID, studentID, _, _, assignmentID, qvid := baseSubmitSetup(t, s)
	sub := submitAssignment(t, s, ws.ID, studentID, assignmentID, qvid, json.RawMessage(`"A"`))
	ap, err := s.Appeals.AppealCreate(ctx(), AppealCreateReq{
		WorkspaceID: ws.ID, UserID: studentID, GradingID: sub.ID, Reason: "有疑问",
	})
	if err != nil {
		t.Fatalf("appeal create: %v", err)
	}
	got, err := s.Appeals.AppealResolve(ctx(), AppealResolveReq{
		WorkspaceID: ws.ID, UserID: teacherID, AppealID: ap.ID,
		Decision: domain.AppealDecisionRejected, TeacherNote: "驳回，原判正确",
	})
	if err != nil {
		t.Fatalf("appeal resolve: %v", err)
	}
	if got.Status != domain.AppealStatusRejected {
		t.Fatalf("expected rejected, got %s", got.Status)
	}
	if v := gradeJSONVersion(t, assignmentRowGradeJSON(t, s, sub.ID)); v != 1 {
		t.Fatalf("rejected must not touch grade_json, got version %d", v)
	}
}

func TestAppealResolveIllegalTransition(t *testing.T) {
	s, _ := newTestServices(t)
	ws, teacherID, studentID, _, _, assignmentID, qvid := baseSubmitSetup(t, s)
	sub := submitAssignment(t, s, ws.ID, studentID, assignmentID, qvid, json.RawMessage(`"A"`))
	ap, err := s.Appeals.AppealCreate(ctx(), AppealCreateReq{
		WorkspaceID: ws.ID, UserID: studentID, GradingID: sub.ID, Reason: "有疑问",
	})
	if err != nil {
		t.Fatalf("appeal create: %v", err)
	}
	newScore := 8.0
	if _, err := s.Appeals.AppealResolve(ctx(), AppealResolveReq{
		WorkspaceID: ws.ID, UserID: teacherID, AppealID: ap.ID,
		Decision: domain.AppealDecisionAccepted, NewScore: &newScore,
	}); err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if st := appealRowStatus(t, s, ap.ID); st != domain.AppealStatusResolved {
		t.Fatalf("expected resolved, got %s", st)
	}
	// resolved 上再次 resolve → INVALID_STATE
	_, err = s.Appeals.AppealResolve(ctx(), AppealResolveReq{
		WorkspaceID: ws.ID, UserID: teacherID, AppealID: ap.ID,
		Decision: domain.AppealDecisionRejected,
	})
	de := domain.AsError(err)
	if de == nil || de.Code != domain.CodeInvalidState {
		t.Fatalf("expected INVALID_STATE, got %v", err)
	}
}

func TestAppealResolveValidation(t *testing.T) {
	s, _ := newTestServices(t)
	ws, teacherID, studentID, _, _, assignmentID, qvid := baseSubmitSetup(t, s)
	sub := submitAssignment(t, s, ws.ID, studentID, assignmentID, qvid, json.RawMessage(`"A"`))
	ap, err := s.Appeals.AppealCreate(ctx(), AppealCreateReq{
		WorkspaceID: ws.ID, UserID: studentID, GradingID: sub.ID, Reason: "有疑问",
	})
	if err != nil {
		t.Fatalf("appeal create: %v", err)
	}

	newScore := 5.0
	cases := []struct {
		name string
		req  AppealResolveReq
		code domain.ErrorCode
	}{
		{"unknown decision", AppealResolveReq{WorkspaceID: ws.ID, UserID: teacherID, AppealID: ap.ID, Decision: "appeal"}, domain.CodeInvalidArgument},
		{"rejected with new_score", AppealResolveReq{WorkspaceID: ws.ID, UserID: teacherID, AppealID: ap.ID, Decision: domain.AppealDecisionRejected, NewScore: &newScore}, domain.CodeInvalidArgument},
		{"negative new_score", AppealResolveReq{WorkspaceID: ws.ID, UserID: teacherID, AppealID: ap.ID, Decision: domain.AppealDecisionAccepted, NewScore: newFloat(-1)}, domain.CodeInvalidArgument},
		{"unknown appeal", AppealResolveReq{WorkspaceID: ws.ID, UserID: teacherID, AppealID: NewID(), Decision: domain.AppealDecisionAccepted}, domain.CodeNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Appeals.AppealResolve(ctx(), tc.req)
			de := domain.AsError(err)
			if de == nil || de.Code != tc.code {
				t.Fatalf("expected %s, got %v", tc.code, err)
			}
		})
	}
}

func TestAppealResolvePermission(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _, studentID, _, _, assignmentID, qvid := baseSubmitSetup(t, s)
	sub := submitAssignment(t, s, ws.ID, studentID, assignmentID, qvid, json.RawMessage(`"A"`))
	ap, err := s.Appeals.AppealCreate(ctx(), AppealCreateReq{
		WorkspaceID: ws.ID, UserID: studentID, GradingID: sub.ID, Reason: "有疑问",
	})
	if err != nil {
		t.Fatalf("appeal create: %v", err)
	}
	otherTeacher := createTeacher(t, s, ws.ID)

	cases := []struct {
		name string
		user string
	}{
		{"student", studentID},
		{"non-owner teacher", otherTeacher},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Appeals.AppealResolve(ctx(), AppealResolveReq{
				WorkspaceID: ws.ID, UserID: tc.user, AppealID: ap.ID,
				Decision: domain.AppealDecisionAccepted,
			})
			isForbidden(t, err)
		})
	}
}

// TestAppealResolveViaAssignmentGrade 复议路径（b）：accept 后教师用 AssignmentGrade 改分 → 申诉自动 resolved。
func TestAppealResolveViaAssignmentGrade(t *testing.T) {
	s, _ := newTestServices(t)
	ws, teacherID, studentID, _, _, assignmentID, qvid := baseSubmitSetup(t, s)
	sub := submitAssignment(t, s, ws.ID, studentID, assignmentID, qvid, json.RawMessage(`"A"`))
	ap, err := s.Appeals.AppealCreate(ctx(), AppealCreateReq{
		WorkspaceID: ws.ID, UserID: studentID, GradingID: sub.ID, Reason: "有疑问",
	})
	if err != nil {
		t.Fatalf("appeal create: %v", err)
	}
	if _, err := s.Appeals.AppealResolve(ctx(), AppealResolveReq{
		WorkspaceID: ws.ID, UserID: teacherID, AppealID: ap.ID,
		Decision: domain.AppealDecisionAccepted,
	}); err != nil {
		t.Fatalf("appeal resolve accept: %v", err)
	}
	if st := appealRowStatus(t, s, ap.ID); st != domain.AppealStatusAccepted {
		t.Fatalf("expected accepted before regrade, got %s", st)
	}
	// 教师重新批阅（version 1 → 2）
	if _, err := s.Assignments.AssignmentGrade(ctx(), AssignmentGradeReq{
		WorkspaceID: ws.ID, UserID: teacherID, SubmissionID: sub.ID, Version: 1,
		GradeJSON: json.RawMessage(`{"items":[],"overall":9,"comment":"复议后改分"}`),
	}); err != nil {
		t.Fatalf("assignment grade: %v", err)
	}
	if st := appealRowStatus(t, s, ap.ID); st != domain.AppealStatusResolved {
		t.Fatalf("expected resolved after regrade, got %s", st)
	}
}

func TestAppealList(t *testing.T) {
	s, _ := newTestServices(t)
	ws, teacherID, studentID, _, _, assignmentID, qvid := baseSubmitSetup(t, s)
	sub := submitAssignment(t, s, ws.ID, studentID, assignmentID, qvid, json.RawMessage(`"A"`))
	if _, err := s.Appeals.AppealCreate(ctx(), AppealCreateReq{
		WorkspaceID: ws.ID, UserID: studentID, GradingID: sub.ID, Reason: "对第一题得分有疑问",
	}); err != nil {
		t.Fatalf("appeal create: %v", err)
	}
	// 教师可列出该作业全部申诉
	got, err := s.Appeals.AppealList(ctx(), AppealListReq{
		WorkspaceID: ws.ID, UserID: teacherID, AssignmentID: assignmentID,
	})
	if err != nil {
		t.Fatalf("appeal list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 appeal, got %d", len(got))
	}
	if got[0].Reason != "对第一题得分有疑问" || got[0].GradingID != sub.ID {
		t.Fatalf("appeal fields wrong: %+v", got[0])
	}

	// 学生 / 非 owner 教师不可列出（FORBIDDEN）
	otherTeacher := createTeacher(t, s, ws.ID)
	for _, tc := range []struct {
		name string
		user string
	}{{"student", studentID}, {"non-owner teacher", otherTeacher}} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Appeals.AppealList(ctx(), AppealListReq{
				WorkspaceID: ws.ID, UserID: tc.user, AssignmentID: assignmentID,
			})
			isForbidden(t, err)
		})
	}

	// 未知作业 → NOT_FOUND
	_, err = s.Appeals.AppealList(ctx(), AppealListReq{
		WorkspaceID: ws.ID, UserID: teacherID, AssignmentID: NewID(),
	})
	de := domain.AsError(err)
	if de == nil || de.Code != domain.CodeNotFound {
		t.Fatalf("expected NOT_FOUND, got %v", err)
	}
}

// TestAppealInAssignmentList 学生 AssignmentList 附带其申诉（前端入口依据）。
func TestAppealInAssignmentList(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _, studentID, _, _, assignmentID, qvid := baseSubmitSetup(t, s)
	sub := submitAssignment(t, s, ws.ID, studentID, assignmentID, qvid, json.RawMessage(`"A"`))
	ap, err := s.Appeals.AppealCreate(ctx(), AppealCreateReq{
		WorkspaceID: ws.ID, UserID: studentID, GradingID: sub.ID, Reason: "有疑问",
	})
	if err != nil {
		t.Fatalf("appeal create: %v", err)
	}
	got, err := s.Assignments.AssignmentList(ctx(), AssignmentListReq{
		WorkspaceID: ws.ID, UserID: studentID,
	})
	if err != nil {
		t.Fatalf("assignment list: %v", err)
	}
	var found *Appeal
	for _, asg := range got {
		if asg.ID == assignmentID && asg.Appeal != nil {
			found = asg.Appeal
		}
	}
	if found == nil {
		t.Fatal("expected appeal attached to assignment, got nil")
	}
	if found.ID != ap.ID {
		t.Fatalf("expected appeal %s, got %s", ap.ID, found.ID)
	}
}

// newFloat 返回 float64 指针。
func newFloat(v float64) *float64 { return &v }
