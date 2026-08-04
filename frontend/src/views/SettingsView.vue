<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { call } from '@/api/client'
import type { Settings } from '@/api/types'
import { useSessionStore } from '@/stores/session'

const session = useSessionStore()

const tab = ref<'model' | 'data'>('model')
const error = ref('')
const info = ref('')

// ---------- 模型配置 ----------
const llm = ref({ kind: 'openai', base_url: '', api_key: '', model: 'gpt-4o-mini', enabled: false })
const embedding = ref({ kind: 'openai', base_url: '', api_key: '', model: 'text-embedding-3-small', enabled: false })
const testing = ref<string | null>(null)
const testResult = ref<Record<string, string>>({})

async function loadSettings() {
  try {
    const st = await call<Settings>('SettingsGet', { workspace_id: session.workspaceId })
    session.settings = st
    const ps = st.provider_status
    if (ps.llm?.configured) {
      llm.value.enabled = true
      if (ps.llm.model) llm.value.model = ps.llm.model
    }
    if (ps.embedding?.configured) {
      embedding.value.enabled = true
      if (ps.embedding.model) embedding.value.model = ps.embedding.model
    }
  } catch (e) {
    error.value = (e as Error).message
  }
}

async function saveProvider(provider: 'llm' | 'embedding', cfg: typeof llm.value) {
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
    info.value = `${provider === 'llm' ? '对话模型' : '向量模型'}配置已保存`
  } catch (e) {
    error.value = (e as Error).message
  }
}

async function testProvider(provider: 'llm' | 'embedding') {
  testing.value = provider
  error.value = ''
  try {
    const r = await call<{ ok: boolean; latency_ms: number; error?: string }>('ProviderTest', {
      workspace_id: session.workspaceId,
      provider,
    })
    testResult.value[provider] = r.ok
      ? `✅ 连接正常（${r.latency_ms}ms）`
      : `❌ ${r.error ?? '连接失败'}`
  } catch (e) {
    testResult.value[provider] = `❌ ${(e as Error).message}`
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
      device_name: '本机浏览器',
      platform: 'web',
      app_version: '2.0.0',
    })
    await call('SyncPush', { workspace_id: session.workspaceId })
    await loadSyncStatus()
    info.value = syncStatus.value?.pending_count === 0 ? '✅ 同步完成，本地队列已清空' : '同步完成'
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    syncing.value = false
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
    error.value = '备份密码至少 4 位'
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
    backupResult.value = `✅ 备份已创建：${r.file_name}（${(r.size_bytes / 1024).toFixed(1)} KB）`
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    backingUp.value = false
  }
}

async function restoreBackup() {
  if (!restorePath.value || restorePassword.value.length < 4) {
    error.value = '请填写备份文件名与密码'
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
    info.value = r.restored ? '✅ 恢复成功，数据已替换为备份内容' : '恢复失败'
    await loadSettings()
  } catch (e) {
    error.value = (e as Error).message
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
    info.value = `导出完成：${r.file_name}，点击下载`
    window.location.href = `/api/v1/files?path=${encodeURIComponent(r.path)}`
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    exporting.value = false
  }
}

