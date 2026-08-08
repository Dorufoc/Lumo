package service

import (
	"bytes"
	"encoding/json"
	"testing"

	"lumo/internal/domain"
)

// statsClassSetup 创建：ws + teacher + class + 两名学生 + 知识点题目 + 试卷 + 已发布作业。
func statsClassSetup(t *testing.T, s *Services) (ws *Workspace, teacherID, classID, assignmentID, qvid, kid, st1, st2 string) {
	t.Helper()
	ws, _ = createWorkspace(t, s)
	teacherID = createTeacher(t, s, ws.ID)
	st1 = createStudent(t, s, ws.ID)
	st2 = createStudent(t, s, ws.ID)
	cls := createClass(t, s, ws.ID, teacherID)
	addClassStudent(t, s, ws.ID, teacherID, cls.ID, st1)
	addClassStudent(t, s, ws.ID, teacherID, cls.ID, st2)
	kid = createKnowledgeNode(t, s, ws.ID, "牛顿第二定律")
	q := publishedQuestion(t, s, ws.ID, mustJSON(map[string]any{
		"type": "single_choice",
		"stem": "作业统计题",
		"options": []map[string]any{
			{"key": "A", "text": "A"}, {"key": "B", "text": "B"}, {"key": "C", "text": "C"},
		},
		"answer":        "A",
		"difficulty":    2,
		"knowledge_ids": []string{kid},
	}))
	qvid = q.CurrentVersion.ID
	paper := publishPaper(t, s, ws.ID, teacherID, "统计试卷", []string{qvid}, 30)
	asg := createAssignment(t, s, ws.ID, teacherID, cls.ID, paper.ID, "统计作业", "2026-08-10T00:00:00Z")
	pub, err := s.Assignments.AssignmentPublish(ctx(), AssignmentPublishReq{
		WorkspaceID: ws.ID, UserID: teacherID, AssignmentID: asg.ID, Version: asg.Version,
	})
	if err != nil {
		t.Fatalf("publish assignment: %v", err)
	}
	return ws, teacherID, cls.ID, pub.ID, qvid, kid, st1, st2
}

func TestClassStatsHappy(t *testing.T) {
	s, _ := newTestServices(t)
	ws, teacherID, classID, assignmentID, qvid, kid, st1, st2 := statsClassSetup(t, s)

	// st1 答对（A），st2 答错（B）
	submitAssignment(t, s, ws.ID, st1, assignmentID, qvid, json.RawMessage(`"A"`))
	submitAssignment(t, s, ws.ID, st2, assignmentID, qvid, json.RawMessage(`"B"`))

	got, err := s.Stats.ClassStats(ctx(), ClassStatsReq{WorkspaceID: ws.ID, UserID: teacherID, ClassID: classID})
	if err != nil {
		t.Fatalf("ClassStats: %v", err)
	}
	if got.StudentTotal != 2 {
		t.Fatalf("StudentTotal = %d, want 2", got.StudentTotal)
	}
	if got.AssignmentTotal != 1 {
		t.Fatalf("AssignmentTotal = %d, want 1", got.AssignmentTotal)
	}
	if got.SubmissionTotal != 2 {
		t.Fatalf("SubmissionTotal = %d, want 2", got.SubmissionTotal)
	}
	if got.GradedTotal != 2 {
		t.Fatalf("GradedTotal = %d, want 2", got.GradedTotal)
	}
	if got.CompletionRate != 1.0 {
		t.Fatalf("CompletionRate = %v, want 1.0", got.CompletionRate)
	}
	if got.AvgScore != 5.0 {
		t.Fatalf("AvgScore = %v, want 5.0", got.AvgScore)
	}
	if got.MaxScore != 10.0 {
		t.Fatalf("MaxScore = %v, want 10.0", got.MaxScore)
	}
	if got.Accuracy != 0.5 {
		t.Fatalf("Accuracy = %v, want 0.5", got.Accuracy)
	}
	if len(got.WeakTop) != 1 {
		t.Fatalf("WeakTop len = %d, want 1", len(got.WeakTop))
	}
	if got.WeakTop[0].KnowledgeID != kid || got.WeakTop[0].WrongCount != 1 {
		t.Fatalf("WeakTop[0] = %+v, want knowledge %s wrong=1", got.WeakTop[0], kid)
	}
}

