<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { localizedMessageOf } from '@/api/client'
import { familyBind, familyUnbind, familyViewGet, parentSettingsUpdate } from '@/api/family'
import type { FamilyViewItem } from '@/api/types'
import { useI18nStore } from '@/stores/i18n'
import { useSessionStore } from '@/stores/session'

const i18n = useI18nStore()
const session = useSessionStore()

const error = ref('')
const info = ref('')
const loading = ref(false)
const saving = ref(false)

const items = ref<FamilyViewItem[]>([])
const bindCode = ref('')
const binding = ref(false)

async function load() {
  loading.value = true
  error.value = ''
  try {
    items.value = (await familyViewGet({ workspace_id: session.workspaceId, user_id: session.userId })) ?? []
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    loading.value = false
  }
}

async function doBind() {
  error.value = ''
  info.value = ''
  if (!bindCode.value.trim()) {
    error.value = i18n.t('family.bindCodeRequired')
    return
  }
  binding.value = true
  try {
    await familyBind({
      workspace_id: session.workspaceId,
      user_id: session.userId,
      invite_code: bindCode.value.trim(),
    })
    bindCode.value = ''
    info.value = i18n.t('family.bindDone')
    await load()
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    binding.value = false
  }
}

async function doUnbind(it: FamilyViewItem) {
  error.value = ''
  info.value = ''
  if (!window.confirm(i18n.t('family.unbindConfirm'))) return
  saving.value = true
  try {
    await familyUnbind({
      workspace_id: session.workspaceId,
      user_id: session.userId,
      binding_id: it.binding_id,
      version: 1,
    })
    info.value = i18n.t('family.unbound')
    await load()
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    saving.value = false
  }
}

async function doSaveSettings(it: FamilyViewItem) {
  error.value = ''
  info.value = ''
  saving.value = true
  try {
    const s = it.settings
    await parentSettingsUpdate({
      workspace_id: session.workspaceId,
      user_id: session.userId,
      student_user_id: it.student.user_id,
      daily_limit_min: s.daily_limit_min,
      ai_disabled: s.ai_disabled,
      report_enabled: s.report_enabled,
    })
    info.value = i18n.t('family.settingsSaved')
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    saving.value = false
  }
}

/** 0~1 浮点 → 百分数。 */
function pct(v: number): string {
  return `${Math.round(v * 100)}%`
}

