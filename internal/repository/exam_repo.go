package repository

import (
	"context"
	"database/sql"

	"lumo/internal/domain"
)

// ExamPaperRow 是 exam_papers 表行。
type ExamPaperRow struct {
	ID          string
	WorkspaceID string
	UserID      string
	Title       string
	ConfigJSON  string
	Status      string
	Version     int
	CreatedAt   string
	UpdatedAt   string
}

// ExamPaperSectionRow 是 exam_paper_sections 表行。
type ExamPaperSectionRow struct {
	ID                string
	PaperID           string
	Title             string
	OrderNo           int
	QuestionVersionIDs string
	Score             int
	CreatedAt         string
}

// ExamRow 是 exams 表行。
type ExamRow struct {
	ID                 string
	PaperID            string
	UserID             string
	Status             string
	StartedAt          *string
	EndedAt            *string
	ScoreSummaryJSON   string
	SuspiciousEventsJSON string
	CreatedAt          string
	UpdatedAt          string
}

const examPaperCols = `id, workspace_id, user_id, title, config_json, status, version, created_at, updated_at`

func scanExamPaper(row interface{ Scan(...any) error }) (*ExamPaperRow, error) {
	var p ExamPaperRow
	if err := row.Scan(&p.ID, &p.WorkspaceID, &p.UserID, &p.Title, &p.ConfigJSON,
		&p.Status, &p.Version, &p.CreatedAt, &p.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	return &p, nil
}

const examPaperSectionCols = `id, paper_id, title, order_no, question_version_ids, score, created_at`

func scanExamPaperSection(row interface{ Scan(...any) error }) (*ExamPaperSectionRow, error) {
	var s ExamPaperSectionRow
	if err := row.Scan(&s.ID, &s.PaperID, &s.Title, &s.OrderNo, &s.QuestionVersionIDs, &s.Score, &s.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	return &s, nil
}

const examCols = `id, paper_id, user_id, status, started_at, ended_at, score_summary_json, suspicious_events_json, created_at, updated_at`

func scanExam(row interface{ Scan(...any) error }) (*ExamRow, error) {
	var e ExamRow
	if err := row.Scan(&e.ID, &e.PaperID, &e.UserID, &e.Status, &e.StartedAt, &e.EndedAt,
		&e.ScoreSummaryJSON, &e.SuspiciousEventsJSON, &e.CreatedAt, &e.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	return &e, nil
}

// CreateExamPaperTx 事务创建试卷及其大题。
func (r *Repo) CreateExamPaperTx(ctx context.Context, p *ExamPaperRow, sections []*ExamPaperSectionRow) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return normalizeErr(err)
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO exam_papers (id, workspace_id, user_id, title, config_json, status, version)
		VALUES (?, ?, ?, ?, ?, 'draft', 1)`,
		p.ID, p.WorkspaceID, p.UserID, p.Title, p.ConfigJSON)
	if err != nil {
		return normalizeErr(err)
	}
	for _, s := range sections {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO exam_paper_sections (id, paper_id, title, order_no, question_version_ids, score)
			VALUES (?, ?, ?, ?, ?, ?)`,
			s.ID, s.PaperID, s.Title, s.OrderNo, s.QuestionVersionIDs, s.Score); err != nil {
			return normalizeErr(err)
		}
	}
	return normalizeErr(tx.Commit())
}

// GetExamPaper 获取试卷（按工作区隔离）。
func (r *Repo) GetExamPaper(ctx context.Context, wsID, id string) (*ExamPaperRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+examPaperCols+` FROM exam_papers WHERE id = ? AND workspace_id = ?`, id, wsID)
	return scanExamPaper(row)
}

// ListExamPapers 列出工作区试卷。
func (r *Repo) ListExamPapers(ctx context.Context, wsID string, limit int) ([]*ExamPaperRow, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+examPaperCols+` FROM exam_papers
		WHERE workspace_id = ? ORDER BY created_at DESC LIMIT ?`, wsID, limit)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*ExamPaperRow
	for rows.Next() {
		p, err := scanExamPaper(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetExamPaperSections 获取试卷大题（按 order_no 排序）。
func (r *Repo) GetExamPaperSections(ctx context.Context, paperID string) ([]*ExamPaperSectionRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+examPaperSectionCols+` FROM exam_paper_sections
		WHERE paper_id = ? ORDER BY order_no`, paperID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*ExamPaperSectionRow
	for rows.Next() {
		s, err := scanExamPaperSection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// UpdateExamPaperStatus 乐观锁更新试卷状态（draft→published→archived）。
func (r *Repo) UpdateExamPaperStatus(ctx context.Context, wsID, id string, version int, status string) (*ExamPaperRow, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE exam_papers SET status = ?, version = version + 1,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ? AND workspace_id = ? AND version = ?`, status, id, wsID, version)
	if err != nil {
		return nil, normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		cur, err := r.GetExamPaper(ctx, wsID, id)
		if err != nil {
			return nil, err
		}
		if cur == nil {
			return nil, NotFoundErr("试卷", id)
		}
		return nil, domain.Conflict("试卷已被修改，请刷新后重试")
	}
	return r.GetExamPaper(ctx, wsID, id)
}

// CreateExam 创建考试（与 practice_session 共享 ID，mode='exam'）。
func (r *Repo) CreateExam(ctx context.Context, e *ExamRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO exams (id, paper_id, user_id, status, started_at)
		VALUES (?, ?, ?, ?, ?)`,
		e.ID, e.PaperID, e.UserID, e.Status, e.StartedAt)
	return normalizeErr(err)
}

// GetExam 获取考试（经试卷关联工作区隔离）。
func (r *Repo) GetExam(ctx context.Context, wsID, id string) (*ExamRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT e.id, e.paper_id, e.user_id, e.status, e.started_at, e.ended_at,
			e.score_summary_json, e.suspicious_events_json, e.created_at, e.updated_at
		FROM exams e JOIN exam_papers p ON p.id = e.paper_id
		WHERE e.id = ? AND p.workspace_id = ?`, id, wsID)
	return scanExam(row)
}

// GetInProgressExam 查询用户对某试卷进行中的考试（created/answering 均视为进行中）。
func (r *Repo) GetInProgressExam(ctx context.Context, userID, paperID string) (*ExamRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+examCols+` FROM exams
		WHERE user_id = ? AND paper_id = ? AND status IN ('created', 'answering')
		ORDER BY created_at DESC LIMIT 1`, userID, paperID)
	return scanExam(row)
}

// UpdateExamFinalized 判定分完成：写成绩摘要、结束时间、推进状态。
func (r *Repo) UpdateExamFinalized(ctx context.Context, id, status, endedAt, scoreSummaryJSON string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE exams SET status = ?, ended_at = ?, score_summary_json = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ?`, status, endedAt, scoreSummaryJSON, id)
	if err != nil {
		return normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return domain.InvalidState("考试状态不允许结束")
	}
	return nil
}

// UpdateExamEnded 记录结束时间（自动提交前兜底）。
func (r *Repo) UpdateExamEnded(ctx context.Context, id, endedAt string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE exams SET ended_at = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ?`, endedAt, id)
	return normalizeErr(err)
}
