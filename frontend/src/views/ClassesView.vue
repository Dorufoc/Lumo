<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { localizedMessageOf } from '@/api/client'
import type { Class, ClassMember } from '@/api/types'
import {
  classArchive,
  classCreate,
  classInvite,
  classList,
  classMemberAdd,
  classMemberList,
  classMemberRemove,
  classUpdate,
} from '@/api/class'
import { useI18nStore } from '@/stores/i18n'
import { useSessionStore } from '@/stores/session'

const i18n = useI18nStore()
const session = useSessionStore()

const error = ref('')
const info = ref('')
const loading = ref(false)
const busy = ref(false)

const classes = ref<Class[]>([])
const membersByClass = ref<Record<string, ClassMember[]>>({})
const expanded = ref<string>('')

const isTeacher = computed(() => session.user?.role === 'teacher')

function idemKey(prefix: string): string {
  return `${prefix}-${session.userId}-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`
}

// ---- 创建班级 ----
const createDialog = ref(false)
const createForm = ref({ name: '', subject: '', semester: '' })

// ---- 编辑班级 ----
const editDialog = ref(false)
const editTarget = ref<Class | null>(null)
const editForm = ref({ name: '', subject: '', semester: '' })

// ---- 邀请码 ----
const inviteTarget = ref<Class | null>(null)
const inviteCode = ref('')
const inviteBusy = ref(false)

// ---- 添加学生 ----
const addTarget = ref<Class | null>(null)
const addStudentId = ref('')
const addBusy = ref(false)

async function load() {
  loading.value = true
  error.value = ''
  info.value = ''
  try {
    classes.value = (await classList({ workspace_id: session.workspaceId, user_id: session.userId })) ?? []
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    loading.value = false
  }
}

async function loadMembers(cls: Class) {
  try {
    membersByClass.value[cls.id] =
      (await classMemberList({ workspace_id: session.workspaceId, user_id: session.userId, class_id: cls.id })) ?? []
  } catch (e) {
    error.value = localizedMessageOf(e)
  }
}

function toggleMembers(cls: Class) {
  if (expanded.value === cls.id) {
    expanded.value = ''
    return
  }
  expanded.value = cls.id
  void loadMembers(cls)
}

function openCreate() {
  createForm.value = { name: '', subject: '', semester: '' }
  createDialog.value = true
}

async function submitCreate() {
  error.value = ''
  info.value = ''
  if (!createForm.value.name.trim()) {
    error.value = `${i18n.t('classes.name')} ${i18n.t('classes.required')}`
    return
  }
  busy.value = true
  try {
    const c = await classCreate({
      workspace_id: session.workspaceId,
      user_id: session.userId,
      name: createForm.value.name.trim(),
      subject: createForm.value.subject.trim(),
      semester: createForm.value.semester.trim(),
      idempotency_key: idemKey('cc'),
    })
    classes.value.unshift(c)
    createDialog.value = false
    info.value = i18n.t('classes.created')
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    busy.value = false
  }
}

function openEdit(cls: Class) {
  editTarget.value = cls
  editForm.value = { name: cls.name, subject: cls.subject, semester: cls.semester }
  editDialog.value = true
}

async function submitEdit() {
  error.value = ''
  info.value = ''
  if (!editTarget.value) return
  if (!editForm.value.name.trim()) {
    error.value = `${i18n.t('classes.name')} ${i18n.t('classes.required')}`
    return
  }
  busy.value = true
  try {
    const c = await classUpdate({
      workspace_id: session.workspaceId,
      user_id: session.userId,
      class_id: editTarget.value.id,
      name: editForm.value.name.trim(),
      subject: editForm.value.subject.trim(),
      semester: editForm.value.semester.trim(),
    })
    const idx = classes.value.findIndex((x) => x.id === c.id)
    if (idx >= 0) classes.value[idx] = c
    editDialog.value = false
    info.value = i18n.t('classes.saved')
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    busy.value = false
  }
}

async function doArchive(cls: Class) {
  error.value = ''
  info.value = ''
  if (!window.confirm(i18n.t('classes.archiveConfirm'))) return
  busy.value = true
  try {
    const c = await classArchive({ workspace_id: session.workspaceId, user_id: session.userId, class_id: cls.id })
    const idx = classes.value.findIndex((x) => x.id === c.id)
    if (idx >= 0) classes.value[idx] = c
    info.value = i18n.t('classes.archivedDone')
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    busy.value = false
  }
}

async function openInvite(cls: Class) {
  inviteTarget.value = cls
  inviteCode.value = ''
  inviteBusy.value = true
  error.value = ''
  try {
    const inv = await classInvite({ workspace_id: session.workspaceId, user_id: session.userId, class_id: cls.id })
    inviteCode.value = inv.code
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    inviteBusy.value = false
  }
}

function openAddMember(cls: Class) {
  addTarget.value = cls
  addStudentId.value = ''
}

