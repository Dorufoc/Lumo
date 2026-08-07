<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { call, localizedMessageOf, openEventStream } from '@/api/client'
import { speakingResultGet, speakingSubmit, speakingUpload, ttsPlay } from '@/api/speech'
import type { PracticeSession, Question, QuestionPage, SpeakingResult, SubmissionDraft } from '@/api/types'
import { useI18nStore } from '@/stores/i18n'
import { useSessionStore } from '@/stores/session'

const session = useSessionStore()
const i18n = useI18nStore()

const loading = ref(false)
const error = ref('')
const info = ref('')

// ---------- 题目选择 ----------
const questions = ref<Question[]>([])
const selectedId = ref('')
const practice = ref<PracticeSession | null>(null)
const submissionId = ref('')
const questionText = ref('')

async function loadQuestions() {
  loading.value = true
  error.value = ''
  try {
    const page = await call<QuestionPage>('QuestionList', {
      workspace_id: session.workspaceId,
      status: 'published',
      limit: 100,
    })
    questions.value = page?.items ?? []
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    loading.value = false
  }
}

/** 选择题目 → 开练习会话 + 保存占位答案生成 submission_id。 */
async function startPractice() {
  const qid = selectedId.value
  if (!qid) {
    error.value = i18n.t('speaking.pickQuestion')
    return
  }
  loading.value = true
  error.value = ''
  result.value = null
  recordedBlob.value = null
  ttsUrl.value = ''
  try {
    const q = questions.value.find((x) => x.id === qid)
    questionText.value = q?.current_version?.payload?.stem ?? ''
    const s = await call<PracticeSession>('PracticeStart', {
      workspace_id: session.workspaceId,
      user_id: session.userId,
      mode: 'practice',
      question_ids: [qid],
      idempotency_key: `spk-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`,
    })
    const draft = await call<SubmissionDraft>('PracticeSaveAnswer', {
      workspace_id: session.workspaceId,
      session_id: s.id,
      question_version_id: s.questions?.[0]?.question_version_id ?? '',
      answer: JSON.stringify(''),
      client_sequence: 1,
    })
    practice.value = s
    submissionId.value = draft.id
    info.value = i18n.t('speaking.ready')
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    loading.value = false
  }
}

// ---------- TTS 朗读 ----------
const ttsBusy = ref(false)
const ttsUrl = ref('')
const ttsAudio = ref<HTMLAudioElement | null>(null)

async function playTTS() {
  if (!selectedId.value) {
    error.value = i18n.t('speaking.pickQuestion')
    return
  }
  ttsBusy.value = true
  error.value = ''
  try {
    const r = await ttsPlay({
      workspace_id: session.workspaceId,
      ref_type: 'question',
      ref_id: selectedId.value,
      speed: 1,
    })
    ttsUrl.value = `/api/v1/files?path=${encodeURIComponent(r.audio_path)}`
    // 音频就绪后自动播放（浏览器 autoplay 策略：用户手势触发的 play() 允许）
    await new Promise<void>((resolve) => {
      const el = ttsAudio.value
      if (!el) return resolve()
      el.onended = () => resolve()
      el.onerror = () => resolve()
      void el.play().catch(() => resolve())
    })
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    ttsBusy.value = false
  }
}

// ---------- 录音（MediaRecorder → webm → WAV PCM16） ----------
const recording = ref(false)
const recordedBlob = ref<Blob | null>(null)
let mediaRecorder: MediaRecorder | null = null
let recChunks: BlobPart[] = []

