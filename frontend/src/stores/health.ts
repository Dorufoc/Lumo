import { defineStore } from 'pinia'
import { healthSettingsUpdate, healthStatsGet } from '@/api/health'
import { localizedMessageOf } from '@/api/client'
import type { HealthSettings, HealthSettingsUpdateReq, HealthStats } from '@/api/types'
import { useSessionStore } from './session'

interface HealthState {
  settings: HealthSettings | null
  stats: HealthStats | null
  loading: boolean
  saving: boolean
  error: string
}

export const useHealthStore = defineStore('health', {
  state: (): HealthState => ({
    settings: null,
    stats: null,
    loading: false,
    saving: false,
    error: '',
  }),
  actions: {
    /** 拉取健康统计（stats_enabled=false 时返回零值而非报错）。 */
    async loadStats() {
      const session = useSessionStore()
      this.loading = true
      this.error = ''
      try {
        this.stats = await healthStatsGet({
          workspace_id: session.workspaceId,
          user_id: session.userId,
        })
      } catch (e) {
        this.error = localizedMessageOf(e)
      } finally {
        this.loading = false
      }
    },
    /** 保存健康设置；开启统计时拉取最新统计。 */
    async update(draft: Omit<HealthSettingsUpdateReq, 'workspace_id' | 'user_id'>) {
      const session = useSessionStore()
      this.saving = true
      this.error = ''
      try {
        this.settings = await healthSettingsUpdate({
          ...draft,
          workspace_id: session.workspaceId,
          user_id: session.userId,
        })
        if (draft.stats_enabled) await this.loadStats()
      } catch (e) {
        this.error = localizedMessageOf(e)
        throw e
      } finally {
        this.saving = false
      }
    },
    /** 全量刷新。 */
    async refresh() {
      await this.loadStats()
    },
  },
})
