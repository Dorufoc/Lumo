import { defineStore } from 'pinia'
import { call } from '@/api/client'
import type {
  BatchResult,
  ExportResult,
  Flashcard,
  FlashcardImportBatch,
} from '@/api/types'
import { useSessionStore } from './session'

interface FlashcardState {
  due: Flashcard[]
  loading: boolean
}

export const useFlashcardStore = defineStore('flashcard', {
  state: (): FlashcardState => ({
    due: [],
    loading: false,
  }),
  actions: {
    async loadDue(limit = 50) {
      const session = useSessionStore()
      this.loading = true
      try {
        this.due = await call<Flashcard[]>('FlashcardListDue', {
          workspace_id: session.workspaceId,
          user_id: session.userId,
          limit,
        })
      } finally {
        this.loading = false
      }
    },
    async create(req: {
      source?: string
      source_ref?: string
      front: string
      back: string
      card_type?: string
    }): Promise<Flashcard> {
      const session = useSessionStore()
      return call<Flashcard>('FlashcardCreate', {
        workspace_id: session.workspaceId,
        user_id: session.userId,
        source: req.source ?? 'manual',
        source_ref: req.source_ref,
        front: req.front,
        back: req.back,
        card_type: req.card_type,
      })
    },
    async generate(sourceRef: string): Promise<Flashcard[]> {
      const session = useSessionStore()
      return call<Flashcard[]>('FlashcardGenerate', {
        workspace_id: session.workspaceId,
        user_id: session.userId,
        source_ref: sourceRef,
        idempotency_key: `fcg-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`,
      })
    },
    async review(id: string, rating: 'again' | 'hard' | 'good'): Promise<Flashcard> {
      const session = useSessionStore()
      return call<Flashcard>('FlashcardReview', {
        workspace_id: session.workspaceId,
        flashcard_id: id,
        rating,
        idempotency_key: `fcr-${id}-${Date.now()}`,
      })
    },
    async batch(action: 'archive' | 'delete' | 'reset', ids: string[]): Promise<BatchResult> {
      const session = useSessionStore()
      return call<BatchResult>('FlashcardBatch', {
        workspace_id: session.workspaceId,
        action,
        ids,
      })
    },
    async importCsv(filePath: string): Promise<FlashcardImportBatch> {
      const session = useSessionStore()
      return call<FlashcardImportBatch>('FlashcardImportCsv', {
        workspace_id: session.workspaceId,
        user_id: session.userId,
        file_path: filePath,
        idempotency_key: `fci-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`,
      })
    },
    async exportAnki(): Promise<ExportResult> {
      const session = useSessionStore()
      return call<ExportResult>('FlashcardExportAnki', {
        workspace_id: session.workspaceId,
        idempotency_key: `fce-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`,
      })
    },
  },
})
