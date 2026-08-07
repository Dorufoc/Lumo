package repository

import (
	"context"
	"database/sql"

	"lumo/internal/domain"
)

// AppealRow 对应 grading_appeals 行（0005_student.sql 4.22 C7）。
type AppealRow struct {
	ID            string
	GradingID     string
	StudentUserID string
	Reason        string
	Status        string
	TeacherNote   string
	CreatedAt     string
	UpdatedAt     string
}

const appealCols = `id, grading_id, student_user_id, reason, status, teacher_note, created_at, updated_at`

func scanAppeal(row interface{ Scan(...any) error }) (*AppealRow, error) {
	var a AppealRow
	if err := row.Scan(&a.ID, &a.GradingID, &a.StudentUserID, &a.Reason,
		&a.Status, &a.TeacherNote, &a.CreatedAt, &a.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	return &a, nil
}

// CreateAppeal 插入申诉记录（grading_id 锚点行由服务层先行创建，满足 FK）。
func (r *Repo) CreateAppeal(ctx context.Context, a *AppealRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO grading_appeals (id, grading_id, student_user_id, reason, status, teacher_note)
		VALUES (?, ?, ?, ?, ?, ?)`,
		a.ID, a.GradingID, a.StudentUserID, a.Reason, a.Status, a.TeacherNote)
	return normalizeErr(err)
}

// GetAppeal 按主键取申诉。
func (r *Repo) GetAppeal(ctx context.Context, id string) (*AppealRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+appealCols+` FROM grading_appeals WHERE id = ?`, id)
	return scanAppeal(row)
}

// GetAppealByGrading 按 grading_id 取申诉（服务层用于重复申诉检测；无 UNIQUE 约束，取最早一条）。
func (r *Repo) GetAppealByGrading(ctx context.Context, gradingID string) (*AppealRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+appealCols+` FROM grading_appeals WHERE grading_id = ? ORDER BY created_at ASC LIMIT 1`, gradingID)
	return scanAppeal(row)
}

// ListAppealsByAssignment 按作业列出全部申诉（教师复议视图），关联 assignment_submissions 定位作业。
func (r *Repo) ListAppealsByAssignment(ctx context.Context, assignmentID string) ([]*AppealRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT a.id, a.grading_id, a.student_user_id, a.reason, a.status,
			a.teacher_note, a.created_at, a.updated_at
		FROM grading_appeals a
		JOIN assignment_submissions s ON s.id = a.grading_id
		WHERE s.assignment_id = ? ORDER BY a.created_at ASC`, assignmentID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	out := []*AppealRow{}
	for rows.Next() {
		a, err := scanAppeal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, normalizeErr(err)
	}
	return out, nil
}

// UpdateAppealStatus 原子状态迁移：仅当当前状态等于 from 时更新。
// 0 行受影响 → 重查：不存在 → NOT_FOUND，否则 INVALID_STATE（非法迁移）。
func (r *Repo) UpdateAppealStatus(ctx context.Context, id, from, to, teacherNote string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE grading_appeals SET status = ?, teacher_note = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ? AND status = ?`, to, teacherNote, id, from)
	if err != nil {
		return normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		cur, err := r.GetAppeal(ctx, id)
		if err != nil {
			return err
		}
		if cur == nil {
			return NotFoundErr("申诉", id)
		}
		return domain.InvalidState("申诉状态已变更，请刷新后重试")
	}
	return nil
}
