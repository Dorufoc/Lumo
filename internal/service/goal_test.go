package service

import (
	"context"
	"testing"
	"time"

	"lumo/internal/domain"
)

func futureDate(days int) string {
	return time.Now().UTC().AddDate(0, 0, days).Format(time.RFC3339)
}

func TestGoalLifecycle(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)

	exam := futureDate(30)
	key := "goal-" + NewID()
	g1, err := s.Goal.GoalCreate(ctx, GoalCreateReq{
		WorkspaceID: ws.ID, UserID: userID, Name: "高数期末", Subject: "math",
		ExamAt: &exam, TargetScore: float64Ptr(85), DailyMinutes: 60,
		AvailableWeekdays: []int{1, 2, 3, 4, 5}, IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	if g1.Status != "draft" {
		t.Fatalf("expected draft, got %s", g1.Status)
	}
	// 幂等
	g2, err := s.Goal.GoalCreate(ctx, GoalCreateReq{
		WorkspaceID: ws.ID, UserID: userID, Name: "高数期末", Subject: "math",
		ExamAt: &exam, TargetScore: float64Ptr(85), DailyMinutes: 60,
		AvailableWeekdays: []int{1, 2, 3, 4, 5}, IdempotencyKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	if g1.ID != g2.ID {
		t.Fatal("idempotency violated")
	}

	// 非法：过去日期
	past := time.Now().UTC().AddDate(0, 0, -1).Format(time.RFC3339)
	if _, err := s.Goal.GoalCreate(ctx, GoalCreateReq{
		WorkspaceID: ws.ID, UserID: userID, Name: "x", ExamAt: &past,
		DailyMinutes: 30, IdempotencyKey: "goal-" + NewID(),
	}); err == nil {
		t.Fatal("expected error for past exam_at")
	}

	// 列表
	goals, err := s.Goal.GoalList(ctx, GoalListReq{WorkspaceID: ws.ID, UserID: userID})
	if err != nil {
		t.Fatal(err)
	}
	if len(goals) != 1 {
		t.Fatalf("expected 1 goal, got %d", len(goals))
	}

	// 更新
	newName := "高数期末冲刺"
	updated, err := s.Goal.GoalUpdate(ctx, GoalUpdateReq{
		WorkspaceID: ws.ID, GoalID: g1.ID, Version: g1.Version, Name: &newName,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != newName {
		t.Fatalf("update failed: %s", updated.Name)
	}

	// 状态机
	if _, err := s.Goal.GoalTransition(ctx, GoalTransitionReq{
		WorkspaceID: ws.ID, GoalID: g1.ID, Version: updated.Version, Action: "complete",
	}); err == nil {
		t.Fatal("expected INVALID_STATE draft->complete")
	}
	g, err := s.Goal.GoalTransition(ctx, GoalTransitionReq{
		WorkspaceID: ws.ID, GoalID: g1.ID, Version: updated.Version, Action: "activate",
	})
	if err != nil {
		t.Fatal(err)
	}
	if g.Status != "active" {
		t.Fatalf("expected active, got %s", g.Status)
	}
	g, err = s.Goal.GoalTransition(ctx, GoalTransitionReq{
		WorkspaceID: ws.ID, GoalID: g1.ID, Version: g.Version, Action: "pause",
	})
	if err != nil {
		t.Fatal(err)
	}
	if g.Status != "paused" {
		t.Fatalf("expected paused, got %s", g.Status)
	}
	g, err = s.Goal.GoalTransition(ctx, GoalTransitionReq{
		WorkspaceID: ws.ID, GoalID: g1.ID, Version: g.Version, Action: "archive",
	})
	if err != nil {
		t.Fatal(err)
	}
	if g.Status != "archived" {
		t.Fatalf("expected archived, got %s", g.Status)
	}
}

func TestPlanGenerateAndTransition(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)

	exam := futureDate(60)
	goal, err := s.Goal.GoalCreate(ctx, GoalCreateReq{
		WorkspaceID: ws.ID, UserID: userID, Name: "四级备考", Subject: "english",
		ExamAt: &exam, DailyMinutes: 90,
		AvailableWeekdays: []int{1, 2, 3, 4, 5, 6, 7}, IdempotencyKey: "goal-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Goal.GoalTransition(ctx, GoalTransitionReq{
		WorkspaceID: ws.ID, GoalID: goal.ID, Version: goal.Version, Action: "activate",
	}); err != nil {
		t.Fatal(err)
	}

	start := time.Now().UTC().Truncate(24 * time.Hour).Format(time.RFC3339)
	end := time.Now().UTC().AddDate(0, 0, 6).Truncate(24 * time.Hour).Format(time.RFC3339)
	tasks, err := s.Goal.PlanGenerate(ctx, PlanGenerateReq{
		WorkspaceID: ws.ID, GoalID: goal.ID,
		RangeStart: start, RangeEnd: end, IdempotencyKey: "plan-" + NewID(),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	// 7 天 × 90 分钟 = 每天 2 个任务（60+30）→ 14 个
	if len(tasks) != 14 {
		t.Fatalf("expected 14 tasks, got %d", len(tasks))
	}
	for _, task := range tasks {
		if task.Status != "planned" || task.TaskType != "practice" {
			t.Fatalf("unexpected task: %+v", task)
		}
		if len(task.ReasonCodes) == 0 || task.GeneratedReason == "" {
			t.Fatalf("task missing reason: %+v", task)
		}
	}

	// 再次生成不重复（幂等补缺）
	tasks2, err := s.Goal.PlanGenerate(ctx, PlanGenerateReq{
		WorkspaceID: ws.ID, GoalID: goal.ID,
		RangeStart: start, RangeEnd: end, IdempotencyKey: "plan-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks2) != 14 {
		t.Fatalf("expected 14 (no duplicates), got %d", len(tasks2))
	}

	// 今日任务
	today, err := s.Goal.PlanListToday(ctx, PlanListTodayReq{WorkspaceID: ws.ID, UserID: userID})
	if err != nil {
		t.Fatal(err)
	}
	if len(today) != 2 {
		t.Fatalf("expected 2 today tasks, got %d", len(today))
	}

	// 任务状态机
	task := today[0]
	t2, err := s.Goal.PlanTaskTransition(ctx, PlanTaskTransitionReq{
		WorkspaceID: ws.ID, TaskID: task.ID, Version: task.Version, Action: "start",
	})
	if err != nil {
		t.Fatal(err)
	}
	if t2.Status != "in_progress" {
		t.Fatalf("expected in_progress, got %s", t2.Status)
	}
	// 非法：planned→complete（需先 start）——today[1] 还是 planned
	if _, err := s.Goal.PlanTaskTransition(ctx, PlanTaskTransitionReq{
		WorkspaceID: ws.ID, TaskID: today[1].ID, Version: today[1].Version, Action: "complete",
	}); err == nil {
		t.Fatal("expected INVALID_STATE planned->complete")
	}
	t2, err = s.Goal.PlanTaskTransition(ctx, PlanTaskTransitionReq{
		WorkspaceID: ws.ID, TaskID: task.ID, Version: t2.Version, Action: "complete",
	})
	if err != nil {
		t.Fatal(err)
	}
	if t2.Status != "completed" {
		t.Fatalf("expected completed, got %s", t2.Status)
	}
	// skip + restore
	t2, err = s.Goal.PlanTaskTransition(ctx, PlanTaskTransitionReq{
		WorkspaceID: ws.ID, TaskID: today[1].ID, Version: today[1].Version, Action: "skip",
		Reason: "状态不好",
	})
	if err != nil {
		t.Fatal(err)
	}
	if t2.Status != "skipped" {
		t.Fatalf("expected skipped, got %s", t2.Status)
	}
	t2, err = s.Goal.PlanTaskTransition(ctx, PlanTaskTransitionReq{
		WorkspaceID: ws.ID, TaskID: today[1].ID, Version: t2.Version, Action: "restore",
	})
	if err != nil {
		t.Fatal(err)
	}
	if t2.Status != "planned" {
		t.Fatalf("expected planned after restore, got %s", t2.Status)
	}
}

func TestPlanGenerateWeekendSkip(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)

	// 只有周末可学
	goal, err := s.Goal.GoalCreate(ctx, GoalCreateReq{
		WorkspaceID: ws.ID, UserID: userID, Name: "周末学习", DailyMinutes: 60,
		AvailableWeekdays: []int{6, 7}, IdempotencyKey: "goal-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Truncate(24 * time.Hour).Format(time.RFC3339)
	end := time.Now().UTC().AddDate(0, 0, 13).Truncate(24 * time.Hour).Format(time.RFC3339)
	tasks, err := s.Goal.PlanGenerate(ctx, PlanGenerateReq{
		WorkspaceID: ws.ID, GoalID: goal.ID,
		RangeStart: start, RangeEnd: end, IdempotencyKey: "plan-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 4 { // 14 天里 2 个周末 → 4 个任务
		t.Fatalf("expected 4 weekend tasks, got %d", len(tasks))
	}
}

func float64Ptr(v float64) *float64 { return &v }

func TestGoalScoping(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)

	if _, err := s.Goal.GoalCreate(ctx, GoalCreateReq{
		WorkspaceID: ws.ID, UserID: userID, Name: "x", DailyMinutes: 30,
		IdempotencyKey: "goal-" + NewID(),
	}); err != nil {
		t.Fatal(err)
	}
	// 其他工作区不可见
	ws2, _ := createWorkspace(t, s)
	goals, err := s.Goal.GoalList(ctx, GoalListReq{WorkspaceID: ws2.ID, UserID: userID})
	if err != nil {
		t.Fatal(err)
	}
	if len(goals) != 0 {
		t.Fatalf("cross-workspace leak: %d", len(goals))
	}
	if _, err := s.Goal.GoalTransition(ctx, GoalTransitionReq{
		WorkspaceID: ws2.ID, GoalID: "no-such", Version: 1, Action: "activate",
	}); err == nil {
		t.Fatal("expected NOT_FOUND")
	}
	_ = domain.CodeNotFound
}
