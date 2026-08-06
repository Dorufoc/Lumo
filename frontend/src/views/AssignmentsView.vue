<script setup lang="ts">
// 作业视图（API 文档 7.11 / 完整设计文档 4.22）：
// 教师：从已发布试卷布置作业 → 发布（乐观锁）→ 查看提交名单；
// 学生：查看待做作业 → 逐题作答 → 提交（幂等）。
import { computed, onMounted, ref } from 'vue'
import { call, localizedMessageOf } from '@/api/client'
import QuestionViewer from '@/components/QuestionViewer.vue'
import type { Assignment, Class, ExamPaper, Question, QuestionPage } from '@/api/types'
import {
  assignmentCreate,
  assignmentList,
  assignmentPublish,
  assignmentSubmissionList,
  assignmentSubmit,
} from '@/api/assignment'
import { classList } from '@/api/class'
import { useI18nStore } from '@/stores/i18n'
import { useSessionStore } from '@/stores/session'

const i18n = useI18nStore()
const session = useSessionStore()

const error = ref('')
const info = ref('')
const loading = ref(false)
const busy = ref(false)

const assignments = ref<Assignment[]>([])
const classes = ref<Class[]>([])
const papers = ref<ExamPaper[]>([])
const library = ref<Question[]>([])
const submissionsByAsg = ref<Record<string, { display_name: string; created_at: string; student_user_id: string }[]>>({})
const expanded = ref<string>('')

const isTeacher = computed(() => session.user?.role === 'teacher')

function idemKey(prefix: string): string {
  return `${prefix}-${session.userId}-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`
}

function classOf(asg: Assignment): Class | undefined {
  return classes.value.find((c) => c.id === asg.class_id)
}

function paperOf(asg: Assignment): ExamPaper | undefined {
  return papers.value.find((p) => p.id === asg.paper_id)
}

async function load() {
  loading.value = true
  error.value = ''
  info.value = ''
  try {
    const [asgs, cls, pp] = await Promise.all([
      assignmentList({ workspace_id: session.workspaceId, user_id: session.userId }),
      classList({ workspace_id: session.workspaceId, user_id: session.userId }),
      call<ExamPaper[]>('ExamPaperList', { workspace_id: session.workspaceId, limit: 100 }),
    ])
    assignments.value = asgs ?? []
    classes.value = cls ?? []
    papers.value = pp ?? []
    if (!isTeacher.value) {
      const lib = await call<QuestionPage>('QuestionList', { workspace_id: session.workspaceId, limit: 100 })
      library.value = lib?.items ?? []
    }
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    loading.value = false
  }
}

// ---- 教师：布置作业 ----
const createDialog = ref(false)
const createForm = ref<{ class_id: string; paper_id: string; title: string; due_at: string; grading_rule: string }>({
  class_id: '',
  paper_id: '',
  title: '',
  due_at: '',
  grading_rule: 'auto',
})

const publishedPapers = computed(() => papers.value.filter((p) => p.status === 'published'))
const activeClasses = computed(() => classes.value.filter((c) => c.status === 'active'))

function openCreate() {
  createForm.value = { class_id: '', paper_id: '', title: '', due_at: '', grading_rule: 'auto' }
  createDialog.value = true
}

function dueToRFC3339(local: string): string {
  if (!local) return ''
  const d = new Date(local)
  return Number.isNaN(d.getTime()) ? '' : d.toISOString()
}

async function submitCreate() {
  error.value = ''
  info.value = ''
  if (!createForm.value.class_id) {
    error.value = i18n.t('assignments.classRequired')
    return
  }
  if (!createForm.value.paper_id) {
    error.value = i18n.t('assignments.paperRequired')
    return
  }
  if (!createForm.value.title.trim()) {
    error.value = i18n.t('assignments.titleRequired')
    return
  }
  const due = dueToRFC3339(createForm.value.due_at)
  if (!due) {
    error.value = i18n.t('assignments.dueRequired')
    return
  }
  busy.value = true
  try {
    const a = await assignmentCreate({
      workspace_id: session.workspaceId,
      user_id: session.userId,
      class_id: createForm.value.class_id,
      paper_id: createForm.value.paper_id,
      title: createForm.value.title.trim(),
      due_at: due,
      grading_rule: createForm.value.grading_rule as Assignment['grading_rule'],
      idempotency_key: idemKey('ac'),
    })
    assignments.value.unshift(a)
    createDialog.value = false
    info.value = i18n.t('assignments.created')
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    busy.value = false
  }
}

