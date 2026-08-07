<script setup lang="ts">
// 插件管理视图（API 文档 7.13 / 完整设计文档 4.24）。
// 插件为全局资源：安装（路径 + Ed25519 签名）→ 权限确认 → 启用 → 运行。
import { onMounted, ref } from 'vue'
import { localizedMessageOf } from '@/api/client'
import {
  pluginConfirmPermissions,
  pluginInstall,
  pluginInvoke,
  pluginList,
  pluginMarketList,
  pluginSetEnabled,
  pluginThemeGet,
  pluginUninstall,
} from '@/api/plugins'
import type { Plugin, PluginInvokeResult, PluginMarketItem } from '@/api/types'
import { useThemeStore } from '@/stores/theme'

const themeStore = useThemeStore()

const loading = ref(false)
const busy = ref(false)
const error = ref('')
const info = ref('')
const plugins = ref<Plugin[]>([])

// 主题插件市场（Todo 37）
const marketLoading = ref(false)
const marketError = ref('')
const market = ref<PluginMarketItem[]>([])
const applyingTheme = ref(false)
const themeError = ref('')

// 安装弹窗
const installDialog = ref(false)
const installForm = ref({ path: '', signature: '' })

// 权限确认弹窗
const permTarget = ref<Plugin | null>(null)
const permChecked = ref<Record<string, boolean>>({})

// 卸载确认弹窗
const uninstallTarget = ref<Plugin | null>(null)
const uninstallBusy = ref(false)

// 运行结果弹窗
const invokeTarget = ref<Plugin | null>(null)
const invokeResult = ref<PluginInvokeResult | null>(null)
const invokeError = ref('')
const invokeBusy = ref(false)

async function refresh() {
  loading.value = true
  error.value = ''
  try {
    plugins.value = await pluginList()
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    loading.value = false
  }
}

async function refreshMarket() {
  marketLoading.value = true
  marketError.value = ''
  try {
    market.value = await pluginMarketList()
  } catch (e) {
    marketError.value = localizedMessageOf(e)
  } finally {
    marketLoading.value = false
  }
}

// 在沙箱中执行主题插件，取回校验后的 tokens 并应用到 CSS 变量（不修改 tokens.css）
async function applyThemePlugin(item: PluginMarketItem) {
  applyingTheme.value = true
  themeError.value = ''
  try {
    const resp = await pluginThemeGet({ plugin_id: item.id })
    if (!resp.ok) {
      themeError.value = resp.error || 'plugins.applyTheme'
      return
    }
    if (resp.tokens && Object.keys(resp.tokens).length > 0) {
      themeStore.applyTokens(resp.tokens)
      info.value = 'plugins.themeApplied'
    } else {
      themeError.value = 'plugins.applyTheme'
    }
  } catch (e) {
    themeError.value = localizedMessageOf(e)
  } finally {
    applyingTheme.value = false
  }
}

function restoreDefaultTheme() {
  themeError.value = ''
  themeStore.clearTokens()
  info.value = 'plugins.applyThemeSuccess'
}

function openInstall() {
  error.value = ''
  info.value = ''
  installForm.value = { path: '', signature: '' }
  installDialog.value = true
}

async function submitInstall() {
  if (!installForm.value.path || !installForm.value.signature) return
  busy.value = true
  error.value = ''
  info.value = ''
  try {
    await pluginInstall({ path: installForm.value.path.trim(), signature: installForm.value.signature.trim() })
    installDialog.value = false
    info.value = 'plugins.installed'
    await refresh()
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    busy.value = false
  }
}

async function toggleEnabled(p: Plugin, enabled: boolean) {
  error.value = ''
  try {
    const updated = await pluginSetEnabled({ plugin_id: p.id, enabled })
    const idx = plugins.value.findIndex((x) => x.id === p.id)
    if (idx >= 0) plugins.value[idx] = updated
    if (enabled) info.value = 'plugins.enabled'
    else info.value = 'plugins.disabled'
  } catch (e) {
    error.value = localizedMessageOf(e)
  }
}

// 启用时若后端返回 INVALID_STATE（声明权限未确认）→ 打开权限确认弹窗
async function onEnableClick(p: Plugin) {
  error.value = ''
  if (p.permissions.length > 0) {
    openPermDialog(p)
    return
  }
  await toggleEnabled(p, true)
}

