package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"lumo/internal/domain"
	"lumo/internal/repository"
)

// stableID 从输入派生确定性 ID：同一 (会话, 题目) 的重试重建产生相同 ID，天然幂等。
func stableID(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(h[:16])
}

// PracticeQuestion 是会话快照中的题目。
type PracticeQuestion struct {
	OrderNo           int             `json:"order_no"`
	QuestionID        string          `json:"question_id"`
	QuestionVersionID string          `json:"question_version_id"`
	Type              string          `json:"type"`
	Payload           json.RawMessage `json:"payload,omitempty"` // 答题中不含标准答案
	MaxScore          float64         `json:"max_score"`
}

// SubmissionDraft 是答案草稿 DTO。
type SubmissionDraft struct {
	ID                string          `json:"id"`
	SessionID         string          `json:"session_id"`
	QuestionVersionID string          `json:"question_version_id"`
	Answer            json.RawMessage `json:"answer"`
	ClientSequence    int             `json:"client_sequence"`
	Status            string          `json:"status"`
	UpdatedAt         string          `json:"updated_at"`
}

// PracticeSession 是练习会话 DTO。
type PracticeSession struct {
	ID           string              `json:"id"`
	WorkspaceID  string              `json:"workspace_id"`
	UserID       string              `json:"user_id"`
	Mode         string              `json:"mode"`
	Status       string              `json:"status"`
	Questions    []*PracticeQuestion `json:"questions"`
	Skipped      []string            `json:"skipped"`
	TimeLimitSec *int                `json:"time_limit_sec"`
	StartedAt    *string             `json:"started_at"`
	SubmittedAt  *string             `json:"submitted_at"`
	Drafts       []*SubmissionDraft  `json:"drafts,omitempty"`
	Version      int                 `json:"version"`
	CreatedAt    string              `json:"created_at"`
	UpdatedAt    string              `json:"updated_at"`
}

// GradingResult 是判分结果 DTO（与 API 文档第 5 章一致）。
type GradingResult struct {
	ID           string   `json:"id"`
	SubmissionID string   `json:"submission_id"`
	Status       string   `json:"status"`
	Score        *float64 `json:"score"`
	MaxScore     float64  `json:"max_score"`
	Method       string   `json:"method"`
	Confidence   *float64 `json:"confidence"`
	RuleVersion  *string  `json:"rule_version"`
	Reason       string   `json:"reason"`
	NeedsReview  bool     `json:"needs_review"`
}

// ResultQuestion 是结果页每题详情。
type ResultQuestion struct {
	OrderNo           int              `json:"order_no"`
	QuestionID        string           `json:"question_id"`
	QuestionVersionID string           `json:"question_version_id"`
	Type              string           `json:"type"`
	Payload           json.RawMessage  `json:"payload"` // 提交后含答案与解析
	Submission        *SubmissionDraft `json:"submission"`
	Grading           *GradingResult   `json:"grading"`
	IsWrong           bool             `json:"is_wrong"`
	Skipped           bool             `json:"skipped"`
}

// WrongAnswerItem 是结果中的错题条目。
type WrongAnswerItem struct {
	ID                string `json:"id"`
	QuestionVersionID string `json:"question_version_id"`
	Cause             string `json:"cause"`
	Status            string `json:"status"`
}

// ReviewAction 是结果中的复习动作。
type ReviewAction struct {
	ReviewCardID  string `json:"review_card_id"`
	WrongAnswerID string `json:"wrong_answer_id"`
	DueAt         string `json:"due_at"`
}

// PracticeResult 是练习结果。
type PracticeResult struct {
	SessionID     string             `json:"session_id"`
	Status        string             `json:"status"`
	TotalScore    float64            `json:"total_score"`
	MaxScore      float64            `json:"max_score"`
	Questions     []*ResultQuestion  `json:"questions"`
	WrongAnswers  []*WrongAnswerItem `json:"wrong_answers"`
	ReviewActions []*ReviewAction    `json:"review_actions"`
}

// PracticeService 实现练习会话、判分与结果用例。
type PracticeService struct{ s *Services }

