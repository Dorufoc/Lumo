package service

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"time"

	"lumo/internal/agent"
	"lumo/internal/domain"
	"lumo/internal/repository"
)

// ExamPaperSection 是试卷大题 DTO。
type ExamPaperSection struct {
	ID                string   `json:"id"`
	PaperID           string   `json:"paper_id"`
	Title             string   `json:"title"`
	OrderNo           int      `json:"order_no"`
	QuestionVersionIDs []string `json:"question_version_ids"`
	Score             int      `json:"score"`
}

// ExamPaper 是试卷 DTO。
type ExamPaper struct {
	ID          string              `json:"id"`
	WorkspaceID string              `json:"workspace_id"`
	UserID      string              `json:"user_id"`
	Title       string              `json:"title"`
	Config      json.RawMessage     `json:"config_json"`
	Status      string              `json:"status"`
	Sections    []*ExamPaperSection `json:"sections"`
	Version     int                 `json:"version"`
	CreatedAt   string              `json:"created_at"`
	UpdatedAt   string              `json:"updated_at"`
}

// Exam 是考试 DTO（共享 ID = practice_session.id，mode='exam'）。
type Exam struct {
	ID          string              `json:"id"`
	PaperID     string              `json:"paper_id"`
	UserID      string              `json:"user_id"`
	Status      string              `json:"status"`
	DurationMin int                 `json:"duration_min"`
	StartedAt   *string             `json:"started_at"`
	EndedAt     *string             `json:"ended_at"`
	Questions   []*PracticeQuestion `json:"questions"`
	CreatedAt   string              `json:"created_at"`
	UpdatedAt   string              `json:"updated_at"`
}

// ExamResult 是考试结果 DTO（成绩/复盘/错题入队列）。
type ExamResult struct {
	ExamID        string              `json:"exam_id"`
	PaperID       string              `json:"paper_id"`
	Status        string              `json:"status"`
	TotalScore    float64             `json:"total_score"`
	MaxScore      float64             `json:"max_score"`
	DurationMin   int                 `json:"duration_min"`
	StartedAt     *string             `json:"started_at"`
	EndedAt       *string             `json:"ended_at"`
	Questions     []*ResultQuestion   `json:"questions"`
	WrongAnswers  []*WrongAnswerItem  `json:"wrong_answers"`
	ReviewActions []*ReviewAction     `json:"review_actions"`
}

// ScoreSummary 是 exams.score_summary_json 内容。
type ScoreSummary struct {
	TotalScore    float64            `json:"total_score"`
	MaxScore      float64            `json:"max_score"`
	WrongCount    int                `json:"wrong_count"`
	QuestionCount int                `json:"question_count"`
	DurationMin   int                `json:"duration_min"`
	StartedAt     string             `json:"started_at"`
	EndedAt       string             `json:"ended_at"`
	WrongAnswers  []*WrongAnswerItem `json:"wrong_answers"`
	ReviewActions []*ReviewAction    `json:"review_actions"`
}

// ExamService 实现组卷与考试用例（4.10）。
// Now 可注入时钟，测试中用于推进倒计时触发自动提交。
type ExamService struct {
	s   *Services
	Now func() time.Time
}

// ExamPaperListReq 试卷列表请求。
type ExamPaperListReq struct {
	WorkspaceID string `json:"workspace_id"`
	Limit       int    `json:"limit"`
}

// ExamPaperList 列出工作区试卷（默认最新 100 张，含各大题）。
func (e *ExamService) ExamPaperList(ctx context.Context, req ExamPaperListReq) ([]*ExamPaper, error) {
	if err := e.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.Limit <= 0 || req.Limit > 100 {
		req.Limit = 100
	}
	rows, err := e.s.Repo.ListExamPapers(ctx, req.WorkspaceID, req.Limit)
	if err != nil {
		return nil, err
	}
	out := make([]*ExamPaper, 0, len(rows))
	for _, p := range rows {
		paper, err := e.paperByID(ctx, req.WorkspaceID, p.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, paper)
	}
	return out, nil
}

