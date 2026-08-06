package service

import (
	"context"
	"crypto/rand"
	"strings"

	"lumo/internal/domain"
	"lumo/internal/repository"
)

// ClassesService 班级管理用例（完整设计文档 4.22 / API 文档 7.11）。
type ClassesService struct{ s *Services }

// Class 是班级 DTO。
type Class struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	OwnerUserID string `json:"owner_user_id"`
	Name        string `json:"name"`
	Subject     string `json:"subject"`
	Semester    string `json:"semester"`
	Status      string `json:"status"`
	InviteCode  string `json:"invite_code"`
	MemberCount int    `json:"member_count"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// InviteCode 是班级邀请码 DTO。
type InviteCode struct {
	ClassID string `json:"class_id"`
	Code    string `json:"code"`
}

// ClassMember 是班级成员 DTO。
type ClassMember struct {
	ID            string `json:"id"`
	ClassID       string `json:"class_id"`
	StudentUserID string `json:"student_user_id"`
	DisplayName   string `json:"display_name"`
	Status        string `json:"status"`
	JoinedAt      string `json:"joined_at"`
}

// ClassCreateReq 创建班级请求（教师）。
type ClassCreateReq struct {
	WorkspaceID    string `json:"workspace_id"`
	UserID         string `json:"user_id"`
	Name           string `json:"name"`
	Subject        string `json:"subject"`
	Semester       string `json:"semester"`
	IdempotencyKey string `json:"idempotency_key"`
}

// ClassListReq 班级列表请求。
type ClassListReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
}

// ClassGetReq 获取班级请求。
type ClassGetReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	ClassID     string `json:"class_id"`
}

// ClassUpdateReq 更新班级请求（教师，字段可选）。
type ClassUpdateReq struct {
	WorkspaceID string  `json:"workspace_id"`
	UserID      string  `json:"user_id"`
	ClassID     string  `json:"class_id"`
	Name        *string `json:"name"`
	Subject     *string `json:"subject"`
	Semester    *string `json:"semester"`
}

// ClassArchiveReq 归档班级请求（教师）。
type ClassArchiveReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	ClassID     string `json:"class_id"`
}

// ClassInviteReq 生成邀请码请求（教师）。
type ClassInviteReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	ClassID     string `json:"class_id"`
}

// ClassMemberAddReq 添加成员请求（教师）。
type ClassMemberAddReq struct {
	WorkspaceID    string `json:"workspace_id"`
	UserID         string `json:"user_id"`
	ClassID        string `json:"class_id"`
	StudentUserID  string `json:"student_user_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

// ClassMemberRemoveReq 移除成员请求（教师）。
type ClassMemberRemoveReq struct {
	WorkspaceID   string `json:"workspace_id"`
	UserID        string `json:"user_id"`
	ClassID       string `json:"class_id"`
	StudentUserID string `json:"student_user_id"`
}

// ClassMemberListReq 班级成员列表请求。
type ClassMemberListReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	ClassID     string `json:"class_id"`
}

// requireTeacher 校验调用者为教师角色；非教师 → FORBIDDEN + 审计。
func (c *ClassesService) requireTeacher(ctx context.Context, wsID, userID, action, classID string) error {
	if userID == "" {
		return domain.InvalidArg("user_id 必填")
	}
	u, err := c.s.Repo.GetUser(ctx, wsID, userID)
	if err != nil {
		return err
	}
	if u == nil {
		return domain.Forbidden("用户不存在")
	}
	if u.Role != "teacher" {
		payload := map[string]any{"forbidden": true, "role": u.Role}
		if classID != "" {
			payload["class_id"] = classID
		}
		c.s.audit(ctx, wsID, action, "class", classID, payload)
		return domain.Forbidden("仅教师可执行此操作")
	}
	return nil
}

