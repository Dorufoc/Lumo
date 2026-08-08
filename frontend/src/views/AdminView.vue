<script setup lang="ts">
// AdminView.vue 管理端视图：审核队列 / Provider 策略 / 功能开关 / 用户禁用 / 审计日志 / 组织管理。
import { computed, onMounted, ref } from 'vue'
import { call, localizedMessageOf } from '@/api/client'
import type { AuditEntry, Class, ProviderPolicy, ReviewQueueItem, UserProfile } from '@/api/types'
import {
  adminAuditList,
  adminFeatureFlagSet,
  adminProviderPolicySet,
  adminReviewDecide,
  adminReviewList,
  adminUserDisable,
} from '@/api/admin'
import { orgClassAssignTeacher, orgClassList, orgTeacherList, orgWorkspaceUpdate } from '@/api/org'
import { useI18nStore } from '@/stores/i18n'
import { useSessionStore } from '@/stores/session'

const i18n = useI18nStore()
const session = useSessionStore()

const tab = ref<'review' | 'policy' | 'flag' | 'users' | 'audit' | 'org'>('review')
const error = ref('')
const info = ref('')
const busy = ref(false)

// 组织管理标签仅对组织管理员/全局管理员可见。
const canManageOrg = computed(() => session.user?.role === 'org_admin' || session.user?.role === 'admin')

// ---- 审核队列 ----
const reviewItems = ref<ReviewQueueItem[]>([])
const reviewLoading = ref(false)
const statusFilter = ref('pending')
const statusText = (s: string): string => {
  const map: Record<string, string> = {
    pending: i18n.t('admin.statusPending'),
    approved: i18n.t('admin.statusApproved'),
    rejected: i18n.t('admin.statusRejected'),
    taken_down: i18n.t('admin.statusTakenDown'),
  }
  return map[s] ?? s
}
const refTypeText = (t: string): string => {
  const map: Record<string, string> = {
    question: 'admin.refTypeQuestion',
    answer: 'admin.refTypeAnswer',
    comment: 'admin.refTypeComment',
    document: 'admin.refTypeDocument',
  }
  return i18n.t(map[t] ?? t)
}

async function loadReview() {
  reviewLoading.value = true
  error.value = ''
  try {
    const page = await adminReviewList({ workspace_id: session.workspaceId, status: statusFilter.value })
    reviewItems.value = page?.items ?? []
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    reviewLoading.value = false
  }
}

async function decide(item: ReviewQueueItem, decision: 'approved' | 'rejected' | 'taken_down') {
  busy.value = true
  error.value = ''
  info.value = ''
  try {
    await adminReviewDecide({
      workspace_id: session.workspaceId,
      item_id: item.id,
      decision,
      reason: '',
    })
    info.value = i18n.t('admin.reviewDone')
    await loadReview()
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    busy.value = false
  }
}

// ---- Provider 策略 ----
const policy = ref<ProviderPolicy>({ provider: 'llm', model: 'gpt-4o-mini', allowed: true })

async function savePolicy() {
  if (!policy.value.provider.trim() || !policy.value.model.trim()) {
    error.value = i18n.t('admin.policyInvalid')
    return
  }
  busy.value = true
  error.value = ''
  info.value = ''
  try {
    const r = await adminProviderPolicySet({
      workspace_id: session.workspaceId,
      provider: policy.value.provider.trim(),
      model: policy.value.model.trim(),
      allowed: policy.value.allowed,
      daily_quota: policy.value.daily_quota ?? undefined,
      monthly_budget: policy.value.monthly_budget ?? undefined,
    })
    policy.value = r
    info.value = i18n.t('admin.policySaved')
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    busy.value = false
  }
}

// ---- 功能开关 ----
const flag = ref({ key: '', enabled: true, rollout_percent: 100 })

async function saveFlag() {
  if (!flag.value.key.trim()) {
    error.value = i18n.t('admin.flagInvalid')
    return
  }
  busy.value = true
  error.value = ''
  info.value = ''
  try {
    await adminFeatureFlagSet({
      workspace_id: session.workspaceId,
      key: flag.value.key.trim(),
      enabled: flag.value.enabled,
      rollout_percent: Number(flag.value.rollout_percent) || 0,
    })
    info.value = i18n.t('admin.flagSaved')
    flag.value.key = ''
    flag.value.enabled = true
    flag.value.rollout_percent = 100
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    busy.value = false
  }
}

