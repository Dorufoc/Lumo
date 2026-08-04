<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref } from 'vue'
import { call, openEventStream, type Citation } from '@/api/client'
import type { AgentMemory, AgentSession } from '@/api/types'
import { useSessionStore } from '@/stores/session'

const session = useSessionStore()

interface Msg {
  role: 'user' | 'assistant'
  content: string
  citations: Citation[]
  streaming?: boolean
  error?: string
}

const agentType = ref<'tutor' | 'diagnoser' | 'router'>('tutor')
const messages = ref<Msg[]>([])
const input = ref('')
const sending = ref(false)
const error = ref('')
const sessionId = ref('')
const memories = ref<AgentMemory[]>([])
let stream: { close: () => void } | null = null

const chatList = ref<HTMLElement | null>(null)

async function ensureSession() {
  if (sessionId.value) return
  const s = await call<AgentSession>('AgentChatCreate', {
    workspace_id: session.workspaceId,
    user_id: session.userId,
    agent: agentType.value,
    context: 'Lumo AI 学习辅导',
  })
  sessionId.value = s.id
  // 历史消息
  const got = await call<AgentSession>('AgentSessionGet', {
    workspace_id: session.workspaceId,
    session_id: s.id,
  })
  messages.value = (got.messages ?? [])
    .filter((m) => m.role === 'user' || m.role === 'assistant')
    .map((m) => ({ role: m.role as 'user' | 'assistant', content: m.content, citations: [] }))
}

async function switchAgent() {
  sessionId.value = ''
  messages.value = []
  await ensureSession()
}

async function send() {
  const text = input.value.trim()
  if (!text || sending.value) return
  input.value = ''
  error.value = ''
  try {
    await ensureSession()
    messages.value.push({ role: 'user', content: text, citations: [] })
    const assistant: Msg = { role: 'assistant', content: '', citations: [], streaming: true }
    messages.value.push(assistant)
    sending.value = true
    await scrollToBottom()

    const req = await call<{ request_id: string; session_id: string }>('AgentChatSend', {
      workspace_id: session.workspaceId,
      session_id: sessionId.value,
      message: text,
    })

    stream = openEventStream(req.request_id, sessionId.value, {
      onDelta: (delta, citations) => {
        assistant.content += delta
        if (citations.length > 0) assistant.citations = citations
        void scrollToBottom()
      },
      onCompleted: () => {
        assistant.streaming = false
        sending.value = false
        stream = null
        void scrollToBottom()
      },
      onError: (err) => {
        assistant.streaming = false
        assistant.error = err.message
        sending.value = false
        stream = null
      },
    })
  } catch (e) {
    sending.value = false
    const last = messages.value[messages.value.length - 1]
    if (last?.role === 'assistant' && last.streaming) {
      last.streaming = false
      last.error = (e as Error).message
    } else {
      error.value = (e as Error).message
    }
  }
}

async function cancel() {
  stream?.close()
  stream = null
  sending.value = false
  const last = messages.value[messages.value.length - 1]
  if (last?.streaming) {
    last.streaming = false
    last.error = '已取消'
  }
}

async function scrollToBottom() {
  await nextTick()
  chatList.value?.scrollTo({ top: chatList.value.scrollHeight })
}

async function loadMemories() {
  try {
    memories.value = await call<AgentMemory[]>('AgentMemoryList', {
      workspace_id: session.workspaceId,
      user_id: session.userId,
    })
  } catch {
    memories.value = []
  }
}

async function deleteMemory(m: AgentMemory) {
  try {
    await call('AgentMemoryDelete', {
      workspace_id: session.workspaceId,
      memory_id: m.id,
      version: m.version,
    })
    await loadMemories()
  } catch (e) {
    error.value = (e as Error).message
  }
}

onBeforeUnmount(() => {
  stream?.close()
})
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>AI Tutor</h1>
        <div class="subtitle">苏格拉底式引导 · 未配置模型时自动降级为引导模板</div>
      </div>
    </div>

    <div v-if="error" class="error-banner">{{ error }}</div>

    <div class="grid" style="grid-template-columns: 1fr 300px">
      <div class="card" style="display: flex; flex-direction: column; min-height: 60vh">
        <div class="tabs">
          <div class="tab" :class="{ active: agentType === 'tutor' }" @click="agentType = 'tutor'; switchAgent()">Tutor 讲解</div>
          <div class="tab" :class="{ active: agentType === 'diagnoser' }" @click="agentType = 'diagnoser'; switchAgent()">错因诊断</div>
          <div class="tab" :class="{ active: agentType === 'router' }" @click="agentType = 'router'; switchAgent()">通用助手</div>
        </div>

        <div ref="chatList" class="chat-list grow" style="overflow-y: auto; padding: var(--space-2)">
          <div v-if="messages.length === 0" class="empty" style="flex: 1">
            <div class="empty-icon">🤖</div>
            <p>把题目或困惑发给我，我会用引导的方式帮你思考，而不是直接给答案。</p>
            <p class="hint">示例：「这道选择题我不理解，请引导我思考」</p>
          </div>
          <div v-for="(m, i) in messages" :key="i" class="chat-msg" :class="m.role">
            <template v-if="m.role === 'assistant'">
              <span :class="{ 'stream-cursor': m.streaming }">{{ m.content || (m.streaming ? '思考中…' : '') }}</span>
              <span v-for="(c, ci) in m.citations" :key="ci" class="citation">📎 {{ c.document_name }}{{ c.section ? ' · ' + c.section : '' }}</span>
              <div v-if="m.error" class="error-text mt-2">⚠ {{ m.error }}</div>
            </template>
            <template v-else>{{ m.content }}</template>
          </div>
        </div>

        <div class="flex gap-2 mt-3">
          <textarea
            v-model="input"
            class="textarea grow"
            style="min-height: 60px"
            placeholder="输入问题…（Enter 发送，Shift+Enter 换行）"
            @keydown.enter.exact.prevent="send"
          ></textarea>
          <div class="flex" style="flex-direction: column; gap: var(--space-2)">
            <button class="btn btn-primary" :disabled="sending || !input.trim()" @click="send">
              {{ sending ? '生成中…' : '发送' }}
            </button>
            <button v-if="sending" class="btn btn-danger btn-sm" @click="cancel">停止</button>
          </div>
        </div>
      </div>

      <div>
        <div class="card">
          <div class="card-title">长期记忆</div>
          <div v-if="memories.length === 0" class="empty" style="padding: var(--space-3)">
            <p class="hint">暂无记忆。配置 AI 后，系统会保存你的学习偏好摘要（可随时删除）。</p>
          </div>
          <div v-for="m in memories" :key="m.id" class="flex-between gap-2 mb-2" style="border-bottom: 1px solid var(--border); padding-bottom: var(--space-2)">
            <div>
              <span class="badge badge-primary">{{ m.memory_type }}</span>
              <div class="hint mt-1">{{ m.summary }}</div>
            </div>
            <button class="btn btn-sm btn-danger" @click="deleteMemory(m)">删除</button>
          </div>
          <button class="btn btn-sm mt-3" @click="loadMemories">刷新</button>
        </div>
      </div>
    </div>
  </div>
</template>
