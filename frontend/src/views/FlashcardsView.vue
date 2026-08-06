<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { localizedMessageOf, upload } from '@/api/client'
import type { FlashcardImportBatch } from '@/api/types'
import { useI18nStore } from '@/stores/i18n'
import { useFlashcardStore } from '@/stores/flashcard'

const store = useFlashcardStore()
const i18n = useI18nStore()

const loading = ref(true)
const error = ref('')
const info = ref('')

const current = ref(0)
const revealed = ref(false)
const submitting = ref(false)
const rating = ref<'again' | 'hard' | 'good' | null>(null)

const cards = computed(() => store.due)
const card = computed(() => cards.value[current.value] ?? null)

const done = ref(0)

// ---------- 复习 ----------
async function load() {
  loading.value = true
  error.value = ''
  try {
    await store.loadDue(100)
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    loading.value = false
  }
}

async function submitRating(r: 'again' | 'hard' | 'good') {
  const c = card.value
  if (!c || submitting.value) return
  submitting.value = true
  rating.value = r
  try {
    await store.review(c.id, r)
    done.value++
    revealed.value = false
    store.due.splice(current.value, 1)
    if (current.value >= store.due.length) current.value = Math.max(0, store.due.length - 1)
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    submitting.value = false
    rating.value = null
  }
}

// ---------- 手动创建 ----------
const front = ref('')
const back = ref('')
const creating = ref(false)

async function createCard() {
  if (!front.value.trim() || !back.value.trim()) return
  creating.value = true
  error.value = ''
  try {
    await store.create({ front: front.value.trim(), back: back.value.trim() })
    front.value = ''
    back.value = ''
    info.value = i18n.t('flashcard.added')
    await store.loadDue(100)
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    creating.value = false
  }
}

// ---------- CSV 导入 ----------
const csvFile = ref<File | null>(null)
const importing = ref(false)
const importBatch = ref<FlashcardImportBatch | null>(null)

function onCsvFile(e: Event) {
  const input = e.target as HTMLInputElement
  csvFile.value = input.files?.[0] ?? null
  importBatch.value = null
}

async function doImportCsv() {
  if (!csvFile.value) return
  importing.value = true
  error.value = ''
  info.value = ''
  try {
    const up = await upload<{ path: string; file_name: string }>('LibraryUpload', csvFile.value)
    importBatch.value = await store.importCsv(up.path)
    info.value = i18n.t('flashcard.importDone', { count: importBatch.value.valid_count })
    await store.loadDue(100)
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    importing.value = false
  }
}

// ---------- Anki 导出 ----------
const exporting = ref(false)

async function doExportAnki() {
  exporting.value = true
  error.value = ''
  info.value = ''
  try {
    const res = await store.exportAnki()
    info.value = i18n.t('flashcard.exportDone', { name: res.file_name })
    window.location.href = `/api/v1/files?path=${encodeURIComponent(res.path)}`
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    exporting.value = false
  }
}

// ---------- 从题库生成 ----------
const sourceRef = ref('')

async function doGenerate() {
  if (!sourceRef.value.trim()) return
  try {
    const cards2 = await store.generate(sourceRef.value.trim())
    info.value = i18n.t('flashcard.generated', { count: cards2.length })
    sourceRef.value = ''
    await store.loadDue(100)
  } catch (e) {
    error.value = localizedMessageOf(e)
  }
}

onMounted(load)

function stateKey(s: string): string {
  return {
    learning: 'flashcard.stateLearning',
    review: 'flashcard.stateReview',
    mastered: 'flashcard.stateMastered',
    archived: 'flashcard.stateArchived',
  }[s] ?? s
}

