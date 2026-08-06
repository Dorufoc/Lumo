// 日历与里程碑 API（API 文档 7.9 / 完整设计文档 4.16）。
import { call } from '@/api/client'
import type {
  CalendarEvent,
  CalendarEventUpsertReq,
  CalendarGetMonthReq,
  CalendarMonth,
  Milestone,
  MilestoneCreateReq,
  MilestoneEvaluateReq,
} from '@/api/types'

/** 月度日历投影。 */
export function calendarGetMonth(req: CalendarGetMonthReq): Promise<CalendarMonth> {
  return call<CalendarMonth>('CalendarGetMonth', { ...req })
}

/** 个人日历事件新增/更新。 */
export function calendarEventUpsert(req: CalendarEventUpsertReq): Promise<CalendarEvent> {
  return call<CalendarEvent>('CalendarEventUpsert', { ...req })
}

/** 目标里程碑创建。 */
export function milestoneCreate(req: MilestoneCreateReq): Promise<Milestone> {
  return call<Milestone>('MilestoneCreate', { ...req })
}

/** 里程碑判定（服务端按验收条件计算）。 */
export function milestoneEvaluate(req: MilestoneEvaluateReq): Promise<Milestone> {
  return call<Milestone>('MilestoneEvaluate', { ...req })
}
