import { defineStore } from 'pinia'

export type ThemeName = 'light' | 'dark'

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

export const useThemeStore = defineStore('theme', {
  state: (): { theme: ThemeName } => ({
    theme: 'light',
  }),
  getters: {
    isDark: (s): boolean => s.theme === 'dark',
  },
  actions: {
    /** 应用启动时调用（main.ts 挂载前），避免首屏主题闪烁。 */
    init() {
      this.theme = resolveInitialTheme()
      applyTheme(this.theme)
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
  },
})
