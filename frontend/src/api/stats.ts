// 教师统计 API（API 文档 7.11 / 完整设计文档 4.22 C6）。
import { call } from '@/api/client'
import type { ClassStats, ClassStatsReq } from '@/api/types'

/** 班级统计（教师/班级创建者；只读聚合接口）。 */
export function classStats(req: ClassStatsReq): Promise<ClassStats> {
  return call<ClassStats>('ClassStats', { ...req })
}
