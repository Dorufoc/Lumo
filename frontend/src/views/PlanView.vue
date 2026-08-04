<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { call } from '@/api/client'
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
    error.value = (e as Error).message
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
    error.value = (e as Error).message
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
    error.value = (e as Error).message
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
    error.value = (e as Error).message
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
    error.value = (e as Error).message
  }
}

const weekdayNames = ['', '周一', '周二', '周三', '周四', '周五', '周六', '周日']
const statusBadge = (s: string) =>
  ({ planned: 'badge', available: 'badge', in_progress: 'badge-primary', completed: 'badge-success', skipped: 'badge-offline' })[s] ?? 'badge'

const activeGoals = computed(() => goals.value.filter((g) => !['completed', 'archived'].includes(g.status)))

onMounted(load)
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>学习计划</h1>
        <div class="subtitle">目标 → 每日任务 → 练习闭环</div>
      </div>
      <button class="btn btn-primary" @click="showCreate = !showCreate">新建目标</button>
    </div>

    <div v-if="error" class="error-banner">{{ error }}</div>
    <div v-if="loading" class="loading"><div class="spinner"></div></div>

    <div v-if="showCreate" class="card">
      <h3>创建学习目标</h3>
      <div class="form-row">
        <div class="field">
          <label>目标名称</label>
          <input v-model="form.name" class="input" placeholder="例如：高数期末 90 分" maxlength="160" />
        </div>
        <div class="field">
          <label>考试日期（可选）</label>
          <input v-model="form.exam_at" type="date" class="input" />
        </div>
      </div>
      <div class="form-row">
        <div class="field">
          <label>目标分数</label>
          <input v-model.number="form.target_score" type="number" class="input" min="0" max="100" />
        </div>
        <div class="field">
          <label>每日学习分钟数</label>
          <input v-model.number="form.daily_minutes" type="number" class="input" min="10" max="480" />
        </div>
      </div>
      <div class="field">
        <label>可用星期</label>
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
            {{ name }}
          </button>
        </div>
      </div>
      <div class="flex gap-3" style="justify-content: flex-end">
        <button class="btn" @click="showCreate = false">取消</button>
        <button class="btn btn-primary" :disabled="creating || !form.name.trim()" @click="createGoal">
          {{ creating ? '创建中…' : '创建' }}
        </button>
      </div>
    </div>

    <template v-if="!loading">
      <!-- 今日任务 -->
      <div class="card">
        <div class="card-title">今日任务（{{ todayTasks.length }}）</div>
        <div v-if="todayTasks.length === 0" class="empty" style="padding: var(--space-4)">
          <p>今天还没有计划任务。</p>
          <p class="hint">创建一个目标并生成计划后，任务会自动出现在这里</p>
        </div>
        <div v-else class="task-list">
          <div v-for="task in todayTasks" :key="task.id" class="task-item">
            <div class="grow">
              <div class="flex gap-2" style="align-items: center">
                <span class="badge" :class="statusBadge(task.status)">{{ task.status }}</span>
                <strong>{{ task.duration_min }} 分钟</strong>
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
                开始
              </button>
              <button v-if="task.status === 'in_progress'" class="btn btn-sm btn-success" @click="taskAction(task, 'complete')">
                完成
              </button>
              <button v-if="!['completed', 'skipped'].includes(task.status)" class="btn btn-sm btn-ghost" @click="taskAction(task, 'skip')">
                跳过
              </button>
              <button v-if="task.status === 'skipped'" class="btn btn-sm" @click="taskAction(task, 'restore')">恢复</button>
              <button
                v-if="task.status !== 'completed'"
                class="btn btn-sm btn-primary"
                @click="router.push('/practice')"
              >
                去练习
              </button>
            </div>
          </div>
        </div>
      </div>

      <!-- 目标列表 -->
      <div class="card">
        <div class="card-title">学习目标（{{ activeGoals.length }} 个进行中）</div>
        <div v-if="goals.length === 0" class="empty" style="padding: var(--space-4)">
          <p>还没有学习目标。</p>
        </div>
        <div v-else class="goal-list">
          <div v-for="g in goals" :key="g.id" class="goal-item">
            <div class="grow">
              <div class="flex gap-2" style="align-items: center">
                <strong>{{ g.name }}</strong>
                <span class="badge" :class="statusBadge(g.status)">{{ g.status }}</span>
                <span v-if="g.target_score" class="text-secondary">目标 {{ g.target_score }} 分</span>
              </div>
              <div class="hint mt-1">
                每日 {{ g.daily_minutes }} 分钟 · {{ weekdayNames[g.available_weekdays[0]] ?? '' }} 起
                <span v-if="g.exam_at">· 考试 {{ g.exam_at?.slice(0, 10) }}</span>
              </div>
            </div>
            <div class="flex gap-2">
              <button v-if="g.status === 'draft'" class="btn btn-sm" @click="transitionGoal(g, 'activate')">激活</button>
              <button v-if="g.status === 'active'" class="btn btn-sm btn-primary" @click="generatePlan(g)">生成计划</button>
              <button v-if="g.status === 'active'" class="btn btn-sm" @click="transitionGoal(g, 'pause')">暂停</button>
              <button v-if="g.status === 'paused'" class="btn btn-sm" @click="transitionGoal(g, 'activate')">恢复</button>
              <button v-if="!['completed', 'archived'].includes(g.status)" class="btn btn-sm btn-ghost" @click="transitionGoal(g, 'complete')">
                完成
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
