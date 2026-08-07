// 插件 API（API 设计文档 7.13 / 完整设计文档 4.24）。
// 插件为全局资源（无 workspace_id / user_id），本地优先安装运行。
import { call } from '@/api/client'
import type {
  DeleteResult,
  Plugin,
  PluginConfirmPermissionsReq,
  PluginInstallReq,
  PluginInvokeReq,
  PluginInvokeResult,
  PluginMarketItem,
  PluginSetEnabledReq,
  PluginThemeGetReq,
  PluginThemeGetResp,
  PluginUninstallReq,
} from '@/api/types'

/** 安装签名合法的插件（路径 + Ed25519 签名），返回新插件（默认禁用）。 */
export function pluginInstall(req: PluginInstallReq): Promise<Plugin> {
  return call<Plugin>('PluginInstall', { ...req })
}

/** 启用/禁用插件；声明了权限但未确认时启用 → INVALID_STATE。 */
export function pluginSetEnabled(req: PluginSetEnabledReq): Promise<Plugin> {
  return call<Plugin>('PluginSetEnabled', { ...req })
}

/** 卸载插件。 */
export function pluginUninstall(req: PluginUninstallReq): Promise<DeleteResult> {
  return call<DeleteResult>('PluginUninstall', { ...req })
}

/** 确认插件权限（弹窗同意后落库），返回最新插件。 */
export function pluginConfirmPermissions(req: PluginConfirmPermissionsReq): Promise<Plugin> {
  return call<Plugin>('PluginConfirmPermissions', { ...req })
}

/** 在隔离沙箱中运行已启用插件。 */
export function pluginInvoke(req: PluginInvokeReq): Promise<PluginInvokeResult> {
  return call<PluginInvokeResult>('PluginInvoke', { ...req })
}

/** 列出全部已安装插件（全局，倒序）。 */
export function pluginList(): Promise<Plugin[]> {
  return call<Plugin[]>('PluginList', {})
}

/** 列出本地市场目录（Todo 37：来源 = plugins 表，含描述与权限的 DTO）。 */
export function pluginMarketList(): Promise<PluginMarketItem[]> {
  return call<PluginMarketItem[]>('PluginMarketList', {})
}

/** 在沙箱中执行主题插件，返回校验后的 theme tokens（用于 CSS 变量应用）。 */
export function pluginThemeGet(req: PluginThemeGetReq): Promise<PluginThemeGetResp> {
  return call<PluginThemeGetResp>('PluginThemeGet', { ...req })
}
