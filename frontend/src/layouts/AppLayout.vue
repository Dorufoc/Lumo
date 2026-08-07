<script setup lang="ts">
import { computed, onMounted } from 'vue'
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

// 侧边栏需读取 provider_status（tts/asr 配置态）决定口语入口显隐；
// 若会话尚未加载过设置（如直接深链进入），补一次加载，不影响现有逻辑。
onMounted(() => {
  if (!session.settings && session.workspace) void session.refreshSettings()
})
</script>

<template>
  <div class="app-shell">
    <aside class="sidebar">
      <div class="sidebar-brand"><span>Lumo AI</span></div>
      <nav class="sidebar-nav">
        <RouterLink v-for="n in visibleNav" :key="n.name" :to="n.to" class="nav-item" :class="{ active: isActive(n) }">
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
