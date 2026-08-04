package service

import (
	"encoding/json"
	"testing"
)

func TestDashboardEmpty(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)

	dash, err := s.Dashboard.DashboardGet(ctx(), DashboardGetReq{WorkspaceID: ws.ID, UserID: userID})
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}
	if !dash.HasEmptyLibrary {
		t.Fatal("empty library should be flagged")
	}
	if dash.TodayTasks.Total != 0 || dash.DueReviews != 0 || dash.StreakDays != 0 {
		t.Fatalf("unexpected empty dashboard: %+v", dash)
	}
	if dash.AIAdvice == "" {
		t.Fatal("advice missing")
	}
}

func TestDashboardWithData(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)

	// 知识点 + 题目
	kn, err := s.Knowledge.KnowledgeCreate(ctx(), KnowledgeCreateReq{WorkspaceID: ws.ID, Name: "薄弱点A"})
	if err != nil {
		t.Fatal(err)
	}
	payload := mustJSON(map[string]any{
		"type": "single_choice", "stem": "1+1=?", 
		"options": []map[string]any{{"key": "A", "text": "1"}, {"key": "B", "text": "2"}},
		"answer": "B", "knowledge_ids": []string{kn.ID},
	})
	q := publishedQuestion(t, s, ws.ID, payload)

	// 练习并答错 → 错题 + 复习卡
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
		Answer: json.RawMessage(`"A"`), ClientSequence: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Practice.PracticeSubmit(ctx(), PracticeSubmitReq{
		WorkspaceID: ws.ID, SessionID: session.ID, Version: session.Version,
		IdempotencyKey: "psub-" + NewID(),
	}); err != nil {
		t.Fatal(err)
	}

	dash, err := s.Dashboard.DashboardGet(ctx(), DashboardGetReq{WorkspaceID: ws.ID, UserID: userID})
	if err != nil {
		t.Fatal(err)
	}
	if dash.HasEmptyLibrary {
		t.Fatal("library should not be empty")
	}
	if dash.RecentAccuracy.Total != 1 || dash.RecentAccuracy.Correct != 0 {
		t.Fatalf("unexpected accuracy: %+v", dash.RecentAccuracy)
	}
	if dash.DueReviews != 1 {
		t.Fatalf("expected 1 due review, got %d", dash.DueReviews)
	}
	if len(dash.WeakKnowledge) != 1 || dash.WeakKnowledge[0].Name != "薄弱点A" {
		t.Fatalf("unexpected weak knowledge: %+v", dash.WeakKnowledge)
	}
	if dash.StreakDays != 1 {
		t.Fatalf("expected streak 1, got %d", dash.StreakDays)
	}
	if dash.AIAdvice == "" {
		t.Fatal("advice missing")
	}
}
