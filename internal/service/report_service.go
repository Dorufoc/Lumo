package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"

	"lumo/internal/agent"
	"lumo/internal/domain"
	"lumo/internal/repository"
)

// ReportService 实现学习报告与数据洞察（API 设计文档 7.5 / 完整设计文档 4.12）。
// 报告数据全部由服务端从事件/事实表聚合，不允许前端传入数值。
// Now 可注入时钟（测试推进时间）；ReportGenerate 同步聚合完成后发布 report:ready 事件。
type ReportService struct {
	s   *Services
	Now func() time.Time
}

// Report 是报告 DTO（reports 表行，payload 已解析为 JSON）。
type Report struct {
	ID          string          `json:"id"`
	WorkspaceID string          `json:"workspace_id"`
	UserID      string          `json:"user_id"`
	Period      string          `json:"period"`
	PeriodStart string          `json:"period_start"`
	PeriodEnd   string          `json:"period_end"`
	Payload     json.RawMessage `json:"payload"`
	Status      string          `json:"status"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
}

// ReportPage 是报告列表分页响应。
type ReportPage struct {
	Items      []*Report `json:"items"`
	NextCursor string    `json:"next_cursor"`
	HasMore    bool      `json:"has_more"`
}

// ReportGenerateReq 生成报告请求（API 文档 7.5）。
type ReportGenerateReq struct {
	WorkspaceID    string `json:"workspace_id"`
	UserID         string `json:"user_id"`
	Period         string `json:"period"` // daily|weekly|monthly
	PeriodStart    string `json:"period_start"`
	PeriodEnd      string `json:"period_end"`
	IdempotencyKey string `json:"idempotency_key"`
}

// ReportListReq 报告列表请求。
type ReportListReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	Period      string `json:"period"`
	Cursor      string `json:"cursor"`
	Limit       int    `json:"limit"`
}

// ReportExportReq 报告导出请求。
type ReportExportReq struct {
	WorkspaceID string `json:"workspace_id"`
	ReportID    string `json:"report_id"`
	Format      string `json:"format"` // pdf|json
}

// InsightGetReq 洞察请求（dimension: knowledge|time|trend）。
type InsightGetReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	Dimension   string `json:"dimension"`
}

// ReportGenerate 生成学习报告：同步聚合（练习量/正确率/复习/专注/打卡/任务/薄弱点/趋势/时段），
// 状态 generating → ready（失败 → failed 并保留原因），完成后发布 report:ready 事件（通知栏）。
// 幂等：idempotency_key 非空时复用 withIdempotency（占位-执行-完成）。
func (r *ReportService) ReportGenerate(ctx context.Context, req ReportGenerateReq) (*Report, error) {
	if err := r.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if !domain.ValidReportPeriod(req.Period) {
		return nil, domain.InvalidArg("period 须为 daily|weekly|monthly")
	}
	start, end, err := reportPeriodWindow(req.Period, req.PeriodStart, req.PeriodEnd)
	if err != nil {
		return nil, err
	}
	if req.IdempotencyKey == "" {
		return r.generateReport(ctx, req, start, end)
	}
	return withIdempotency(r.s, ctx, req.WorkspaceID, req.IdempotencyKey, "ReportGenerate", func() (*Report, error) {
		return r.generateReport(ctx, req, start, end)
	})
}

// generateReport 执行生成（幂等函数体）。
func (r *ReportService) generateReport(ctx context.Context, req ReportGenerateReq, start, end time.Time) (*Report, error) {
	now := r.Now().UTC()
	row := &repository.ReportRow{
		ID: NewID(), WorkspaceID: req.WorkspaceID, UserID: req.UserID,
		Period: req.Period, PeriodStart: start.Format(time.RFC3339), PeriodEnd: end.Format(time.RFC3339),
		PayloadJSON: "{}", Status: domain.ReportStatusGenerating,
	}
	if err := r.s.Repo.CreateReport(ctx, row); err != nil {
		return nil, err
	}
	r.s.audit(ctx, req.WorkspaceID, "report.generate", "report", row.ID,
		map[string]any{"period": req.Period, "period_start": row.PeriodStart, "period_end": row.PeriodEnd})

	payload, err := r.computeReportPayload(ctx, req.WorkspaceID, req.UserID, req.Period, start, end, now)
	status := domain.ReportStatusReady
	if err != nil {
		// 失败保留状态与原因（设计 4.12 失败处理）；重试幂等。
		payload = &domain.ReportPayload{
			Period: req.Period, PeriodStart: row.PeriodStart, PeriodEnd: row.PeriodEnd,
			GeneratedAt: now.Format(time.RFC3339), SchemaVersion: "2.0.0",
		}
		status = domain.ReportStatusFailed
	}
	payloadJSON := repository.MarshalJSON(payload)
	if err := r.s.Repo.UpdateReportResult(ctx, row.ID, status, payloadJSON, now.Format(time.RFC3339)); err != nil {
		return nil, err
	}
	// 聚合结果缓存（report_cache，设计 4.12 可缓存于 report_cache）。
	_ = r.s.Repo.PutReportCache(ctx, reportCacheKey(req.WorkspaceID, req.UserID, req.Period, row.PeriodStart, row.PeriodEnd), payloadJSON)

	if status == domain.ReportStatusReady {
		if err := r.s.UserEvents.Publish(req.UserID, agent.Event{
			Name: agent.EventReportReady,
			Payload: map[string]any{
				"report_id": row.ID, "period": req.Period, "status": status,
				"ref_type": "report", "ref_id": row.ID,
			},
		}); err != nil {
			return nil, err
		}
		r.s.audit(ctx, req.WorkspaceID, "report.ready", "report", row.ID,
			map[string]any{"period": req.Period, "status": status})
	} else {
		r.s.audit(ctx, req.WorkspaceID, "report.failed", "report", row.ID,
			map[string]any{"period": req.Period, "reason": err.Error()})
	}
	return r.readReport(ctx, req.WorkspaceID, row.ID)
}

// ReportList 分页列出报告（newest-first；period 为空不过滤）。
func (r *ReportService) ReportList(ctx context.Context, req ReportListReq) (*ReportPage, error) {
	if err := r.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if req.Period != "" && !domain.ValidReportPeriod(req.Period) {
		return nil, domain.InvalidArg("period 须为 daily|weekly|monthly")
	}
	if req.Limit < 0 {
		return nil, domain.InvalidArg("limit 不能为负")
	}
	rows, next, hasMore, err := r.s.Repo.ListReports(ctx, req.WorkspaceID, req.UserID, req.Period, req.Cursor, req.Limit)
	if err != nil {
		return nil, err
	}
	items := make([]*Report, 0, len(rows))
	for _, row := range rows {
		items = append(items, reportFromRow(row))
	}
	return &ReportPage{Items: items, NextCursor: next, HasMore: hasMore}, nil
}

// ReportExport 导出报告（pdf|json）到 exports/ 目录，返回下载路径（经 GET /api/v1/files）。
// 仅允许 status=ready 的报告导出；PDF 渲染失败回退 JSON 下载（设计 4.12 失败处理）。
func (r *ReportService) ReportExport(ctx context.Context, req ReportExportReq) (*ExportResult, error) {
	if err := r.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.ReportID == "" {
		return nil, domain.InvalidArg("report_id 必填")
	}
	if req.Format != "pdf" && req.Format != "json" {
		return nil, domain.InvalidArg("format 仅允许 pdf/json")
	}
	report, err := r.readReport(ctx, req.WorkspaceID, req.ReportID)
	if err != nil {
		return nil, err
	}
	if report.Status != domain.ReportStatusReady {
		return nil, domain.InvalidState("报告未就绪，无法导出")
	}

	exportsDir := filepath.Join(r.s.Cfg.DataDir, "exports")
	if err := os.MkdirAll(exportsDir, 0o700); err != nil {
		return nil, err
	}
	base := fmt.Sprintf("report-%s-%s", report.Period, safeTimestamp())

	if req.Format == "json" {
		fileName := base + ".json"
		path := filepath.Join(exportsDir, fileName)
		if err := os.WriteFile(path, report.Payload, 0o600); err != nil {
			return nil, err
		}
		r.s.audit(ctx, req.WorkspaceID, "report.export", "report", req.ReportID,
			map[string]any{"format": "json", "file": fileName})
		return &ExportResult{Path: filepath.Join("exports", fileName), FileName: fileName, Format: "json", SizeBytes: int64(len(report.Payload))}, nil
	}

	// PDF
	var payload domain.ReportPayload
	if err := json.Unmarshal(report.Payload, &payload); err != nil {
		return nil, domain.InvalidState("报告载荷损坏，无法导出 PDF")
	}
	data, err := renderReportPDF(&payload)
	if err != nil {
		// PDF 渲染失败回退 JSON 下载（设计 4.12）
		fileName := base + ".json"
		if werr := os.WriteFile(filepath.Join(exportsDir, fileName), report.Payload, 0o600); werr != nil {
			return nil, werr
		}
		r.s.audit(ctx, req.WorkspaceID, "report.export", "report", req.ReportID,
			map[string]any{"format": "json", "file": fileName, "fallback": true})
		return &ExportResult{Path: filepath.Join("exports", fileName), FileName: fileName, Format: "json", SizeBytes: int64(len(report.Payload))}, nil
	}
	fileName := base + ".pdf"
	if err := os.WriteFile(filepath.Join(exportsDir, fileName), data, 0o600); err != nil {
		return nil, err
	}
	r.s.audit(ctx, req.WorkspaceID, "report.export", "report", req.ReportID,
		map[string]any{"format": "pdf", "file": fileName, "size": len(data)})
	return &ExportResult{Path: filepath.Join("exports", fileName), FileName: fileName, Format: "pdf", SizeBytes: int64(len(data))}, nil
}

// InsightGet 返回按维度聚合的洞察（knowledge/time/trend，设计 4.12 R4/R5/R7）。
func (r *ReportService) InsightGet(ctx context.Context, req InsightGetReq) (*domain.Insight, error) {
	if err := r.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if !domain.ValidInsightDimension(req.Dimension) {
		return nil, domain.InvalidArg("dimension 须为 knowledge|time|trend")
	}
	switch req.Dimension {
	case domain.InsightDimensionKnowledge:
		return r.insightKnowledge(ctx, req.WorkspaceID, req.UserID)
	case domain.InsightDimensionTime:
		return r.insightTime(ctx, req.WorkspaceID, req.UserID)
	default:
		return r.insightTrend(ctx, req.WorkspaceID, req.UserID)
	}
}

// computeReportPayload 聚合报告数据（全部来自事实表，前端不传数值）。
func (r *ReportService) computeReportPayload(ctx context.Context, wsID, userID, period string, start, end time.Time, now time.Time) (*domain.ReportPayload, error) {
	db := r.s.Repo.DB()
	startStr := start.Format(time.RFC3339)
	endStr := end.Format(time.RFC3339)
	startDate := start.Format("2006-01-02")
	endDate := end.Format("2006-01-02")

	summary := domain.ReportSummary{}

	// 练习量：期间内已提交的 submission 数
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM submissions s
		JOIN practice_sessions p ON s.session_id = p.id
		WHERE p.workspace_id = ? AND p.user_id = ? AND s.status = 'submitted'
		  AND s.submitted_at >= ? AND s.submitted_at < ?`,
		wsID, userID, startStr, endStr).Scan(&summary.PracticeCount); err != nil {
		return nil, dbErr(err)
	}

	// 正确率：期间内规则判分客观题
	if err := db.QueryRowContext(ctx, `
		SELECT COALESCE(sum(CASE WHEN g.score >= g.max_score THEN 1 ELSE 0 END), 0),
		       COALESCE(count(*), 0)
		FROM grading_results g
		JOIN submissions s ON g.submission_id = s.id
		JOIN practice_sessions p ON s.session_id = p.id
		WHERE p.workspace_id = ? AND p.user_id = ? AND g.status = 'completed' AND g.method = 'rule'
		  AND s.submitted_at >= ? AND s.submitted_at < ?`,
		wsID, userID, startStr, endStr).Scan(&summary.CorrectCount, &summary.AccuracySamples); err != nil {
		return nil, dbErr(err)
	}
	if summary.AccuracySamples >= domain.MinReportSample {
		summary.Accuracy = float64(summary.CorrectCount) / float64(summary.AccuracySamples)
	} else {
		// 样本不足：不展示百分比（设计 4.12 规则）
		summary.SampleInsufficient = true
	}

	// 复习：期间内复习次数 + 期末到期卡数
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM review_events e
		JOIN review_cards c ON e.review_card_id = c.id
		WHERE c.workspace_id = ? AND c.user_id = ? AND e.created_at >= ? AND e.created_at < ?`,
		wsID, userID, startStr, endStr).Scan(&summary.ReviewDone); err != nil {
		return nil, dbErr(err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM review_cards
		WHERE workspace_id = ? AND user_id = ? AND status = 'active' AND due_at <= ?`,
		wsID, userID, endStr).Scan(&summary.ReviewDue); err != nil {
		return nil, dbErr(err)
	}

	// 专注：期间内完成的计时会话（分钟与次数）
	var focusSeconds int
	if err := db.QueryRowContext(ctx, `
		SELECT COALESCE(sum(actual_seconds), 0), count(*) FROM timer_sessions
		WHERE workspace_id = ? AND user_id = ? AND status = 'completed'
		  AND COALESCE(started_at, created_at) >= ? AND COALESCE(started_at, created_at) < ?`,
		wsID, userID, startStr, endStr).Scan(&focusSeconds, &summary.FocusSessions); err != nil {
		return nil, dbErr(err)
	}
	summary.FocusMinutes = focusSeconds / 60

	// 打卡：期间内打卡天数（checkins.date 为 YYYY-MM-DD）
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM checkins WHERE user_id = ? AND date >= ? AND date <= ?`,
		userID, startDate, endDate).Scan(&summary.CheckinDays); err != nil {
		return nil, dbErr(err)
	}

	// 任务：期间内到期任务完成率
	if err := db.QueryRowContext(ctx, `
		SELECT count(*), COALESCE(sum(CASE WHEN status = 'completed' THEN 1 ELSE 0 END), 0)
		FROM plan_tasks
		WHERE workspace_id = ? AND user_id = ? AND deleted_at IS NULL
		  AND due_at >= ? AND due_at < ?`,
		wsID, userID, startStr, endStr).Scan(&summary.TaskTotal, &summary.TaskDone); err != nil {
		return nil, dbErr(err)
	}

	// 薄弱知识点 Top5
	weak, err := queryWeakKnowledge(ctx, db, wsID, userID, startStr, endStr, 5)
	if err != nil {
		return nil, err
	}

	// 每日趋势
	trend, err := queryTrend(ctx, db, wsID, userID, startStr, endStr)
	if err != nil {
		return nil, err
	}

	// 时段分布（早/午/晚）
	dist, err := queryTimeDistribution(ctx, db, wsID, userID, startStr, endStr)
	if err != nil {
		return nil, err
	}

	// 专注中断原因
	interrupts, err := queryInterruptReasons(ctx, db, wsID, userID, startStr, endStr)
	if err != nil {
		return nil, err
	}

	payload := &domain.ReportPayload{
		Period:           period,
		PeriodStart:      startStr,
		PeriodEnd:        endStr,
		GeneratedAt:      now.Format(time.RFC3339),
		SchemaVersion:    "2.0.0",
		Summary:          summary,
		WeakKnowledge:    weak,
		Trend:            trend,
		TimeDistribution: dist,
		InterruptReasons: interrupts,
		Suggestions:      buildReportSuggestions(summary, weak),
	}
	return payload, nil
}

// insightKnowledge 知识点洞察（R4：正确率/练习量/最近复习时间）。
func (r *ReportService) insightKnowledge(ctx context.Context, wsID, userID string) (*domain.Insight, error) {
	db := r.s.Repo.DB()
	rows, err := db.QueryContext(ctx, `
		SELECT k.id, k.name,
		       COALESCE(sum(CASE WHEN g.score >= g.max_score THEN 1 ELSE 0 END), 0),
		       count(*), max(w.updated_at)
		FROM wrong_answers w
		JOIN question_knowledge qk ON qk.question_version_id = w.question_version_id
		JOIN knowledge_nodes k ON k.id = qk.knowledge_id
		JOIN submissions s ON s.id = w.submission_id
		JOIN grading_results g ON g.submission_id = s.id
		WHERE w.workspace_id = ? AND w.user_id = ? AND w.deleted_at IS NULL AND w.status = 'active'
		  AND g.status = 'completed' AND g.method = 'rule'
		GROUP BY k.id, k.name
		ORDER BY count(*) DESC
		LIMIT 20`, wsID, userID)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	var out []domain.KnowledgeInsight
	for rows.Next() {
		var ki domain.KnowledgeInsight
		var correct, total int
		var last *string
		if err := rows.Scan(&ki.KnowledgeID, &ki.Name, &correct, &total, &last); err != nil {
			return nil, dbErr(err)
		}
		ki.CorrectCount = correct
		ki.PracticeCount = total
		if total > 0 {
			ki.Accuracy = float64(correct) / float64(total)
		}
		ki.LastReviewedAt = last
		out = append(out, ki)
	}
	if err := rows.Err(); err != nil {
		return nil, dbErr(err)
	}
	return &domain.Insight{Dimension: domain.InsightDimensionKnowledge, Knowledge: out}, nil
}

// insightTime 时间分析（R5：时段分布/单次平均时长/专注中断原因）。
func (r *ReportService) insightTime(ctx context.Context, wsID, userID string) (*domain.Insight, error) {
	db := r.s.Repo.DB()
	now := r.Now().UTC()
	start := now.AddDate(0, 0, -29).Format(time.RFC3339)

	dist, err := queryTimeDistribution(ctx, db, wsID, userID, start, now.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	var avgSec float64
	if err := db.QueryRowContext(ctx, `
		SELECT COALESCE(avg(actual_seconds), 0) FROM timer_sessions
		WHERE workspace_id = ? AND user_id = ? AND status = 'completed'
		  AND COALESCE(started_at, created_at) >= ?`,
		wsID, userID, start).Scan(&avgSec); err != nil {
		return nil, dbErr(err)
	}
	interrupts, err := queryInterruptReasons(ctx, db, wsID, userID, start, now.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	ti := &domain.TimeInsight{
		Distribution:     dist,
		AvgSessionMin:    float64(int(avgSec/6+0.5)) / 10, // 秒 → 分钟，保留 1 位
		InterruptReasons: interrupts,
	}
	return &domain.Insight{Dimension: domain.InsightDimensionTime, Time: ti}, nil
}

// insightTrend 趋势洞察（R7：近 7 日每日练习量与正确率）。
func (r *ReportService) insightTrend(ctx context.Context, wsID, userID string) (*domain.Insight, error) {
	db := r.s.Repo.DB()
	now := r.Now().UTC()
	start := now.AddDate(0, 0, -6).Format(time.RFC3339)
	trend, err := queryTrend(ctx, db, wsID, userID, start, now.Add(time.Hour*24).Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	return &domain.Insight{Dimension: domain.InsightDimensionTrend, Trend: &domain.TrendInsight{Points: trend}}, nil
}

// readReport 读回报告；不存在返回 NOT_FOUND。
func (r *ReportService) readReport(ctx context.Context, workspaceID, reportID string) (*Report, error) {
	row, err := r.s.Repo.GetReport(ctx, workspaceID, reportID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, domain.NotFound("报告不存在或已被删除")
	}
	return reportFromRow(row), nil
}

func reportFromRow(row *repository.ReportRow) *Report {
	return &Report{
		ID: row.ID, WorkspaceID: row.WorkspaceID, UserID: row.UserID,
		Period: row.Period, PeriodStart: row.PeriodStart, PeriodEnd: row.PeriodEnd,
		Payload: json.RawMessage(row.PayloadJSON), Status: row.Status,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

// reportPeriodWindow 解析 period_start/period_end（YYYY-MM-DD，缺失时按周期推导）。
func reportPeriodWindow(period, startStr, endStr string) (time.Time, time.Time, error) {
	parse := func(s string) (time.Time, error) {
		return time.Parse("2006-01-02", s)
	}
	var start, end time.Time
	var err error
	if startStr != "" {
		start, err = parse(startStr)
		if err != nil {
			return time.Time{}, time.Time{}, domain.InvalidArg("period_start 须为 YYYY-MM-DD")
		}
	}
	if endStr != "" {
		end, err = parse(endStr)
		if err != nil {
			return time.Time{}, time.Time{}, domain.InvalidArg("period_end 须为 YYYY-MM-DD")
		}
	}
	if startStr == "" && endStr == "" {
		return time.Time{}, time.Time{}, domain.InvalidArg("period_start / period_end 至少提供一个")
	}
	if startStr == "" {
		start = end
	}
	if endStr == "" {
		end = start
	}
	if end.Before(start) {
		return time.Time{}, time.Time{}, domain.InvalidArg("period_end 不能早于 period_start")
	}
	// 周期内截止到 end 当日 23:59:59（聚合用 [start, end+1day)）
	start = start.UTC()
	end = end.AddDate(0, 0, 1).UTC()
	return start, end, nil
}

func reportCacheKey(wsID, userID, period, startStr, endStr string) string {
	return wsID + "|" + userID + "|" + period + "|" + startStr + "|" + endStr
}

// queryWeakKnowledge 查询期间内薄弱知识点（按错题数降序，limit 限制条数）。
func queryWeakKnowledge(ctx context.Context, db *sql.DB, wsID, userID, startStr, endStr string, limit int) ([]domain.WeakKnowledgeItem, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT k.id, k.name, count(*) AS cnt
		FROM wrong_answers w
		JOIN question_knowledge qk ON qk.question_version_id = w.question_version_id
		JOIN knowledge_nodes k ON k.id = qk.knowledge_id
		WHERE w.workspace_id = ? AND w.user_id = ? AND w.deleted_at IS NULL AND w.status = 'active'
		  AND w.created_at >= ? AND w.created_at < ?
		GROUP BY k.id, k.name
		ORDER BY cnt DESC
		LIMIT ?`, wsID, userID, startStr, endStr, limit)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	var out []domain.WeakKnowledgeItem
	for rows.Next() {
		var wk domain.WeakKnowledgeItem
		if err := rows.Scan(&wk.KnowledgeID, &wk.Name, &wk.WrongCount); err != nil {
			return nil, dbErr(err)
		}
		out = append(out, wk)
	}
	if err := rows.Err(); err != nil {
		return nil, dbErr(err)
	}
	return out, nil
}

