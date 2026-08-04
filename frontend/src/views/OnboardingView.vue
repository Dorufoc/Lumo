<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { useSessionStore } from '@/stores/session'

const router = useRouter()
const session = useSessionStore()

const name = ref('')
const creating = ref(false)
const error = ref('')

async function createWorkspace() {
  if (!name.value.trim()) {
    error.value = '请输入工作区名称'
    return
  }
  creating.value = true
  error.value = ''
  try {
    await session.createWorkspace(name.value.trim())
    router.push('/dashboard')
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    creating.value = false
  }
}

async function selectWorkspace(id: string) {
  try {
    await session.activate(id)
    router.push('/dashboard')
  } catch (e) {
    error.value = (e as Error).message
  }
}
</script>

<template>
  <div class="onboarding">
    <div class="card onboarding-card">
      <h1>👋 欢迎使用 Lumo AI</h1>
      <p class="subtitle">本地优先的智能刷题平台：目标 → 练习 → 判分 → 错题复习，离线也能完整使用。</p>

      <div v-if="error" class="error-banner">{{ error }}</div>

      <div class="field">
        <label for="ws-name">新建学习空间</label>
        <input id="ws-name" v-model="name" class="input" placeholder="例如：高等数学期末复习" maxlength="120" @keyup.enter="createWorkspace" />
      </div>
      <button class="btn btn-primary" :disabled="creating" @click="createWorkspace">
        {{ creating ? '创建中…' : '创建并开始' }}
      </button>

      <template v-if="session.workspaces.length > 0">
        <div class="divider"></div>
        <h3>已有学习空间</h3>
        <div class="ws-list">
          <button v-for="ws in session.workspaces" :key="ws.id" class="ws-item" @click="selectWorkspace(ws.id)">
            <span class="ws-name">{{ ws.name }}</span>
            <span class="badge">{{ ws.owner_type }}</span>
          </button>
        </div>
      </template>
    </div>
  </div>
</template>

<style scoped>
.onboarding {
  height: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
  background: linear-gradient(160deg, #eff6ff 0%, #f8fafc 60%);
}
.onboarding-card {
  width: 420px;
  padding: var(--space-6);
}
.divider {
  border-top: 1px solid var(--border);
  margin: var(--space-5) 0 var(--space-4);
}
.ws-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}
.ws-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 10px 14px;
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  background: var(--bg-surface);
  cursor: pointer;
  text-align: left;
}
.ws-item:hover {
  border-color: var(--color-primary);
}
</style>
