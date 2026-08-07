// 管理端 Admin API（Todo 26：审核队列 / Provider 策略 / 功能开关 / 用户禁用 / 审计查询）。
import { call } from '@/api/client'
import type {
  AdminAuditListReq,
  AdminFeatureFlagSetReq,
  AdminProviderPolicySetReq,
  AdminReviewDecideReq,
  AdminReviewListReq,
  AdminUserDisableReq,
  AuditPage,
  FeatureFlag,
  ProviderPolicy,
  ReviewQueuePage,
  ReviewQueueItem,
  UserStatus,
} from '@/api/types'

/** 列出审核队列（status 空=全部）。 */
export function adminReviewList(req: AdminReviewListReq): Promise<ReviewQueuePage> {
  return call<ReviewQueuePage>('AdminReviewList', { ...req })
}

/** 决策审核条目（仅 pending 可迁移）。 */
export function adminReviewDecide(req: AdminReviewDecideReq): Promise<ReviewQueueItem> {
  return call<ReviewQueueItem>('AdminReviewDecide', { ...req })
}

/** 写入或更新 Provider 策略（审计门禁）。 */
export function adminProviderPolicySet(req: AdminProviderPolicySetReq): Promise<ProviderPolicy> {
  return call<ProviderPolicy>('AdminProviderPolicySet', { ...req })
}

/** 写入或更新功能开关（审计门禁）。 */
export function adminFeatureFlagSet(req: AdminFeatureFlagSetReq): Promise<FeatureFlag> {
  return call<FeatureFlag>('AdminFeatureFlagSet', { ...req })
}

/** 禁用用户（写 disabled_at；审计门禁）。 */
export function adminUserDisable(req: AdminUserDisableReq): Promise<UserStatus> {
  return call<UserStatus>('AdminUserDisable', { ...req })
}

/** 列出审计事件（action / actor_id 过滤）。 */
export function adminAuditList(req: AdminAuditListReq): Promise<AuditPage> {
  return call<AuditPage>('AdminAuditList', { ...req })
}