// ---- 用户禁用 ----
const users = ref<UserProfile[]>([])
const targetUserId = ref('')
const disableReason = ref('')

async function loadUsers() {
  try {
    users.value =
      (await call<UserProfile[]>('UserList', { workspace_id: session.workspaceId }).catch(() => [])) ?? []
  } catch {
    users.value = []
  }
}

async function disableUser() {
  if (!targetUserId.value) {
    error.value = i18n.t('admin.userInvalid')
    return
  }
  busy.value = true
  error.value = ''
  info.value = ''
  try {
    const r = await adminUserDisable({
      workspace_id: session.workspaceId,
      user_id: targetUserId.value,
      reason: disableReason.value.trim(),
    })
    info.value = i18n.t('admin.userDisabled', { at: r.disabled_at ?? '–' })
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    busy.value = false
  }
}

// ---- 审计日志 ----
const auditItems = ref<AuditEntry[]>([])
const auditAction = ref('')
const auditActor = ref('')
const auditLoading = ref(false)

async function loadAudit() {
  auditLoading.value = true
  error.value = ''
  try {
    const page = await adminAuditList({
      workspace_id: session.workspaceId,
      action: auditAction.value.trim() || undefined,
      actor_id: auditActor.value.trim() || undefined,
    })
    auditItems.value = page?.items ?? []
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    auditLoading.value = false
  }
}

function fmtJSON(j: string): string {
  if (!j || j === 'null' || j === '{}') return '–'
  return j.length > 120 ? `${j.slice(0, 120)}…` : j
}

// ---- 组织管理（组织-班级-教师层级） ----
const orgName = ref('')
const orgAdminUserId = ref('')
const orgClasses = ref<Class[]>([])
const orgTeachers = ref<UserProfile[]>([])
const orgLoading = ref(false)

const assignMap = ref<Record<string, string>>({})

async function loadOrg() {
  if (!canManageOrg.value) return
  orgLoading.value = true
  error.value = ''
  try {
    const ws = await call<{ org_name: string | null; org_admin_user_id: string | null }>('WorkspaceGet', {
      workspace_id: session.workspaceId,
    })
    orgName.value = ws?.org_name ?? ''
    orgAdminUserId.value = ws?.org_admin_user_id ?? ''
    orgClasses.value = (await orgClassList({ workspace_id: session.workspaceId, user_id: session.user!.id })) ?? []
    orgTeachers.value = (await orgTeacherList({ workspace_id: session.workspaceId, user_id: session.user!.id })) ?? []
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    orgLoading.value = false
  }
}

async function saveOrg() {
  if (orgName.value.length > 120) {
    error.value = i18n.t('admin.orgNameInvalid')
    return
  }
  busy.value = true
  error.value = ''
  info.value = ''
  try {
    await orgWorkspaceUpdate({
      workspace_id: session.workspaceId,
      user_id: session.user!.id,
      org_name: orgName.value,
      org_admin_user_id: orgAdminUserId.value,
    })
    info.value = i18n.t('admin.orgSaved')
    await loadOrg()
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    busy.value = false
  }
}

async function assignTeacher(cls: Class) {
  const tid = assignMap.value[cls.id]
  if (!tid) return
  busy.value = true
  error.value = ''
  info.value = ''
  try {
    await orgClassAssignTeacher({
      workspace_id: session.workspaceId,
      user_id: session.user!.id,
      class_id: cls.id,
      teacher_user_id: tid,
    })
    info.value = i18n.t('admin.orgAssigned')
    await loadOrg()
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    busy.value = false
  }
}

function ownerName(userId: string): string {
  const t = orgTeachers.value.find((u) => u.id === userId)
  return t ? t.display_name : userId
}

