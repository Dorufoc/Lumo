// 知识图谱 API（API 文档 7.16 / 完整设计文档 4.19）。
import { call } from '@/api/client'
import type {
  KnowledgeGraph,
  KnowledgeGraphGetReq,
  MasteryExplanation,
  MasteryExplainReq,
  MasterySnapshotListReq,
  MasterySnapshotPage,
} from '@/api/types'

/** 获取工作区知识图谱（可附带当前用户掌握度）。 */
export function knowledgeGraphGet(req: KnowledgeGraphGetReq): Promise<KnowledgeGraph> {
  return call<KnowledgeGraph>('KnowledgeGraphGet', { ...req })
}

/** 分页列出用户掌握度快照。 */
export function masterySnapshotList(req: MasterySnapshotListReq): Promise<MasterySnapshotPage> {
  return call<MasterySnapshotPage>('MasterySnapshotList', { ...req })
}

/** 掌握度解释：公式说明 + 最近 10 次作答明细。 */
export function masteryExplain(req: MasteryExplainReq): Promise<MasteryExplanation> {
  return call<MasteryExplanation>('MasteryExplain', { ...req })
}