// ---- 教师：发布 ----
async function doPublish(asg: Assignment) {
  error.value = ''
  info.value = ''
  if (!window.confirm(i18n.t('assignments.publishConfirm'))) return
  busy.value = true
  try {
    const a = await assignmentPublish({
      workspace_id: session.workspaceId,
      user_id: session.userId,
      assignment_id: asg.id,
      version: asg.version,
    })
    const idx = assignments.value.findIndex((x) => x.id === a.id)
    if (idx >= 0) assignments.value[idx] = a
    info.value = i18n.t('assignments.published')
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    busy.value = false
  }
}

// ---- 教师：提交名单 ----
async function loadSubmissions(asg: Assignment) {
  try {
    submissionsByAsg.value[asg.id] =
      (await assignmentSubmissionList({
        workspace_id: session.workspaceId,
        user_id: session.userId,
        assignment_id: asg.id,
      })) ?? []
  } catch (e) {
    error.value = localizedMessageOf(e)
  }
}

function toggleSubmissions(asg: Assignment) {
  if (expanded.value === asg.id) {
    expanded.value = ''
    return
  }
  expanded.value = asg.id
  void loadSubmissions(asg)
}

// ---- 学生：作答 ----
const answerTarget = ref<Assignment | null>(null)
const answerStep = ref(0)
const answers = ref<Record<string, string | string[] | null>>({})
const confirmSubmit = ref(false)

const answerQuestions = computed(() => {
  const p = answerTarget.value ? paperOf(answerTarget.value) : undefined
  if (!p) return []
  const qvidToPayload = new Map<string, Question['current_version']>()
  for (const q of library.value) {
    if (q.current_version) qvidToPayload.set(q.current_version.id, q.current_version)
  }
  const out: { question_version_id: string; payload: Question['current_version'] }[] = []
  for (const s of p.sections) {
    for (const qvid of s.question_version_ids) {
      const v = qvidToPayload.get(qvid)
      if (v) out.push({ question_version_id: qvid, payload: v })
    }
  }
  return out
})

const answeredCount = computed(() => answerQuestions.value.filter((q) => (answers.value[q.question_version_id] ?? null) != null).length)

function openAnswer(asg: Assignment) {
  answerTarget.value = asg
  answerStep.value = 0
  answers.value = {}
  confirmSubmit.value = false
}

function closeAnswer() {
  answerTarget.value = null
}

async function submitAnswer() {
  if (!answerTarget.value) return
  error.value = ''
  info.value = ''
  const payload: { question_version_id: string; answer: unknown }[] = []
  for (const q of answerQuestions.value) {
    const v = answers.value[q.question_version_id]
    if (v != null && v !== '') payload.push({ question_version_id: q.question_version_id, answer: v })
  }
  busy.value = true
  try {
    await assignmentSubmit({
      workspace_id: session.workspaceId,
      user_id: session.userId,
      assignment_id: answerTarget.value.id,
      answers: payload,
      idempotency_key: idemKey('as'),
    })
    answerTarget.value = null
    info.value = i18n.t('assignments.submitDone')
    await load()
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    busy.value = false
  }
}

function fmtDue(dueAt: string): string {
  const d = new Date(dueAt)
  return Number.isNaN(d.getTime()) ? dueAt : d.toLocaleString()
}

