<script setup lang="ts">
import { useRoute } from 'vue-router'

const route = useRoute()

const nav = [
  { name: 'dashboard', to: '/dashboard', icon: '📊', label: '今日' },
  { name: 'practice', to: '/practice', icon: '✏️', label: '练习' },
  { name: 'plan', to: '/plan', icon: '📅', label: '计划' },
  { name: 'review', to: '/review', icon: '🔁', label: '错题复习' },
  { name: 'library', to: '/library', icon: '📚', label: '题库与资料' },
  { name: 'tutor', to: '/tutor', icon: '🤖', label: 'AI Tutor' },
  { name: 'settings', to: '/settings', icon: '⚙️', label: '设置与数据' },
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
          <span aria-hidden="true">{{ n.icon }}</span><span>{{ n.label }}</span>
        </RouterLink>
      </nav>
      <div class="sidebar-footer">本地优先 · 数据保存在本机</div>
    </aside>
    <main class="main">
      <RouterView />
    </main>
  </div>
</template>
