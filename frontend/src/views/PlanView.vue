<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { call, localizedMessageOf } from '@/api/client'
import type { LearningGoal, PlanTask } from '@/api/types'
import { useSessionStore } from '@/stores/session'
import { useRouter } from 'vue-router'
const router = useRouter()
const session = useSessionStore()

const goals = ref<LearningGoal[]>([])
const todayTasks = ref<PlanTask[]>([])
const loading = ref(true)
const error = ref('')
const showCreate = ref(false)

const form = ref({
  name: '',
  exam_at: '',
  target_score: 85,
  daily_minutes: 60,
  weekdays: [1, 2, 3, 4, 5, 6, 7],
})
const creating = ref(false)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [g, t] = await Promise.all([
      call<LearningGoal[]>('GoalList', { workspace_id: session.workspaceId, user_id: session.userId }),
      call<PlanTask[]>('PlanListToday', { workspace_id: session.workspaceId, user_id: session.userId }),
    ])
    goals.value = g ?? []
    todayTasks.value = t ?? []
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    loading.value = false
  }
}

async function createGoal() {
  if (!form.value.name.trim()) return
  creating.value = true
  error.value = ''
  try {
    await call<LearningGoal>('GoalCreate', {
      workspace_id: session.workspaceId,
      user_id: session.userId,
      name: form.value.name,
      exam_at: form.value.exam_at ? new Date(form.value.exam_at).toISOString() : undefined,
      target_score: form.value.target_score,
      daily_minutes: form.value.daily_minutes,
      available_weekdays: form.value.weekdays,
      idempotency_key: `goal-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`,
    })
    showCreate.value = false
    form.value.name = ''
    await load()
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    creating.value = false
  }
}

async function transitionGoal(goal: LearningGoal, action: string) {
  try {
    await call<LearningGoal>('GoalTransition', {
      workspace_id: session.workspaceId,
      goal_id: goal.id,
      version: goal.version,
      action,
    })
    await load()
  } catch (e) {
    error.value = localizedMessageOf(e)
  }
}

async function generatePlan(goal: LearningGoal) {
  try {
    await call<PlanTask[]>('PlanGenerate', {
      workspace_id: session.workspaceId,
      goal_id: goal.id,
      idempotency_key: `plan-${goal.id}-${Date.now()}`,
    })
    await load()
  } catch (e) {
    error.value = localizedMessageOf(e)
  }
}

async function taskAction(task: PlanTask, action: string) {
  try {
    await call<PlanTask>('PlanTaskTransition', {
      workspace_id: session.workspaceId,
      task_id: task.id,
      version: task.version,
      action,
    })
    await load()
  } catch (e) {
    error.value = localizedMessageOf(e)
  }
}

const weekdayNames = ['', 'plan.weekMon', 'plan.weekTue', 'plan.weekWed', 'plan.weekThu', 'plan.weekFri', 'plan.weekSat', 'plan.weekSun']
const statusBadge = (s: string) =>
  ({ planned: 'badge', available: 'badge', in_progress: 'badge-primary', completed: 'badge-success', skipped: 'badge-offline' })[s] ?? 'badge'

const activeGoals = computed(() => goals.value.filter((g) => !['completed', 'archived'].includes(g.status)))

