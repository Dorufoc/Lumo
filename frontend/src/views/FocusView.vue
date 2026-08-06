<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { localizedMessageOf, ApiException } from '@/api/client'
import { useI18nStore } from '@/stores/i18n'
import { useFocusStore } from '@/stores/focus'

const i18n = useI18nStore()
const store = useFocusStore()

const loading = ref(true)
const error = ref('')
const info = ref('')

const mode = ref<'pomodoro' | 'free'>('pomodoro')
const plannedMinutes = ref(25)

const busy = computed(() => store.busy)
const active = computed(() => store.active)

const endDialog = ref(false)
const selectedReason = ref('')
const reasonOptions = [
  { id: 'rest', labelKey: 'focus.reasonRest' },
  { id: 'leave', labelKey: 'focus.reasonLeave' },
  { id: 'distracted', labelKey: 'focus.reasonDistracted' },
  { id: 'other', labelKey: 'focus.reasonOther' },
  { id: '__none__', labelKey: 'focus.reasonNone' },
]

// 客户端计时（仅展示；服务端以 started_at/ended_at 为准）
const nowMs = ref(Date.now())
let ticker: ReturnType<typeof setInterval> | undefined

const elapsed = computed(() => {
  const a = active.value
  if (!a?.started_at) return 0
  const start = new Date(a.started_at).getTime()
  return Number.isNaN(start) ? 0 : Math.max(0, Math.floor((nowMs.value - start) / 1000))
})

const remaining = computed(() => {
  const a = active.value
  if (!a || a.planned_minutes <= 0) return 0
  const start = a.started_at ? new Date(a.started_at).getTime() : 0
  if (Number.isNaN(start)) return 0
  return Math.max(0, Math.round((start + a.planned_minutes * 60000 - nowMs.value) / 1000))
})

const displaySeconds = computed(() => {
  if (!active.value) return plannedMinutes.value * 60
  return active.value.planned_minutes > 0 ? remaining.value : elapsed.value
})

function fmtClock(sec: number): string {
  const s = Math.max(0, Math.floor(sec))
  const mm = Math.floor(s / 60)
  const ss = s % 60
  return `${mm < 10 ? '0' : ''}${mm}:${ss < 10 ? '0' : ''}${ss}`
}

const statsDuration = computed(() => {
  const sec = store.stats?.total_seconds ?? 0
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (h > 0) return i18n.t('focus.hoursMinutes', { hours: String(h), minutes: String(m) })
  return i18n.t('focus.minutesOnly', { minutes: String(m) })
})

const interruptionRateText = computed(() => {
  const r = store.stats?.interruption_rate ?? 0
  return `${(r * 100).toFixed(1)}%`
})

// 切到番茄钟时若时长为 0（自由不限时遗留）重置为默认 25
watch(mode, (m) => {
  if (m === 'pomodoro' && (plannedMinutes.value < 1 || plannedMinutes.value > 120)) {
    plannedMinutes.value = 25
  }
})

async function load() {
  loading.value = true
  error.value = ''
  info.value = ''
  try {
    store.restoreActive()
    await store.load()
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    loading.value = false
  }
}

async function doStart() {
  error.value = ''
  info.value = ''
  try {
    await store.start(mode.value, plannedMinutes.value)
    info.value = i18n.t('focus.started')
  } catch (e) {
    if (e instanceof ApiException && e.code === 'INVALID_STATE' && store.stale) {
      error.value = i18n.t('focus.staleFound')
    } else {
      error.value = localizedMessageOf(e)
    }
  }
}

function doStop() {
  // 计时已走完 → 直接正常结束；否则提前结束需选择原因
  if (remaining.value <= 0) {
    void finishEnd('')
    return
  }
  selectedReason.value = ''
  endDialog.value = true
}

async function finishEnd(reason: string) {
  endDialog.value = false
  error.value = ''
  info.value = ''
  try {
    const t = await store.end(reason)
    info.value = i18n.t(statusInfoKey[t.status] ?? 'focus.endedAbandoned')
  } catch (e) {
    error.value = localizedMessageOf(e)
  }
}