onMounted(() => {
  void loadReview()
  void loadUsers()
  void loadOrg()
})
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('admin.title') }}</h1>
        <div class="subtitle">{{ $t('admin.subtitle') }}</div>
      </div>
    </div>

    <div v-if="error" class="error-banner">
      <span>{{ error }}</span>
      <button class="btn btn-sm" @click="error = ''">{{ $t('common.close') }}</button>
    </div>
    <div v-if="info" class="offline-banner">{{ info }}</div>

    <div class="tabs">
      <div class="tab" :class="{ active: tab === 'review' }" @click="tab = 'review'">{{ $t('admin.tabReview') }}</div>
      <div class="tab" :class="{ active: tab === 'policy' }" @click="tab = 'policy'">{{ $t('admin.tabPolicy') }}</div>
      <div class="tab" :class="{ active: tab === 'flag' }" @click="tab = 'flag'">{{ $t('admin.tabFlag') }}</div>
      <div class="tab" :class="{ active: tab === 'users' }" @click="tab = 'users'">{{ $t('admin.tabUsers') }}</div>
      <div class="tab" :class="{ active: tab === 'audit' }" @click="tab = 'audit'">{{ $t('admin.tabAudit') }}</div>
      <div v-if="canManageOrg" class="tab" :class="{ active: tab === 'org' }" @click="tab = 'org'">{{ $t('admin.tabOrg') }}</div>
    </div>

    <!-- 审核队列 -->
    <template v-if="tab === 'review'">
      <div class="card">
        <div class="flex-between mb-3">
          <div class="card-title" style="margin: 0">{{ $t('admin.tabReview') }}</div>
          <select v-model="statusFilter" class="select" style="max-width: 180px" @change="loadReview">
            <option value="pending">{{ $t('admin.statusPending') }}</option>
            <option value="approved">{{ $t('admin.statusApproved') }}</option>
            <option value="rejected">{{ $t('admin.statusRejected') }}</option>
            <option value="taken_down">{{ $t('admin.statusTakenDown') }}</option>
            <option value="">{{ $t('common.all') }}</option>
          </select>
        </div>
        <div v-if="reviewLoading" class="text-secondary">{{ $t('common.loading') }}</div>
        <div v-else-if="reviewItems.length === 0" class="hint">{{ $t('admin.reviewEmpty') }}</div>
        <table v-else class="table">
          <thead>
            <tr>
              <th>{{ $t('admin.colRefType') }}</th>
              <th>{{ $t('admin.colRefId') }}</th>
              <th>{{ $t('admin.colStatus') }}</th>
              <th>{{ $t('admin.colReason') }}</th>
              <th>{{ $t('admin.colReviewedAt') }}</th>
              <th>{{ $t('admin.colActions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="item in reviewItems" :key="item.id">
              <td>{{ refTypeText(item.ref_type) }}</td>
              <td class="mono">{{ item.ref_id }}</td>
              <td>
                <span class="badge" :class="item.status === 'pending' ? 'badge-warning' : 'badge-success'">
                  {{ statusText(item.status) }}
                </span>
              </td>
              <td>{{ item.reason || '–' }}</td>
              <td>{{ item.reviewed_at || '–' }}</td>
              <td>
                <div v-if="item.status === 'pending'" class="flex gap-1">
                  <button class="btn btn-sm btn-primary" :disabled="busy" @click="decide(item, 'approved')">
                    {{ $t('admin.actionApprove') }}
                  </button>
                  <button class="btn btn-sm" :disabled="busy" @click="decide(item, 'rejected')">
                    {{ $t('admin.actionReject') }}
                  </button>
                  <button class="btn btn-sm btn-danger" :disabled="busy" @click="decide(item, 'taken_down')">
                    {{ $t('admin.actionTakeDown') }}
                  </button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <!-- Provider 策略 -->
    <template v-if="tab === 'policy'">
      <div class="card">
        <div class="card-title">{{ $t('admin.tabPolicy') }}</div>
        <p class="text-secondary mb-3">{{ $t('admin.policyHint') }}</p>
        <div class="form-row">
          <div class="field">
            <label>{{ $t('admin.policyProvider') }}</label>
            <input v-model="policy.provider" class="input" placeholder="llm" />
          </div>
          <div class="field">
            <label>{{ $t('admin.policyModel') }}</label>
            <input v-model="policy.model" class="input" placeholder="gpt-4o-mini" />
          </div>
        </div>
        <div class="form-row">
          <div class="field">
            <label>{{ $t('admin.policyDailyQuota') }}</label>
            <input v-model.number="policy.daily_quota" type="number" min="0" class="input" :placeholder="$t('admin.policyPlaceholderQuota')" />
          </div>
          <div class="field">
            <label>{{ $t('admin.policyMonthlyBudget') }}</label>
            <input v-model.number="policy.monthly_budget" type="number" min="0" class="input" :placeholder="$t('admin.policyPlaceholderQuota')" />
          </div>
          <div class="field" style="justify-content: center">
            <label class="checkbox-row">
              <input v-model="policy.allowed" type="checkbox" />
              <span>{{ $t('admin.policyAllowed') }}</span>
            </label>
          </div>
        </div>
        <div class="flex" style="justify-content: flex-end">
          <button class="btn btn-primary" :disabled="busy" @click="savePolicy">{{ $t('common.save') }}</button>
        </div>
      </div>
    </template>

    <!-- 功能开关 -->
    <template v-if="tab === 'flag'">
      <div class="card">
        <div class="card-title">{{ $t('admin.tabFlag') }}</div>
        <p class="text-secondary mb-3">{{ $t('admin.flagHint') }}</p>
        <div class="form-row">
          <div class="field">
            <label>{{ $t('admin.flagKey') }}</label>
            <input v-model="flag.key" class="input" placeholder="e.g. ai_tutor" />
          </div>
          <div class="field">
            <label>{{ $t('admin.flagRollout') }}</label>
            <input v-model.number="flag.rollout_percent" type="number" min="0" max="100" class="input" />
          </div>
          <div class="field" style="justify-content: center">
            <label class="checkbox-row">
              <input v-model="flag.enabled" type="checkbox" />
              <span>{{ $t('admin.flagEnabled') }}</span>
            </label>
          </div>
        </div>
        <div class="flex" style="justify-content: flex-end">
          <button class="btn btn-primary" :disabled="busy" @click="saveFlag">{{ $t('common.save') }}</button>
        </div>
      </div>
    </template>

    <!-- 用户禁用 -->
    <template v-if="tab === 'users'">
      <div class="card">
        <div class="card-title">{{ $t('admin.tabUsers') }}</div>
        <p class="text-secondary mb-3">{{ $t('admin.userHint') }}</p>
        <div class="form-row">
          <div class="field">
            <label>{{ $t('admin.userLabel') }}</label>
            <select v-model="targetUserId" class="select">
              <option value="" disabled>{{ $t('common.select') }}</option>
              <option v-for="u in users" :key="u.id" :value="u.id">
                {{ u.display_name }}（{{ u.role }}）
              </option>
            </select>
          </div>
          <div class="field">
            <label>{{ $t('admin.userReason') }}</label>
            <input v-model="disableReason" class="input" :placeholder="$t('admin.userReasonPlaceholder')" />
          </div>
        </div>
        <button class="btn btn-danger" :disabled="busy" @click="disableUser">{{ $t('admin.userDisable') }}</button>
      </div>
    </template>

    <!-- 审计日志 -->
    <template v-if="tab === 'audit'">
      <div class="card">
        <div class="card-title">{{ $t('admin.tabAudit') }}</div>
        <p class="text-secondary mb-3">{{ $t('admin.auditHint') }}</p>
        <div class="form-row">
          <div class="field">
            <label>{{ $t('admin.colAction') }}</label>
            <input v-model="auditAction" class="input" :placeholder="$t('admin.auditActionPlaceholder')" />
          </div>
          <div class="field">
            <label>{{ $t('admin.colActor') }}</label>
            <input v-model="auditActor" class="input" :placeholder="$t('admin.auditActorPlaceholder')" />
          </div>
          <div class="field" style="justify-content: flex-end">
            <button class="btn" :disabled="auditLoading" @click="loadAudit">{{ $t('admin.auditFilter') }}</button>
          </div>
        </div>
        <div v-if="auditLoading" class="text-secondary">{{ $t('common.loading') }}</div>
        <div v-else-if="auditItems.length === 0" class="hint">{{ $t('admin.auditEmpty') }}</div>
        <table v-else class="table">
          <thead>
            <tr>
              <th>{{ $t('admin.colCreatedAt') }}</th>
              <th>{{ $t('admin.colAction') }}</th>
              <th>{{ $t('admin.colEntity') }}</th>
              <th>{{ $t('admin.colActor') }}</th>
              <th>{{ $t('admin.auditBefore') }}</th>
              <th>{{ $t('admin.auditAfter') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="e in auditItems" :key="e.id">
              <td class="mono">{{ e.created_at }}</td>
              <td class="mono">{{ e.action }}</td>
              <td class="mono">{{ e.entity_type }}:{{ e.entity_id ?? '–' }}</td>
              <td class="mono">{{ e.actor_role ?? '' }}{{ e.actor_id ? ':' + e.actor_id : '' }}</td>
              <td class="mono">{{ fmtJSON(e.before_json) }}</td>
              <td class="mono">{{ fmtJSON(e.after_json) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <!-- 组织管理（组织-班级-教师层级） -->
    <template v-if="tab === 'org'">
      <div v-if="orgLoading" class="text-secondary">{{ $t('common.loading') }}</div>
      <template v-else>
        <div class="card">
          <div class="card-title">{{ $t('admin.tabOrg') }}</div>
          <p class="text-secondary mb-3">{{ $t('admin.orgName') }} / {{ $t('admin.orgAdmin') }}</p>
          <div class="form-row">
            <div class="field">
              <label>{{ $t('admin.orgName') }}</label>
              <input v-model="orgName" class="input" maxlength="120" :placeholder="$t('admin.orgNamePlaceholder')" />
            </div>
            <div class="field">
              <label>{{ $t('admin.orgAdmin') }}</label>
              <input v-model="orgAdminUserId" class="input" :placeholder="$t('admin.orgAdminPlaceholder')" />
            </div>
          </div>
          <div class="flex" style="justify-content: flex-end">
            <button class="btn btn-primary" :disabled="busy" @click="saveOrg">{{ $t('common.save') }}</button>
          </div>
        </div>

        <div class="card">
          <div class="card-title">{{ $t('admin.orgClasses') }}</div>
          <div v-if="orgClasses.length === 0" class="hint">{{ $t('admin.orgClassEmpty') }}</div>
          <table v-else class="table">
            <thead>
              <tr>
                <th>{{ $t('admin.orgColName') }}</th>
                <th>{{ $t('admin.orgColOwner') }}</th>
                <th>{{ $t('admin.orgColSubject') }}</th>
                <th>{{ $t('admin.orgColMembers') }}</th>
                <th>{{ $t('admin.orgAssignTeacher') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="cls in orgClasses" :key="cls.id">
                <td>{{ cls.name }}</td>
                <td>{{ ownerName(cls.owner_user_id) }}</td>
                <td>{{ cls.subject || '–' }}</td>
                <td>{{ cls.member_count }}</td>
                <td>
                  <div class="flex gap-1">
                    <select v-model="assignMap[cls.id]" class="select" style="max-width: 180px">
                      <option value="" disabled>{{ $t('admin.orgSelectTeacher') }}</option>
                      <option v-for="t in orgTeachers" :key="t.id" :value="t.id">
                        {{ t.display_name }}（{{ t.role }}）
                      </option>
                    </select>
                    <button class="btn btn-sm btn-primary" :disabled="busy || !assignMap[cls.id]" @click="assignTeacher(cls)">
                      {{ $t('admin.orgAssignTeacher') }}
                    </button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <div class="card">
          <div class="card-title">{{ $t('admin.orgTeachers') }}</div>
          <div v-if="orgTeachers.length === 0" class="hint">{{ $t('admin.orgTeacherEmpty') }}</div>
          <ul v-else class="plain-list">
            <li v-for="t in orgTeachers" :key="t.id">
              {{ t.display_name }}（{{ t.role }}）<span class="text-secondary">· {{ t.id }}</span>
            </li>
          </ul>
        </div>
      </template>
    </template>
  </div>
</template>
