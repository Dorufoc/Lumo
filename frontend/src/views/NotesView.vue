<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { localizedMessageOf } from '@/api/client'
import type { Note, NoteKind } from '@/api/types'
import { useI18nStore } from '@/stores/i18n'
import { useNoteStore } from '@/stores/note'

const store = useNoteStore()
const i18n = useI18nStore()

const loading = ref(true)
const error = ref('')
const info = ref('')

// ---------- 列表与筛选 ----------
const keyword = ref('')
const kindFilter = ref<NoteKind | ''>('')
const tagFilter = ref('')
const searched = ref('')

const notes = computed(() => store.notes)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const page = await store.list({
      kind: kindFilter.value || undefined,
      tag: tagFilter.value.trim() || undefined,
      keyword: searched.value || undefined,
      limit: 50,
    })
    if (page.has_more) info.value = i18n.t('note.truncatedHint')
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    loading.value = false
  }
}

function doSearch() {
  searched.value = keyword.value.trim()
  void load()
}

function resetFilters() {
  keyword.value = ''
  kindFilter.value = ''
  tagFilter.value = ''
  searched.value = ''
  void load()
}

function kindKey(kind: string): string {
  return {
    question: 'note.kindQuestion',
    document: 'note.kindDocument',
    agent: 'note.kindAgent',
    free: 'note.kindFree',
  }[kind] ?? kind
}

// ---------- 新建 / 编辑 ----------
const editingId = ref('')
const editingVersion = ref(0)
const formKind = ref<NoteKind>('free')
const formTitle = ref('')
const formBody = ref('')
const formTags = ref('')
const saving = ref(false)

const isEditing = computed(() => editingId.value !== '')

function openCreate() {
  editingId.value = ''
  editingVersion.value = 0
  formKind.value = 'free'
  formTitle.value = ''
  formBody.value = ''
  formTags.value = ''
  error.value = ''
}

function openEdit(n: Note) {
  editingId.value = n.id
  editingVersion.value = n.version
  formKind.value = n.kind
  formTitle.value = n.title
  formBody.value = n.body_md
  formTags.value = n.tags.join(', ')
  error.value = ''
}

async function saveNote() {
  if (!formTitle.value.trim()) return
  saving.value = true
  error.value = ''
  const tags = formTags.value
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
  try {
    if (isEditing.value) {
      const updated = await store.update(editingId.value, editingVersion.value, {
        kind: formKind.value,
        title: formTitle.value.trim(),
        bodyMd: formBody.value,
        tags,
      })
      await load()
      info.value = i18n.t('note.saved', { title: updated.title })
    } else {
      const created = await store.create({
        kind: formKind.value,
        title: formTitle.value.trim(),
        bodyMd: formBody.value,
        tags,
      })
      await load()
      info.value = i18n.t('note.created', { title: created.title })
    }
    openCreate()
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    saving.value = false
  }
}

// ---------- 删除 ----------
const confirmId = ref('')
const confirmTitle = ref('')

function askDelete(n: Note) {
  confirmId.value = n.id
  confirmTitle.value = n.title
}

async function doDelete() {
  if (!confirmId.value) return
  error.value = ''
  try {
    await store.remove(confirmId.value, confirmIdVersion.value)
    confirmId.value = ''
    info.value = i18n.t('note.deleted')
  } catch (e) {
    error.value = localizedMessageOf(e)
    confirmId.value = ''
  }
}

// 删除确认时用最新已知版本（编辑状态通常已是当前版本）。
const confirmIdVersion = computed(() => {
  const n = store.notes.find((x) => x.id === confirmId.value)
  return n?.version ?? 1
})

function cancelDelete() {
  confirmId.value = ''
}

// ---------- 转闪卡 ----------
const fcNoteId = ref('')

async function toFlashcard(n: Note) {
  fcNoteId.value = n.id
  error.value = ''
  try {
    const card = await store.toFlashcard(n.id)
    info.value = i18n.t('note.flashcardCreated', { front: card.front })
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    fcNoteId.value = ''
  }
}

