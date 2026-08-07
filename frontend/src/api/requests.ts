// 求题闭环 API（完整设计文档 4.20 P2 求题请求 / Todo 36）。
// 状态机 content_requests.status[open|fulfilled|closed]：open 唯一中间态。
import { call } from '@/api/client'
import type {
  ContentRequest,
  ContentRequestCancelReq,
  ContentRequestCreateReq,
  ContentRequestGenerateReq,
  ContentRequestListReq,
  ContentRequestReviewReq,
} from '@/api/types'

/** 提交求题请求（status=open；幂等去重）。 */
export function contentRequestCreate(req: ContentRequestCreateReq): Promise<ContentRequest> {
  return call<ContentRequest>('ContentRequestCreate', { ...req })
}

/** 生成题目草稿（open 停留，创建 pending 审核项；未配置 Provider 降级模板）。 */
export function contentRequestGenerate(req: ContentRequestGenerateReq): Promise<ContentRequest> {
  return call<ContentRequest>('ContentRequestGenerate', { ...req })
}

/** 审核决策：approved → fulfilled + 题目入库；rejected → closed。 */
export function contentRequestReview(req: ContentRequestReviewReq): Promise<ContentRequest> {
  return call<ContentRequest>('ContentRequestReview', { ...req })
}

/** 取消请求：open → closed（题目不入库）。 */
export function contentRequestCancel(req: ContentRequestCancelReq): Promise<ContentRequest> {
  return call<ContentRequest>('ContentRequestCancel', { ...req })
}

/** 我的请求列表（user_id 空 = 全部）。 */
export function contentRequestList(req: ContentRequestListReq): Promise<ContentRequest[]> {
  return call<ContentRequest[]>('ContentRequestList', { ...req })
}
