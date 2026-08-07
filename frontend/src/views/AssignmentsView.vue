<script setup lang="ts">
// 作业视图（API 文档 7.11 / 完整设计文档 4.22）：
// 教师：从已发布试卷布置作业 → 发布（乐观锁）→ 查看提交名单；
// 学生：查看待做作业 → 逐题作答 → 提交（幂等）。
import { computed, onMounted, ref } from 'vue'
import { call, localizedMessageOf } from '@/api/client'
import QuestionViewer from '@/components/QuestionViewer.vue'
import type {
  Appeal,
  AppealDecision,
  Assignment,
  AssignmentSubmission,
  Class,
  ExamPaper,
  PracticeResult,
  Question,
  QuestionPage,
  ResultQuestion,
} from '@/api/types'
import {
  appealCreate,
  appealList,
  appealResolve,
  assignmentCreate,
  assignmentGrade,
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
const submissionsByAsg = ref<Record<string, AssignmentSubmission[]>>({})
const appealsByAsg = ref<Record<string, Appeal[]>>({})
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
  // 同步加载该作业申诉（教师复议视图）
  try {
    appealsByAsg.value[asg.id] =
      (await appealList({
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

// ---- 教师：批阅 ----
const gradeTarget = ref<AssignmentSubmission | null>(null)
const gradeItems = ref<GradeItem[]>([])
const gradeQuestions = ref<ResultQuestion[]>([])
const gradeVersion = ref(0)
const gradeOverall = ref(0)
const gradeComment = ref('')
const pregrading = ref(false)

interface GradeItem {
  question_version_id: string
  type: string
  max_score: number
  score: number
  comment: string
  status: string
}

function gradeJSONVersion(gj: Record<string, unknown> | undefined): number {
  const v = gj?.version
  return typeof v === 'number' ? v : 0
}

function gradeJSONOverall(gj: Record<string, unknown> | undefined): number {
  const v = gj?.overall
  return typeof v === 'number' ? v : 0
}

function gradeJSONItems(gj: Record<string, unknown> | undefined): GradeItem[] {
  const items = Array.isArray(gj?.items) ? (gj.items as Record<string, unknown>[]) : []
  return items.map((it) => ({
    question_version_id: String(it.question_version_id ?? ''),
    type: String(it.type ?? ''),
    max_score: typeof it.max_score === 'number' ? it.max_score : 0,
    score: typeof it.score === 'number' ? it.score : 0,
    comment: typeof it.comment === 'string' ? it.comment : '',
    status: String(it.status ?? 'graded'),
  }))
}

function answerText(v: unknown): string {
  if (v == null) return ''
  if (typeof v === 'string') return v
  if (Array.isArray(v)) return v.join(', ')
  return JSON.stringify(v)
}

function payloadMaxScore(q: ResultQuestion): number {
  const v = q.payload?.grading_config?.max_score
  return typeof v === 'number' ? v : 10
}

function gradeTotalMax(gj: Record<string, unknown> | undefined): number {
  return gradeJSONItems(gj).reduce((n, it) => n + it.max_score, 0)
}

async function openGrade(sub: AssignmentSubmission) {
  error.value = ''
  info.value = ''
  gradeTarget.value = sub
  gradeVersion.value = gradeJSONVersion(sub.grade_json)
  gradeOverall.value = gradeJSONOverall(sub.grade_json)
  gradeComment.value = typeof sub.grade_json?.comment === 'string' ? (sub.grade_json.comment as string) : ''
  gradeItems.value = gradeJSONItems(sub.grade_json)
  gradeQuestions.value = []
  // 读取该提交的作答（会话快照含分值；作答经 submissions 对齐）
  if (sub.session_id) {
    try {
      const res = await call<PracticeResult>('PracticeGetResult', {
        workspace_id: session.workspaceId,
        session_id: sub.session_id,
      })
      gradeQuestions.value = res?.questions ?? []
      // 未批阅（teacher/hybrid）时以作答初始化待改项（分值默认 10）
      if (gradeItems.value.length === 0) {
        gradeItems.value = gradeQuestions.value.map((q) => ({
          question_version_id: q.question_version_id,
          type: q.type,
          max_score: payloadMaxScore(q),
          score: 0,
          comment: '',
          status: 'pending',
        }))
        gradeOverall.value = 0
      }
    } catch (e) {
      error.value = localizedMessageOf(e)
    }
  }
}

function gradeItemOf(q: ResultQuestion): GradeItem {
  const existing = gradeItems.value.find((i) => i.question_version_id === q.question_version_id)
  if (existing) return existing
  const created: GradeItem = {
    question_version_id: q.question_version_id,
    type: q.type,
    max_score: payloadMaxScore(q),
    score: 0,
    comment: '',
    status: 'pending',
  }
  gradeItems.value.push(created)
  return created
}

function recalcOverall() {
  gradeOverall.value = gradeItems.value.reduce((n, it) => n + (it.score || 0), 0)
}

async function doPreGrade() {
  if (!gradeTarget.value) return
  error.value = ''
  info.value = ''
  pregrading.value = true
  try {
    const out = await assignmentGrade({
      workspace_id: session.workspaceId,
      user_id: session.userId,
      submission_id: gradeTarget.value.id,
      version: gradeVersion.value,
      pre_grade: true,
    })
    if (out?.message) info.value = out.message
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    pregrading.value = false
  }
}

async function saveGrade() {
  if (!gradeTarget.value) return
  error.value = ''
  info.value = ''
  recalcOverall()
  const items = gradeItems.value.map((it) => ({
    question_version_id: it.question_version_id,
    type: it.type,
    max_score: it.max_score,
    score: it.score,
    status: 'graded',
    comment: it.comment,
  }))
  busy.value = true
  try {
    const out = await assignmentGrade({
      workspace_id: session.workspaceId,
      user_id: session.userId,
      submission_id: gradeTarget.value.id,
      grade_json: { items, overall: gradeOverall.value, comment: gradeComment.value },
      version: gradeVersion.value,
    })
    const asgID = gradeTarget.value.assignment_id
    gradeTarget.value = null
    // 刷新名单与列表（回显最新版本）
    await loadSubmissions({ id: asgID } as Assignment)
    await load()
    if (out?.message) info.value = out.message
  } catch (e) {
    error.value = localizedMessageOf(e)
    if (gradeTarget.value) {
      // 乐观锁冲突：重新读取最新版本号
      await loadSubmissions({ id: gradeTarget.value.assignment_id } as Assignment)
      const fresh = submissionsByAsg.value[gradeTarget.value.assignment_id]?.find((s) => s.id === gradeTarget.value?.id)
      if (fresh) gradeVersion.value = gradeJSONVersion(fresh.grade_json)
    }
  } finally {
    busy.value = false
  }
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

// ---- 学生：申诉 ----
const appealTarget = ref<Assignment | null>(null)
const appealReason = ref('')

function openAppeal(asg: Assignment) {
  error.value = ''
  appealReason.value = ''
  appealTarget.value = asg
}

function appealStatusKey(s: Appeal['status']): string {
  switch (s) {
    case 'pending':
      return 'appeal.statusPending'
    case 'accepted':
      return 'appeal.statusAccepted'
    case 'rejected':
      return 'appeal.statusRejected'
    default:
      return 'appeal.statusResolved'
  }
}

async function submitAppeal() {
  if (!appealTarget.value || !appealTarget.value.submission) return
  error.value = ''
  info.value = ''
  const reason = appealReason.value.trim()
  if (!reason) {
    error.value = i18n.t('appeal.reasonRequired')
    return
  }
  busy.value = true
  try {
    await appealCreate({
      workspace_id: session.workspaceId,
      user_id: session.userId,
      grading_id: appealTarget.value.submission.id,
      reason,
    })
    appealTarget.value = null
    info.value = i18n.t('appeal.created')
    await load()
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    busy.value = false
  }
}

// ---- 教师：复议 ----
const resolveTarget = ref<{ asgID: string; appeal: Appeal } | null>(null)
const resolveDecision = ref<AppealDecision>('accepted')
const resolveNewScore = ref<number | null>(null)
const resolveNote = ref('')

function appealOf(asgID: string, gradingID: string): Appeal | undefined {
  return (appealsByAsg.value[asgID] ?? []).find((a) => a.grading_id === gradingID)
}

function appealBadgeClass(s: Appeal['status']): string {
  switch (s) {
    case 'pending':
      return 'badge-warning'
    case 'accepted':
      return 'badge-primary'
    case 'rejected':
      return 'badge-error'
    default:
      return 'badge-success'
  }
}

function openResolve(asgID: string, sub: AssignmentSubmission) {
  const ap = appealOf(asgID, sub.id)
  if (!ap) return
  error.value = ''
  info.value = ''
  resolveTarget.value = { asgID, appeal: ap }
  resolveDecision.value = 'accepted'
  resolveNewScore.value = null
  resolveNote.value = ap.teacher_note || ''
}

async function submitResolve() {
  if (!resolveTarget.value) return
  error.value = ''
  info.value = ''
  const { asgID, appeal: ap } = resolveTarget.value
  busy.value = true
  try {
    const req: { workspace_id: string; user_id: string; appeal_id: string; decision: AppealDecision; new_score?: number; teacher_note?: string } = {
      workspace_id: session.workspaceId,
      user_id: session.userId,
      appeal_id: ap.id,
      decision: resolveDecision.value,
    }
    if (resolveDecision.value === 'accepted' && resolveNewScore.value != null) {
      req.new_score = resolveNewScore.value
    }
    if (resolveNote.value.trim()) req.teacher_note = resolveNote.value.trim()
    await appealResolve(req)
    resolveTarget.value = null
    info.value = i18n.t('appeal.resolved')
    await loadSubmissions({ id: asgID } as Assignment)
    await load()
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    busy.value = false
  }
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
          <template v-else>
            <button v-if="asg.status === 'published' && !asg.submission" class="btn btn-sm btn-primary" @click="openAnswer(asg)">
              {{ $t('assignments.submit') }}
            </button>
            <span v-if="asg.submission && asg.submission.graded_at" class="badge badge-success">
              {{ $t('assignments.graded') }} · {{ gradeJSONOverall(asg.submission.grade_json) }}/{{ gradeTotalMax(asg.submission.grade_json) }}
            </span>
            <span v-if="asg.appeal" class="badge" :class="appealBadgeClass(asg.appeal.status)">
              {{ $t(appealStatusKey(asg.appeal.status)) }}
            </span>
            <button v-if="asg.submission && asg.submission.graded_at && !asg.appeal" class="btn btn-sm" :disabled="busy" @click="openAppeal(asg)">
              {{ $t('appeal.create') }}
            </button>
            <span v-if="asg.submission && !asg.submission.graded_at && !asg.appeal" class="badge badge-warning">{{ $t('assignments.pendingGrade') }}</span>
            <span v-if="!asg.submission && asg.status !== 'published'" class="hint">{{ $t('assignments.statusDraft') }}</span>
          </template>
        </div>

        <!-- 提交名单（教师） -->
        <div v-if="isTeacher && expanded === asg.id" class="submissions">
          <div v-if="(submissionsByAsg[asg.id] ?? []).length === 0" class="empty">
            <p>{{ $t('assignments.submissionsEmpty') }}</p>
          </div>
          <div v-else class="submission-list">
            <div v-for="(s, i) in submissionsByAsg[asg.id]" :key="i" class="submission-row">
              <span class="submission-name">{{ s.display_name || s.student_user_id }}</span>
              <span v-if="s.graded_at" class="badge badge-success">{{ $t('assignments.graded') }}</span>
              <span v-else class="badge badge-warning">{{ $t('assignments.pendingGrade') }}</span>
              <span v-if="appealOf(asg.id, s.id)" class="badge" :class="appealBadgeClass(appealOf(asg.id, s.id)!.status)">
                {{ $t(appealStatusKey(appealOf(asg.id, s.id)!.status)) }}
              </span>
              <span class="hint">{{ $t('assignments.submittedAt') }} {{ fmtDue(s.created_at) }}</span>
              <button v-if="appealOf(asg.id, s.id)" class="btn btn-sm" :disabled="busy" @click="openResolve(asg.id, s)">
                {{ $t('appeal.resolve') }}
              </button>
              <button class="btn btn-sm btn-primary" :disabled="busy" @click="openGrade(s)">
                {{ $t('assignments.grade') }}
              </button>
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

    <!-- 批阅弹窗（教师） -->
    <div v-if="gradeTarget" class="modal-mask">
      <div class="card modal modal-lg">
        <div class="flex-between mb-3">
          <h3 style="margin: 0">{{ $t('assignments.gradeTitle') }} · {{ gradeTarget.display_name || gradeTarget.student_user_id }}</h3>
          <button class="btn btn-sm" @click="gradeTarget = null">{{ $t('common.close') }}</button>
        </div>

        <div v-if="gradeQuestions.length === 0" class="empty">
          <p>{{ $t('assignments.gradeNoAnswers') }}</p>
        </div>
        <template v-else>
          <div class="mb-3" style="max-height: 55vh; overflow-y: auto; display: flex; flex-direction: column; gap: var(--space-3)">
            <div v-for="(q, i) in gradeQuestions" :key="q.question_version_id" class="card">
              <div class="flex-between" style="align-items: flex-start; gap: var(--space-2)">
                <div class="grade-question-text">
                  <div class="text-secondary">{{ i + 1 }}. {{ $t('assignments.gradeType', { type: q.type }) }} · {{ gradeItemOf(q).max_score }}{{ $t('assignments.gradeMaxScore') }}</div>
                  <div class="mt-1">{{ q.payload?.stem || '' }}</div>
                </div>
              </div>
              <div class="hint mt-1">{{ $t('assignments.gradeAnswerLabel') }} {{ answerText(q.submission?.answer) }}</div>
              <div class="grade-inputs mt-2">
                <label class="grade-score-field">
                  <span class="text-secondary">{{ $t('assignments.gradeScore') }}</span>
                  <input
                    v-model.number="gradeItemOf(q).score"
                    class="input input-sm"
                    type="number"
                    min="0"
                    :max="gradeItemOf(q).max_score"
                    @change="recalcOverall"
                  />
                </label>
                <label class="grade-comment-field">
                  <span class="text-secondary">{{ $t('assignments.gradeComment') }}</span>
                  <input v-model="gradeItemOf(q).comment" class="input input-sm" type="text" :placeholder="$t('assignments.gradeCommentPlaceholder')" />
                </label>
              </div>
            </div>
          </div>

          <div class="flex-between mb-3" style="align-items: center">
            <div class="flex gap-2" style="align-items: center">
              <span class="text-secondary">{{ $t('assignments.gradeVersion', { version: gradeVersion }) }}</span>
              <button class="btn btn-sm" :disabled="pregrading" @click="doPreGrade">
                {{ pregrading ? $t('common.processing') : $t('assignments.preGrade') }}
              </button>
            </div>
            <div class="flex gap-2" style="align-items: center">
              <span class="text-secondary">{{ $t('assignments.gradeOverall', { score: gradeOverall }) }}</span>
              <button class="btn btn-sm btn-primary" :disabled="busy" @click="saveGrade">
                {{ busy ? $t('common.saving') : $t('assignments.saveGrade') }}
              </button>
            </div>
          </div>
        </template>
      </div>
    </div>

    <!-- 申诉弹窗（学生） -->
    <div v-if="appealTarget" class="modal-mask">
      <div class="card modal">
        <div class="flex-between mb-3">
          <h3 style="margin: 0">{{ $t('appeal.title') }} · {{ appealTarget.title }}</h3>
          <button class="btn btn-sm" @click="appealTarget = null">{{ $t('common.close') }}</button>
        </div>
        <div class="field">
          <label>{{ $t('appeal.reasonLabel') }} *</label>
          <textarea
            v-model="appealReason"
            class="input"
            rows="4"
            :placeholder="$t('appeal.reasonPlaceholder')"
            style="min-height: 120px; resize: vertical"
          ></textarea>
        </div>
        <div class="flex gap-3 mt-3">
          <button class="btn" :disabled="busy" @click="appealTarget = null">{{ $t('common.cancel') }}</button>
          <button class="btn btn-primary" :disabled="busy || !appealReason.trim()" @click="submitAppeal">
            {{ busy ? $t('common.submitting') : $t('appeal.submit') }}
          </button>
        </div>
      </div>
    </div>

    <!-- 复议弹窗（教师） -->
    <div v-if="resolveTarget" class="modal-mask">
      <div class="card modal">
        <div class="flex-between mb-3">
          <h3 style="margin: 0">{{ $t('appeal.resolveTitle') }}</h3>
          <button class="btn btn-sm" @click="resolveTarget = null">{{ $t('common.close') }}</button>
        </div>
        <div class="hint mb-3" style="white-space: pre-wrap">
          {{ $t('appeal.reasonPreview', { reason: resolveTarget.appeal.reason }) }}
        </div>
        <div class="field">
          <label>{{ $t('appeal.decisionLabel') }}</label>
          <select v-model="resolveDecision" class="input">
            <option value="accepted">{{ $t('appeal.decisionAccept') }}</option>
            <option value="rejected">{{ $t('appeal.decisionReject') }}</option>
          </select>
        </div>
        <div v-if="resolveDecision === 'accepted'" class="field">
          <label>{{ $t('appeal.newScoreLabel') }}</label>
          <input v-model.number="resolveNewScore" class="input" type="number" min="0" :placeholder="$t('appeal.newScorePlaceholder')" />
        </div>
        <div class="field">
          <label>{{ $t('appeal.teacherNoteLabel') }}</label>
          <input v-model="resolveNote" class="input" type="text" :placeholder="$t('appeal.teacherNotePlaceholder')" />
        </div>
        <div class="flex gap-3 mt-3">
          <button class="btn" :disabled="busy" @click="resolveTarget = null">{{ $t('common.cancel') }}</button>
          <button class="btn btn-primary" :disabled="busy" @click="submitResolve">
            {{ busy ? $t('common.saving') : $t('appeal.submit') }}
          </button>
        </div>
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

.grade-question-text {
  flex: 1;
  min-width: 0;
}

.grade-inputs {
  display: flex;
  gap: var(--space-2);
}

.grade-score-field {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}

.grade-comment-field {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex: 1;
  min-width: 0;
}
</style>
