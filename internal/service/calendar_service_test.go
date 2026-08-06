package service

import (
	"encoding/json"
	"testing"
	"time"

	"lumo/internal/domain"
	"lumo/internal/repository"
)

// seedGoal 创建学习目标（供里程碑测试复用）。
func seedGoal(t *testing.T, s *Services, wsID, userID string) *LearningGoal {
	t.Helper()
	g, err := s.Goal.GoalCreate(ctx(), GoalCreateReq{
		WorkspaceID: wsID, UserID: userID, Name: "数学目标", Subject: "math",
		DailyMinutes: 30, AvailableWeekdays: []int{1, 2, 3, 4, 5},
		IdempotencyKey: "goal-" + NewID(),
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	return g
}

// monthOf 返回本地当前月份（YYYY-MM）。
func monthOf() string { return time.Now().Format("2006-01") }

func TestCalendarGetMonthEmpty(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)

	m, err := s.Calendar.CalendarGetMonth(ctx(), CalendarGetMonthReq{
		WorkspaceID: ws.ID, UserID: userID, Month: monthOf(),
	})
	if err != nil {
		t.Fatalf("get month: %v", err)
	}
	if m.Month != monthOf() {
		t.Fatalf("month echo mismatch: %s != %s", m.Month, monthOf())
	}
	if len(m.Entries) != 0 {
		t.Fatalf("expected 0 entries, got %d: %+v", len(m.Entries), m.Entries)
	}
}

func TestCalendarGetMonthAggregates(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)

	// 1) 计划任务（due_at 为本地当天 RFC3339）
	if err := s.Repo.CreatePlanTask(ctx(), &repository.PlanTaskRow{
		ID: NewID(), WorkspaceID: ws.ID, UserID: userID, TaskType: "practice",
		DueAt: time.Now().Local().Format(time.RFC3339), DurationMin: 30, Priority: 50,
		ReasonCodes: mustJSON([]string{"PLAN_INIT"}), GeneratedReason: "测试任务",
	}); err != nil {
		t.Fatalf("create plan task: %v", err)
	}

	// 2) 复习卡（答错一题触发错题归档 + 复习卡）
	q := publishedQuestion(t, s, ws.ID, scPayload("1+1=?", "B"))
	sess, err := s.Practice.PracticeStart(ctx(), PracticeStartReq{
		WorkspaceID: ws.ID, UserID: userID, Mode: "practice",
		QuestionIDs: []string{q.ID}, IdempotencyKey: "ps-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	qv := sess.Questions[0].QuestionVersionID
	if _, err := s.Practice.PracticeSaveAnswer(ctx(), PracticeSaveAnswerReq{
		WorkspaceID: ws.ID, SessionID: sess.ID, QuestionVersionID: qv,
		Answer: json.RawMessage(`"A"`), ClientSequence: 1, // 错误答案
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Practice.PracticeSubmit(ctx(), PracticeSubmitReq{
		WorkspaceID: ws.ID, SessionID: sess.ID, Version: sess.Version,
		IdempotencyKey: "psub-" + NewID(),
	}); err != nil {
		t.Fatal(err)
	}

	// 3) 打卡
	if _, err := s.Checkin.CheckinCreate(ctx(), CheckinCreateReq{
		WorkspaceID: ws.ID, UserID: userID, Minutes: 30, IdempotencyKey: "ck-" + NewID(),
	}); err != nil {
		t.Fatalf("checkin: %v", err)
	}

	// 4) 专注会话（开始+结束）
	ts, err := s.Focus.TimerStart(ctx(), TimerStartReq{
		WorkspaceID: ws.ID, UserID: userID, Mode: "pomodoro", PlannedMinutes: 25,
		IdempotencyKey: "tm-" + NewID(),
	})
	if err != nil {
		t.Fatalf("timer start: %v", err)
	}
	if _, err := s.Focus.TimerEnd(ctx(), TimerEndReq{
		WorkspaceID: ws.ID, UserID: userID, SessionID: ts.ID,
	}); err != nil {
		t.Fatalf("timer end: %v", err)
	}

	// 5) 考试（试卷 + 考试行）
	paperID := NewID()
	if err := s.Repo.CreateExamPaperTx(ctx(), &repository.ExamPaperRow{
		ID: paperID, WorkspaceID: ws.ID, UserID: userID, Title: "期中测试", ConfigJSON: "{}",
	}, nil); err != nil {
		t.Fatalf("create paper: %v", err)
	}
	started := time.Now().Local().Format(time.RFC3339)
	if err := s.Repo.CreateExam(ctx(), &repository.ExamRow{
		ID: NewID(), PaperID: paperID, UserID: userID, Status: "answering", StartedAt: &started,
	}); err != nil {
		t.Fatalf("create exam: %v", err)
	}

	// 6) 个人事件
	if _, err := s.Calendar.CalendarEventUpsert(ctx(), CalendarEventUpsertReq{
		WorkspaceID: ws.ID, UserID: userID, Kind: domain.CalendarKindPersonal,
		EventDate: time.Now().Local().Format("2006-01-02"), StartTime: strPtr("14:00"),
		DurationMin: 60, Title: "家长会", IdempotencyKey: "ce-" + NewID(),
	}); err != nil {
		t.Fatalf("upsert personal event: %v", err)
	}

	m, err := s.Calendar.CalendarGetMonth(ctx(), CalendarGetMonthReq{
		WorkspaceID: ws.ID, UserID: userID, Month: monthOf(),
	})
	if err != nil {
		t.Fatalf("get month: %v", err)
	}
	kinds := map[string]int{}
	for _, e := range m.Entries {
		kinds[e.Kind]++
	}
	for _, want := range []string{
		domain.CalendarKindTask, domain.CalendarKindReview, domain.CalendarKindCheckin,
		domain.CalendarKindFocus, domain.CalendarKindExam, domain.CalendarKindPersonal,
	} {
		if kinds[want] == 0 {
			t.Fatalf("missing kind %q in month entries: %+v", want, m.Entries)
		}
	}
	// 个人事件应带 title 与 event_date
	for _, e := range m.Entries {
		if e.Kind == domain.CalendarKindPersonal && e.Title != "家长会" {
			t.Fatalf("personal event title mismatch: %+v", e)
		}
	}
}

func TestCalendarGetMonthInvalidMonth(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	if _, err := s.Calendar.CalendarGetMonth(ctx(), CalendarGetMonthReq{
		WorkspaceID: ws.ID, UserID: userID, Month: "2026-13",
	}); err == nil {
		t.Fatal("expected INVALID_ARGUMENT for bad month")
	} else if domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("expected INVALID_ARGUMENT, got %s", domain.AsError(err).Code)
	}
	if _, err := s.Calendar.CalendarGetMonth(ctx(), CalendarGetMonthReq{
		WorkspaceID: ws.ID, UserID: "", Month: monthOf(),
	}); err == nil {
		t.Fatal("expected INVALID_ARGUMENT for empty user_id")
	}
}

func TestCalendarEventUpsertPersonalLifecycle(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)

	created, err := s.Calendar.CalendarEventUpsert(ctx(), CalendarEventUpsertReq{
		WorkspaceID: ws.ID, UserID: userID, Kind: domain.CalendarKindPersonal,
		EventDate: "2026-08-10", StartTime: strPtr("09:30"), DurationMin: 45,
		Title: "备考冲刺", IdempotencyKey: "ce-" + NewID(),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == "" || created.Kind != domain.CalendarKindPersonal || created.Title != "备考冲刺" {
		t.Fatalf("unexpected created event: %+v", created)
	}
	if created.StartTime == nil || *created.StartTime != "09:30" {
		t.Fatalf("start_time not persisted: %+v", created)
	}

	// 更新（同事件 id）
	updated, err := s.Calendar.CalendarEventUpsert(ctx(), CalendarEventUpsertReq{
		WorkspaceID: ws.ID, UserID: userID, EventID: created.ID, Kind: domain.CalendarKindPersonal,
		EventDate: "2026-08-11", DurationMin: 90, Title: "备考冲刺·改期",
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.ID != created.ID || updated.Title != "备考冲刺·改期" || updated.EventDate != "2026-08-11" {
		t.Fatalf("update failed: %+v", updated)
	}
}

func TestCalendarEventUpsertRejectsNonPersonal(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	for _, kind := range []string{
		domain.CalendarKindTask, domain.CalendarKindReview,
		domain.CalendarKindExam, domain.CalendarKindCheckin, domain.CalendarKindFocus,
	} {
		if _, err := s.Calendar.CalendarEventUpsert(ctx(), CalendarEventUpsertReq{
			WorkspaceID: ws.ID, UserID: userID, Kind: kind,
			EventDate: "2026-08-10", Title: "x", IdempotencyKey: "ce-" + NewID(),
		}); err == nil {
			t.Fatalf("expected INVALID_ARGUMENT for kind=%s", kind)
		} else if domain.AsError(err).Code != domain.CodeInvalidArgument {
			t.Fatalf("kind=%s expected INVALID_ARGUMENT, got %s", kind, domain.AsError(err).Code)
		}
	}
	if _, err := s.Calendar.CalendarEventUpsert(ctx(), CalendarEventUpsertReq{
		WorkspaceID: ws.ID, UserID: userID, Kind: "hack",
		EventDate: "2026-08-10", Title: "x", IdempotencyKey: "ce-" + NewID(),
	}); err == nil {
		t.Fatal("expected INVALID_ARGUMENT for unknown kind")
	}
	// 非法日期
	if _, err := s.Calendar.CalendarEventUpsert(ctx(), CalendarEventUpsertReq{
		WorkspaceID: ws.ID, UserID: userID, Kind: domain.CalendarKindPersonal,
		EventDate: "2026/08/10", Title: "x", IdempotencyKey: "ce-" + NewID(),
	}); err == nil {
		t.Fatal("expected INVALID_ARGUMENT for bad event_date")
	}
	// 非法 start_time
	if _, err := s.Calendar.CalendarEventUpsert(ctx(), CalendarEventUpsertReq{
		WorkspaceID: ws.ID, UserID: userID, Kind: domain.CalendarKindPersonal,
		EventDate: "2026-08-10", StartTime: strPtr("25:99"), Title: "x",
		IdempotencyKey: "ce-" + NewID(),
	}); err == nil {
		t.Fatal("expected INVALID_ARGUMENT for bad start_time")
	}
}

func TestMilestoneCreate(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	goal := seedGoal(t, s, ws.ID, userID)

	m, err := s.Calendar.MilestoneCreate(ctx(), MilestoneCreateReq{
		WorkspaceID: ws.ID, UserID: userID, GoalID: goal.ID,
		Title: "完成基础 60%", DueAt: "2026-09-01",
		Criteria: mustJSON(map[string]any{"type": "practice", "count": 60, "min_accuracy": 0.8}),
		IdempotencyKey: "ms-" + NewID(),
	})
	if err != nil {
		t.Fatalf("create milestone: %v", err)
	}
	if m.ID == "" || m.GoalID != goal.ID || m.Title != "完成基础 60%" {
		t.Fatalf("unexpected milestone: %+v", m)
	}
	if m.Status != domain.MilestoneStatusPending || m.AchievedAt != nil {
		t.Fatalf("expected pending with no achieved_at: %+v", m)
	}
	var criteria map[string]any
	if err := json.Unmarshal(m.Criteria, &criteria); err != nil {
		t.Fatalf("criteria not persisted as json: %v", err)
	}
	if criteria["count"].(float64) != 60 {
		t.Fatalf("criteria count mismatch: %s", m.Criteria)
	}
}

func TestMilestoneCreateValidation(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	goal := seedGoal(t, s, ws.ID, userID)

	cases := []struct {
		name string
		req  MilestoneCreateReq
		code domain.ErrorCode
	}{
		{
			name: "bad criteria type", code: domain.CodeInvalidArgument,
			req: MilestoneCreateReq{
				WorkspaceID: ws.ID, UserID: userID, GoalID: goal.ID,
				Title: "x", DueAt: "2026-09-01",
				Criteria: mustJSON(map[string]any{"type": "bogus", "count": 1}),
				IdempotencyKey: "ms-" + NewID(),
			},
		},
		{
			name: "zero count", code: domain.CodeInvalidArgument,
			req: MilestoneCreateReq{
				WorkspaceID: ws.ID, UserID: userID, GoalID: goal.ID,
				Title: "x", DueAt: "2026-09-01",
				Criteria: mustJSON(map[string]any{"type": "practice", "count": 0}),
				IdempotencyKey: "ms-" + NewID(),
			},
		},
		{
			name: "tasks with min_accuracy", code: domain.CodeInvalidArgument,
			req: MilestoneCreateReq{
				WorkspaceID: ws.ID, UserID: userID, GoalID: goal.ID,
				Title: "x", DueAt: "2026-09-01",
				Criteria: mustJSON(map[string]any{"type": "tasks", "count": 1, "min_accuracy": 0.5}),
				IdempotencyKey: "ms-" + NewID(),
			},
		},
		{
			name: "bad due_at", code: domain.CodeInvalidArgument,
			req: MilestoneCreateReq{
				WorkspaceID: ws.ID, UserID: userID, GoalID: goal.ID,
				Title: "x", DueAt: "2026/09/01",
				Criteria: mustJSON(map[string]any{"type": "practice", "count": 1}),
				IdempotencyKey: "ms-" + NewID(),
			},
		},
		{
			name: "empty title", code: domain.CodeInvalidArgument,
			req: MilestoneCreateReq{
				WorkspaceID: ws.ID, UserID: userID, GoalID: goal.ID,
				Title: "   ", DueAt: "2026-09-01",
				Criteria: mustJSON(map[string]any{"type": "practice", "count": 1}),
				IdempotencyKey: "ms-" + NewID(),
			},
		},
		{
			name: "missing goal", code: domain.CodeNotFound,
			req: MilestoneCreateReq{
				WorkspaceID: ws.ID, UserID: userID, GoalID: "no-such-goal",
				Title: "x", DueAt: "2026-09-01",
				Criteria: mustJSON(map[string]any{"type": "practice", "count": 1}),
				IdempotencyKey: "ms-" + NewID(),
			},
		},
	}
	for _, c := range cases {
		if _, err := s.Calendar.MilestoneCreate(ctx(), c.req); err == nil {
			t.Fatalf("%s: expected error", c.name)
		} else if domain.AsError(err).Code != c.code {
			t.Fatalf("%s: expected %s, got %s", c.name, c.code, domain.AsError(err).Code)
		}
	}
}

func TestMilestoneEvaluatePractice(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	goal := seedGoal(t, s, ws.ID, userID)

	// 完成 2 道题并提交（1 对 1 错 → accuracy 0.5）
	q1 := publishedQuestion(t, s, ws.ID, scPayload("1+1=?", "B"))
	q2 := publishedQuestion(t, s, ws.ID, scPayload("2+2=?", "C"))
	sess, err := s.Practice.PracticeStart(ctx(), PracticeStartReq{
		WorkspaceID: ws.ID, UserID: userID, Mode: "practice",
		QuestionIDs: []string{q1.ID, q2.ID}, IdempotencyKey: "ps-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, q := range sess.Questions {
		ans := json.RawMessage(`"B"`)
		if i == 1 {
			ans = json.RawMessage(`"A"`) // q2 答错
		}
		if _, err := s.Practice.PracticeSaveAnswer(ctx(), PracticeSaveAnswerReq{
			WorkspaceID: ws.ID, SessionID: sess.ID, QuestionVersionID: q.QuestionVersionID,
			Answer: ans, ClientSequence: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Practice.PracticeSubmit(ctx(), PracticeSubmitReq{
		WorkspaceID: ws.ID, SessionID: sess.ID, Version: sess.Version,
		IdempotencyKey: "psub-" + NewID(),
	}); err != nil {
		t.Fatal(err)
	}

	m, err := s.Calendar.MilestoneCreate(ctx(), MilestoneCreateReq{
		WorkspaceID: ws.ID, UserID: userID, GoalID: goal.ID,
		Title: "做 2 题正确率 50%", DueAt: "2026-09-01",
		Criteria: mustJSON(map[string]any{"type": "practice", "count": 2, "min_accuracy": 0.5}),
		IdempotencyKey: "ms-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	eval, err := s.Calendar.MilestoneEvaluate(ctx(), MilestoneEvaluateReq{
		WorkspaceID: ws.ID, UserID: userID, MilestoneID: m.ID,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if eval.Status != domain.MilestoneStatusAchieved || eval.AchievedAt == nil {
		t.Fatalf("expected achieved: %+v", eval)
	}

	// 再建一个未达成里程碑（需要 10 题）→ not_met
	m2, err := s.Calendar.MilestoneCreate(ctx(), MilestoneCreateReq{
		WorkspaceID: ws.ID, UserID: userID, GoalID: goal.ID,
		Title: "做 10 题", DueAt: "2026-09-01",
		Criteria: mustJSON(map[string]any{"type": "practice", "count": 10}),
		IdempotencyKey: "ms-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	eval2, err := s.Calendar.MilestoneEvaluate(ctx(), MilestoneEvaluateReq{
		WorkspaceID: ws.ID, UserID: userID, MilestoneID: m2.ID,
	})
	if err != nil {
		t.Fatalf("evaluate 2: %v", err)
	}
	if eval2.Status != domain.MilestoneStatusNotMet {
		t.Fatalf("expected not_met: %+v", eval2)
	}
}

func TestMilestoneEvaluateTasks(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	goal := seedGoal(t, s, ws.ID, userID)

	// 完成 2 个计划任务
	for i := 0; i < 2; i++ {
		task := &repository.PlanTaskRow{
			ID: NewID(), WorkspaceID: ws.ID, UserID: userID, GoalID: &goal.ID,
			TaskType: "practice", DueAt: Now(), DurationMin: 30, Priority: 50,
			ReasonCodes: mustJSON([]string{"PLAN_INIT"}), GeneratedReason: "t",
		}
		if err := s.Repo.CreatePlanTask(ctx(), task); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Repo.UpdatePlanTaskStatus(ctx(), ws.ID, task.ID, 1, "completed"); err != nil {
			t.Fatal(err)
		}
	}

	m, err := s.Calendar.MilestoneCreate(ctx(), MilestoneCreateReq{
		WorkspaceID: ws.ID, UserID: userID, GoalID: goal.ID,
		Title: "完成 2 个任务", DueAt: "2026-09-01",
		Criteria: mustJSON(map[string]any{"type": "tasks", "count": 2}),
		IdempotencyKey: "ms-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	eval, err := s.Calendar.MilestoneEvaluate(ctx(), MilestoneEvaluateReq{
		WorkspaceID: ws.ID, UserID: userID, MilestoneID: m.ID,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if eval.Status != domain.MilestoneStatusAchieved {
		t.Fatalf("expected achieved: %+v", eval)
	}

	// 未达成（需要 3 个任务）
	m2, err := s.Calendar.MilestoneCreate(ctx(), MilestoneCreateReq{
		WorkspaceID: ws.ID, UserID: userID, GoalID: goal.ID,
		Title: "完成 3 个任务", DueAt: "2026-09-01",
		Criteria: mustJSON(map[string]any{"type": "tasks", "count": 3}),
		IdempotencyKey: "ms-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	eval2, err := s.Calendar.MilestoneEvaluate(ctx(), MilestoneEvaluateReq{
		WorkspaceID: ws.ID, UserID: userID, MilestoneID: m2.ID,
	})
	if err != nil {
		t.Fatalf("evaluate 2: %v", err)
	}
	if eval2.Status != domain.MilestoneStatusNotMet {
		t.Fatalf("expected not_met: %+v", eval2)
	}
}

func TestMilestoneEvaluateMissing(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	if _, err := s.Calendar.MilestoneEvaluate(ctx(), MilestoneEvaluateReq{
		WorkspaceID: ws.ID, UserID: userID, MilestoneID: "no-such-milestone",
	}); err == nil {
		t.Fatal("expected NOT_FOUND")
	} else if domain.AsError(err).Code != domain.CodeNotFound {
		t.Fatalf("expected NOT_FOUND, got %s", domain.AsError(err).Code)
	}
}
