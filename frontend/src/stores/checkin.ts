import { defineStore } from 'pinia'
import { call } from '@/api/client'
import type { AchievementView, Checkin, Streak } from '@/api/types'
import { useSessionStore } from './session'

interface CheckinState {
  streak: Streak | null
  achievements: AchievementView[]
  today: Checkin | null
  loading: boolean
}

/** 本地持久化键：今日打卡记录（键含日期，跨天自动失效）。 */
function todayKey(userId: string, date: string): string {
  return `lumo.checkin.today.${userId}.${date}`
}

export const useCheckinStore = defineStore('checkin', {
  state: (): CheckinState => ({
    streak: null,
    achievements: [],
    today: null,
    loading: false,
  }),
  actions: {
    async load() {
      const session = useSessionStore()
      this.loading = true
      try {
        const [streak, achievements] = await Promise.all([
          call<Streak>('StreakGet', { workspace_id: session.workspaceId, user_id: session.userId }),
          call<AchievementView[]>('AchievementList', { workspace_id: session.workspaceId, user_id: session.userId }),
        ])
        this.streak = streak
        this.achievements = achievements
      } finally {
        this.loading = false
      }
    },
    /** 恢复本地"今日已打卡"状态（仅当日有效）。 */
    restoreToday(date: string) {
      const session = useSessionStore()
      try {
        const raw = localStorage.getItem(todayKey(session.userId, date))
        if (raw) this.today = JSON.parse(raw) as Checkin
      } catch {
        this.today = null
      }
    },
    async checkin(minutes = 0): Promise<Checkin> {
      const session = useSessionStore()
      const c = await call<Checkin>('CheckinCreate', {
        workspace_id: session.workspaceId,
        user_id: session.userId,
        minutes,
        idempotency_key: `chk-${session.userId}-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`,
      })
      this.today = c
      try {
        localStorage.setItem(todayKey(session.userId, c.date), JSON.stringify(c))
      } catch {
        // 持久化失败不阻断打卡
      }
      await this.load()
      return c
    },
    async makeup(date: string, minutes = 0): Promise<Checkin> {
      const session = useSessionStore()
      const c = await call<Checkin>('CheckinMakeup', {
        workspace_id: session.workspaceId,
        user_id: session.userId,
        date,
        minutes,
        idempotency_key: `ckm-${session.userId}-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`,
      })
      await this.load()
      return c
    },
  },
})
