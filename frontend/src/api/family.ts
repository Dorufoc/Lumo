// 家庭绑定与家长模式 API（API 文档 7.10 / 完整设计文档 4.21）。
import { call } from '@/api/client'
import type {
  DeleteResult,
  FamilyBindReq,
  FamilyBinding,
  FamilyInvite,
  FamilyInviteCreateReq,
  FamilyInviteGetReq,
  FamilyOverview,
  FamilyUnbindReq,
  FamilyViewItem,
  FamilyViewReq,
  ParentSettings,
  ParentSettingsUpdateReq,
} from '@/api/types'

/** 生成/刷新家庭邀请码（学生，24h 有效）。 */
export function familyInviteCreate(req: FamilyInviteCreateReq): Promise<FamilyInvite> {
  return call<FamilyInvite>('FamilyInviteCreate', { ...req })
}

/** 学生家庭面板（当前邀请码 + 绑定列表）。 */
export function familyInviteGet(req: FamilyInviteGetReq): Promise<FamilyOverview> {
  return call<FamilyOverview>('FamilyInviteGet', { ...req })
}

/** 家长通过邀请码绑定学生。 */
export function familyBind(req: FamilyBindReq): Promise<FamilyBinding> {
  return call<FamilyBinding>('FamilyBind', { ...req })
}

/** 解除绑定（学生或家长任一方）。 */
export function familyUnbind(req: FamilyUnbindReq): Promise<DeleteResult> {
  return call<DeleteResult>('FamilyUnbind', { ...req })
}

/** 家长更新学生使用限制（每日时长 / AI 开关 / 周报开关）。 */
export function parentSettingsUpdate(req: ParentSettingsUpdateReq): Promise<ParentSettings> {
  return call<ParentSettings>('ParentSettingsUpdate', { ...req })
}

/** 家长视图聚合（仅家长角色可调用）。 */
export function familyViewGet(req: FamilyViewReq): Promise<FamilyViewItem[]> {
  return call<FamilyViewItem[]>('FamilyViewGet', { ...req })
}