// ExamPaperCreateReq 手动组卷请求。
type ExamPaperCreateReq struct {
	WorkspaceID    string          `json:"workspace_id"`
	UserID         string          `json:"user_id"`
	Title          string          `json:"title"`
	ConfigJSON     json.RawMessage `json:"config_json"`
	IdempotencyKey string          `json:"idempotency_key"`
}

// ExamPaperCreate 手动组卷：解析 config_json，创建试卷与各大题。
func (e *ExamService) ExamPaperCreate(ctx context.Context, req ExamPaperCreateReq) (*ExamPaper, error) {
	if err := e.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if len(req.Title) == 0 || len(req.Title) > 200 {
		return nil, domain.InvalidArg("title 长度须为 1-200")
	}
	cfg, err := domain.ParseExamPaperConfig(req.ConfigJSON)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return withIdempotency(e.s, ctx, req.WorkspaceID, req.IdempotencyKey, "ExamPaperCreate", func() (*ExamPaper, error) {
		return e.createPaper(ctx, req.WorkspaceID, req.UserID, req.Title, req.ConfigJSON, cfg.Sections)
	})
}

// ExamAutoGenerateConfig 自动组卷配置。
type ExamAutoGenerateConfig struct {
	KnowledgeRatio map[string]float64 `json:"knowledge_ratio"`
	DifficultyDist map[string]float64 `json:"difficulty_dist"`
	Count          int                `json:"count"`
	Types          []string           `json:"types"`
	DurationMin    int                `json:"duration_min"`
}

// ExamPaperAutoGenerateReq 自动组卷请求。
type ExamPaperAutoGenerateReq struct {
	WorkspaceID    string                 `json:"workspace_id"`
	UserID         string                 `json:"user_id"`
	Title          string                 `json:"title"`
	Config         ExamAutoGenerateConfig `json:"config"`
	IdempotencyKey string                 `json:"idempotency_key"`
}

// ExamPaperAutoGenerate 按配置抽题组卷；不满足知识点覆盖率/难度分布返回 INVALID_ARGUMENT。
func (e *ExamService) ExamPaperAutoGenerate(ctx context.Context, req ExamPaperAutoGenerateReq) (*ExamPaper, error) {
	if err := e.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if len(req.Title) == 0 || len(req.Title) > 200 {
		return nil, domain.InvalidArg("title 长度须为 1-200")
	}
	if req.Config.Count <= 0 {
		return nil, domain.InvalidArg("count 必须大于 0")
	}
	if req.Config.DurationMin <= 0 {
		return nil, domain.InvalidArg("duration_min 必须大于 0")
	}
	if len(req.Config.KnowledgeRatio) == 0 {
		return nil, domain.InvalidArg("knowledge_ratio 不能为空")
	}
	if len(req.Config.DifficultyDist) == 0 {
		return nil, domain.InvalidArg("difficulty_dist 不能为空")
	}
	return withIdempotency(e.s, ctx, req.WorkspaceID, req.IdempotencyKey, "ExamPaperAutoGenerate", func() (*ExamPaper, error) {
		selected, err := e.pickQuestions(ctx, req.WorkspaceID, req.Config)
		if err != nil {
			return nil, err
		}
		section := domain.ExamPaperSectionConfig{
			Title: "自动组卷", OrderNo: 1,
			QuestionVersionIDs: selected, Score: 10 * len(selected),
		}
		configJSON := mustJSON(map[string]any{
			"duration_min": req.Config.DurationMin,
			"sections":     []domain.ExamPaperSectionConfig{section},
		})
		return e.createPaper(ctx, req.WorkspaceID, req.UserID, req.Title, configJSON,
			[]domain.ExamPaperSectionConfig{section})
	})
}

