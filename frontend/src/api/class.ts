// 班级管理 API（API 文档 7.11 / 完整设计文档 4.22）。
import { call } from '@/api/client'
import type {
  Class,
  ClassArchiveReq,
  ClassCreateReq,
  ClassGetReq,
  ClassInviteReq,
  ClassListReq,
  ClassMember,
  ClassMemberAddReq,
  ClassMemberListReq,
  ClassMemberRemoveReq,
  ClassUpdateReq,
  InviteCode,
} from '@/api/types'

/** 创建班级（教师）。 */
export function classCreate(req: ClassCreateReq): Promise<Class> {
  return call<Class>('ClassCreate', { ...req })
}

/** 班级列表（教师=创建的；学生=加入的）。 */
export function classList(req: ClassListReq): Promise<Class[]> {
  return call<Class[]>('ClassList', { ...req })
}

/** 班级详情（创建者或 active 成员）。 */
export function classGet(req: ClassGetReq): Promise<Class> {
  return call<Class>('ClassGet', { ...req })
}

/** 更新班级（教师）。 */
export function classUpdate(req: ClassUpdateReq): Promise<Class> {
  return call<Class>('ClassUpdate', { ...req })
}

/** 归档班级（教师）。 */
export function classArchive(req: ClassArchiveReq): Promise<Class> {
  return call<Class>('ClassArchive', { ...req })
}

/** 生成/重置邀请码（教师）。 */
export function classInvite(req: ClassInviteReq): Promise<InviteCode> {
  return call<InviteCode>('ClassInvite', { ...req })
}

/** 添加学生到班级（教师）。 */
export function classMemberAdd(req: ClassMemberAddReq): Promise<Class> {
  return call<Class>('ClassMemberAdd', { ...req })
}

/** 移除学生（教师）。 */
export function classMemberRemove(req: ClassMemberRemoveReq): Promise<Class> {
  return call<Class>('ClassMemberRemove', { ...req })
}

/** 班级成员列表（创建者或 active 成员）。 */
export function classMemberList(req: ClassMemberListReq): Promise<ClassMember[]> {
  return call<ClassMember[]>('ClassMemberList', { ...req })
}
