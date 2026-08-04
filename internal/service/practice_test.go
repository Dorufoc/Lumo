package service

import (
	"context"
	"encoding/json"
	"testing"

	"lumo/internal/domain"
)

// publishedQuestion 创建并发布一道题，返回 question。
func publishedQuestion(t *testing.T, s *Services, wsID string, payload json.RawMessage) *Question {
	t.Helper()
	q, err := s.Knowledge.QuestionCreateDraft(ctx(), QuestionCreateDraftReq{
		WorkspaceID: wsID, Payload: payload, IdempotencyKey: "pq-" + NewID(),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	q, err = s.Knowledge.QuestionTransition(ctx(), QuestionTransitionReq{
		WorkspaceID: wsID, QuestionID: q.ID, Version: q.Version, Action: "review",
	})
	if err != nil {
		t.Fatal(err)
	}
	q, err = s.Knowledge.QuestionTransition(ctx(), QuestionTransitionReq{
		WorkspaceID: wsID, QuestionID: q.ID, Version: q.Version, Action: "publish",
	})
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func ctx() context.Context { return context.Background() }

func TestGradingRules(t *testing.T) {
	// 单选
	p := mustPayload(map[string]any{"type": "single_choice", "stem": "s", "answer": "B"})
	g, err := GradeObjective(p, json.RawMessage(`"B"`), 10)
	if err != nil || g.Score != 10 {
		t.Fatalf("sc correct: %+v %v", g, err)
	}
	g, _ = GradeObjective(p, json.RawMessage(`"A"`), 10)
	if g.Score != 0 {
		t.Fatalf("sc wrong should be 0: %+v", g)
	}

	// 多选 exact
	p = mustPayload(map[string]any{"type": "multiple_choice", "stem": "s", "answer": []string{"A", "C"}})
	g, err = GradeObjective(p, json.RawMessage(`["A","C"]`), 10)
	if err != nil || g.Score != 10 {
		t.Fatalf("mc exact correct: %+v %v", g, err)
	}
	g, _ = GradeObjective(p, json.RawMessage(`["A"]`), 10)
	if g.Score != 0 {
		t.Fatalf("mc exact partial should be 0: %+v", g)
	}

	// 多选 partial
	p = mustPayload(map[string]any{
		"type": "multiple_choice", "stem": "s", "answer": []string{"A", "C"},
		"grading_config": map[string]any{"mode": "partial"},
	})
	g, _ = GradeObjective(p, json.RawMessage(`["A"]`), 10)
	if g.Score != 5 {
		t.Fatalf("mc partial half: %+v", g)
	}
	g, _ = GradeObjective(p, json.RawMessage(`["A","B"]`), 10)
	if g.Score != 0 { // 1 对 1 错 → (1-1)/2 = 0
		t.Fatalf("mc partial wrong should be 0: %+v", g)
	}

	// 填空多空
	p = mustPayload(map[string]any{"type": "fill_blank", "stem": "a____b____", "answer": []string{"1", "2"}})
	g, _ = GradeObjective(p, json.RawMessage(`["1","x"]`), 10)
	if g.Score != 5 {
		t.Fatalf("fill half: %+v", g)
	}
	// 大小写不敏感
	p = mustPayload(map[string]any{"type": "fill_blank", "stem": "s", "answer": "hello"})
	g, _ = GradeObjective(p, json.RawMessage(`"HELLO"`), 10)
	if g.Score != 10 {
		t.Fatalf("fill case-insensitive: %+v", g)
	}
	// 数值容差
	p = mustPayload(map[string]any{
		"type": "fill_blank", "stem": "s", "answer": "3.14",
		"grading_config": map[string]any{"numeric": true, "tolerance": 0.01},
	})
	g, _ = GradeObjective(p, json.RawMessage(`"3.141"`), 10)
	if g.Score != 10 {
		t.Fatalf("fill numeric tolerance: %+v", g)
	}
	g, _ = GradeObjective(p, json.RawMessage(`"3.2"`), 10)
	if g.Score != 0 {
		t.Fatalf("fill numeric out of tolerance: %+v", g)
	}
}

func mustPayload(v any) *QuestionPayload {
	p, err := parseQuestionPayload(mustJSON(v))
	if err != nil {
		panic(err)
	}
	return p
}

func TestPracticeFullFlow(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)

	q1 := publishedQuestion(t, s, ws.ID, scPayload("1+1=?", "B"))
	q2 := publishedQuestion(t, s, ws.ID, mcPayload("多选题", []string{"A", "C"}))
	q3 := publishedQuestion(t, s, ws.ID, mustJSON(map[string]any{
		"type": "fill_blank", "stem": "1米=____厘米", "answer": "100",
	}))
	q4 := publishedQuestion(t, s, ws.ID, mustJSON(map[string]any{
		"type": "short_answer", "stem": "简述", "answer": "参考",
	}))

	session, err := s.Practice.PracticeStart(ctx(), PracticeStartReq{
		WorkspaceID: ws.ID, UserID: userID, Mode: "practice",
		QuestionIDs: []string{q1.ID, q2.ID, q3.ID, q4.ID},
		IdempotencyKey: "ps-" + NewID(),
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if session.Status != "answering" || len(session.Questions) != 4 {
		t.Fatalf("unexpected session: %+v", session)
	}
	// 答题中不暴露答案
	for _, q := range session.Questions {
		var payload map[string]any
		_ = json.Unmarshal(q.Payload, &payload)
		if payload["answer"] != nil {
			t.Fatal("answer leaked in answering session")
		}
	}
	// 快照含 version id
	qv1 := session.Questions[0].QuestionVersionID
	qv2 := session.Questions[1].QuestionVersionID
	qv3 := session.Questions[2].QuestionVersionID
	qv4 := session.Questions[3].QuestionVersionID

	// 保存草稿 + 过期序号
	draft, err := s.Practice.PracticeSaveAnswer(ctx(), PracticeSaveAnswerReq{
		WorkspaceID: ws.ID, SessionID: session.ID, QuestionVersionID: qv1,
		Answer: json.RawMessage(`"B"`), ClientSequence: 1,
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if draft.Answer[0] != '"' {
		t.Fatalf("unexpected draft: %+v", draft)
	}
	if _, err := s.Practice.PracticeSaveAnswer(ctx(), PracticeSaveAnswerReq{
		WorkspaceID: ws.ID, SessionID: session.ID, QuestionVersionID: qv1,
		Answer: json.RawMessage(`"A"`), ClientSequence: 0,
	}); err == nil {
		t.Fatal("expected CONFLICT for stale sequence")
	}
	// 保存其余答案
	for i, qv := range []string{qv2, qv3} {
		ans := json.RawMessage(`["A","C"]`)
		if i == 1 {
			ans = json.RawMessage(`"100"`)
		}
		if _, err := s.Practice.PracticeSaveAnswer(ctx(), PracticeSaveAnswerReq{
			WorkspaceID: ws.ID, SessionID: session.ID, QuestionVersionID: qv,
			Answer: ans, ClientSequence: 1,
		}); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}

	// 提交（固定幂等键，供重复调用验证）
	submitKey := "psub-" + NewID()
	result, err := s.Practice.PracticeSubmit(ctx(), PracticeSubmitReq{
		WorkspaceID: ws.ID, SessionID: session.ID, Version: session.Version,
		IdempotencyKey: submitKey,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if result.Status != "graded" {
		t.Fatalf("unexpected status: %s", result.Status)
	}
	// q1 10 分 + q2 10 分 + q3 10 分 + q4(未答→failed 0 分) = 30 / 40
	if result.TotalScore != 30 || result.MaxScore != 40 {
		t.Fatalf("unexpected score: %+v", result)
	}
	if len(result.WrongAnswers) != 1 {
		t.Fatalf("expected 1 wrong answer, got %d", len(result.WrongAnswers))
	}
	if len(result.ReviewActions) != 1 {
		t.Fatalf("expected 1 review action, got %d", len(result.ReviewActions))
	}
	// q4 简答题：无 AI → failed
	for _, q := range result.Questions {
		if q.QuestionVersionID == qv4 {
			if q.Grading.Status != "failed" {
				t.Fatalf("short answer should be failed without AI: %+v", q.Grading)
			}
			if q.Grading.Method != "rubric_llm" {
				t.Fatalf("expected rubric_llm method, got %s", q.Grading.Method)
			}
		}
	}
	// 提交后解析可见
	for _, q := range result.Questions {
		var payload map[string]any
		if err := json.Unmarshal(q.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["answer"] == nil {
			t.Fatal("analysis/answer should be visible after submit")
		}
	}

	// 幂等提交（同一幂等键返回相同结果）
	result2, err := s.Practice.PracticeSubmit(ctx(), PracticeSubmitReq{
		WorkspaceID: ws.ID, SessionID: session.ID, Version: session.Version,
		IdempotencyKey: submitKey,
	})
	if err != nil {
		t.Fatalf("second submit: %v", err)
	}
	if result2.SessionID != result.SessionID || result2.TotalScore != result.TotalScore {
		t.Fatal("submit not idempotent")
	}
	// 不同幂等键的重复提交应被拒绝（会话已 graded）
	if _, err := s.Practice.PracticeSubmit(ctx(), PracticeSubmitReq{
		WorkspaceID: ws.ID, SessionID: session.ID, Version: session.Version,
		IdempotencyKey: "psub-x-" + NewID(),
	}); err == nil {
		t.Fatal("expected INVALID_STATE for re-submit with new key")
	}
	// 提交后不能保存答案
	if _, err := s.Practice.PracticeSaveAnswer(ctx(), PracticeSaveAnswerReq{
		WorkspaceID: ws.ID, SessionID: session.ID, QuestionVersionID: qv1,
		Answer: json.RawMessage(`"A"`), ClientSequence: 2,
	}); err == nil {
		t.Fatal("expected INVALID_STATE saving after submit")
	}
}

func TestPracticeSkipAndWrongFlow(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)

	q1 := publishedQuestion(t, s, ws.ID, scPayload("跳过测试", "A"))
	session, err := s.Practice.PracticeStart(ctx(), PracticeStartReq{
		WorkspaceID: ws.ID, UserID: userID, Mode: "practice",
		QuestionIDs: []string{q1.ID}, IdempotencyKey: "ps-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	qv := session.Questions[0].QuestionVersionID

	// 跳过
	session, err = s.Practice.PracticeSkipQuestion(ctx(), PracticeSkipQuestionReq{
		WorkspaceID: ws.ID, SessionID: session.ID, QuestionVersionID: qv,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(session.Skipped) != 1 {
		t.Fatalf("expected 1 skipped, got %+v", session.Skipped)
	}

	result, err := s.Practice.PracticeSubmit(ctx(), PracticeSubmitReq{
		WorkspaceID: ws.ID, SessionID: session.ID, Version: session.Version,
		IdempotencyKey: "psub-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.TotalScore != 0 {
		t.Fatalf("skipped should be 0: %+v", result)
	}
	if !result.Questions[0].Skipped {
		t.Fatal("question should be marked skipped")
	}

	// 错题应进入复习
	due, err := s.Review.ReviewListDue(ctx(), ReviewListDueReq{WorkspaceID: ws.ID, UserID: userID})
	if err != nil {
		t.Fatal(err)
	}
	if len(due) == 0 {
		t.Fatal("expected due review card")
	}
	card := due[0]
	if card.Question == nil {
		t.Fatal("review card should include question")
	}

	// 更新错因
	wrongs, err := s.Review.WrongAnswerList(ctx(), WrongAnswerListReq{WorkspaceID: ws.ID, UserID: userID})
	if err != nil {
		t.Fatal(err)
	}
	if len(wrongs.Items) != 1 {
		t.Fatalf("expected 1 wrong answer, got %d", len(wrongs.Items))
	}
	wa := wrongs.Items[0]
	updated, err := s.Review.WrongAnswerUpdateCause(ctx(), WrongAnswerUpdateCauseReq{
		WorkspaceID: ws.ID, WrongID: wa.ID, Version: wa.Version, Cause: "concept",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Cause != "concept" {
		t.Fatalf("cause update failed: %s", updated.Cause)
	}
}

func TestReviewSM2(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)

	q := publishedQuestion(t, s, ws.ID, scPayload("复习测试", "A"))
	session, err := s.Practice.PracticeStart(ctx(), PracticeStartReq{
		WorkspaceID: ws.ID, UserID: userID, Mode: "practice",
		QuestionIDs: []string{q.ID}, IdempotencyKey: "ps-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Practice.PracticeSaveAnswer(ctx(), PracticeSaveAnswerReq{
		WorkspaceID: ws.ID, SessionID: session.ID,
		QuestionVersionID: session.Questions[0].QuestionVersionID,
		Answer: json.RawMessage(`"B"`), ClientSequence: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Practice.PracticeSubmit(ctx(), PracticeSubmitReq{
		WorkspaceID: ws.ID, SessionID: session.ID, Version: session.Version,
		IdempotencyKey: "psub-" + NewID(),
	}); err != nil {
		t.Fatal(err)
	}

	due, err := s.Review.ReviewListDue(ctx(), ReviewListDueReq{WorkspaceID: ws.ID, UserID: userID})
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("expected 1 due card, got %d", len(due))
	}
	card := due[0]

	// 评级 good → interval 1 天
	updated, err := s.Review.ReviewSubmit(ctx(), ReviewSubmitReq{
		WorkspaceID: ws.ID, ReviewCardID: card.ID, Rating: "good", IdempotencyKey: "rv-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Repetition != 1 || updated.IntervalDays != 1 {
		t.Fatalf("unexpected sm2 result: %+v", updated)
	}
	// again → 重置
	updated, err = s.Review.ReviewSubmit(ctx(), ReviewSubmitReq{
		WorkspaceID: ws.ID, ReviewCardID: card.ID, Rating: "again", IdempotencyKey: "rv-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Repetition != 0 || updated.IntervalDays != 1 || updated.EaseFactor >= 2.5 {
		t.Fatalf("unexpected sm2 again: %+v", updated)
	}
	// 历史
	hist, err := s.Review.ReviewHistoryList(ctx(), ReviewHistoryListReq{
		WorkspaceID: ws.ID, ReviewCardID: card.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hist.Items) != 2 {
		t.Fatalf("expected 2 events, got %d", len(hist.Items))
	}
	// 非法评级
	if _, err := s.Review.ReviewSubmit(ctx(), ReviewSubmitReq{
		WorkspaceID: ws.ID, ReviewCardID: card.ID, Rating: "easy", IdempotencyKey: "rv-" + NewID(),
	}); err == nil {
		t.Fatal("expected INVALID_ARGUMENT for bad rating")
	}
	_ = domain.CodeInvalidArgument
}

func TestPracticeScoping(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	ws2, _ := createWorkspace(t, s)

	q := publishedQuestion(t, s, ws.ID, scPayload("隔离测试", "A"))

	// 其他工作区无法开始
	if _, err := s.Practice.PracticeStart(ctx(), PracticeStartReq{
		WorkspaceID: ws2.ID, UserID: userID, Mode: "practice",
		QuestionIDs: []string{q.ID}, IdempotencyKey: "ps-" + NewID(),
	}); err == nil {
		t.Fatal("expected error cross-workspace question")
	}
	// 未发布题不能练习
	qd, err := s.Knowledge.QuestionCreateDraft(ctx(), QuestionCreateDraftReq{
		WorkspaceID: ws.ID, Payload: scPayload("未发布", "A"), IdempotencyKey: "pq-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Practice.PracticeStart(ctx(), PracticeStartReq{
		WorkspaceID: ws.ID, UserID: userID, Mode: "practice",
		QuestionIDs: []string{qd.ID}, IdempotencyKey: "ps-" + NewID(),
	}); err == nil {
		t.Fatal("expected INVALID_STATE for unpublished question")
	}
}