onMounted(load)
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('note.title') }}</h1>
        <div class="subtitle">{{ $t('note.subtitle') }}</div>
      </div>
      <div class="flex gap-3">
        <button class="btn" @click="load">{{ $t('common.refresh') }}</button>
        <button class="btn btn-primary" @click="openCreate">{{ $t('note.new') }}</button>
      </div>
    </div>

    <div v-if="error" class="error-banner">{{ error }}</div>
    <div v-if="info" class="offline-banner">{{ info }}</div>

    <!-- 筛选栏 -->
    <div class="card mb-3">
      <div class="flex gap-3" style="flex-wrap: wrap">
        <input
          v-model="keyword"
          class="input"
          style="flex: 1 1 200px"
          :placeholder="$t('note.searchPlaceholder')"
          @keyup.enter="doSearch"
        />
        <select v-model="kindFilter" class="input" style="max-width: 160px">
          <option value="">{{ $t('note.allKinds') }}</option>
          <option value="question">{{ $t('note.kindQuestion') }}</option>
          <option value="document">{{ $t('note.kindDocument') }}</option>
          <option value="agent">{{ $t('note.kindAgent') }}</option>
          <option value="free">{{ $t('note.kindFree') }}</option>
        </select>
        <input
          v-model="tagFilter"
          class="input"
          style="flex: 0 1 140px"
          :placeholder="$t('note.tagFilterPlaceholder')"
          @keyup.enter="doSearch"
        />
        <button class="btn" @click="doSearch">{{ $t('note.search') }}</button>
        <button v-if="searched || kindFilter || tagFilter" class="btn" @click="resetFilters">
          {{ $t('note.resetFilters') }}
        </button>
      </div>
    </div>

    <!-- 列表 -->
    <div v-if="loading" class="loading"><div class="spinner"></div></div>

    <div v-else-if="notes.length === 0" class="empty">
      <div class="empty-icon">📝</div>
      <p>{{ $t('note.empty') }}</p>
      <p class="hint">{{ $t('note.emptyHint') }}</p>
    </div>

    <div v-else class="flex-col gap-3">
      <div v-for="n in notes" :key="n.id" class="card note-card">
        <div class="flex-between">
          <div style="min-width: 0">
            <div class="flex gap-2" style="align-items: center">
              <span class="badge">{{ $t(kindKey(n.kind)) }}</span>
              <h3 class="note-title">{{ n.title }}</h3>
            </div>
            <div v-if="n.tags.length" class="flex gap-2 mt-2" style="flex-wrap: wrap">
              <span v-for="t in n.tags" :key="t" class="tag-chip">#{{ t }}</span>
            </div>
          </div>
          <div class="flex-col gap-2" style="align-items: flex-end">
            <div class="flex gap-2">
              <button class="btn" :disabled="fcNoteId === n.id" @click="toFlashcard(n)">
                {{ fcNoteId === n.id ? $t('common.submitting') : $t('note.toFlashcard') }}
              </button>
              <button class="btn" @click="openEdit(n)">{{ $t('note.edit') }}</button>
              <button class="btn btn-danger" @click="askDelete(n)">{{ $t('note.delete') }}</button>
            </div>
            <span class="hint">{{ $t('note.updatedAt') }} · {{ n.updated_at.slice(0, 10) }}</span>
          </div>
        </div>
        <p v-if="n.body_md" class="note-body">{{ n.body_md }}</p>
      </div>
    </div>

    <!-- 新建 / 编辑表单 -->
    <div class="card mt-4">
      <div class="card-title">{{ isEditing ? $t('note.editTitle') : $t('note.createTitle') }}</div>
      <div class="flex-col gap-2">
        <div class="flex gap-3" style="flex-wrap: wrap">
          <select v-model="formKind" class="input" style="max-width: 140px">
            <option value="question">{{ $t('note.kindQuestion') }}</option>
            <option value="document">{{ $t('note.kindDocument') }}</option>
            <option value="agent">{{ $t('note.kindAgent') }}</option>
            <option value="free">{{ $t('note.kindFree') }}</option>
          </select>
          <input v-model="formTitle" class="input" style="flex: 1" :placeholder="$t('note.titlePlaceholder')" />
          <input v-model="formTags" class="input" style="flex: 0 1 220px" :placeholder="$t('note.tagsPlaceholder')" />
        </div>
        <textarea
          v-model="formBody"
          class="textarea"
          :placeholder="$t('note.bodyPlaceholder')"
          style="min-height: 120px"
        ></textarea>
        <div class="flex gap-3" style="justify-content: flex-end">
          <button v-if="isEditing" class="btn" @click="openCreate">{{ $t('common.cancel') }}</button>
          <button class="btn btn-primary" :disabled="saving || !formTitle.trim()" @click="saveNote">
            {{ saving ? $t('common.submitting') : $t('note.save') }}
          </button>
        </div>
      </div>
    </div>

    <!-- 删除确认 -->
    <div v-if="confirmId" class="modal-mask">
      <div class="card modal">
        <h3>{{ $t('note.confirmDeleteTitle') }}</h3>
        <p class="text-secondary">{{ $t('note.confirmDeleteBody', { title: confirmTitle }) }}</p>
        <div class="flex gap-3" style="justify-content: flex-end">
          <button class="btn" @click="cancelDelete">{{ $t('common.cancel') }}</button>
          <button class="btn btn-danger" @click="doDelete">{{ $t('common.delete') }}</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.note-card {
  transition: box-shadow 0.15s ease;
}
.note-title {
  font-size: var(--font-md);
  margin: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.note-body {
  margin: var(--space-2) 0 0;
  color: var(--text-secondary);
  white-space: pre-wrap;
  display: -webkit-box;
  -webkit-line-clamp: 3;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.tag-chip {
  font-size: var(--text-xs);
  color: var(--color-primary);
  background: var(--color-primary-soft);
  border-radius: var(--radius-full);
  padding: 2px 10px;
}
</style>
