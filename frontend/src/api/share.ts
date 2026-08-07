// 创作分享 API（API 文档 7.13 / 完整设计文档 4.20）。
import { call } from '@/api/client'
import type {
  DeleteResult,
  Share,
  ShareCreateReq,
  ShareResolveReq,
  ShareResolveResult,
  ShareRevokeReq,
} from '@/api/types'

/** 创建分享链接（强制安全扫描，未通过则拒绝发布）。 */
export function shareCreate(req: ShareCreateReq): Promise<Share> {
  return call<Share>('ShareCreate', { ...req })
}

/** 撤销分享链接（立即失效，仅属主可操作）。 */
export function shareRevoke(req: ShareRevokeReq): Promise<DeleteResult> {
  return call<DeleteResult>('ShareRevoke', { ...req })
}

/** 通过 token 消费分享链接（公开入口，返回下载路径）。 */
export function shareResolve(req: ShareResolveReq): Promise<ShareResolveResult> {
  return call<ShareResolveResult>('ShareResolve', { ...req })
}
