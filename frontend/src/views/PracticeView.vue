<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import QuestionViewer from '@/components/QuestionViewer.vue'
import { localizedMessageOf } from '@/api/client'
import { useI18nStore } from '@/stores/i18n'
import { usePracticeStore } from '@/stores/practice'

const route = useRoute()
const router = useRouter()
const store = usePracticeStore()
const i18n = useI18nStore()

const picking = ref(true)
const selected = ref<Set<string>>(new Set())
const loading = ref(false)
const error = ref('')
const current = ref(0)
const confirmSubmit = ref(false)
const submitting = ref(false)

const sessionId = computed(() => (typeof route.params.sessionId === 'string' ? route.params.sessionId : undefined))

// ---------- 选题模式 ----------
async function loadLibrary() {
  loading.value = true
  error.value = ''
  try {
    await store.loadLibrary()
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    loading.value = false
  }
}

function togglePick(id: string) {
  const next = new Set(selected.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  selected.value = next
}

async function startPractice() {
  if (selected.value.size === 0) {
    error.value = i18n.t('practice.selectFirst')
    return
  }
  loading.value = true
  error.value = ''
  try {
    await store.start([...selected.value])
    picking.value = false
    current.value = 0
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    loading.value = false
  }
}

// ---------- 答题模式 ----------
const questions = computed(() => store.session?.questions ?? [])
const q = computed(() => questions.value[current.value])

const currentAnswer = computed({
  get: () => (q.value ? (store.answers[q.value.question_version_id] ?? null) : null),
  set: (v: string | string[] | null) => {
    if (!q.value) return
    store.answers[q.value.question_version_id] = v
    void store.saveAnswer(q.value.question_version_id, v)
  },
})

const isSkipped = computed(() => (q.value ? store.session?.skipped.includes(q.value.question_version_id) ?? false : false))
const progress = computed(() =>
  questions.value.length > 0 ? Math.round(((current.value + 1) / questions.value.length) * 100) : 0,
)

// 计时
const remain = ref(0)
let timer: ReturnType<typeof setInterval> | null = null

function startTimer() {
  const limit = store.session?.time_limit_sec
  if (!limit) return
  remain.value = limit
  timer = setInterval(() => {
    remain.value--
    if (remain.value <= 0) {
      clearInterval(timer!)
      void doSubmit()
    }
  }, 1000)
}

const remainText = computed(() => {
  const m = Math.floor(remain.value / 60)
  const s = remain.value % 60
  return `${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`
})

async function doSubmit() {
  if (submitting.value) return
  submitting.value = true
  confirmSubmit.value = false
  try {
    const result = await store.submit()
    router.push(`/result/${result.session_id}`)
  } catch (e) {
    error.value = localizedMessageOf(e)
    submitting.value = false
  }
}

async function skipCurrent() {
  if (!q.value) return
  try {
    await store.skip(q.value.question_version_id)
  } catch (e) {
    error.value = localizedMessageOf(e)
  }
}

onMounted(async () => {
  if (sessionId.value) {
    picking.value = false
    loading.value = true
    try {
      await store.resume(sessionId.value)
      startTimer()
    } catch (e) {
      error.value = localizedMessageOf(e)
    } finally {
      loading.value = false
    }
  } else {
    await loadLibrary()
  }
})

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

    <!-- 选题模式 -->
    <template v-if="picking">
      <div class="page-header">
        <div>
          <h1>{{ $t('practice.title') }}</h1>
          <div class="subtitle">{{ $t('practice.subtitle') }}</div>
        </div>
        <button class="btn btn-primary" :disabled="loading || selected.size === 0" @click="startPractice">
          {{ $t('practice.startButton', { count: selected.size }) }}
        </button>
      </div>
      <div v-if="loading" class="loading"><div class="spinner"></div></div>
      <div v-else-if="!store.library || store.library.items.length === 0" class="empty">
        <div class="empty-icon">📚</div>
        <p>{{ $t('practice.emptyLibrary') }}</p>
        <RouterLink to="/library" class="btn btn-primary">{{ $t('common.goImport') }}</RouterLink>
      </div>
      <div v-else class="card">
        <table class="table">
          <thead><tr><th style="width: 40px"></th><th>{{ $t('practice.colStem') }}</th><th>{{ $t('practice.colType') }}</th><th>{{ $t('practice.colKnowledge') }}</th></tr></thead>
          <tbody>
            <tr v-for="item in store.library.items" :key="item.id" style="cursor: pointer" @click="togglePick(item.id)">
              <td><input type="checkbox" :checked="selected.has(item.id)" @click.stop /></td>
              <td>{{ item.current_version?.payload.stem?.slice(0, 60) }}</td>
              <td><span class="badge">{{ item.type }}</span></td>
              <td class="text-muted">{{ item.tags?.join(', ') || '–' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <!-- 答题模式 -->
    <template v-else-if="store.session && q">
      <div class="flex-between mb-3">
        <div class="flex gap-2" style="align-items: center">
          <button class="btn btn-sm" :disabled="current === 0" @click="current--">{{ $t('practice.prev') }}</button>
          <span class="text-secondary">{{ $t('practice.questionCount', { current: current + 1, total: questions.length }) }}</span>
          <button class="btn btn-sm" :disabled="current >= questions.length - 1" @click="current++">{{ $t('practice.next') }}</button>
          <button class="btn btn-sm btn-ghost" @click="skipCurrent">{{ $t('common.skip') }}</button>
        </div>
        <div class="flex gap-2" style="align-items: center">
          <span v-if="remain > 0" class="badge" :class="{ 'badge-error': remain < 60 }">⏱ {{ remainText }}</span>
          <button class="btn btn-primary" :disabled="submitting" @click="confirmSubmit = true">{{ $t('practice.submit') }}</button>
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
            'btn-ghost': store.session?.skipped.includes(item.question_version_id),
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
        <div v-if="isSkipped" class="hint mt-2">{{ $t('practice.skippedHint') }}</div>
      </div>
    </template>

    <!-- 提交确认 -->
    <div v-if="confirmSubmit" class="modal-mask" @click.self="confirmSubmit = false">
      <div class="card" style="width: 400px; margin: auto">
        <h3>{{ $t('practice.confirmTitle') }}</h3>
        <p class="text-secondary">
          {{ $t('practice.confirmBody', { total: questions.length, answered: Object.keys(store.answers).filter((k) => store.answers[k]).length }) }}
          {{ $t('practice.confirmBodyNote') }}
        </p>
        <div class="flex gap-3" style="justify-content: flex-end">
          <button class="btn" @click="confirmSubmit = false">{{ $t('practice.backCheck') }}</button>
          <button class="btn btn-primary" :disabled="submitting" @click="doSubmit">
            {{ submitting ? $t('practice.submitting') : $t('practice.confirmSubmit') }}
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
