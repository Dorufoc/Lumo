import { defineStore } from 'pinia'
import { call } from '@/api/client'
import { t } from '@/i18n'
import type { Exam, ExamPaper, ExamResult } from '@/api/types'
import { useSessionStore } from './session'

interface ExamState {
  papers: ExamPaper[]
  active: Exam | null
  answers: Record<string, string | string[] | null>
  seq: Record<string, number>
  saving: Record<string, boolean>
}

export const useExamStore = defineStore('exam', {
  state: (): ExamState => ({
    papers: [],
    active: null,
    answers: {},
    seq: {},
    saving: {},
  }),
  actions: {
    async listPapers() {
      const session = useSessionStore()
      this.papers = await call<ExamPaper[]>('ExamPaperList', {
        workspace_id: session.workspaceId,
        limit: 100,
      })
    },
    async createPaper(title: string, configJson: Record<string, unknown>): Promise<ExamPaper> {
      const session = useSessionStore()
      const paper = await call<ExamPaper>('ExamPaperCreate', {
        workspace_id: session.workspaceId,
        user_id: session.userId,
        title,
        config_json: configJson,
        idempotency_key: `epc-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`,
      })
      await this.listPapers()
      return paper
    },
    async autoGenerate(title: string, config: Record<string, unknown>): Promise<ExamPaper> {
      const session = useSessionStore()
      const paper = await call<ExamPaper>('ExamPaperAutoGenerate', {
        workspace_id: session.workspaceId,
        user_id: session.userId,
        title,
        config,
        idempotency_key: `epg-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`,
      })
      await this.listPapers()
      return paper
    },
    async publishPaper(paperId: string, version: number): Promise<ExamPaper> {
      const session = useSessionStore()
      const paper = await call<ExamPaper>('ExamPaperPublish', {
        workspace_id: session.workspaceId,
        paper_id: paperId,
        version,
      })
      await this.listPapers()
      return paper
    },
    async start(paperId: string): Promise<Exam> {
      const session = useSessionStore()
      const exam = await call<Exam>('ExamStart', {
        workspace_id: session.workspaceId,
        user_id: session.userId,
        paper_id: paperId,
        idempotency_key: `es-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`,
      })
      this.active = exam
      this.answers = {}
      this.seq = {}
      for (const d of exam.questions) this.answers[d.question_version_id] = null
      return exam
    },
    async saveAnswer(questionVersionId: string, answer: string | string[] | null) {
      if (!this.active) return
      const session = useSessionStore()
      this.answers[questionVersionId] = answer
      if (answer === null || answer === '') return
      const nextSeq = (this.seq[questionVersionId] ?? 0) + 1
      this.seq[questionVersionId] = nextSeq
      this.saving[questionVersionId] = true
      try {
        await call('PracticeSaveAnswer', {
          workspace_id: session.workspaceId,
          session_id: this.active.id,
          question_version_id: questionVersionId,
          answer,
          client_sequence: nextSeq,
        })
      } finally {
        this.saving[questionVersionId] = false
      }
    },
    async autoSubmit(): Promise<ExamResult> {
      if (!this.active) throw new Error(t('error.noSession'))
      const session = useSessionStore()
      return call<ExamResult>('ExamAutoSubmit', {
        workspace_id: session.workspaceId,
        exam_id: this.active.id,
      })
    },
    async getResult(): Promise<ExamResult> {
      if (!this.active) throw new Error(t('error.noSession'))
      const session = useSessionStore()
      return call<ExamResult>('ExamGetResult', {
        workspace_id: session.workspaceId,
        exam_id: this.active.id,
      })
    },
  },
})
