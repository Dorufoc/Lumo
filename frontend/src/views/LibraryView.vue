<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { call, openEventStream, upload, type Citation } from '@/api/client'
import type { Document, DocumentPage, ImportBatch, ImportPreview, KnowledgeNode, Question, QuestionPage } from '@/api/types'
import { useSessionStore } from '@/stores/session'

const session = useSessionStore()

const tab = ref<'questions' | 'import' | 'knowledge' | 'documents'>('questions')
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
    error.value = (e as Error).message
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
    error.value = (e as Error).message
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
    error.value = (e as Error).message
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
    error.value = (e as Error).message
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
    error.value = (e as Error).message
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
    info.value = doc.status === 'indexed' ? `✅ 已导入并索引：${doc.file_name}` : `文档状态：${doc.status}`
    await loadDocs()
  } catch (e) {
    error.value = (e as Error).message
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
    error.value = (e as Error).message
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
        error.value = err.message
      },
    })
  } catch (e) {
    ragStreaming.value = false
    error.value = (e as Error).message
  }
}

const info = ref('')

onMounted(() => {
  void loadQuestions()
  void loadTree()
  void loadDocs()
})

function typeLabel(t: string) {
  return { single_choice: '单选', multiple_choice: '多选', fill_blank: '填空', short_answer: '简答', code: '代码' }[t] ?? t
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>题库与资料</h1>
        <div class="subtitle">导入 Markdown / JSON / 纯文本题库，管理题目与知识点</div>
      </div>
    </div>

    <div v-if="error" class="error-banner">{{ error }}</div>

    <div class="tabs">
      <div class="tab" :class="{ active: tab === 'questions' }" @click="tab = 'questions'">题目（{{ questions.length }}）</div>
      <div class="tab" :class="{ active: tab === 'import' }" @click="tab = 'import'">导入题库</div>
      <div class="tab" :class="{ active: tab === 'knowledge' }" @click="tab = 'knowledge'">知识点</div>
      <div class="tab" :class="{ active: tab === 'documents' }" @click="tab = 'documents'">资料问答</div>
    </div>

    <!-- 题目列表 -->
    <template v-if="tab === 'questions'">
      <div v-if="loading" class="loading"><div class="spinner"></div></div>
      <div v-else-if="questions.length === 0" class="empty">
        <div class="empty-icon">📚</div>
        <p>题库为空，去「导入题库」开始吧。</p>
      </div>
      <div v-else class="card">
        <table class="table">
          <thead>
            <tr><th>题干</th><th>题型</th><th>状态</th><th>来源</th><th>操作</th></tr>
          </thead>
          <tbody>
            <tr v-for="q in questions" :key="q.id">
              <td class="grow">{{ q.current_version?.payload.stem?.slice(0, 70) }}</td>
              <td><span class="badge">{{ typeLabel(q.type) }}</span></td>
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
                  <button v-if="q.status === 'draft'" class="btn btn-sm" @click="transition(q, 'review')">送审</button>
                  <button v-if="q.status === 'reviewed'" class="btn btn-sm btn-success" @click="transition(q, 'publish')">发布</button>
                  <button v-if="!['published', 'archived'].includes(q.status)" class="btn btn-sm btn-ghost" @click="transition(q, 'archive')">归档</button>
                  <button v-if="q.status === 'published'" class="btn btn-sm btn-ghost" @click="transition(q, 'archive')">归档</button>
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
        <h3>导入题库</h3>
        <p class="text-secondary mb-3">
          支持 <strong>Markdown</strong>（`## 题干` / `A. 选项` / `答案：A` / `解析：…`）、<strong>JSON</strong>
          （`{"questions": [...]}` 或数组）、<strong>纯文本</strong>（`1. 题干` 开头）。判断题用 A 正确 / B 错误。
        </p>
        <div class="flex gap-3">
          <input type="file" accept=".md,.markdown,.json,.txt,.text" class="input" style="max-width: 400px" @change="onFile" />
          <button class="btn btn-primary" :disabled="!file || importing" @click="doPreflight">
            {{ importing ? '解析中…' : '解析文件' }}
          </button>
        </div>
        <div v-if="file" class="hint mt-2">{{ file.name }} · {{ (file.size / 1024).toFixed(1) }} KB</div>
      </div>

      <div v-if="preview" class="card">
        <div class="flex-between mb-3">
          <h3 style="margin: 0">解析结果：{{ preview.file_name }}</h3>
          <span class="badge" :class="preview.error_count > 0 ? 'badge-warning' : 'badge-success'">
            {{ preview.valid_count }} 道有效 / {{ preview.error_count }} 道错误
          </span>
        </div>
        <div v-if="preview.errors.length > 0" class="mb-3">
          <div v-for="e in preview.errors.slice(0, 10)" :key="e.item_no" class="error-text">
            第 {{ e.item_no }} 题：{{ e.error }}
          </div>
          <div v-if="preview.errors.length > 10" class="hint">…还有 {{ preview.errors.length - 10 }} 条错误</div>
        </div>
        <div v-if="preview.preview_items.length > 0" class="mb-3">
          <div class="hint mb-2">预览（前 {{ preview.preview_items.length }} 道）：</div>
          <div v-for="(item, i) in preview.preview_items" :key="i" class="text-secondary" style="white-space: pre-wrap">
            {{ i + 1 }}. {{ item.stem }}
          </div>
        </div>
        <button v-if="!batch" class="btn btn-success" :disabled="importing || preview.valid_count === 0" @click="doCommit">
          {{ importing ? '导入中…' : `确认导入 ${preview.valid_count} 道题` }}
        </button>
        <div v-if="batch" class="badge badge-success">✅ 已导入：{{ batch.items.filter((i) => i.status === 'imported').length }} 道题入库</div>
      </div>
    </template>

    <!-- 知识点 -->
    <template v-if="tab === 'knowledge'">
      <div class="card">
        <div class="flex gap-3">
          <input v-model="newKnow" class="input" style="max-width: 300px" placeholder="知识点名称，如：微积分" @keyup.enter="createKnow" />
          <button class="btn btn-primary" :disabled="!newKnow.trim()" @click="createKnow">添加</button>
        </div>
        <div v-if="tree.length === 0" class="empty" style="padding: var(--space-4)">
          <p>暂无知识点。</p>
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
          <div class="card-title">本地资料（支持 Markdown / TXT）</div>
          <div class="flex gap-3 mb-3">
            <input type="file" accept=".md,.markdown,.txt,.text" class="input" style="max-width: 320px" @change="onDocFile" />
            <button class="btn btn-primary" :disabled="!docFile || docImporting" @click="doDocImport">
              {{ docImporting ? '导入中…' : '导入并索引' }}
            </button>
          </div>
          <div v-if="docs.length === 0" class="empty" style="padding: var(--space-3)">
            <p class="hint">导入学习资料（讲义/笔记），即可基于资料提问。</p>
          </div>
          <table v-else class="table">
            <thead><tr><th>文件</th><th>大小</th><th>状态</th><th></th></tr></thead>
            <tbody>
              <tr v-for="d in docs" :key="d.id">
                <td>{{ d.file_name }}</td>
                <td class="text-muted">{{ (d.byte_size / 1024).toFixed(1) }} KB</td>
                <td>
                  <span class="badge" :class="{ 'badge-success': d.status === 'indexed', 'badge-error': d.status === 'failed' }">
                    {{ d.status }}
                  </span>
                </td>
                <td><button class="btn btn-sm btn-ghost" @click="deleteDoc(d)">删除</button></td>
              </tr>
            </tbody>
          </table>
        </div>

        <div class="card">
          <div class="card-title">资料问答（RAG）</div>
          <textarea v-model="ragQuestion" class="textarea" style="min-height: 80px" placeholder="基于已导入资料提问，例如：万有引力定律的内容是什么？"></textarea>
          <button class="btn btn-primary mt-2" :disabled="ragStreaming || !ragQuestion.trim()" @click="ragAsk">
            {{ ragStreaming ? '回答中…' : '提问' }}
          </button>
          <div v-if="ragAnswer" class="chat-msg assistant mt-3" style="max-width: 100%">
            <span :class="{ 'stream-cursor': ragStreaming }">{{ ragAnswer }}</span>
            <span v-for="(c, i) in ragCitations" :key="i" class="citation">📎 {{ c.document_name }}{{ c.section ? ' · ' + c.section : '' }}</span>
          </div>
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
