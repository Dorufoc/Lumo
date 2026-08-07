<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { useSessionStore } from '@/stores/session'

const route = useRoute()
const session = useSessionStore()

type NavItem = { name: string; to: string; icon: string; labelKey: string; roles?: string[] }

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
  { name: 'library', to: '/library', icon: '📚', labelKey: 'nav.library' },
  { name: 'tutor', to: '/tutor', icon: '🤖', labelKey: 'nav.tutor' },
  { name: 'family', to: '/family', icon: '👨‍👩‍👧', labelKey: 'nav.family', roles: ['parent'] },
  { name: 'knowledgeGraph', to: '/knowledge-graph', icon: '🕸️', labelKey: 'nav.knowledgeGraph' },
  { name: 'settings', to: '/settings', icon: '⚙️', labelKey: 'nav.settings' },
]

const visibleNav = computed(() => {
  const role = session.user?.role
  return nav.filter((n) => !n.roles || !role || n.roles.includes(role))
})

const isActive = (n: NavItem) =>
  route.name === n.name || (n.name === 'practice' && route.name === 'result')
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
