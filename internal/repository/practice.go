package repository

import (
	"context"
	"database/sql"
	"encoding/json"

	"lumo/internal/domain"
)

// PracticeSessionRow 是 practice_sessions 表行。
type PracticeSessionRow struct {
	ID                 string
	WorkspaceID        string
	UserID             string
	Mode               string
	Status             string
	QuestionSnapshot   json.RawMessage
	TimeLimitSec       *int
	Skipped            json.RawMessage
	StartedAt          *string
	SubmittedAt        *string
	CreatedAt          string
	UpdatedAt          string
	Version            int
}

// SubmissionRow 是 submissions 表行。
type SubmissionRow struct {
	ID                 string
	SessionID          string
	QuestionVersionID  string
	AttemptNo          int
	Answer             json.RawMessage
	Status             string
	ClientSequence     int
	SubmittedAt        *string
	CreatedAt          string
	UpdatedAt          string
}

// GradingRow 是 grading_results 表行。
type GradingRow struct {
	ID            string
	SubmissionID  string
	Status        string
	Score         *float64
	MaxScore      float64
	Method        string
	Confidence    *float64
	RuleVersion   *string
	Matched       json.RawMessage
	Reason        string
	NeedsReview   bool
	CreatedAt     string
}

const sessionCols = `id, workspace_id, user_id, mode, status, question_snapshot_json, time_limit_sec,
	skipped_json, started_at, submitted_at, created_at, updated_at, version`

func scanSession(row interface{ Scan(...any) error }) (*PracticeSessionRow, error) {
	var s PracticeSessionRow
	var snapshot, skipped string
	if err := row.Scan(&s.ID, &s.WorkspaceID, &s.UserID, &s.Mode, &s.Status, &snapshot,
		&s.TimeLimitSec, &skipped, &s.StartedAt, &s.SubmittedAt, &s.CreatedAt, &s.UpdatedAt, &s.Version); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	s.QuestionSnapshot = json.RawMessage(snapshot)
	s.Skipped = json.RawMessage(skipped)
	return &s, nil
}

// CreateSession 创建练习会话（status=created）。
func (r *Repo) CreateSession(ctx context.Context, s *PracticeSessionRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO practice_sessions (id, workspace_id, user_id, mode, status, question_snapshot_json,
			time_limit_sec, skipped_json, started_at, version)
		VALUES (?, ?, ?, ?, 'created', ?, ?, '[]', ?, 1)`,
		s.ID, s.WorkspaceID, s.UserID, s.Mode, string(s.QuestionSnapshot), s.TimeLimitSec, s.StartedAt)
	return normalizeErr(err)
}

// GetSession 获取会话。
func (r *Repo) GetSession(ctx context.Context, wsID, id string) (*PracticeSessionRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+sessionCols+` FROM practice_sessions
		WHERE id = ? AND workspace_id = ?`, id, wsID)
	return scanSession(row)
}

// UpdateSessionStatus 乐观锁更新会话状态。
func (r *Repo) UpdateSessionStatus(ctx context.Context, wsID, id string, version int, status string) (*PracticeSessionRow, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE practice_sessions SET status = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), version = version + 1
		WHERE id = ? AND workspace_id = ? AND version = ?`,
		status, id, wsID, version)
	if err != nil {
		return nil, normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		cur, err := r.GetSession(ctx, wsID, id)
		if err != nil {
			return nil, err
		}
		if cur == nil {
			return nil, NotFoundErr("练习会话", id)
		}
		return nil, domain.Conflict("练习会话已被修改，请刷新后重试")
	}
	return r.GetSession(ctx, wsID, id)
}

// MarkSessionStarted 将会话置为 answering 并记录开始时间。
func (r *Repo) MarkSessionStarted(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE practice_sessions SET status = 'answering',
			started_at = COALESCE(started_at, strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ? AND status = 'created'`, id)
	if err != nil {
		return normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return domain.InvalidState("练习会话状态不允许开始答题")
	}
	return nil
}

// SubmitSession 提交会话：更新状态与 submitted_at。
func (r *Repo) SubmitSession(ctx context.Context, id string, version int) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE practice_sessions SET status = 'submitted',
			submitted_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), version = version + 1
		WHERE id = ? AND status = 'answering' AND version = ?`, id, version)
	if err != nil {
		return normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		cur, err := r.GetSession(ctx, "", id)
		if err != nil {
			return err
		}
		if cur != nil && cur.Status == "submitted" {
			return domain.Conflict("练习已提交，请勿重复提交")
		}
		return domain.InvalidState("练习会话状态不允许提交")
	}
	return nil
}

