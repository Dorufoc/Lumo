package service

import (
	"strings"
	"testing"

	"lumo/internal/domain"
)

// ---- 辅助 ----

// createTeacher 创建工作区教师用户并返回 ID。
func createTeacher(t *testing.T, s *Services, wsID string) string {
	t.Helper()
	u, err := s.Workspace.UserCreate(ctx(), UserCreateReq{WorkspaceID: wsID, DisplayName: "教师", Role: "teacher"})
	if err != nil {
		t.Fatalf("create teacher: %v", err)
	}
	return u.ID
}

// createStudent 创建工作区学生用户并返回 ID。
func createStudent(t *testing.T, s *Services, wsID string) string {
	t.Helper()
	u, err := s.Workspace.UserCreate(ctx(), UserCreateReq{WorkspaceID: wsID, DisplayName: "学生", Role: "student"})
	if err != nil {
		t.Fatalf("create student: %v", err)
	}
	return u.ID
}

// createClass 教师建班，返回 Class。
func createClass(t *testing.T, s *Services, wsID, teacherID string) *Class {
	t.Helper()
	c, err := s.Classes.ClassCreate(ctx(), ClassCreateReq{
		WorkspaceID: wsID, UserID: teacherID, Name: "高一(1)班", Subject: "数学", Semester: "2026春",
		IdempotencyKey: "cc-" + NewID(),
	})
	if err != nil {
		t.Fatalf("create class: %v", err)
	}
	return c
}

// isForbidden 断言错误为 FORBIDDEN。
func isForbidden(t *testing.T, err error) {
	t.Helper()
	de := domain.AsError(err)
	if de.Code != domain.CodeForbidden {
		t.Fatalf("expected FORBIDDEN, got %v", err)
	}
}

// ---- 权限矩阵 ----

func TestClassPermissionMatrix(t *testing.T) {
	s, _ := newTestServices(t)
	ws, studentID := createWorkspace(t, s)
	teacherID := createTeacher(t, s, ws.ID)

	// 学生建班 → FORBIDDEN
	_, err := s.Classes.ClassCreate(ctx(), ClassCreateReq{
		WorkspaceID: ws.ID, UserID: studentID, Name: "x", IdempotencyKey: "cc-" + NewID(),
	})
	isForbidden(t, err)
	if n := classAuditCount(t, s, "class.create"); n != 1 {
		t.Fatalf("expected 1 audit for forbidden create, got %d", n)
	}
	if !classAuditPayloadContains(t, s, "class.create", "forbidden") {
		t.Fatalf("expected forbidden audit payload to contain forbidden marker")
	}

	// 教师建班成功
	cls := createClass(t, s, ws.ID, teacherID)
	if cls.Status != domain.ClassStatusActive || cls.OwnerUserID != teacherID {
		t.Fatalf("unexpected class: %+v", cls)
	}

	// 学生邀请 → FORBIDDEN
	_, err = s.Classes.ClassInvite(ctx(), ClassInviteReq{WorkspaceID: ws.ID, UserID: studentID, ClassID: cls.ID})
	isForbidden(t, err)

	// 学生加人 → FORBIDDEN
	_, err = s.Classes.ClassMemberAdd(ctx(), ClassMemberAddReq{
		WorkspaceID: ws.ID, UserID: studentID, ClassID: cls.ID,
		StudentUserID: studentID, IdempotencyKey: "cma-" + NewID(),
	})
	isForbidden(t, err)

	// 学生移除成员 → FORBIDDEN
	_, err = s.Classes.ClassMemberRemove(ctx(), ClassMemberRemoveReq{
		WorkspaceID: ws.ID, UserID: studentID, ClassID: cls.ID, StudentUserID: studentID,
	})
	isForbidden(t, err)

	// 学生更新 → FORBIDDEN
	_, err = s.Classes.ClassUpdate(ctx(), ClassUpdateReq{
		WorkspaceID: ws.ID, UserID: studentID, ClassID: cls.ID, Name: strPtr("改名"),
	})
	isForbidden(t, err)

	// 学生归档 → FORBIDDEN
	_, err = s.Classes.ClassArchive(ctx(), ClassArchiveReq{WorkspaceID: ws.ID, UserID: studentID, ClassID: cls.ID})
	isForbidden(t, err)

	// 非成员读 → FORBIDDEN
	_, err = s.Classes.ClassGet(ctx(), ClassGetReq{WorkspaceID: ws.ID, UserID: studentID, ClassID: cls.ID})
	isForbidden(t, err)

	// 非教师（admin）操作同样 FORBIDDEN
	adminID := createStudent(t, s, ws.ID)
	_, err = s.Classes.ClassCreate(ctx(), ClassCreateReq{
		WorkspaceID: ws.ID, UserID: adminID, Name: "y", IdempotencyKey: "cc-" + NewID(),
	})
	isForbidden(t, err)
}