// pickQuestions 按知识点占比与难度分布抽题；不足时返回 INVALID_ARGUMENT。
func (e *ExamService) pickQuestions(ctx context.Context, wsID string, cfg ExamAutoGenerateConfig) ([]string, error) {
	type cand struct {
		qvid       string
		difficulty int
	}
	// knowledge → (difficulty → qvids)
	byKnowledge := map[string]map[int][]string{}
	typeFilter := map[string]bool{}
	for _, t := range cfg.Types {
		typeFilter[t] = true
	}
	rows, _, _, err := e.s.Repo.ListQuestions(ctx, wsID, repository.QuestionFilter{Status: "published", Limit: 100})
	if err != nil {
		return nil, err
	}
	for _, q := range rows {
		if q.CurrentVersionID == nil {
			continue
		}
		v, err := e.s.Repo.GetQuestionVersion(ctx, *q.CurrentVersionID)
		if err != nil || v == nil {
			continue
		}
		pl, err := parseQuestionPayload(v.Payload)
		if err != nil {
			continue
		}
		if len(typeFilter) > 0 && !typeFilter[pl.Type] {
			continue
		}
		for _, kid := range pl.KnowledgeIDs {
			if byKnowledge[kid] == nil {
				byKnowledge[kid] = map[int][]string{}
			}
			byKnowledge[kid][pl.Difficulty] = append(byKnowledge[kid][pl.Difficulty], v.ID)
		}
	}

	// 校验每个知识点覆盖率与难度分布是否可满足
	selected := map[string]bool{}
	var order []string
	for kid, ratio := range cfg.KnowledgeRatio {
		targetK := int(math.Round(float64(cfg.Count) * ratio))
		if targetK <= 0 {
			continue
		}
		pool := byKnowledge[kid]
		total := 0
		for _, ids := range pool {
			total += len(ids)
		}
		if total < targetK {
			return nil, domain.InvalidArg("知识点 %s 题目不足：需 %d 道，实际可用 %d 道", kid, targetK, total)
		}
		for diffKey, dRatio := range cfg.DifficultyDist {
			diff, err := strconv.Atoi(diffKey)
			if err != nil {
				return nil, domain.InvalidArg("difficulty_dist 键 %q 不是数字", diffKey)
			}
			targetD := int(math.Round(float64(targetK) * dRatio))
			if targetD <= 0 {
				continue
			}
			ids := pool[diff]
			if len(ids) < targetD {
				return nil, domain.InvalidArg("知识点 %s 难度 %s 题目不足：需 %d 道，实际可用 %d 道", kid, diffKey, targetD, len(ids))
			}
			sort.Strings(ids)
			for _, id := range ids {
				if targetD <= 0 {
					break
				}
				if !selected[id] {
					selected[id] = true
					order = append(order, id)
					targetD--
				}
			}
		}
	}
	if len(order) < cfg.Count {
		return nil, domain.InvalidArg("可组题目不足：需 %d 道，实际可组 %d 道", cfg.Count, len(order))
	}
	return order[:cfg.Count], nil
}

// ExamPaperPublishReq 发布试卷请求。
type ExamPaperPublishReq struct {
	WorkspaceID string `json:"workspace_id"`
	PaperID     string `json:"paper_id"`
	Version     int    `json:"version"`
}

// ExamPaperPublish 发布试卷（draft→published，乐观锁；发布后题目版本冻结）。
func (e *ExamService) ExamPaperPublish(ctx context.Context, req ExamPaperPublishReq) (*ExamPaper, error) {
	if err := e.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	paper, err := e.s.Repo.GetExamPaper(ctx, req.WorkspaceID, req.PaperID)
	if err != nil {
		return nil, err
	}
	if paper == nil {
		return nil, domain.NotFound("试卷不存在")
	}
	if paper.Status != domain.ExamPaperStatusDraft {
		return nil, domain.InvalidState("仅草稿试卷可发布")
	}
	// 发布前校验题目均可用（题目版本冻结前最后一道闸）。
	sections, err := e.s.Repo.GetExamPaperSections(ctx, req.PaperID)
	if err != nil {
		return nil, err
	}
	for _, s := range sections {
		var ids []string
		if err := json.Unmarshal([]byte(s.QuestionVersionIDs), &ids); err != nil {
			return nil, domain.InvalidArg("大题 %s 题目引用损坏", s.ID)
		}
		for _, qvid := range ids {
			v, err := e.s.Repo.GetQuestionVersion(ctx, qvid)
			if err != nil {
				return nil, err
			}
			if v == nil {
				return nil, domain.InvalidArg("题目版本不存在：%s", qvid)
			}
			q, err := e.s.Repo.GetQuestion(ctx, req.WorkspaceID, v.QuestionID)
			if err != nil {
				return nil, err
			}
			if q == nil || q.Status != "published" {
				return nil, domain.InvalidArg("试卷包含未发布题目，请先发布题目")
			}
		}
	}
	updated, err := e.s.Repo.UpdateExamPaperStatus(ctx, req.WorkspaceID, req.PaperID, req.Version, domain.ExamPaperStatusPublished)
	if err != nil {
		return nil, err
	}
	e.s.audit(ctx, req.WorkspaceID, "exam.publish", "exam_paper", req.PaperID, map[string]any{"version": updated.Version})
	return e.paperByID(ctx, req.WorkspaceID, req.PaperID)
}

