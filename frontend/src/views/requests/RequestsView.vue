<script setup lang="ts">
// 求题请求视图（完整设计文档 4.20 P2 求题请求 / Todo 36）。
// 提交表单（求题方向描述 + 关联知识点）→ 我的请求列表（状态跟踪：open/fulfilled/closed
// + 审核状态 pending/approved/rejected）→ 生成草稿 / 审核通过入库 / 拒绝 / 取消。
import { onMounted, ref } from 'vue'
import { call, localizedMessageOf } from '@/api/client'
import {
  contentRequestCancel,
  contentRequestCreate,
  contentRequestGenerate,
  contentRequestList,
  contentRequestReview,
} from '@/api/requests'
import type { ContentRequest, KnowledgeNode } from '@/api/types'
import { useI18nStore } from '@/stores/i18n'
import { useSessionStore } from '@/stores/session'

const session = useSessionStore()
const i18n = useI18nStore()

// 模板用：首字母大写（open → Open），用于拼装 i18n key。
function cap(s: string) {
  return s ? s.charAt(0).toUpperCase() + s.slice(1) : s
}

const loading = ref(false)
const busy = ref(false)
const actionBusy = ref<Record<string, boolean>>({})
const error = ref('')
const info = ref('')
const requests = ref<ContentRequest[]>([])
const tree = ref<KnowledgeNode[]>([])
const description = ref('')
const selectedKnowledge = ref<string[]>([])
const generateCount = ref(3)

// 拉平知识树为可勾选列表（带缩进路径）。
const flatKnowledge = ref<{ id: string; label: string }[]>([])

function flatten(nodes: KnowledgeNode[], depth: number) {
  for (const n of nodes) {
    flatKnowledge.value.push({ id: n.id, label: `${'　'.repeat(depth)}${n.name}` })
    if (n.children?.length) flatten(n.children, depth + 1)
  }
}

async function loadKnowledge() {
  if (!session.workspaceId) return
  flatKnowledge.value = []
  try {
    tree.value = await call<KnowledgeNode[]>('KnowledgeTreeGet', { workspace_id: session.workspaceId })
    flatten(tree.value, 0)
  } catch {
    // 知识点树加载失败不阻断页面
  }
}

async function refresh() {
  if (!session.workspaceId) return
  loading.value = true
  error.value = ''
  try {
    requests.value = await contentRequestList({ workspace_id: session.workspaceId })
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    loading.value = false
  }
}