function openPermDialog(p: Plugin) {
  permTarget.value = p
  permChecked.value = {}
  for (const perm of p.manifest.permissions) permChecked.value[perm] = true
}

async function submitPermConfirm(agree: boolean) {
  if (!permTarget.value) return
  const target = permTarget.value
  busy.value = true
  error.value = ''
  try {
    if (agree) {
      const perms = target.manifest.permissions.filter((perm) => permChecked.value[perm])
      await pluginConfirmPermissions({ plugin_id: target.id, permissions: perms })
      await toggleEnabled(target, true)
    }
    permTarget.value = null
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    busy.value = false
  }
}

function openUninstall(p: Plugin) {
  uninstallTarget.value = p
}

async function submitUninstall() {
  if (!uninstallTarget.value) return
  const target = uninstallTarget.value
  uninstallBusy.value = true
  error.value = ''
  try {
    await pluginUninstall({ plugin_id: target.id })
    uninstallTarget.value = null
    info.value = 'plugins.uninstalled'
    await refresh()
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    uninstallBusy.value = false
  }
}

async function runPlugin(p: Plugin) {
  invokeTarget.value = p
  invokeResult.value = null
  invokeError.value = ''
  invokeBusy.value = true
  try {
    invokeResult.value = await pluginInvoke({ plugin_id: p.id })
  } catch (e) {
    invokeError.value = localizedMessageOf(e)
  } finally {
    invokeBusy.value = false
  }
}

function formatResult(r: PluginInvokeResult | null): string {
  if (!r) return ''
  return JSON.stringify(r.result ?? null, null, 2)
}

