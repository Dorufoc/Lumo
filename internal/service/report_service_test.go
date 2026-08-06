package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lumo/internal/domain"
)

// ---- 辅助 ----

// reportFixedNow 固定时钟：锚定到真实当天 10:00 UTC。
// 报告聚合查询按种子数据（真实 Now / 当天日期）过滤，锚定当天保证数据落入周期窗口。
func reportFixedNow() (*time.Time, func() time.Time) {
	base := time.Now().UTC()
	t := time.Date(base.Year(), base.Month(), base.Day(), 10, 0, 0, 0, time.UTC)
	return &t, func() time.Time { return t }
}

// reportToday 返回固定时钟当天日期（YYYY-MM-DD）。
func reportToday(now time.Time) string { return now.Format("2006-01-02") }

// reportNotificationCount 统计用户某事件类型通知条数（事实断言：每次 ready 一条）。
func reportNotificationCount(t *testing.T, s *Services, userID, kind string) int {
	t.Helper()
	var n int
	if err := s.Repo.DB().QueryRowContext(ctx(),
		`SELECT COUNT(*) FROM notifications WHERE user_id = ? AND kind = ?`,
		userID, kind).Scan(&n); err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	return n
}

// reportRowCount 统计用户报告行数（快照不可变断言：每次生成新行）。
func reportRowCount(t *testing.T, s *Services, userID string) int {
	t.Helper()
	var n int
	if err := s.Repo.DB().QueryRowContext(ctx(),
		`SELECT COUNT(*) FROM reports WHERE user_id = ?`, userID).Scan(&n); err != nil {
		t.Fatalf("count reports: %v", err)
	}
	return n
}

// seedReportPracticeWrong 种一条答错练习：知识点+题目+提交（生成错题与复习卡）。
func seedReportPracticeWrong(t *testing.T, s *Services, ws *Workspace, userID string) string {
	t.Helper()
	kn, err := s.Knowledge.KnowledgeCreate(ctx(), KnowledgeCreateReq{WorkspaceID: ws.ID, Name: "薄弱点A"})
	if err != nil {
		t.Fatalf("create knowledge: %v", err)
	}
	payload := mustJSON(map[string]any{
		"type": "single_choice", "stem": "1+1=?",
		"options":       []map[string]any{{"key": "A", "text": "1"}, {"key": "B", "text": "2"}},
		"answer":        "B",
		"knowledge_ids": []string{kn.ID},
	})
	q := publishedQuestion(t, s, ws.ID, payload)
	session, err := s.Practice.PracticeStart(ctx(), PracticeStartReq{
		WorkspaceID: ws.ID, UserID: userID, Mode: "practice",
		QuestionIDs: []string{q.ID}, IdempotencyKey: "ps-" + NewID(),
	})
	if err != nil {
		t.Fatalf("practice start: %v", err)
	}
	if _, err := s.Practice.PracticeSaveAnswer(ctx(), PracticeSaveAnswerReq{
		WorkspaceID: ws.ID, SessionID: session.ID,
		QuestionVersionID: session.Questions[0].QuestionVersionID,
		Answer:            json.RawMessage(`"A"`), ClientSequence: 1,
	}); err != nil {
		t.Fatalf("save answer: %v", err)
	}
	if _, err := s.Practice.PracticeSubmit(ctx(), PracticeSubmitReq{
		WorkspaceID: ws.ID, SessionID: session.ID, Version: session.Version,
		IdempotencyKey: "psub-" + NewID(),
	}); err != nil {
		t.Fatalf("practice submit: %v", err)
	}
	return kn.ID
}

// seedReportFocusSession 种一条完成的番茄钟会话（25 分钟）。
func seedReportFocusSession(t *testing.T, s *Services, ws *Workspace, userID string, now *time.Time) {
	t.Helper()
	started, err := s.Focus.TimerStart(ctx(), TimerStartReq{
		WorkspaceID: ws.ID, UserID: userID, Mode: domain.TimerModePomodoro,
		PlannedMinutes: 25, IdempotencyKey: "fm-" + NewID(),
	})
	if err != nil {
		t.Fatalf("timer start: %v", err)
	}
	*now = now.Add(25 * time.Minute)
	if _, err := s.Focus.TimerEnd(ctx(), TimerEndReq{
		WorkspaceID: ws.ID, UserID: userID, SessionID: started.ID,
	}); err != nil {
		t.Fatalf("timer end: %v", err)
	}
}

