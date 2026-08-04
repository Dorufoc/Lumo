<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { call } from '@/api/client'
import type { Dashboard } from '@/api/types'
import { useSessionStore } from '@/stores/session'

const router = useRouter()
const session = useSessionStore()

const dash = ref<Dashboard | null>(null)
const loading = ref(true)
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    dash.value = await call<Dashboard>('DashboardGet', {
      workspace_id: session.workspaceId,
      user_id: session.userId,
    })
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

onMounted(load)

const accuracyText = (rate: number) => `${Math.round(rate * 100)}%`
const accuracyClass = (rate: number) => (rate >= 0.7 ? 'text-success' : rate >= 0.5 ? 'text-warning' : 'text-error')
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>今日概览</h1>
        <div class="subtitle">{{ session.workspace?.name }} · 本地学习空间</div>
      </div>
      <RouterLink to="/practice" class="btn btn-primary">开始练习</RouterLink>
    </div>

    <div v-if="error" class="error-banner">
      <span>{{ error }}</span>
      <button class="btn btn-sm" @click="load">重试</button>
    </div>
    <div v-if="loading" class="loading"><div class="spinner"></div></div>

    <template v-if="dash">
      <div class="grid grid-4 mb-4">
        <div class="stat-card clickable" @click="router.push('/plan')">
          <div class="stat-value">{{ dash.today_tasks.total }}</div>
          <div class="stat-label">今日任务（已完成 {{ dash.today_tasks.completed }}）</div>
        </div>
        <div class="stat-card clickable" @click="router.push('/review')">
          <div class="stat-value" :class="{ 'text-error': dash.due_reviews > 0 }">{{ dash.due_reviews }}</div>
          <div class="stat-label">到期复习</div>
        </div>
        <div class="stat-card">
          <div class="stat-value">🔥 {{ dash.streak_days }}</div>
          <div class="stat-label">连续学习天数</div>
        </div>
        <div class="stat-card">
          <div class="stat-value" :class="accuracyClass(dash.recent_accuracy.rate)">
            {{ dash.recent_accuracy.total > 0 ? accuracyText(dash.recent_accuracy.rate) : '–' }}
          </div>
          <div class="stat-label">近 7 天正确率（{{ dash.recent_accuracy.correct }}/{{ dash.recent_accuracy.total }}）</div>
        </div>
      </div>

      <div class="grid grid-2">
        <div class="card">
          <div class="card-title">AI 建议</div>
          <p class="text-secondary" style="white-space: pre-wrap">{{ dash.ai_advice }}</p>
        </div>
        <div class="card">
          <div class="card-title">薄弱知识点</div>
          <div v-if="dash.weak_knowledge.length === 0" class="empty" style="padding: var(--space-4)">
            暂无薄弱知识点，继续保持！
          </div>
          <table v-else class="table">
            <thead><tr><th>知识点</th><th>错题数</th></tr></thead>
            <tbody>
              <tr v-for="wk in dash.weak_knowledge" :key="wk.knowledge_id">
                <td>{{ wk.name }}</td>
                <td><span class="badge badge-error">{{ wk.wrong_count }}</span></td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <div v-if="dash.has_empty_library" class="card" style="border-color: var(--color-warning)">
        <div class="card-title">🚀 快速开始</div>
        <p class="text-secondary mb-3">题库还是空的。导入一份题库（支持 Markdown / JSON / 纯文本），或手动创建题目，即可开始练习闭环。</p>
        <RouterLink to="/library" class="btn btn-primary">去导入题库</RouterLink>
      </div>
    </template>
  </div>
</template>