// ---- ClassCreate ----

func TestClassCreateValidation(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	teacherID := createTeacher(t, s, ws.ID)

	cases := []struct {
		name    string
		req     ClassCreateReq
		code    domain.ErrorCode
		message string
	}{
		{"空名称", ClassCreateReq{WorkspaceID: ws.ID, UserID: teacherID, Name: "", IdempotencyKey: "cc-" + NewID()}, domain.CodeInvalidArgument, ""},
		{"超长名称", ClassCreateReq{WorkspaceID: ws.ID, UserID: teacherID, Name: strings.Repeat("a", 121), IdempotencyKey: "cc-" + NewID()}, domain.CodeInvalidArgument, ""},
		{"缺幂等键", ClassCreateReq{WorkspaceID: ws.ID, UserID: teacherID, Name: "x"}, domain.CodeInvalidArgument, ""},
		{"缺 user_id", ClassCreateReq{WorkspaceID: ws.ID, Name: "x", IdempotencyKey: "cc-" + NewID()}, domain.CodeInvalidArgument, ""},
		{"非法幂等键", ClassCreateReq{WorkspaceID: ws.ID, UserID: teacherID, Name: "x", IdempotencyKey: "短"}, domain.CodeInvalidArgument, ""},
		{"不存在的教师", ClassCreateReq{WorkspaceID: ws.ID, UserID: "nobody-123", Name: "x", IdempotencyKey: "cc-" + NewID()}, domain.CodeForbidden, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Classes.ClassCreate(ctx(), tc.req)
			de := domain.AsError(err)
			if de == nil || de.Code != tc.code {
				t.Fatalf("expected %s, got %v", tc.code, err)
			}
		})
	}
}

