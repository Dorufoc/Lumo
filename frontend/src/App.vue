<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { RouterView, useRoute } from 'vue-router'
import AppLayout from '@/layouts/AppLayout.vue'
import { useSessionStore } from '@/stores/session'

const route = useRoute()
const session = useSessionStore()

onMounted(() => {
  // 启动时恢复本地会话（工作区/用户），失败时也放行进入首页
  void session.bootstrap()
})

const ready = computed(() => session.ready)
const isOnboarding = computed(() => route.name === 'onboarding')
</script>

<template>
  <div v-if="!ready" class="loading"><div class="spinner"></div>&nbsp;正在连接本地服务…</div>
  <component :is="isOnboarding ? RouterView : AppLayout" v-else />
</template>
