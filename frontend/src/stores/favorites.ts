import { defineStore } from 'pinia'
import { call } from '@/api/client'
import type { Favorite, FavoritePage, ReadLaterAction, ReadLaterItem } from '@/api/types'
import { useSessionStore } from './session'

interface FavoritesState {
  favorites: Favorite[]
  readLater: ReadLaterItem[]
  loading: boolean
}

/**
 * 收藏 / 稍后读 store。
 * 后端契约（API 设计文档 7.8）仅暴露 FavoriteToggle / FavoriteList /
 * ReadLaterAdd / ReadLaterTransition / DocumentSummarize 五个方法，无
 * ReadLaterList；故稍后读队列仅以本次会话内 add/transition 返回结果维护。
 */
export const useFavoritesStore = defineStore('favorites', {
  state: (): FavoritesState => ({
    favorites: [],
    readLater: [],
    loading: false,
  }),
  actions: {
    async load() {
      const session = useSessionStore()
      this.loading = true
      try {
        const favPage = await call<FavoritePage>('FavoriteList', { workspace_id: session.workspaceId, user_id: session.userId })
        this.favorites = favPage?.items ?? []
      } finally {
        this.loading = false
      }
    },
    async toggle(refType: string, refId: string): Promise<Favorite> {
      const session = useSessionStore()
      const fav = await call<Favorite>('FavoriteToggle', {
        workspace_id: session.workspaceId,
        user_id: session.userId,
        ref_type: refType,
        ref_id: refId,
      })
      await this.load()
      return fav
    },
    async addReadLater(documentId: string): Promise<ReadLaterItem> {
      const session = useSessionStore()
      const item = await call<ReadLaterItem>('ReadLaterAdd', {
        workspace_id: session.workspaceId,
        user_id: session.userId,
        document_id: documentId,
      })
      const idx = this.readLater.findIndex((r) => r.id === item.id)
      if (idx >= 0) this.readLater.splice(idx, 1, item)
      else this.readLater.unshift(item)
      return item
    },
    async transitionReadLater(itemId: string, action: ReadLaterAction): Promise<ReadLaterItem> {
      const session = useSessionStore()
      const item = await call<ReadLaterItem>('ReadLaterTransition', {
        workspace_id: session.workspaceId,
        user_id: session.userId,
        item_id: itemId,
        action,
      })
      const idx = this.readLater.findIndex((r) => r.id === item.id)
      if (idx >= 0) this.readLater.splice(idx, 1, item)
      return item
    },
  },
})
