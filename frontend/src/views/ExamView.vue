<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import QuestionViewer from '@/components/QuestionViewer.vue'
import { call, localizedMessageOf } from '@/api/client'
import type { ExamPaper, ExamResult, KnowledgeNode, QuestionPage } from '@/api/types'
import { useExamStore } from '@/stores/exam'
import { useI18nStore } from '@/stores/i18n'
import { useSessionStore } from '@/stores/session'

const store = useExamStore()
const session = useSessionStore()
const i18n = useI18nStore()

const loading = ref(false)
const error = ref('')
const mode = ref<'list' | 'answer' | 'result'>('list')

// ---------- 试卷列表 ----------
async function loadPapers() {
  loading.value = true
  error.value = ''
  try {
    await store.listPapers()
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    loading.value = false
  }
}

// ---------- 新建试卷（手动/自动） ----------
const showCreate = ref(false)
const createTab = ref<'manual' | 'auto'>('manual')
const manualTitle = ref('')
const manualDuration = ref(30)
const manualSelected = ref<Set<string>>(new Set())
const library = ref<QuestionPage | null>(null)

const autoTitle = ref('')
const autoCount = ref(10)
const autoDuration = ref(30)
const autoDifficulty = ref(3)
const autoTypes = ref<string[]>(['single_choice', 'multiple_choice', 'fill_blank'])
const tree = ref<KnowledgeNode[]>([])
const autoKnowledge = ref<Set<string>>(new Set())

function flatten(nodes: KnowledgeNode[], acc: KnowledgeNode[]): KnowledgeNode[] {
  for (const n of nodes) {
    acc.push(n)
    if (n.children?.length) flatten(n.children, acc)
  }
  return acc
}
const allKnowledge = computed(() => flatten(tree.value, []))

async function openCreate(tab: 'manual' | 'auto') {
  showCreate.value = true
  createTab.value = tab
  error.value = ''
  if (tab === 'manual') {
    if (!library.value) {
      library.value = await call<QuestionPage>('QuestionList', {
        workspace_id: session.workspaceId,
        status: 'published',
        limit: 100,
      }).catch(() => null)
    }
  } else {
    if (tree.value.length === 0) {
      tree.value = await call<KnowledgeNode[]>('KnowledgeTreeGet', {
        workspace_id: session.workspaceId,
      }).catch(() => [])
    }
  }
}

async function submitCreate() {
  error.value = ''
  loading.value = true
  try {
    if (createTab.value === 'manual') {
      const qvids = [...manualSelected.value]
      if (!manualTitle.value.trim() || qvids.length === 0) {
        throw new Error(i18n.t('exam.titlePlaceholder') + ' / ' + i18n.t('exam.empty'))
      }
      await store.createPaper(manualTitle.value.trim(), {
        duration_min: manualDuration.value,
        sections: [{ title: i18n.t('exam.sectionTitle'), order_no: 1, question_version_ids: qvids, score: 10 }],
      })
    } else {
      if (!autoTitle.value.trim() || autoCount.value <= 0 || autoKnowledge.value.size === 0) {
        throw new Error(i18n.t('exam.autoHint'))
      }
      const ratio: Record<string, number> = {}
      for (const k of autoKnowledge.value) ratio[k] = 1 / autoKnowledge.value.size
      await store.autoGenerate(autoTitle.value.trim(), {
        knowledge_ratio: ratio,
        difficulty_dist: { [String(autoDifficulty.value)]: 1.0 },
        count: autoCount.value,
        types: autoTypes.value,
        duration_min: autoDuration.value,
      })
    }
    showCreate.value = false
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    loading.value = false
  }
}

async function publish(paper: ExamPaper) {
  if (!window.confirm(i18n.t('exam.publishConfirm'))) return
  error.value = ''
  loading.value = true
  try {
    await store.publishPaper(paper.id, paper.version)
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    loading.value = false
  }
}

async function start(paper: ExamPaper) {
  if (!window.confirm(i18n.t('exam.startConfirm', { duration: String(paper.config_json.duration_min ?? 0) }))) return
  error.value = ''
  loading.value = true
  try {
    await store.start(paper.id)
    mode.value = 'answer'
    current.value = 0
    startTimer()
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    loading.value = false
  }
}

// ---------- 答题 ----------
const current = ref(0)
const submitting = ref(false)
const confirmSubmit = ref(false)
const questions = computed(() => store.active?.questions ?? [])
const q = computed(() => questions.value[current.value])
const progress = computed(() =>
  questions.value.length > 0 ? Math.round(((current.value + 1) / questions.value.length) * 100) : 0,
)
const answeredCount = computed(() => Object.values(store.answers).filter((a) => a !== null && a !== '').length)