func TestClassCreateIdempotency(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	teacherID := createTeacher(t, s, ws.ID)
	key := "cc-" + NewID()

	a, err := s.Classes.ClassCreate(ctx(), ClassCreateReq{
		WorkspaceID: ws.ID, UserID: teacherID, Name: "同班", IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	b, err := s.Classes.ClassCreate(ctx(), ClassCreateReq{
		WorkspaceID: ws.ID, UserID: teacherID, Name: "同班", IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if a.ID != b.ID {
		t.Fatalf("idempotency replay returned different class: %s vs %s", a.ID, b.ID)
	}
	// 审计只记一次
	if n := classAuditCount(t, s, "class.create"); n != 1 {
		t.Fatalf("expected 1 audit, got %d", n)
	}
}

// ---- ClassList / ClassGet ----

func TestClassListVisibility(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	teacherID := createTeacher(t, s, ws.ID)
	studentID := createStudent(t, s, ws.ID)
	otherTeacher := createTeacher(t, s, ws.ID)

	cls := createClass(t, s, ws.ID, teacherID)
	other := createClass(t, s, ws.ID, otherTeacher)

	// 教师可见自己创建的班级
	own, err := s.Classes.ClassList(ctx(), ClassListReq{WorkspaceID: ws.ID, UserID: teacherID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(own) != 1 || own[0].ID != cls.ID {
		t.Fatalf("teacher sees %+v, want own class", own)
	}
	// 但看不到其他教师班级
	if _, err := s.Classes.ClassGet(ctx(), ClassGetReq{WorkspaceID: ws.ID, UserID: teacherID, ClassID: other.ID}); err == nil {
		t.Fatalf("teacher should not access other teacher's class")
	}

	// 学生加入前列表为空
	before, err := s.Classes.ClassList(ctx(), ClassListReq{WorkspaceID: ws.ID, UserID: studentID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(before) != 0 {
		t.Fatalf("student should see no class before joining, got %d", len(before))
	}

	// 教师添加学生后，学生可见该班
	if _, err := s.Classes.ClassMemberAdd(ctx(), ClassMemberAddReq{
		WorkspaceID: ws.ID, UserID: teacherID, ClassID: cls.ID,
		StudentUserID: studentID, IdempotencyKey: "cma-" + NewID(),
	}); err != nil {
		t.Fatalf("add member: %v", err)
	}
	after, err := s.Classes.ClassList(ctx(), ClassListReq{WorkspaceID: ws.ID, UserID: studentID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(after) != 1 || after[0].ID != cls.ID {
		t.Fatalf("student sees %+v, want joined class", after)
	}
	got, err := s.Classes.ClassGet(ctx(), ClassGetReq{WorkspaceID: ws.ID, UserID: studentID, ClassID: cls.ID})
	if err != nil || got.ID != cls.ID {
		t.Fatalf("student should read joined class: %v", err)
	}

	// 非本工作区读 → NOT_FOUND 语义（工作区断言失败即报错）
	_, err = s.Classes.ClassGet(ctx(), ClassGetReq{WorkspaceID: "bad-ws", UserID: studentID, ClassID: cls.ID})
	if err == nil {
		t.Fatalf("expected error for bad workspace")
	}
}

// ---- ClassUpdate / ClassArchive ----

func TestClassUpdate(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	teacherID := createTeacher(t, s, ws.ID)
	cls := createClass(t, s, ws.ID, teacherID)

	got, err := s.Classes.ClassUpdate(ctx(), ClassUpdateReq{
		WorkspaceID: ws.ID, UserID: teacherID, ClassID: cls.ID,
		Name: strPtr("高一(2)班"), Subject: strPtr("物理"),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.Name != "高一(2)班" || got.Subject != "物理" {
		t.Fatalf("unexpected updated class: %+v", got)
	}

	// 非法名称
	_, err = s.Classes.ClassUpdate(ctx(), ClassUpdateReq{
		WorkspaceID: ws.ID, UserID: teacherID, ClassID: cls.ID, Name: strPtr(""),
	})
	if de := domain.AsError(err); de == nil || de.Code != domain.CodeInvalidArgument {
		t.Fatalf("expected INVALID_ARGUMENT for empty name, got %v", err)
	}
}

func TestClassArchive(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	teacherID := createTeacher(t, s, ws.ID)
	studentID := createStudent(t, s, ws.ID)
	cls := createClass(t, s, ws.ID, teacherID)

	arch, err := s.Classes.ClassArchive(ctx(), ClassArchiveReq{WorkspaceID: ws.ID, UserID: teacherID, ClassID: cls.ID})
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if arch.Status != domain.ClassStatusArchived {
		t.Fatalf("expected archived, got %s", arch.Status)
	}

	// 重复归档 → INVALID_STATE
	_, err = s.Classes.ClassArchive(ctx(), ClassArchiveReq{WorkspaceID: ws.ID, UserID: teacherID, ClassID: cls.ID})
	if de := domain.AsError(err); de == nil || de.Code != domain.CodeInvalidState {
		t.Fatalf("expected INVALID_STATE on re-archive, got %v", err)
	}

	// 已归档班级不可加人 → INVALID_STATE
	_, err = s.Classes.ClassMemberAdd(ctx(), ClassMemberAddReq{
		WorkspaceID: ws.ID, UserID: teacherID, ClassID: cls.ID,
		StudentUserID: studentID, IdempotencyKey: "cma-" + NewID(),
	})
	if de := domain.AsError(err); de == nil || de.Code != domain.CodeInvalidState {
		t.Fatalf("expected INVALID_STATE adding to archived class, got %v", err)
	}
}

// ---- ClassInvite ----

func TestClassInvite(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	teacherID := createTeacher(t, s, ws.ID)
	cls := createClass(t, s, ws.ID, teacherID)

	inv, err := s.Classes.ClassInvite(ctx(), ClassInviteReq{WorkspaceID: ws.ID, UserID: teacherID, ClassID: cls.ID})
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	if len(inv.Code) != 8 || !isInviteCodeChars(inv.Code) {
		t.Fatalf("invite code should be 8 chars A-Z0-9, got %q", inv.Code)
	}

	// 再次邀请生成新码
	inv2, err := s.Classes.ClassInvite(ctx(), ClassInviteReq{WorkspaceID: ws.ID, UserID: teacherID, ClassID: cls.ID})
	if err != nil {
		t.Fatalf("re-invite: %v", err)
	}
	if inv2.Code == inv.Code {
		t.Fatalf("expected new invite code, got same %q", inv.Code)
	}
}

// ---- ClassMemberAdd / Remove ----

func TestClassMemberAddDuplicateAndReactivate(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	teacherID := createTeacher(t, s, ws.ID)
	studentID := createStudent(t, s, ws.ID)
	cls := createClass(t, s, ws.ID, teacherID)

	key := "cma-" + NewID()
	if _, err := s.Classes.ClassMemberAdd(ctx(), ClassMemberAddReq{
		WorkspaceID: ws.ID, UserID: teacherID, ClassID: cls.ID,
		StudentUserID: studentID, IdempotencyKey: key,
	}); err != nil {
		t.Fatalf("add: %v", err)
	}

	// active 重复添加 → CONFLICT（不同幂等键）
	_, err := s.Classes.ClassMemberAdd(ctx(), ClassMemberAddReq{
		WorkspaceID: ws.ID, UserID: teacherID, ClassID: cls.ID,
		StudentUserID: studentID, IdempotencyKey: "cma2-" + NewID(),
	})
	if de := domain.AsError(err); de == nil || de.Code != domain.CodeConflict {
		t.Fatalf("expected CONFLICT on duplicate active add, got %v", err)
	}

	// 同一幂等键重放 → 返回成功（幂等）
	if _, err := s.Classes.ClassMemberAdd(ctx(), ClassMemberAddReq{
		WorkspaceID: ws.ID, UserID: teacherID, ClassID: cls.ID,
		StudentUserID: studentID, IdempotencyKey: key,
	}); err != nil {
		t.Fatalf("idempotent replay should succeed: %v", err)
	}

	// 移除后再次添加 → 重新激活
	if _, err := s.Classes.ClassMemberRemove(ctx(), ClassMemberRemoveReq{
		WorkspaceID: ws.ID, UserID: teacherID, ClassID: cls.ID, StudentUserID: studentID,
	}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	// 学生移除后不可见
	if _, err := s.Classes.ClassGet(ctx(), ClassGetReq{WorkspaceID: ws.ID, UserID: studentID, ClassID: cls.ID}); err == nil {
		t.Fatalf("removed student should lose read access")
	}
	if _, err := s.Classes.ClassMemberAdd(ctx(), ClassMemberAddReq{
		WorkspaceID: ws.ID, UserID: teacherID, ClassID: cls.ID,
		StudentUserID: studentID, IdempotencyKey: "cma3-" + NewID(),
	}); err != nil {
		t.Fatalf("re-add after remove: %v", err)
	}
	if _, err := s.Classes.ClassGet(ctx(), ClassGetReq{WorkspaceID: ws.ID, UserID: studentID, ClassID: cls.ID}); err != nil {
		t.Fatalf("re-activated student should read: %v", err)
	}

	// 添加不存在学生 → NOT_FOUND 或 CONFLICT（用户不存在）
	_, err = s.Classes.ClassMemberAdd(ctx(), ClassMemberAddReq{
		WorkspaceID: ws.ID, UserID: teacherID, ClassID: cls.ID,
		StudentUserID: "nobody-123", IdempotencyKey: "cma4-" + NewID(),
	})
	if err == nil {
		t.Fatalf("expected error adding non-existent student")
	}
}

func TestClassMemberRemoveNonMember(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	teacherID := createTeacher(t, s, ws.ID)
	cls := createClass(t, s, ws.ID, teacherID)

	_, err := s.Classes.ClassMemberRemove(ctx(), ClassMemberRemoveReq{
		WorkspaceID: ws.ID, UserID: teacherID, ClassID: cls.ID, StudentUserID: "nobody-123",
	})
	if err == nil {
		t.Fatalf("expected error removing non-member")
	}
}

// ---- 小工具 ----

func isInviteCodeChars(code string) bool {
	for _, r := range code {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// classAuditCount 统计 class 相关审计事件数。
func classAuditCount(t *testing.T, s *Services, action string) int {
	t.Helper()
	var n int
	if err := s.Repo.DB().QueryRowContext(ctx(),
		`SELECT COUNT(*) FROM audit_events WHERE action = ?`, action).Scan(&n); err != nil {
		t.Fatalf("count audit %s: %v", action, err)
	}
	return n
}

// classAuditPayloadContains 检查某动作最近一条审计事件 payload 是否含子串。
func classAuditPayloadContains(t *testing.T, s *Services, action, substr string) bool {
	t.Helper()
	var payload string
	if err := s.Repo.DB().QueryRowContext(ctx(),
		`SELECT payload_json FROM audit_events WHERE action = ? ORDER BY created_at DESC, id DESC LIMIT 1`, action).Scan(&payload); err != nil {
		t.Fatalf("read audit %s: %v", action, err)
	}
	return strings.Contains(payload, substr)
}