function typeKey(c: string): string {
  return {
    basic: 'flashcard.typeBasic',
    choice: 'flashcard.typeChoice',
    cloze: 'flashcard.typeCloze',
    code: 'flashcard.typeCode',
  }[c] ?? c
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('flashcard.title') }}</h1>
        <div class="subtitle">{{ $t('flashcard.subtitle') }}</div>
      </div>
      <button class="btn" @click="load">{{ $t('common.refresh') }}</button>
    </div>

    <div v-if="error" class="error-banner">{{ error }}</div>
    <div v-if="info" class="offline-banner">{{ info }}</div>

    <!-- 到期队列 -->
    <div v-if="loading" class="loading"><div class="spinner"></div></div>

    <div v-else-if="cards.length === 0" class="empty">
      <div class="empty-icon">🎴</div>
      <p>{{ $t('flashcard.empty') }}</p>
      <p class="hint">{{ $t('flashcard.emptyHint') }}</p>
    </div>

    <template v-else>
      <div class="flex-between mb-3">
        <span class="text-secondary">{{ $t('flashcard.progress', { due: cards.length, done }) }}</span>
        <div class="progress grow" style="max-width: 300px"><div :style="{ width: '30%' }"></div></div>
      </div>

      <div class="card" v-if="card">
        <div class="flashcard-face" :class="{ revealed }">
          <div class="badge mb-2">{{ $t(typeKey(card.card_type)) }} · {{ $t(stateKey(card.state)) }}</div>
          <template v-if="!revealed">
            <h3 class="flashcard-front">{{ card.front }}</h3>
            <div class="flex" style="justify-content: flex-end; margin-top: var(--space-4)">
              <button class="btn btn-primary" @click="revealed = true">{{ $t('flashcard.reveal') }}</button>
            </div>
          </template>
          <template v-else>
            <h3 class="flashcard-front">{{ card.front }}</h3>
            <div class="flashcard-back">{{ card.back }}</div>
            <div class="flex gap-3 mt-4" style="justify-content: flex-end">
              <button class="btn btn-danger" :disabled="submitting" @click="submitRating('again')">
                {{ rating === 'again' ? $t('common.submitting') : $t('flashcard.again') }}
              </button>
              <button class="btn" :disabled="submitting" @click="submitRating('hard')">
                {{ rating === 'hard' ? $t('common.submitting') : $t('flashcard.hard') }}
              </button>
              <button class="btn btn-success" :disabled="submitting" @click="submitRating('good')">
                {{ rating === 'good' ? $t('common.submitting') : $t('flashcard.good') }}
              </button>
            </div>
          </template>
        </div>
      </div>
    </template>

    <!-- 管理面板 -->
    <div class="grid mt-4" style="grid-template-columns: 1fr 1fr">
      <div class="card">
        <div class="card-title">{{ $t('flashcard.createTitle') }}</div>
        <div class="flex-col gap-2">
          <input v-model="front" class="input" :placeholder="$t('flashcard.frontPlaceholder')" />
          <textarea v-model="back" class="textarea" :placeholder="$t('flashcard.backPlaceholder')" style="min-height: 80px"></textarea>
          <button class="btn btn-primary" :disabled="creating || !front.trim() || !back.trim()" @click="createCard">
            {{ creating ? $t('common.creating') : $t('flashcard.create') }}
          </button>
        </div>
      </div>

      <div class="flex-col gap-3">
        <div class="card">
          <div class="card-title">{{ $t('flashcard.importTitle') }}</div>
          <div class="flex gap-3">
            <input type="file" accept=".csv" class="input" style="max-width: 240px" @change="onCsvFile" />
            <button class="btn" :disabled="!csvFile || importing" @click="doImportCsv">
              {{ importing ? $t('common.importing') : $t('flashcard.import') }}
            </button>
          </div>
          <div v-if="csvFile" class="hint mt-2">{{ csvFile.name }} · {{ (csvFile.size / 1024).toFixed(1) }} KB</div>
          <p class="hint mt-2">{{ $t('flashcard.importHint') }}</p>
        </div>

        <div class="card">
          <div class="card-title">{{ $t('flashcard.exportTitle') }}</div>
          <div class="flex gap-3">
            <button class="btn" :disabled="exporting" @click="doExportAnki">
              {{ exporting ? $t('common.submitting') : $t('flashcard.export') }}
            </button>
          </div>
        </div>

        <div class="card">
          <div class="card-title">{{ $t('flashcard.generateTitle') }}</div>
          <div class="flex gap-3">
            <input v-model="sourceRef" class="input" style="max-width: 240px" :placeholder="$t('flashcard.generatePlaceholder')" />
            <button class="btn" :disabled="!sourceRef.trim()" @click="doGenerate">{{ $t('flashcard.generate') }}</button>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.flashcard-face {
  min-height: 240px;
  display: flex;
  flex-direction: column;
  justify-content: center;
}
.flashcard-front {
  font-size: var(--font-lg);
  margin: var(--space-2) 0;
}
.flashcard-back {
  font-size: var(--font-md);
  white-space: pre-wrap;
  color: var(--text-secondary);
  padding: var(--space-3);
  background: var(--surface-2);
  border-radius: var(--radius);
}
</style>
