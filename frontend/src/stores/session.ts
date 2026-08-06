import { defineStore } from 'pinia'
import { call } from '@/api/client'
import { t } from '@/i18n'
import type { Settings, UserProfile, Workspace } from '@/api/types'

interface SessionState {
  ready: boolean
  workspaces: Workspace[]
  workspace: Workspace | null
  user: UserProfile | null
  settings: Settings | null
}

const WS_KEY = 'lumo.workspace_id'
const USER_KEY = 'lumo.user_id'

export const useSessionStore = defineStore('session', {
  state: (): SessionState => ({
    ready: false,
    workspaces: [],
    workspace: null,
    user: null,
    settings: null,
  }),
  getters: {
    workspaceId: (s) => s.workspace?.id ?? '',
    userId: (s) => s.user?.id ?? '',
  },
  actions: {
    /** 启动时恢复本地会话（localStorage 中的 workspace/user）。 */
    async bootstrap() {
      try {
        const wsId = localStorage.getItem(WS_KEY)
        const userId = localStorage.getItem(USER_KEY)
        this.workspaces = (await call<Workspace[]>('WorkspaceList')) ?? []
        if (wsId) {
          const ws = this.workspaces.find((w) => w.id === wsId)
          if (ws) {
            await this.activate(ws.id, userId ?? '')
            this.ready = true
            return
          }
        }
        this.ready = true
      } catch {
        this.ready = true
      }
    },
    async activate(workspaceId: string, userId?: string) {
      this.workspace = await call<Workspace>('WorkspaceGet', { workspace_id: workspaceId })
      if (!userId) {
        const list = await call<UserProfile[]>('UserList', { workspace_id: workspaceId }).catch(() => null)
        userId = list?.[0]?.id
      }
      if (!userId) {
        const created = await call<UserProfile>('UserCreate', { workspace_id: workspaceId, display_name: t('common.learner') })
        userId = created.id
      }
      this.user = await call<UserProfile>('UserGetProfile', { workspace_id: workspaceId, user_id: userId })
      this.settings = await call<Settings>('SettingsGet', { workspace_id: workspaceId })
      localStorage.setItem(WS_KEY, workspaceId)
      localStorage.setItem(USER_KEY, userId)
    },
    async createWorkspace(name: string): Promise<Workspace> {
      const ws = await call<Workspace>('WorkspaceCreate', {
        name,
        owner_type: 'local',
        idempotency_key: `ws-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`,
      })
      this.workspaces.push(ws)
      await this.activate(ws.id)
      return ws
    },
    async refreshSettings() {
      if (!this.workspace) return
      this.settings = await call<Settings>('SettingsGet', { workspace_id: this.workspace.id })
    },
  },
})
