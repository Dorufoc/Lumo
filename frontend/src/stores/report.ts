import { defineStore } from 'pinia'
import { call } from '@/api/client'
import type {
  ExportResult,
  Insight,
  InsightDimension,
  Report,
  ReportPage,
  ReportPeriod,
} from '@/api/types'
import { useSessionStore } from './session'

interface ReportState {
  items: Report[]
  latest: Report | null
  insight: Insight | null
  nextCursor: string
  hasMore: boolean
  loading: boolean
  generating: boolean
  busy: boolean
}

function newIdempotencyKey(prefix: string): string {
  return `${prefix}-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`
}

export const useReportStore = defineStore('report', {
  state: (): ReportState => ({
    items: [],
    latest: null,
    insight: null,
    nextCursor: '',
    hasMore: false,
    loading: false,
    generating: false,
    busy: false,
  }),
  actions: {
    /** 生成学习报告（服务端同步聚合，完成后 report:ready 事件进通知栏）。 */
    async generate(req: {
      period: ReportPeriod
      period_start: string
      period_end: string
    }): Promise<Report> {
      const session = useSessionStore()
      this.generating = true
      try {
        const rep = await call<Report>('ReportGenerate', {
          workspace_id: session.workspaceId,
          user_id: session.userId,
          period: req.period,
          period_start: req.period_start,
          period_end: req.period_end,
          idempotency_key: newIdempotencyKey('rg'),
        })
        this.latest = rep
        return rep
      } finally {
        this.generating = false
      }
    },
    /** 加载第一页（可选按周期过滤）。 */
    async load(period?: ReportPeriod, limit = 50) {
      const session = useSessionStore()
      this.loading = true
      try {
        const page = await call<ReportPage>('ReportList', {
          workspace_id: session.workspaceId,
          user_id: session.userId,
          period: period ?? '',
          limit,
        })
        this.items = page.items
        this.nextCursor = page.next_cursor
        this.hasMore = page.has_more
        if (page.items.length > 0 && !this.latest) this.latest = page.items[0]
      } finally {
        this.loading = false
      }
    },
    /** 游标加载下一页。 */
    async loadMore(period?: ReportPeriod) {
      if (!this.nextCursor || !this.hasMore || this.busy) return
      const session = useSessionStore()
      this.busy = true
      try {
        const page = await call<ReportPage>('ReportList', {
          workspace_id: session.workspaceId,
          user_id: session.userId,
          period: period ?? '',
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
    /** 导出报告（pdf|json），返回下载元信息（经 GET /api/v1/files 拉取）。 */
    async exportReport(id: string, format: 'pdf' | 'json'): Promise<ExportResult> {
      const session = useSessionStore()
      return call<ExportResult>('ReportExport', {
        workspace_id: session.workspaceId,
        report_id: id,
        format,
      })
    },
    /** 拉取洞察（knowledge|time|trend）。 */
    async loadInsight(dimension: InsightDimension) {
      const session = useSessionStore()
      this.insight = await call<Insight>('InsightGet', {
        workspace_id: session.workspaceId,
        user_id: session.userId,
        dimension,
      })
      return this.insight
    },
  },
})