// queryTrend 查询期间内每日练习量与正确数（按日升序）。
func queryTrend(ctx context.Context, db *sql.DB, wsID, userID, startStr, endStr string) ([]domain.TrendPoint, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT date(s.submitted_at),
		       count(*),
		       COALESCE(sum(CASE WHEN g.score >= g.max_score THEN 1 ELSE 0 END), 0)
		FROM submissions s
		JOIN practice_sessions p ON s.session_id = p.id
		JOIN grading_results g ON g.submission_id = s.id
		WHERE p.workspace_id = ? AND p.user_id = ? AND s.status = 'submitted'
		  AND g.status = 'completed' AND g.method = 'rule'
		  AND s.submitted_at >= ? AND s.submitted_at < ?
		GROUP BY date(s.submitted_at)
		ORDER BY date(s.submitted_at)`, wsID, userID, startStr, endStr)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	var out []domain.TrendPoint
	for rows.Next() {
		var tp domain.TrendPoint
		var correct int
		if err := rows.Scan(&tp.Date, &tp.PracticeCount, &correct); err != nil {
			return nil, dbErr(err)
		}
		tp.CorrectCount = correct
		if tp.PracticeCount > 0 {
			tp.Accuracy = float64(correct) / float64(tp.PracticeCount)
		}
		out = append(out, tp)
	}
	if err := rows.Err(); err != nil {
		return nil, dbErr(err)
	}
	return out, nil
}

// queryTimeDistribution 查询学习时段分布（早<12 / 午12-18 / 晚>=18，UTC）。
func queryTimeDistribution(ctx context.Context, db *sql.DB, wsID, userID, startStr, endStr string) (domain.TimeDistribution, error) {
	var dist domain.TimeDistribution
	rows, err := db.QueryContext(ctx, `
		SELECT
		  CASE WHEN CAST(strftime('%H', s.submitted_at) AS INTEGER) < 12 THEN 'morning'
		       WHEN CAST(strftime('%H', s.submitted_at) AS INTEGER) < 18 THEN 'afternoon'
		       ELSE 'evening' END AS slot,
		  count(*)
		FROM submissions s
		JOIN practice_sessions p ON s.session_id = p.id
		WHERE p.workspace_id = ? AND p.user_id = ? AND s.status = 'submitted'
		  AND s.submitted_at >= ? AND s.submitted_at < ?
		GROUP BY slot`, wsID, userID, startStr, endStr)
	if err != nil {
		return dist, dbErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var slot string
		var n int
		if err := rows.Scan(&slot, &n); err != nil {
			return dist, dbErr(err)
		}
		switch slot {
		case "morning":
			dist.Morning = n
		case "afternoon":
			dist.Afternoon = n
		case "evening":
			dist.Evening = n
		}
	}
	if err := rows.Err(); err != nil {
		return dist, dbErr(err)
	}
	return dist, nil
}

// queryInterruptReasons 统计期间内专注中断原因分布。
func queryInterruptReasons(ctx context.Context, db *sql.DB, wsID, userID, startStr, endStr string) (map[string]int, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT COALESCE(interrupt_reason, ''), count(*)
		FROM timer_sessions
		WHERE workspace_id = ? AND user_id = ? AND status IN ('interrupted', 'abandoned')
		  AND COALESCE(started_at, created_at) >= ? AND COALESCE(started_at, created_at) < ?
		GROUP BY interrupt_reason`, wsID, userID, startStr, endStr)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var reason string
		var n int
		if err := rows.Scan(&reason, &n); err != nil {
			return nil, dbErr(err)
		}
		if reason == "" {
			reason = "unknown"
		}
		out[reason] = n
	}
	if err := rows.Err(); err != nil {
		return nil, dbErr(err)
	}
	return out, nil
}

