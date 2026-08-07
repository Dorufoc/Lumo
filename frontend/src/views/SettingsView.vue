<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { call, localizedMessageOf } from '@/api/client'
import { familyInviteCreate, familyInviteGet, familyUnbind } from '@/api/family'
import type { FamilyOverview, Settings, SyncPushResult } from '@/api/types'
import { useI18nStore } from '@/stores/i18n'
import { useSessionStore } from '@/stores/session'

const session = useSessionStore()
const i18n = useI18nStore()

const isStudent = computed(() => session.user?.role === 'student')
const tab = ref<'model' | 'data' | 'family'>('model')
const error = ref('')
const info = ref('')

// ---------- 模型配置 ----------
const llm = ref({ kind: 'openai', base_url: '', api_key: '', model: 'gpt-4o-mini', enabled: false })
const embedding = ref({ kind: 'openai', base_url: '', api_key: '', model: 'text-embedding-3-small', enabled: false })
const tts = ref({ kind: 'openai', base_url: '', api_key: '', model: 'tts-1', enabled: false })
const asr = ref({ kind: 'openai', base_url: '', api_key: '', model: 'whisper-1', enabled: false })
type ProviderName = 'llm' | 'embedding' | 'tts' | 'asr'
const testing = ref<string | null>(null)
const testResult = ref<Record<string, string>>({})

function applyProviderState(target: { enabled: boolean; model: string }, cfg?: { configured?: boolean; model?: string }) {
  if (cfg?.configured) {
    target.enabled = true
    if (cfg.model) target.model = cfg.model
  }
}

async function loadSettings() {
  try {
    const st = await call<Settings>('SettingsGet', { workspace_id: session.workspaceId })
    session.settings = st
    const ps = st.provider_status
    applyProviderState(llm.value, ps.llm)
    applyProviderState(embedding.value, ps.embedding)
    applyProviderState(tts.value, ps.tts)
    applyProviderState(asr.value, ps.asr)
  } catch (e) {
    error.value = localizedMessageOf(e)
  }
}

async function saveProvider(provider: ProviderName, cfg: typeof llm.value) {
  error.value = ''
  info.value = ''
  try {
    const st = await call<Settings>('ProviderConfigure', {
      workspace_id: session.workspaceId,
      provider,
      kind: cfg.kind,
      base_url: cfg.base_url,
      api_key: cfg.api_key || undefined,
      model: cfg.model,
      enabled: cfg.enabled,
    })
    session.settings = st
    cfg.api_key = ''
    const key: Record<ProviderName, string> = {
      llm: 'settings.llmSaved',
      embedding: 'settings.embeddingSaved',
      tts: 'settings.ttsSaved',
      asr: 'settings.asrSaved',
    }
    info.value = i18n.t(key[provider])
  } catch (e) {
    error.value = localizedMessageOf(e)
  }
}

async function testProvider(provider: ProviderName) {
  testing.value = provider
  error.value = ''
  try {
    const r = await call<{ ok: boolean; latency_ms: number; error?: string }>('ProviderTest', {
      workspace_id: session.workspaceId,
      provider,
    })
    testResult.value[provider] = r.ok
      ? i18n.t('settings.testOk', { ms: r.latency_ms })
      : `❌ ${r.error ?? i18n.t('settings.testFailed')}`
  } catch (e) {
    testResult.value[provider] = `❌ ${localizedMessageOf(e)}`
  } finally {
    testing.value = null
  }
}

// ---------- 数据管理 ----------
const backupPassword = ref('')
const backingUp = ref(false)
const backupResult = ref('')

// ---------- 同步 ----------
const syncStatus = ref<{ pending_count: number; state: string; last_error: string; device_id: string } | null>(null)
const syncing = ref(false)

async function loadSyncStatus() {
  try {
    syncStatus.value = await call('SyncStatusGet', { workspace_id: session.workspaceId })
  } catch {
    syncStatus.value = null
  }
}

