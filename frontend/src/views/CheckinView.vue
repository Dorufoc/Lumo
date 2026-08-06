<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { localizedMessageOf } from '@/api/client'
import { useI18nStore } from '@/stores/i18n'
import { useCheckinStore } from '@/stores/checkin'

const i18n = useI18nStore()
const store = useCheckinStore()

const loading = ref(true)
const error = ref('')
const info = ref('')

const checking = ref(false)
const making = ref(false)
const makeupDate = ref('')

const todayStr = fmtDate(new Date())

const checkedToday = computed(() => {
  if (store.today) return true
  const st = store.streak
  return !!st && st.streak > 0 && st.date === todayStr
})

// 由 streak 锚点 + 连击天数反推已打卡日期集合（周视图高亮）
const checkedDates = computed(() => {
  const set = new Set<string>()
  if (store.today) set.add(store.today.date)
  const st = store.streak
  if (st && st.streak > 0 && st.date) {
    const anchor = new Date(`${st.date}T00:00:00`)
    for (let i = 0; i < st.streak; i++) {
      set.add(fmtDate(addDays(anchor, -i)))
    }
  }
  return set
})

const week = computed(() => {
  const out: { date: string; dayKey: string; checked: boolean; today: boolean }[] = []
  for (let i = 6; i >= 0; i--) {
    const d = addDays(new Date(), -i)
    const dateStr = fmtDate(d)
    out.push({
      date: dateStr,
      dayKey: dayKeys[d.getDay()],
      checked: checkedDates.value.has(dateStr),
      today: dateStr === todayStr,
    })
  }
  return out
})

const makeupMax = computed(() => fmtDate(addDays(new Date(), -1)))

async function load() {
  loading.value = true
  error.value = ''
  info.value = ''
  try {
    store.restoreToday(todayStr)
    await store.load()
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    loading.value = false
  }
}

async function doCheckin() {
  checking.value = true
  error.value = ''
  info.value = ''
  const prevCodes = new Set(store.achievements.filter((a) => a.is_unlocked).map((a) => a.code))
  try {
    const c = await store.checkin()
    const fresh = store.achievements.filter((a) => a.is_unlocked && !prevCodes.has(a.code))
    info.value =
      fresh.length > 0
        ? i18n.t('checkin.achievementUnlocked', { title: i18n.t(fresh[0].title_key) })
        : i18n.t('checkin.checked', { date: c.date })
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    checking.value = false
  }
}

async function doMakeup() {
  if (!makeupDate.value) return
  making.value = true
  error.value = ''
  info.value = ''
  try {
    const c = await store.makeup(makeupDate.value)
    info.value = i18n.t('checkin.makeupDone', { date: c.date })
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    making.value = false
  }
}

function fmtDate(d: Date): string {
  const m = d.getMonth() + 1
  const day = d.getDate()
  return `${d.getFullYear()}-${m < 10 ? '0' : ''}${m}-${day < 10 ? '0' : ''}${day}`
}

function addDays(d: Date, n: number): Date {
  const x = new Date(d)
  x.setDate(x.getDate() + n)
  return x
}

// 周日→周六（JS Date.getDay() 索引），复用计划页周标签
const dayKeys = [
  'plan.weekSun',
  'plan.weekMon',
  'plan.weekTue',
  'plan.weekWed',
  'plan.weekThu',
  'plan.weekFri',
  'plan.weekSat',
]