onMounted(() => {
  refresh()
  refreshMarket()
})
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('plugins.title') }}</h1>
        <div class="subtitle">{{ $t('plugins.subtitle') }}</div>
      </div>
      <button class="btn btn-primary" @click="openInstall">{{ $t('plugins.install') }}</button>
    </div>

    <div v-if="error" class="error-banner">{{ error }}</div>
    <div v-if="info" class="offline-banner">{{ $t(info) }}</div>

    <div v-if="loading" class="loading"><div class="spinner"></div></div>
    <div v-else-if="plugins.length === 0" class="empty">{{ $t('plugins.empty') }}</div>

    <div v-else class="plugin-list">
      <div v-for="p in plugins" :key="p.id" class="card plugin-card">
        <div class="plugin-head">
          <div>
            <span class="plugin-name">{{ p.name }}</span>
            <span class="plugin-version">v{{ p.version }}</span>
            <span class="plugin-state" :class="p.enabled ? 'ok' : 'off'">
              {{ p.enabled ? $t('plugins.enabledState') : $t('plugins.disabledState') }}
            </span>
          </div>
          <div class="flex gap-2">
            <button class="btn btn-sm" :disabled="!p.enabled" @click="runPlugin(p)">{{ $t('plugins.run') }}</button>
            <button v-if="!p.enabled" class="btn btn-sm btn-primary" @click="onEnableClick(p)">{{ $t('plugins.enable') }}</button>
            <button v-else class="btn btn-sm" @click="toggleEnabled(p, false)">{{ $t('plugins.disable') }}</button>
            <button class="btn btn-sm btn-danger" @click="openUninstall(p)">{{ $t('plugins.uninstall') }}</button>
          </div>
        </div>
        <div class="plugin-meta">
          <div v-if="p.manifest.description" class="plugin-desc">{{ p.manifest.description }}</div>
          <div class="plugin-row">
            <span class="meta-label">{{ $t('plugins.entrypoint') }}</span>
            <code>{{ p.manifest.entrypoint.join(' ') }}</code>
          </div>
          <div class="plugin-row">
            <span class="meta-label">{{ $t('plugins.permissions') }}</span>
            <span v-if="p.manifest.permissions.length === 0" class="meta-none">{{ $t('plugins.noPermissions') }}</span>
            <span v-else class="perm-chips">
              <span v-for="perm in p.manifest.permissions" :key="perm" class="chip" :class="{ confirmed: p.permissions.includes(perm) }">
                {{ perm }}
              </span>
            </span>
          </div>
          <div class="plugin-row">
            <span class="meta-label">{{ $t('plugins.installedAt') }}</span>
            <span>{{ p.installed_at }}</span>
          </div>
        </div>
      </div>
    </div>

    <!-- 主题插件市场（Todo 37） -->
    <div class="market-section">
      <div class="page-header market-header">
        <div>
          <h2>{{ $t('plugins.marketTitle') }}</h2>
          <div class="subtitle">{{ $t('plugins.marketSubtitle') }}</div>
        </div>
        <button class="btn btn-sm" :disabled="applyingTheme" @click="restoreDefaultTheme">
          {{ $t('plugins.clearTheme') }}
        </button>
      </div>

      <div v-if="marketError" class="error-banner">{{ marketError }}</div>
      <div v-if="themeError" class="error-banner">{{ themeError }}</div>
      <div v-if="info" class="offline-banner">{{ $t(info) }}</div>

      <div v-if="marketLoading" class="loading"><div class="spinner"></div></div>
      <div v-else-if="market.length === 0" class="empty">{{ $t('plugins.marketEmpty') }}</div>

      <div v-else class="plugin-list">
        <div v-for="item in market" :key="item.id" class="card plugin-card">
          <div class="plugin-head">
            <div>
              <span class="plugin-name">{{ item.name }}</span>
              <span class="plugin-version">v{{ item.version }}</span>
              <span class="plugin-state" :class="item.enabled ? 'ok' : 'off'">
                {{ item.enabled ? $t('plugins.enabledState') : $t('plugins.disabledState') }}
              </span>
            </div>
            <div class="flex gap-2">
              <button class="btn btn-sm btn-primary" :disabled="applyingTheme" @click="applyThemePlugin(item)">
                {{ $t('plugins.applyTheme') }}
              </button>
            </div>
          </div>
          <div class="plugin-meta">
            <div v-if="item.description" class="plugin-desc">{{ item.description }}</div>
            <div class="plugin-row">
              <span class="meta-label">{{ $t('plugins.marketPermissions') }}</span>
              <span v-if="item.permissions.length === 0" class="meta-none">{{ $t('plugins.noPermissions') }}</span>
              <span v-else class="perm-chips">
                <span v-for="perm in item.permissions" :key="perm" class="chip">{{ perm }}</span>
              </span>
            </div>
            <div class="plugin-row">
              <span class="meta-label">{{ $t('plugins.installedAt') }}</span>
              <span>{{ item.installed_at }}</span>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- 安装弹窗 -->
    <div v-if="installDialog" class="modal-mask">
      <div class="card modal">
        <h3>{{ $t('plugins.install') }}</h3>
        <div class="field">
          <label>{{ $t('plugins.path') }} *</label>
          <input v-model="installForm.path" class="input" type="text" :placeholder="$t('plugins.pathPlaceholder')" />
        </div>
        <div class="field">
          <label>{{ $t('plugins.signature') }} *</label>
          <input v-model="installForm.signature" class="input" type="text" :placeholder="$t('plugins.signaturePlaceholder')" />
        </div>
        <div class="flex gap-3 mt-3">
          <button class="btn" :disabled="busy" @click="installDialog = false">{{ $t('common.cancel') }}</button>
          <button
            class="btn btn-primary"
            :disabled="busy || !installForm.path || !installForm.signature"
            @click="submitInstall"
          >
            {{ busy ? $t('common.submitting') : $t('plugins.confirmInstall') }}
          </button>
        </div>
      </div>
    </div>

    <!-- 权限确认弹窗 -->
    <div v-if="permTarget" class="modal-mask">
      <div class="card modal">
        <h3>{{ $t('plugins.confirmPermissionsTitle') }}</h3>
        <p class="perm-hint">{{ $t('plugins.confirmPermissionsHint', { name: permTarget.name }) }}</p>
        <div class="perm-options">
          <label v-for="perm in permTarget.manifest.permissions" :key="perm" class="check-line">
            <input v-model="permChecked[perm]" type="checkbox" />
            <span>{{ perm }}</span>
          </label>
        </div>
        <div class="flex gap-3 mt-3">
          <button class="btn" :disabled="busy" @click="submitPermConfirm(false)">{{ $t('plugins.reject') }}</button>
          <button class="btn btn-primary" :disabled="busy" @click="submitPermConfirm(true)">
            {{ busy ? $t('common.submitting') : $t('plugins.agree') }}
          </button>
        </div>
      </div>
    </div>

    <!-- 卸载确认弹窗 -->
    <div v-if="uninstallTarget" class="modal-mask">
      <div class="card modal">
        <h3>{{ $t('plugins.confirmUninstall') }}</h3>
        <p class="perm-hint">{{ $t('plugins.confirmUninstallHint', { name: uninstallTarget.name }) }}</p>
        <div class="flex gap-3 mt-3">
          <button class="btn" :disabled="uninstallBusy" @click="uninstallTarget = null">{{ $t('common.cancel') }}</button>
          <button class="btn btn-danger" :disabled="uninstallBusy" @click="submitUninstall">
            {{ uninstallBusy ? $t('common.submitting') : $t('plugins.confirmUninstall') }}
          </button>
        </div>
      </div>
    </div>

    <!-- 运行结果弹窗 -->
    <div v-if="invokeTarget" class="modal-mask">
      <div class="card modal modal-lg">
        <h3>{{ $t('plugins.runResult', { name: invokeTarget.name }) }}</h3>
        <div v-if="invokeBusy" class="loading"><div class="spinner"></div></div>
        <div v-else-if="invokeError" class="error-banner">{{ invokeError }}</div>
        <div v-else>
          <div v-if="invokeResult" :class="invokeResult.ok ? 'result-ok' : 'result-fail'">
            <span v-if="invokeResult.ok">{{ $t('plugins.runOk') }}</span>
            <span v-else>{{ $t('plugins.runFailed') }}: {{ invokeResult.error }}</span>
          </div>
          <pre v-if="invokeResult && invokeResult.ok" class="result-pre">{{ formatResult(invokeResult) }}</pre>
        </div>
        <div class="flex gap-3 mt-3">
          <button class="btn" @click="invokeTarget = null">{{ $t('common.close') }}</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.market-section {
  margin-top: var(--space-5);
  padding-top: var(--space-4);
  border-top: 1px solid var(--border);
}

