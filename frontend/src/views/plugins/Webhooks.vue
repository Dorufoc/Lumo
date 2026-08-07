<script setup lang="ts">
// Webhook 出站分发视图（完整设计文档 4.23 / Todo 31）。
// 订阅按工作区隔离：创建（url + 事件白名单勾选 + 可选 secret_ref）→ 测试发送 → 删除。
import { onMounted, ref } from 'vue'
import { localizedMessageOf } from '@/api/client'
import { webhookDelete, webhookList, webhookSubscribe, webhookTestSend } from '@/api/webhooks'
import { WEBHOOK_EVENTS, type WebhookSubscription } from '@/api/types'
import { useSessionStore } from '@/stores/session'

const session = useSessionStore()

const loading = ref(false)
const busy = ref(false)
const error = ref('')
const info = ref('')
const subs = ref<WebhookSubscription[]>([])

// 创建弹窗
const createDialog = ref(false)
const createForm = ref<{ url: string; secret_ref: string; event_types: string[] }>({
  url: '',
  secret_ref: '',
  event_types: [],
})

// 测试发送结果弹窗
const testTarget = ref<WebhookSubscription | null>(null)
const testBusy = ref(false)
const testResult = ref<{ ok: boolean; status_code: number; error: string } | null>(null)

// 删除确认弹窗
const deleteTarget = ref<WebhookSubscription | null>(null)
const deleteBusy = ref(false)

async function refresh() {
  if (!session.workspaceId) return
  loading.value = true
  error.value = ''
  try {
    subs.value = await webhookList({ workspace_id: session.workspaceId })
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    loading.value = false
  }
}

function openCreate() {
  error.value = ''
  info.value = ''
  createForm.value = { url: '', secret_ref: '', event_types: [] }
  createDialog.value = true
}

function toggleEvent(ev: string) {
  const idx = createForm.value.event_types.indexOf(ev)
  if (idx >= 0) createForm.value.event_types.splice(idx, 1)
  else createForm.value.event_types.push(ev)
}

async function submitCreate() {
  const url = createForm.value.url.trim()
  if (!url || createForm.value.event_types.length === 0) return
  busy.value = true
  error.value = ''
  info.value = ''
  try {
    await webhookSubscribe({
      workspace_id: session.workspaceId,
      url,
      event_types: createForm.value.event_types as WebhookSubscription['event_types'],
      secret_ref: createForm.value.secret_ref.trim() || null,
      idempotency_key: `wh-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`,
    })
    createDialog.value = false
    info.value = 'webhooks.subscribed'
    await refresh()
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    busy.value = false
  }
}

async function runTest(sub: WebhookSubscription) {
  testTarget.value = sub
  testResult.value = null
  testBusy.value = true
  try {
    testResult.value = await webhookTestSend({ workspace_id: session.workspaceId, subscription_id: sub.id })
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    testBusy.value = false
  }
}

function openDelete(sub: WebhookSubscription) {
  deleteTarget.value = sub
}

async function submitDelete() {
  if (!deleteTarget.value) return
  const target = deleteTarget.value
  deleteBusy.value = true
  error.value = ''
  try {
    await webhookDelete({ workspace_id: session.workspaceId, subscription_id: target.id })
    deleteTarget.value = null
    info.value = 'webhooks.deleted'
    await refresh()
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    deleteBusy.value = false
  }
}