async function doSync() {
  syncing.value = true
  error.value = ''
  try {
    await call('SyncDeviceRegister', {
      device_id: 'local-web',
      device_name: i18n.t('settings.deviceName'),
      platform: 'web',
      app_version: '2.0.0',
    })
    if (cloudMode.value === 'cloud' && cloudServer.value.configured) {
      // 云同步模式：推送到 cloud-server（未配置 token 时后端返回 FEATURE_DISABLED，此处已拦截）。
      const res = await call<SyncPushResult>('SyncCloudPush', {
        workspace_id: session.workspaceId,
        user_id: session.userId,
      })
      const conflicts = (res?.items ?? []).filter((i) => i.result === 'conflict').length
      info.value = conflicts > 0 ? i18n.t('settings.syncConflict', { count: conflicts }) : i18n.t('settings.syncDone')
    } else {
      // 本地默认：in-process SyncService
      await call('SyncPush', { workspace_id: session.workspaceId })
      info.value = syncStatus.value?.pending_count === 0 ? i18n.t('settings.syncDone') : i18n.t('settings.syncDoneShort')
    }
    await loadSyncStatus()
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    syncing.value = false
  }
}

// ---------- 云同步（Todo 34：可切换 cloud-server）----------
const cloudServer = ref<{ configured: boolean; mode: 'inprocess' | 'cloud' }>({ configured: false, mode: 'inprocess' })
const cloudMode = ref<'inprocess' | 'cloud'>('inprocess')
const cloudSaving = ref(false)

function loadCloudStatus() {
  cloudServer.value = session.settings?.cloud_server ?? { configured: false, mode: 'inprocess' }
  const settings = (session.settings?.settings as Record<string, unknown> | undefined) ?? {}
  // 有效模式：仅在配置了 token 时允许 cloud；否则强制回退 inprocess（与后端计算一致）。
  cloudMode.value = settings.sync_mode === 'cloud' && cloudServer.value.configured ? 'cloud' : 'inprocess'
}

async function toggleCloudMode() {
  if (cloudSaving.value) return
  const next: 'inprocess' | 'cloud' = cloudMode.value === 'cloud' ? 'inprocess' : 'cloud'
  if (next === 'cloud' && !cloudServer.value.configured) return // 未配置不允许开启
  cloudSaving.value = true
  error.value = ''
  try {
    const settings = (session.settings?.settings as Record<string, unknown> | undefined) ?? {}
    await call('SettingsUpdate', {
      workspace_id: session.workspaceId,
      version: session.settings?.version ?? 0,
      settings: { ...settings, sync_mode: next },
    })
    await session.refreshSettings()
    loadCloudStatus()
    info.value = next === 'cloud' ? i18n.t('settings.cloudEnabled') : i18n.t('settings.cloudDisabled')
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    cloudSaving.value = false
  }
}

const restorePath = ref('')
const restorePassword = ref('')
const restoring = ref(false)

const exportScope = ref<'all' | 'questions' | 'learning_records' | 'documents'>('all')
const exportFormat = ref<'json' | 'zip'>('json')
const exporting = ref(false)

async function createBackup() {
  if (backupPassword.value.length < 4) {
    error.value = i18n.t('settings.backupPasswordShort')
    return
  }
  backingUp.value = true
  error.value = ''
  try {
    const r = await call<{ file_name: string; size_bytes: number }>('BackupCreate', {
      workspace_id: session.workspaceId,
      password: backupPassword.value,
      idempotency_key: `bak-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`,
    })
    backupResult.value = i18n.t('settings.backupCreated', { name: r.file_name, size: (r.size_bytes / 1024).toFixed(1) })
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    backingUp.value = false
  }
}

async function restoreBackup() {
  if (!restorePath.value || restorePassword.value.length < 4) {
    error.value = i18n.t('settings.restoreRequired')
    return
  }
  restoring.value = true
  error.value = ''
  info.value = ''
  try {
    const r = await call<{ restored: boolean }>('BackupRestore', {
      backup_path: restorePath.value,
      password: restorePassword.value,
      target_workspace_id: session.workspaceId,
    })
    info.value = r.restored ? i18n.t('settings.restoreOk') : i18n.t('settings.restoreFailed')
    await loadSettings()
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    restoring.value = false
  }
}

async function doExport() {
  exporting.value = true
  error.value = ''
  try {
    const r = await call<{ path: string; file_name: string; format: string }>('DataExport', {
      workspace_id: session.workspaceId,
      scope: exportScope.value,
      format: exportFormat.value,
    })
    info.value = i18n.t('settings.exportDone', { name: r.file_name })
    window.location.href = `/api/v1/files?path=${encodeURIComponent(r.path)}`
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    exporting.value = false
  }
}

