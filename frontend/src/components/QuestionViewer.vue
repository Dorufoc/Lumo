<script setup lang="ts">
// 题目渲染组件：答题模式（隐藏答案）与结果模式（显示判分与解析）。
import { computed } from 'vue'
import type { Question, QuestionPayload } from '@/api/types'

const props = defineProps<{
  payload: QuestionPayload
  question?: Question
  mode: 'answer' | 'result'
  modelValue?: string | string[] | null
  grading?: { score?: number | null; max_score: number; reason?: string; status?: string } | null
  wrong?: boolean
}>()

const emit = defineEmits<{ (e: 'update:modelValue', v: string | string[] | null): void }>()

const isChoice = computed(() => props.payload.type === 'single_choice' || props.payload.type === 'multiple_choice')
const isMulti = computed(() => props.payload.type === 'multiple_choice')
const isFill = computed(() => props.payload.type === 'fill_blank')
const isShort = computed(() => props.payload.type === 'short_answer' || props.payload.type === 'code')

const stdAnswer = computed(() => props.payload.answer)
const stdSet = computed(() => new Set(Array.isArray(stdAnswer.value) ? stdAnswer.value : [stdAnswer.value]))
const mySet = computed(() => {
  const v = props.modelValue
  if (Array.isArray(v)) return new Set(v)
  if (typeof v === 'string') return new Set([v])
  return new Set<string>()
})

function toggle(key: string) {
  if (props.mode !== 'answer') return
  if (isMulti.value) {
    const next = new Set(mySet.value)
    if (next.has(key)) next.delete(key)
    else next.add(key)
    emit('update:modelValue', [...next])
  } else {
    emit('update:modelValue', mySet.value.has(key) ? null : key)
  }
}

function optionClass(key: string): string {
  if (props.mode === 'result') {
    const mine = mySet.value.has(key)
    const std = stdSet.value.has(key)
    if (std) return 'correct'
    if (mine) return 'incorrect'
    return ''
  }
  return mySet.value.has(key) ? 'selected' : ''
}

const fillAnswer = computed({
  get: () => {
    const v = props.modelValue
    if (Array.isArray(v)) return v.join('\n')
    return typeof v === 'string' ? v : ''
  },
  set: (v: string) => {
    const parts = v.split('\n').map((s) => s.trim())
    emit('update:modelValue', parts.length > 1 ? parts : (parts[0] ?? ''))
  },
})

const answerTextProxy = computed({
  get: () => (typeof props.modelValue === 'string' ? props.modelValue : ''),
  set: (v: string) => emit('update:modelValue', v),
})

const answerText = computed(() => {
  const v = props.modelValue
  if (Array.isArray(v)) return v.join('、')
  return typeof v === 'string' ? v : ''
})

const fillText = computed(() => (typeof stdAnswer.value === 'string' ? stdAnswer.value : stdAnswer.value.join(' / ')))
</script>

<template>
  <div>
    <div class="question-stem">{{ payload.stem }}</div>

    <!-- 选择题 -->
    <div v-if="isChoice">
      <div v-for="opt in payload.options ?? []" :key="opt.key" class="option-item" :class="optionClass(opt.key)" @click="toggle(opt.key)">
        <span class="option-key">{{ opt.key }}</span>
        <span class="grow">{{ opt.text }}</span>
        <span v-if="mode === 'result' && stdSet.has(opt.key)" class="text-success">✓</span>
        <span v-else-if="mode === 'result' && mySet.has(opt.key)" class="text-error">✗</span>
      </div>
    </div>

    <!-- 填空 -->
    <div v-else-if="isFill">
      <textarea
        v-if="mode === 'answer'"
        v-model="fillAnswer"
        class="textarea"
        :placeholder="$t('question.fillPlaceholder')"
        rows="2"
      ></textarea>
      <div v-else>
        <div class="field"><label>{{ $t('question.yourAnswer') }}</label><div class="input" style="min-height: 40px">{{ answerText || $t('question.unanswered') }}</div></div>
        <div class="field"><label>{{ $t('question.standardAnswer') }}</label><div class="input" style="min-height: 40px">{{ fillText }}</div></div>
      </div>
    </div>

    <!-- 简答/代码 -->
    <div v-else-if="isShort">
      <div v-if="payload.type === 'code' && payload.grading_config?.language" class="badge badge-primary mb-2">
        {{ $t('question.codeHint', { lang: String(payload.grading_config.language) }) }}
      </div>
      <textarea
        v-if="mode === 'answer'"
        v-model="answerTextProxy"
        class="textarea"
        :placeholder="payload.type === 'code' ? $t('question.codePlaceholder') : $t('question.answerPlaceholder')"
        rows="6"
      ></textarea>
      <div v-else>
        <div class="field"><label>{{ $t('question.yourAnswer') }}</label><div class="input" style="min-height: 40px; white-space: pre-wrap">{{ answerText || $t('question.unanswered') }}</div></div>
      </div>
    </div>

    <!-- 结果：判分与解析 -->
    <template v-if="mode === 'result'">
      <div class="flex gap-3 mt-3 mb-2">
        <span class="badge" :class="wrong ? 'badge-error' : 'badge-success'">
          {{ wrong ? $t('question.wrong') : $t('question.correct') }} · {{ grading?.score ?? 0 }}/{{ grading?.max_score ?? 0 }}
        </span>
        <span v-if="grading?.status === 'failed'" class="badge badge-warning">{{ $t('question.gradingFailed') }}</span>
        <span v-else-if="grading?.status === 'needs_review'" class="badge badge-warning">{{ $t('question.needsReview') }}</span>
      </div>
      <div v-if="grading?.reason" class="text-secondary mb-3" style="white-space: pre-wrap">{{ grading.reason }}</div>
      <div v-if="payload.analysis" class="card" style="background: var(--color-primary-soft); border-color: var(--color-primary)">
        <div class="card-title" style="font-size: var(--text-base)">{{ $t('question.analysis') }}</div>
        <div style="white-space: pre-wrap">{{ payload.analysis }}</div>
      </div>
    </template>
  </div>
</template>