// buildReportSuggestions 基于聚合结果生成确定性建议。
func buildReportSuggestions(summary domain.ReportSummary, weak []domain.WeakKnowledgeItem) []string {
	var out []string
	if summary.ReviewDue > 0 {
		out = append(out, fmt.Sprintf("有 %d 张复习卡到期，建议优先完成复习（间隔记忆效果最佳）。", summary.ReviewDue))
	}
	if len(weak) > 0 {
		names := make([]string, 0, 3)
		for _, w := range weak {
			names = append(names, w.Name)
		}
		out = append(out, "近期薄弱知识点："+strings.Join(names, "、")+"，建议针对练习。")
	}
	if summary.PracticeCount == 0 && summary.ReviewDone == 0 {
		out = append(out, "本期暂无练习与复习记录，试试从一次练习开始。")
	}
	if summary.AccuracySamples >= domain.MinReportSample && summary.Accuracy < 0.6 {
		out = append(out, fmt.Sprintf("本期正确率 %.0f%%，可适当降低难度巩固基础。", summary.Accuracy*100))
	}
	if summary.FocusMinutes == 0 {
		out = append(out, "本期没有专注记录，可用番茄钟保持学习节奏。")
	}
	if len(out) == 0 {
		out = append(out, "状态很好：保持每日练习节奏，数据将在下一期报告中继续累积。")
	}
	return out
}

