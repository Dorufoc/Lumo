import { defineStore } from 'pinia'
import { defaultLocale, dictionaries, setActiveLocale, translate } from '@/i18n'
import type { Locale, TranslateFn, TranslateParams } from '@/i18n'

/** localStorage 持久化键（完整设计文档.md §7.6.1：i18n 状态全局持久化，语言切换即时生效且不丢页面状态）。 */
const LOCALE_KEY = 'lumo-locale'

function isLocale(value: string | null): value is Locale {
  return value === 'zh-CN' || value === 'en-US'
}

/** 初始化优先级：localStorage 手动选择 → 默认 zh-CN。 */
function resolveInitialLocale(): Locale {
  try {
    const saved = localStorage.getItem(LOCALE_KEY)
    if (isLocale(saved)) return saved
  } catch {
    // localStorage 不可用（隐私模式等）时回退默认，保证不破版
  }
  return defaultLocale
}

export const useI18nStore = defineStore('i18n', {
  state: (): { locale: Locale } => ({
    locale: defaultLocale,
  }),
  getters: {
    /** 绑定当前 locale 的翻译函数：t('common.save') → 当前语言文案；缺 key 回退 key 本身。 */
    t: (s): TranslateFn => (key: string, params?: TranslateParams) => translate(dictionaries[s.locale], key, params),
  },
  actions: {
    /** 应用启动时调用（main.ts 挂载前）：从 localStorage 恢复 locale 并同步模块级词典。 */
    init() {
      this.locale = resolveInitialLocale()
      setActiveLocale(this.locale)
    },
    /** 切换语言并持久化；非法 locale 回退默认 zh-CN。 */
    setLocale(locale: Locale) {
      const next: Locale = isLocale(locale) ? locale : defaultLocale
      if (next !== this.locale) this.locale = next
      setActiveLocale(next)
      try {
        localStorage.setItem(LOCALE_KEY, next)
      } catch {
        // 持久化失败不阻断本次切换
      }
    },
    /** 在 zh-CN 与 en-US 之间来回切换（UI 便捷入口）。 */
    toggleLocale() {
      this.setLocale(this.locale === 'zh-CN' ? 'en-US' : 'zh-CN')
    },
  },
})

/** 调试/QA 入口类型：main.ts 把 store 实例暴露到 window.__lumoI18n（本地优先应用，无安全影响）。 */
declare global {
  interface Window {
    __lumoI18n?: ReturnType<typeof useI18nStore>
  }
}
