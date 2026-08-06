package repository

import (
	"context"
	"database/sql"

	"lumo/internal/domain"
)

// ClassRow 是 classes 表行（0005_student.sql 4.22）。
type ClassRow struct {
	ID          string
	WorkspaceID string
	OwnerUserID string
	Name        string
	Subject     string
	Semester    string
	Status      string
	InviteCode  string
	CreatedAt   string
	UpdatedAt   string
}

const classCols = `id, workspace_id, owner_user_id, name, subject, semester, status, invite_code, created_at, updated_at`

func scanClass(row interface{ Scan(...any) error }) (*ClassRow, error) {
	var c ClassRow
	if err := row.Scan(&c.ID, &c.WorkspaceID, &c.OwnerUserID, &c.Name, &c.Subject,
		&c.Semester, &c.Status, &c.InviteCode, &c.CreatedAt, &c.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	return &c, nil
}

// CreateClass 创建班级（owner 为教师用户，状态默认 active）。
func (r *Repo) CreateClass(ctx context.Context, c *ClassRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO classes (id, workspace_id, owner_user_id, name, subject, semester, status, invite_code)
		VALUES (?, ?, ?, ?, ?, ?, 'active', ?)`,
		c.ID, c.WorkspaceID, c.OwnerUserID, c.Name, c.Subject, c.Semester, c.InviteCode)
	return normalizeErr(err)
}

// GetClass 获取班级（按工作区隔离）。
func (r *Repo) GetClass(ctx context.Context, wsID, id string) (*ClassRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+classCols+` FROM classes
		WHERE id = ? AND workspace_id = ?`, id, wsID)
	return scanClass(row)
}

// ListClassesForUser 列出用户可见班级：本人创建（教师）或为 active 成员（学生）。
func (r *Repo) ListClassesForUser(ctx context.Context, wsID, userID string) ([]*ClassRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+classCols+` FROM classes
		WHERE workspace_id = ? AND (owner_user_id = ? OR id IN (
			SELECT class_id FROM class_members WHERE student_user_id = ? AND status = 'active'))
		ORDER BY created_at DESC`, wsID, userID, userID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*ClassRow
	for rows.Next() {
		c, err := scanClass(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, normalizeErr(rows.Err())
}

// UpdateClass 更新班级基础信息（名称/科目/学期），返回更新后的行。
func (r *Repo) UpdateClass(ctx context.Context, wsID, id, name, subject, semester string) (*ClassRow, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE classes SET name = ?, subject = ?, semester = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ? AND workspace_id = ?`,
		name, subject, semester, id, wsID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, NotFoundErr("班级", id)
	}
	return r.GetClass(ctx, wsID, id)
}

// UpdateClassStatus 更新班级状态（active↔archived），返回更新后的行。
func (r *Repo) UpdateClassStatus(ctx context.Context, wsID, id, status string) (*ClassRow, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE classes SET status = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ? AND workspace_id = ?`,
		status, id, wsID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, NotFoundErr("班级", id)
	}
	return r.GetClass(ctx, wsID, id)
}

// RegenerateInviteCode 重置邀请码（ClassInvite 每次调用重新生成）。
func (r *Repo) RegenerateInviteCode(ctx context.Context, wsID, id, code string) (*ClassRow, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE classes SET invite_code = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ? AND workspace_id = ?`,
		code, id, wsID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, NotFoundErr("班级", id)
	}
	return r.GetClass(ctx, wsID, id)
}

// ClassMemberRow 是 class_members 表行（0005_student.sql 4.22）。
type ClassMemberRow struct {
	ID            string
	ClassID       string
	StudentUserID string
	Status        string
	JoinedAt      string
	CreatedAt     string
}

const classMemberCols = `id, class_id, student_user_id, status, joined_at, created_at`

func scanClassMember(row interface{ Scan(...any) error }) (*ClassMemberRow, error) {
	var m ClassMemberRow
	if err := row.Scan(&m.ID, &m.ClassID, &m.StudentUserID, &m.Status, &m.JoinedAt, &m.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	return &m, nil
}

// CreateClassMember 添加班级成员（状态 active）。class_members 无唯一约束，调用方须先查重。
func (r *Repo) CreateClassMember(ctx context.Context, m *ClassMemberRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO class_members (id, class_id, student_user_id, status)
		VALUES (?, ?, ?, 'active')`,
		m.ID, m.ClassID, m.StudentUserID)
	return normalizeErr(err)
}

// GetClassMember 按 班级+学生 查询成员记录（判断 active/removed）。
func (r *Repo) GetClassMember(ctx context.Context, classID, studentUserID string) (*ClassMemberRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+classMemberCols+` FROM class_members
		WHERE class_id = ? AND student_user_id = ?`, classID, studentUserID)
	return scanClassMember(row)
}

// UpdateClassMemberStatus 更新成员状态（active↔removed），返回更新后的行。
func (r *Repo) UpdateClassMemberStatus(ctx context.Context, classID, studentUserID, status string) (*ClassMemberRow, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE class_members SET status = ?
		WHERE class_id = ? AND student_user_id = ?`,
		status, classID, studentUserID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, domain.NotFound("班级成员(%s/%s) 不存在", classID, studentUserID)
	}
	return r.GetClassMember(ctx, classID, studentUserID)
}

// ListClassMembers 列出班级全部成员（含 removed，供教师名单展示）。
func (r *Repo) ListClassMembers(ctx context.Context, classID string) ([]*ClassMemberRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+classMemberCols+` FROM class_members
		WHERE class_id = ? ORDER BY created_at`, classID)
	if err != nil {
		return nil, normalizeErr(err)
	}
	defer rows.Close()
	var out []*ClassMemberRow
	for rows.Next() {
		m, err := scanClassMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, normalizeErr(rows.Err())
}

// CountActiveMembers 统计班级 active 成员数（学生列表/班级视图用）。
func (r *Repo) CountActiveMembers(ctx context.Context, classID string) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM class_members
		WHERE class_id = ? AND status = 'active'`, classID).Scan(&n); err != nil {
		return 0, normalizeErr(err)
	}
	return n, nil
}
