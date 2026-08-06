<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { call, localizeApiError, localizedMessageOf, openEventStream, upload, type Citation } from '@/api/client'
import type { Document, DocumentPage, Favorite, ImportBatch, ImportPreview, KnowledgeNode, Question, QuestionPage, ReadLaterAction, ReadLaterItem } from '@/api/types'
import { useFavoritesStore } from '@/stores/favorites'
import { useI18nStore } from '@/stores/i18n'
import { useSessionStore } from '@/stores/session'

const session = useSessionStore()
const i18n = useI18nStore()
const favStore = useFavoritesStore()

const tab = ref<'questions' | 'import' | 'knowledge' | 'documents' | 'favorites'>('questions')
const loading = ref(true)
const error = ref('')

// ---------- 题目列表 ----------
const questions = ref<Question[]>([])
async function loadQuestions() {
  loading.value = true
  error.value = ''
  try {
    const page = await call<QuestionPage>('QuestionList', {
      workspace_id: session.workspaceId,
      limit: 100,
    })
    questions.value = page?.items ?? []
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    loading.value = false
  }
}

async function transition(q: Question, action: string) {
  try {
    await call<Question>('QuestionTransition', {
      workspace_id: session.workspaceId,
      question_id: q.id,
      version: q.version,
      action,
    })
    await loadQuestions()
  } catch (e) {
    error.value = localizedMessageOf(e)
  }
}

// ---------- 导入 ----------
const file = ref<File | null>(null)
const importing = ref(false)
const preview = ref<ImportPreview | null>(null)
const batch = ref<ImportBatch | null>(null)

function onFile(e: Event) {
  const input = e.target as HTMLInputElement
  file.value = input.files?.[0] ?? null
  preview.value = null
  batch.value = null
}

async function doPreflight() {
  if (!file.value) return
  importing.value = true
  error.value = ''
  try {
    const up = await upload<{ path: string; file_name: string }>('LibraryUpload', file.value)
    const format = file.value.name.endsWith('.json') ? 'json' : file.value.name.endsWith('.txt') ? 'text' : 'markdown'
    preview.value = await call<ImportPreview>('LibraryPreflightImport', {
      workspace_id: session.workspaceId,
      file_path: up.path,
      format,
      idempotency_key: `imp-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`,
    })
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    importing.value = false
  }
}

async function doCommit() {
  if (!preview.value) return
  importing.value = true
  error.value = ''
  try {
    batch.value = await call<ImportBatch>('LibraryCommitImport', {
      workspace_id: session.workspaceId,
      batch_id: preview.value.batch_id,
      idempotency_key: `impc-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`,
    })
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    importing.value = false
  }
}

// ---------- 知识点 ----------
const tree = ref<KnowledgeNode[]>([])
const newKnow = ref('')
async function loadTree() {
  tree.value = await call<KnowledgeNode[]>('KnowledgeTreeGet', { workspace_id: session.workspaceId })
}
async function createKnow() {
  if (!newKnow.value.trim()) return
  try {
    await call<KnowledgeNode>('KnowledgeCreate', {
      workspace_id: session.workspaceId,
      name: newKnow.value.trim(),
    })
    newKnow.value = ''
    await loadTree()
  } catch (e) {
    error.value = localizedMessageOf(e)
  }
}

// ---------- 资料与 RAG ----------
const docs = ref<Document[]>([])
const docFile = ref<File | null>(null)
const docImporting = ref(false)
const ragQuestion = ref('')
const ragAnswer = ref('')
const ragStreaming = ref(false)
const ragCitations = ref<Citation[]>([])

async function loadDocs() {
  const page = await call<DocumentPage>('DocumentList', { workspace_id: session.workspaceId })
  docs.value = page?.items ?? []
}

function onDocFile(e: Event) {
  const input = e.target as HTMLInputElement
  docFile.value = input.files?.[0] ?? null
}