onMounted(load)
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('family.title') }}</h1>
        <div class="subtitle">{{ $t('family.subtitle') }}</div>
      </div>
      <button class="btn btn-sm" :disabled="loading" @click="load">{{ $t('common.refresh') }}</button>
    </div>

    <div v-if="error" class="error-banner">{{ error }}</div>
    <div v-if="info" class="offline-banner">{{ info }}</div>

    <!-- 绑定学生 -->
    <div class="card">
      <div class="card-title">{{ $t('family.bindTitle') }}</div>
      <p class="text-secondary mb-3">{{ $t('family.bindHint') }}</p>
      <div class="flex gap-2">
        <input v-model="bindCode" class="input" style="max-width: 280px" :placeholder="$t('family.inviteCodePlaceholder')" />
        <button class="btn btn-primary" :disabled="binding" @click="doBind">
          {{ binding ? $t('common.processing') : $t('family.bindBtn') }}
        </button>
      </div>
    </div>

    <div v-if="loading" class="loading"><div class="spinner"></div></div>

    <div v-else-if="items.length === 0" class="card">
      <div class="empty">
        <div class="empty-icon">👨‍👩‍👧</div>
        <p>{{ $t('family.noStudents') }}</p>
      </div>
    </div>

    <div v-else class="family-list">
      <div v-for="it in items" :key="it.student.user_id" class="card family-card">
        <div class="flex-between mb-3">
          <div class="card-title" style="margin: 0">
            {{ $t('family.student') }}：{{ it.student.display_name }}
          </div>
          <button class="btn btn-sm btn-danger" :disabled="saving" @click="doUnbind(it)">
            {{ $t('family.unbind') }}
          </button>
        </div>

        <!-- 学习概览（G2 聚合卡片） -->
        <div class="stats-grid">
          <div class="stat">
            <div class="stat-value">{{ it.study_minutes.today }}</div>
            <div class="stat-label">{{ $t('family.todayMinutes') }}</div>
          </div>
          <div class="stat">
            <div class="stat-value">{{ it.study_minutes.week }}</div>
            <div class="stat-label">{{ $t('family.weekMinutes') }}</div>
          </div>
          <div class="stat">
            <div class="stat-value">{{ it.streak_days }}</div>
            <div class="stat-label">{{ $t('family.streakDays') }}</div>
          </div>
          <div class="stat">
            <div class="stat-value">{{ it.total_checkins }}</div>
            <div class="stat-label">{{ $t('family.totalCheckins') }}</div>
          </div>
          <div class="stat">
            <div class="stat-value">{{ it.task_summary.completed }}/{{ it.task_summary.total }}</div>
            <div class="stat-label">{{ $t('family.taskSummary') }}</div>
          </div>
          <div class="stat">
            <div class="stat-value">{{ pct(it.accuracy.rate) }}</div>
            <div class="stat-label">{{ $t('family.accuracy') }}</div>
          </div>
        </div>

        <!-- 薄弱知识点（仅聚合名称+错题数） -->
        <div class="mt-3">
          <div class="text-secondary mb-2">{{ $t('family.weakKnowledge') }}</div>
          <div v-if="(it.weak_knowledge ?? []).length === 0" class="hint">{{ $t('family.noWeak') }}</div>
          <div v-else class="weak-list">
            <span v-for="wk in (it.weak_knowledge ?? [])" :key="wk.knowledge_id" class="badge badge-warning">
              {{ wk.name }} · {{ wk.wrong_count }}
            </span>
          </div>
        </div>

        <!-- 使用限制设置 -->
        <div class="settings-panel">
          <div class="card-title" style="font-size: 1rem">{{ $t('family.settingsTitle') }}</div>
          <div class="form-row">
            <div class="field">
              <label>{{ $t('family.dailyLimitLabel') }}</label>
              <input v-model.number="it.settings.daily_limit_min" type="number" min="0" max="1440" class="input" style="max-width: 160px" />
              <div class="hint">{{ $t('family.dailyLimitZero') }}</div>
            </div>
          </div>
          <div class="switch-row">
            <label class="switch-label">
              <input v-model="it.settings.ai_disabled" type="checkbox" class="checkbox" />
              {{ $t('family.aiDisabledLabel') }}
            </label>
          </div>
          <div class="switch-row">
            <label class="switch-label">
              <input v-model="it.settings.report_enabled" type="checkbox" class="checkbox" />
              {{ $t('family.reportEnabledLabel') }}
            </label>
          </div>
          <div class="flex gap-2" style="justify-content: flex-end; margin-top: var(--space-3)">
            <button class="btn btn-primary" :disabled="saving" @click="doSaveSettings(it)">
              {{ saving ? $t('common.saving') : $t('family.saveSettings') }}
            </button>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.family-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}

.family-card {
  padding: var(--space-3);
}

.stats-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(120px, 1fr));
  gap: var(--space-2);
}

.stat {
  padding: var(--space-2);
  background: var(--bg-subtle);
  border-radius: var(--radius-sm);
  text-align: center;
}

.stat-value {
  font-size: var(--text-xl);
  font-weight: 700;
}

.stat-label {
  font-size: var(--text-xs);
  color: var(--text-secondary);
  margin-top: 2px;
}

.weak-list {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-1);
}

.settings-panel {
  margin-top: var(--space-3);
  padding-top: var(--space-3);
  border-top: 1px solid var(--border);
}

.switch-row {
  margin-bottom: var(--space-2);
}

.switch-label {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  cursor: pointer;
}

.checkbox {
  width: 16px;
  height: 16px;
}
</style>
