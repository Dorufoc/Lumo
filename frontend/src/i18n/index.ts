// 手写 i18n 引擎（不引入 vue-i18n 依赖，见 .omo/plans Todo 7 验收）。
// 职责：语言包注册表、点路径取词、{param} 插值、缺 key 回退（返回 key 本身，绝不空白）。

import { zhCN } from './zh-CN'
import { enUS } from './en-US'
import type { Messages } from './zh-CN'

export { zhCN, enUS }
export type { Messages }

/** 支持的语言环境。 */
export type Locale = 'zh-CN' | 'en-US'

/** 默认语言（完整设计文档.md 4.24 O6：默认 zh-CN，en-US 可选）。 */
export const defaultLocale: Locale = 'zh-CN'

/** 语言包注册表：store 经 dictionaries[locale] 取当前词典。 */
export const dictionaries: Record<Locale, Messages> = {
  'zh-CN': zhCN,
  'en-US': enUS,
}

/** 插值参数：t('common.greeting', { name: 'Lumo' }) → {name} 的替换值。 */
export type TranslateParams = Record<string, string | number>

/** 翻译函数签名（store getter 与全局 $t 共用）。 */
export type TranslateFn = (key: string, params?: TranslateParams) => string

/**
 * 核心解析器：点路径取词（'common.save'）+ {name} 插值 + 缺 key 回退。
 * - 任意一段路径缺失（含中间节点）→ 返回完整 key（t('nav.does.not.exist') → 'nav.does.not.exist'）。
 * - key 解析到非字符串节点（中间层对象/空串）→ 同样回退 key，绝不返回空白/undefined。
 */
export function translate(dict: Messages, key: string, params?: TranslateParams): string {
  let node: unknown = dict
  for (const part of key.split('.')) {
    if (node === null || typeof node !== 'object') return key
    const obj = node as Record<string, unknown>
    if (!Object.prototype.hasOwnProperty.call(obj, part)) return key
    node = obj[part]
  }
  if (typeof node !== 'string') return key
  return params ? interpolate(node, params) : node
}

/** 把 {name} 占位符替换为参数值；未提供的参数保留原占位符原样输出。 */
function interpolate(template: string, params: TranslateParams): string {
  return template.replace(/\{(\w+)\}/g, (match, name: string) =>
    Object.prototype.hasOwnProperty.call(params, name) ? String(params[name]) : match,
  )
}

/** 绑定固定词典的翻译函数（纯函数形态，供非响应式上下文/测试使用）。 */
export function createT(dict: Messages): TranslateFn {
  return (key, params) => translate(dict, key, params)
}

// ---- 模块级活动词典（供命令行/调试 QA 使用；响应式入口是 stores/i18n.ts 的 getter t）----

let activeDict: Messages = dictionaries[defaultLocale]

/** 切换模块级活动词典；由 store 在 init/setLocale 时同步调用。 */
export function setActiveLocale(locale: Locale): void {
  activeDict = dictionaries[locale]
}

/** 模块级翻译函数：使用最近一次 setActiveLocale 设定的词典；缺 key 回退 key。 */
export function t(key: string, params?: TranslateParams): string {
  return translate(activeDict, key, params)
}

// 全局模板辅助 $t（与 vue-i18n 同名约定），供组件模板直接使用：{{ $t('common.save') }}
declare module 'vue' {
  interface ComponentCustomProperties {
    $t: TranslateFn
  }
}