async function doDocImport() {
  if (!docFile.value) return
  docImporting.value = true
  error.value = ''
  try {
    const up = await upload<{ path: string; file_name: string }>('LibraryUpload', docFile.value)
    const doc = await call<Document>('DocumentImport', {
      workspace_id: session.workspaceId,
      file_path: up.path,
      idempotency_key: `doc-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`,
    })
    docFile.value = null
    info.value = doc.status === 'indexed' ? i18n.t('library.docImported', { name: doc.file_name }) : i18n.t('library.docStatus', { status: doc.status })
    await loadDocs()
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    docImporting.value = false
  }
}

async function deleteDoc(d: Document) {
  try {
    await call('DocumentDelete', {
      workspace_id: session.workspaceId,
      document_id: d.id,
      version: d.version,
    })
    await loadDocs()
  } catch (e) {
    error.value = localizedMessageOf(e)
  }
}

async function ragAsk() {
  const q = ragQuestion.value.trim()
  if (!q || ragStreaming.value) return
  ragStreaming.value = true
  ragAnswer.value = ''
  ragCitations.value = []
  error.value = ''
  try {
    const req = await call<{ request_id: string; session_id: string }>('RAGAsk', {
      workspace_id: session.workspaceId,
      user_id: session.userId,
      question: q,
    })
    let text = ''
    openEventStream(req.request_id, req.session_id, {
      onDelta: (delta, citations) => {
        text += delta
        ragAnswer.value = text
        if (citations.length > 0) ragCitations.value = citations
      },
      onCompleted: () => {
        ragStreaming.value = false
      },
      onError: (err) => {
        ragStreaming.value = false
        error.value = localizeApiError(err)
      },
    })
  } catch (e) {
    ragStreaming.value = false
    error.value = localizedMessageOf(e)
  }
}

const info = ref('')

onMounted(() => {
  void loadQuestions()
  void loadTree()
  void loadDocs()
  void favStore.load()
})

function typeKey(t: string) {
  return {
    single_choice: 'question.typeSingle',
    multiple_choice: 'question.typeMultiple',
    fill_blank: 'question.typeFill',
    short_answer: 'question.typeShort',
    code: 'question.typeCode',
  }[t] ?? t
}

// ---------- 收藏 / 稍后读 ----------
function refTypeKey(t: string) {
  return {
    question: 'library.refTypeQuestion',
    document: 'library.refTypeDocument',
    agent_message: 'library.refTypeAgentMessage',
    note: 'library.refTypeNote',
  }[t] ?? t
}

function readLaterStatusKey(s: string) {
  return {
    queued: 'library.readLaterQueued',
    read: 'library.readLaterRead',
    skipped: 'library.readLaterSkipped',
  }[s] ?? s
}

async function unsave(fav: Favorite) {
  try {
    await favStore.toggle(fav.ref_type, fav.ref_id)
  } catch (e) {
    error.value = localizedMessageOf(e)
  }
}

async function readLaterAction(item: ReadLaterItem, action: ReadLaterAction) {
  try {
    await favStore.transitionReadLater(item.id, action)
  } catch (e) {
    error.value = localizedMessageOf(e)
  }
}

async function addReadLater(d: Document) {
  try {
    await favStore.addReadLater(d.id)
    info.value = i18n.t('library.readLaterAdded', { name: d.file_name })
  } catch (e) {
    error.value = localizedMessageOf(e)
  }
}

