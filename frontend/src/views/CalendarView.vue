<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { localizedMessageOf } from '@/api/client'
import type { CalendarEntry, Milestone } from '@/api/types'
import { useI18nStore } from '@/stores/i18n'
import { useCalendarStore } from '@/stores/calendar'

const i18n = useI18nStore()
const store = useCalendarStore()

const error = ref('')
const info = ref('')

// 新建个人事件表单
const eventDialog = ref(false)
const eventForm = ref({
  title: '',
  event_date: '',
  start_time: '',
  duration_min: 60,
})

// 新建里程碑表单
const milestoneForm = ref({
  goal_id: '',
  title: '',
  due_at: '',
  criteria_type: 'practice' as 'practice' | 'tasks',
  count: 10,
  min_accuracy: 0.8,
})

/** 本组件内新建的里程碑（后端无 MilestoneList，仅展示本次会话创建的）。 */
const createdMilestones = ref<Milestone[]>([])

const kindLabelKey: Record<string, string> = {
  task: 'calendar.kindTask',
  review: 'calendar.kindReview',
  exam: 'calendar.kindExam',
  checkin: 'calendar.kindCheckin',
  focus: 'calendar.kindFocus',
  personal: 'calendar.kindPersonal',
}

const kindIcon: Record<string, string> = {
  task: '📝',
  review: '🔁',
  exam: '📄',
  checkin: '✅',
  focus: '⏱️',
  personal: '⭐',
}

const kindClass: Record<string, string> = {
  task: 'kind-task',
  review: 'kind-review',
  exam: 'kind-exam',
  checkin: 'kind-checkin',
  focus: 'kind-focus',
  personal: 'kind-personal',
}

/** 本月日历网格（6 行 × 7 列，周一开头），每格含日期字符串。 */
interface DayCell {
  date: string // YYYY-MM-DD
  day: number
  inMonth: boolean
  isToday: boolean
  entries: CalendarEntry[]
}

function buildGrid(): DayCell[] {
  const [y, m] = store.month.split('-').map(Number)
  const first = new Date(y, m - 1, 1)
  // 周一开头：getDay() 0=周日 → (0+6)%7=6
  const lead = (first.getDay() + 6) % 7
  const daysInMonth = new Date(y, m, 0).getDate()
  const todayStr = new Date()
  const todayLocal = `${todayStr.getFullYear()}-${String(todayStr.getMonth() + 1).padStart(2, '0')}-${String(
    todayStr.getDate(),
  ).padStart(2, '0')}`
  const cells: DayCell[] = []
  const total = Math.ceil((lead + daysInMonth) / 7) * 7
  for (let i = 0; i < total; i++) {
    const offset = i - lead
    const d = new Date(y, m - 1, offset + 1)
    const date = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
    cells.push({
      date,
      day: d.getDate(),
      inMonth: offset >= 0 && offset < daysInMonth,
      isToday: date === todayLocal,
      entries: store.entriesByDate(date),
    })
  }
  return cells
}

const grid = computed(() => buildGrid())

const weekdayKeys = ['calendar.weekMon', 'calendar.weekTue', 'calendar.weekWed', 'calendar.weekThu', 'calendar.weekFri', 'calendar.weekSat', 'calendar.weekSun']

const monthLabel = computed(() => {
  const [y, m] = store.month.split('-').map(Number)
  return i18n.t('calendar.monthLabel', { year: String(y), month: String(m) })
})

function openAddEvent(day?: string) {
  eventForm.value = {
    title: '',
    event_date: day ?? store.currentMonth() + '-01',
    start_time: '',
    duration_min: 60,
  }
  eventDialog.value = true
}

async function saveEvent() {
  error.value = ''
  info.value = ''
  if (!eventForm.value.title.trim()) {
    error.value = `${i18n.t('calendar.eventTitle')} ${i18n.t('calendar.required')}`
    return
  }
  if (!eventForm.value.event_date) {
    error.value = `${i18n.t('calendar.eventDate')} ${i18n.t('calendar.required')}`
    return
  }
  try {
    await store.upsertEvent({
      kind: 'personal',
      event_date: eventForm.value.event_date,
      start_time: eventForm.value.start_time || null,
      duration_min: eventForm.value.duration_min,
      title: eventForm.value.title.trim(),
    })
    eventDialog.value = false
    info.value = i18n.t('common.complete')
  } catch (e) {
    error.value = localizedMessageOf(e)
  }
}

function openMilestone() {
  milestoneForm.value = {
    goal_id: store.goals[0]?.id ?? '',
    title: '',
    due_at: '',
    criteria_type: 'practice',
    count: 10,
    min_accuracy: 0.8,
  }
  store.milestoneDialog = true
}