const currentAnswer = computed({
  get: () => (q.value ? (store.answers[q.value.question_version_id] ?? null) : null),
  set: (v: string | string[] | null) => {
    if (!q.value) return
    store.answers[q.value.question_version_id] = v
    void store.saveAnswer(q.value.question_version_id, v)
  },
})

// 倒计时（仅 UX：后端以 started_at + duration_min 为准，惰性自动提交兜底）
const remain = ref(0)
let timer: ReturnType<typeof setInterval> | null = null

function startTimer() {
  const durationMin = store.active?.duration_min ?? 0
  remain.value = durationMin * 60
  if (remain.value <= 0) return
  timer = setInterval(() => {
    remain.value--
    if (remain.value <= 0) {
      clearInterval(timer!)
      timer = null
      void doAutoSubmit()
    }
  }, 1000)
}

const remainText = computed(() => {
  const m = Math.floor(remain.value / 60)
  const s = remain.value % 60
  return `${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`
})

async function doAutoSubmit() {
  if (submitting.value) return
  submitting.value = true
  confirmSubmit.value = false
  error.value = ''
  try {
    const result = await store.autoSubmit()
    if (result.status === 'graded') {
      resultRef.value = result
      mode.value = 'result'
    } else {
      error.value = i18n.t('exam.autoSubmitted')
    }
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    submitting.value = false
  }
}

// ---------- 结果 ----------
const resultRef = ref<ExamResult | null>(null)

const accuracy = computed(() => {
  const r = resultRef.value
  if (!r || r.max_score === 0) return 0
  return Math.round((r.total_score / r.max_score) * 100)
})
const correctCount = computed(() => resultRef.value?.questions.filter((x) => !x.is_wrong).length ?? 0)
const openIndex = ref<number | null>(0)

function answerOf(x: ExamResult['questions'][number]): string | string[] | null {
  const a = x.submission?.answer
  if (a === null || a === undefined) return null
  if (typeof a === 'string') return a
  if (Array.isArray(a)) return a as string[]
  return null
}

onMounted(loadPapers)
onBeforeUnmount(() => {
  if (timer) clearInterval(timer)
})
</script>