// PracticeStartReq 开始练习请求。
type PracticeStartReq struct {
	WorkspaceID    string   `json:"workspace_id"`
	UserID         string   `json:"user_id"`
	Mode           string   `json:"mode"` // practice | review | exam
	QuestionIDs    []string `json:"question_ids"`
	TimeLimitSec   *int     `json:"time_limit_sec"`
	IdempotencyKey string   `json:"idempotency_key"`
}

// PracticeStart 创建练习会话并固定题目版本快照。
func (p *PracticeService) PracticeStart(ctx context.Context, req PracticeStartReq) (*PracticeSession, error) {
	if err := p.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if err := p.s.assertUserActive(ctx, req.UserID); err != nil {
		return nil, err
	}
	// 家长端只读：家长角色不能发起练习（4.21 家长视图只读）。
	if err := p.s.assertNotParent(ctx, req.WorkspaceID, req.UserID, "practice.start"); err != nil {
		return nil, err
	}
	// 家长每日时长限额（4.21 G3：超限 → QUOTA_EXCEEDED）。
	if err := p.s.enforceParentDailyLimit(ctx, req.UserID); err != nil {
		return nil, err
	}
	if req.Mode == "" {
		req.Mode = "practice"
	}
	if req.Mode != "practice" && req.Mode != "review" && req.Mode != "exam" {
		return nil, domain.InvalidArg("mode 仅允许 practice/review/exam")
	}
	if len(req.QuestionIDs) == 0 {
		return nil, domain.InvalidArg("question_ids 不能为空")
	}
	if len(req.QuestionIDs) > 200 {
		return nil, domain.InvalidArg("单次练习题目不能超过 200 道")
	}

	return withIdempotency(p.s, ctx, req.WorkspaceID, req.IdempotencyKey, "PracticeStart", func() (*PracticeSession, error) {
		// 校验题目存在且已发布，固定版本快照。
		snapshot := make([]*PracticeQuestion, 0, len(req.QuestionIDs))
		seen := map[string]bool{}
		for i, qid := range req.QuestionIDs {
			if seen[qid] {
				continue
			}
			seen[qid] = true
			q, err := p.s.Repo.GetQuestion(ctx, req.WorkspaceID, qid)
			if err != nil {
				return nil, err
			}
			if q == nil {
				return nil, domain.NotFound("题目不存在或已被删除")
			}
			if q.Status != "published" {
				return nil, domain.InvalidState("题目 %s 尚未发布，不能练习", qid)
			}
			if q.CurrentVersionID == nil {
				return nil, domain.InvalidState("题目 %s 没有可用版本", qid)
			}
			v, err := p.s.Repo.GetQuestionVersion(ctx, *q.CurrentVersionID)
			if err != nil {
				return nil, err
			}
			if v == nil {
				return nil, domain.NotFound("题目版本不存在")
			}
			payload, err := parseQuestionPayload(v.Payload)
			if err != nil {
				return nil, err
			}
			snapshot = append(snapshot, &PracticeQuestion{
				OrderNo: i + 1, QuestionID: qid, QuestionVersionID: v.ID,
				Type: q.Type, MaxScore: gradingMaxScore(payload, 10),
			})
		}
		now := Now()
		session := &repository.PracticeSessionRow{
			ID: NewID(), WorkspaceID: req.WorkspaceID, UserID: req.UserID,
			Mode: req.Mode, QuestionSnapshot: mustJSON(snapshot),
			TimeLimitSec: req.TimeLimitSec, StartedAt: &now,
		}
		if err := p.s.Repo.CreateSession(ctx, session); err != nil {
			return nil, err
		}
		if err := p.s.Repo.MarkSessionStarted(ctx, session.ID); err != nil {
			return nil, err
		}
		p.s.audit(ctx, req.WorkspaceID, "practice.start", "practice_session", session.ID,
			map[string]any{"mode": req.Mode, "questions": len(snapshot)})
		return p.sessionByID(ctx, req.WorkspaceID, session.ID)
	})
}

// PracticeGetReq 获取会话请求。
type PracticeGetReq struct {
	WorkspaceID string `json:"workspace_id"`
	SessionID   string `json:"session_id"`
}

// PracticeGet 获取会话（答题中不暴露标准答案）。
func (p *PracticeService) PracticeGet(ctx context.Context, req PracticeGetReq) (*PracticeSession, error) {
	if err := p.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	return p.sessionByID(ctx, req.WorkspaceID, req.SessionID)
}