onMounted(refresh)
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('webhooks.title') }}</h1>
        <div class="subtitle">{{ $t('webhooks.subtitle') }}</div>
      </div>
      <button class="btn btn-primary" @click="openCreate">{{ $t('webhooks.subscribe') }}</button>
    </div>

    <div v-if="error" class="error-banner">{{ error }}</div>
    <div v-if="info" class="offline-banner">{{ $t(info) }}</div>

    <div v-if="loading" class="loading"><div class="spinner"></div></div>
    <div v-else-if="subs.length === 0" class="empty">{{ $t('webhooks.empty') }}</div>

    <div v-else class="hook-list">
      <div v-for="sub in subs" :key="sub.id" class="card hook-card">
        <div class="hook-head">
          <div class="hook-url">
            <code>{{ sub.url }}</code>
            <span class="hook-state" :class="sub.enabled ? 'ok' : 'off'">
              {{ sub.enabled ? $t('webhooks.enabledState') : $t('webhooks.disabledState') }}
            </span>
          </div>
          <div class="flex gap-2">
            <button class="btn btn-sm" :disabled="testBusy" @click="runTest(sub)">{{ $t('webhooks.testSend') }}</button>
            <button class="btn btn-sm btn-danger" @click="openDelete(sub)">{{ $t('webhooks.delete') }}</button>
          </div>
        </div>
        <div class="hook-meta">
          <div class="hook-row">
            <span class="meta-label">{{ $t('webhooks.events') }}</span>
            <span class="ev-chips">
              <span v-for="ev in sub.event_types" :key="ev" class="chip">{{ ev }}</span>
            </span>
          </div>
          <div class="hook-row">
            <span class="meta-label">{{ $t('webhooks.secretRef') }}</span>
            <span v-if="sub.secret_ref" class="meta-value">{{ sub.secret_ref }}</span>
            <span v-else class="meta-none">{{ $t('webhooks.noSecret') }}</span>
          </div>
          <div class="hook-row">
            <span class="meta-label">{{ $t('webhooks.createdAt') }}</span>
            <span>{{ sub.created_at }}</span>
          </div>
        </div>
      </div>
    </div>

    <!-- 创建订阅弹窗 -->
    <div v-if="createDialog" class="modal-mask">
      <div class="card modal">
        <h3>{{ $t('webhooks.subscribe') }}</h3>
        <div class="field">
          <label>{{ $t('webhooks.url') }} *</label>
          <input v-model="createForm.url" class="input" type="text" :placeholder="$t('webhooks.urlPlaceholder')" />
        </div>
        <div class="field">
          <label>{{ $t('webhooks.secretRef') }}</label>
          <input v-model="createForm.secret_ref" class="input" type="text" :placeholder="$t('webhooks.secretRefPlaceholder')" />
          <div class="field-hint">{{ $t('webhooks.secretRefHint') }}</div>
        </div>
        <div class="field">
          <label>{{ $t('webhooks.events') }} *</label>
          <div class="ev-options">
            <label v-for="ev in WEBHOOK_EVENTS" :key="ev" class="check-line">
              <input type="checkbox" :checked="createForm.event_types.includes(ev)" @change="toggleEvent(ev)" />
              <span>{{ ev }}</span>
            </label>
          </div>
        </div>
        <div class="flex gap-3 mt-3">
          <button class="btn" :disabled="busy" @click="createDialog = false">{{ $t('common.cancel') }}</button>
          <button
            class="btn btn-primary"
            :disabled="busy || !createForm.url.trim() || createForm.event_types.length === 0"
            @click="submitCreate"
          >
            {{ busy ? $t('common.submitting') : $t('webhooks.confirmSubscribe') }}
          </button>
        </div>
      </div>
    </div>

    <!-- 测试发送结果弹窗 -->
    <div v-if="testTarget" class="modal-mask">
      <div class="card modal">
        <h3>{{ $t('webhooks.testResult', { url: testTarget.url }) }}</h3>
        <div v-if="testBusy" class="loading"><div class="spinner"></div></div>
        <div v-else-if="testResult">
          <div :class="testResult.ok ? 'result-ok' : 'result-fail'">
            <span v-if="testResult.ok">
              {{ $t('webhooks.testOk') }}<span v-if="testResult.status_code"> · HTTP {{ testResult.status_code }}</span>
            </span>
            <span v-else>{{ $t('webhooks.testFailed') }}: {{ testResult.error }}</span>
          </div>
        </div>
        <div class="flex gap-3 mt-3">
          <button class="btn" @click="testTarget = null">{{ $t('common.close') }}</button>
        </div>
      </div>
    </div>

    <!-- 删除确认弹窗 -->
    <div v-if="deleteTarget" class="modal-mask">
      <div class="card modal">
        <h3>{{ $t('webhooks.confirmDelete') }}</h3>
        <p class="perm-hint">{{ $t('webhooks.confirmDeleteHint', { url: deleteTarget.url }) }}</p>
        <div class="flex gap-3 mt-3">
          <button class="btn" :disabled="deleteBusy" @click="deleteTarget = null">{{ $t('common.cancel') }}</button>
          <button class="btn btn-danger" :disabled="deleteBusy" @click="submitDelete">
            {{ deleteBusy ? $t('common.submitting') : $t('webhooks.confirmDelete') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.hook-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}

.hook-card {
  padding: var(--space-4);
}

.hook-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: var(--space-3);
  flex-wrap: wrap;
}

.hook-url {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  flex-wrap: wrap;
}

.hook-state {
  font-size: var(--text-xs);
  padding: 2px 8px;
  border-radius: var(--radius-full);
}

.hook-state.ok {
  color: var(--success);
  background: color-mix(in srgb, var(--success) 12%, transparent);
}

.hook-state.off {
  color: var(--text-muted);
  background: var(--bg-elevated);
}

.hook-meta {
  margin-top: var(--space-3);
  font-size: var(--text-xs);
  color: var(--text-secondary);
}

.hook-row {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  margin-top: var(--space-1);
  flex-wrap: wrap;
}

.meta-label {
  min-width: 72px;
  color: var(--text-muted);
}

.meta-value {
  color: var(--text);
}

.meta-none {
  color: var(--text-muted);
}

.ev-chips {
  display: inline-flex;
  gap: var(--space-1);
  flex-wrap: wrap;
}

.chip {
  font-size: var(--text-xs);
  padding: 2px 8px;
  border-radius: var(--radius-full);
  background: var(--bg-elevated);
  color: var(--text-secondary);
}

.ev-options {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
  gap: var(--space-1);
  margin-top: var(--space-2);
}

.check-line {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  font-size: var(--text-sm);
  color: var(--text-secondary);
}

.field-hint {
  margin-top: var(--space-1);
  font-size: var(--text-xs);
  color: var(--text-muted);
}

.perm-hint {
  color: var(--text-secondary);
  font-size: var(--text-xs);
  margin: var(--space-2) 0;
}

.result-ok {
  color: var(--success);
  font-size: var(--text-sm);
  margin: var(--space-2) 0;
}

.result-fail {
  color: var(--danger);
  font-size: var(--text-sm);
  margin: var(--space-2) 0;
}
</style>
