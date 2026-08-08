import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import './styles/main.css'
import { useThemeStore } from './stores/theme'
import { useI18nStore } from './stores/i18n'

const app = createApp(App)
const pinia = createPinia()
app.use(pinia)
app.use(router)

// 挂载前应用主题（localStorage → prefers-color-scheme），避免首屏主题闪烁
useThemeStore(pinia).init()

// 挂载前初始化 i18n store（恢复持久化 locale），并暴露全局 $t 模板辅助
const i18n = useI18nStore(pinia)
i18n.init()
app.config.globalProperties.$t = (key: string, params?: Record<string, string | number>) => i18n.t(key, params)
// 调试/QA 入口：浏览器控制台可直接调用 __lumoI18n（setLocale / t / toggleLocale）
window.__lumoI18n = i18n

app.mount('#app')

// PWA：仅生产模式注册 Service Worker（开发模式跳过，避免干扰 vite HMR/代理）
// sw.js 由构建插件注入当次产物清单，作用域为根 /，与 manifest start_url 一致。
if (import.meta.env.PROD && 'serviceWorker' in navigator) {
  window.addEventListener('load', () => {
    navigator.serviceWorker
      .register('/sw.js')
      .then((reg) => {
        console.info('[pwa] Service Worker registered:', reg.scope)
      })
      .catch((err) => {
        console.warn('[pwa] Service Worker 注册失败:', err)
      })
  })
}