// seedReportCheckin 种一条当日打卡。
func seedReportCheckin(t *testing.T, s *Services, ws *Workspace, userID string) {
	t.Helper()
	if _, err := s.Checkin.CheckinCreate(ctx(), CheckinCreateReq{
		WorkspaceID: ws.ID, UserID: userID, Minutes: 30, IdempotencyKey: "ck-" + NewID(),
	}); err != nil {
		t.Fatalf("checkin: %v", err)
	}
}

// ---- 场景 1：ReportGenerate 参数校验 ----

func TestReportGenerateValidation(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	today := reportToday(time.Now().UTC())

	bad := []ReportGenerateReq{
		{WorkspaceID: ws.ID, UserID: "", Period: "daily", PeriodStart: today, PeriodEnd: today},
		{WorkspaceID: ws.ID, UserID: userID, Period: "hourly", PeriodStart: today, PeriodEnd: today},
		{WorkspaceID: ws.ID, UserID: userID, Period: "daily"},                                              // 无任何日期
		{WorkspaceID: ws.ID, UserID: userID, Period: "daily", PeriodStart: "bad"},                          // 非法日期
		{WorkspaceID: ws.ID, UserID: userID, Period: "daily", PeriodStart: today, PeriodEnd: "2020-01-01"}, // end < start
	}
	for i, req := range bad {
		if _, err := s.Report.ReportGenerate(ctx(), req); err == nil {
			t.Fatalf("case %d: expected error, got nil", i)
		} else if domain.AsError(err).Code != domain.CodeInvalidArgument {
			t.Fatalf("case %d: expected INVALID_ARGUMENT, got %s", i, domain.AsError(err).Code)
		}
	}
}

// ---- 场景 2：空工作区生成 → ready + report:ready 事件落库 ----