async function saveMilestone() {
  error.value = ''
  info.value = ''
  if (!milestoneForm.value.goal_id) {
    error.value = `${i18n.t('calendar.milestoneGoal')} ${i18n.t('calendar.requiredSelect')}`
    return
  }
  if (!milestoneForm.value.title.trim()) {
    error.value = `${i18n.t('calendar.milestoneName')} ${i18n.t('calendar.required')}`
    return
  }
  if (!milestoneForm.value.due_at) {
    error.value = `${i18n.t('calendar.milestoneDue')} ${i18n.t('calendar.required')}`
    return
  }
  try {
    const m = await store.createMilestone({
      goal_id: milestoneForm.value.goal_id,
      title: milestoneForm.value.title.trim(),
      due_at: new Date(milestoneForm.value.due_at).toISOString().slice(0, 10),
      criteria_json: {
        type: milestoneForm.value.criteria_type,
        count: milestoneForm.value.count,
        min_accuracy:
          milestoneForm.value.criteria_type === 'practice' ? milestoneForm.value.min_accuracy : undefined,
      },
    })
    createdMilestones.value.unshift(m)
    info.value = i18n.t('calendar.milestoneSaved')
  } catch (e) {
    error.value = localizedMessageOf(e)
  }
}

async function evaluate(m: Milestone) {
  error.value = ''
  info.value = ''
  try {
    const r = await store.evaluateMilestone(m.id)
    const idx = createdMilestones.value.findIndex((x) => x.id === r.id)
    if (idx >= 0) createdMilestones.value[idx] = r
    info.value = i18n.t('calendar.evaluated', { status: i18n.t(statusKey(r.status)) })
  } catch (e) {
    error.value = localizedMessageOf(e)
  }
}

function statusKey(s: string): string {
  return s === 'achieved'
    ? 'calendar.statusAchieved'
    : s === 'not_met'
      ? 'calendar.statusNotMet'
      : 'calendar.statusPending'
}

async function load() {
  store.init()
  error.value = ''
  info.value = ''
  try {
    await store.loadMonth()
    await store.loadGoals()
  } catch (e) {
    error.value = localizedMessageOf(e)
  }
}

async function goToday() {
  store.month = store.currentMonth()
  await store.loadMonth()
}

