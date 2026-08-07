import { defineStore } from 'pinia'

export type ThemeName = 'light' | 'dark'

/** 主题插件 tokens 持久化键（Todo 37：应用主题插件后存 CSS 变量，重启后保持）。 */
const TOKENS_KEY = 'lumo-theme-tokens'

/** localStorage 持久化键（完整设计文档.md §7.5.5：主题选择由 theme store 持久化，重启后保持）。 */
const THEME_KEY = 'lumo-theme'

/** 把主题落到 <html data-theme="...">，并同步 color-scheme（原生控件/滚动条跟随）。 */
function applyTheme(theme: ThemeName): void {
  const el = document.documentElement
  el.dataset.theme = theme
  el.style.colorScheme = theme
}

/** 初始化优先级：localStorage 手动选择 → prefers-color-scheme 系统偏好 → 浅色兜底。 */
function resolveInitialTheme(): ThemeName {
  try {
    const saved = localStorage.getItem(THEME_KEY)
    if (saved === 'light' || saved === 'dark') return saved
    if (window.matchMedia?.('(prefers-color-scheme: dark)').matches) return 'dark'
  } catch {
    // localStorage / matchMedia 不可用（隐私模式等）时回退浅色，保证不破版
  }
  return 'light'
}

/** 读取持久化的主题 tokens（无效 JSON / 非对象时返回空映射）。 */
function readSavedTokens(): Record<string, string> {
  try {
    const raw = localStorage.getItem(TOKENS_KEY)
    if (!raw) return {}
    const v = JSON.parse(raw)
    if (v && typeof v === 'object' && !Array.isArray(v)) return v as Record<string, string>
  } catch {
    // 损坏的缓存视为无 tokens，不阻断启动
  }
  return {}
}

export const useThemeStore = defineStore('theme', {
  state: (): { theme: ThemeName; tokens: Record<string, string> } => ({
    theme: 'light',
    tokens: {},
  }),
  getters: {
    isDark: (s): boolean => s.theme === 'dark',
  },
  actions: {
    /** 应用启动时调用（main.ts 挂载前），避免首屏主题闪烁；并恢复持久化的主题 tokens。 */
    init() {
      this.theme = resolveInitialTheme()
      applyTheme(this.theme)
      this.tokens = readSavedTokens()
      for (const [k, v] of Object.entries(this.tokens)) {
        document.documentElement.style.setProperty(k, v)
      }
    },
    /** 手动切换并持久化；覆盖系统偏好，重启后由 localStorage 保持。 */
    setTheme(theme: ThemeName) {
      this.theme = theme
      applyTheme(theme)
      try {
        localStorage.setItem(THEME_KEY, theme)
      } catch {
        // 持久化失败不阻断本次切换
      }
    },
    toggleTheme() {
      this.setTheme(this.theme === 'dark' ? 'light' : 'dark')
    },
    /** 应用主题插件 tokens（后端已校验格式防注入）；每次整体替换，无需处理残留变量。 */
    applyTokens(tokens: Record<string, string>) {
      this.tokens = { ...tokens }
      const el = document.documentElement
      for (const [k, v] of Object.entries(this.tokens)) {
        el.style.setProperty(k, v)
      }
      try {
        localStorage.setItem(TOKENS_KEY, JSON.stringify(this.tokens))
      } catch {
        // 持久化失败不阻断本次应用
      }
    },
    /** 清除主题 tokens，恢复 tokens.css 默认值（移除内联变量）。 */
    clearTokens() {
      const el = document.documentElement
      for (const k of Object.keys(this.tokens)) {
        el.style.removeProperty(k)
      }
      this.tokens = {}
      try {
        localStorage.removeItem(TOKENS_KEY)
      } catch {
        // 清理失败不阻断
      }
    },
  },
})
