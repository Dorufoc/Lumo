<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import QuestionViewer from '@/components/QuestionViewer.vue'
import { call } from '@/api/client'
import type { PracticeResult } from '@/api/types'
import { useSessionStore } from '@/stores/session'

const route = useRoute()
const session = useSessionStore()

const result = ref<PracticeResult | null>(null)
const loading = ref(true)
const error = ref('')
const openIndex = ref<number | null>(0)

onMounted(async () => {
  const sessionId = route.params.sessionId as string
  try {
    result.value = await call<PracticeResult>('PracticeGetResult', {
      workspace_id: session.workspaceId,
      session_id: sessionId,
    })
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
})

const accuracy = computed(() => {
  const r = result.value
  if (!r || r.max_score === 0) return 0
  return Math.round((r.total_score / r.max_score) * 100)
})
const correctCount = computed(() => result.value?.questions.filter((q) => !q.is_wrong).length ?? 0)

function answerOf(q: PracticeResult['questions'][number]): string | string[] | null {
  const a = q.submission?.answer
  if (a === null || a === undefined) return null
  if (typeof a === 'string') return a
  if (Array.isArray(a)) return a as string[]
  return null
}
</script>

<template>
  <div>
    <div v-if="error" class="error-banner">{{ error }}</div>
    <div v-if="loading" class="loading"><div class="spinner"></div></div>

    <template v-if="result">
      <div class="card" style="text-align: center; padding: var(--space-6)">
        <h1 style="font-size: 40px" :class="accuracy >= 60 ? 'text-success' : 'text-error'">
          {{ accuracy }}%
        </h1>
        <div class="text-secondary mb-3">
          总分 {{ result.total_score }} / {{ result.max_score }} · 答对 {{ correctCount }} / {{ result.questions.length }} 题
        </div>
        <div class="flex gap-3" style="justify-content: center">
          <RouterLink to="/practice" class="btn btn-primary">再练一组</RouterLink>
          <RouterLink to="/review" class="btn">去复习错题</RouterLink>
        </div>
      </div>

      <div class="card" v-for="(q, i) in result.questions" :key="q.question_version_id">
        <div class="flex-between mb-2">
          <div class="flex gap-2" style="align-items: center">
            <span class="badge" :class="q.is_wrong ? 'badge-error' : 'badge-success'">
              {{ q.is_wrong ? '✗' : '✓' }} 第 {{ i + 1 }} 题
            </span>
            <span class="badge">{{ q.type }}</span>
            <span v-if="q.skipped" class="badge badge-warning">已跳过</span>
          </div>
          <button class="btn btn-sm btn-ghost" @click="openIndex = openIndex === i ? null : i">
            {{ openIndex === i ? '收起' : '查看' }}
          </button>
        </div>
        <QuestionViewer
          v-if="openIndex === i"
          :payload="q.payload"
          :mode="'result'"
          :model-value="answerOf(q)"
          :grading="q.grading"
          :wrong="q.is_wrong"
        />
        <div v-else class="text-secondary" style="white-space: pre-wrap">
          {{ (q.payload.stem ?? '').slice(0, 80) }}{{ (q.payload.stem ?? '').length > 80 ? '…' : '' }}
        </div>
      </div>
    </template>
  </div>
</template>