async function submitAddMember() {
  error.value = ''
  info.value = ''
  if (!addTarget.value) return
  if (!addStudentId.value.trim()) {
    error.value = `${i18n.t('classes.studentId')} ${i18n.t('classes.required')}`
    return
  }
  addBusy.value = true
  try {
    const c = await classMemberAdd({
      workspace_id: session.workspaceId,
      user_id: session.userId,
      class_id: addTarget.value.id,
      student_user_id: addStudentId.value.trim(),
      idempotency_key: idemKey('cma'),
    })
    const idx = classes.value.findIndex((x) => x.id === c.id)
    if (idx >= 0) classes.value[idx] = c
    addTarget.value = null
    info.value = i18n.t('classes.memberAdded')
    await loadMembers(c)
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    addBusy.value = false
  }
}

async function doRemoveMember(cls: Class, m: ClassMember) {
  error.value = ''
  info.value = ''
  busy.value = true
  try {
    const c = await classMemberRemove({
      workspace_id: session.workspaceId,
      user_id: session.userId,
      class_id: cls.id,
      student_user_id: m.student_user_id,
    })
    const idx = classes.value.findIndex((x) => x.id === c.id)
    if (idx >= 0) classes.value[idx] = c
    info.value = i18n.t('classes.memberRemoved')
    await loadMembers(c)
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    busy.value = false
  }
}

function copyCode() {
  if (!inviteCode.value) return
  void navigator.clipboard?.writeText(inviteCode.value)
  info.value = i18n.t('classes.inviteCopied')
}