async function startRecord() {
  error.value = ''
  if (!navigator.mediaDevices?.getUserMedia || typeof MediaRecorder === 'undefined') {
    error.value = i18n.t('speaking.micUnsupported')
    return
  }
  try {
    const stream = await navigator.mediaDevices.getUserMedia({ audio: true })
    recChunks = []
    const mime = MediaRecorder.isTypeSupported('audio/webm;codecs=opus') ? 'audio/webm;codecs=opus' : ''
    mediaRecorder = new MediaRecorder(stream, mime ? { mimeType: mime } : undefined)
    mediaRecorder.ondataavailable = (e) => {
      if (e.data.size > 0) recChunks.push(e.data)
    }
    mediaRecorder.onstop = () => void onRecordStop(stream)
    mediaRecorder.start()
    recording.value = true
  } catch {
    error.value = i18n.t('speaking.micDenied')
  }
}

function stopRecord() {
  mediaRecorder?.stop()
  recording.value = false
}

async function onRecordStop(stream: MediaStream) {
  stream.getTracks().forEach((t) => t.stop())
  const blob = new Blob(recChunks, { type: mediaRecorder?.mimeType || 'audio/webm' })
  recChunks = []
  try {
    const wav = await toWav(blob)
    if (wav.size > 20 << 20) {
      error.value = i18n.t('speaking.tooLarge')
      return
    }
    recordedBlob.value = wav
    info.value = i18n.t('speaking.recorded', { size: (wav.size / 1024).toFixed(1) })
  } catch {
    error.value = i18n.t('speaking.formatError')
  }
}