<template>
  <div>
    <div v-if="error" class="error-banner">
      <span>{{ error }}</span>
      <button class="btn btn-sm" @click="error = ''">{{ $t('common.close') }}</button>
    </div>
    <div v-if="loading" class="loading"><div class="spinner"></div></div>

    <!-- 列表 -->
    <template v-if="mode === 'list'">
      <div class="page-header">
        <div>
          <h1>{{ $t('exam.title') }}</h1>
          <div class="subtitle">{{ $t('exam.subtitle') }}</div>
        </div>
        <div class="flex gap-2">
          <button class="btn" @click="openCreate('manual')">{{ $t('exam.createPaper') }}</button>
          <button class="btn btn-primary" @click="openCreate('auto')">{{ $t('exam.autoGenerate') }}</button>
        </div>
      </div>

      <div v-if="store.papers.length === 0" class="empty">
        <div class="empty-icon">📝</div>
        <p>{{ $t('exam.empty') }}</p>
        <button class="btn btn-primary" @click="openCreate('auto')">{{ $t('exam.autoGenerate') }}</button>
      </div>

      <div v-else class="card">
        <table class="table">
          <thead>
            <tr>
              <th>{{ $t('exam.colTitle') }}</th>
              <th>{{ $t('exam.colStatus') }}</th>
              <th>{{ $t('exam.colQuestions') }}</th>
              <th>{{ $t('exam.colDuration') }}</th>
              <th style="width: 220px"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="p in store.papers" :key="p.id">
              <td>{{ p.title }}</td>
              <td>
                <span class="badge" :class="{ 'badge-success': p.status === 'published' }">
                  {{ $t('exam.status' + p.status.charAt(0).toUpperCase() + p.status.slice(1)) }}
                </span>
              </td>
              <td class="text-muted">
                {{ p.sections.reduce((n, s) => n + s.question_version_ids.length, 0) }} ·
                {{ p.sections.length }} {{ $t('exam.colSections') }}
              </td>
              <td class="text-muted">{{ p.config_json.duration_min ?? '–' }} min</td>
              <td>
                <div class="flex gap-2" style="justify-content: flex-end">
                  <button v-if="p.status === 'draft'" class="btn btn-sm" @click="publish(p)">{{ $t('exam.publish') }}</button>
                  <button v-if="p.status === 'published'" class="btn btn-sm btn-primary" @click="start(p)">{{ $t('exam.start') }}</button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <!-- 答题 -->
    <template v-else-if="mode === 'answer' && store.active && q">
      <div class="flex-between mb-3">
        <div class="flex gap-2" style="align-items: center">
          <button class="btn btn-sm" :disabled="current === 0" @click="current--">{{ $t('practice.prev') }}</button>
          <span class="text-secondary">{{ $t('practice.questionCount', { current: current + 1, total: questions.length }) }}</span>
          <button class="btn btn-sm" :disabled="current >= questions.length - 1" @click="current++">{{ $t('practice.next') }}</button>
        </div>
        <div class="flex gap-2" style="align-items: center">
          <span class="text-secondary">{{ $t('exam.answeredCount', { answered: answeredCount, total: questions.length }) }}</span>
          <span class="badge" :class="{ 'badge-error': remain < 60 }">⏱ {{ remainText }}</span>
          <button class="btn btn-primary" :disabled="submitting" @click="confirmSubmit = true">{{ $t('exam.submitNow') }}</button>
        </div>
      </div>
      <div class="progress mb-4"><div :style="{ width: progress + '%' }"></div></div>

      <div class="flex gap-2 mb-3" style="flex-wrap: wrap">
        <button
          v-for="(item, i) in questions"
          :key="item.question_version_id"
          class="btn btn-sm"
          :class="{
            'btn-primary': i === current,
            'btn-success': store.answers[item.question_version_id],
            'btn-ghost': !store.answers[item.question_version_id],
          }"
          @click="current = i"
        >
          {{ i + 1 }}
        </button>
      </div>

      <div class="card">
        <QuestionViewer
          :key="q.question_version_id"
          :payload="q.payload ?? ({ type: q.type, stem: '', answer: '' } as any)"
          :mode="'answer'"
          :model-value="currentAnswer"
          @update:model-value="currentAnswer = $event as any"
        />
      </div>
    </template>

    <!-- 结果 -->
    <template v-else-if="mode === 'result' && resultRef">
      <div class="card" style="text-align: center; padding: var(--space-6)">
        <h1 style="font-size: 40px" :class="accuracy >= 60 ? 'text-success' : 'text-error'">{{ accuracy }}%</h1>
        <div class="text-secondary mb-3">
          {{ $t('exam.scoreSummary', { score: resultRef.total_score, max: resultRef.max_score }) }} ·
          {{ $t('exam.correctCount', { correct: correctCount, total: resultRef.questions.length }) }}
        </div>
        <div class="flex gap-3" style="justify-content: center">
          <RouterLink to="/exams" class="btn">{{ $t('exam.backToList') }}</RouterLink>
          <RouterLink to="/review" class="btn btn-primary">{{ $t('exam.goReview') }}</RouterLink>
        </div>
      </div>

      <div class="card" v-for="(x, i) in resultRef.questions" :key="x.question_version_id">
        <div class="flex-between mb-2">
          <div class="flex gap-2" style="align-items: center">
            <span class="badge" :class="x.is_wrong ? 'badge-error' : 'badge-success'">
              {{ x.is_wrong ? '✗' : '✓' }} {{ $t('result.questionNo', { num: i + 1 }) }}
            </span>
            <span class="badge">{{ x.type }}</span>
            <span v-if="x.skipped" class="badge badge-warning">{{ $t('result.skipped') }}</span>
          </div>
          <button class="btn btn-sm btn-ghost" @click="openIndex = openIndex === i ? null : i">
            {{ openIndex === i ? $t('result.collapse') : $t('result.view') }}
          </button>
        </div>
        <QuestionViewer
          v-if="openIndex === i"
          :payload="x.payload"
          :mode="'result'"
          :model-value="answerOf(x)"
          :grading="x.grading"
          :wrong="x.is_wrong"
        />
        <div v-else class="text-secondary" style="white-space: pre-wrap">
          {{ (x.payload.stem ?? '').slice(0, 80) }}{{ (x.payload.stem ?? '').length > 80 ? '…' : '' }}
        </div>
      </div>
    </template>

    <!-- 新建试卷弹窗 -->
    <div v-if="showCreate" class="modal-mask" @click.self="showCreate = false">
      <div class="card" style="width: 640px; margin: auto; max-height: 80vh; overflow-y: auto">
        <h3>{{ $t('exam.createPaper') }}</h3>
        <div class="flex gap-2 mb-3">
          <button class="btn btn-sm" :class="{ 'btn-primary': createTab === 'manual' }" @click="createTab = 'manual'">{{ $t('exam.manualTab') }}</button>
          <button class="btn btn-sm" :class="{ 'btn-primary': createTab === 'auto' }" @click="createTab = 'auto'">{{ $t('exam.autoTab') }}</button>
        </div>

        <template v-if="createTab === 'manual'">
          <div class="field"><label>{{ $t('exam.colTitle') }}</label><input v-model="manualTitle" class="input" :placeholder="$t('exam.titlePlaceholder')" /></div>
          <div class="field"><label>{{ $t('exam.colDuration') }} (min)</label><input v-model.number="manualDuration" type="number" min="1" class="input" /></div>
          <div class="field">
            <label>{{ $t('practice.colStem') }}</label>
            <div style="max-height: 260px; overflow-y: auto; border: 1px solid var(--border); border-radius: var(--radius); padding: var(--space-2)">
              <label v-for="item in library?.items ?? []" :key="item.id" class="flex gap-2" style="padding: 6px 8px; cursor: pointer">
                <input type="checkbox" :checked="manualSelected.has(item.id)" @change="manualSelected.has(item.id) ? manualSelected.delete(item.id) : manualSelected.add(item.id)" />
                <span style="white-space: nowrap; overflow: hidden; text-overflow: ellipsis">{{ item.current_version?.payload.stem?.slice(0, 80) }}</span>
              </label>
            </div>
          </div>
        </template>

        <template v-else>
          <div class="field"><label>{{ $t('exam.colTitle') }}</label><input v-model="autoTitle" class="input" :placeholder="$t('exam.titlePlaceholder')" /></div>
          <div class="flex gap-3">
            <div class="field grow"><label>{{ $t('exam.countPlaceholder') }}</label><input v-model.number="autoCount" type="number" min="1" class="input" /></div>
            <div class="field grow"><label>{{ $t('exam.colDuration') }} (min)</label><input v-model.number="autoDuration" type="number" min="1" class="input" /></div>
            <div class="field grow">
              <label>{{ $t('question.difficulty') }}</label>
              <select v-model.number="autoDifficulty" class="input">
                <option v-for="d in [1, 2, 3, 4, 5]" :key="d" :value="d">{{ d }}</option>
              </select>
            </div>
          </div>
          <div class="field">
            <label>{{ $t('question.type') }}</label>
            <div class="flex gap-3">
              <label v-for="tp in ['single_choice', 'multiple_choice', 'fill_blank']" :key="tp" class="flex gap-1" style="align-items: center">
                <input type="checkbox" :checked="autoTypes.includes(tp)" @change="autoTypes.includes(tp) ? autoTypes.splice(autoTypes.indexOf(tp), 1) : autoTypes.push(tp)" />
                <span>{{ tp }}</span>
              </label>
            </div>
          </div>
          <div class="field">
            <label>{{ $t('exam.selectKnowledge') }}</label>
            <div style="max-height: 200px; overflow-y: auto; border: 1px solid var(--border); border-radius: var(--radius); padding: var(--space-2)">
              <label v-for="k in allKnowledge" :key="k.id" class="flex gap-2" style="padding: 4px 8px; cursor: pointer">
                <input type="checkbox" :checked="autoKnowledge.has(k.id)" @change="autoKnowledge.has(k.id) ? autoKnowledge.delete(k.id) : autoKnowledge.add(k.id)" />
                <span>{{ k.name }}</span>
              </label>
            </div>
          </div>
        </template>

        <div class="hint mb-3">{{ createTab === 'manual' ? $t('exam.configHint') : $t('exam.autoHint') }}</div>
        <div class="flex gap-3" style="justify-content: flex-end">
          <button class="btn" @click="showCreate = false">{{ $t('common.cancel') }}</button>
          <button class="btn btn-primary" :disabled="loading" @click="submitCreate">
            {{ loading ? $t('common.creating') : $t('exam.createButton') }}
          </button>
        </div>
      </div>
    </div>

    <!-- 提交确认 -->
    <div v-if="confirmSubmit" class="modal-mask" @click.self="confirmSubmit = false">
      <div class="card" style="width: 400px; margin: auto">
        <h3>{{ $t('exam.submitNow') }}</h3>
        <p class="text-secondary">{{ $t('practice.confirmBodyNote') }}</p>
        <div class="flex gap-3" style="justify-content: flex-end">
          <button class="btn" @click="confirmSubmit = false">{{ $t('common.cancel') }}</button>
          <button class="btn btn-primary" :disabled="submitting" @click="doAutoSubmit">
            {{ submitting ? $t('exam.submitting') : $t('exam.submitNow') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.modal-mask {
  position: fixed;
  inset: 0;
  background: var(--overlay);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 100;
}
</style>