// assertReadableClass 校验调用者可读班级：本人创建（教师）或为 active 成员（学生）。
func (c *ClassesService) assertReadableClass(ctx context.Context, wsID, userID, classID string) (*repository.ClassRow, error) {
	if userID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if classID == "" || !domain.ValidID(classID) {
		return nil, domain.InvalidArg("class_id 无效")
	}
	cls, err := c.s.Repo.GetClass(ctx, wsID, classID)
	if err != nil {
		return nil, err
	}
	if cls == nil {
		return nil, domain.NotFound("班级不存在")
	}
	if cls.OwnerUserID == userID {
		return cls, nil
	}
	m, err := c.s.Repo.GetClassMember(ctx, classID, userID)
	if err != nil {
		return nil, err
	}
	if m == nil || m.Status != domain.ClassMemberStatusActive {
		return nil, domain.Forbidden("无权访问该班级")
	}
	return cls, nil
}

// assertEditableClass 校验调用者可管理班级：教师且为班级创建者。
func (c *ClassesService) assertEditableClass(ctx context.Context, wsID, userID, action, classID string) (*repository.ClassRow, error) {
	cls, err := c.s.Repo.GetClass(ctx, wsID, classID)
	if err != nil {
		return nil, err
	}
	if cls == nil {
		return nil, domain.NotFound("班级不存在")
	}
	if err := c.requireTeacher(ctx, wsID, userID, action, classID); err != nil {
		return nil, err
	}
	if cls.OwnerUserID != userID {
		c.s.audit(ctx, wsID, action, "class", classID,
			map[string]any{"forbidden": true, "reason": "not owner"})
		return nil, domain.Forbidden("仅班级创建者可管理该班级")
	}
	return cls, nil
}

// ClassCreate 创建班级（教师）。
func (c *ClassesService) ClassCreate(ctx context.Context, req ClassCreateReq) (*Class, error) {
	if err := c.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.IdempotencyKey == "" {
		return nil, domain.InvalidArg("idempotency_key 必填")
	}
	return withIdempotency(c.s, ctx, req.WorkspaceID, req.IdempotencyKey, "ClassCreate",
		func() (*Class, error) { return c.doCreate(ctx, req) })
}

func (c *ClassesService) doCreate(ctx context.Context, req ClassCreateReq) (*Class, error) {
	if err := c.requireTeacher(ctx, req.WorkspaceID, req.UserID, "class.create", ""); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Name)
	if !domain.ValidClassName(name) {
		return nil, domain.InvalidArg("班级名称长度须为 1-120")
	}
	if !domain.ValidClassSubject(req.Subject) {
		return nil, domain.InvalidArg("科目长度不能超过 80")
	}
	if !domain.ValidClassSemester(req.Semester) {
		return nil, domain.InvalidArg("学期长度不能超过 40")
	}
	row := &repository.ClassRow{
		ID: NewID(), WorkspaceID: req.WorkspaceID, OwnerUserID: req.UserID,
		Name: name, Subject: req.Subject, Semester: req.Semester,
		Status: domain.ClassStatusActive,
	}
	if err := c.s.Repo.CreateClass(ctx, row); err != nil {
		return nil, err
	}
	c.s.audit(ctx, req.WorkspaceID, "class.create", "class", row.ID,
		map[string]any{"name": row.Name, "subject": row.Subject, "semester": row.Semester})
	return c.classFromRow(ctx, row), nil
}

// ClassList 列出当前用户可见班级（教师=创建班级；学生=加入班级）。
func (c *ClassesService) ClassList(ctx context.Context, req ClassListReq) ([]*Class, error) {
	if err := c.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	rows, err := c.s.Repo.ListClassesForUser(ctx, req.WorkspaceID, req.UserID)
	if err != nil {
		return nil, err
	}
	out := make([]*Class, 0, len(rows))
	for _, r := range rows {
		out = append(out, c.classFromRow(ctx, r))
	}
	return out, nil
}

// ClassGet 获取班级详情（创建者或 active 成员）。
func (c *ClassesService) ClassGet(ctx context.Context, req ClassGetReq) (*Class, error) {
	if err := c.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	cls, err := c.assertReadableClass(ctx, req.WorkspaceID, req.UserID, req.ClassID)
	if err != nil {
		return nil, err
	}
	return c.classFromRow(ctx, cls), nil
}