onMounted(load)
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('assignments.title') }}</h1>
        <div class="subtitle">{{ $t('assignments.subtitle') }}</div>
      </div>
      <div class="flex gap-2">
        <button class="btn btn-sm" :disabled="loading" @click="load">{{ $t('common.refresh') }}</button>
        <button v-if="isTeacher" class="btn btn-sm btn-primary" :disabled="loading" @click="openCreate">
          {{ $t('assignments.createAssignment') }}
        </button>
      </div>
    </div>

    <div v-if="error" class="error-banner">{{ error }}</div>
    <div v-if="info" class="offline-banner">{{ info }}</div>

    <div v-if="loading" class="loading"><div class="spinner"></div></div>

    <div v-else-if="assignments.length === 0" class="card">
      <div class="empty">
        <div class="empty-icon">📝</div>
        <p>{{ isTeacher ? $t('assignments.emptyTeacher') : $t('assignments.emptyStudent') }}</p>
      </div>
    </div>

    <div v-else class="assignment-list">
      <div v-for="asg in assignments" :key="asg.id" class="card assignment-card">
        <div class="assignment-head">
          <div class="assignment-info">
            <div class="assignment-title">
              {{ asg.title }}
              <span class="badge" :class="asg.status === 'published' ? 'badge-success' : asg.status === 'draft' ? 'badge-warning' : 'badge-error'">
                {{ $t(asg.status === 'published' ? 'assignments.statusPublished' : asg.status === 'draft' ? 'assignments.statusDraft' : 'assignments.statusClosed') }}
              </span>
            </div>
            <div class="hint">
              {{ classOf(asg)?.name || asg.class_id }} · {{ paperOf(asg)?.title || asg.paper_id }} · {{ $t('assignments.dueLabel') }} {{ fmtDue(asg.due_at) }}
            </div>
          </div>

          <!-- 教师操作 -->
          <div v-if="isTeacher" class="flex gap-2">
            <button v-if="asg.status === 'draft'" class="btn btn-sm btn-primary" :disabled="busy" @click="doPublish(asg)">
              {{ $t('assignments.publish') }}
            </button>
            <button class="btn btn-sm" @click="toggleSubmissions(asg)">
              {{ $t('assignments.viewSubmissions') }} {{ expanded === asg.id ? '▾' : '▸' }}
            </button>
          </div>

          <!-- 学生操作 -->
          <button v-else-if="asg.status === 'published'" class="btn btn-sm btn-primary" @click="openAnswer(asg)">
            {{ $t('assignments.submit') }}
          </button>
          <span v-else class="hint">{{ $t('assignments.statusDraft') }}</span>
        </div>

        <!-- 提交名单（教师） -->
        <div v-if="isTeacher && expanded === asg.id" class="submissions">
          <div v-if="(submissionsByAsg[asg.id] ?? []).length === 0" class="empty">
            <p>{{ $t('assignments.submissionsEmpty') }}</p>
          </div>
          <div v-else class="submission-list">
            <div v-for="(s, i) in submissionsByAsg[asg.id]" :key="i" class="submission-row">
              <span class="submission-name">{{ s.display_name || s.student_user_id }}</span>
              <span class="badge badge-success">{{ $t('assignments.submitted') }}</span>
              <span class="hint">{{ $t('assignments.submittedAt') }} {{ fmtDue(s.created_at) }}</span>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- 布置作业弹窗（教师） -->
    <div v-if="createDialog" class="modal-mask">
      <div class="card modal">
        <h3>{{ $t('assignments.createAssignment') }}</h3>
        <div v-if="activeClasses.length === 0" class="error-banner">{{ $t('assignments.noClasses') }}</div>
        <div v-else-if="publishedPapers.length === 0" class="error-banner">{{ $t('assignments.noPapers') }}</div>
        <div class="field">
          <label>{{ $t('assignments.classLabel') }} *</label>
          <select v-model="createForm.class_id" class="input">
            <option value="" disabled>{{ $t('assignments.classPlaceholder') }}</option>
            <option v-for="c in activeClasses" :key="c.id" :value="c.id">{{ c.name }}</option>
          </select>
        </div>
        <div class="field">
          <label>{{ $t('assignments.paperLabel') }} *</label>
          <select v-model="createForm.paper_id" class="input">
            <option value="" disabled>{{ $t('assignments.paperPlaceholder') }}</option>
            <option v-for="p in publishedPapers" :key="p.id" :value="p.id">
              {{ p.title }}（{{ p.sections.reduce((n, s) => n + s.question_version_ids.length, 0) }} {{ $t('exam.colQuestions') }}）
            </option>
          </select>
        </div>
        <div class="field">
          <label>{{ $t('assignments.titleLabel') }} *</label>
          <input v-model="createForm.title" class="input" type="text" :placeholder="$t('assignments.titlePlaceholder')" />
        </div>
        <div class="field">
          <label>{{ $t('assignments.dueLabel') }} *</label>
          <input v-model="createForm.due_at" class="input" type="datetime-local" />
        </div>
        <div class="field">
          <label>{{ $t('assignments.gradingRule') }}</label>
          <select v-model="createForm.grading_rule" class="input">
            <option value="auto">{{ $t('assignments.gradingAuto') }}</option>
            <option value="teacher">{{ $t('assignments.gradingTeacher') }}</option>
            <option value="hybrid">{{ $t('assignments.gradingHybrid') }}</option>
          </select>
        </div>
        <div class="flex gap-3 mt-3">
          <button class="btn" :disabled="busy" @click="createDialog = false">{{ $t('common.cancel') }}</button>
          <button class="btn btn-primary" :disabled="busy || activeClasses.length === 0 || publishedPapers.length === 0" @click="submitCreate">
            {{ busy ? $t('common.creating') : $t('assignments.confirmCreate') }}
          </button>
        </div>
      </div>
    </div>

    <!-- 作答弹窗（学生） -->
    <div v-if="answerTarget" class="modal-mask">
      <div class="card modal modal-lg">
        <div class="flex-between mb-3">
          <h3 style="margin: 0">{{ answerTarget.title }}</h3>
          <button class="btn btn-sm" @click="closeAnswer">{{ $t('common.close') }}</button>
        </div>
        <div v-if="answerQuestions.length === 0" class="empty">
          <p>{{ $t('assignments.emptyStudent') }}</p>
        </div>
        <template v-else>
          <div class="flex-between mb-3">
            <div class="flex gap-2" style="align-items: center">
              <button class="btn btn-sm" :disabled="answerStep === 0" @click="answerStep--">{{ $t('practice.prev') }}</button>
              <span class="text-secondary">{{ $t('practice.questionCount', { current: answerStep + 1, total: answerQuestions.length }) }}</span>
              <button class="btn btn-sm" :disabled="answerStep >= answerQuestions.length - 1" @click="answerStep++">{{ $t('practice.next') }}</button>
            </div>
            <div class="flex gap-2" style="align-items: center">
              <span class="text-secondary">{{ $t('assignments.answered', { answered: answeredCount, total: answerQuestions.length }) }}</span>
              <button class="btn btn-sm btn-primary" :disabled="busy" @click="confirmSubmit = true">
                {{ $t('assignments.submit') }}
              </button>
            </div>
          </div>
          <div class="flex gap-2 mb-3" style="flex-wrap: wrap">
            <button
              v-for="(item, i) in answerQuestions"
              :key="item.question_version_id"
              class="btn btn-sm"
              :class="{
                'btn-primary': i === answerStep,
                'btn-success': (answers[item.question_version_id] ?? null) != null,
                'btn-ghost': (answers[item.question_version_id] ?? null) == null,
              }"
              @click="answerStep = i"
            >
              {{ i + 1 }}
            </button>
          </div>
          <div class="card">
            <QuestionViewer
              :key="answerQuestions[answerStep].question_version_id"
              :payload="answerQuestions[answerStep].payload?.payload ?? ({ type: 'short_answer', stem: '', answer: '' } as any)"
              :mode="'answer'"
              :model-value="answers[answerQuestions[answerStep].question_version_id] ?? null"
              @update:model-value="answers[answerQuestions[answerStep].question_version_id] = $event as any"
            />
          </div>
          <div v-if="confirmSubmit" class="confirm-bar">
            <span>{{ $t('assignments.confirmSubmit') }}</span>
            <div class="flex gap-2">
              <button class="btn btn-sm" :disabled="busy" @click="confirmSubmit = false">{{ $t('common.cancel') }}</button>
              <button class="btn btn-sm btn-primary" :disabled="busy" @click="submitAnswer">
                {{ busy ? $t('common.submitting') : $t('assignments.confirmSubmit') }}
              </button>
            </div>
          </div>
        </template>
      </div>
    </div>
  </div>
</template>

<style scoped>
.assignment-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}

.assignment-card {
  padding: var(--space-3);
}

.assignment-head {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}

.assignment-info {
  flex: 1;
  min-width: 0;
}

.assignment-title {
  font-weight: 600;
  display: flex;
  align-items: center;
  gap: var(--space-2);
}

.submissions {
  margin-top: var(--space-3);
  padding-top: var(--space-3);
  border-top: 1px solid var(--border);
}

.submission-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
}

.submission-row {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-1) 0;
}

.submission-name {
  flex: 1;
  min-width: 0;
  font-weight: 500;
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

.modal-lg {
  width: min(720px, 94vw);
}

.confirm-bar {
  margin-top: var(--space-3);
  padding: var(--space-3);
  background: var(--bg-subtle);
  border-radius: var(--radius-sm);
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
}
</style>