function docName(id: string) {
  return docs.value.find((d) => d.id === id)?.file_name ?? id.slice(0, 8)
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('library.title') }}</h1>
        <div class="subtitle">{{ $t('library.subtitle') }}</div>
      </div>
    </div>

    <div v-if="error" class="error-banner">{{ error }}</div>

    <div class="tabs">
      <div class="tab" :class="{ active: tab === 'questions' }" @click="tab = 'questions'">{{ $t('library.tabQuestions', { count: questions.length }) }}</div>
      <div class="tab" :class="{ active: tab === 'import' }" @click="tab = 'import'">{{ $t('library.tabImport') }}</div>
      <div class="tab" :class="{ active: tab === 'knowledge' }" @click="tab = 'knowledge'">{{ $t('library.tabKnowledge') }}</div>
      <div class="tab" :class="{ active: tab === 'documents' }" @click="tab = 'documents'">{{ $t('library.tabDocuments') }}</div>
      <div class="tab" :class="{ active: tab === 'favorites' }" @click="tab = 'favorites'">{{ $t('library.tabFavorites') }}</div>
    </div>

    <!-- 题目列表 -->
    <template v-if="tab === 'questions'">
      <div v-if="loading" class="loading"><div class="spinner"></div></div>
      <div v-else-if="questions.length === 0" class="empty">
        <div class="empty-icon">📚</div>
        <p>{{ $t('library.emptyQuestions') }}</p>
      </div>
      <div v-else class="card">
        <table class="table">
          <thead>
            <tr><th>{{ $t('library.colStem') }}</th><th>{{ $t('library.colType') }}</th><th>{{ $t('library.colStatus') }}</th><th>{{ $t('library.colSource') }}</th><th>{{ $t('library.colActions') }}</th></tr>
          </thead>
          <tbody>
            <tr v-for="q in questions" :key="q.id">
              <td class="grow">{{ q.current_version?.payload.stem?.slice(0, 70) }}</td>
              <td><span class="badge">{{ $t(typeKey(q.type)) }}</span></td>
              <td>
                <span class="badge" :class="{
                  'badge-success': q.status === 'published',
                  'badge-warning': q.status === 'reviewed',
                  'badge-offline': q.status === 'archived',
                }">{{ q.status }}</span>
              </td>
              <td class="text-muted">{{ q.source }}</td>
              <td>
                <div class="flex gap-2">
                  <button v-if="q.status === 'draft'" class="btn btn-sm" @click="transition(q, 'review')">{{ $t('library.actionReview') }}</button>
                  <button v-if="q.status === 'reviewed'" class="btn btn-sm btn-success" @click="transition(q, 'publish')">{{ $t('library.actionPublish') }}</button>
                  <button v-if="!['published', 'archived'].includes(q.status)" class="btn btn-sm btn-ghost" @click="transition(q, 'archive')">{{ $t('library.actionArchive') }}</button>
                  <button v-if="q.status === 'published'" class="btn btn-sm btn-ghost" @click="transition(q, 'archive')">{{ $t('library.actionArchive') }}</button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <!-- 导入 -->
    <template v-if="tab === 'import'">
      <div class="card">
        <h3>{{ $t('library.importTitle') }}</h3>
        <p class="text-secondary mb-3" v-html="$t('library.importHelp')"></p>
        <div class="flex gap-3">
          <input type="file" accept=".md,.markdown,.json,.txt,.text" class="input" style="max-width: 400px" @change="onFile" />
          <button class="btn btn-primary" :disabled="!file || importing" @click="doPreflight">
            {{ importing ? $t('library.parsing') : $t('library.parseFile') }}
          </button>
        </div>
        <div v-if="file" class="hint mt-2">{{ file.name }} · {{ (file.size / 1024).toFixed(1) }} KB</div>
      </div>

      <div v-if="preview" class="card">
        <div class="flex-between mb-3">
          <h3 style="margin: 0">{{ $t('library.parseResult', { name: preview.file_name }) }}</h3>
          <span class="badge" :class="preview.error_count > 0 ? 'badge-warning' : 'badge-success'">
            {{ $t('library.parseStats', { valid: preview.valid_count, error: preview.error_count }) }}
          </span>
        </div>
        <div v-if="preview.errors.length > 0" class="mb-3">
          <div v-for="e in preview.errors.slice(0, 10)" :key="e.item_no" class="error-text">
            {{ $t('library.parseErrorLine', { no: e.item_no, error: e.error }) }}
          </div>
          <div v-if="preview.errors.length > 10" class="hint">{{ $t('library.moreErrors', { count: preview.errors.length - 10 }) }}</div>
        </div>
        <div v-if="preview.preview_items.length > 0" class="mb-3">
          <div class="hint mb-2">{{ $t('library.previewLabel', { count: preview.preview_items.length }) }}</div>
          <div v-for="(item, i) in preview.preview_items" :key="i" class="text-secondary" style="white-space: pre-wrap">
            {{ i + 1 }}. {{ item.stem }}
          </div>
        </div>
        <button v-if="!batch" class="btn btn-success" :disabled="importing || preview.valid_count === 0" @click="doCommit">
          {{ importing ? $t('common.importing') : $t('library.confirmImport', { count: preview.valid_count }) }}
        </button>
        <div v-if="batch" class="badge badge-success">{{ $t('library.imported', { count: batch.items.filter((i) => i.status === 'imported').length }) }}</div>
      </div>
    </template>

    <!-- 知识点 -->
    <template v-if="tab === 'knowledge'">
      <div class="card">
        <div class="flex gap-3">
          <input v-model="newKnow" class="input" style="max-width: 300px" :placeholder="$t('library.knowledgePlaceholder')" @keyup.enter="createKnow" />
          <button class="btn btn-primary" :disabled="!newKnow.trim()" @click="createKnow">{{ $t('common.add') }}</button>
        </div>
        <div v-if="tree.length === 0" class="empty" style="padding: var(--space-4)">
          <p>{{ $t('library.emptyKnowledge') }}</p>
        </div>
        <ul v-else class="know-tree">
          <li v-for="node in tree" :key="node.id">
            <span class="badge badge-primary">{{ node.name }}</span>
            <ul v-if="node.children?.length">
              <li v-for="child in node.children" :key="child.id">
                <span class="badge">{{ child.name }}</span>
              </li>
            </ul>
          </li>
        </ul>
      </div>
    </template>

    <!-- 资料与 RAG -->
    <template v-if="tab === 'documents'">
      <div v-if="info" class="offline-banner">{{ info }}</div>
      <div class="grid" style="grid-template-columns: 1fr 1fr">
        <div class="card">
          <div class="card-title">{{ $t('library.localDocs') }}</div>
          <div class="flex gap-3 mb-3">
            <input type="file" accept=".md,.markdown,.txt,.text" class="input" style="max-width: 320px" @change="onDocFile" />
            <button class="btn btn-primary" :disabled="!docFile || docImporting" @click="doDocImport">
              {{ docImporting ? $t('common.importing') : $t('library.importAndIndex') }}
            </button>
          </div>
          <div v-if="docs.length === 0" class="empty" style="padding: var(--space-3)">
            <p class="hint">{{ $t('library.docsEmptyHint') }}</p>
          </div>
          <table v-else class="table">
            <thead><tr><th>{{ $t('library.colFile') }}</th><th>{{ $t('library.colSize') }}</th><th>{{ $t('library.colStatus') }}</th><th></th></tr></thead>
            <tbody>
              <tr v-for="d in docs" :key="d.id">
                <td>{{ d.file_name }}</td>
                <td class="text-muted">{{ (d.byte_size / 1024).toFixed(1) }} KB</td>
                <td>
                  <span class="badge" :class="{ 'badge-success': d.status === 'indexed', 'badge-error': d.status === 'failed' }">
                    {{ d.status }}
                  </span>
                </td>
                <td>
                  <div class="flex gap-2">
                    <button class="btn btn-sm" @click="addReadLater(d)">{{ $t('library.readLaterAdd') }}</button>
                    <button class="btn btn-sm btn-ghost" @click="deleteDoc(d)">{{ $t('common.delete') }}</button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <div class="card">
          <div class="card-title">{{ $t('library.ragTitle') }}</div>
          <textarea v-model="ragQuestion" class="textarea" style="min-height: 80px" :placeholder="$t('library.ragPlaceholder')"></textarea>
          <button class="btn btn-primary mt-2" :disabled="ragStreaming || !ragQuestion.trim()" @click="ragAsk">
            {{ ragStreaming ? $t('library.answering') : $t('library.ask') }}
          </button>
          <div v-if="ragAnswer" class="chat-msg assistant mt-3" style="max-width: 100%">
            <span :class="{ 'stream-cursor': ragStreaming }">{{ ragAnswer }}</span>
            <span v-for="(c, i) in ragCitations" :key="i" class="citation">📎 {{ c.document_name }}{{ c.section ? ' · ' + c.section : '' }}</span>
          </div>
        </div>
      </div>
    </template>

    <!-- 收藏 / 稍后读 -->
    <template v-if="tab === 'favorites'">
      <div v-if="info" class="offline-banner">{{ info }}</div>
      <div class="grid" style="grid-template-columns: 1fr 1fr">
        <div class="card">
          <div class="card-title">{{ $t('library.favTitle') }}</div>
          <div v-if="favStore.loading" class="loading"><div class="spinner"></div></div>
          <div v-else-if="favStore.favorites.length === 0" class="empty" style="padding: var(--space-3)">
            <p class="hint">{{ $t('library.favEmpty') }}</p>
          </div>
          <table v-else class="table">
            <thead>
              <tr>
                <th>{{ $t('library.colRefType') }}</th>
                <th>{{ $t('library.colGroup') }}</th>
                <th>{{ $t('library.colNote') }}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="f in favStore.favorites" :key="f.id">
                <td><span class="badge">{{ $t(refTypeKey(f.ref_type)) }}</span></td>
                <td class="text-muted">{{ f.group_name || '—' }}</td>
                <td class="grow">{{ f.note?.slice(0, 40) || '—' }}</td>
                <td><button class="btn btn-sm btn-ghost" @click="unsave(f)">{{ $t('library.actionUnsave') }}</button></td>
              </tr>
            </tbody>
          </table>
        </div>

        <div class="card">
          <div class="card-title">{{ $t('library.readLaterTitle') }}</div>
          <div v-if="favStore.readLater.length === 0" class="empty" style="padding: var(--space-3)">
            <p class="hint">{{ $t('library.readLaterEmpty') }}</p>
          </div>
          <table v-else class="table">
            <thead>
              <tr>
                <th>{{ $t('library.colDocument') }}</th>
                <th>{{ $t('library.colStatus') }}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="r in favStore.readLater" :key="r.id">
                <td class="grow">{{ docName(r.document_id) }}</td>
                <td>
                  <span class="badge" :class="{
                    'badge-warning': r.status === 'queued',
                    'badge-success': r.status === 'read',
                    'badge-offline': r.status === 'skipped',
                  }">{{ $t(readLaterStatusKey(r.status)) }}</span>
                </td>
                <td>
                  <div class="flex gap-2">
                    <button v-if="r.status === 'queued'" class="btn btn-sm btn-success" @click="readLaterAction(r, 'read')">{{ $t('library.readLaterMarkRead') }}</button>
                    <button v-if="r.status === 'queued'" class="btn btn-sm btn-ghost" @click="readLaterAction(r, 'skip')">{{ $t('library.readLaterSkip') }}</button>
                    <button v-if="r.status !== 'queued'" class="btn btn-sm btn-ghost" @click="readLaterAction(r, 'requeue')">{{ $t('library.readLaterRequeue') }}</button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.know-tree {
  list-style: none;
  padding-left: var(--space-4);
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  margin-top: var(--space-4);
}
.know-tree ul {
  list-style: none;
  padding-left: var(--space-4);
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  margin-top: var(--space-2);
}
</style>
