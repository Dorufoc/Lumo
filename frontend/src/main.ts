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
