// 组织版 Org API（Todo 41：机构元数据 / 组织班级视图 / 指派教师 / 教师列表）。
import { call } from '@/api/client'
import type { Class, OrgClassAssignTeacherReq, OrgClassListReq, OrgTeacherListReq, OrgWorkspaceUpdateReq, UserProfile, Workspace } from '@/api/types'

/** 更新机构元数据（org_name/org_admin_user_id；仅组织管理员）。 */
export function orgWorkspaceUpdate(req: OrgWorkspaceUpdateReq): Promise<Workspace> {
  return call<Workspace>('OrgWorkspaceUpdate', { ...req })
}

/** 列出组织内全部班级（组织管理员视图）。 */
export function orgClassList(req: OrgClassListReq): Promise<Class[]> {
  return call<Class[]>('OrgClassList', { ...req })
}

/** 指派教师为班级负责人（班级在教师间转移）。 */
export function orgClassAssignTeacher(req: OrgClassAssignTeacherReq): Promise<Class> {
  return call<Class>('OrgClassAssignTeacher', { ...req })
}

/** 列出组织内教师（组织管理员视图）。 */
export function orgTeacherList(req: OrgTeacherListReq): Promise<UserProfile[]> {
  return call<UserProfile[]>('OrgTeacherList', { ...req })
}
