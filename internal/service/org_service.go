// org_service.go 组织版用例（组织-班级-教师层级，完整设计文档 11.9.5 / Todo 41）。
// 组织管理员（org_admin）可管理组织内全部班级、指派教师、设置机构元数据；
// 教师仅能管理自己创建的班级；admin 管理全局（管理端）。
package service

import (
	"context"
	"strings"

	"lumo/internal/domain"
)

// OrgService 实现组织管理用例。
type OrgService struct{ s *Services }

// OrgWorkspaceUpdateReq 更新机构元数据请求。
type OrgWorkspaceUpdateReq struct {
	WorkspaceID    string  `json:"workspace_id"`
	UserID         string  `json:"user_id"`
	OrgName        *string `json:"org_name"`          // 空串清空
	OrgAdminUserID *string `json:"org_admin_user_id"` // 必须是工作区内的 org_admin/admin 用户；空串清空
}

// OrgClassListReq 组织班级列表请求。
type OrgClassListReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
}

// OrgClassAssignTeacherReq 指派班级负责人请求。
type OrgClassAssignTeacherReq struct {
	WorkspaceID   string `json:"workspace_id"`
	UserID        string `json:"user_id"`
	ClassID       string `json:"class_id"`
	TeacherUserID string `json:"teacher_user_id"`
}

// OrgTeacherListReq 组织教师列表请求。
type OrgTeacherListReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
}

// requireOrgAdmin 校验调用者为组织管理员或全局管理员；非组织管理员 → FORBIDDEN + 审计。
func (o *OrgService) requireOrgAdmin(ctx context.Context, wsID, userID, action string) error {
	if userID == "" {
		return domain.InvalidArg("user_id 必填")
	}
	u, err := o.s.Repo.GetUser(ctx, wsID, userID)
	if err != nil {
		return err
	}
	if u == nil {
		return domain.Forbidden("用户不存在")
	}
	if u.Role != "org_admin" && u.Role != "admin" {
		o.s.audit(ctx, wsID, action, "org", "",
			map[string]any{"forbidden": true, "role": u.Role})
		return domain.Forbidden("仅组织管理员可执行此操作")
	}
	return nil
}

// OrgWorkspaceUpdate 更新机构元数据（org_name/org_admin_user_id）。
func (o *OrgService) OrgWorkspaceUpdate(ctx context.Context, req OrgWorkspaceUpdateReq) (*Workspace, error) {
	if err := o.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if err := o.requireOrgAdmin(ctx, req.WorkspaceID, req.UserID, "org.workspace.update"); err != nil {
		return nil, err
	}
	cur, err := o.s.Repo.GetWorkspace(ctx, req.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, domain.NotFound("工作区不存在或已被删除")
	}
	var orgName, orgAdminUserID *string
	if req.OrgName != nil {
		name := strings.TrimSpace(*req.OrgName)
		if name == "" {
			orgName = nil
		} else {
			if len(name) > 120 {
				return nil, domain.InvalidArg("机构名称长度不能超过 120")
			}
			orgName = &name
		}
	} else {
		orgName = cur.OrgName
	}
	if req.OrgAdminUserID != nil {
		uid := strings.TrimSpace(*req.OrgAdminUserID)
		if uid == "" {
			orgAdminUserID = nil
		} else {
			u, err := o.s.Repo.GetUser(ctx, req.WorkspaceID, uid)
			if err != nil {
				return nil, err
			}
			if u == nil {
				return nil, domain.InvalidArg("org_admin_user_id 指向的用户不存在")
			}
			if u.Role != "org_admin" && u.Role != "admin" {
				return nil, domain.InvalidArg("org_admin_user_id 必须是组织管理员或管理员角色")
			}
			orgAdminUserID = &uid
		}
	} else {
		orgAdminUserID = cur.OrgAdminUserID
	}
	row, err := o.s.Repo.UpdateWorkspaceOrg(ctx, req.WorkspaceID, orgName, orgAdminUserID)
	if err != nil {
		return nil, err
	}
	o.s.audit(ctx, req.WorkspaceID, "org.workspace.update", "workspace", req.WorkspaceID,
		map[string]any{"org_name": orgName, "org_admin_user_id": orgAdminUserID})
	return workspaceFromRow(row), nil
}

// OrgClassList 列出组织内全部班级（组织管理员视图）。
func (o *OrgService) OrgClassList(ctx context.Context, req OrgClassListReq) ([]*Class, error) {
	if err := o.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if err := o.requireOrgAdmin(ctx, req.WorkspaceID, req.UserID, "org.class.list"); err != nil {
		return nil, err
	}
	rows, err := o.s.Repo.ListClassesInWorkspace(ctx, req.WorkspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]*Class, 0, len(rows))
	for _, r := range rows {
		out = append(out, o.s.Classes.classFromRow(ctx, r))
	}
	return out, nil
}

// OrgClassAssignTeacher 指派教师为班级负责人（班级在教师间转移）。
func (o *OrgService) OrgClassAssignTeacher(ctx context.Context, req OrgClassAssignTeacherReq) (*Class, error) {
	if err := o.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if err := o.requireOrgAdmin(ctx, req.WorkspaceID, req.UserID, "org.class.assign_teacher"); err != nil {
		return nil, err
	}
	if req.ClassID == "" || !domain.ValidID(req.ClassID) {
		return nil, domain.InvalidArg("class_id 无效")
	}
	cls, err := o.s.Repo.GetClass(ctx, req.WorkspaceID, req.ClassID)
	if err != nil {
		return nil, err
	}
	if cls == nil {
		return nil, domain.NotFound("班级不存在")
	}
	if req.TeacherUserID == "" || !domain.ValidID(req.TeacherUserID) {
		return nil, domain.InvalidArg("teacher_user_id 无效")
	}
	teacher, err := o.s.Repo.GetUser(ctx, req.WorkspaceID, req.TeacherUserID)
	if err != nil {
		return nil, err
	}
	if teacher == nil {
		return nil, domain.InvalidArg("teacher_user_id 指向的用户不存在")
	}
	if teacher.Role != "teacher" {
		return nil, domain.InvalidArg("teacher_user_id 必须是教师角色")
	}
	row, err := o.s.Repo.ReassignClassOwner(ctx, req.WorkspaceID, req.ClassID, req.TeacherUserID)
	if err != nil {
		return nil, err
	}
	o.s.audit(ctx, req.WorkspaceID, "org.class.assign_teacher", "class", req.ClassID,
		map[string]any{"teacher_user_id": req.TeacherUserID})
	return o.s.Classes.classFromRow(ctx, row), nil
}

// OrgTeacherList 列出组织内教师（组织管理员视图）。
func (o *OrgService) OrgTeacherList(ctx context.Context, req OrgTeacherListReq) ([]*UserProfile, error) {
	if err := o.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if err := o.requireOrgAdmin(ctx, req.WorkspaceID, req.UserID, "org.teacher.list"); err != nil {
		return nil, err
	}
	rows, err := o.s.Repo.ListUsers(ctx, req.WorkspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]*UserProfile, 0, len(rows))
	for _, r := range rows {
		if r.Role == "teacher" {
			out = append(out, userFromRow(r))
		}
	}
	return out, nil
}
