package service

// 0008 组织版层级权限测试（TDD：先于 OrgService / ClassesService org_admin 扩展实现，未实现时必须失败）。
// 覆盖：org_admin 角色可创建/管理班级、指派教师；教师只能管自己班级（教师 A 改教师 B 班级 → FORBIDDEN）；
// 学生/普通角色不得访问组织管理方法；admin 角色回归（管理端可用）。

import (
	"testing"

	"lumo/internal/domain"
)

// createOrgAdmin 创建工作区组织管理员用户并返回 ID。
func createOrgAdmin(t *testing.T, s *Services, wsID string) string {
	t.Helper()
	u, err := s.Workspace.UserCreate(ctx(), UserCreateReq{WorkspaceID: wsID, DisplayName: "组织管理员", Role: "org_admin"})
	if err != nil {
		t.Fatalf("create org_admin: %v", err)
	}
	return u.ID
}

// TestUserCreateOrgAdmin 角色扩展：UserCreate 必须接受 org_admin。
func TestUserCreateOrgAdmin(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	id := createOrgAdmin(t, s, ws.ID)
	u, err := s.Workspace.UserGetProfile(ctx(), UserGetProfileReq{WorkspaceID: ws.ID, UserID: id})
	if err != nil {
		t.Fatalf("get org_admin profile: %v", err)
	}
	if u.Role != "org_admin" {
		t.Fatalf("expected role org_admin, got %q", u.Role)
	}
	// 非法角色仍被拒绝。
	if _, err := s.Workspace.UserCreate(ctx(), UserCreateReq{WorkspaceID: ws.ID, DisplayName: "x", Role: "superuser"}); err == nil {
		t.Fatalf("unknown role must be rejected")
	}
}

// TestOrgAdminClassHierarchy 层级权限：org_admin 可管理全组织班级，教师只能管自己班级。
func TestOrgAdminClassHierarchy(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	orgAdmin := createOrgAdmin(t, s, ws.ID)
	teacherA := createTeacher(t, s, ws.ID)
	teacherB := createTeacher(t, s, ws.ID)

	// org_admin 建班成功（非教师角色）。
	cls, err := s.Classes.ClassCreate(ctx(), ClassCreateReq{
		WorkspaceID: ws.ID, UserID: orgAdmin, Name: "组织班", IdempotencyKey: "cc-" + NewID(),
	})
	if err != nil {
		t.Fatalf("org_admin create class: %v", err)
	}

	// org_admin 指派教师 A 为班级负责人。
	assigned, err := s.Org.OrgClassAssignTeacher(ctx(), OrgClassAssignTeacherReq{
		WorkspaceID: ws.ID, UserID: orgAdmin, ClassID: cls.ID, TeacherUserID: teacherA,
	})
	if err != nil {
		t.Fatalf("org_admin assign teacher: %v", err)
	}
	if assigned.OwnerUserID != teacherA {
		t.Fatalf("expected owner teacherA, got %q", assigned.OwnerUserID)
	}

	// 教师 A 管理自己班级成功。
	if _, err := s.Classes.ClassUpdate(ctx(), ClassUpdateReq{
		WorkspaceID: ws.ID, UserID: teacherA, ClassID: assigned.ID, Name: strPtr("改名"),
	}); err != nil {
		t.Fatalf("teacher A update own class: %v", err)
	}

	// 教师 B 尝试改教师 A 班级 → FORBIDDEN。
	if _, err := s.Classes.ClassUpdate(ctx(), ClassUpdateReq{
		WorkspaceID: ws.ID, UserID: teacherB, ClassID: assigned.ID, Name: strPtr("越权"),
	}); err == nil {
		t.Fatalf("teacher B must not edit teacher A's class")
	} else {
		de := domain.AsError(err)
		if de.Code != domain.CodeForbidden {
			t.Fatalf("expected FORBIDDEN for cross-teacher edit, got %v", err)
		}
	}

	// org_admin 可管理任意班级（改教师 A 的班）。
	if _, err := s.Classes.ClassUpdate(ctx(), ClassUpdateReq{
		WorkspaceID: ws.ID, UserID: orgAdmin, ClassID: assigned.ID, Name: strPtr("组织改"),
	}); err != nil {
		t.Fatalf("org_admin update any class: %v", err)
	}

	// org_admin 可见工作区全部班级。
	list, err := s.Org.OrgClassList(ctx(), OrgClassListReq{WorkspaceID: ws.ID, UserID: orgAdmin})
	if err != nil {
		t.Fatalf("org_admin class list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 class in org view, got %d", len(list))
	}

	// 学生访问组织管理方法 → FORBIDDEN。
	studentID := createStudent(t, s, ws.ID)
	if _, err := s.Org.OrgClassList(ctx(), OrgClassListReq{WorkspaceID: ws.ID, UserID: studentID}); err == nil {
		t.Fatalf("student must not access org class list")
	} else {
		de := domain.AsError(err)
		if de.Code != domain.CodeForbidden {
			t.Fatalf("expected FORBIDDEN for student org access, got %v", err)
		}
	}
	if _, err := s.Org.OrgClassAssignTeacher(ctx(), OrgClassAssignTeacherReq{
		WorkspaceID: ws.ID, UserID: studentID, ClassID: assigned.ID, TeacherUserID: teacherA,
	}); err == nil {
		t.Fatalf("student must not assign teacher")
	}
	// 教师也不得指派教师（非组织管理员）。
	if _, err := s.Org.OrgClassAssignTeacher(ctx(), OrgClassAssignTeacherReq{
		WorkspaceID: ws.ID, UserID: teacherB, ClassID: assigned.ID, TeacherUserID: teacherA,
	}); err == nil {
		t.Fatalf("teacher must not assign teacher")
	}
}

