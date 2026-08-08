<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { localizedMessageOf } from '@/api/client'
import { useI18nStore } from '@/stores/i18n'
import { useSessionStore } from '@/stores/session'

const router = useRouter()
const session = useSessionStore()
const i18n = useI18nStore()

const name = ref('')
const creating = ref(false)
const error = ref('')

async function createWorkspace() {
  if (!name.value.trim()) {
    error.value = i18n.t('onboarding.nameRequired')
    return
  }
  creating.value = true
  error.value = ''
  try {
    await session.createWorkspace(name.value.trim())
    router.push('/dashboard')
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    creating.value = false
  }
}

async function selectWorkspace(id: string) {
  try {
    await session.activate(id)
    router.push('/dashboard')
  } catch (e) {
    error.value = localizedMessageOf(e)
  }
}
</script>

<template>
  <div class="onboarding">
    <div class="card onboarding-card">
      <h1>{{ $t('onboarding.welcome') }}</h1>
      <p class="subtitle">{{ $t('onboarding.subtitle') }}</p>

      <div v-if="error" class="error-banner">{{ error }}</div>

      <div class="field">
        <label for="ws-name">{{ $t('onboarding.newWorkspace') }}</label>
        <input id="ws-name" v-model="name" class="input" :placeholder="$t('onboarding.wsPlaceholder')" maxlength="120" @keyup.enter="createWorkspace" />
      </div>
      <button class="btn btn-primary" :disabled="creating" @click="createWorkspace">
        {{ creating ? $t('common.creating') : $t('onboarding.createAndStart') }}
      </button>

      <template v-if="session.workspaces.length > 0">
        <div class="divider"></div>
        <h3>{{ $t('onboarding.existing') }}</h3>
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
  background: var(--gradient-soft), var(--bg-glow), var(--bg);
}
.onboarding-card {
  width: min(420px, 92vw);
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
  background: var(--color-bg-glass);
  cursor: pointer;
  text-align: left;
  transition: border-color 0.2s var(--ease-out), transform 0.2s var(--ease-out);
}
.ws-item:hover {
  border-color: var(--color-primary);
  transform: translateY(-1px);
}
</style>
