<script setup lang="ts">
import { onMounted, ref } from 'vue'
import QuestionViewer from '@/components/QuestionViewer.vue'
import { call } from '@/api/client'
import type { ReviewCard } from '@/api/types'
import { useSessionStore } from '@/stores/session'

const session = useSessionStore()

const due = ref<ReviewCard[]>([])
const loading = ref(true)
const error = ref('')
const current = ref(0)
const rating = ref<'again' | 'hard' | 'good' | null>(null)
const submitting = ref(false)
const done = ref(0)

const card = () => due.value[current.value]
const revealed = ref(false)

onMounted(load)

async function load() {
  loading.value = true
  error.value = ''
  try {
    due.value = await call<ReviewCard[]>('ReviewListDue', {
      workspace_id: session.workspaceId,
      user_id: session.userId,
      limit: 50,
    })
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

function answerOf(): string | string[] | null {
  const c = card()
  const a = c?.wrong_answer?.answer
  if (a === null || a === undefined) return null
  if (typeof a === 'string') return a
  if (Array.isArray(a)) return a as string[]
  return null
}

async function submitRating(r: 'again' | 'hard' | 'good') {
  const c = card()
  if (!c || submitting.value) return
  submitting.value = true
  rating.value = r
  try {
    await call<ReviewCard>('ReviewSubmit', {
      workspace_id: session.workspaceId,
      review_card_id: c.id,
      rating: r,
      idempotency_key: `rv-${c.id}-${Date.now()}`,
    })
    done.value++
    revealed.value = false
    due.value.splice(current.value, 1)
    if (current.value >= due.value.length) current.value = Math.max(0, due.value.length - 1)
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    submitting.value = false
    rating.value = null
  }
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>错题复习</h1>
        <div class="subtitle">间隔记忆：不会 → 模糊 → 掌握，由服务端计算下次复习时间</div>
      </div>
      <button class="btn" @click="load">刷新</button>
    </div>

    <div v-if="error" class="error-banner">{{ error }}</div>
    <div v-if="loading" class="loading"><div class="spinner"></div></div>

    <div v-else-if="due.length === 0" class="empty">
      <div class="empty-icon">🎉</div>
      <p>当前没有到期的复习卡，太棒了！</p>
      <p class="hint">错题会自动进入复习队列</p>
    </div>

    <template v-else>
      <div class="flex-between mb-3">
        <span class="text-secondary">还剩 {{ due.length }} 张 · 本轮已完成 {{ done }} 张</span>
        <div class="progress grow" style="max-width: 300px"><div :style="{ width: '30%' }"></div></div>
      </div>

      <div class="card" v-if="card()">
        <!-- 先回忆：只显示题干；点击“显示答案”后展示判分与解析 -->
        <QuestionViewer
          v-if="!revealed"
          :key="card()!.id"
          :payload="card()!.question?.current_version?.payload ?? ({ type: 'short_answer', stem: '', answer: '' } as any)"
          :mode="'answer'"
        />
        <template v-else>
          <QuestionViewer
            :key="card()!.id + '-revealed'"
            :payload="card()!.question?.current_version?.payload ?? ({ type: 'short_answer', stem: '', answer: '' } as any)"
            :mode="'result'"
            :model-value="answerOf()"
            :wrong="true"
          />
          <div class="flex gap-3 mt-4" style="justify-content: flex-end">
            <button class="btn btn-danger" :disabled="submitting" @click="submitRating('again')">
              {{ rating === 'again' ? '提交中…' : '😵 不会' }}
            </button>
            <button class="btn" :disabled="submitting" @click="submitRating('hard')">
              {{ rating === 'hard' ? '提交中…' : '🤔 模糊' }}
            </button>
            <button class="btn btn-success" :disabled="submitting" @click="submitRating('good')">
              {{ rating === 'good' ? '提交中…' : '😎 掌握' }}
            </button>
          </div>
        </template>
        <div v-if="!revealed" class="flex" style="justify-content: flex-end; margin-top: var(--space-4)">
          <button class="btn" @click="revealed = true">显示答案与解析</button>
        </div>
      </div>
    </template>
  </div>
</template>