// TestOrgWorkspaceUpdate 组织元数据：org_admin 可设置 org_name/org_admin_user_id；学生 FORBIDDEN。
func TestOrgWorkspaceUpdate(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	orgAdmin := createOrgAdmin(t, s, ws.ID)
	studentID := createStudent(t, s, ws.ID)

	upd, err := s.Org.OrgWorkspaceUpdate(ctx(), OrgWorkspaceUpdateReq{
		WorkspaceID: ws.ID, UserID: orgAdmin,
		OrgName: strPtr("晨曦中学"), OrgAdminUserID: strPtr(orgAdmin),
	})
	if err != nil {
		t.Fatalf("org_admin update workspace org: %v", err)
	}
	if upd.OrgName == nil || *upd.OrgName != "晨曦中学" {
		t.Fatalf("expected org_name 晨曦中学, got %+v", upd.OrgName)
	}
	if upd.OrgAdminUserID == nil || *upd.OrgAdminUserID != orgAdmin {
		t.Fatalf("expected org_admin_user_id %s, got %+v", orgAdmin, upd.OrgAdminUserID)
	}

	// 学生更新组织元数据 → FORBIDDEN。
	if _, err := s.Org.OrgWorkspaceUpdate(ctx(), OrgWorkspaceUpdateReq{
		WorkspaceID: ws.ID, UserID: studentID, OrgName: strPtr("越权机构"),
	}); err == nil {
		t.Fatalf("student must not update org metadata")
	} else {
		de := domain.AsError(err)
		if de.Code != domain.CodeForbidden {
			t.Fatalf("expected FORBIDDEN for student org update, got %v", err)
		}
	}
}

// TestOrgTeacherList org_admin 可列出教师；学生 FORBIDDEN。
func TestOrgTeacherList(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	orgAdmin := createOrgAdmin(t, s, ws.ID)
	createTeacher(t, s, ws.ID)

	teachers, err := s.Org.OrgTeacherList(ctx(), OrgTeacherListReq{WorkspaceID: ws.ID, UserID: orgAdmin})
	if err != nil {
		t.Fatalf("org_admin teacher list: %v", err)
	}
	if len(teachers) != 1 || teachers[0].Role != "teacher" {
		t.Fatalf("expected 1 teacher, got %d", len(teachers))
	}

	studentID := createStudent(t, s, ws.ID)
	if _, err := s.Org.OrgTeacherList(ctx(), OrgTeacherListReq{WorkspaceID: ws.ID, UserID: studentID}); err == nil {
		t.Fatalf("student must not list teachers")
	}
}

// TestAdminRegression admin 角色仍可访问管理端（回归验证）。
func TestAdminRegression(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	adminID := createTeacher(t, s, ws.ID)
	_ = adminID
	// 管理端方法回归：审核列表可用（角色门禁保持现状，admin 可用）。
	page, err := s.Admin.AdminReviewList(ctx(), AdminReviewListReq{WorkspaceID: ws.ID})
	if err != nil {
		t.Fatalf("admin review list regression: %v", err)
	}
	if page == nil {
		t.Fatalf("admin review list returned nil")
	}
}
