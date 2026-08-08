<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useSessionStore } from '@/stores/session'

const route = useRoute()
const session = useSessionStore()

type NavItem = { name: string; to: string; icon: string; labelKey: string; roles?: string[]; requiresSpeech?: boolean }

const nav: NavItem[] = [
  { name: 'dashboard', to: '/dashboard', icon: '📊', labelKey: 'nav.dashboard' },
  { name: 'practice', to: '/practice', icon: '✏️', labelKey: 'nav.practice' },
  { name: 'plan', to: '/plan', icon: '📅', labelKey: 'nav.plan' },
  { name: 'calendar', to: '/calendar', icon: '🗓️', labelKey: 'nav.calendar' },
  { name: 'classes', to: '/classes', icon: '🏫', labelKey: 'nav.classes' },
  { name: 'assignments', to: '/assignments', icon: '📝', labelKey: 'nav.assignments' },
  { name: 'review', to: '/review', icon: '🔁', labelKey: 'nav.review' },
  { name: 'reports', to: '/reports', icon: '📈', labelKey: 'nav.reports' },
  { name: 'health', to: '/health', icon: '🏥', labelKey: 'nav.health' },
  { name: 'speaking', to: '/speaking', icon: '🎤', labelKey: 'nav.speaking', requiresSpeech: true },
  { name: 'library', to: '/library', icon: '📚', labelKey: 'nav.library' },
  { name: 'tutor', to: '/tutor', icon: '🤖', labelKey: 'nav.tutor' },
  { name: 'family', to: '/family', icon: '👨‍👩‍👧', labelKey: 'nav.family', roles: ['parent'] },
  { name: 'knowledgeGraph', to: '/knowledge-graph', icon: '🕸️', labelKey: 'nav.knowledgeGraph' },
  { name: 'plugins', to: '/plugins', icon: '🧩', labelKey: 'nav.plugins' },
  { name: 'webhooks', to: '/webhooks', icon: '🔗', labelKey: 'nav.webhooks' },
  { name: 'community', to: '/community', icon: '🏘️', labelKey: 'nav.community' },
  { name: 'requests', to: '/requests', icon: '🙋', labelKey: 'nav.requests' },
  { name: 'settings', to: '/settings', icon: '⚙️', labelKey: 'nav.settings' },
]

// TTS/ASR 均未配置时隐藏口语练习入口（契约：前端隐藏入口）。
const speechEnabled = computed(() => {
  const ps = session.settings?.provider_status
  return Boolean(ps?.tts?.configured || ps?.asr?.configured)
})

const visibleNav = computed(() => {
  const role = session.user?.role
  return nav.filter((n) => {
    if (n.requiresSpeech && !speechEnabled.value) return false
    return !n.roles || !role || n.roles.includes(role)
  })
})

const isActive = (n: NavItem) =>
  route.name === n.name || (n.name === 'practice' && route.name === 'result')

// 移动端抽屉导航（≤767px，设计文档 7.5.6）：汉堡展开 / 遮罩与 Esc 关闭 / 锁定 body 滚动。
const drawerOpen = ref(false)
const menuBtn = ref<HTMLButtonElement | null>(null)
const drawerEl = ref<HTMLElement | null>(null)

const openDrawer = () => {
  drawerOpen.value = true
}
const closeDrawer = () => {
  drawerOpen.value = false
}

const onKeydown = (e: KeyboardEvent) => {
  if (e.key === 'Escape' && drawerOpen.value) closeDrawer()
}

onMounted(() => {
  window.addEventListener('keydown', onKeydown)
  // 侧边栏需读取 provider_status（tts/asr 配置态）决定口语入口显隐；
  // 若会话尚未加载过设置（如直接深链进入），补一次加载，不影响现有逻辑。
  if (!session.settings && session.workspace) void session.refreshSettings()
})

onUnmounted(() => {
  window.removeEventListener('keydown', onKeydown)
})

watch(drawerOpen, async (open) => {
  document.body.style.overflow = open ? 'hidden' : ''
  // 焦点管理：打开聚焦面板，关闭聚焦汉堡（7.5.6）
  if (open) {
    await nextTick()
    drawerEl.value?.focus()
  } else {
    menuBtn.value?.focus()
  }
})

// 路由切换后自动关闭抽屉（避免跳转后遮罩残留）。
watch(() => route.fullPath, () => {
  if (drawerOpen.value) closeDrawer()
})
</script>

<template>
  <div class="app-shell">
    <!-- 移动端顶栏（≤767px 显示，7.5.6） -->
    <header class="mobile-header">
      <button ref="menuBtn" class="menu-btn" type="button"
        :aria-label="drawerOpen ? $t('nav.closeMenu') : $t('nav.openMenu')"
        :aria-expanded="drawerOpen" @click="drawerOpen ? closeDrawer() : openDrawer()">
        <span class="hamburger" aria-hidden="true"></span>
      </button>
      <span class="mobile-brand">Lumo AI</span>
    </header>

    <aside class="sidebar">
      <div class="sidebar-brand"><span>Lumo AI</span></div>
      <nav class="sidebar-nav">
        <RouterLink v-for="n in visibleNav" :key="n.name" :to="n.to" class="nav-item" :class="{ active: isActive(n) }">
          <span aria-hidden="true">{{ n.icon }}</span><span>{{ $t(n.labelKey) }}</span>
        </RouterLink>
      </nav>
      <div class="sidebar-footer">{{ $t('nav.footer') }}</div>
    </aside>

    <!-- 抽屉式导航（移动端，7.5.6） -->
    <div v-if="drawerOpen" class="drawer-mask" @click="closeDrawer"></div>
    <aside v-if="drawerOpen" ref="drawerEl" class="drawer" role="dialog" aria-modal="true" tabindex="-1">
      <div class="sidebar-brand"><span>Lumo AI</span></div>
      <nav class="sidebar-nav">
        <RouterLink v-for="n in visibleNav" :key="n.name" :to="n.to" class="nav-item" :class="{ active: isActive(n) }" @click="closeDrawer">
          <span aria-hidden="true">{{ n.icon }}</span><span>{{ $t(n.labelKey) }}</span>
        </RouterLink>
      </nav>
      <div class="sidebar-footer">{{ $t('nav.footer') }}</div>
    </aside>

    <main class="main">
      <RouterView />
    </main>
  </div>
</template>
