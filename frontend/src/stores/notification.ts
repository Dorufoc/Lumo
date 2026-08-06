import { defineStore } from 'pinia'
import { call } from '@/api/client'
import type { MarkReadResult, Notification, NotificationPage, Reminder, ReminderKind, TestResult } from '@/api/types'
import { useSessionStore } from './session'

interface NotificationState {
  items: Notification[]
  nextCursor: string
  hasMore: boolean
  unreadOnly: boolean
  loading: boolean
  busy: boolean
}

export const useNotificationStore = defineStore('notification', {
  state: (): NotificationState => ({
    items: [],
    nextCursor: '',
    hasMore: false,
    unreadOnly: false,
    loading: false,
    busy: false,
  }),
  actions: {
    /** 拉取第一页（unread_only 切换后重载）。 */
    async load(limit = 50) {
      const session = useSessionStore()
      this.loading = true
      try {
        const page = await call<NotificationPage>('NotificationList', {
          workspace_id: session.workspaceId,
          user_id: session.userId,
          unread_only: this.unreadOnly,
          limit,
        })
        this.items = page.items
        this.nextCursor = page.next_cursor
        this.hasMore = page.has_more
      } finally {
        this.loading = false
      }
    },
    /** 游标加载下一页（追加去重）。 */
    async loadMore() {
      if (!this.nextCursor || !this.hasMore || this.busy) return
      const session = useSessionStore()
      this.busy = true
      try {
        const page = await call<NotificationPage>('NotificationList', {
          workspace_id: session.workspaceId,
          user_id: session.userId,
          unread_only: this.unreadOnly,
          cursor: this.nextCursor,
          limit: 50,
        })
        const seen = new Set(this.items.map((i) => i.id))
        for (const it of page.items) {
          if (!seen.has(it.id)) this.items.push(it)
        }
        this.nextCursor = page.next_cursor
        this.hasMore = page.has_more
      } finally {
        this.busy = false
      }
    },
    /** 标记全部未读为已读，返回更新行数。 */
    async markAllRead(): Promise<number> {
      const session = useSessionStore()
      const unread = this.items.filter((i) => !i.read_at)
      if (unread.length === 0) return 0
      const res = await call<MarkReadResult>('NotificationMarkRead', {
        workspace_id: session.workspaceId,
        user_id: session.userId,
        ids: unread.map((i) => i.id),
      })
      await this.load()
      return res.updated
    },
    /** 标记单条已读（本地即时更新 read_at，不整页重载）。 */
    async markRead(id: string) {
      const session = useSessionStore()
      await call<MarkReadResult>('NotificationMarkRead', {
        workspace_id: session.workspaceId,
        user_id: session.userId,
        ids: [id],
      })
      const it = this.items.find((x) => x.id === id)
      if (it) it.read_at = new Date().toISOString()
    },
    /** 新增/更新提醒（(user_id, kind) upsert 语义）。 */
    async upsert(kind: ReminderKind, ruleJson: string, enabled: boolean): Promise<Reminder> {
      const session = useSessionStore()
      return call<Reminder>('ReminderUpsert', {
        workspace_id: session.workspaceId,
        user_id: session.userId,
        kind,
        rule_json: ruleJson,
        enabled,
      })
    },
    /** 测试发送：立即产生一条通知（确定性测试钩子）。 */
    async testSend(kind: ReminderKind): Promise<TestResult> {
      const session = useSessionStore()
      return call<TestResult>('ReminderTestSend', {
        workspace_id: session.workspaceId,
        user_id: session.userId,
        kind,
      })
    },
  },
})