func TestReportGenerateReadyPublishesEvent(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	now, clock := reportFixedNow()
	s.Report.Now = clock
	today := reportToday(*now)

	rep, err := s.Report.ReportGenerate(ctx(), ReportGenerateReq{
		WorkspaceID: ws.ID, UserID: userID, Period: domain.ReportPeriodDaily,
		PeriodStart: today, PeriodEnd: today, IdempotencyKey: "rg-" + NewID(),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if rep.Status != domain.ReportStatusReady {
		t.Fatalf("expected ready, got %s", rep.Status)
	}
	if rep.Period != domain.ReportPeriodDaily {
		t.Fatalf("unexpected period: %s", rep.Period)
	}

	// 载荷可解析且为空聚合 → 样本不足 + 建议非空
	var pl domain.ReportPayload
	if err := json.Unmarshal(rep.Payload, &pl); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if !pl.Summary.SampleInsufficient {
		t.Fatal("expected sample_insufficient=true for empty data")
	}
	if len(pl.Suggestions) == 0 {
		t.Fatal("expected non-empty suggestions")
	}

	// report:ready 事件 → notifications 落库一条
	if n := reportNotificationCount(t, s, userID, "report:ready"); n != 1 {
		t.Fatalf("expected 1 report:ready notification, got %d", n)
	}
}

// ---- 场景 3：有数据生成 → 聚合正确（练习/错题/复习到期/专注/打卡） ----

func TestReportGenerateWithData(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	now, clock := reportFixedNow()
	s.Report.Now = clock
	s.Focus.Now = clock
	s.Checkin.Now = clock
	today := reportToday(*now)

	knID := seedReportPracticeWrong(t, s, ws, userID)
	seedReportFocusSession(t, s, ws, userID, now)
	seedReportCheckin(t, s, ws, userID)

	rep, err := s.Report.ReportGenerate(ctx(), ReportGenerateReq{
		WorkspaceID: ws.ID, UserID: userID, Period: domain.ReportPeriodDaily,
		PeriodStart: today, PeriodEnd: today, IdempotencyKey: "rg-" + NewID(),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	var pl domain.ReportPayload
	if err := json.Unmarshal(rep.Payload, &pl); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	sum := pl.Summary
	if sum.PracticeCount != 1 {
		t.Fatalf("expected practice_count=1, got %d", sum.PracticeCount)
	}
	if sum.AccuracySamples != 1 || sum.CorrectCount != 0 {
		t.Fatalf("unexpected accuracy: samples=%d correct=%d", sum.AccuracySamples, sum.CorrectCount)
	}
	if !sum.SampleInsufficient {
		t.Fatal("expected sample_insufficient=true (1 sample < 20)")
	}
	if sum.ReviewDue < 1 {
		t.Fatalf("expected review_due>=1, got %d", sum.ReviewDue)
	}
	if sum.FocusSessions != 1 || sum.FocusMinutes != 25 {
		t.Fatalf("unexpected focus: sessions=%d minutes=%d", sum.FocusSessions, sum.FocusMinutes)
	}
	if sum.CheckinDays != 1 {
		t.Fatalf("expected checkin_days=1, got %d", sum.CheckinDays)
	}

	// 薄弱知识点 + 趋势点 + 时段分布各 1
	if len(pl.WeakKnowledge) != 1 || pl.WeakKnowledge[0].KnowledgeID != knID || pl.WeakKnowledge[0].WrongCount != 1 {
		t.Fatalf("unexpected weak knowledge: %+v", pl.WeakKnowledge)
	}
	if len(pl.Trend) != 1 || pl.Trend[0].PracticeCount != 1 {
		t.Fatalf("unexpected trend: %+v", pl.Trend)
	}
	if pl.TimeDistribution.Morning+pl.TimeDistribution.Afternoon+pl.TimeDistribution.Evening != 1 {
		t.Fatalf("unexpected time distribution: %+v", pl.TimeDistribution)
	}
}

// ---- 场景 4：快照不可变 —— 每次生成新行，旧报告载荷不被覆盖 ----

func TestReportSnapshotImmutable(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	now, clock := reportFixedNow()
	s.Report.Now = clock
	today := reportToday(*now)

	seedReportPracticeWrong(t, s, ws, userID)

	first, err := s.Report.ReportGenerate(ctx(), ReportGenerateReq{
		WorkspaceID: ws.ID, UserID: userID, Period: domain.ReportPeriodDaily,
		PeriodStart: today, PeriodEnd: today, IdempotencyKey: "rg-" + NewID(),
	})
	if err != nil {
		t.Fatalf("first generate: %v", err)
	}
	firstPayload := string(first.Payload)

	// 再次生成（不同幂等键）→ 新行
	second, err := s.Report.ReportGenerate(ctx(), ReportGenerateReq{
		WorkspaceID: ws.ID, UserID: userID, Period: domain.ReportPeriodDaily,
		PeriodStart: today, PeriodEnd: today, IdempotencyKey: "rg-" + NewID(),
	})
	if err != nil {
		t.Fatalf("second generate: %v", err)
	}
	if first.ID == second.ID {
		t.Fatal("expected distinct report snapshots")
	}
	if n := reportRowCount(t, s, userID); n != 2 {
		t.Fatalf("expected 2 report rows, got %d", n)
	}

	// 旧报告载荷不变
	old, err := s.Repo.GetReport(ctx(), ws.ID, first.ID)
	if err != nil || old == nil {
		t.Fatalf("re-read first report: %v", err)
	}
	if old.PayloadJSON != firstPayload {
		t.Fatal("snapshot mutated: first report payload changed")
	}
}

// ---- 场景 5：ReportGenerate 幂等（同键重放返回同一报告） ----

func TestReportGenerateIdempotent(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	today := reportToday(time.Now().UTC())
	key := "rg-" + NewID()

	first, err := s.Report.ReportGenerate(ctx(), ReportGenerateReq{
		WorkspaceID: ws.ID, UserID: userID, Period: domain.ReportPeriodDaily,
		PeriodStart: today, PeriodEnd: today, IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("first generate: %v", err)
	}
	replay, err := s.Report.ReportGenerate(ctx(), ReportGenerateReq{
		WorkspaceID: ws.ID, UserID: userID, Period: domain.ReportPeriodDaily,
		PeriodStart: today, PeriodEnd: today, IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("replay generate: %v", err)
	}
	if replay.ID != first.ID {
		t.Fatalf("idempotency violated: %s != %s", replay.ID, first.ID)
	}
	if n := reportRowCount(t, s, userID); n != 1 {
		t.Fatalf("expected 1 report row, got %d", n)
	}
}

// ---- 场景 6：ReportList 分页（cursor / has_more / period 过滤） ----

func TestReportListPagination(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	now, clock := reportFixedNow()
	s.Report.Now = clock
	today := reportToday(*now)

	for i := 0; i < 3; i++ {
		if _, err := s.Report.ReportGenerate(ctx(), ReportGenerateReq{
			WorkspaceID: ws.ID, UserID: userID, Period: domain.ReportPeriodDaily,
			PeriodStart: today, PeriodEnd: today, IdempotencyKey: "rg-" + NewID(),
		}); err != nil {
			t.Fatalf("generate %d: %v", i, err)
		}
	}

	page1, err := s.Report.ReportList(ctx(), ReportListReq{
		WorkspaceID: ws.ID, UserID: userID, Limit: 2,
	})
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1.Items) != 2 || !page1.HasMore || page1.NextCursor == "" {
		t.Fatalf("unexpected page1: items=%d has_more=%v cursor=%q", len(page1.Items), page1.HasMore, page1.NextCursor)
	}

	page2, err := s.Report.ReportList(ctx(), ReportListReq{
		WorkspaceID: ws.ID, UserID: userID, Limit: 2, Cursor: page1.NextCursor,
	})
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2.Items) != 1 || page2.HasMore {
		t.Fatalf("unexpected page2: items=%d has_more=%v", len(page2.Items), page2.HasMore)
	}

	// 无跨页重复
	seen := map[string]bool{}
	for _, it := range append(page1.Items, page2.Items...) {
		if seen[it.ID] {
			t.Fatalf("duplicate report across pages: %s", it.ID)
		}
		seen[it.ID] = true
	}

	// period 过滤：weekly 无数据
	empty, err := s.Report.ReportList(ctx(), ReportListReq{
		WorkspaceID: ws.ID, UserID: userID, Period: domain.ReportPeriodWeekly,
	})
	if err != nil {
		t.Fatalf("filtered list: %v", err)
	}
	if len(empty.Items) != 0 {
		t.Fatalf("expected 0 weekly reports, got %d", len(empty.Items))
	}
}

// ---- 场景 7：ReportExport JSON + PDF（文件落盘、格式正确） ----

func TestReportExportJSONAndPDF(t *testing.T) {
	s, cfg := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	now, clock := reportFixedNow()
	s.Report.Now = clock
	today := reportToday(*now)

	seedReportPracticeWrong(t, s, ws, userID)
	rep, err := s.Report.ReportGenerate(ctx(), ReportGenerateReq{
		WorkspaceID: ws.ID, UserID: userID, Period: domain.ReportPeriodDaily,
		PeriodStart: today, PeriodEnd: today, IdempotencyKey: "rg-" + NewID(),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	// JSON 导出
	jsonRes, err := s.Report.ReportExport(ctx(), ReportExportReq{
		WorkspaceID: ws.ID, ReportID: rep.ID, Format: "json",
	})
	if err != nil {
		t.Fatalf("export json: %v", err)
	}
	if jsonRes.Format != "json" || jsonRes.SizeBytes == 0 {
		t.Fatalf("unexpected json export: %+v", jsonRes)
	}
	if b, err := os.ReadFile(filepath.Join(cfg.DataDir, jsonRes.Path)); err != nil {
		t.Fatalf("read json export: %v", err)
	} else if !json.Valid(b) {
		t.Fatal("exported json is not valid JSON")
	}

	// PDF 导出（本机存在 CJK 字体 → pdf；否则回退 json）
	pdfRes, err := s.Report.ReportExport(ctx(), ReportExportReq{
		WorkspaceID: ws.ID, ReportID: rep.ID, Format: "pdf",
	})
	if err != nil {
		t.Fatalf("export pdf: %v", err)
	}
	if pdfRes.Format != "pdf" && pdfRes.Format != "json" {
		t.Fatalf("unexpected pdf export format: %s", pdfRes.Format)
	}
	b, err := os.ReadFile(filepath.Join(cfg.DataDir, pdfRes.Path))
	if err != nil {
		t.Fatalf("read pdf export: %v", err)
	}
	if pdfRes.Format == "pdf" && !strings.HasPrefix(string(b), "%PDF") {
		t.Fatal("pdf export does not start with %PDF magic")
	}
}

// ---- 场景 8：ReportExport 守卫 + InsightGet 维度 ----

func TestReportExportGuards(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	today := reportToday(time.Now().UTC())

	rep, err := s.Report.ReportGenerate(ctx(), ReportGenerateReq{
		WorkspaceID: ws.ID, UserID: userID, Period: domain.ReportPeriodDaily,
		PeriodStart: today, PeriodEnd: today, IdempotencyKey: "rg-" + NewID(),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	cases := []struct {
		name string
		req  ReportExportReq
		code domain.ErrorCode
	}{
		{"invalid format", ReportExportReq{WorkspaceID: ws.ID, ReportID: rep.ID, Format: "docx"}, domain.CodeInvalidArgument},
		{"missing report_id", ReportExportReq{WorkspaceID: ws.ID, Format: "json"}, domain.CodeInvalidArgument},
		{"not found", ReportExportReq{WorkspaceID: ws.ID, ReportID: "nonexistent", Format: "json"}, domain.CodeNotFound},
	}
	for _, c := range cases {
		if _, err := s.Report.ReportExport(ctx(), c.req); err == nil {
			t.Fatalf("%s: expected error, got nil", c.name)
		} else if domain.AsError(err).Code != c.code {
			t.Fatalf("%s: expected %s, got %s", c.name, c.code, domain.AsError(err).Code)
		}
	}
}

func TestInsightGetDimensions(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	_, clock := reportFixedNow()
	s.Report.Now = clock

	seedReportPracticeWrong(t, s, ws, userID)

	// knowledge 维度：错题 → 知识点洞察
	ki, err := s.Report.InsightGet(ctx(), InsightGetReq{
		WorkspaceID: ws.ID, UserID: userID, Dimension: domain.InsightDimensionKnowledge,
	})
	if err != nil {
		t.Fatalf("insight knowledge: %v", err)
	}
	if ki.Dimension != domain.InsightDimensionKnowledge || len(ki.Knowledge) != 1 {
		t.Fatalf("unexpected knowledge insight: %+v", ki)
	}
	if ki.Knowledge[0].PracticeCount != 1 || ki.Knowledge[0].CorrectCount != 0 {
		t.Fatalf("unexpected knowledge stats: %+v", ki.Knowledge[0])
	}

	// time 维度
	ti, err := s.Report.InsightGet(ctx(), InsightGetReq{
		WorkspaceID: ws.ID, UserID: userID, Dimension: domain.InsightDimensionTime,
	})
	if err != nil {
		t.Fatalf("insight time: %v", err)
	}
	if ti.Dimension != domain.InsightDimensionTime || ti.Time == nil {
		t.Fatalf("unexpected time insight: %+v", ti)
	}

	// trend 维度
	tr, err := s.Report.InsightGet(ctx(), InsightGetReq{
		WorkspaceID: ws.ID, UserID: userID, Dimension: domain.InsightDimensionTrend,
	})
	if err != nil {
		t.Fatalf("insight trend: %v", err)
	}
	if tr.Dimension != domain.InsightDimensionTrend || tr.Trend == nil {
		t.Fatalf("unexpected trend insight: %+v", tr)
	}

	// 非法维度
	if _, err := s.Report.InsightGet(ctx(), InsightGetReq{
		WorkspaceID: ws.ID, UserID: userID, Dimension: "weekly",
	}); err == nil || domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("expected INVALID_ARGUMENT for bad dimension, got %v", err)
	}
}
