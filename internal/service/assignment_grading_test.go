package service

import (
	"encoding/json"
	"testing"

	"lumo/internal/domain"
)

// ---- Todo 23 批阅 Grading ----

// createAssignmentRule 教师按指定判分方式创建作业（草稿态）。
func createAssignmentRule(t *testing.T, s *Services, wsID, teacherID, classID, paperID, title, dueAt, rule string) *Assignment {
	t.Helper()
	a, err := s.Assignments.AssignmentCreate(ctx(), AssignmentCreateReq{
		WorkspaceID: wsID, UserID: teacherID, ClassID: classID, PaperID: paperID,
		Title: title, DueAt: dueAt, GradingRule: rule,
		IdempotencyKey: "ac-" + NewID(),
	})
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}
	return a
}

// publishAssignment 发布作业并返回已发布版本。
func publishAssignment(t *testing.T, s *Services, wsID, teacherID string, asg *Assignment) *Assignment {
	t.Helper()
	pub, err := s.Assignments.AssignmentPublish(ctx(), AssignmentPublishReq{
		WorkspaceID: wsID, UserID: teacherID, AssignmentID: asg.ID, Version: asg.Version,
	})
	if err != nil {
		t.Fatalf("publish assignment: %v", err)
	}
	return pub
}

// submitAssignment 学生提交作业（返回提交记录）。
func submitAssignment(t *testing.T, s *Services, wsID, studentID, assignmentID, qvid string, answer json.RawMessage) *AssignmentSubmission {
	t.Helper()
	sub, err := s.Assignments.AssignmentSubmit(ctx(), AssignmentSubmitReq{
		WorkspaceID: wsID, UserID: studentID, AssignmentID: assignmentID,
		Answers:        []AssignmentAnswer{{QuestionVersionID: qvid, Answer: answer}},
		IdempotencyKey: "as-" + NewID(),
	})
	if err != nil {
		t.Fatalf("submit assignment: %v", err)
	}
	return sub
}

// gradeJSONVersion 解析 grade_json 的 version 字段（空/非法 → 0）。
func gradeJSONVersion(t *testing.T, raw string) int {
	t.Helper()
	if raw == "" || raw == "{}" {
		return 0
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("grade_json 非法: %v", err)
	}
	v, _ := m["version"].(float64)
	return int(v)
}

// ---- 客观题 auto 自动判分即已批 ----