// ClassUpdate 更新班级信息（教师）。
func (c *ClassesService) ClassUpdate(ctx context.Context, req ClassUpdateReq) (*Class, error) {
	if err := c.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	cls, err := c.assertEditableClass(ctx, req.WorkspaceID, req.UserID, "class.update", req.ClassID)
	if err != nil {
		return nil, err
	}
	name, subject, semester := cls.Name, cls.Subject, cls.Semester
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
		if !domain.ValidClassName(name) {
			return nil, domain.InvalidArg("班级名称长度须为 1-120")
		}
	}
	if req.Subject != nil {
		if !domain.ValidClassSubject(*req.Subject) {
			return nil, domain.InvalidArg("科目长度不能超过 80")
		}
		subject = *req.Subject
	}
	if req.Semester != nil {
		if !domain.ValidClassSemester(*req.Semester) {
			return nil, domain.InvalidArg("学期长度不能超过 40")
		}
		semester = *req.Semester
	}
	row, err := c.s.Repo.UpdateClass(ctx, req.WorkspaceID, req.ClassID, name, subject, semester)
	if err != nil {
		return nil, err
	}
	c.s.audit(ctx, req.WorkspaceID, "class.update", "class", req.ClassID,
		map[string]any{"name": name, "subject": subject, "semester": semester})
	return c.classFromRow(ctx, row), nil
}

// ClassArchive 归档班级（教师，active→archived）。
func (c *ClassesService) ClassArchive(ctx context.Context, req ClassArchiveReq) (*Class, error) {
	if err := c.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	cls, err := c.assertEditableClass(ctx, req.WorkspaceID, req.UserID, "class.archive", req.ClassID)
	if err != nil {
		return nil, err
	}
	if cls.Status != domain.ClassStatusActive {
		return nil, domain.InvalidState("班级已归档，无需重复操作")
	}
	row, err := c.s.Repo.UpdateClassStatus(ctx, req.WorkspaceID, req.ClassID, domain.ClassStatusArchived)
	if err != nil {
		return nil, err
	}
	c.s.audit(ctx, req.WorkspaceID, "class.archive", "class", req.ClassID, nil)
	return c.classFromRow(ctx, row), nil
}

// ClassInvite 生成/重置班级邀请码（教师，每次调用重新生成）。
func (c *ClassesService) ClassInvite(ctx context.Context, req ClassInviteReq) (*InviteCode, error) {
	if err := c.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if _, err := c.assertEditableClass(ctx, req.WorkspaceID, req.UserID, "class.invite", req.ClassID); err != nil {
		return nil, err
	}
	code, err := newInviteCode()
	if err != nil {
		return nil, err
	}
	row, err := c.s.Repo.RegenerateInviteCode(ctx, req.WorkspaceID, req.ClassID, code)
	if err != nil {
		return nil, err
	}
	c.s.audit(ctx, req.WorkspaceID, "class.invite", "class", req.ClassID, map[string]any{"code": code})
	return &InviteCode{ClassID: row.ID, Code: row.InviteCode}, nil
}

// ClassMemberAdd 添加学生到班级（教师）。
func (c *ClassesService) ClassMemberAdd(ctx context.Context, req ClassMemberAddReq) (*Class, error) {
	if err := c.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	return withIdempotency(c.s, ctx, req.WorkspaceID, req.IdempotencyKey, "ClassMemberAdd",
		func() (*Class, error) { return c.doAddMember(ctx, req) })
}