function newKey(prefix: string) {
  return `${prefix}-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
}

async function submit() {
  if (!session.workspaceId) return
  const desc = description.value.trim()
  if (!desc) return
  busy.value = true
  error.value = ''
  info.value = ''
  try {
    await contentRequestCreate({
      workspace_id: session.workspaceId,
      user_id: session.userId,
      knowledge_ids: selectedKnowledge.value,
      description: desc,
      idempotency_key: newKey('cr'),
    })
    description.value = ''
    selectedKnowledge.value = []
    info.value = 'requests.submitSuccess'
    await refresh()
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    busy.value = false
  }
}

async function generate(req: ContentRequest) {
  if (!session.workspaceId || actionBusy.value[req.id]) return
  actionBusy.value = { ...actionBusy.value, [req.id]: true }
  error.value = ''
  try {
    await contentRequestGenerate({
      workspace_id: session.workspaceId,
      user_id: session.userId,
      request_id: req.id,
      count: generateCount.value,
      idempotency_key: newKey('rg'),
    })
    info.value = 'requests.submitSuccess'
    await refresh()
  } catch (e) {
    error.value = `${localizedMessageOf(e)}`
  } finally {
    actionBusy.value = { ...actionBusy.value, [req.id]: false }
  }
}

async function approve(req: ContentRequest) {
  if (!session.workspaceId || actionBusy.value[req.id]) return
  actionBusy.value = { ...actionBusy.value, [req.id]: true }
  error.value = ''
  try {
    await contentRequestReview({
      workspace_id: session.workspaceId,
      user_id: session.userId,
      request_id: req.id,
      decision: 'approved',
      reason: '',
    })
    await refresh()
  } catch (e) {
    error.value = `${localizedMessageOf(e)}`
  } finally {
    actionBusy.value = { ...actionBusy.value, [req.id]: false }
  }
}

async function reject(req: ContentRequest) {
  if (!session.workspaceId || actionBusy.value[req.id]) return
  const entered = window.prompt(i18n.t('requests.reviewReasonPlaceholder'))
  if (entered === null) return
  const reason = entered
  actionBusy.value = { ...actionBusy.value, [req.id]: true }
  error.value = ''
  try {
    await contentRequestReview({
      workspace_id: session.workspaceId,
      user_id: session.userId,
      request_id: req.id,
      decision: 'rejected',
      reason,
    })
    await refresh()
  } catch (e) {
    error.value = `${localizedMessageOf(e)}`
  } finally {
    actionBusy.value = { ...actionBusy.value, [req.id]: false }
  }
}

async function cancel(req: ContentRequest) {
  if (!session.workspaceId || actionBusy.value[req.id]) return
  actionBusy.value = { ...actionBusy.value, [req.id]: true }
  error.value = ''
  try {
    await contentRequestCancel({
      workspace_id: session.workspaceId,
      user_id: session.userId,
      request_id: req.id,
    })
    await refresh()
  } catch (e) {
    error.value = `${localizedMessageOf(e)}`
  } finally {
    actionBusy.value = { ...actionBusy.value, [req.id]: false }
  }
}

onMounted(() => {
  void loadKnowledge()
  void refresh()
})
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('requests.title') }}</h1>
        <div class="subtitle">{{ $t('requests.subtitle') }}</div>
      </div>
    </div>

    <div v-if="error" class="error-banner">{{ error }}</div>
    <div v-if="info" class="offline-banner">{{ $t(info) }}</div>

    <!-- 提交表单 -->
    <div class="card submit-card">
      <h3>{{ $t('requests.submit') }}</h3>
      <div class="field">
        <textarea
          v-model="description"
          class="input request-input"
          rows="4"
          :placeholder="$t('requests.descriptionPlaceholder')"
        />
      </div>
      <div class="field">
        <div class="label">{{ $t('requests.selectKnowledge') }}</div>
        <div v-if="flatKnowledge.length === 0" class="muted">{{ $t('requests.noKnowledge') }}</div>
        <div v-else class="know-grid">
          <label v-for="k in flatKnowledge" :key="k.id" class="know-chip">
            <input v-model="selectedKnowledge" type="checkbox" :value="k.id" />
            <span>{{ k.label }}</span>
          </label>
        </div>
      </div>
      <button class="btn btn-primary" :disabled="busy || !description.trim()" @click="submit">
        {{ busy ? $t('requests.submitting') : $t('requests.submit') }}
      </button>
    </div>

    <!-- 题量选择（作用于下一次生成） -->
    <div class="card count-card">
      <span class="label">{{ $t('requests.generateCount') }}:</span>
      <select v-model.number="generateCount" class="input count-select">
        <option :value="1">1</option>
        <option :value="3">3</option>
        <option :value="5">5</option>
        <option :value="10">10</option>
      </select>
    </div>

    <div v-if="loading" class="loading"><div class="spinner"></div></div>
    <div v-else-if="requests.length === 0" class="empty">{{ $t('requests.empty') }}</div>

    <!-- 我的请求列表 + 状态跟踪 -->
    <div v-else class="feed">
      <div v-for="req in requests" :key="req.id" class="card request-card">
        <div class="req-head">
          <h3 class="req-title">{{ req.description }}</h3>
          <div class="req-badges">
            <span class="badge" :class="`status-${req.status}`">{{ $t(`requests.status${cap(req.status)}`) }}</span>
            <span v-if="req.review_status" class="badge" :class="`review-${req.review_status}`">
              {{ $t(`requests.review${cap(req.review_status)}`) }}
            </span>
          </div>
        </div>
        <div class="req-meta">
          <span v-if="req.knowledge_ids?.length">
            {{ $t('requests.knowledge') }}: {{ req.knowledge_ids.join(', ') }}
          </span>
          <span>{{ $t('requests.questionCount') }}: {{ req.question_count ?? 0 }}</span>
          <span>{{ $t('requests.created') }}: {{ req.created_at }}</span>
        </div>
        <div v-if="req.review_reason" class="req-reason">
          {{ $t('requests.reviewReason') }}: {{ req.review_reason }}
        </div>
        <div class="req-actions">
          <template v-if="req.status === 'open'">
            <button class="btn btn-sm" :disabled="actionBusy[req.id]" @click="generate(req)">
              {{ actionBusy[req.id] ? $t('requests.generating') : $t('requests.generate') }}
            </button>
            <button
              v-if="req.review_status === 'pending'"
              class="btn btn-sm btn-primary"
              :disabled="actionBusy[req.id]"
              @click="approve(req)"
            >
              {{ $t('requests.reviewApprove') }}
            </button>
            <button
              v-if="req.review_status === 'pending'"
              class="btn btn-sm"
              :disabled="actionBusy[req.id]"
              @click="reject(req)"
            >
              {{ $t('requests.reviewReject') }}
            </button>
            <button class="btn btn-sm" :disabled="actionBusy[req.id]" @click="cancel(req)">
              {{ $t('requests.cancel') }}
            </button>
          </template>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.submit-card,
.count-card {
  padding: var(--space-4);
  margin-bottom: var(--space-3);
}

.request-input {
  resize: vertical;
}

.label {
  font-size: var(--text-sm);
  color: var(--text-secondary);
  margin-bottom: var(--space-1);
}

.muted {
  font-size: var(--text-sm);
  color: var(--text-muted);
}

.know-grid {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
}

.know-chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 4px 10px;
  border: 1px solid var(--border);
  border-radius: 999px;
  font-size: var(--text-sm);
  cursor: pointer;
}

.count-card {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}

.count-select {
  max-width: 90px;
}

.feed {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}

.request-card {
  padding: var(--space-4);
}

.req-head {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: var(--space-3);
  flex-wrap: wrap;
}

.req-title {
  margin: 0;
  flex: 1;
  min-width: 200px;
}

.req-badges {
  display: flex;
  gap: var(--space-2);
  flex-wrap: wrap;
}

.badge {
  font-size: var(--text-xs);
  padding: 2px 10px;
  border-radius: 999px;
  border: 1px solid var(--border);
  color: var(--text-secondary);
}

.badge.status-open {
  color: var(--color-primary);
  border-color: currentColor;
}

.badge.status-fulfilled {
  color: var(--color-success);
  border-color: currentColor;
}

.badge.status-closed,
.badge.review-rejected {
  color: var(--color-error);
  border-color: currentColor;
}

.badge.review-pending {
  color: var(--color-warning);
  border-color: currentColor;
}

.badge.review-approved {
  color: var(--color-success);
  border-color: currentColor;
}

.req-meta {
  display: flex;
  gap: var(--space-3);
  font-size: var(--text-xs);
  color: var(--text-muted);
  margin-top: var(--space-2);
  flex-wrap: wrap;
}

.req-reason {
  font-size: var(--text-sm);
  color: var(--text-secondary);
  margin-top: var(--space-1);
}

.req-actions {
  display: flex;
  gap: var(--space-2);
  margin-top: var(--space-3);
  flex-wrap: wrap;
}
</style>