func TestAssignmentAutoGradeObjective(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _, studentID, _, _, assignmentID, qvid := baseSubmitSetup(t, s) // createAssignment 默认 auto

	// 答对 → 提交后即已批（version=1，得分满分）
	sub := submitAssignment(t, s, ws.ID, studentID, assignmentID, qvid, json.RawMessage(`"A"`))
	raw := assignmentRowGradeJSON(t, s, sub.ID)
	if v := gradeJSONVersion(t, raw); v != 1 {
		t.Fatalf("auto 判分后 version 应为 1, got %d (raw=%s)", v, raw)
	}
	var gp map[string]any
	if err := json.Unmarshal([]byte(raw), &gp); err != nil {
		t.Fatal(err)
	}
	items, _ := gp["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	item, _ := items[0].(map[string]any)
	if item["score"] != float64(10) || item["max_score"] != float64(10) || item["status"] != "graded" {
		t.Fatalf("unexpected item: %+v", item)
	}
	if gp["overall"] != float64(10) {
		t.Fatalf("unexpected overall: %v", gp["overall"])
	}
	// graded_at 已落库（已批）
	var gradedAt *string
	if err := s.Repo.DB().QueryRowContext(ctx(),
		`SELECT graded_at FROM assignment_submissions WHERE id = ?`, sub.ID).Scan(&gradedAt); err != nil {
		t.Fatal(err)
	}
	if gradedAt == nil || *gradedAt == "" {
		t.Fatal("auto 判分后 graded_at 应为非空")
	}
}

func TestAssignmentAutoGradeWrongObjective(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _, studentID, _, _, assignmentID, qvid := baseSubmitSetup(t, s)

	sub := submitAssignment(t, s, ws.ID, studentID, assignmentID, qvid, json.RawMessage(`"B"`))
	raw := assignmentRowGradeJSON(t, s, sub.ID)
	var gp map[string]any
	if err := json.Unmarshal([]byte(raw), &gp); err != nil {
		t.Fatal(err)
	}
	items, _ := gp["items"].([]any)
	item, _ := items[0].(map[string]any)
	if item["score"] != float64(0) {
		t.Fatalf("答错应得 0 分, got %v", item["score"])
	}
	if gp["overall"] != float64(0) {
		t.Fatalf("overall 应为 0, got %v", gp["overall"])
	}
}

// ---- teacher 规则：提交后仍待批（'{}'），教师 AssignmentGrade 写分 version 1→2 ----

func TestAssignmentTeacherRuleStaysPending(t *testing.T) {
	s, _ := newTestServices(t)
	ws, teacherID, studentID, classID, paperID, _, qvid := baseSubmitSetup(t, s)
	asg := publishAssignment(t, s, ws.ID, teacherID,
		createAssignmentRule(t, s, ws.ID, teacherID, classID, paperID, "教师批改作业", "2026-08-10T00:00:00Z", domain.GradingRuleTeacher))

	sub := submitAssignment(t, s, ws.ID, studentID, asg.ID, qvid, json.RawMessage(`"A"`))
	raw := assignmentRowGradeJSON(t, s, sub.ID)
	if raw != "{}" {
		t.Fatalf("teacher 规则提交后应为空态 '{}', got %s", raw)
	}
	var gradedAt *string
	if err := s.Repo.DB().QueryRowContext(ctx(),
		`SELECT graded_at FROM assignment_submissions WHERE id = ?`, sub.ID).Scan(&gradedAt); err != nil {
		t.Fatal(err)
	}
	if gradedAt != nil {
		t.Fatal("teacher 规则未批改时 graded_at 应为 NULL")
	}
}

func TestAssignmentGradeTeacherWriteAndBump(t *testing.T) {
	s, _ := newTestServices(t)
	ws, teacherID, studentID, classID, paperID, _, qvid := baseSubmitSetup(t, s)
	asg := publishAssignment(t, s, ws.ID, teacherID,
		createAssignmentRule(t, s, ws.ID, teacherID, classID, paperID, "教师批改作业", "2026-08-10T00:00:00Z", domain.GradingRuleTeacher))
	sub := submitAssignment(t, s, ws.ID, studentID, asg.ID, qvid, json.RawMessage(`"A"`))

	// 教师写分：version 0 → 1
	gradeJSON := mustJSON(map[string]any{
		"items": []map[string]any{
			{"question_version_id": qvid, "type": "single_choice", "max_score": 10, "score": 8, "status": "graded", "comment": "部分正确"},
		},
		"overall": 8,
		"comment": "不错",
	})
	graded, err := s.Assignments.AssignmentGrade(ctx(), AssignmentGradeReq{
		WorkspaceID: ws.ID, UserID: teacherID, SubmissionID: sub.ID,
		GradeJSON: gradeJSON, Version: 0,
	})
	if err != nil {
		t.Fatalf("grade: %v", err)
	}
	if v := gradeJSONVersion(t, string(graded.GradeJSON)); v != 1 {
		t.Fatalf("第一次批阅后 version 应为 1, got %d", v)
	}
	if graded.GradedAt == nil || *graded.GradedAt == "" {
		t.Fatal("批阅后 graded_at 应为非空")
	}

	// 再次批阅（改分）：version 1 → 2
	gradeJSON2 := mustJSON(map[string]any{
		"items": []map[string]any{
			{"question_version_id": qvid, "type": "single_choice", "max_score": 10, "score": 10, "status": "graded", "comment": "已改满分"},
		},
		"overall": 10,
		"comment": "修正",
	})
	graded2, err := s.Assignments.AssignmentGrade(ctx(), AssignmentGradeReq{
		WorkspaceID: ws.ID, UserID: teacherID, SubmissionID: sub.ID,
		GradeJSON: gradeJSON2, Version: 1,
	})
	if err != nil {
		t.Fatalf("re-grade: %v", err)
	}
	if v := gradeJSONVersion(t, string(graded2.GradeJSON)); v != 2 {
		t.Fatalf("第二次批阅后 version 应为 2, got %d", v)
	}
	if n := classAuditCount(t, s, "assignment.grade"); n != 2 {
		t.Fatalf("expected 2 grade audits, got %d", n)
	}
}

// ---- 乐观锁：版本不匹配 → CONFLICT ----

func TestAssignmentGradeVersionConflict(t *testing.T) {
	s, _ := newTestServices(t)
	ws, teacherID, studentID, classID, paperID, _, qvid := baseSubmitSetup(t, s)
	asg := publishAssignment(t, s, ws.ID, teacherID,
		createAssignmentRule(t, s, ws.ID, teacherID, classID, paperID, "教师批改作业", "2026-08-10T00:00:00Z", domain.GradingRuleTeacher))
	sub := submitAssignment(t, s, ws.ID, studentID, asg.ID, qvid, json.RawMessage(`"A"`))

	if _, err := s.Assignments.AssignmentGrade(ctx(), AssignmentGradeReq{
		WorkspaceID: ws.ID, UserID: teacherID, SubmissionID: sub.ID,
		GradeJSON: mustJSON(map[string]any{"items": []any{}, "overall": 5, "comment": ""}), Version: 0,
	}); err != nil {
		t.Fatalf("first grade: %v", err)
	}
	// 陈旧版本（0，当前已是 1）→ CONFLICT
	_, err := s.Assignments.AssignmentGrade(ctx(), AssignmentGradeReq{
		WorkspaceID: ws.ID, UserID: teacherID, SubmissionID: sub.ID,
		GradeJSON: mustJSON(map[string]any{"items": []any{}, "overall": 9, "comment": ""}), Version: 0,
	})
	if de := domain.AsError(err); de == nil || de.Code != domain.CodeConflict {
		t.Fatalf("expected CONFLICT on stale version, got %v", err)
	}
}

// ---- 权限：非教师/非创建者 FORBIDDEN；提交不存在 NOT_FOUND ----

func TestAssignmentGradePermission(t *testing.T) {
	s, _ := newTestServices(t)
	ws, teacherID, studentID, classID, paperID, _, qvid := baseSubmitSetup(t, s)
	asg := publishAssignment(t, s, ws.ID, teacherID,
		createAssignmentRule(t, s, ws.ID, teacherID, classID, paperID, "教师批改作业", "2026-08-10T00:00:00Z", domain.GradingRuleTeacher))
	sub := submitAssignment(t, s, ws.ID, studentID, asg.ID, qvid, json.RawMessage(`"A"`))
	otherTeacher := createTeacher(t, s, ws.ID)

	// 学生批阅 → FORBIDDEN
	_, err := s.Assignments.AssignmentGrade(ctx(), AssignmentGradeReq{
		WorkspaceID: ws.ID, UserID: studentID, SubmissionID: sub.ID,
		GradeJSON: mustJSON(map[string]any{}), Version: 0,
	})
	isForbidden(t, err)
	// 非班级创建者教师 → FORBIDDEN
	_, err = s.Assignments.AssignmentGrade(ctx(), AssignmentGradeReq{
		WorkspaceID: ws.ID, UserID: otherTeacher, SubmissionID: sub.ID,
		GradeJSON: mustJSON(map[string]any{}), Version: 0,
	})
	isForbidden(t, err)
	// 提交不存在 → NOT_FOUND
	_, err = s.Assignments.AssignmentGrade(ctx(), AssignmentGradeReq{
		WorkspaceID: ws.ID, UserID: teacherID, SubmissionID: "no-sub-1",
		GradeJSON: mustJSON(map[string]any{}), Version: 0,
	})
	if de := domain.AsError(err); de == nil || de.Code != domain.CodeNotFound {
		t.Fatalf("expected NOT_FOUND, got %v", err)
	}
	// 空 grade_json → INVALID_ARGUMENT
	_, err = s.Assignments.AssignmentGrade(ctx(), AssignmentGradeReq{
		WorkspaceID: ws.ID, UserID: teacherID, SubmissionID: sub.ID,
		GradeJSON: nil, Version: 0,
	})
	if de := domain.AsError(err); de == nil || de.Code != domain.CodeInvalidArgument {
		t.Fatalf("expected INVALID_ARGUMENT for empty grade_json, got %v", err)
	}
}

// ---- EssayGrader 预批：未配置 Provider → 降级提示，不阻断 ----

func TestAssignmentGradePreGradeDegraded(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	teacherID := createTeacher(t, s, ws.ID)
	studentID := createStudent(t, s, ws.ID)
	cls := createClass(t, s, ws.ID, teacherID)
	addClassStudent(t, s, ws.ID, teacherID, cls.ID, studentID)
	q := publishedQuestion(t, s, ws.ID, mustJSON(map[string]any{
		"type": "short_answer", "stem": "简述光合作用", "answer": "参考",
	}))
	paper := publishPaper(t, s, ws.ID, teacherID, "已发布卷", []string{q.CurrentVersion.ID}, 30)
	asg := publishAssignment(t, s, ws.ID, teacherID,
		createAssignmentRule(t, s, ws.ID, teacherID, cls.ID, paper.ID, "主观题作业", "2026-08-10T00:00:00Z", domain.GradingRuleHybrid))
	sub := submitAssignment(t, s, ws.ID, studentID, asg.ID, q.CurrentVersion.ID, json.RawMessage(`"光合作用释放氧气"`))

	// 预批（pre_grade=true）：不落库、不改 version；未配置 Provider → 降级提示
	out, err := s.Assignments.AssignmentGrade(ctx(), AssignmentGradeReq{
		WorkspaceID: ws.ID, UserID: teacherID, SubmissionID: sub.ID,
		PreGrade: true, Version: 0,
	})
	if err != nil {
		t.Fatalf("pre-grade should not fail: %v", err)
	}
	if out.Message == "" {
		t.Fatal("预批降级时应返回 Message 提示")
	}
	if v := gradeJSONVersion(t, string(out.GradeJSON)); v != 0 {
		t.Fatalf("预批不应改 version, got %d", v)
	}
	// 未落库：DB 仍为 '{}'
	if raw := assignmentRowGradeJSON(t, s, sub.ID); raw != "{}" {
		t.Fatalf("预批不应写库, got %s", raw)
	}
	// 预批后教师仍可正常批阅（不阻断）
	graded, err := s.Assignments.AssignmentGrade(ctx(), AssignmentGradeReq{
		WorkspaceID: ws.ID, UserID: teacherID, SubmissionID: sub.ID,
		GradeJSON: mustJSON(map[string]any{
			"items": []map[string]any{
				{"question_version_id": q.CurrentVersion.ID, "type": "short_answer", "max_score": 10, "score": 9, "status": "graded", "comment": "要点齐全"},
			},
			"overall": 9, "comment": "很好",
		}), Version: 0,
	})
	if err != nil {
		t.Fatalf("manual grade after pre-grade: %v", err)
	}
	if v := gradeJSONVersion(t, string(graded.GradeJSON)); v != 1 {
		t.Fatalf("manual grade version 应为 1, got %d", v)
	}
}

// ---- 学生端：AssignmentList 携带本人提交（可见得分） ----

func TestAssignmentListStudentSeesGrade(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _, studentID, _, _, assignmentID, qvid := baseSubmitSetup(t, s)
	submitAssignment(t, s, ws.ID, studentID, assignmentID, qvid, json.RawMessage(`"A"`))

	list, err := s.Assignments.AssignmentList(ctx(), AssignmentListReq{WorkspaceID: ws.ID, UserID: studentID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Submission == nil {
		t.Fatalf("student should see own submission, got %+v", list)
	}
	if v := gradeJSONVersion(t, string(list[0].Submission.GradeJSON)); v != 1 {
		t.Fatalf("student submission grade version 应为 1, got %d", v)
	}
}
