import { defineStore } from 'pinia'
import { calendarEventUpsert, calendarGetMonth, milestoneCreate, milestoneEvaluate } from '@/api/calendar'
import type { ApiException } from '@/api/client'
import type {
  CalendarEntry,
  CalendarEvent,
  CalendarEventUpsertReq,
  LearningGoal,
  Milestone,
  MilestoneCreateReq,
} from '@/api/types'
import { call } from '@/api/client'
import { useSessionStore } from './session'

interface CalendarState {
  /** 当前查看月份（YYYY-MM）。 */
  month: string
  entries: CalendarEntry[]
  loading: boolean
  busy: boolean
  error: string
  /** 个人事件编辑弹窗。 */
  editing: CalendarEvent | null
  /** 目标列表（里程碑创建需选择挂靠目标，GoalList 提供）。 */
  goals: LearningGoal[]
  /** 新建里程碑弹窗。 */
  milestoneDialog: boolean
}

export const useCalendarStore = defineStore('calendar', {
  state: (): CalendarState => ({
    month: '',
    entries: [],
    loading: false,
    busy: false,
    error: '',
    editing: null,
    goals: [],
    milestoneDialog: false,
  }),
  actions: {
    /** 初始化月份为本地当前月。 */
    init() {
      if (!this.month) this.month = this.currentMonth()
    },
    /** 本地当前月（YYYY-MM），与后端 monthOf 约定一致。 */
    currentMonth(): string {
      const d = new Date()
      return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`
    },
    /** 切换月份（±1 个月），刷新月视图。 */
    async shiftMonth(delta: number) {
      const [y, m] = this.month.split('-').map(Number)
      const d = new Date(y, m - 1 + delta, 1)
      this.month = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`
      await this.loadMonth()
    },
    /** 加载月视图投影。 */
    async loadMonth() {
      const session = useSessionStore()
      this.loading = true
      this.error = ''
      try {
        const m = await calendarGetMonth({
          workspace_id: session.workspaceId,
          user_id: session.userId,
          month: this.month,
        })
        this.entries = m.entries ?? []
      } catch (e) {
        this.error = e instanceof Error ? e.message : String(e)
      } finally {
        this.loading = false
      }
    },
    /** 新增/更新个人事件。 */
    async upsertEvent(draft: Omit<CalendarEventUpsertReq, 'workspace_id' | 'user_id' | 'idempotency_key'>) {
      const session = useSessionStore()
      this.busy = true
      this.error = ''
      try {
        await calendarEventUpsert({
          ...draft,
          workspace_id: session.workspaceId,
          user_id: session.userId,
          idempotency_key: `ce-${session.userId}-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`,
        })
        await this.loadMonth()
      } catch (e) {
        this.error = e instanceof Error ? e.message : String(e)
      } finally {
        this.busy = false
      }
    },
    /** 加载目标列表（里程碑挂靠目标）。 */
    async loadGoals() {
      const session = useSessionStore()
      try {
        const goals = await call<LearningGoal[]>('GoalList', {
          workspace_id: session.workspaceId,
          user_id: session.userId,
        })
        this.goals = goals ?? []
      } catch (e) {
        this.error = e instanceof Error ? e.message : String(e)
      }
    },
    /** 创建里程碑（后端无列表接口，返回新建结果供前端展示）。 */
    async createMilestone(draft: Omit<MilestoneCreateReq, 'workspace_id' | 'user_id' | 'idempotency_key'>): Promise<Milestone> {
      const session = useSessionStore()
      this.busy = true
      this.error = ''
      try {
        const m = await milestoneCreate({
          ...draft,
          workspace_id: session.workspaceId,
          user_id: session.userId,
          idempotency_key: `ms-${session.userId}-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`,
        })
        this.milestoneDialog = false
        return m
      } catch (e) {
        if (e instanceof Error) {
          const err = e as ApiException
          this.error = err.localizedMessage ? err.localizedMessage() : err.message
        } else {
          this.error = String(e)
        }
        throw e
      } finally {
        this.busy = false
      }
    },
    /** 判定里程碑（服务端按验收条件计算）。 */
    async evaluateMilestone(milestoneId: string): Promise<Milestone> {
      const session = useSessionStore()
      this.busy = true
      this.error = ''
      try {
        return await milestoneEvaluate({
          workspace_id: session.workspaceId,
          user_id: session.userId,
          milestone_id: milestoneId,
        })
      } catch (e) {
        this.error = e instanceof Error ? e.message : String(e)
        throw e
      } finally {
        this.busy = false
      }
    },
    /** 按日期汇总条目（用于单元格渲染）。 */
    entriesByDate(date: string): CalendarEntry[] {
      return this.entries.filter((e) => e.event_date === date)
    },
    /** 刷新所有日历相关数据。 */
    async refresh() {
      await this.loadMonth()
      await this.loadGoals()
    },
  },
})