func TestClassStatsAssignmentFilter(t *testing.T) {
	s, _ := newTestServices(t)
	ws, teacherID, classID, assignmentID, qvid, _, st1, st2 := statsClassSetup(t, s)

	// 第二个作业（同试卷），无人提交
	paper, err := s.Exam.ExamPaperCreate(ctx(), ExamPaperCreateReq{
		WorkspaceID: ws.ID, UserID: teacherID, Title: "统计试卷二",
		ConfigJSON: examPaperConfig(30, []map[string]any{
			examSection("第一部分", 1, []string{qvid}, 10),
		}),
		IdempotencyKey: "epc-" + NewID(),
	})
	if err != nil {
		t.Fatalf("create paper2: %v", err)
	}
	if _, err := s.Exam.ExamPaperPublish(ctx(), ExamPaperPublishReq{
		WorkspaceID: ws.ID, PaperID: paper.ID, Version: paper.Version,
	}); err != nil {
		t.Fatalf("publish paper2: %v", err)
	}
	asg2, err := s.Assignments.AssignmentCreate(ctx(), AssignmentCreateReq{
		WorkspaceID: ws.ID, UserID: teacherID, ClassID: classID, PaperID: paper.ID,
		Title: "统计作业二", DueAt: "2026-08-12T00:00:00Z", GradingRule: "auto",
		IdempotencyKey: "ac2-" + NewID(),
	})
	if err != nil {
		t.Fatalf("create assignment2: %v", err)
	}
	if _, err := s.Assignments.AssignmentPublish(ctx(), AssignmentPublishReq{
		WorkspaceID: ws.ID, UserID: teacherID, AssignmentID: asg2.ID, Version: asg2.Version,
	}); err != nil {
		t.Fatalf("publish assignment2: %v", err)
	}
	submitAssignment(t, s, ws.ID, st1, assignmentID, qvid, json.RawMessage(`"A"`))
	submitAssignment(t, s, ws.ID, st2, assignmentID, qvid, json.RawMessage(`"A"`))

	// 全类范围：2 个作业，2 份提交
	all, err := s.Stats.ClassStats(ctx(), ClassStatsReq{WorkspaceID: ws.ID, UserID: teacherID, ClassID: classID})
	if err != nil {
		t.Fatalf("ClassStats all: %v", err)
	}
	if all.AssignmentTotal != 2 || all.SubmissionTotal != 2 {
		t.Fatalf("class scope = %+v, want 2 assignments 2 submissions", all)
	}
	if all.CompletionRate != 0.5 {
		t.Fatalf("CompletionRate = %v, want 0.5", all.CompletionRate)
	}

	// 指定作业范围：仅 assignmentID 的提交
	scoped, err := s.Stats.ClassStats(ctx(), ClassStatsReq{
		WorkspaceID: ws.ID, UserID: teacherID, ClassID: classID, AssignmentID: assignmentID,
	})
	if err != nil {
		t.Fatalf("ClassStats scoped: %v", err)
	}
	if scoped.AssignmentTotal != 1 || scoped.SubmissionTotal != 2 {
		t.Fatalf("scoped = %+v, want 1 assignment 2 submissions", scoped)
	}
	if scoped.CompletionRate != 1.0 {
		t.Fatalf("scoped CompletionRate = %v, want 1.0", scoped.CompletionRate)
	}
}