// ---------- 家庭（学生端：邀请码 + 绑定列表） ----------
const family = ref<FamilyOverview>({ invite: null, active_parents: 0, bindings: [] })
const familyLoading = ref(false)
const inviteBusy = ref(false)

async function loadFamily() {
  familyLoading.value = true
  try {
    family.value = (await familyInviteGet({ workspace_id: session.workspaceId, user_id: session.userId })) ?? {
      invite: null,
      active_parents: 0,
      bindings: [],
    }
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    familyLoading.value = false
  }
}

async function doGenerateInvite() {
  error.value = ''
  info.value = ''
  inviteBusy.value = true
  try {
    await familyInviteCreate({
      workspace_id: session.workspaceId,
      user_id: session.userId,
      idempotency_key: `fam-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`,
    })
    await loadFamily()
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    inviteBusy.value = false
  }
}

function copyInvite() {
  if (!family.value.invite?.code) return
  void navigator.clipboard?.writeText(family.value.invite.code)
  info.value = i18n.t('family.inviteCopied')
}

async function doUnbind(bindingId: string) {
  error.value = ''
  info.value = ''
  if (!window.confirm(i18n.t('family.unbindConfirm'))) return
  inviteBusy.value = true
  try {
    await familyUnbind({
      workspace_id: session.workspaceId,
      user_id: session.userId,
      binding_id: bindingId,
      version: 1,
    })
    info.value = i18n.t('family.unbound')
    await loadFamily()
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    inviteBusy.value = false
  }
}

function formatExpires(expiresAt: string): string {
  try {
    return new Date(expiresAt).toLocaleString()
  } catch {
    return expiresAt
  }
}

