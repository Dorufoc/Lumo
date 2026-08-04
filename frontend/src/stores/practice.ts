import { defineStore } from 'pinia'
import { call } from '@/api/client'
import type { PracticeResult, PracticeSession, QuestionPage } from '@/api/types'
import { useSessionStore } from './session'

interface PracticeState {
  session: PracticeSession | null
  answers: Record<string, string | string[] | null>
  seq: Record<string, number>
  saving: Record<string, boolean>
  library: QuestionPage | null
}

export const usePracticeStore = defineStore('practice', {
  state: (): PracticeState => ({
    session: null,
    answers: {},
    seq: {},
    saving: {},
    library: null,
  }),
  actions: {
    async loadLibrary() {
      const session = useSessionStore()
      this.library = await call<QuestionPage>('QuestionList', {
        workspace_id: session.workspaceId,
        status: 'published',
        limit: 100,
      })
    },
    async start(questionIds: string[]) {
      const session = useSessionStore()
      const s = await call<PracticeSession>('PracticeStart', {
        workspace_id: session.workspaceId,
        user_id: session.userId,
        mode: 'practice',
        question_ids: questionIds,
        idempotency_key: `ps-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`,
      })
      this.session = s
      this.answers = {}
      this.seq = {}
      // 恢复已有草稿
      for (const d of s.drafts ?? []) {
        this.answers[d.question_version_id] = d.answer as string | string[]
        this.seq[d.question_version_id] = d.client_sequence
      }
      return s
    },
    async resume(sessionId: string) {
      const session = useSessionStore()
      const s = await call<PracticeSession>('PracticeGet', {
        workspace_id: session.workspaceId,
        session_id: sessionId,
      })
      this.session = s
      this.answers = {}
      this.seq = {}
      for (const d of s.drafts ?? []) {
        this.answers[d.question_version_id] = d.answer as string | string[]
        this.seq[d.question_version_id] = d.client_sequence
      }
      return s
    },
    async saveAnswer(questionVersionId: string, answer: string | string[] | null) {
      if (!this.session) return
      const session = useSessionStore()
      this.answers[questionVersionId] = answer
      if (answer === null || answer === '') return
      const nextSeq = (this.seq[questionVersionId] ?? 0) + 1
      this.seq[questionVersionId] = nextSeq
      this.saving[questionVersionId] = true
      try {
        await call('PracticeSaveAnswer', {
          workspace_id: session.workspaceId,
          session_id: this.session.id,
          question_version_id: questionVersionId,
          answer,
          client_sequence: nextSeq,
        })
      } finally {
        this.saving[questionVersionId] = false
      }
    },
    async skip(questionVersionId: string) {
      if (!this.session) return
      const session = useSessionStore()
      this.session = await call<PracticeSession>('PracticeSkipQuestion', {
        workspace_id: session.workspaceId,
        session_id: this.session.id,
        question_version_id: questionVersionId,
      })
    },
    async submit(): Promise<PracticeResult> {
      if (!this.session) throw new Error('没有进行中的练习')
      const session = useSessionStore()
      const result = await call<PracticeResult>('PracticeSubmit', {
        workspace_id: session.workspaceId,
        session_id: this.session.id,
        version: this.session.version,
        idempotency_key: `psub-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`,
      })
      return result
    },
  },
})