onMounted(load)
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('checkin.title') }}</h1>
        <div class="subtitle">{{ $t('checkin.subtitle') }}</div>
      </div>
      <button class="btn btn-sm" :disabled="loading" @click="load">{{ $t('common.refresh') }}</button>
    </div>

    <div v-if="error" class="error-banner">{{ error }}</div>
    <div v-if="info" class="offline-banner">{{ info }}</div>

    <div v-if="loading" class="loading"><div class="spinner"></div></div>

    <template v-else>
      <!-- 连击主卡 -->
      <div class="card">
        <div class="flex-between">
          <div>
            <div class="streak-value">{{ store.streak?.streak ?? 0 }}</div>
            <div class="stat-label">{{ $t('checkin.streakLabel') }}</div>
            <div class="hint mt-2">
              {{ $t('checkin.totalLabel') }} {{ store.streak?.total_checkins ?? 0 }} {{ $t('checkin.timesUnit') }}
            </div>
          </div>
          <div class="checkin-actions">
            <button v-if="!checkedToday" class="btn btn-primary" :disabled="checking" @click="doCheckin">
              {{ checking ? $t('common.submitting') : $t('checkin.checkinButton') }}
            </button>
            <button v-else class="btn btn-success" :disabled="checking" @click="doCheckin">
              {{ $t('checkin.checkedToday') }}
            </button>
            <div class="hint">{{ $t('checkin.checkinHint') }}</div>
          </div>
        </div>

        <!-- 最近 7 天 -->
        <div class="week-strip">
          <div
            v-for="day in week"
            :key="day.date"
            class="week-cell"
            :class="{ checked: day.checked, today: day.today }"
          >
            <div class="week-day">{{ $t(day.dayKey) }}</div>
            <div class="week-dot"></div>
            <div class="week-date">{{ day.date.slice(5) }}</div>
          </div>
        </div>
      </div>

      <!-- 补签 -->
      <div class="card">
        <div class="card-title">{{ $t('checkin.makeupTitle') }}</div>
        <div class="flex gap-3">
          <input v-model="makeupDate" type="date" class="input makeup-input" :max="makeupMax" />
          <button class="btn" :disabled="making || !makeupDate" @click="doMakeup">
            {{ making ? $t('common.submitting') : $t('checkin.makeupButton') }}
          </button>
        </div>
        <div class="hint mt-2">{{ $t('checkin.makeupHint') }}</div>
      </div>

      <!-- 成就墙 -->
      <div class="card">
        <div class="card-title">{{ $t('checkin.achievementsTitle') }}</div>
        <div v-if="store.achievements.length === 0" class="empty">
          <div class="empty-icon">🏅</div>
          <p>{{ $t('checkin.achievementsEmpty') }}</p>
        </div>
        <div v-else class="grid grid-3">
          <div v-for="a in store.achievements" :key="a.id" class="ach-card" :class="{ locked: !a.is_unlocked }">
            <div class="ach-icon">{{ a.icon }}</div>
            <div class="ach-title">{{ $t(a.title_key) }}</div>
            <div class="ach-desc">{{ $t(a.description_key) }}</div>
            <div class="mt-2">
              <span v-if="a.is_unlocked" class="badge badge-success">{{ $t('achievement.unlocked') }}</span>
              <span v-else class="badge badge-offline">{{ $t('achievement.locked') }}</span>
            </div>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.streak-value {
  font-size: var(--text-4xl, 48px);
  font-weight: 700;
  line-height: 1.1;
  background: var(--gradient);
  -webkit-background-clip: text;
  background-clip: text;
  color: transparent;
}

.checkin-actions {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: var(--space-2);
}

.week-strip {
  display: flex;
  gap: var(--space-2);
  margin-top: var(--space-4);
}

.week-cell {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: var(--space-1);
  padding: var(--space-2) 0;
  border-radius: var(--radius-sm);
  border: 1px solid var(--border);
  background: var(--bg-surface);
}

.week-cell.today {
  border-color: var(--color-primary);
}

.week-day {
  font-size: var(--text-xs);
  color: var(--text-secondary);
}

.week-dot {
  width: 14px;
  height: 14px;
  border-radius: 50%;
  border: 2px solid var(--border-strong);
  background: var(--bg-surface);
}

.week-cell.checked .week-dot {
  background: var(--gradient);
  border-color: transparent;
  box-shadow: var(--glow-primary);
}

.week-date {
  font-size: var(--text-xs);
  color: var(--text-muted);
}

.ach-card {
  display: flex;
  flex-direction: column;
  align-items: center;
  text-align: center;
  gap: var(--space-1);
  padding: var(--space-4) var(--space-3);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--bg-surface);
}

.ach-card.locked {
  opacity: 0.55;
}

.ach-icon {
  font-size: 32px;
  line-height: 1;
}

.ach-card.locked .ach-icon {
  filter: grayscale(1);
}

.ach-title {
  font-weight: 600;
  font-size: var(--text-base);
}

.ach-desc {
  font-size: var(--text-xs);
  color: var(--text-secondary);
}

.makeup-input {
  max-width: 220px;
}
</style>