// renderReportPDF 用 fpdf 渲染报告 PDF（嵌入 CJK 字体渲染中文）。
// 找不到中文字体时回退核心字体（仅拉丁字符可渲染，返回 err 由调用方回退 JSON）。
func renderReportPDF(payload *domain.ReportPayload) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetAutoPageBreak(true, 20)
	pdf.AddPage()

	cjk := reportCJKFontBytes()
	if cjk != nil {
		pdf.AddUTF8FontFromBytes("cjk", "", cjk)
	}
	family := "Helvetica"
	if cjk != nil {
		family = "cjk"
	}
	pdf.SetFont(family, "B", 16)
	pdf.Cell(0, 10, "学习报告")
	pdf.Ln(8)
	pdf.SetFont(family, "", 10)
	pdf.Cell(0, 6, fmt.Sprintf("周期：%s  %s ~ %s", reportPeriodLabel(payload.Period), payload.PeriodStart[:10], payload.PeriodEnd[:10]))
	pdf.Ln(4)
	pdf.Cell(0, 6, "生成时间："+payload.GeneratedAt)
	pdf.Ln(10)

	s := payload.Summary
	lines := []string{
		fmt.Sprintf("练习量：%d 题", s.PracticeCount),
		fmt.Sprintf("正确率：%s（样本 %d）", accuracyText(s), s.AccuracySamples),
		fmt.Sprintf("复习：完成 %d 次，到期 %d 张", s.ReviewDone, s.ReviewDue),
		fmt.Sprintf("专注时长：%d 分钟（%d 次）", s.FocusMinutes, s.FocusSessions),
		fmt.Sprintf("打卡天数：%d 天", s.CheckinDays),
		fmt.Sprintf("任务完成：%d / %d", s.TaskDone, s.TaskTotal),
	}
	pdf.SetFont(family, "", 11)
	for _, l := range lines {
		pdf.Cell(0, 8, l)
		pdf.Ln(8)
	}
	pdf.Ln(4)

	if len(payload.WeakKnowledge) > 0 {
		pdf.SetFont(family, "B", 12)
		pdf.Cell(0, 8, "薄弱知识点 Top"+fmt.Sprint(len(payload.WeakKnowledge)))
		pdf.Ln(8)
		pdf.SetFont(family, "", 11)
		for _, w := range payload.WeakKnowledge {
			pdf.Cell(0, 7, fmt.Sprintf("• %s（错题 %d 次）", w.Name, w.WrongCount))
			pdf.Ln(7)
		}
		pdf.Ln(4)
	}

	if len(payload.Suggestions) > 0 {
		pdf.SetFont(family, "B", 12)
		pdf.Cell(0, 8, "建议")
		pdf.Ln(8)
		pdf.SetFont(family, "", 11)
		for _, sg := range payload.Suggestions {
			pdf.MultiCell(0, 7, "• "+sg, "", "", false)
			pdf.Ln(2)
		}
	}

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func accuracyText(s domain.ReportSummary) string {
	if s.SampleInsufficient {
		return "样本不足"
	}
	return fmt.Sprintf("%.0f%%", s.Accuracy*100)
}

func reportPeriodLabel(p string) string {
	switch p {
	case domain.ReportPeriodDaily:
		return "日报"
	case domain.ReportPeriodWeekly:
		return "周报"
	case domain.ReportPeriodMonthly:
		return "月报"
	}
	return p
}

// reportCJKFontBytes 定位系统 CJK TrueType 字体（Windows/macOS/Linux 常见路径）。
// 找不到返回 nil（调用方回退核心字体/JSON）。
func reportCJKFontBytes() []byte {
	candidates := []string{
		`C:\Windows\Fonts\Deng.ttf`,
		`C:\Windows\Fonts\msyh.ttf`,
		`C:\Windows\Fonts\simsun.ttc`,
		`C:\Windows\Fonts\simhei.ttf`,
		`/System/Library/Fonts/PingFang.ttc`,
		`/System/Library/Fonts/STHeiti Light.ttc`,
		`/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc`,
		`/usr/share/fonts/truetype/droid/DroidSansFallbackFull.ttf`,
	}
	for _, p := range candidates {
		b, err := os.ReadFile(p)
		if err == nil && len(b) > 1024 {
			return b
		}
	}
	return nil
}
