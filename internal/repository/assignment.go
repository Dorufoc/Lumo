package repository

import (
	"context"
	"database/sql"

	"lumo/internal/domain"
)

// AssignmentRow 是 assignments 表行（0005_student.sql 4.22）。
// 注意：assignments 无 workspace_id 列，工作区隔离经 classes.workspace_id 关联实现。
type AssignmentRow struct {
	ID          string
	ClassID     string
	PaperID     string
	Title       string
	DueAt       string
	GradingRule string
	Status      string
	Version     int
	CreatedAt   string
	UpdatedAt   string
}

const assignmentCols = `a.id, a.class_id, a.paper_id, a.title, a.due_at, a.grading_rule, a.status, a.version, a.created_at, a.updated_at`

func scanAssignment(row interface{ Scan(...any) error }) (*AssignmentRow, error) {
	var a AssignmentRow
	if err := row.Scan(&a.ID, &a.ClassID, &a.PaperID, &a.Title, &a.DueAt,
		&a.GradingRule, &a.Status, &a.Version, &a.CreatedAt, &a.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	return &a, nil
}

// CreateAssignment 创建作业（草稿态，version=1）。
func (r *Repo) CreateAssignment(ctx context.Context, a *AssignmentRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO assignments (id, class_id, paper_id, title, due_at, grading_rule, status, version)
		VALUES (?, ?, ?, ?, ?, ?, 'draft', 1)`,
		a.ID, a.ClassID, a.PaperID, a.Title, a.DueAt, a.GradingRule)
	return normalizeErr(err)
}

// GetAssignment 获取作业（经 classes 关联实现工作区隔离）。
func (r *Repo) GetAssignment(ctx context.Context, wsID, id string) (*AssignmentRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+assignmentCols+` FROM assignments a
		JOIN classes c ON c.id = a.class_id
		WHERE a.id = ? AND c.workspace_id = ?`, id, wsID)
	return scanAssignment(row)
}

// ListAssignmentsForUser 列出用户可见作业：本人创建班级（教师）或为 active 成员班级（学生）。
func (r *Repo) ListAssignmentsForUser(ctx context.Context, wsID, userID string) ([]*AssignmentRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+assignmentCols+` FROM assignments a
		JOIN classes c ON c.id = a.class_id
		WHERE c.workspace_id = ? AND (c.owner_user_id = ? OR a.class_id IN (
			SELECT class_id FROM class_members WHERE student_user_id = ? AND status = 'active'))
		ORDER BY a.created_at DESC`, wsID, userID, userID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*AssignmentRow
	for rows.Next() {
		a, err := scanAssignment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, normalizeErr(rows.Err())
}

// UpdateAssignmentStatus 乐观锁更新作业状态（draft→published→closed）。
func (r *Repo) UpdateAssignmentStatus(ctx context.Context, wsID, id string, version int, status string) (*AssignmentRow, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE assignments SET status = ?, version = version + 1,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ? AND version = ? AND class_id IN (
			SELECT id FROM classes WHERE workspace_id = ?)`,
		status, id, version, wsID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		cur, err := r.GetAssignment(ctx, wsID, id)
		if err != nil {
			return nil, err
		}
		if cur == nil {
			return nil, NotFoundErr("作业", id)
		}
		return nil, domain.Conflict("作业已被修改，请刷新后重试")
	}
	return r.GetAssignment(ctx, wsID, id)
}

// AssignmentSubmissionRow 是 assignment_submissions 表行（0005_student.sql 4.22）。
// submission_id 可空：未提交答案时为空；提交后指向本次作答所在的 submissions 行。
type AssignmentSubmissionRow struct {
	ID            string
	AssignmentID  string
	StudentUserID string
	SubmissionID  *string
	GradeJSON     string
	GradedAt      *string
	CreatedAt     string
}

const assignmentSubmissionCols = `id, assignment_id, student_user_id, submission_id, grade_json, graded_at, created_at`

func scanAssignmentSubmission(row interface{ Scan(...any) error }) (*AssignmentSubmissionRow, error) {
	var s AssignmentSubmissionRow
	if err := row.Scan(&s.ID, &s.AssignmentID, &s.StudentUserID, &s.SubmissionID,
		&s.GradeJSON, &s.GradedAt, &s.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	return &s, nil
}

// CreateAssignmentSubmission 创建作业提交记录（grade_json 空态 '{}'）。
func (r *Repo) CreateAssignmentSubmission(ctx context.Context, s *AssignmentSubmissionRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO assignment_submissions (id, assignment_id, student_user_id, submission_id, grade_json)
		VALUES (?, ?, ?, ?, '{}')`,
		s.ID, s.AssignmentID, s.StudentUserID, s.SubmissionID)
	return normalizeErr(err)
}

// GetAssignmentSubmission 按 作业+学生 查询提交记录（判重/幂等）。
func (r *Repo) GetAssignmentSubmission(ctx context.Context, assignmentID, studentUserID string) (*AssignmentSubmissionRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+assignmentSubmissionCols+` FROM assignment_submissions
		WHERE assignment_id = ? AND student_user_id = ?`, assignmentID, studentUserID)
	return scanAssignmentSubmission(row)
}

// ListAssignmentSubmissions 列出作业全部提交（教师端名单视图，含 graded_at 排序）。
func (r *Repo) ListAssignmentSubmissions(ctx context.Context, assignmentID string) ([]*AssignmentSubmissionRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+assignmentSubmissionCols+` FROM assignment_submissions
		WHERE assignment_id = ? ORDER BY created_at`, assignmentID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*AssignmentSubmissionRow
	for rows.Next() {
		s, err := scanAssignmentSubmission(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, normalizeErr(rows.Err())
}

// CountAssignmentSubmissions 统计作业已提交人数（教师端列表角标）。
func (r *Repo) CountAssignmentSubmissions(ctx context.Context, assignmentID string) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM assignment_submissions
		WHERE assignment_id = ?`, assignmentID).Scan(&n); err != nil {
		return 0, normalizeErr(err)
	}
	return n, nil
}