const statusInfoKey: Record<string, string> = {
  completed: 'focus.endedCompleted',
  interrupted: 'focus.endedInterrupted',
  abandoned: 'focus.endedAbandoned',
}

function confirmEnd() {
  void finishEnd(selectedReason.value === '__none__' ? '' : selectedReason.value)
}

async function recoverStale() {
  const st = store.stale
  if (!st) return
  error.value = ''
  info.value = ''
  try {
    await store.endStale(st.session_id)
    info.value = i18n.t('focus.staleEnded')
  } catch (e) {
    error.value = localizedMessageOf(e)
  }
}

onMounted(() => {
  ticker = setInterval(() => {
    nowMs.value = Date.now()
  }, 1000)
  void load()
})

onUnmounted(() => {
  if (ticker) clearInterval(ticker)
})
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('focus.title') }}</h1>
        <div class="subtitle">{{ $t('focus.subtitle') }}</div>
      </div>
      <button class="btn btn-sm" :disabled="loading" @click="load">{{ $t('common.refresh') }}</button>
    </div>

    <div v-if="error" class="error-banner">{{ error }}</div>
    <div v-if="info" class="offline-banner">{{ info }}</div>

    <div v-if="store.stale" class="card stale-card">
      <div class="flex-between">
        <div>
          <div class="card-title">{{ $t('focus.staleFound') }}</div>
          <div class="hint">{{ $t('focus.staleHint') }}</div>
        </div>
        <button class="btn" :disabled="busy" @click="recoverStale">{{ $t('focus.staleEnd') }}</button>
      </div>
    </div>

    <div v-if="loading" class="loading"><div class="spinner"></div></div>

    <template v-else>
      <!-- 计时主卡 -->
      <div class="card timer-card">
        <div class="flex-between">
          <div>
            <div class="card-title">{{ $t('focus.timerTitle') }}</div>
            <div v-if="active" class="hint">{{ $t('focus.running') }} · {{ $t('focus.startedAt') }} {{ (active.started_at ?? '').slice(11, 19) }}</div>
          </div>
          <span v-if="active" class="badge badge-success">{{ $t('focus.running') }}</span>
        </div>

        <div class="clock" :class="{ running: !!active }">{{ fmtClock(displaySeconds) }}</div>

        <template v-if="!active">
          <!-- 模式切换 -->
          <div class="mode-toggle">
            <button
              class="btn"
              :class="{ 'btn-primary': mode === 'pomodoro' }"
              @click="mode = 'pomodoro'"
            >
              {{ $t('focus.modePomodoro') }}
            </button>
            <button class="btn" :class="{ 'btn-primary': mode === 'free' }" @click="mode = 'free'">
              {{ $t('focus.modeFree') }}
            </button>
          </div>

          <!-- 计划时长 -->
          <div class="plan-row">
            <template v-if="mode === 'pomodoro'">
              <input
                v-model.number="plannedMinutes"
                type="range"
                min="5"
                max="120"
                step="5"
                class="slider"
              />
              <div class="plan-value">{{ plannedMinutes }} {{ $t('focus.minutesUnit') }}</div>
            </template>
            <template v-else>
              <div class="plan-value">{{ $t('focus.plannedLabel') }}</div>
              <input
                v-model.number="plannedMinutes"
                type="number"
                min="0"
                max="1440"
                class="input plan-number"
              />
              <div class="plan-value">{{ $t('focus.minutesUnit') }}（0 = {{ $t('focus.untimed') }}）</div>
            </template>
          </div>
          <div class="hint">{{ $t('focus.plannedHint') }}</div>

          <button class="btn btn-primary btn-lg" :disabled="busy" @click="doStart">
            {{ busy ? $t('common.submitting') : $t('focus.start') }}
          </button>
        </template>

        <template v-else>
          <div class="hint mb-2">
            {{ active.planned_minutes > 0 ? $t('focus.remaining') : $t('focus.elapsedLabel') }} ·
            {{ $t('focus.plannedValue', { minutes: String(active.planned_minutes) }) }}
          </div>
          <button class="btn btn-error btn-lg" :disabled="busy" @click="doStop">
            {{ $t('focus.stop') }}
          </button>
        </template>
      </div>

      <!-- 中断原因对话框 -->
      <div v-if="endDialog" class="modal-mask">
        <div class="modal">
          <div class="modal-title">{{ $t('focus.endTitle') }}</div>
          <div class="hint mb-2">{{ $t('focus.endHint') }}</div>
          <div class="reason-grid">
            <button
              v-for="opt in reasonOptions"
              :key="opt.id"
              class="btn reason-btn"
              :class="{ 'btn-primary': selectedReason === opt.id }"
              @click="selectedReason = opt.id"
            >
              {{ $t(opt.labelKey) }}
            </button>
          </div>
          <div class="flex gap-3 mt-3">
            <button class="btn" :disabled="busy" @click="endDialog = false">{{ $t('focus.cancelEnd') }}</button>
            <button class="btn btn-primary" :disabled="busy || selectedReason === ''" @click="confirmEnd">
              {{ $t('focus.confirmEnd') }}
            </button>
          </div>
        </div>
      </div>

      <!-- 统计卡 -->
      <div class="card">
        <div class="card-title">{{ $t('focus.statsTitle') }}</div>
        <div v-if="!store.stats || store.stats.total_sessions === 0" class="empty">
          <div class="empty-icon">⏱️</div>
          <p>{{ $t('focus.statsEmpty') }}</p>
        </div>
        <div v-else class="stats-grid">
          <div class="stat-cell">
            <div class="stat-value">{{ statsDuration }}</div>
            <div class="stat-label">{{ $t('focus.statsTotal') }}</div>
          </div>
          <div class="stat-cell">
            <div class="stat-value">{{ store.stats.total_sessions }}</div>
            <div class="stat-label">{{ $t('focus.statsSessions') }}</div>
          </div>
          <div class="stat-cell">
            <div class="stat-value">{{ interruptionRateText }}</div>
            <div class="stat-label">{{ $t('focus.statsRate') }}</div>
          </div>
          <div class="stat-cell">
            <div class="stat-value breakdown">
              <span class="ok">{{ store.stats.completed_sessions }}</span> /
              <span class="warn">{{ store.stats.interrupted_sessions }}</span> /
              <span class="muted">{{ store.stats.abandoned_sessions }}</span>
            </div>
            <div class="stat-label">
              {{ $t('focus.statsCompleted') }} / {{ $t('focus.statsInterrupted') }} / {{ $t('focus.statsAbandoned') }}
            </div>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.timer-card {
  text-align: center;
}