/** 非 wav/mp3/m4a（如 webm）→ AudioContext decode → 重编码 WAV PCM16（与后端 MockTTS 头部一致）。 */
async function toWav(blob: Blob): Promise<Blob> {
  if (/audio\/(wav|x-wav|mp3|m4a|mp4|mpeg)/.test(blob.type)) return blob
  const buf = await blob.arrayBuffer()
  const Ctor = (window.AudioContext ?? (window as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext)!
  const ctx = new Ctor()
  try {
    const audioBuf = await ctx.decodeAudioData(buf)
    return new Blob([encodeWAV(audioBuf.getChannelData(0), audioBuf.sampleRate)], { type: 'audio/wav' })
  } finally {
    void ctx.close()
  }
}

/** 44 字节 RIFF 头 + 16-bit PCM 单声道（与 internal/provider/speech_mock.go wavPCM16 一致）。 */
function encodeWAV(samples: Float32Array, sampleRate: number): ArrayBuffer {
  const buffer = new ArrayBuffer(44 + samples.length * 2)
  const view = new DataView(buffer)
  const writeStr = (off: number, s: string) => {
    for (let i = 0; i < s.length; i++) view.setUint8(off + i, s.charCodeAt(i))
  }
  writeStr(0, 'RIFF')
  view.setUint32(4, 36 + samples.length * 2, true)
  writeStr(8, 'WAVE')
  writeStr(12, 'fmt ')
  view.setUint32(16, 16, true)
  view.setUint16(20, 1, true) // PCM
  view.setUint16(22, 1, true) // 单声道
  view.setUint32(24, sampleRate, true)
  view.setUint32(28, sampleRate * 2, true)
  view.setUint16(32, 2, true)
  view.setUint16(34, 16, true)
  writeStr(36, 'data')
  view.setUint32(40, samples.length * 2, true)
  let offset = 44
  for (let i = 0; i < samples.length; i++) {
    const s = Math.max(-1, Math.min(1, samples[i]))
    view.setInt16(offset, s < 0 ? s * 0x8000 : s * 0x7fff, true)
    offset += 2
  }
  return buffer
}

// ---------- 提交与结果 ----------
const submitting = ref(false)
const result = ref<SpeakingResult | null>(null)

async function submit() {
  if (!recordedBlob.value || submitting.value) return
  if (!submissionId.value) {
    error.value = i18n.t('speaking.pickQuestion')
    return
  }
  submitting.value = true
  error.value = ''
  try {
    const file = new File([recordedBlob.value], `speaking-${Date.now()}.wav`, { type: 'audio/wav' })
    const up = await speakingUpload(file, session.userId)
    const key = `spk-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`
    result.value = await speakingSubmit({
      workspace_id: session.workspaceId,
      submission_id: submissionId.value,
      audio_path: up.path,
      idempotency_key: key,
    })
    info.value = i18n.t('speaking.success')
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    submitting.value = false
  }
}

/** grading:updated 与当前结果匹配时自动刷新（不轮询）。 */
let stream: { close: () => void } | null = null

async function refreshResult() {
  if (!submissionId.value) return
  try {
    result.value = await speakingResultGet({
      workspace_id: session.workspaceId,
      submission_id: submissionId.value,
    })
  } catch {
    // 保留当前结果
  }
}

const scoreEntries = computed(() => {
  const s = result.value?.scores
  if (!s) return []
  return [
    { key: 'pronunciation', label: i18n.t('speaking.pronunciation'), value: s.pronunciation ?? 0 },
    { key: 'fluency', label: i18n.t('speaking.fluency'), value: s.fluency ?? 0 },
    { key: 'completeness', label: i18n.t('speaking.completeness'), value: s.completeness ?? 0 },
    { key: 'grammar', label: i18n.t('speaking.grammar'), value: s.grammar ?? 0 },
  ]
})

const overall = computed(() => {
  const s = result.value?.scores
  if (!s) return 0
  const vals = Object.values(s)
  return vals.length > 0 ? vals.reduce((a, b) => a + b, 0) / vals.length : 0
})

// ---------- Provider 配置状态 ----------
const ps = computed(() => session.settings?.provider_status ?? {})
const hasTTS = computed(() => ps.value.tts?.configured ?? false)
const hasASR = computed(() => ps.value.asr?.configured ?? false)
const notConfigured = computed(() => !hasTTS.value && !hasASR.value)

onMounted(() => {
  void loadQuestions()
  if (!session.settings) void session.refreshSettings()
  // 订阅用户级领域事件（grading:updated）
  stream = openEventStream(
    `spk-${Date.now()}`,
    '',
    {
      onGradingUpdated: (g) => {
        if (result.value && g.grading_id === result.value.id && g.status === 'graded') {
          void refreshResult()
        }
      },
    },
    session.userId,
  )
})

onUnmounted(() => {
  stream?.close()
  stream = null
  mediaRecorder?.state === 'recording' && mediaRecorder.stop()
})
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('speaking.title') }}</h1>
        <div class="subtitle">{{ $t('speaking.subtitle') }}</div>
      </div>
    </div>

    <div v-if="error" class="error-banner">
      <span>{{ error }}</span>
      <button class="btn btn-sm" @click="error = ''">{{ $t('common.close') }}</button>
    </div>
    <div v-if="info" class="offline-banner">{{ info }}</div>

    <!-- 未配置 Provider 引导 -->
    <div v-if="notConfigured" class="card">
      <div class="card-title">{{ $t('speaking.notConfigured') }}</div>
      <p class="text-secondary mb-3">{{ $t('speaking.configHint') }}</p>
      <RouterLink class="btn btn-primary" to="/settings">{{ $t('speaking.goSettings') }}</RouterLink>
    </div>

    <template v-else>
      <!-- 题目选择 -->
      <div class="card">
        <div class="card-title">{{ $t('speaking.selectTitle') }}</div>
        <div class="form-row">
          <div class="field">
            <label>{{ $t('speaking.selectLabel') }}</label>
            <select v-model="selectedId" class="select" :disabled="loading">
              <option value="">{{ $t('speaking.placeholder') }}</option>
              <option v-for="q in questions" :key="q.id" :value="q.id">{{ q.current_version?.payload?.stem ?? q.id }}</option>
            </select>
          </div>
          <div class="field">
            <label>&nbsp;</label>
            <div class="flex gap-2">
              <button class="btn btn-primary" :disabled="loading" @click="startPractice">
                {{ loading ? $t('speaking.processing') : $t('speaking.startPractice') }}
              </button>
            </div>
          </div>
        </div>
        <div v-if="questions.length === 0 && !loading" class="hint mt-2">{{ $t('speaking.emptyQuestions') }}</div>

        <!-- 朗读文本 + TTS -->
        <div v-if="questionText" class="reading-box mt-3">
          <div class="reading-text">{{ questionText }}</div>
          <div v-if="hasTTS" class="flex gap-2 mt-2">
            <button class="btn" :disabled="ttsBusy" @click="playTTS">
              {{ ttsBusy ? $t('speaking.ttsBusy') : $t('speaking.reading') }}
            </button>
          </div>
          <audio ref="ttsAudio" v-if="ttsUrl" :src="ttsUrl" style="display: none" />
        </div>
      </div>

      <!-- 录音与提交 -->
      <div class="card">
        <div class="card-title">{{ $t('speaking.recordTitle') }}</div>
        <p v-if="!submissionId" class="text-secondary mb-3">{{ $t('speaking.startFirst') }}</p>
        <template v-else>
          <div class="flex gap-3" style="align-items: center">
            <button v-if="!recording" class="btn btn-primary" @click="startRecord">
              {{ $t('speaking.record') }}
            </button>
            <button v-else class="btn btn-danger" @click="stopRecord">
              {{ $t('speaking.stop') }}
            </button>
            <span v-if="recording" class="badge badge-warning">{{ $t('speaking.recording') }}</span>
            <span v-if="recordedBlob && !recording" class="badge badge-success">{{ $t('speaking.recordedOk') }}</span>
          </div>
          <div class="flex gap-2 mt-3" style="justify-content: flex-end">
            <button class="btn btn-primary" :disabled="!recordedBlob || submitting" @click="submit">
              {{ submitting ? $t('speaking.submitting') : $t('speaking.submit') }}
            </button>
          </div>
          <div v-if="hasASR" class="hint mt-2">{{ $t('speaking.idempotencyHint') }}</div>
        </template>
      </div>

      <!-- 评分结果 -->
      <div v-if="result" class="card">
        <div class="flex-between mb-3">
          <div class="card-title" style="margin: 0">{{ $t('speaking.scores') }}</div>
          <span class="badge badge-success">{{ $t('speaking.overall') }} {{ overall.toFixed(1) }}</span>
        </div>
        <div class="score-list">
          <div v-for="s in scoreEntries" :key="s.key" class="score-row">
            <span class="score-label">{{ s.label }}</span>
            <div class="score-bar">
              <div class="score-fill" :style="{ width: `${Math.min(100, s.value * 10)}%` }" />
            </div>
            <span class="score-value">{{ s.value.toFixed(1) }}</span>
          </div>
        </div>
        <div class="transcript mt-3">
          <div class="transcript-label">{{ $t('speaking.transcript') }}</div>
          <div v-if="result.transcript" class="transcript-text">{{ result.transcript }}</div>
          <div v-else class="hint">{{ $t('speaking.transcriptEmpty') }}</div>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.reading-box {
  padding: var(--space-4);
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  background: var(--bg-subtle);
}

.reading-text {
  font-size: var(--text-lg);
  line-height: 1.7;
  color: var(--text);
}

.score-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}

.score-row {
  display: flex;
  align-items: center;
  gap: var(--space-3);
}

.score-label {
  width: 88px;
  font-size: var(--text-sm);
  color: var(--text-secondary);
  flex-shrink: 0;
}

.score-bar {
  flex: 1;
  height: 8px;
  border-radius: var(--radius-full);
  background: var(--bg-subtle);
  overflow: hidden;
}

.score-fill {
  height: 100%;
  background: var(--gradient);
  border-radius: var(--radius-full);
  transition: width 0.4s var(--ease-out);
}

.score-value {
  width: 40px;
  text-align: right;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

.transcript {
  padding-top: var(--space-3);
  border-top: 1px solid var(--border);
}

.transcript-label {
  font-size: var(--text-sm);
  color: var(--text-secondary);
  margin-bottom: var(--space-2);
}

.transcript-text {
  line-height: 1.8;
  color: var(--text);
}
</style>
