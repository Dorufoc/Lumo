<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { localizedMessageOf } from '@/api/client'
import type { ReminderKind } from '@/api/types'
import { useI18nStore } from '@/stores/i18n'
import { useNotificationStore } from '@/stores/notification'

const i18n = useI18nStore()
const store = useNotificationStore()

const loading = ref(true)
const error = ref('')
const info = ref('')

// 提醒设置（interval 规则）
const kinds: ReminderKind[] = ['review', 'goal', 'exam', 'streak', 'health']
const reminderKind = ref<ReminderKind>('review')
const minutes = ref(60)
const repeat = ref(true)
const enabled = ref(true)
const saving = ref(false)

// 测试发送
const testKind = ref<ReminderKind>('review')
const testing = ref(false)

const unreadCount = computed(() => store.items.filter((n) => !n.read_at).length)

async function load() {
  loading.value = true
  error.value = ''
  info.value = ''
  try {
    await store.load()
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    loading.value = false
  }
}

async function onToggleUnread() {
  await load()
}

async function onMarkAll() {
  error.value = ''
  info.value = ''
  try {
    const n = await store.markAllRead()
    if (n > 0) info.value = i18n.t('notifications.markAllReadDone', { count: n })
  } catch (e) {
    error.value = localizedMessageOf(e)
  }
}

async function onMarkRead(id: string) {
  try {
    await store.markRead(id)
  } catch (e) {
    error.value = localizedMessageOf(e)
  }
}

async function doUpsert() {
  saving.value = true
  error.value = ''
  info.value = ''
  try {
    const rule = JSON.stringify({ type: 'interval', minutes: minutes.value, repeat: repeat.value })
    const r = await store.upsert(reminderKind.value, rule, enabled.value)
    info.value = i18n.t('reminder.upserted', { kind: i18n.t(`reminder.kinds.${r.kind}`) })
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    saving.value = false
  }
}

async function doTestSend() {
  testing.value = true
  error.value = ''
  info.value = ''
  try {
    const res = await store.testSend(testKind.value)
    info.value = i18n.t('reminder.testSent', { kind: i18n.t(`reminder.kinds.${res.kind}`) })
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    testing.value = false
  }
}

/** body_args（Record<string, unknown>）→ i18n 插值参数（string|number）。 */
function bodyParams(args: Record<string, unknown>): Record<string, string | number> {
  const out: Record<string, string | number> = {}
  for (const [k, v] of Object.entries(args ?? {})) {
    if (typeof v === 'string' || typeof v === 'number') out[k] = v
  }
  return out
}

function fmtTime(s: string): string {
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return s
  return d.toLocaleString()
}

onMounted(load)
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('notifications.title') }}</h1>
        <div class="subtitle">{{ $t('reminder.subtitle') }}</div>
      </div>
      <button class="btn btn-sm" :disabled="loading" @click="load">{{ $t('common.refresh') }}</button>
    </div>

    <div v-if="error" class="error-banner">{{ error }}</div>
    <div v-if="info" class="offline-banner">{{ info }}</div>

    <!-- 提醒设置（interval 规则） -->
    <div class="card">
      <div class="card-title">{{ $t('reminder.title') }}</div>
      <div class="form-grid">
        <label class="field">
          <span>{{ $t('notifications.kindLabel') }}</span>
          <select v-model="reminderKind" class="input">
            <option v-for="k in kinds" :key="k" :value="k">{{ $t(`reminder.kinds.${k}`) }}</option>
          </select>
        </label>
        <label class="field">
          <span>{{ $t('reminder.intervalMinutes') }}</span>
          <input v-model.number="minutes" type="number" min="1" class="input" />
        </label>
        <label class="check-row">
          <input v-model="repeat" type="checkbox" />
          <span>{{ $t('reminder.repeat') }}</span>
        </label>
        <label class="check-row">
          <input v-model="enabled" type="checkbox" />
          <span>{{ enabled ? $t('reminder.enable') : $t('reminder.disable') }}</span>
        </label>
      </div>
      <div class="mt-2">
        <button class="btn btn-primary" :disabled="saving" @click="doUpsert">
          {{ saving ? $t('common.submitting') : $t('common.save') }}
        </button>
      </div>
    </div>

    <!-- 测试发送 -->
    <div class="card">
      <div class="card-title">{{ $t('reminder.testSend') }}</div>
      <div class="flex gap-3">
        <select v-model="testKind" class="input test-kind">
          <option v-for="k in kinds" :key="k" :value="k">{{ $t(`reminder.kinds.${k}`) }}</option>
        </select>
        <button class="btn" :disabled="testing" @click="doTestSend">
          {{ testing ? $t('common.submitting') : $t('reminder.testSend') }}
        </button>
      </div>
    </div>

    <!-- 通知中心 -->
    <div class="card">
      <div class="flex-between">
        <div class="card-title">
          {{ $t('notifications.title') }}
          <span v-if="unreadCount > 0" class="badge badge-warning">{{ $t('notifications.unread', { count: unreadCount }) }}</span>
        </div>
        <div class="flex gap-3">
          <label class="check-row">
            <input v-model="store.unreadOnly" type="checkbox" @change="onToggleUnread" />
            <span>{{ $t('notifications.unreadOnly') }}</span>
          </label>
          <button class="btn btn-sm" :disabled="store.busy" @click="onMarkAll">
            {{ $t('notifications.markAllRead') }}
          </button>
        </div>
      </div>

      <div v-if="loading" class="loading"><div class="spinner"></div></div>
      <div v-else-if="store.items.length === 0" class="empty">
        <div class="empty-icon">🔔</div>
        <p>{{ $t('notifications.empty') }}</p>
      </div>
      <ul v-else class="notif-list">
        <li
          v-for="n in store.items"
          :key="n.id"
          class="notif-item"
          :class="{ unread: !n.read_at }"
          @click="n.read_at ? undefined : onMarkRead(n.id)"
        >
          <div class="notif-dot"></div>
          <div class="notif-body">
            <div class="notif-title">{{ $t(n.title_key, bodyParams(n.body_args)) }}</div>
            <div class="notif-meta">{{ fmtTime(n.created_at) }}</div>
          </div>
        </li>
      </ul>
      <div v-if="store.hasMore" class="mt-2">
        <button class="btn btn-sm" :disabled="store.busy" @click="store.loadMore()">
          {{ store.busy ? $t('common.submitting') : $t('notifications.loadMore') }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.form-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: var(--space-3);
  align-items: end;
  margin-bottom: var(--space-3);
}

.field {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
  font-size: var(--text-xs);
  color: var(--text-secondary);
}

.test-kind {
  max-width: 220px;
}

.notif-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}

.notif-item {
  display: flex;
  align-items: flex-start;
  gap: var(--space-2);
  padding: var(--space-3);
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  background: var(--bg-surface);
  cursor: pointer;
}

.notif-item.unread {
  border-color: var(--color-primary);
  background: var(--color-primary-soft);
}

.notif-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--border-strong);
  margin-top: 6px;
  flex-shrink: 0;
}

.notif-item.unread .notif-dot {
  background: var(--color-primary);
}

.notif-body {
  flex: 1;
  min-width: 0;
}

.notif-title {
  font-size: var(--text-base);
  color: var(--text);
}

.notif-meta {
  font-size: var(--text-xs);
  color: var(--text-muted);
  margin-top: var(--space-1);
}
</style>