.clock {
  font-size: var(--text-4xl, 48px);
  font-weight: 700;
  font-variant-numeric: tabular-nums;
  letter-spacing: 2px;
  margin: var(--space-4) 0;
  color: var(--text);
}

.clock.running {
  background: var(--gradient);
  -webkit-background-clip: text;
  background-clip: text;
  color: transparent;
}

.mode-toggle {
  display: flex;
  justify-content: center;
  gap: var(--space-2);
  margin-bottom: var(--space-4);
}

.plan-row {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: var(--space-3);
  margin-bottom: var(--space-2);
}

.slider {
  flex: 1;
  max-width: 320px;
  accent-color: var(--color-primary);
}

.plan-value {
  font-weight: 600;
  white-space: nowrap;
}

.plan-number {
  width: 90px;
  text-align: center;
}

.btn-lg {
  min-width: 180px;
  margin-top: var(--space-3);
}

.stale-card {
  border-color: var(--color-warning);
}

.reason-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: var(--space-2);
}

.reason-btn {
  justify-content: center;
}

.stats-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: var(--space-3);
}

.stat-cell {
  text-align: center;
  padding: var(--space-3);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--bg-surface);
}

.stat-value {
  font-size: var(--text-2xl, 24px);
  font-weight: 700;
}

.stat-value.breakdown {
  font-size: var(--text-xl, 20px);
}

.stat-value .ok {
  color: var(--color-success);
}

.stat-value .warn {
  color: var(--color-warning);
}

.stat-value .muted {
  color: var(--text-muted);
}

.stat-label {
  font-size: var(--text-xs);
  color: var(--text-secondary);
  margin-top: var(--space-1);
}

.mb-2 {
  margin-bottom: var(--space-2);
}

.mt-3 {
  margin-top: var(--space-3);
}
</style>