// PracticeSaveAnswerReq 保存草稿请求。
type PracticeSaveAnswerReq struct {
	WorkspaceID       string          `json:"workspace_id"`
	SessionID         string          `json:"session_id"`
	QuestionVersionID string          `json:"question_version_id"`
	Answer            json.RawMessage `json:"answer"`
	ClientSequence    int             `json:"client_sequence"`
	IdempotencyKey    string          `json:"idempotency_key"`
}

// PracticeSaveAnswer 保存答案草稿（客户端序号单调）。
func (p *PracticeService) PracticeSaveAnswer(ctx context.Context, req PracticeSaveAnswerReq) (*SubmissionDraft, error) {
	if err := p.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	session, err := p.s.Repo.GetSession(ctx, req.WorkspaceID, req.SessionID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, domain.NotFound("练习会话不存在")
	}
	if err := p.s.assertUserActive(ctx, session.UserID); err != nil {
		return nil, err
	}
	if session.Status != "answering" {
		return nil, domain.InvalidState("会话状态 %s 不允许保存答案", session.Status)
	}
	if len(req.Answer) == 0 || !json.Valid(req.Answer) {
		return nil, domain.InvalidArg("answer 必填且为合法 JSON")
	}
	// 校验题目属于该会话
	questions := p.snapshotQuestions(session)
	found := false
	for _, q := range questions {
		if q.QuestionVersionID == req.QuestionVersionID {
			found = true
			break
		}
	}
	if !found {
		return nil, domain.InvalidArg("题目不属于该会话")
	}
	// 显式幂等键防重放；无键时依赖客户端序号单调保护。
	var draft *SubmissionDraft
	var saveErr error
	if req.IdempotencyKey == "" {
		draft, saveErr = p.upsertDraft(ctx, req)
	} else {
		draft, saveErr = withIdempotency(p.s, ctx, req.WorkspaceID, req.IdempotencyKey, "PracticeSaveAnswer", func() (*SubmissionDraft, error) {
			return p.upsertDraft(ctx, req)
		})
	}
	if saveErr != nil {
		return nil, saveErr
	}
	// 考试会话：保存成功后惰性检查截止时间，到期自动提交并终止答题。
	if session.Mode == "exam" {
		if err := p.examExpiryGuard(ctx, req.WorkspaceID, req.SessionID); err != nil {
			return nil, err
		}
	}
	return draft, nil
}

// examExpiryGuard 考试到期钩子：保存/跳过成功落库后触发惰性自动提交。
func (p *PracticeService) examExpiryGuard(ctx context.Context, wsID, sessionID string) error {
	if p.s.Exam == nil {
		return nil
	}
	if _, finalized, err := p.s.Exam.maybeAutoSubmit(ctx, wsID, sessionID, p.s.Exam.Now()); err != nil {
		return err
	} else if finalized {
		return domain.InvalidState("考试已到截止时间自动提交，请查看考试结果")
	}
	return nil
}

// upsertDraft 执行草稿写入（序号单调校验）。
func (p *PracticeService) upsertDraft(ctx context.Context, req PracticeSaveAnswerReq) (*SubmissionDraft, error) {
	ok, err := p.s.Repo.UpsertDraft(ctx, &repository.SubmissionRow{
		ID: NewID(), SessionID: req.SessionID, QuestionVersionID: req.QuestionVersionID,
		Answer: req.Answer, ClientSequence: req.ClientSequence, Status: "draft",
	})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, domain.Conflict("收到过期序号（%d），已忽略", req.ClientSequence)
	}
	subs, err := p.s.Repo.ListSubmissions(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}
	for _, sub := range subs {
		if sub.QuestionVersionID == req.QuestionVersionID {
			return &SubmissionDraft{
				ID: sub.ID, SessionID: sub.SessionID, QuestionVersionID: sub.QuestionVersionID,
				Answer: sub.Answer, ClientSequence: sub.ClientSequence,
				Status: sub.Status, UpdatedAt: sub.UpdatedAt,
			}, nil
		}
	}
	return nil, domain.WrapError(domain.CodeInternal, "草稿保存后未找到", nil)
}