func TestClassStatsEmptyClass(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	teacherID := createTeacher(t, s, ws.ID)
	cls := createClass(t, s, ws.ID, teacherID) // 无成员

	got, err := s.Stats.ClassStats(ctx(), ClassStatsReq{WorkspaceID: ws.ID, UserID: teacherID, ClassID: cls.ID})
	if err != nil {
		t.Fatalf("ClassStats empty: %v", err)
	}
	if got.StudentTotal != 0 || got.SubmissionTotal != 0 || got.GradedTotal != 0 {
		t.Fatalf("empty stats = %+v, want all zero", got)
	}
	if got.CompletionRate != 0 || got.AvgScore != 0 || got.Accuracy != 0 || len(got.WeakTop) != 0 {
		t.Fatalf("empty stats nonzero: %+v", got)
	}
	// 无提交时 weak_top 必须是空切片而非 null（避免前端 .length 白屏）。
	if got.WeakTop == nil {
		t.Fatal("weak_top should be non-nil empty slice, not null")
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"weak_top":[]`)) {
		t.Fatalf("weak_top should marshal to [] not null: %s", raw)
	}
}

func TestClassStatsPermission(t *testing.T) {
	s, _ := newTestServices(t)
	ws, teacherID, classID, _, _, _, st1, _ := statsClassSetup(t, s)

	// 学生 → FORBIDDEN
	_, err := s.Stats.ClassStats(ctx(), ClassStatsReq{WorkspaceID: ws.ID, UserID: st1, ClassID: classID})
	isForbidden(t, err)

	// 非创建者教师 → FORBIDDEN
	other := createTeacher(t, s, ws.ID)
	_, err = s.Stats.ClassStats(ctx(), ClassStatsReq{WorkspaceID: ws.ID, UserID: other, ClassID: classID})
	isForbidden(t, err)

	// 未知班级 → NOT_FOUND
	_, err = s.Stats.ClassStats(ctx(), ClassStatsReq{WorkspaceID: ws.ID, UserID: teacherID, ClassID: "class-missing"})
	if de := domain.AsError(err); de == nil || de.Code != domain.CodeNotFound {
		t.Fatalf("unknown class err = %v, want NOT_FOUND", err)
	}
}

func TestClassStatsValidation(t *testing.T) {
	s, _ := newTestServices(t)
	ws, teacherID, classID, _, _, _, _, _ := statsClassSetup(t, s)

	// 缺少 class_id → INVALID_ARG
	_, err := s.Stats.ClassStats(ctx(), ClassStatsReq{WorkspaceID: ws.ID, UserID: teacherID})
	if de := domain.AsError(err); de == nil || de.Code != domain.CodeInvalidArgument {
		t.Fatalf("missing class_id err = %v, want INVALID_ARG", err)
	}

	// 作业不属于该班级 → NOT_FOUND
	ws2, _ := createWorkspace(t, s)
	t2 := createTeacher(t, s, ws2.ID)
	cls2 := createClass(t, s, ws2.ID, t2)
	stX := createStudent(t, s, ws2.ID)
	addClassStudent(t, s, ws2.ID, t2, cls2.ID, stX)
	q2 := publishedQuestion(t, s, ws2.ID, scPayload("其他作业", "A"))
	paper2 := publishPaper(t, s, ws2.ID, t2, "其他试卷", []string{q2.CurrentVersion.ID}, 30)
	asg2 := createAssignment(t, s, ws2.ID, t2, cls2.ID, paper2.ID, "其他作业", "2026-08-20T00:00:00Z")
	if _, err := s.Assignments.AssignmentPublish(ctx(), AssignmentPublishReq{
		WorkspaceID: ws2.ID, UserID: t2, AssignmentID: asg2.ID, Version: asg2.Version,
	}); err != nil {
		t.Fatalf("publish other assignment: %v", err)
	}
	_, err = s.Stats.ClassStats(ctx(), ClassStatsReq{
		WorkspaceID: ws.ID, UserID: teacherID, ClassID: classID, AssignmentID: asg2.ID,
	})
	if de := domain.AsError(err); de == nil || de.Code != domain.CodeNotFound {
		t.Fatalf("foreign assignment err = %v, want NOT_FOUND", err)
	}
}