onMounted(load)
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('calendar.title') }}</h1>
        <div class="subtitle">{{ $t('calendar.subtitle') }}</div>
      </div>
      <button class="btn btn-sm" :disabled="store.loading" @click="load">{{ $t('common.refresh') }}</button>
    </div>

    <div v-if="error" class="error-banner">{{ error }}</div>
    <div v-if="info" class="offline-banner">{{ info }}</div>

    <!-- 月导航 -->
    <div class="toolbar">
      <button class="btn" :disabled="store.loading || store.busy" @click="store.shiftMonth(-1)">{{ $t('calendar.prevMonth') }}</button>
      <button class="btn btn-ghost" :disabled="store.loading || store.busy" @click="goToday">{{ $t('calendar.today') }}</button>
      <button class="btn" :disabled="store.loading || store.busy" @click="store.shiftMonth(1)">{{ $t('calendar.nextMonth') }}</button>
      <span class="month-label">{{ monthLabel }}</span>
      <span class="right">
        <button class="btn btn-primary" :disabled="store.loading || store.busy" @click="openAddEvent()">{{ $t('calendar.addEvent') }}</button>
        <button class="btn btn-ghost" :disabled="store.loading || store.busy || store.goals.length === 0" @click="openMilestone">{{ $t('calendar.newMilestone') }}</button>
      </span>
    </div>

    <div v-if="store.loading" class="loading"><div class="spinner"></div></div>

    <!-- 月网格 -->
    <div v-else class="card cal-card">
      <div class="cal-grid cal-head">
        <div v-for="d in weekdayKeys" :key="d" class="cal-head-cell">{{ $t(d) }}</div>
      </div>
      <div class="cal-grid">
        <div
          v-for="c in grid"
          :key="c.date"
          class="cal-cell"
          :class="{ 'out-month': !c.inMonth, today: c.isToday, clickable: c.inMonth }"
          :title="c.date"
          @click="c.inMonth && openAddEvent(c.date)"
        >
          <div class="cal-day">{{ c.day }}</div>
          <div v-if="c.entries.length > 0" class="cal-entries">
            <div
              v-for="(e, i) in c.entries.slice(0, 3)"
              :key="i"
              class="cal-entry"
              :class="kindClass[e.kind] ?? 'kind-personal'"
              :title="`${i18n.t(kindLabelKey[e.kind] ?? 'calendar.kindPersonal')} · ${e.title || e.event_date}`"
            >
              <span aria-hidden="true">{{ kindIcon[e.kind] ?? '⭐' }}</span>
              <span class="entry-text">{{ e.title || i18n.t(kindLabelKey[e.kind] ?? 'calendar.kindPersonal') }}</span>
            </div>
            <div v-if="c.entries.length > 3" class="cal-more">+{{ c.entries.length - 3 }}</div>
          </div>
        </div>
      </div>
      <div v-if="store.entries.length === 0" class="empty">
        <div class="empty-icon">🗓️</div>
        <p>{{ $t('calendar.emptyMonth') }}</p>
      </div>
    </div>

    <!-- 里程碑卡片 -->
    <div class="card">
      <div class="flex-between">
        <div>
          <div class="card-title">{{ $t('calendar.milestoneTitle') }}</div>
          <div class="hint">{{ $t('calendar.milestoneSubtitle') }}</div>
        </div>
        <button class="btn btn-sm" :disabled="store.busy || store.goals.length === 0" @click="openMilestone">
          {{ $t('calendar.newMilestone') }}
        </button>
      </div>
      <div v-if="store.goals.length === 0" class="empty">
        <div class="empty-icon">🎯</div>
        <p>{{ $t('calendar.noGoals') }}</p>
      </div>
      <div v-else-if="createdMilestones.length === 0" class="empty">
        <div class="empty-icon">🎯</div>
        <p>{{ $t('calendar.milestoneSaved') }}</p>
      </div>
      <div v-else class="milestone-list">
        <div v-for="m in createdMilestones" :key="m.id" class="milestone-row">
          <div class="milestone-info">
            <div class="milestone-title">{{ m.title }}</div>
            <div class="hint">
              {{ $t('calendar.milestoneDue') }} {{ m.due_at.slice(0, 10) }} ·
              <span v-if="m.criteria_json.type === 'practice'">
                {{ $t('calendar.criteriaHintPractice', { count: String(m.criteria_json.count) }) }}
                <template v-if="m.criteria_json.min_accuracy != null"> · ≥ {{ m.criteria_json.min_accuracy * 100 }}%</template>
              </span>
              <span v-else>{{ $t('calendar.criteriaHintTasks', { count: String(m.criteria_json.count) }) }}</span>
            </div>
          </div>
          <span
            class="badge"
            :class="m.status === 'achieved' ? 'badge-success' : m.status === 'not_met' ? 'badge-error' : 'badge-warning'"
          >{{ $t(statusKey(m.status)) }}</span>
          <button
            v-if="m.status === 'pending'"
            class="btn btn-sm btn-primary"
            :disabled="store.busy"
            @click="evaluate(m)"
          >
            {{ store.busy ? $t('calendar.evaluating') : $t('calendar.evaluate') }}
          </button>
        </div>
      </div>
    </div>

    <!-- 新建个人事件弹窗 -->
    <div v-if="eventDialog" class="modal-mask">
      <div class="card modal">
        <h3>{{ $t('calendar.addEvent') }}</h3>
        <div class="field">
          <label>{{ $t('calendar.eventTitle') }}</label>
          <input v-model="eventForm.title" class="input" type="text" :placeholder="$t('calendar.eventTitlePlaceholder')" />
        </div>
        <div class="form-row">
          <div class="field">
            <label>{{ $t('calendar.eventDate') }}</label>
            <input v-model="eventForm.event_date" class="input" type="date" />
          </div>
          <div class="field">
            <label>{{ $t('calendar.eventTime') }}</label>
            <input v-model="eventForm.start_time" class="input" type="time" />
          </div>
          <div class="field">
            <label>{{ $t('calendar.eventDuration') }}</label>
            <input v-model.number="eventForm.duration_min" class="input" type="number" min="0" max="1440" />
          </div>
        </div>
        <div class="flex gap-3 mt-3">
          <button class="btn" :disabled="store.busy" @click="eventDialog = false">{{ $t('common.cancel') }}</button>
          <button class="btn btn-primary" :disabled="store.busy" @click="saveEvent">
            {{ store.busy ? $t('common.submitting') : $t('calendar.saveEvent') }}
          </button>
        </div>
      </div>
    </div>

    <!-- 新建里程碑弹窗 -->
    <div v-if="store.milestoneDialog" class="modal-mask">
      <div class="card modal">
        <h3>{{ $t('calendar.newMilestone') }}</h3>
        <div class="field">
          <label>{{ $t('calendar.milestoneGoal') }}</label>
          <select v-model="milestoneForm.goal_id" class="select">
            <option v-for="g in store.goals" :key="g.id" :value="g.id">{{ g.name }}</option>
          </select>
        </div>
        <div class="field">
          <label>{{ $t('calendar.milestoneName') }}</label>
          <input v-model="milestoneForm.title" class="input" type="text" />
        </div>
        <div class="form-row">
          <div class="field">
            <label>{{ $t('calendar.milestoneDue') }}</label>
            <input v-model="milestoneForm.due_at" class="input" type="date" />
          </div>
          <div class="field">
            <label>{{ $t('calendar.criteriaType') }}</label>
            <select v-model="milestoneForm.criteria_type" class="select">
              <option value="practice">{{ $t('calendar.criteriaPractice') }}</option>
              <option value="tasks">{{ $t('calendar.criteriaTasks') }}</option>
            </select>
          </div>
          <div class="field">
            <label>{{ $t('calendar.criteriaCount') }}</label>
            <input v-model.number="milestoneForm.count" class="input" type="number" min="1" />
          </div>
          <div v-if="milestoneForm.criteria_type === 'practice'" class="field">
            <label>{{ $t('calendar.criteriaAccuracy') }}</label>
            <input v-model.number="milestoneForm.min_accuracy" class="input" type="number" step="0.05" min="0" max="1" />
          </div>
        </div>
        <div v-if="milestoneForm.criteria_type === 'practice'" class="hint">
          {{ $t('calendar.criteriaAccuracyHint') }} ·
          {{ $t('calendar.criteriaHintPractice', { count: String(milestoneForm.count) }) }}
        </div>
        <div v-else class="hint">{{ $t('calendar.criteriaHintTasks', { count: String(milestoneForm.count) }) }}</div>
        <div class="flex gap-3 mt-3">
          <button class="btn" :disabled="store.busy" @click="store.milestoneDialog = false">{{ $t('common.cancel') }}</button>
          <button class="btn btn-primary" :disabled="store.busy" @click="saveMilestone">
            {{ store.busy ? $t('common.creating') : $t('calendar.createMilestone') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.month-label {
  font-weight: 600;
  font-size: var(--text-lg);
  margin-left: var(--space-2);
}

.cal-card {
  overflow: hidden;
}

.cal-grid {
  display: grid;
  grid-template-columns: repeat(7, 1fr);
  gap: 2px;
}

.cal-head {
  margin-bottom: 2px;
}

.cal-head-cell {
  text-align: center;
  font-size: var(--text-sm);
  font-weight: 600;
  color: var(--text-secondary);
  padding: var(--space-1) 0;
}

.cal-cell {
  min-height: 84px;
  padding: 4px;
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  background: var(--bg-surface);
  display: flex;
  flex-direction: column;
  gap: 2px;
  overflow: hidden;
}

.cal-cell.out-month {
  background: var(--bg-subtle);
  opacity: 0.55;
}

.cal-cell.today {
  border-color: var(--color-primary);
  box-shadow: inset 0 0 0 1px var(--color-primary);
}

.cal-cell.clickable {
  cursor: pointer;
}

.cal-cell.clickable:hover {
  border-color: var(--color-primary);
}

.cal-day {
  font-size: var(--text-sm);
  font-weight: 600;
  color: var(--text-secondary);
  line-height: 1.4;
}

.cal-cell.today .cal-day {
  color: var(--color-primary);
}

.cal-entries {
  display: flex;
  flex-direction: column;
  gap: 1px;
}

.cal-entry {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: var(--text-xs);
  padding: 1px 4px;
  border-radius: 4px;
  white-space: nowrap;
  overflow: hidden;
}

.entry-text {
  overflow: hidden;
  text-overflow: ellipsis;
}

.kind-task {
  background: var(--color-primary-soft);
  color: var(--color-primary);
}

.kind-review {
  background: var(--color-warning-soft);
  color: var(--color-warning);
}

.kind-exam {
  background: var(--color-success-soft);
  color: var(--color-success);
}

.kind-checkin {
  background: var(--bg-subtle);
  color: var(--color-success);
}

.kind-focus {
  background: var(--bg-subtle);
  color: var(--text-secondary);
}

.kind-personal {
  background: var(--gradient-soft);
  color: var(--color-primary);
}

.cal-more {
  font-size: var(--text-xs);
  color: var(--text-muted);
  text-align: center;
}

.milestone-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}

.milestone-row {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  padding: var(--space-3);
  border: 1px solid var(--border);
  border-radius: var(--radius);
}

.milestone-info {
  flex: 1;
  min-width: 0;
}

.milestone-title {
  font-weight: 600;
}

.modal-mask {
  position: fixed;
  inset: 0;
  background: var(--overlay);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 100;
}

.modal {
  width: min(560px, 92vw);
}
</style>