// PracticeSkipQuestionReq 跳过题目请求。
type PracticeSkipQuestionReq struct {
	WorkspaceID       string `json:"workspace_id"`
	SessionID         string `json:"session_id"`
	QuestionVersionID string `json:"question_version_id"`
}

// PracticeSkipQuestion 记录跳过（写入 skipped_json）。
func (p *PracticeService) PracticeSkipQuestion(ctx context.Context, req PracticeSkipQuestionReq) (*PracticeSession, error) {
	if err := p.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	session, err := p.s.Repo.GetSession(ctx, req.WorkspaceID, req.SessionID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, domain.NotFound("练习会话不存在")
	}
	if err := p.s.assertUserActive(ctx, session.UserID); err != nil {
		return nil, err
	}
	if session.Status != "answering" {
		return nil, domain.InvalidState("会话状态 %s 不允许跳过", session.Status)
	}
	var skipped []string
	_ = json.Unmarshal(session.Skipped, &skipped)
	for _, s := range skipped {
		if s == req.QuestionVersionID {
			return p.sessionByID(ctx, req.WorkspaceID, req.SessionID)
		}
	}
	skipped = append(skipped, req.QuestionVersionID)
	if _, err := p.s.Repo.DB().ExecContext(ctx, `
		UPDATE practice_sessions SET skipped_json = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ? AND status = 'answering'`,
		string(mustJSON(skipped)), req.SessionID); err != nil {
		return nil, dbErr(err)
	}
	// 考试会话：跳过成功后惰性检查截止时间，到期自动提交并终止答题。
	if session.Mode == "exam" {
		if err := p.examExpiryGuard(ctx, req.WorkspaceID, req.SessionID); err != nil {
			return nil, err
		}
	}
	return p.sessionByID(ctx, req.WorkspaceID, req.SessionID)
}

// PracticeSubmitReq 提交练习请求。
type PracticeSubmitReq struct {
	WorkspaceID    string `json:"workspace_id"`
	SessionID      string `json:"session_id"`
	Version        int    `json:"version"`
	IdempotencyKey string `json:"idempotency_key"`
}

// PracticeSubmit 提交并判分：客观题同步规则判分，主观题进入 AI 评分（未配置则 failed 降级）。
func (p *PracticeService) PracticeSubmit(ctx context.Context, req PracticeSubmitReq) (*PracticeResult, error) {
	if err := p.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.IdempotencyKey == "" {
		return nil, domain.InvalidArg("idempotency_key 必填")
	}
	// 考试会话禁止经练习提交入口手动提交：考试仅由 ExamAutoSubmit 到期自动判分。
	session, err := p.s.Repo.GetSession(ctx, req.WorkspaceID, req.SessionID)
	if err != nil {
		return nil, err
	}
	if session != nil && session.Mode == "exam" {
		return nil, domain.InvalidState("考试会话不支持手动提交，请使用 ExamAutoSubmit")
	}
	return withIdempotency(p.s, ctx, req.WorkspaceID, req.IdempotencyKey, "PracticeSubmit", func() (*PracticeResult, error) {
		return p.doSubmit(ctx, req.WorkspaceID, req.SessionID, req.Version)
	})
}