func (c *ClassesService) doAddMember(ctx context.Context, req ClassMemberAddReq) (*Class, error) {
	cls, err := c.assertEditableClass(ctx, req.WorkspaceID, req.UserID, "class.member_add", req.ClassID)
	if err != nil {
		return nil, err
	}
	if cls.Status != domain.ClassStatusActive {
		return nil, domain.InvalidState("已归档班级不可添加成员")
	}
	if req.StudentUserID == "" || !domain.ValidID(req.StudentUserID) {
		return nil, domain.InvalidArg("student_user_id 无效")
	}
	stu, err := c.s.Repo.GetUser(ctx, req.WorkspaceID, req.StudentUserID)
	if err != nil {
		return nil, err
	}
	if stu == nil {
		return nil, domain.NotFound("学生不存在")
	}
	if stu.Role != "student" {
		return nil, domain.InvalidArg("只能添加学生角色的用户")
	}
	existing, err := c.s.Repo.GetClassMember(ctx, req.ClassID, req.StudentUserID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.Status == domain.ClassMemberStatusActive {
			return nil, domain.Conflict("该学生已在班级中")
		}
		// removed → 重新激活
		if _, err := c.s.Repo.UpdateClassMemberStatus(ctx, req.ClassID, req.StudentUserID, domain.ClassMemberStatusActive); err != nil {
			return nil, err
		}
		c.s.audit(ctx, req.WorkspaceID, "class.member_add", "class", req.ClassID,
			map[string]any{"student_user_id": req.StudentUserID, "reactivated": true})
		return c.classFromRow(ctx, cls), nil
	}
	if err := c.s.Repo.CreateClassMember(ctx, &repository.ClassMemberRow{
		ID: NewID(), ClassID: req.ClassID, StudentUserID: req.StudentUserID,
	}); err != nil {
		return nil, err
	}
	c.s.audit(ctx, req.WorkspaceID, "class.member_add", "class", req.ClassID,
		map[string]any{"student_user_id": req.StudentUserID})
	return c.classFromRow(ctx, cls), nil
}

// ClassMemberRemove 移除学生（教师，active→removed）。
func (c *ClassesService) ClassMemberRemove(ctx context.Context, req ClassMemberRemoveReq) (*Class, error) {
	if err := c.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	cls, err := c.assertEditableClass(ctx, req.WorkspaceID, req.UserID, "class.member_remove", req.ClassID)
	if err != nil {
		return nil, err
	}
	if req.StudentUserID == "" || !domain.ValidID(req.StudentUserID) {
		return nil, domain.InvalidArg("student_user_id 无效")
	}
	existing, err := c.s.Repo.GetClassMember(ctx, req.ClassID, req.StudentUserID)
	if err != nil {
		return nil, err
	}
	if existing == nil || existing.Status != domain.ClassMemberStatusActive {
		return nil, domain.NotFound("该学生不在班级中")
	}
	if _, err := c.s.Repo.UpdateClassMemberStatus(ctx, req.ClassID, req.StudentUserID, domain.ClassMemberStatusRemoved); err != nil {
		return nil, err
	}
	c.s.audit(ctx, req.WorkspaceID, "class.member_remove", "class", req.ClassID,
		map[string]any{"student_user_id": req.StudentUserID})
	return c.classFromRow(ctx, cls), nil
}

// ClassMemberList 列出班级成员（创建者或 active 成员可见）。
func (c *ClassesService) ClassMemberList(ctx context.Context, req ClassMemberListReq) ([]*ClassMember, error) {
	if err := c.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if _, err := c.assertReadableClass(ctx, req.WorkspaceID, req.UserID, req.ClassID); err != nil {
		return nil, err
	}
	rows, err := c.s.Repo.ListClassMembers(ctx, req.ClassID)
	if err != nil {
		return nil, err
	}
	out := make([]*ClassMember, 0, len(rows))
	for _, m := range rows {
		cm := &ClassMember{
			ID: m.ID, ClassID: m.ClassID, StudentUserID: m.StudentUserID,
			Status: m.Status, JoinedAt: m.JoinedAt,
		}
		if u, err := c.s.Repo.GetUser(ctx, req.WorkspaceID, m.StudentUserID); err == nil && u != nil {
			cm.DisplayName = u.DisplayName
		}
		out = append(out, cm)
	}
	return out, nil
}

func (c *ClassesService) classFromRow(ctx context.Context, r *repository.ClassRow) *Class {
	cnt, err := c.s.Repo.CountActiveMembers(ctx, r.ID)
	if err != nil {
		cnt = 0
	}
	return &Class{
		ID: r.ID, WorkspaceID: r.WorkspaceID, OwnerUserID: r.OwnerUserID,
		Name: r.Name, Subject: r.Subject, Semester: r.Semester,
		Status: r.Status, InviteCode: r.InviteCode, MemberCount: cnt,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// inviteCodeAlphabet 邀请码字符集（A-Z0-9，排除易混淆字符）。
const inviteCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// newInviteCode 生成 8 位邀请码（crypto/rand，A-Z0-9）。
func newInviteCode() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = inviteCodeAlphabet[int(b[i])%len(inviteCodeAlphabet)]
	}
	return string(b), nil
}
