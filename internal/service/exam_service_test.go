package service

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"lumo/internal/domain"
)

// ---- 辅助 ----

// examSection 构造 config_json 中的一道大题。
func examSection(title string, orderNo int, qvIDs []string, score int) map[string]any {
	return map[string]any{
		"title":               title,
		"order_no":            orderNo,
		"question_version_ids": qvIDs,
		"score":               score,
	}
}

// examPaperConfig 构造试卷 config_json（duration_min + sections）。
func examPaperConfig(durationMin int, sections []map[string]any) json.RawMessage {
	return mustJSON(map[string]any{
		"duration_min": durationMin,
		"sections":     sections,
	})
}

// examFixedTime 解析固定 RFC3339 时间（与 domain.NowUTC 同精度）。
func examFixedTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// examPubQuestion 创建并发布一道带知识点/难度的单选题。
func examPubQuestion(t *testing.T, s *Services, wsID string, stem string, kIDs []string, difficulty int) *Question {
	t.Helper()
	payload := mustJSON(map[string]any{
		"type": "single_choice",
		"stem": stem,
		"options": []map[string]any{
			{"key": "A", "text": "A"}, {"key": "B", "text": "B"}, {"key": "C", "text": "C"},
		},
		"answer":        "A",
		"difficulty":    difficulty,
		"knowledge_ids": kIDs,
	})
	return publishedQuestion(t, s, wsID, payload)
}

// examNotificationCount 统计用户某类通知条数（exam:auto_submitted 事件落库断言）。
func examNotificationCount(t *testing.T, s *Services, userID, kind string) int {
	t.Helper()
	var n int
	err := s.Repo.DB().QueryRowContext(ctx(),
		`SELECT COUNT(*) FROM notifications WHERE user_id = ? AND kind = ?`, userID, kind).Scan(&n)
	if err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	return n
}

// examRowStatus 直读 exams 行状态（服务内未暴露的状态断言）。
func examRowStatus(t *testing.T, s *Services, examID string) string {
	t.Helper()
	var status string
	err := s.Repo.DB().QueryRowContext(ctx(),
		`SELECT status FROM exams WHERE id = ?`, examID).Scan(&status)
	if err != nil {
		t.Fatalf("read exam status: %v", err)
	}
	return status
}

// ---- 场景 1：完整链路 组卷→发布→开始→答题→自动提交→结果 ----

