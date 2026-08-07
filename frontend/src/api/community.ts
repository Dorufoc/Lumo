// 本地内容社区 API（完整设计文档 4.20 社区 / Todo 35）。
// 帖子存本地 JSON 文件（<DataDir>/community/<post_id>.json）；点赞计数维护在 likes 字段。
import { call } from '@/api/client'
import type {
  CommunityPost,
  CommunityPostCreateReq,
  CommunityPostGetReq,
  CommunityPostLikeReq,
  CommunityPostLikeResp,
  CommunityPostListReq,
} from '@/api/types'

/** 发布帖子（强制安全扫描，不 clean 拒绝发布）。 */
export function communityPostCreate(req: CommunityPostCreateReq): Promise<CommunityPost> {
  return call<CommunityPost>('CommunityPostCreate', { ...req })
}

/** 帖子列表（按 created_at 倒序）。 */
export function communityPostList(req: CommunityPostListReq): Promise<CommunityPost[]> {
  return call<CommunityPost[]>('CommunityPostList', { ...req })
}

/** 帖子详情（不存在 → NOT_FOUND）。 */
export function communityPostGet(req: CommunityPostGetReq): Promise<CommunityPost> {
  return call<CommunityPost>('CommunityPostGet', { ...req })
}

/** 帖子点赞（计数 +1，返回新计数；不存在 → NOT_FOUND）。 */
export function communityPostLike(req: CommunityPostLikeReq): Promise<CommunityPostLikeResp> {
  return call<CommunityPostLikeResp>('CommunityPostLike', { ...req })
}