onMounted(() => {
  void loadSettings()
  void loadSyncStatus()
  loadCloudStatus()
  if (isStudent.value) void loadFamily()
})
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('settings.title') }}</h1>
        <div class="subtitle">{{ $t('settings.subtitle') }}</div>
      </div>
    </div>

    <div v-if="error" class="error-banner">
      <span>{{ error }}</span>
      <button class="btn btn-sm" @click="error = ''">{{ $t('common.close') }}</button>
    </div>
    <div v-if="info" class="offline-banner">{{ info }}</div>

    <div class="tabs">
      <div class="tab" :class="{ active: tab === 'model' }" @click="tab = 'model'">{{ $t('settings.tabModel') }}</div>
      <div class="tab" :class="{ active: tab === 'data' }" @click="tab = 'data'">{{ $t('settings.tabData') }}</div>
      <div v-if="isStudent" class="tab" :class="{ active: tab === 'family' }" @click="tab = 'family'">
        {{ $t('family.tabStudent') }}
      </div>
      <RouterLink class="tab" to="/admin">{{ $t('admin.title') }}</RouterLink>
    </div>

    <!-- 模型配置 -->
    <template v-if="tab === 'model'">
      <div class="card">
        <div class="flex-between mb-3">
          <div class="card-title" style="margin: 0">{{ $t('settings.llmTitle') }}</div>
          <span class="badge" :class="llm.enabled ? 'badge-success' : 'badge-offline'">
            {{ llm.enabled ? $t('settings.configured') : $t('settings.notConfigured') }}
          </span>
        </div>
        <div class="form-row">
          <div class="field">
            <label>{{ $t('settings.type') }}</label>
            <select v-model="llm.kind" class="select">
              <option value="openai">{{ $t('settings.openaiCompat') }}</option>
              <option value="local">{{ $t('settings.localModel') }}</option>
              <option value="mock">{{ $t('settings.localMock') }}</option>
            </select>
          </div>
          <div class="field">
            <label>Base URL</label>
            <input
              v-model="llm.base_url"
              class="input"
              :placeholder="llm.kind === 'local' ? $t('settings.localUrlPlaceholder') : $t('settings.baseUrlPlaceholder')"
            />
          </div>
        </div>
        <div class="form-row">
          <div class="field">
            <label>{{ $t('settings.modelName') }}</label>
            <input v-model="llm.model" class="input" placeholder="gpt-4o-mini" />
          </div>
          <div v-if="llm.kind !== 'local'" class="field">
            <label>{{ $t('settings.apiKeyLabel') }}</label>
            <input v-model="llm.api_key" type="password" class="input" placeholder="sk-…" />
          </div>
        </div>
        <div v-if="llm.kind === 'local'" class="hint mb-2">{{ $t('settings.localNoKeyHint') }}</div>
        <div class="flex gap-2" style="justify-content: flex-end">
          <button class="btn" :disabled="testing === 'llm'" @click="testProvider('llm')">
            {{ testing === 'llm' ? $t('settings.testing') : $t('settings.test') }}
          </button>
          <button class="btn btn-primary" @click="saveProvider('llm', llm)">{{ $t('common.save') }}</button>
        </div>
        <div v-if="testResult.llm" class="hint mt-2">{{ testResult.llm }}</div>
      </div>

      <div class="card">
        <div class="flex-between mb-3">
          <div class="card-title" style="margin: 0">{{ $t('settings.embeddingTitle') }}</div>
          <span class="badge" :class="embedding.enabled ? 'badge-success' : 'badge-offline'">
            {{ embedding.enabled ? $t('settings.configured') : $t('settings.notConfigured') }}
          </span>
        </div>
        <div class="form-row">
          <div class="field">
            <label>{{ $t('settings.type') }}</label>
            <select v-model="embedding.kind" class="select">
              <option value="openai">{{ $t('settings.openaiCompat') }}</option>
              <option value="local">{{ $t('settings.localModel') }}</option>
              <option value="mock">{{ $t('settings.localMock') }}</option>
            </select>
          </div>
          <div class="field">
            <label>Base URL</label>
            <input
              v-model="embedding.base_url"
              class="input"
              :placeholder="embedding.kind === 'local' ? $t('settings.localUrlPlaceholder') : $t('settings.baseUrlPlaceholder')"
            />
          </div>
        </div>
        <div class="form-row">
          <div class="field">
            <label>{{ $t('settings.modelName') }}</label>
            <input v-model="embedding.model" class="input" placeholder="text-embedding-3-small" />
          </div>
          <div v-if="embedding.kind !== 'local'" class="field">
            <label>{{ $t('settings.apiKeyLabel') }}</label>
            <input v-model="embedding.api_key" type="password" class="input" placeholder="sk-…" />
          </div>
        </div>
        <div v-if="embedding.kind === 'local'" class="hint mb-2">{{ $t('settings.localNoKeyHint') }}</div>
        <div class="flex gap-2" style="justify-content: flex-end">
          <button class="btn" :disabled="testing === 'embedding'" @click="testProvider('embedding')">
            {{ testing === 'embedding' ? $t('settings.testing') : $t('settings.test') }}
          </button>
          <button class="btn btn-primary" @click="saveProvider('embedding', embedding)">{{ $t('common.save') }}</button>
        </div>
        <div v-if="testResult.embedding" class="hint mt-2">{{ testResult.embedding }}</div>
        <div class="hint mt-2">{{ $t('settings.secretsHint') }}</div>
      </div>

      <div class="card">
        <div class="flex-between mb-3">
          <div class="card-title" style="margin: 0">{{ $t('settings.ttsTitle') }}</div>
          <span class="badge" :class="tts.enabled ? 'badge-success' : 'badge-offline'">
            {{ tts.enabled ? $t('settings.configured') : $t('settings.notConfigured') }}
          </span>
        </div>
        <div class="form-row">
          <div class="field">
            <label>{{ $t('settings.type') }}</label>
            <select v-model="tts.kind" class="select">
              <option value="openai">{{ $t('settings.openaiCompat') }}</option>
              <option value="mock">{{ $t('settings.localMock') }}</option>
            </select>
          </div>
          <div class="field">
            <label>Base URL</label>
            <input v-model="tts.base_url" class="input" :placeholder="$t('settings.baseUrlPlaceholder')" />
          </div>
        </div>
        <div class="form-row">
          <div class="field">
            <label>{{ $t('settings.modelName') }}</label>
            <input v-model="tts.model" class="input" placeholder="tts-1" />
          </div>
          <div class="field">
            <label>{{ $t('settings.apiKeyLabel') }}</label>
            <input v-model="tts.api_key" type="password" class="input" placeholder="sk-…" />
          </div>
        </div>
        <div v-if="tts.kind === 'mock'" class="hint mb-2">{{ $t('settings.mockNoKeyHint') }}</div>
        <div class="flex gap-2" style="justify-content: flex-end">
          <button class="btn" :disabled="testing === 'tts'" @click="testProvider('tts')">
            {{ testing === 'tts' ? $t('settings.testing') : $t('settings.test') }}
          </button>
          <button class="btn btn-primary" @click="saveProvider('tts', tts)">{{ $t('common.save') }}</button>
        </div>
        <div v-if="testResult.tts" class="hint mt-2">{{ testResult.tts }}</div>
      </div>

      <div class="card">
        <div class="flex-between mb-3">
          <div class="card-title" style="margin: 0">{{ $t('settings.asrTitle') }}</div>
          <span class="badge" :class="asr.enabled ? 'badge-success' : 'badge-offline'">
            {{ asr.enabled ? $t('settings.configured') : $t('settings.notConfigured') }}
          </span>
        </div>
        <div class="form-row">
          <div class="field">
            <label>{{ $t('settings.type') }}</label>
            <select v-model="asr.kind" class="select">
              <option value="openai">{{ $t('settings.openaiCompat') }}</option>
              <option value="mock">{{ $t('settings.localMock') }}</option>
            </select>
          </div>
          <div class="field">
            <label>Base URL</label>
            <input v-model="asr.base_url" class="input" :placeholder="$t('settings.baseUrlPlaceholder')" />
          </div>
        </div>
        <div class="form-row">
          <div class="field">
            <label>{{ $t('settings.modelName') }}</label>
            <input v-model="asr.model" class="input" placeholder="whisper-1" />
          </div>
          <div class="field">
            <label>{{ $t('settings.apiKeyLabel') }}</label>
            <input v-model="asr.api_key" type="password" class="input" placeholder="sk-…" />
          </div>
        </div>
        <div v-if="asr.kind === 'mock'" class="hint mb-2">{{ $t('settings.mockNoKeyHint') }}</div>
        <div class="flex gap-2" style="justify-content: flex-end">
          <button class="btn" :disabled="testing === 'asr'" @click="testProvider('asr')">
            {{ testing === 'asr' ? $t('settings.testing') : $t('settings.test') }}
          </button>
          <button class="btn btn-primary" @click="saveProvider('asr', asr)">{{ $t('common.save') }}</button>
        </div>
        <div v-if="testResult.asr" class="hint mt-2">{{ testResult.asr }}</div>
      </div>
    </template>

    <!-- 数据管理 -->
    <template v-if="tab === 'data'">
      <div class="card">
        <div class="card-title">{{ $t('settings.syncTitle') }}</div>
        <p class="text-secondary mb-3">
          {{ $t('settings.syncHint') }}
        </p>
        <div class="flex gap-3" style="align-items: center">
          <span class="badge" :class="(syncStatus?.pending_count ?? 0) > 0 ? 'badge-warning' : 'badge-success'">
            {{ $t('settings.pending', { count: syncStatus?.pending_count ?? '–' }) }}
          </span>
          <button class="btn btn-primary" :disabled="syncing" @click="doSync">
            {{ syncing ? $t('settings.syncing') : $t('settings.syncNow') }}
          </button>
        </div>
      </div>

      <!-- 云同步服务端（Todo 34：本地默认 in-process，可切换 cloud-server） -->
      <div class="card">
        <div class="card-title">{{ $t('settings.cloudServerTitle') }}</div>
        <p class="text-secondary mb-3">{{ $t('settings.cloudServerHint') }}</p>
        <div class="flex gap-3" style="align-items: center; flex-wrap: wrap">
          <label style="display: inline-flex; align-items: center; gap: 8px">
            <input
              type="checkbox"
              :checked="cloudMode === 'cloud'"
              :disabled="cloudSaving || !cloudServer.configured"
              @change="toggleCloudMode"
            />
            <span>{{ $t('settings.cloudServerEnabled') }}</span>
          </label>
          <span class="badge" :class="cloudServer.configured ? 'badge-success' : 'badge-error'">
            {{ cloudServer.configured ? $t('settings.cloudServerConfigured') : $t('settings.cloudServerNotConfigured') }}
          </span>
          <span v-if="cloudMode === 'cloud'" class="badge badge-primary">{{ $t('settings.cloudServerActive') }}</span>
        </div>
        <p v-if="!cloudServer.configured" class="hint mt-2">{{ $t('settings.cloudServerMissingToken') }}</p>
      </div>

      <div class="card">
        <div class="card-title">{{ $t('settings.backupTitle') }}</div>
        <p class="text-secondary mb-3">{{ $t('settings.backupHint') }}</p>
        <div class="flex gap-3">
          <input v-model="backupPassword" type="password" class="input" style="max-width: 260px" :placeholder="$t('settings.backupPassword')" />
          <button class="btn btn-primary" :disabled="backingUp" @click="createBackup">
            {{ backingUp ? $t('settings.backingUp') : $t('settings.createBackup') }}
          </button>
        </div>
        <div v-if="backupResult" class="hint mt-2">{{ backupResult }}</div>
      </div>

      <div class="card">
        <div class="card-title">{{ $t('settings.restoreTitle') }}</div>
        <p class="text-secondary mb-3">{{ $t('settings.restoreHint') }}</p>
        <div class="form-row">
          <div class="field">
            <label>{{ $t('settings.backupFileLabel') }}</label>
            <input v-model="restorePath" class="input" placeholder="backup-….sqz" />
          </div>
          <div class="field">
            <label>{{ $t('settings.password') }}</label>
            <input v-model="restorePassword" type="password" class="input" :placeholder="$t('settings.backupPassword')" />
          </div>
        </div>
        <button class="btn btn-danger" :disabled="restoring" @click="restoreBackup">
          {{ restoring ? $t('settings.restoring') : $t('settings.restoreButton') }}
        </button>
      </div>

      <div class="card">
        <div class="card-title">{{ $t('settings.exportTitle') }}</div>
        <div class="form-row">
          <div class="field">
            <label>{{ $t('settings.scope') }}</label>
            <select v-model="exportScope" class="select">
              <option value="all">{{ $t('settings.scopeAll') }}</option>
              <option value="questions">{{ $t('settings.scopeQuestions') }}</option>
              <option value="learning_records">{{ $t('settings.scopeRecords') }}</option>
              <option value="documents">{{ $t('settings.scopeDocs') }}</option>
            </select>
          </div>
          <div class="field">
            <label>{{ $t('settings.format') }}</label>
            <select v-model="exportFormat" class="select">
              <option value="json">JSON</option>
              <option value="zip">ZIP</option>
            </select>
          </div>
        </div>
        <button class="btn btn-primary" :disabled="exporting" @click="doExport">
          {{ exporting ? $t('settings.exporting') : $t('settings.exportDownload') }}
        </button>
      </div>
    </template>

    <!-- 家庭（学生端） -->
    <template v-if="tab === 'family'">
      <div class="card">
        <div class="flex-between mb-3">
          <div class="card-title" style="margin: 0">{{ $t('family.inviteTitle') }}</div>
          <button class="btn btn-sm btn-primary" :disabled="inviteBusy" @click="doGenerateInvite">
            {{ family.invite ? $t('family.regenerating') : $t('family.generateInvite') }}
          </button>
        </div>
        <p class="text-secondary mb-3">{{ $t('family.inviteHint') }}</p>
        <div v-if="family.invite" class="invite-code">
          <span class="code">{{ family.invite.code }}</span>
          <button class="btn btn-sm" @click="copyInvite">{{ $t('family.copy') }}</button>
        </div>
        <div v-if="family.invite" class="hint">
          {{ $t('family.expiresAt', { time: formatExpires(family.invite.expires_at) }) }}
        </div>
        <div v-else class="hint">{{ $t('family.inviteEmpty') }}</div>
      </div>

      <div class="card">
        <div class="card-title">
          {{ $t('family.boundParents', { count: family.active_parents, max: 2 }) }}
        </div>
        <div v-if="family.bindings.length === 0" class="hint mt-2">{{ $t('family.noParents') }}</div>
        <div v-else class="parent-list">
          <div v-for="b in family.bindings" :key="b.id" class="parent-row">
            <span class="parent-name">{{ b.parent_display_name || b.parent_user_id }}</span>
            <button class="btn btn-sm btn-danger" :disabled="inviteBusy" @click="doUnbind(b.id)">
              {{ $t('family.unbind') }}
            </button>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
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

.parent-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
  margin-top: var(--space-2);
}

.parent-row {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-1) 0;
}

.parent-name {
  flex: 1;
  min-width: 0;
  font-weight: 500;
}
</style>