onMounted(() => {
  void loadSettings()
  void loadSyncStatus()
})
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>设置与数据</h1>
        <div class="subtitle">模型 Provider 配置 · 备份恢复 · 数据导出</div>
      </div>
    </div>

    <div v-if="error" class="error-banner">
      <span>{{ error }}</span>
      <button class="btn btn-sm" @click="error = ''">关闭</button>
    </div>
    <div v-if="info" class="offline-banner">{{ info }}</div>

    <div class="tabs">
      <div class="tab" :class="{ active: tab === 'model' }" @click="tab = 'model'">模型配置</div>
      <div class="tab" :class="{ active: tab === 'data' }" @click="tab = 'data'">数据管理</div>
    </div>

    <!-- 模型配置 -->
    <template v-if="tab === 'model'">
      <div class="card">
        <div class="flex-between mb-3">
          <div class="card-title" style="margin: 0">对话模型（Tutor / Grader / 诊断）</div>
          <span class="badge" :class="llm.enabled ? 'badge-success' : 'badge-offline'">
            {{ llm.enabled ? '已配置' : '未配置' }}
          </span>
        </div>
        <div class="form-row">
          <div class="field">
            <label>类型</label>
            <select v-model="llm.kind" class="select">
              <option value="openai">OpenAI 兼容</option>
              <option value="mock">本地模拟（测试）</option>
            </select>
          </div>
          <div class="field">
            <label>Base URL</label>
            <input v-model="llm.base_url" class="input" placeholder="https://api.openai.com/v1（默认）" />
          </div>
        </div>
        <div class="form-row">
          <div class="field">
            <label>模型名</label>
            <input v-model="llm.model" class="input" placeholder="gpt-4o-mini" />
          </div>
          <div class="field">
            <label>API Key（不回读，留空表示不修改）</label>
            <input v-model="llm.api_key" type="password" class="input" placeholder="sk-…" />
          </div>
        </div>
        <div class="flex gap-2" style="justify-content: flex-end">
          <button class="btn" :disabled="testing === 'llm'" @click="testProvider('llm')">
            {{ testing === 'llm' ? '测试中…' : '测试连接' }}
          </button>
          <button class="btn btn-primary" @click="saveProvider('llm', llm)">保存</button>
        </div>
        <div v-if="testResult.llm" class="hint mt-2">{{ testResult.llm }}</div>
      </div>

      <div class="card">
        <div class="flex-between mb-3">
          <div class="card-title" style="margin: 0">向量模型（资料检索）</div>
          <span class="badge" :class="embedding.enabled ? 'badge-success' : 'badge-offline'">
            {{ embedding.enabled ? '已配置' : '未配置' }}
          </span>
        </div>
        <div class="form-row">
          <div class="field">
            <label>类型</label>
            <select v-model="embedding.kind" class="select">
              <option value="openai">OpenAI 兼容</option>
              <option value="mock">本地模拟（测试）</option>
            </select>
          </div>
          <div class="field">
            <label>Base URL</label>
            <input v-model="embedding.base_url" class="input" placeholder="https://api.openai.com/v1（默认）" />
          </div>
        </div>
        <div class="form-row">
          <div class="field">
            <label>模型名</label>
            <input v-model="embedding.model" class="input" placeholder="text-embedding-3-small" />
          </div>
          <div class="field">
            <label>API Key（不回读，留空表示不修改）</label>
            <input v-model="embedding.api_key" type="password" class="input" placeholder="sk-…" />
          </div>
        </div>
        <div class="flex gap-2" style="justify-content: flex-end">
          <button class="btn" :disabled="testing === 'embedding'" @click="testProvider('embedding')">
            {{ testing === 'embedding' ? '测试中…' : '测试连接' }}
          </button>
          <button class="btn btn-primary" @click="saveProvider('embedding', embedding)">保存</button>
        </div>
        <div v-if="testResult.embedding" class="hint mt-2">{{ testResult.embedding }}</div>
        <div class="hint mt-2">密钥仅保存在本机 secrets 文件中，任何接口都不回读密钥。</div>
      </div>
    </template>

    <!-- 数据管理 -->
    <template v-if="tab === 'data'">
      <div class="card">
        <div class="card-title">云同步（本地模拟）</div>
        <p class="text-secondary mb-3">
          变更以幂等操作日志推送到同步服务端；当前为本地模拟实现（数据存于 sim-server 目录），后续可无缝切换真实云服务。
        </p>
        <div class="flex gap-3" style="align-items: center">
          <span class="badge" :class="(syncStatus?.pending_count ?? 0) > 0 ? 'badge-warning' : 'badge-success'">
            待推送 {{ syncStatus?.pending_count ?? '–' }}
          </span>
          <button class="btn btn-primary" :disabled="syncing" @click="doSync">
            {{ syncing ? '同步中…' : '立即同步' }}
          </button>
        </div>
      </div>

      <div class="card">
        <div class="card-title">创建加密备份</div>
        <p class="text-secondary mb-3">备份为数据库一致性快照（AES-256-GCM 加密），请牢记密码。</p>
        <div class="flex gap-3">
          <input v-model="backupPassword" type="password" class="input" style="max-width: 260px" placeholder="备份密码" />
          <button class="btn btn-primary" :disabled="backingUp" @click="createBackup">
            {{ backingUp ? '备份中…' : '创建备份' }}
          </button>
        </div>
        <div v-if="backupResult" class="hint mt-2">{{ backupResult }}</div>
      </div>

      <div class="card">
        <div class="card-title">恢复备份</div>
        <p class="text-secondary mb-3">恢复将整体替换当前数据（恢复前会自动保留一份 pre-restore 备份）。</p>
        <div class="form-row">
          <div class="field">
            <label>备份文件名</label>
            <input v-model="restorePath" class="input" placeholder="backup-….sqz" />
          </div>
          <div class="field">
            <label>密码</label>
            <input v-model="restorePassword" type="password" class="input" placeholder="备份密码" />
          </div>
        </div>
        <button class="btn btn-danger" :disabled="restoring" @click="restoreBackup">
          {{ restoring ? '恢复中…' : '恢复备份' }}
        </button>
      </div>

      <div class="card">
        <div class="card-title">数据导出</div>
        <div class="form-row">
          <div class="field">
            <label>范围</label>
            <select v-model="exportScope" class="select">
              <option value="all">全部</option>
              <option value="questions">题库</option>
              <option value="learning_records">学习记录</option>
              <option value="documents">资料</option>
            </select>
          </div>
          <div class="field">
            <label>格式</label>
            <select v-model="exportFormat" class="select">
              <option value="json">JSON</option>
              <option value="zip">ZIP</option>
            </select>
          </div>
        </div>
        <button class="btn btn-primary" :disabled="exporting" @click="doExport">
          {{ exporting ? '导出中…' : '导出并下载' }}
        </button>
      </div>
    </template>
  </div>
</template>
