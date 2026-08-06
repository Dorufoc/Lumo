<script setup lang="ts">
// Health & focus-assist view (design doc 4.17 / API doc 7.16).
import { onMounted, ref } from 'vue'
import { localizedMessageOf } from '@/api/client'
import type { HealthSettingsUpdateReq } from '@/api/types'
import { useHealthStore } from '@/stores/health'
import { useNotificationStore } from '@/stores/notification'

const store = useHealthStore()
const notif = useNotificationStore()

const info = ref('')
const testing = ref(false)

const draft = ref<Omit<HealthSettingsUpdateReq, 'workspace_id' | 'user_id'>>({
  sedentary_enabled: false,
  eye_enabled: false,
  night_mode: 'auto',
  blue_light_filter: false,
  stats_enabled: true,
})

const nightModes = [
  { value: 'auto', labelKey: 'health.nightAuto' },
  { value: 'light', labelKey: 'health.nightLight' },
  { value: 'dark', labelKey: 'health.nightDark' },
  { value: 'custom', labelKey: 'health.nightCustom' },
] as const

async function onLoad() {
  await store.refresh()
}

async function onSave() {
  info.value = ''
  store.error = ''
  try {
    await store.update({ ...draft.value })
    info.value = 'health.saved'
  } catch {
    // error already surfaced via store.error
  }
}

async function onTestSend() {
  testing.value = true
  store.error = ''
  info.value = ''
  try {
    await notif.testSend('health')
    info.value = 'health.testSent'
  } catch (e) {
    store.error = localizedMessageOf(e)
  } finally {
    testing.value = false
  }
}

function ratePercent(rate: number): string {
  return `${Math.round((rate ?? 0) * 100)}%`
}

onMounted(onLoad)
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('health.title') }}</h1>
        <div class="subtitle">{{ $t('health.subtitle') }}</div>
      </div>
      <button class="btn btn-sm" :disabled="store.loading" @click="onLoad">{{ $t('common.refresh') }}</button>
    </div>

    <div v-if="store.error" class="error-banner">{{ store.error }}</div>
    <div v-if="info" class="offline-banner">{{ $t(info) }}</div>

    <!-- settings card -->
    <div class="card">
      <div class="card-title">{{ $t('health.settingsTitle') }}</div>
      <div class="form-grid">
        <label class="check-line">
          <input v-model="draft.sedentary_enabled" type="checkbox" />
          <span>{{ $t('health.sedentaryEnabled') }}</span>
        </label>
        <label class="check-line">
          <input v-model="draft.eye_enabled" type="checkbox" />
          <span>{{ $t('health.eyeEnabled') }}</span>
        </label>
        <label class="field">
          <span>{{ $t('health.nightMode') }}</span>
          <select v-model="draft.night_mode" class="input">
            <option v-for="m in nightModes" :key="m.value" :value="m.value">{{ $t(m.labelKey) }}</option>
          </select>
        </label>
        <label class="check-line">
          <input v-model="draft.blue_light_filter" type="checkbox" />
          <span>{{ $t('health.blueLightFilter') }}</span>
        </label>
        <label class="check-line">
          <input v-model="draft.stats_enabled" type="checkbox" />
          <span>{{ $t('health.statsEnabled') }}</span>
        </label>
      </div>
      <div class="mt-2">
        <button class="btn btn-primary" :disabled="store.saving" @click="onSave">
          {{ store.saving ? $t('common.submitting') : $t('common.save') }}
        </button>
      </div>
    </div>

    <!-- stats card -->
    <div class="card">
      <div class="flex-between">
        <div class="card-title">{{ $t('health.statsTitle') }}</div>
        <button class="btn btn-sm" :disabled="testing" @click="onTestSend">
          {{ testing ? $t('common.submitting') : $t('health.testSend') }}
        </button>
      </div>
      <div v-if="store.loading" class="loading"><div class="spinner"></div></div>
      <div v-else-if="!store.stats" class="empty">{{ $t('health.statsEmpty') }}</div>
      <div v-else-if="!store.stats.stats_enabled" class="empty">{{ $t('health.statsDisabledHint') }}</div>
      <div v-else class="stats-grid">
        <div class="stat-box">
          <div class="stat-value">{{ store.stats.sedentary_count }}</div>
          <div class="stat-label">{{ $t('health.sedentaryCount') }}</div>
        </div>
        <div class="stat-box">
          <div class="stat-value">{{ ratePercent(store.stats.rest_completion_rate) }}</div>
          <div class="stat-label">{{ $t('health.restCompletionRate') }}</div>
        </div>
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

.check-line {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  font-size: var(--text-xs);
  color: var(--text-secondary);
  padding-bottom: var(--space-1);
}

.stats-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
  gap: var(--space-3);
}

.stat-box {
  padding: var(--space-3);
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  background: var(--bg-surface);
  text-align: center;
}

.stat-value {
  font-size: var(--text-2xl);
  font-weight: 700;
  color: var(--text);
}

.stat-label {
  font-size: var(--text-xs);
  color: var(--text-muted);
  margin-top: var(--space-1);
}
</style>