// ExamStartReq 开始考试请求。
type ExamStartReq struct {
	WorkspaceID    string `json:"workspace_id"`
	UserID         string `json:"user_id"`
	PaperID        string `json:"paper_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

// ExamStart 开始考试：锁定题目顺序/时长；进行中重入返回 EXAM_IN_PROGRESS。
// 考试与练习会话共享同一 ID（mode='exam'），答题经既有 PracticeSaveAnswer 落库。
func (e *ExamService) ExamStart(ctx context.Context, req ExamStartReq) (*Exam, error) {
	if err := e.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	paper, err := e.s.Repo.GetExamPaper(ctx, req.WorkspaceID, req.PaperID)
	if err != nil {
		return nil, err
	}
	if paper == nil {
		return nil, domain.NotFound("试卷不存在")
	}
	if paper.Status != domain.ExamPaperStatusPublished {
		return nil, domain.InvalidState("试卷尚未发布，不能开始考试")
	}
	cfg, err := domain.ParseExamPaperConfig(json.RawMessage(paper.ConfigJSON))
	if err != nil {
		return nil, err
	}
	if cfg.DurationMin <= 0 {
		return nil, domain.InvalidArg("试卷未配置考试时长 duration_min")
	}
	return withIdempotency(e.s, ctx, req.WorkspaceID, req.IdempotencyKey, "ExamStart", func() (*Exam, error) {
		if existing, err := e.s.Repo.GetInProgressExam(ctx, req.UserID, req.PaperID); err != nil {
			return nil, err
		} else if existing != nil {
			return nil, domain.ExamInProgress("该试卷已有进行中的考试，请先完成或等待自动提交")
		}
		sections, err := e.s.Repo.GetExamPaperSections(ctx, req.PaperID)
		if err != nil {
			return nil, err
		}
		snapshot, err := e.buildSnapshot(ctx, sections)
		if err != nil {
			return nil, err
		}
		now := e.Now().UTC().Format(time.RFC3339)
		examID := NewID()
		session := &repository.PracticeSessionRow{
			ID: examID, WorkspaceID: req.WorkspaceID, UserID: req.UserID,
			Mode: "exam", QuestionSnapshot: mustJSON(snapshot), StartedAt: &now,
		}
		if err := e.s.Repo.CreateSession(ctx, session); err != nil {
			return nil, err
		}
		if err := e.s.Repo.MarkSessionStarted(ctx, examID); err != nil {
			return nil, err
		}
		if err := e.s.Repo.CreateExam(ctx, &repository.ExamRow{
			ID: examID, PaperID: req.PaperID, UserID: req.UserID,
			Status: domain.ExamStatusAnswering, StartedAt: &now,
		}); err != nil {
			return nil, err
		}
		e.s.audit(ctx, req.WorkspaceID, "exam.start", "exam", examID, map[string]any{"paper_id": req.PaperID})
		return e.examByID(ctx, req.WorkspaceID, examID, cfg.DurationMin)
	})
}

// buildSnapshot 按大题顺序构造题目快照（锁定顺序与分值）。
func (e *ExamService) buildSnapshot(ctx context.Context, sections []*repository.ExamPaperSectionRow) ([]*PracticeQuestion, error) {
	var out []*PracticeQuestion
	orderNo := 0
	for _, s := range sections {
		var ids []string
		if err := json.Unmarshal([]byte(s.QuestionVersionIDs), &ids); err != nil {
			return nil, domain.InvalidArg("大题 %s 题目引用损坏", s.ID)
		}
		for _, qvid := range ids {
			v, err := e.s.Repo.GetQuestionVersion(ctx, qvid)
			if err != nil {
				return nil, err
			}
			if v == nil {
				return nil, domain.NotFound("题目版本不存在")
			}
			pl, err := parseQuestionPayload(v.Payload)
			if err != nil {
				return nil, err
			}
			orderNo++
			out = append(out, &PracticeQuestion{
				OrderNo: orderNo, QuestionID: v.QuestionID, QuestionVersionID: v.ID,
				Type: pl.Type, MaxScore: gradingMaxScore(pl, 10),
			})
		}
	}
	if len(out) == 0 {
		return nil, domain.InvalidArg("试卷没有可考试的题目")
	}
	return out, nil
}

// ExamAutoSubmitReq 自动提交请求。
type ExamAutoSubmitReq struct {
	WorkspaceID string `json:"workspace_id"`
	ExamID      string `json:"exam_id"`
}

// ExamAutoSubmit 到期自动提交：惰性时钟检查，未到期返回进行中状态，已提交返回 CONFLICT。
func (e *ExamService) ExamAutoSubmit(ctx context.Context, req ExamAutoSubmitReq) (*ExamResult, error) {
	if err := e.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	exam, err := e.s.Repo.GetExam(ctx, req.WorkspaceID, req.ExamID)
	if err != nil {
		return nil, err
	}
	if exam == nil {
		return nil, domain.NotFound("考试不存在")
	}
	if exam.Status == domain.ExamStatusGraded {
		return nil, domain.Conflict("考试已自动提交，请勿重复提交")
	}
	result, finalized, err := e.maybeAutoSubmit(ctx, req.WorkspaceID, req.ExamID, e.Now())
	if err != nil {
		return nil, err
	}
	if finalized {
		return result, nil
	}
	// 未到期：返回进行中状态（客户端继续倒计时）。
	cfg, _ := domain.ParseExamPaperConfig(json.RawMessage(e.paperConfigOf(ctx, req.WorkspaceID, exam.PaperID)))
	duration := 0
	if cfg != nil {
		duration = cfg.DurationMin
	}
	return &ExamResult{
		ExamID: exam.ID, PaperID: exam.PaperID, Status: exam.Status,
		DurationMin: duration, StartedAt: exam.StartedAt, EndedAt: exam.EndedAt,
	}, nil
}

// ExamGetResultReq 获取考试结果请求。
type ExamGetResultReq struct {
	WorkspaceID string `json:"workspace_id"`
	ExamID      string `json:"exam_id"`
}

// ExamGetResult 获取成绩/复盘：入口惰性检查，到期先自动提交再出分。
func (e *ExamService) ExamGetResult(ctx context.Context, req ExamGetResultReq) (*ExamResult, error) {
	if err := e.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	exam, err := e.s.Repo.GetExam(ctx, req.WorkspaceID, req.ExamID)
	if err != nil {
		return nil, err
	}
	if exam == nil {
		return nil, domain.NotFound("考试不存在")
	}
	if _, _, err := e.maybeAutoSubmit(ctx, req.WorkspaceID, req.ExamID, e.Now()); err != nil {
		return nil, err
	}
	exam, err = e.s.Repo.GetExam(ctx, req.WorkspaceID, req.ExamID)
	if err != nil {
		return nil, err
	}
	if exam.Status != domain.ExamStatusGraded {
		return nil, domain.InvalidState("考试尚未结束")
	}
	return e.examResultFromSummary(ctx, req.WorkspaceID, exam)
}

// maybeAutoSubmit 惰性自动提交：now - started_at >= duration_min 且状态为进行中时，
// 复用练习判分/错题归档流水线完成判分，写入成绩摘要并发布 exam:auto_submitted 事件。
func (e *ExamService) maybeAutoSubmit(ctx context.Context, wsID, examID string, now time.Time) (*ExamResult, bool, error) {
	exam, err := e.s.Repo.GetExam(ctx, wsID, examID)
	if err != nil {
		return nil, false, err
	}
	if exam == nil || exam.Status != domain.ExamStatusAnswering {
		return nil, false, nil
	}
	cfg, err := domain.ParseExamPaperConfig(json.RawMessage(e.paperConfigOf(ctx, wsID, exam.PaperID)))
	if err != nil || cfg == nil || cfg.DurationMin <= 0 {
		// 缺少 duration_min：惰性检查永不触发。
		return nil, false, nil
	}
	started, err := domain.ParseTime(*exam.StartedAt)
	if err != nil {
		return nil, false, nil
	}
	if now.Sub(started) < time.Duration(cfg.DurationMin)*time.Minute {
		return nil, false, nil
	}
	return e.finalize(ctx, wsID, exam, cfg.DurationMin, now)
}

// finalize 判分并落库成绩摘要。
func (e *ExamService) finalize(ctx context.Context, wsID string, exam *repository.ExamRow, durationMin int, now time.Time) (*ExamResult, bool, error) {
	session, err := e.s.Repo.GetSession(ctx, wsID, exam.ID)
	if err != nil {
		return nil, false, err
	}
	if session == nil || session.Status != "answering" {
		return nil, false, nil
	}
	result, err := e.s.Practice.doSubmit(ctx, wsID, exam.ID, session.Version)
	if err != nil {
		return nil, false, err
	}
	endedAt := now.UTC().Format(time.RFC3339)
	summary := &ScoreSummary{
		TotalScore: result.TotalScore, MaxScore: result.MaxScore,
		WrongCount: len(result.WrongAnswers), QuestionCount: len(result.Questions),
		DurationMin: durationMin, StartedAt: *exam.StartedAt, EndedAt: endedAt,
		WrongAnswers: result.WrongAnswers, ReviewActions: result.ReviewActions,
	}
	if err := e.s.Repo.UpdateExamFinalized(ctx, exam.ID, domain.ExamStatusGraded, endedAt, string(mustJSON(summary))); err != nil {
		return nil, false, err
	}
	// 发布用户级领域事件（不阻塞答题；Agent 未装配时静默）。
	if e.s.Agent != nil && e.s.Agent.UserEvents != nil {
		_ = e.s.Agent.UserEvents.Publish(exam.UserID, agent.Event{
			Name: agent.EventExamAutoSubmitted,
			Payload: map[string]any{"exam_id": exam.ID, "status": domain.ExamStatusGraded},
		})
	}
	e.s.audit(ctx, wsID, "exam.auto_submit", "exam", exam.ID,
		map[string]any{"score": result.TotalScore, "max": result.MaxScore})
	out := &ExamResult{
		ExamID: exam.ID, PaperID: exam.PaperID, Status: domain.ExamStatusGraded,
		TotalScore: result.TotalScore, MaxScore: result.MaxScore, DurationMin: durationMin,
		StartedAt: exam.StartedAt, EndedAt: &endedAt,
		Questions: result.Questions, WrongAnswers: result.WrongAnswers,
		ReviewActions: result.ReviewActions,
	}
	return out, true, nil
}

// examResultFromSummary 从练习结果与成绩摘要组装考试结果 DTO。
func (e *ExamService) examResultFromSummary(ctx context.Context, wsID string, exam *repository.ExamRow) (*ExamResult, error) {
	res, err := e.s.Practice.PracticeGetResult(ctx, PracticeGetResultReq{WorkspaceID: wsID, SessionID: exam.ID})
	if err != nil {
		return nil, err
	}
	var summary ScoreSummary
	_ = json.Unmarshal([]byte(exam.ScoreSummaryJSON), &summary)
	return &ExamResult{
		ExamID: exam.ID, PaperID: exam.PaperID, Status: exam.Status,
		TotalScore: res.TotalScore, MaxScore: res.MaxScore, DurationMin: summary.DurationMin,
		StartedAt: exam.StartedAt, EndedAt: exam.EndedAt,
		Questions: res.Questions, WrongAnswers: summary.WrongAnswers,
		ReviewActions: summary.ReviewActions,
	}, nil
}

// paperConfigOf 读取试卷 config_json（供惰性时长检查）。
func (e *ExamService) paperConfigOf(ctx context.Context, wsID, paperID string) string {
	p, err := e.s.Repo.GetExamPaper(ctx, wsID, paperID)
	if err != nil || p == nil {
		return "{}"
	}
	return p.ConfigJSON
}

// createPaper 创建试卷与各大题（草稿态）。
func (e *ExamService) createPaper(ctx context.Context, wsID, userID, title string, configJSON json.RawMessage, sectionConfigs []domain.ExamPaperSectionConfig) (*ExamPaper, error) {
	paperID := NewID()
	paper := &repository.ExamPaperRow{
		ID: paperID, WorkspaceID: wsID, UserID: userID, Title: title,
		ConfigJSON: string(configJSON), Status: domain.ExamPaperStatusDraft, Version: 1,
	}
	sections := make([]*repository.ExamPaperSectionRow, 0, len(sectionConfigs))
	for _, sc := range sectionConfigs {
		sections = append(sections, &repository.ExamPaperSectionRow{
			ID: NewID(), PaperID: paperID, Title: sc.Title, OrderNo: sc.OrderNo,
			QuestionVersionIDs: string(mustJSON(sc.QuestionVersionIDs)), Score: sc.Score,
		})
	}
	if err := e.s.Repo.CreateExamPaperTx(ctx, paper, sections); err != nil {
		return nil, err
	}
	e.s.audit(ctx, wsID, "exam.paper_create", "exam_paper", paperID, map[string]any{"title": title, "sections": len(sections)})
	return e.paperByID(ctx, wsID, paperID)
}

// paperByID 组装试卷 DTO。
func (e *ExamService) paperByID(ctx context.Context, wsID, id string) (*ExamPaper, error) {
	p, err := e.s.Repo.GetExamPaper(ctx, wsID, id)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, domain.NotFound("试卷不存在")
	}
	sections, err := e.s.Repo.GetExamPaperSections(ctx, id)
	if err != nil {
		return nil, err
	}
	out := &ExamPaper{
		ID: p.ID, WorkspaceID: p.WorkspaceID, UserID: p.UserID, Title: p.Title,
		Config: json.RawMessage(p.ConfigJSON), Status: p.Status,
		Version: p.Version, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
	for _, s := range sections {
		var ids []string
		_ = json.Unmarshal([]byte(s.QuestionVersionIDs), &ids)
		out.Sections = append(out.Sections, &ExamPaperSection{
			ID: s.ID, PaperID: s.PaperID, Title: s.Title, OrderNo: s.OrderNo,
			QuestionVersionIDs: ids, Score: s.Score,
		})
	}
	return out, nil
}

// examByID 组装考试 DTO（答题中隐藏答案）。
func (e *ExamService) examByID(ctx context.Context, wsID, id string, durationMin int) (*Exam, error) {
	exam, err := e.s.Repo.GetExam(ctx, wsID, id)
	if err != nil {
		return nil, err
	}
	if exam == nil {
		return nil, domain.NotFound("考试不存在")
	}
	session, err := e.s.Practice.sessionByID(ctx, wsID, id)
	if err != nil {
		return nil, err
	}
	return &Exam{
		ID: exam.ID, PaperID: exam.PaperID, UserID: exam.UserID, Status: exam.Status,
		DurationMin: durationMin, StartedAt: exam.StartedAt, EndedAt: exam.EndedAt,
		Questions: session.Questions, CreatedAt: exam.CreatedAt, UpdatedAt: exam.UpdatedAt,
	}, nil
}