onMounted(load)
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('classes.title') }}</h1>
        <div class="subtitle">{{ $t('classes.subtitle') }}</div>
      </div>
      <div class="flex gap-2">
        <button class="btn btn-sm" :disabled="loading" @click="load">{{ $t('common.refresh') }}</button>
        <button v-if="isTeacher" class="btn btn-sm btn-primary" :disabled="loading" @click="openCreate">
          {{ $t('classes.createClass') }}
        </button>
      </div>
    </div>

    <div v-if="error" class="error-banner">{{ error }}</div>
    <div v-if="info" class="offline-banner">{{ info }}</div>

    <div v-if="loading" class="loading"><div class="spinner"></div></div>

    <div v-else-if="classes.length === 0" class="card">
      <div class="empty">
        <div class="empty-icon">🏫</div>
        <p>{{ isTeacher ? $t('classes.emptyTeacher') : $t('classes.emptyStudent') }}</p>
        <p v-if="!isTeacher" class="hint">{{ $t('classes.joinHint') }}</p>
      </div>
    </div>

    <div v-else class="class-list">
      <div v-for="cls in classes" :key="cls.id" class="card class-card" :class="{ archived: cls.status === 'archived' }">
        <div class="class-head" @click="toggleMembers(cls)">
          <div class="class-info">
            <div class="class-title">
              {{ cls.name }}
              <span
                class="badge"
                :class="cls.status === 'active' ? 'badge-success' : 'badge-warning'"
              >{{ $t(cls.status === 'active' ? 'classes.statusActive' : 'classes.statusArchived') }}</span>
            </div>
            <div class="hint">
              <template v-if="cls.subject || cls.semester">{{ cls.subject }} · {{ cls.semester }}</template>
              <template v-else>{{ $t('classes.noMeta') }}</template>
              · {{ $t('classes.memberCount', { count: String(cls.member_count) }) }}
            </div>
          </div>
          <span class="hint">{{ expanded === cls.id ? '▾' : '▸' }}</span>
        </div>

        <!-- 邀请码（教师） -->
        <div v-if="isTeacher && inviteTarget?.id === cls.id" class="invite-panel">
          <div class="invite-code">
            <code class="code">{{ inviteBusy ? $t('classes.generating') : (inviteCode || '—') }}</code>
            <button v-if="inviteCode" class="btn btn-sm" :disabled="inviteBusy" @click="copyCode">{{ $t('classes.copy') }}</button>
            <button class="btn btn-sm" :disabled="inviteBusy" @click="openInvite(cls)">{{ $t('classes.regenerate') }}</button>
          </div>
          <div class="hint">{{ $t('classes.inviteHint') }}</div>
        </div>

        <!-- 成员区 -->
        <div v-if="expanded === cls.id" class="members">
          <div v-if="isTeacher" class="flex gap-2 mb-2">
            <button class="btn btn-sm" @click="openInvite(cls)">{{ $t('classes.invite') }}</button>
            <button v-if="cls.status === 'active'" class="btn btn-sm" @click="openAddMember(cls)">{{ $t('classes.addMember') }}</button>
          </div>

          <div v-if="addTarget?.id === cls.id" class="member-add-row">
            <input v-model="addStudentId" class="input" type="text" :placeholder="$t('classes.studentIdPlaceholder')" />
            <button class="btn btn-sm btn-primary" :disabled="addBusy" @click="submitAddMember">
              {{ addBusy ? $t('common.submitting') : $t('classes.confirmAdd') }}
            </button>
          </div>

          <div v-if="(membersByClass[cls.id] ?? []).length === 0" class="empty">
            <p>{{ $t('classes.membersEmpty') }}</p>
          </div>
          <div v-else class="member-list">
            <div v-for="m in membersByClass[cls.id]" :key="m.id" class="member-row">
              <span class="member-name">{{ m.display_name || m.student_user_id }}</span>
              <span class="hint">{{ m.student_user_id.slice(0, 8) }}…</span>
              <span class="badge" :class="m.status === 'active' ? 'badge-success' : 'badge-warning'">
                {{ $t(m.status === 'active' ? 'classes.memberActive' : 'classes.memberRemovedBadge') }}
              </span>
              <button v-if="isTeacher && m.status === 'active'" class="btn btn-sm btn-danger" :disabled="busy" @click="doRemoveMember(cls, m)">
                {{ $t('classes.remove') }}
              </button>
            </div>
          </div>
        </div>

        <!-- 教师操作 -->
        <div v-if="isTeacher" class="class-actions">
          <button v-if="cls.status === 'active'" class="btn btn-sm" :disabled="busy" @click="openEdit(cls)">{{ $t('classes.edit') }}</button>
          <button v-if="cls.status === 'active'" class="btn btn-sm btn-danger" :disabled="busy" @click="doArchive(cls)">{{ $t('classes.archive') }}</button>
        </div>
      </div>
    </div>

    <!-- 创建班级弹窗 -->
    <div v-if="createDialog" class="modal-mask">
      <div class="card modal">
        <h3>{{ $t('classes.createClass') }}</h3>
        <div class="field">
          <label>{{ $t('classes.name') }} *</label>
          <input v-model="createForm.name" class="input" type="text" :placeholder="$t('classes.namePlaceholder')" />
        </div>
        <div class="form-row">
          <div class="field">
            <label>{{ $t('classes.subject') }}</label>
            <input v-model="createForm.subject" class="input" type="text" :placeholder="$t('classes.subjectPlaceholder')" />
          </div>
          <div class="field">
            <label>{{ $t('classes.semester') }}</label>
            <input v-model="createForm.semester" class="input" type="text" :placeholder="$t('classes.semesterPlaceholder')" />
          </div>
        </div>
        <div class="flex gap-3 mt-3">
          <button class="btn" :disabled="busy" @click="createDialog = false">{{ $t('common.cancel') }}</button>
          <button class="btn btn-primary" :disabled="busy" @click="submitCreate">
            {{ busy ? $t('common.creating') : $t('classes.confirmCreate') }}
          </button>
        </div>
      </div>
    </div>

    <!-- 编辑班级弹窗 -->
    <div v-if="editDialog" class="modal-mask">
      <div class="card modal">
        <h3>{{ $t('classes.edit') }}</h3>
        <div class="field">
          <label>{{ $t('classes.name') }} *</label>
          <input v-model="editForm.name" class="input" type="text" />
        </div>
        <div class="form-row">
          <div class="field">
            <label>{{ $t('classes.subject') }}</label>
            <input v-model="editForm.subject" class="input" type="text" />
          </div>
          <div class="field">
            <label>{{ $t('classes.semester') }}</label>
            <input v-model="editForm.semester" class="input" type="text" />
          </div>
        </div>
        <div class="flex gap-3 mt-3">
          <button class="btn" :disabled="busy" @click="editDialog = false">{{ $t('common.cancel') }}</button>
          <button class="btn btn-primary" :disabled="busy" @click="submitEdit">
            {{ busy ? $t('common.submitting') : $t('classes.save') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.class-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}

.class-card {
  padding: var(--space-3);
}

.class-card.archived {
  opacity: 0.7;
}

.class-head {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  cursor: pointer;
}

.class-info {
  flex: 1;
  min-width: 0;
}

.class-title {
  font-weight: 600;
  display: flex;
  align-items: center;
  gap: var(--space-2);
}

.members {
  margin-top: var(--space-3);
  padding-top: var(--space-3);
  border-top: 1px solid var(--border);
}

.invite-panel {
  margin-top: var(--space-3);
  padding: var(--space-2);
  background: var(--bg-subtle);
  border-radius: var(--radius-sm);
}

.invite-code {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  margin-bottom: var(--space-1);
}

.code {
  font-size: var(--text-lg);
  font-weight: 700;
  letter-spacing: 0.1em;
}

.member-add-row {
  display: flex;
  gap: var(--space-2);
  margin-bottom: var(--space-2);
}

.member-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
}

.member-row {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-1) 0;
}

.member-name {
  flex: 1;
  min-width: 0;
  font-weight: 500;
}

.class-actions {
  display: flex;
  gap: var(--space-2);
  margin-top: var(--space-3);
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
</style>
