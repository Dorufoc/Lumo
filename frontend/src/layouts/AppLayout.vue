<script setup lang="ts">
import { useRoute } from 'vue-router'

const route = useRoute()

const nav = [
  { name: 'dashboard', to: '/dashboard', icon: '📊', labelKey: 'nav.dashboard' },
  { name: 'practice', to: '/practice', icon: '✏️', labelKey: 'nav.practice' },
  { name: 'plan', to: '/plan', icon: '📅', labelKey: 'nav.plan' },
  { name: 'review', to: '/review', icon: '🔁', labelKey: 'nav.review' },
  { name: 'library', to: '/library', icon: '📚', labelKey: 'nav.library' },
  { name: 'tutor', to: '/tutor', icon: '🤖', labelKey: 'nav.tutor' },
  { name: 'settings', to: '/settings', icon: '⚙️', labelKey: 'nav.settings' },
]

const isActive = (n: { name: string; to: string }) =>
  route.name === n.name || (n.name === 'practice' && route.name === 'result')
</script>

<template>
  <div class="app-shell">
    <aside class="sidebar">
      <div class="sidebar-brand"><span>Lumo AI</span></div>
      <nav class="sidebar-nav">
        <RouterLink v-for="n in nav" :key="n.name" :to="n.to" class="nav-item" :class="{ active: isActive(n) }">
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