onMounted(load)
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('plan.title') }}</h1>
        <div class="subtitle">{{ $t('plan.subtitle') }}</div>
      </div>
      <button class="btn btn-primary" @click="showCreate = !showCreate">{{ $t('plan.newGoal') }}</button>
    </div>

    <div v-if="error" class="error-banner">{{ error }}</div>
    <div v-if="loading" class="loading"><div class="spinner"></div></div>

    <div v-if="showCreate" class="card">
      <h3>{{ $t('plan.createGoalTitle') }}</h3>
      <div class="form-row">
        <div class="field">
          <label>{{ $t('plan.goalName') }}</label>
          <input v-model="form.name" class="input" :placeholder="$t('plan.goalPlaceholder')" maxlength="160" />
        </div>
        <div class="field">
          <label>{{ $t('plan.examDate') }}</label>
          <input v-model="form.exam_at" type="date" class="input" />
        </div>
      </div>
      <div class="form-row">
        <div class="field">
          <label>{{ $t('plan.targetScore') }}</label>
          <input v-model.number="form.target_score" type="number" class="input" min="0" max="100" />
        </div>
        <div class="field">
          <label>{{ $t('plan.dailyMinutes') }}</label>
          <input v-model.number="form.daily_minutes" type="number" class="input" min="10" max="480" />
        </div>
      </div>
      <div class="field">
        <label>{{ $t('plan.availableWeekdays') }}</label>
        <div class="flex gap-2" style="flex-wrap: wrap">
          <button
            v-for="(name, i) in weekdayNames.slice(1)"
            :key="i + 1"
            type="button"
            class="btn btn-sm"
            :class="{ 'btn-primary': form.weekdays.includes(i + 1) }"
            @click="
              form.weekdays.includes(i + 1)
                ? (form.weekdays = form.weekdays.filter((w) => w !== i + 1))
                : form.weekdays.push(i + 1)
            "
          >
            {{ $t(name) }}
          </button>
        </div>
      </div>
      <div class="flex gap-3" style="justify-content: flex-end">
        <button class="btn" @click="showCreate = false">{{ $t('common.cancel') }}</button>
        <button class="btn btn-primary" :disabled="creating || !form.name.trim()" @click="createGoal">
          {{ creating ? $t('common.creating') : $t('common.create') }}
        </button>
      </div>
    </div>

    <template v-if="!loading">
      <!-- 今日任务 -->
      <div class="card">
        <div class="card-title">{{ $t('plan.todayTasks', { count: todayTasks.length }) }}</div>
        <div v-if="todayTasks.length === 0" class="empty" style="padding: var(--space-4)">
          <p>{{ $t('plan.noTasks') }}</p>
          <p class="hint">{{ $t('plan.noTasksHint') }}</p>
        </div>
        <div v-else class="task-list">
          <div v-for="task in todayTasks" :key="task.id" class="task-item">
            <div class="grow">
              <div class="flex gap-2" style="align-items: center">
                <span class="badge" :class="statusBadge(task.status)">{{ task.status }}</span>
                <strong>{{ $t('plan.durationMinutes', { minutes: task.duration_min }) }}</strong>
                <span class="text-muted">{{ task.task_type }}</span>
              </div>
              <div class="hint mt-1">{{ task.generated_reason }}</div>
            </div>
            <div class="flex gap-2">
              <button
                v-if="task.status === 'planned' || task.status === 'available'"
                class="btn btn-sm"
                @click="taskAction(task, 'start')"
              >
                {{ $t('plan.start') }}
              </button>
              <button v-if="task.status === 'in_progress'" class="btn btn-sm btn-success" @click="taskAction(task, 'complete')">
                {{ $t('common.complete') }}
              </button>
              <button v-if="!['completed', 'skipped'].includes(task.status)" class="btn btn-sm btn-ghost" @click="taskAction(task, 'skip')">
                {{ $t('common.skip') }}
              </button>
              <button v-if="task.status === 'skipped'" class="btn btn-sm" @click="taskAction(task, 'restore')">{{ $t('plan.restore') }}</button>
              <button
                v-if="task.status !== 'completed'"
                class="btn btn-sm btn-primary"
                @click="router.push('/practice')"
              >
                {{ $t('plan.goPractice') }}
              </button>
            </div>
          </div>
        </div>
      </div>

      <!-- 目标列表 -->
      <div class="card">
        <div class="card-title">{{ $t('plan.goalsTitle', { count: activeGoals.length }) }}</div>
        <div v-if="goals.length === 0" class="empty" style="padding: var(--space-4)">
          <p>{{ $t('plan.noGoals') }}</p>
        </div>
        <div v-else class="goal-list">
          <div v-for="g in goals" :key="g.id" class="goal-item">
            <div class="grow">
              <div class="flex gap-2" style="align-items: center">
                <strong>{{ g.name }}</strong>
                <span class="badge" :class="statusBadge(g.status)">{{ g.status }}</span>
                <span v-if="g.target_score" class="text-secondary">{{ $t('plan.targetScoreLabel', { score: g.target_score }) }}</span>
              </div>
              <div class="hint mt-1">
                {{ $t('plan.goalMeta', { minutes: g.daily_minutes, weekday: $t(weekdayNames[g.available_weekdays[0]] ?? '') }) }}
                <span v-if="g.exam_at">{{ $t('plan.examMeta', { date: g.exam_at?.slice(0, 10) ?? '' }) }}</span>
              </div>
            </div>
            <div class="flex gap-2">
              <button v-if="g.status === 'draft'" class="btn btn-sm" @click="transitionGoal(g, 'activate')">{{ $t('plan.activate') }}</button>
              <button v-if="g.status === 'active'" class="btn btn-sm btn-primary" @click="generatePlan(g)">{{ $t('plan.generate') }}</button>
              <button v-if="g.status === 'active'" class="btn btn-sm" @click="transitionGoal(g, 'pause')">{{ $t('plan.pause') }}</button>
              <button v-if="g.status === 'paused'" class="btn btn-sm" @click="transitionGoal(g, 'activate')">{{ $t('plan.restore') }}</button>
              <button v-if="!['completed', 'archived'].includes(g.status)" class="btn btn-sm btn-ghost" @click="transitionGoal(g, 'complete')">
                {{ $t('common.complete') }}
              </button>
            </div>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.task-list,
.goal-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}
.task-item,
.goal-item {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  padding: 12px 14px;
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
}
.task-item:hover,
.goal-item:hover {
  border-color: var(--border-strong);
}
</style>