// MarkSessionGraded 标记会话已判分。
func (r *Repo) MarkSessionGraded(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE practice_sessions SET status = 'graded',
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ? AND status = 'submitted'`, id)
	return normalizeErr(err)
}

// UpsertDraft 保存答案草稿（客户端序号单调递增，忽略过期序号）。
func (r *Repo) UpsertDraft(ctx context.Context, d *SubmissionRow) (bool, error) {
	// 已存在且序号更大 → 忽略（返回 false）
	var exists int
	var seq int
	err := r.db.QueryRowContext(ctx, `
		SELECT count(*), COALESCE(MAX(client_sequence), -1) FROM submissions
		WHERE session_id = ? AND question_version_id = ?`, d.SessionID, d.QuestionVersionID).Scan(&exists, &seq)
	if err != nil {
		return false, normalizeErr(err)
	}
	if exists > 0 && d.ClientSequence < seq {
		return false, nil
	}
	if exists > 0 {
		_, err := r.db.ExecContext(ctx, `
			UPDATE submissions SET answer_json = ?, client_sequence = ?, status = 'draft',
				updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE session_id = ? AND question_version_id = ? AND attempt_no = 1`,
			string(d.Answer), d.ClientSequence, d.SessionID, d.QuestionVersionID)
		return true, normalizeErr(err)
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO submissions (id, session_id, question_version_id, attempt_no, answer_json, status, client_sequence)
		VALUES (?, ?, ?, 1, ?, 'draft', ?)`,
		d.ID, d.SessionID, d.QuestionVersionID, string(d.Answer), d.ClientSequence)
	return true, normalizeErr(err)
}

// GetSubmission 获取提交。
func (r *Repo) GetSubmission(ctx context.Context, id string) (*SubmissionRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, session_id, question_version_id, attempt_no, answer_json, status, client_sequence, submitted_at, created_at, updated_at
		FROM submissions WHERE id = ?`, id)
	var s SubmissionRow
	var answer string
	if err := row.Scan(&s.ID, &s.SessionID, &s.QuestionVersionID, &s.AttemptNo, &answer,
		&s.Status, &s.ClientSequence, &s.SubmittedAt, &s.CreatedAt, &s.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	s.Answer = json.RawMessage(answer)
	return &s, nil
}

// ListSubmissions 列出会话提交。
func (r *Repo) ListSubmissions(ctx context.Context, sessionID string) ([]*SubmissionRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, session_id, question_version_id, attempt_no, answer_json, status, client_sequence, submitted_at, created_at, updated_at
		FROM submissions WHERE session_id = ? ORDER BY created_at`, sessionID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*SubmissionRow
	for rows.Next() {
		var s SubmissionRow
		var answer string
		if err := rows.Scan(&s.ID, &s.SessionID, &s.QuestionVersionID, &s.AttemptNo, &answer,
			&s.Status, &s.ClientSequence, &s.SubmittedAt, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, normalizeErr(err)
		}
		s.Answer = json.RawMessage(answer)
		out = append(out, &s)
	}
	return out, rows.Err()
}

// MarkSubmissionSubmitted 标记提交已定稿。
func (r *Repo) MarkSubmissionSubmitted(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE submissions SET status = 'submitted',
			submitted_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ? AND status = 'draft'`, id)
	return normalizeErr(err)
}

// CreateGrading 创建判分记录。
func (r *Repo) CreateGrading(ctx context.Context, g *GradingRow) error {
	matched := g.Matched
	if len(matched) == 0 {
		matched = json.RawMessage("[]")
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO grading_results (id, submission_id, status, score, max_score, method, confidence,
			rule_version, matched_json, reason, needs_review)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		g.ID, g.SubmissionID, g.Status, g.Score, g.MaxScore, g.Method, g.Confidence,
		g.RuleVersion, string(matched), g.Reason, boolToInt(g.NeedsReview))
	return normalizeErr(err)
}

// GetGrading 获取判分记录。
func (r *Repo) GetGrading(ctx context.Context, id string) (*GradingRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, submission_id, status, score, max_score, method, confidence, rule_version,
			matched_json, reason, needs_review, created_at
		FROM grading_results WHERE id = ?`, id)
	var g GradingRow
	var matched string
	var needsReview int
	if err := row.Scan(&g.ID, &g.SubmissionID, &g.Status, &g.Score, &g.MaxScore, &g.Method,
		&g.Confidence, &g.RuleVersion, &matched, &g.Reason, &needsReview, &g.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	g.Matched = json.RawMessage(matched)
	g.NeedsReview = needsReview == 1
	return &g, nil
}

// GetGradingBySubmission 获取提交的最新判分。
func (r *Repo) GetGradingBySubmission(ctx context.Context, submissionID string) (*GradingRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, submission_id, status, score, max_score, method, confidence, rule_version,
			matched_json, reason, needs_review, created_at
		FROM grading_results WHERE submission_id = ? ORDER BY created_at DESC LIMIT 1`, submissionID)
	var g GradingRow
	var matched string
	var needsReview int
	if err := row.Scan(&g.ID, &g.SubmissionID, &g.Status, &g.Score, &g.MaxScore, &g.Method,
		&g.Confidence, &g.RuleVersion, &matched, &g.Reason, &needsReview, &g.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	g.Matched = json.RawMessage(matched)
	g.NeedsReview = needsReview == 1
	return &g, nil
}

// UpdateGrading 更新判分结果（异步评分完成）。
func (r *Repo) UpdateGrading(ctx context.Context, id string, status string, score *float64, confidence *float64, reason string, needsReview bool) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE grading_results SET status = ?, score = ?, confidence = ?, reason = ?, needs_review = ?
		WHERE id = ? AND status IN ('pending', 'failed')`,
		status, score, confidence, reason, boolToInt(needsReview), id)
	return normalizeErr(err)
}

// UpdateGradingReview 申请人工复核。
func (r *Repo) UpdateGradingReview(ctx context.Context, id string, reason string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE grading_results SET needs_review = 1, status = 'needs_review', reason = ?
		WHERE id = ? AND status = 'completed'`, reason, id)
	if err != nil {
		return normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return domain.InvalidState("仅已完成的判分可申请复核")
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
