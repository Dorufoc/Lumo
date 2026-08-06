import { defineStore } from 'pinia'
import { call, ApiException } from '@/api/client'
import type { TimerSession, TimerStats } from '@/api/types'
import { useSessionStore } from './session'

interface StaleTimer {
  session_id: string
  started_at: string | null
}

interface FocusState {
  active: TimerSession | null
  stats: TimerStats | null
  loading: boolean
  busy: boolean
  /** 服务端仍有一个活动会话（INVALID_STATE 详情带回），供前端"结束上一个"恢复。 */
  stale: StaleTimer | null
}

/** 本地持久化键：活动计时会话（按用户隔离；浏览器关闭后恢复展示）。 */
function activeKey(userId: string): string {
  return `lumo.focus.active.${userId}`
}

export const useFocusStore = defineStore('focus', {
  state: (): FocusState => ({
    active: null,
    stats: null,
    loading: false,
    busy: false,
    stale: null,
  }),
  actions: {
    async load() {
      const session = useSessionStore()
      this.loading = true
      try {
        const stats = await call<TimerStats>('TimerStats', {
          workspace_id: session.workspaceId,
          user_id: session.userId,
        })
        this.stats = stats
      } finally {
        this.loading = false
      }
    },
    /** 恢复本地暂存的活动会话（仅当日/未结束有效）。 */
    restoreActive() {
      const session = useSessionStore()
      try {
        const raw = localStorage.getItem(activeKey(session.userId))
        if (raw) {
          const a = JSON.parse(raw) as TimerSession
          if (a && a.id && !a.ended_at) this.active = a
        }
      } catch {
        this.active = null
      }
    },
    /** 开始专注：单活动约束由服务端强制（INVALID_STATE 时带回活动会话详情）。 */
    async start(mode: 'pomodoro' | 'free', plannedMinutes: number): Promise<TimerSession> {
      const session = useSessionStore()
      this.busy = true
      this.stale = null
      try {
        const t = await call<TimerSession>('TimerStart', {
          workspace_id: session.workspaceId,
          user_id: session.userId,
          mode,
          planned_minutes: plannedMinutes,
          idempotency_key: `fm-${session.userId}-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`,
        })
        this.active = t
        try {
          localStorage.setItem(activeKey(session.userId), JSON.stringify(t))
        } catch {
          // 持久化失败不阻断计时
        }
        return t
      } catch (e) {
        if (e instanceof ApiException && e.code === 'INVALID_STATE' && e.details && typeof e.details.session_id === 'string') {
          this.stale = {
            session_id: e.details.session_id,
            started_at: typeof e.details.started_at === 'string' ? e.details.started_at : null,
          }
        }
        throw e
      } finally {
        this.busy = false
      }
    },
    /** 结束当前活动会话（interrupt_reason 为空串 = 正常完成）。 */
    async end(interruptReason = ''): Promise<TimerSession> {
      const session = useSessionStore()
      if (!this.active) throw new Error('no active session')
      this.busy = true
      try {
        const t = await call<TimerSession>('TimerEnd', {
          workspace_id: session.workspaceId,
          user_id: session.userId,
          session_id: this.active.id,
          interrupt_reason: interruptReason,
        })
        this.clearActive()
        await this.load()
        return t
      } finally {
        this.busy = false
      }
    },
    /** 结束服务端遗留的活动会话（惰性恢复入口），然后刷新统计。 */
    async endStale(sessionId: string, interruptReason = ''): Promise<void> {
      const session = useSessionStore()
      this.busy = true
      try {
        await call<TimerSession>('TimerEnd', {
          workspace_id: session.workspaceId,
          user_id: session.userId,
          session_id: sessionId,
          interrupt_reason: interruptReason,
        })
        this.stale = null
        await this.load()
      } finally {
        this.busy = false
      }
    },
    clearActive() {
      const session = useSessionStore()
      this.active = null
      try {
        localStorage.removeItem(activeKey(session.userId))
      } catch {
        // 忽略清除失败
      }
    },
  },
})