func TestExamFullFlow(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)

	q1 := publishedQuestion(t, s, ws.ID, scPayload("1+1=?", "B"))
	q2 := publishedQuestion(t, s, ws.ID, scPayload("2+2=?", "C"))
	qv1 := q1.CurrentVersion.ID
	qv2 := q2.CurrentVersion.ID

	// 草稿卷
	paper, err := s.Exam.ExamPaperCreate(ctx(), ExamPaperCreateReq{
		WorkspaceID: ws.ID, UserID: userID, Title: "期中模拟",
		ConfigJSON: examPaperConfig(60, []map[string]any{
			examSection("第一部分", 1, []string{qv1, qv2}, 10),
		}),
		IdempotencyKey: "epc-" + NewID(),
	})
	if err != nil {
		t.Fatalf("create paper: %v", err)
	}
	if paper.Status != "draft" || paper.Version != 1 || len(paper.Sections) != 1 {
		t.Fatalf("unexpected paper: %+v", paper)
	}
	if paper.Sections == nil {
		t.Fatal("sections should be non-nil, not null")
	}
	if paper.Sections[0].QuestionVersionIDs == nil {
		t.Fatal("question_version_ids should be non-nil empty slice, not null")
	}
	rawPaper, err := json.Marshal(paper)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(rawPaper, []byte(`"sections":[{`)) {
		t.Fatalf("sections should marshal to array: %s", rawPaper)
	}
	if !bytes.Contains(rawPaper, []byte(`"question_version_ids":["`)) {
		t.Fatalf("question_version_ids should marshal to array: %s", rawPaper)
	}

	// 未发布不能开始考试
	if _, err := s.Exam.ExamStart(ctx(), ExamStartReq{
		WorkspaceID: ws.ID, UserID: userID, PaperID: paper.ID, IdempotencyKey: "es-" + NewID(),
	}); err == nil {
		t.Fatal("expected error starting unpublished paper")
	} else if domain.AsError(err).Code != domain.CodeInvalidState {
		t.Fatalf("expected INVALID_STATE, got %s", domain.AsError(err).Code)
	}

	// 发布
	paper, err = s.Exam.ExamPaperPublish(ctx(), ExamPaperPublishReq{
		WorkspaceID: ws.ID, PaperID: paper.ID, Version: paper.Version,
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if paper.Status != "published" || paper.Version != 2 {
		t.Fatalf("unexpected published paper: %+v", paper)
	}

	// 开始考试：固定时钟，锁定时长与题目顺序
	t0 := examFixedTime("2026-08-06T10:00:00Z")
	s.Exam.Now = func() time.Time { return t0 }
	exam, err := s.Exam.ExamStart(ctx(), ExamStartReq{
		WorkspaceID: ws.ID, UserID: userID, PaperID: paper.ID, IdempotencyKey: "es-" + NewID(),
	})
	if err != nil {
		t.Fatalf("start exam: %v", err)
	}
	if exam.Status != "answering" || len(exam.Questions) != 2 || exam.DurationMin != 60 {
		t.Fatalf("unexpected exam: %+v", exam)
	}
	if exam.StartedAt == nil || *exam.StartedAt != "2026-08-06T10:00:00Z" {
		t.Fatalf("started_at not locked: %+v", exam.StartedAt)
	}
	if exam.Questions[0].QuestionVersionID != qv1 || exam.Questions[1].QuestionVersionID != qv2 {
		t.Fatal("question order not locked")
	}
	// 答题中不暴露标准答案
	for _, q := range exam.Questions {
		var payload map[string]any
		_ = json.Unmarshal(q.Payload, &payload)
		if payload["answer"] != nil {
			t.Fatal("answer leaked in answering exam")
		}
	}

	// 通过既有练习基础设施保存答案（共享会话 ID == exam.ID）
	if _, err := s.Practice.PracticeSaveAnswer(ctx(), PracticeSaveAnswerReq{
		WorkspaceID: ws.ID, SessionID: exam.ID, QuestionVersionID: qv1,
		Answer: json.RawMessage(`"B"`), ClientSequence: 1,
	}); err != nil {
		t.Fatalf("save q1: %v", err)
	}
	if _, err := s.Practice.PracticeSaveAnswer(ctx(), PracticeSaveAnswerReq{
		WorkspaceID: ws.ID, SessionID: exam.ID, QuestionVersionID: qv2,
		Answer: json.RawMessage(`"A"`), ClientSequence: 1,
	}); err != nil {
		t.Fatalf("save q2: %v", err)
	}

	// 倒计时到期：推进时钟 → 自动提交
	s.Exam.Now = func() time.Time { return t0.Add(61 * time.Minute) }
	result, err := s.Exam.ExamAutoSubmit(ctx(), ExamAutoSubmitReq{WorkspaceID: ws.ID, ExamID: exam.ID})
	if err != nil {
		t.Fatalf("auto submit: %v", err)
	}
	if result.Status != "graded" {
		t.Fatalf("unexpected status: %s", result.Status)
	}
	// q1 正确 10 分，q2 错误 0 分 → 10/20
	if result.TotalScore != 10 || result.MaxScore != 20 {
		t.Fatalf("unexpected score: %+v", result)
	}
	if len(result.WrongAnswers) != 1 || len(result.ReviewActions) != 1 {
		t.Fatalf("expected 1 wrong + 1 review action, got %+v", result)
	}
	if examRowStatus(t, s, exam.ID) != "graded" {
		t.Fatalf("exam should be graded, got %s", examRowStatus(t, s, exam.ID))
	}

	// 结果读取：成绩不变
	res, err := s.Exam.ExamGetResult(ctx(), ExamGetResultReq{WorkspaceID: ws.ID, ExamID: exam.ID})
	if err != nil {
		t.Fatalf("get result: %v", err)
	}
	if res.TotalScore != 10 || res.MaxScore != 20 || res.Status != "graded" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

// ---- 场景 2：进行中考试重入 → EXAM_IN_PROGRESS ----

func TestExamAntiReentry(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)

	q := publishedQuestion(t, s, ws.ID, scPayload("重入", "A"))
	paper, err := s.Exam.ExamPaperCreate(ctx(), ExamPaperCreateReq{
		WorkspaceID: ws.ID, UserID: userID, Title: "防重入卷",
		ConfigJSON: examPaperConfig(30, []map[string]any{
			examSection("第一部分", 1, []string{q.CurrentVersion.ID}, 10),
		}),
		IdempotencyKey: "epc-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	paper, err = s.Exam.ExamPaperPublish(ctx(), ExamPaperPublishReq{
		WorkspaceID: ws.ID, PaperID: paper.ID, Version: paper.Version,
	})
	if err != nil {
		t.Fatal(err)
	}

	first, err := s.Exam.ExamStart(ctx(), ExamStartReq{
		WorkspaceID: ws.ID, UserID: userID, PaperID: paper.ID, IdempotencyKey: "es-" + NewID(),
	})
	if err != nil {
		t.Fatalf("first start: %v", err)
	}

	second, err := s.Exam.ExamStart(ctx(), ExamStartReq{
		WorkspaceID: ws.ID, UserID: userID, PaperID: paper.ID, IdempotencyKey: "es2-" + NewID(),
	})
	if err == nil {
		t.Fatalf("expected EXAM_IN_PROGRESS, got second exam %+v", second)
	}
	if domain.AsError(err).Code != domain.CodeExamInProgress {
		t.Fatalf("expected EXAM_IN_PROGRESS, got %s", domain.AsError(err).Code)
	}
	_ = first

	// 其他用户不受影响
	ws2, user2 := createWorkspace(t, s)
	q2 := publishedQuestion(t, s, ws2.ID, scPayload("他人", "A"))
	paper2, err := s.Exam.ExamPaperCreate(ctx(), ExamPaperCreateReq{
		WorkspaceID: ws2.ID, UserID: user2, Title: "另一卷",
		ConfigJSON: examPaperConfig(30, []map[string]any{
			examSection("第一部分", 1, []string{q2.CurrentVersion.ID}, 10),
		}),
		IdempotencyKey: "epc-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	paper2, err = s.Exam.ExamPaperPublish(ctx(), ExamPaperPublishReq{
		WorkspaceID: ws2.ID, PaperID: paper2.ID, Version: paper2.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exam.ExamStart(ctx(), ExamStartReq{
		WorkspaceID: ws2.ID, UserID: user2, PaperID: paper2.ID, IdempotencyKey: "es-" + NewID(),
	}); err != nil {
		t.Fatalf("other user should start freely: %v", err)
	}
}

// ---- 场景 3：到期自动提交发布 exam:auto_submitted 事件 ----

func TestExamAutoSubmitPublishesEvent(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)

	q := publishedQuestion(t, s, ws.ID, scPayload("事件", "A"))
	paper, err := s.Exam.ExamPaperCreate(ctx(), ExamPaperCreateReq{
		WorkspaceID: ws.ID, UserID: userID, Title: "事件卷",
		ConfigJSON: examPaperConfig(10, []map[string]any{
			examSection("第一部分", 1, []string{q.CurrentVersion.ID}, 10),
		}),
		IdempotencyKey: "epc-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	paper, err = s.Exam.ExamPaperPublish(ctx(), ExamPaperPublishReq{
		WorkspaceID: ws.ID, PaperID: paper.ID, Version: paper.Version,
	})
	if err != nil {
		t.Fatal(err)
	}

	t0 := examFixedTime("2026-08-06T14:00:00Z")
	s.Exam.Now = func() time.Time { return t0 }
	exam, err := s.Exam.ExamStart(ctx(), ExamStartReq{
		WorkspaceID: ws.ID, UserID: userID, PaperID: paper.ID, IdempotencyKey: "es-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// 到期后任何入口（此处用 ExamGetResult）触发惰性自动提交
	s.Exam.Now = func() time.Time { return t0.Add(10 * time.Minute) }
	res, err := s.Exam.ExamGetResult(ctx(), ExamGetResultReq{WorkspaceID: ws.ID, ExamID: exam.ID})
	if err != nil {
		t.Fatalf("get result: %v", err)
	}
	if res.Status != "graded" {
		t.Fatalf("expected auto-submit on expiry, got %s", res.Status)
	}
	if n := examNotificationCount(t, s, userID, "exam:auto_submitted"); n != 1 {
		t.Fatalf("expected 1 exam:auto_submitted notification, got %d", n)
	}
}

// ---- 场景 4：未到期不自动提交 ----

func TestExamNoAutoSubmitBeforeExpiry(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)

	q := publishedQuestion(t, s, ws.ID, scPayload("未到期", "A"))
	paper, err := s.Exam.ExamPaperCreate(ctx(), ExamPaperCreateReq{
		WorkspaceID: ws.ID, UserID: userID, Title: "未到期卷",
		ConfigJSON: examPaperConfig(60, []map[string]any{
			examSection("第一部分", 1, []string{q.CurrentVersion.ID}, 10),
		}),
		IdempotencyKey: "epc-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	paper, err = s.Exam.ExamPaperPublish(ctx(), ExamPaperPublishReq{
		WorkspaceID: ws.ID, PaperID: paper.ID, Version: paper.Version,
	})
	if err != nil {
		t.Fatal(err)
	}

	t0 := examFixedTime("2026-08-06T08:00:00Z")
	s.Exam.Now = func() time.Time { return t0 }
	exam, err := s.Exam.ExamStart(ctx(), ExamStartReq{
		WorkspaceID: ws.ID, UserID: userID, PaperID: paper.ID, IdempotencyKey: "es-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// 30 分钟 < 60 分钟：ExamAutoSubmit 不得判分
	s.Exam.Now = func() time.Time { return t0.Add(30 * time.Minute) }
	res, err := s.Exam.ExamAutoSubmit(ctx(), ExamAutoSubmitReq{WorkspaceID: ws.ID, ExamID: exam.ID})
	if err != nil {
		t.Fatalf("auto submit before expiry should not error: %v", err)
	}
	if res.Status != "answering" {
		t.Fatalf("expected answering before expiry, got %s", res.Status)
	}
	if examRowStatus(t, s, exam.ID) != "answering" {
		t.Fatal("exam should still be answering")
	}
	if n := examNotificationCount(t, s, userID, "exam:auto_submitted"); n != 0 {
		t.Fatalf("no event expected before expiry, got %d", n)
	}
}

// ---- 场景 5：缺少 duration_min → ExamStart INVALID_ARGUMENT；惰性检查不触发 ----

func TestExamMissingDurationMin(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)

	q := publishedQuestion(t, s, ws.ID, scPayload("缺时长", "A"))

	// 5a: 无 duration_min 的卷 → 开始考试 INVALID_ARGUMENT
	paper, err := s.Exam.ExamPaperCreate(ctx(), ExamPaperCreateReq{
		WorkspaceID: ws.ID, UserID: userID, Title: "缺时长卷",
		ConfigJSON: mustJSON(map[string]any{"sections": []map[string]any{
			examSection("第一部分", 1, []string{q.CurrentVersion.ID}, 10),
		}}),
		IdempotencyKey: "epc-" + NewID(),
	})
	if err != nil {
		t.Fatalf("create paper without duration: %v", err)
	}
	paper, err = s.Exam.ExamPaperPublish(ctx(), ExamPaperPublishReq{
		WorkspaceID: ws.ID, PaperID: paper.ID, Version: paper.Version,
	})
	if err != nil {
		t.Fatalf("publish paper without duration: %v", err)
	}
	if _, err := s.Exam.ExamStart(ctx(), ExamStartReq{
		WorkspaceID: ws.ID, UserID: userID, PaperID: paper.ID, IdempotencyKey: "es-" + NewID(),
	}); err == nil {
		t.Fatal("expected INVALID_ARGUMENT for missing duration_min")
	} else if domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("expected INVALID_ARGUMENT, got %s", domain.AsError(err).Code)
	}

	// 5b: 开始时有时长，之后 config_json 丢失 duration_min → 惰性检查永不触发
	paper2, err := s.Exam.ExamPaperCreate(ctx(), ExamPaperCreateReq{
		WorkspaceID: ws.ID, UserID: userID, Title: "后丢时长卷",
		ConfigJSON: examPaperConfig(30, []map[string]any{
			examSection("第一部分", 1, []string{q.CurrentVersion.ID}, 10),
		}),
		IdempotencyKey: "epc2-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	paper2, err = s.Exam.ExamPaperPublish(ctx(), ExamPaperPublishReq{
		WorkspaceID: ws.ID, PaperID: paper2.ID, Version: paper2.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	t0 := examFixedTime("2026-08-06T09:00:00Z")
	s.Exam.Now = func() time.Time { return t0 }
	exam, err := s.Exam.ExamStart(ctx(), ExamStartReq{
		WorkspaceID: ws.ID, UserID: userID, PaperID: paper2.ID, IdempotencyKey: "es2-" + NewID(),
	})
	if err != nil {
		t.Fatalf("start with duration: %v", err)
	}
	// 直改 config_json 移除 duration_min
	if _, err := s.Repo.DB().ExecContext(ctx(),
		`UPDATE exam_papers SET config_json = ? WHERE id = ?`,
		string(mustJSON(map[string]any{"sections": []map[string]any{
			examSection("第一部分", 1, []string{q.CurrentVersion.ID}, 10),
		}})), paper2.ID); err != nil {
		t.Fatal(err)
	}
	s.Exam.Now = func() time.Time { return t0.Add(31 * time.Minute) }
	if _, err := s.Exam.ExamGetResult(ctx(), ExamGetResultReq{WorkspaceID: ws.ID, ExamID: exam.ID}); err == nil {
		t.Fatal("expected INVALID_STATE (exam never auto-submitted without duration_min)")
	} else if domain.AsError(err).Code != domain.CodeInvalidState {
		t.Fatalf("expected INVALID_STATE, got %s", domain.AsError(err).Code)
	}
	if examRowStatus(t, s, exam.ID) != "answering" {
		t.Fatal("exam should remain answering without duration_min")
	}
}

// ---- 场景 6：已自动提交后再提交 → CONFLICT 且成绩不变 ----

func TestExamSubmitAfterAutoSubmitConflict(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)

	q := publishedQuestion(t, s, ws.ID, scPayload("已提交", "A"))
	paper, err := s.Exam.ExamPaperCreate(ctx(), ExamPaperCreateReq{
		WorkspaceID: ws.ID, UserID: userID, Title: "已提交卷",
		ConfigJSON: examPaperConfig(10, []map[string]any{
			examSection("第一部分", 1, []string{q.CurrentVersion.ID}, 10),
		}),
		IdempotencyKey: "epc-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	paper, err = s.Exam.ExamPaperPublish(ctx(), ExamPaperPublishReq{
		WorkspaceID: ws.ID, PaperID: paper.ID, Version: paper.Version,
	})
	if err != nil {
		t.Fatal(err)
	}

	t0 := examFixedTime("2026-08-06T11:00:00Z")
	s.Exam.Now = func() time.Time { return t0 }
	exam, err := s.Exam.ExamStart(ctx(), ExamStartReq{
		WorkspaceID: ws.ID, UserID: userID, PaperID: paper.ID, IdempotencyKey: "es-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}

	s.Exam.Now = func() time.Time { return t0.Add(10 * time.Minute) }
	first, err := s.Exam.ExamAutoSubmit(ctx(), ExamAutoSubmitReq{WorkspaceID: ws.ID, ExamID: exam.ID})
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}

	// 再次提交 → CONFLICT
	if _, err := s.Exam.ExamAutoSubmit(ctx(), ExamAutoSubmitReq{WorkspaceID: ws.ID, ExamID: exam.ID}); err == nil {
		t.Fatal("expected CONFLICT on resubmit")
	} else if domain.AsError(err).Code != domain.CodeConflict {
		t.Fatalf("expected CONFLICT, got %s", domain.AsError(err).Code)
	}
	// 成绩不变
	after, err := s.Exam.ExamGetResult(ctx(), ExamGetResultReq{WorkspaceID: ws.ID, ExamID: exam.ID})
	if err != nil {
		t.Fatal(err)
	}
	if after.TotalScore != first.TotalScore || after.Status != "graded" {
		t.Fatalf("score changed after conflict: %+v vs %+v", after, first)
	}
}

// ---- 附加：自动组卷（满足配置 + 不满足 → INVALID_ARGUMENT） ----

func TestExamPaperAutoGenerate(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)

	k1, err := s.Knowledge.KnowledgeCreate(ctx(), KnowledgeCreateReq{WorkspaceID: ws.ID, Name: "代数"})
	if err != nil {
		t.Fatal(err)
	}
	examPubQuestion(t, s, ws.ID, "自动组卷题一", []string{k1.ID}, 3)
	examPubQuestion(t, s, ws.ID, "自动组卷题二", []string{k1.ID}, 3)

	// 满足配置：1 个知识点、难度 3、2 道单选
	paper, err := s.Exam.ExamPaperAutoGenerate(ctx(), ExamPaperAutoGenerateReq{
		WorkspaceID: ws.ID, UserID: userID, Title: "自动卷",
		Config: ExamAutoGenerateConfig{
			KnowledgeRatio: map[string]float64{k1.ID: 1.0},
			DifficultyDist: map[string]float64{"3": 1.0},
			Count:          2,
			Types:          []string{"single_choice"},
			DurationMin:    45,
		},
		IdempotencyKey: "epg-" + NewID(),
	})
	if err != nil {
		t.Fatalf("auto generate: %v", err)
	}
	if paper.Status != "draft" || len(paper.Sections) != 1 {
		t.Fatalf("unexpected auto paper: %+v", paper)
	}
	if len(paper.Sections[0].QuestionVersionIDs) != 2 {
		t.Fatalf("expected 2 questions, got %+v", paper.Sections[0].QuestionVersionIDs)
	}

	// 不满足配置：count=5 但只有 2 道 → INVALID_ARGUMENT
	if _, err := s.Exam.ExamPaperAutoGenerate(ctx(), ExamPaperAutoGenerateReq{
		WorkspaceID: ws.ID, UserID: userID, Title: "超量卷",
		Config: ExamAutoGenerateConfig{
			KnowledgeRatio: map[string]float64{k1.ID: 1.0},
			DifficultyDist: map[string]float64{"3": 1.0},
			Count:          5,
			Types:          []string{"single_choice"},
			DurationMin:    45,
		},
		IdempotencyKey: "epg2-" + NewID(),
	}); err == nil {
		t.Fatal("expected INVALID_ARGUMENT for unsatisfiable auto-generate")
	} else if domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("expected INVALID_ARGUMENT, got %s", domain.AsError(err).Code)
	}
}
