import { defineStore } from 'pinia'
import { call } from '@/api/client'
import type {
  Annotation,
  AnnotationCreateReq,
  DeleteResult,
  Flashcard,
  Note,
  NoteKind,
  NotePage,
} from '@/api/types'
import { useSessionStore } from './session'

interface NoteState {
  notes: Note[]
  loading: boolean
}

export const useNoteStore = defineStore('note', {
  state: (): NoteState => ({
    notes: [],
    loading: false,
  }),
  actions: {
    async list(req: { kind?: NoteKind; knowledgeId?: string; tag?: string; keyword?: string; limit?: number }): Promise<NotePage> {
      const session = useSessionStore()
      this.loading = true
      try {
        const page = await call<NotePage>('NoteList', {
          workspace_id: session.workspaceId,
          kind: req.kind,
          knowledge_id: req.knowledgeId,
          tag: req.tag,
          keyword: req.keyword,
          limit: req.limit ?? 50,
        })
        this.notes = page.items
        return page
      } finally {
        this.loading = false
      }
    },
    async create(req: {
      kind?: NoteKind
      title: string
      bodyMd?: string
      knowledgeIds?: string[]
      tags?: string[]
    }): Promise<Note> {
      const session = useSessionStore()
      const note = await call<Note>('NoteCreate', {
        workspace_id: session.workspaceId,
        user_id: session.userId,
        kind: req.kind,
        title: req.title,
        body_md: req.bodyMd,
        knowledge_ids: req.knowledgeIds,
        tags: req.tags,
      })
      this.notes.unshift(note)
      return note
    },
    async update(
      id: string,
      version: number,
      req: { kind?: NoteKind; title: string; bodyMd?: string; knowledgeIds?: string[]; tags?: string[] },
    ): Promise<Note> {
      const session = useSessionStore()
      const note = await call<Note>('NoteUpdate', {
        workspace_id: session.workspaceId,
        note_id: id,
        version,
        kind: req.kind,
        title: req.title,
        body_md: req.bodyMd,
        knowledge_ids: req.knowledgeIds,
        tags: req.tags,
      })
      const idx = this.notes.findIndex((n) => n.id === id)
      if (idx >= 0) this.notes[idx] = note
      return note
    },
    async remove(id: string, version: number): Promise<DeleteResult> {
      const session = useSessionStore()
      const res = await call<DeleteResult>('NoteDelete', {
        workspace_id: session.workspaceId,
        note_id: id,
        version,
      })
      this.notes = this.notes.filter((n) => n.id !== id)
      return res
    },
    async toFlashcard(noteId: string): Promise<Flashcard> {
      const session = useSessionStore()
      return call<Flashcard>('NoteToFlashcard', {
        workspace_id: session.workspaceId,
        user_id: session.userId,
        note_id: noteId,
        idempotency_key: `n2fc-${noteId}-${Date.now()}`,
      })
    },
    async createAnnotation(req: Omit<AnnotationCreateReq, 'workspace_id'>): Promise<Annotation> {
      const session = useSessionStore()
      return call<Annotation>('AnnotationCreate', {
        workspace_id: session.workspaceId,
        ...req,
      })
    },
  },
})