// doSubmit 实际提交逻辑（事务内）。
func (p *PracticeService) doSubmit(ctx context.Context, wsID, sessionID string, version int) (*PracticeResult, error) {
	session, err := p.s.Repo.GetSession(ctx, wsID, sessionID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, domain.NotFound("练习会话不存在")
	}
	if err := p.s.assertUserActive(ctx, session.UserID); err != nil {
		return nil, err
	}
	if session.Status != "answering" {
		return nil, domain.InvalidState("会话状态 %s 不允许提交", session.Status)
	}
	questions := p.snapshotQuestions(session)
	var skipped []string
	_ = json.Unmarshal(session.Skipped, &skipped)
	skippedSet := map[string]bool{}
	for _, s := range skipped {
		skippedSet[s] = true
	}
	subs, err := p.s.Repo.ListSubmissions(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	subByQV := map[string]*repository.SubmissionRow{}
	for _, sub := range subs {
		subByQV[sub.QuestionVersionID] = sub
	}

	totalScore := 0.0
	maxTotal := 0.0
	result := &PracticeResult{SessionID: sessionID, Status: "graded"}

	for _, q := range questions {
		maxTotal += q.MaxScore
		sub := subByQV[q.QuestionVersionID]
		skippedQ := skippedSet[q.QuestionVersionID]

		rq := &ResultQuestion{
			OrderNo: q.OrderNo, QuestionID: q.QuestionID,
			QuestionVersionID: q.QuestionVersionID, Type: q.Type, Skipped: skippedQ,
		}
		// 未作答（含跳过）→ 空提交，0 分（确定性 ID：重试重建幂等）
		if sub == nil {
			subID := stableID("sub", sessionID, q.QuestionVersionID)
			if existing, err := p.s.Repo.GetSubmission(ctx, subID); err == nil && existing != nil {
				sub = existing
			} else {
				sub = &repository.SubmissionRow{
					ID: subID, SessionID: sessionID, QuestionVersionID: q.QuestionVersionID,
					Answer: json.RawMessage("{}"), Status: "submitted",
					SubmittedAt: strPtr(Now()),
				}
				if _, err := p.s.Repo.DB().ExecContext(ctx, `
					INSERT INTO submissions (id, session_id, question_version_id, attempt_no, answer_json, status, submitted_at)
					VALUES (?, ?, ?, 1, '{}', 'submitted', ?)
					ON CONFLICT(session_id, question_version_id, attempt_no) DO NOTHING`,
					sub.ID, sessionID, q.QuestionVersionID, Now()); err != nil {
					return nil, dbErr(err)
				}
			}
		} else {
			if err := p.s.Repo.MarkSubmissionSubmitted(ctx, sub.ID); err != nil {
				return nil, err
			}
		}
		rq.Submission = &SubmissionDraft{
			ID: sub.ID, SessionID: sub.SessionID, QuestionVersionID: sub.QuestionVersionID,
			Answer: sub.Answer, ClientSequence: sub.ClientSequence, Status: "submitted",
		}

		// 判分
		grading, err := p.gradeSubmission(ctx, wsID, q, sub)
		if err != nil {
			return nil, err
		}
		rq.Grading = grading
		score := 0.0
		if grading.Score != nil {
			score = *grading.Score
		}
		totalScore += score
		rq.IsWrong = score < q.MaxScore

		// 错题归档 + 复习卡（跳过/未答也视为未掌握；确定性 ID 防重复归档）
		if rq.IsWrong {
			wrongID := stableID("wrong", sub.ID)
			if existing, err := p.s.Repo.GetWrongAnswer(ctx, wsID, wrongID); err == nil && existing != nil {
				result.WrongAnswers = append(result.WrongAnswers, &WrongAnswerItem{
					ID: existing.ID, QuestionVersionID: q.QuestionVersionID,
					Cause: existing.Cause, Status: existing.Status,
				})
				if existing.LatestGradingID != nil {
					if g, err := p.s.Repo.GetGrading(ctx, *existing.LatestGradingID); err == nil && g != nil {
						rq.Grading = gradingFromRow(g)
						score := 0.0
						if g.Score != nil {
							score = *g.Score
						}
						rq.IsWrong = score < q.MaxScore
					}
				}
			} else {
				if err := p.s.Repo.CreateWrongAnswer(ctx, &repository.WrongAnswerRow{
					ID: wrongID, WorkspaceID: wsID, UserID: session.UserID,
					SubmissionID: sub.ID, QuestionVersionID: q.QuestionVersionID,
					Answer: sub.Answer, LatestGradingID: &grading.ID,
				}); err != nil {
					return nil, err
				}
				cardID := stableID("card", sub.ID)
				if existingCard, err := p.s.Repo.GetReviewCard(ctx, wsID, cardID); err == nil && existingCard != nil {
					result.ReviewActions = append(result.ReviewActions, &ReviewAction{
						ReviewCardID: existingCard.ID, WrongAnswerID: wrongID, DueAt: existingCard.DueAt,
					})
				} else {
					due := Now()
					if err := p.s.Repo.CreateReviewCard(ctx, &repository.ReviewCardRow{
						ID: cardID, WorkspaceID: wsID, UserID: session.UserID,
						WrongAnswerID: wrongID, Repetition: 0, IntervalDays: 0,
						EaseFactor: 2.5, DueAt: due,
					}); err != nil {
						return nil, err
					}
					result.ReviewActions = append(result.ReviewActions, &ReviewAction{
						ReviewCardID: cardID, WrongAnswerID: wrongID, DueAt: due,
					})
				}
				result.WrongAnswers = append(result.WrongAnswers, &WrongAnswerItem{
					ID: wrongID, QuestionVersionID: q.QuestionVersionID, Cause: "unknown", Status: "active",
				})
			}
		}
		// 解析可见（提交后包含答案与解析）
		payload, _ := p.s.Repo.GetQuestionVersion(ctx, q.QuestionVersionID)
		if payload != nil {
			rq.Payload = payload.Payload
		}
		result.Questions = append(result.Questions, rq)
	}

	// 会话状态推进
	if err := p.s.Repo.SubmitSession(ctx, sessionID, session.Version); err != nil {
		return nil, err
	}
	if err := p.s.Repo.MarkSessionGraded(ctx, sessionID); err != nil {
		return nil, err
	}
	result.TotalScore = totalScore
	result.MaxScore = maxTotal
	p.s.audit(ctx, wsID, "practice.submit", "practice_session", sessionID,
		map[string]any{"score": totalScore, "max": maxTotal})
	return result, nil
}

// gradeSubmission 单题判分：客观题规则引擎；主观题 AI（未配置 → failed 降级）。
// 同一提交已存在判分记录时直接复用（提交重试幂等）。
func (p *PracticeService) gradeSubmission(ctx context.Context, wsID string, q *PracticeQuestion, sub *repository.SubmissionRow) (*GradingResult, error) {
	if existing, err := p.s.Repo.GetGradingBySubmission(ctx, sub.ID); err != nil {
		return nil, err
	} else if existing != nil {
		return gradingFromRow(existing), nil
	}
	version, err := p.s.Repo.GetQuestionVersion(ctx, q.QuestionVersionID)
	if err != nil {
		return nil, err
	}
	if version == nil {
		return nil, domain.NotFound("题目版本不存在")
	}
	payload, err := parseQuestionPayload(version.Payload)
	if err != nil {
		return nil, err
	}
	switch payload.Type {
	case "single_choice", "multiple_choice", "fill_blank":
		gr, err := GradeObjective(payload, sub.Answer, q.MaxScore)
		if err != nil {
			// 答案格式非法（如未作答 {}）→ 0 分
			score := 0.0
			ruleVer := "rule-v1"
			grading := &repository.GradingRow{
				ID: NewID(), SubmissionID: sub.ID, Status: "completed",
				Score: &score, MaxScore: q.MaxScore, Method: "rule",
				RuleVersion: &ruleVer, Matched: mustJSON([]string{"答案格式非法或未作答"}),
				Reason: "答案格式非法或未作答",
			}
			if err := p.s.Repo.CreateGrading(ctx, grading); err != nil {
				return nil, err
			}
			return p.gradingByID(ctx, grading.ID)
		}
		ruleVer := "rule-v1"
		grading := &repository.GradingRow{
			ID: NewID(), SubmissionID: sub.ID, Status: "completed",
			Score: &gr.Score, MaxScore: gr.MaxScore, Method: "rule",
			RuleVersion: &ruleVer, Matched: mustJSON(gr.Matched), Reason: gr.Reason,
		}
		if err := p.s.Repo.CreateGrading(ctx, grading); err != nil {
			return nil, err
		}
		return p.gradingByID(ctx, grading.ID)
	default:
		// 简答/代码：AI 评分（P2 Agent 接入）；未配置 Provider → failed 降级。
		method := "rubric_llm"
		if payload.Type == "code" {
			method = "code_runner"
		}
		grading := &repository.GradingRow{
			ID: NewID(), SubmissionID: sub.ID, Status: "pending",
			MaxScore: q.MaxScore, Method: method,
		}
		if err := p.s.Repo.CreateGrading(ctx, grading); err != nil {
			return nil, err
		}
		if p.s.Grader == nil {
			reason := "未配置 AI 评分服务，请查看标准解析或配置 Provider 后重新评分"
			if err := p.s.Repo.UpdateGrading(ctx, grading.ID, "failed", nil, nil, reason, false); err != nil {
				return nil, err
			}
		} else {
			go p.s.Grader.Grade(ctx, wsID, grading.ID)
		}
		return p.gradingByID(ctx, grading.ID)
	}
}

// PracticeGetResultReq 获取结果请求。
type PracticeGetResultReq struct {
	WorkspaceID string `json:"workspace_id"`
	SessionID   string `json:"session_id"`
}

// PracticeGetResult 获取练习结果。
func (p *PracticeService) PracticeGetResult(ctx context.Context, req PracticeGetResultReq) (*PracticeResult, error) {
	if err := p.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	session, err := p.s.Repo.GetSession(ctx, req.WorkspaceID, req.SessionID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, domain.NotFound("练习会话不存在")
	}
	if session.Status != "graded" && session.Status != "submitted" {
		return nil, domain.InvalidState("会话尚未提交")
	}
	questions := p.snapshotQuestions(session)
	var skipped []string
	_ = json.Unmarshal(session.Skipped, &skipped)
	skippedSet := map[string]bool{}
	for _, s := range skipped {
		skippedSet[s] = true
	}
	subs, err := p.s.Repo.ListSubmissions(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}
	result := &PracticeResult{SessionID: req.SessionID, Status: session.Status}
	total := 0.0
	maxTotal := 0.0
	for _, q := range questions {
		maxTotal += q.MaxScore
		rq := &ResultQuestion{
			OrderNo: q.OrderNo, QuestionID: q.QuestionID,
			QuestionVersionID: q.QuestionVersionID, Type: q.Type, Skipped: skippedSet[q.QuestionVersionID],
		}
		payload, _ := p.s.Repo.GetQuestionVersion(ctx, q.QuestionVersionID)
		if payload != nil {
			rq.Payload = payload.Payload
		}
		for _, sub := range subs {
			if sub.QuestionVersionID == q.QuestionVersionID {
				rq.Submission = &SubmissionDraft{
					ID: sub.ID, SessionID: sub.SessionID, QuestionVersionID: sub.QuestionVersionID,
					Answer: sub.Answer, ClientSequence: sub.ClientSequence, Status: sub.Status,
					UpdatedAt: sub.UpdatedAt,
				}
				g, err := p.s.Repo.GetGradingBySubmission(ctx, sub.ID)
				if err != nil {
					return nil, err
				}
				if g != nil {
					rq.Grading = gradingFromRow(g)
					if g.Score != nil {
						total += *g.Score
						rq.IsWrong = *g.Score < q.MaxScore
					}
				}
				break
			}
		}
		result.Questions = append(result.Questions, rq)
	}
	result.TotalScore = total
	result.MaxScore = maxTotal
	return result, nil
}

// GradingGetReq 获取判分请求。
type GradingGetReq struct {
	WorkspaceID string `json:"workspace_id"`
	GradingID   string `json:"grading_id"`
}

// GradingGet 获取判分详情。
func (p *PracticeService) GradingGet(ctx context.Context, req GradingGetReq) (*GradingResult, error) {
	if err := p.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	g, err := p.s.Repo.GetGrading(ctx, req.GradingID)
	if err != nil {
		return nil, err
	}
	if g == nil {
		return nil, domain.NotFound("判分记录不存在")
	}
	// 归属校验：通过 submission → session → workspace
	sub, err := p.s.Repo.GetSubmission(ctx, g.SubmissionID)
	if err != nil {
		return nil, err
	}
	if sub == nil || !p.submissionInWorkspace(ctx, sub.SessionID, req.WorkspaceID) {
		return nil, domain.NotFound("判分记录不存在")
	}
	return gradingFromRow(g), nil
}

// GradingRequestReviewReq 申请复核请求。
type GradingRequestReviewReq struct {
	WorkspaceID    string `json:"workspace_id"`
	GradingID      string `json:"grading_id"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotency_key"`
}

// GradingRequestReview 申请人工复核。
func (p *PracticeService) GradingRequestReview(ctx context.Context, req GradingRequestReviewReq) (*GradingResult, error) {
	if err := p.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.Reason == "" {
		return nil, domain.InvalidArg("reason 必填")
	}
	g, err := p.s.Repo.GetGrading(ctx, req.GradingID)
	if err != nil {
		return nil, err
	}
	if g == nil {
		return nil, domain.NotFound("判分记录不存在")
	}
	// 归属校验：通过 submission → session → workspace，并校验用户活跃
	sub, err := p.s.Repo.GetSubmission(ctx, g.SubmissionID)
	if err != nil {
		return nil, err
	}
	if sub == nil || !p.submissionInWorkspace(ctx, sub.SessionID, req.WorkspaceID) {
		return nil, domain.NotFound("判分记录不存在")
	}
	if sess, err := p.s.Repo.GetSession(ctx, req.WorkspaceID, sub.SessionID); err == nil && sess != nil {
		if err := p.s.assertUserActive(ctx, sess.UserID); err != nil {
			return nil, err
		}
	}
	if err := p.s.Repo.UpdateGradingReview(ctx, req.GradingID, req.Reason); err != nil {
		return nil, err
	}
	row, err := p.s.Repo.GetGrading(ctx, req.GradingID)
	if err != nil {
		return nil, err
	}
	return gradingFromRow(row), nil
}

// sessionByID 组装会话 DTO（答题中隐藏答案）。
func (p *PracticeService) sessionByID(ctx context.Context, wsID, id string) (*PracticeSession, error) {
	session, err := p.s.Repo.GetSession(ctx, wsID, id)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, domain.NotFound("练习会话不存在")
	}
	out := &PracticeSession{
		ID: session.ID, WorkspaceID: session.WorkspaceID, UserID: session.UserID,
		Mode: session.Mode, Status: session.Status,
		TimeLimitSec: session.TimeLimitSec, StartedAt: session.StartedAt,
		SubmittedAt: session.SubmittedAt, Version: session.Version,
		CreatedAt: session.CreatedAt, UpdatedAt: session.UpdatedAt,
	}
	_ = json.Unmarshal(session.Skipped, &out.Skipped)
	// 答题中返回题干（脱敏：不含标准答案与解析）。
	answering := session.Status == "answering" || session.Status == "created"
	for _, q := range p.snapshotQuestions(session) {
		if answering {
			if v, err := p.s.Repo.GetQuestionVersion(ctx, q.QuestionVersionID); err == nil && v != nil {
				if pl, err := parseQuestionPayload(v.Payload); err == nil {
					pl.Answer = json.RawMessage("")
					pl.Analysis = ""
					q.Payload = mustJSON(pl)
				}
			}
		}
		out.Questions = append(out.Questions, q)
	}
	if session.Status == "answering" || session.Status == "created" {
		subs, err := p.s.Repo.ListSubmissions(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, sub := range subs {
			out.Drafts = append(out.Drafts, &SubmissionDraft{
				ID: sub.ID, SessionID: sub.SessionID, QuestionVersionID: sub.QuestionVersionID,
				Answer: sub.Answer, ClientSequence: sub.ClientSequence,
				Status: sub.Status, UpdatedAt: sub.UpdatedAt,
			})
		}
	}
	return out, nil
}

// snapshotQuestions 解析会话快照。
func (p *PracticeService) snapshotQuestions(session *repository.PracticeSessionRow) []*PracticeQuestion {
	var questions []*PracticeQuestion
	_ = json.Unmarshal(session.QuestionSnapshot, &questions)
	return questions
}

func (p *PracticeService) gradingByID(ctx context.Context, id string) (*GradingResult, error) {
	g, err := p.s.Repo.GetGrading(ctx, id)
	if err != nil {
		return nil, err
	}
	if g == nil {
		return nil, domain.NotFound("判分记录不存在")
	}
	return gradingFromRow(g), nil
}

func gradingFromRow(g *repository.GradingRow) *GradingResult {
	return &GradingResult{
		ID: g.ID, SubmissionID: g.SubmissionID, Status: g.Status,
		Score: g.Score, MaxScore: g.MaxScore, Method: g.Method,
		Confidence: g.Confidence, RuleVersion: g.RuleVersion,
		Reason: g.Reason, NeedsReview: g.NeedsReview,
	}
}

func (p *PracticeService) submissionInWorkspace(ctx context.Context, sessionID, wsID string) bool {
	s, err := p.s.Repo.GetSession(ctx, wsID, sessionID)
	return err == nil && s != nil
}

func strPtr(s string) *string { return &s }