.market-header {
  margin-bottom: var(--space-3);
}

.plugin-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}

.plugin-card {
  padding: var(--space-4);
}

.plugin-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: var(--space-3);
  flex-wrap: wrap;
}

.plugin-name {
  font-size: var(--text-lg);
  font-weight: 700;
  color: var(--text);
}

.plugin-version {
  margin-left: var(--space-2);
  font-size: var(--text-xs);
  color: var(--text-muted);
  background: var(--bg-elevated);
  padding: 2px 8px;
  border-radius: var(--radius-full);
}

.plugin-state {
  margin-left: var(--space-2);
  font-size: var(--text-xs);
  padding: 2px 8px;
  border-radius: var(--radius-full);
}

.plugin-state.ok {
  color: var(--success);
  background: color-mix(in srgb, var(--success) 12%, transparent);
}

.plugin-state.off {
  color: var(--text-muted);
  background: var(--bg-elevated);
}

.plugin-meta {
  margin-top: var(--space-3);
  font-size: var(--text-xs);
  color: var(--text-secondary);
}

.plugin-desc {
  margin-bottom: var(--space-2);
}

.plugin-row {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  margin-top: var(--space-1);
}

.meta-label {
  min-width: 72px;
  color: var(--text-muted);
}

.meta-none {
  color: var(--text-muted);
}

.perm-chips {
  display: inline-flex;
  gap: var(--space-1);
  flex-wrap: wrap;
}

.chip {
  font-size: var(--text-xs);
  padding: 2px 8px;
  border-radius: var(--radius-full);
  background: var(--bg-elevated);
  color: var(--text-secondary);
}

.chip.confirmed {
  color: var(--success);
  background: color-mix(in srgb, var(--success) 12%, transparent);
}

.perm-hint {
  color: var(--text-secondary);
  font-size: var(--text-xs);
  margin: var(--space-2) 0;
}

.perm-options {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
  margin-top: var(--space-2);
}

.result-ok {
  color: var(--success);
  font-size: var(--text-sm);
  margin: var(--space-2) 0;
}

.result-fail {
  color: var(--danger);
  font-size: var(--text-sm);
  margin: var(--space-2) 0;
}

.result-pre {
  max-height: 320px;
  overflow: auto;
  background: var(--bg-elevated);
  border-radius: var(--radius-sm);
  padding: var(--space-3);
  font-size: var(--text-xs);
  color: var(--text);
}

.modal-lg {
  width: 560px;
  max-width: 92vw;
}
</style>
