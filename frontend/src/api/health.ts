// 健康与专注辅助 API（API 设计文档 7.16 / 完整设计文档 4.17）。
import { call } from '@/api/client'
import type { HealthSettings, HealthSettingsUpdateReq, HealthStats, HealthStatsGetReq } from '@/api/types'

/** 健康设置新增/更新。 */
export function healthSettingsUpdate(req: HealthSettingsUpdateReq): Promise<HealthSettings> {
  return call<HealthSettings>('HealthSettingsUpdate', { ...req })
}

/** 健康统计查询。 */
export function healthStatsGet(req: HealthStatsGetReq): Promise<HealthStats> {
  return call<HealthStats>('HealthStatsGet', { ...req })
}
