<script setup lang="ts">
// 内容社区帖子详情视图（完整设计文档 4.20 社区 / Todo 35）。
// body_md 渲染（本地内容直接文本展示，不做 HTML 注入）+ 点赞；不存在 → NOT_FOUND 提示。
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ApiException, localizedMessageOf } from '@/api/client'
import { communityPostGet, communityPostLike } from '@/api/community'
import type { CommunityPost } from '@/api/types'
import { useSessionStore } from '@/stores/session'

const session = useSessionStore()
const route = useRoute()
const router = useRouter()

const loading = ref(true)
const busy = ref(false)
const error = ref('')
const notFound = ref(false)
const post = ref<CommunityPost | null>(null)

async function refresh() {
  if (!session.workspaceId) return
  loading.value = true
  error.value = ''
  notFound.value = false
  try {
    post.value = await communityPostGet({
      workspace_id: session.workspaceId,
      post_id: String(route.params.postId),
    })
  } catch (e) {
    if (e instanceof ApiException && e.code === 'NOT_FOUND') {
      notFound.value = true
    } else {
      error.value = localizedMessageOf(e)
    }
  } finally {
    loading.value = false
  }
}

async function like() {
  if (!session.workspaceId || !post.value || busy.value) return
  busy.value = true
  error.value = ''
  try {
    const res = await communityPostLike({ workspace_id: session.workspaceId, post_id: post.value.id })
    post.value = { ...post.value, likes: res.likes }
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    busy.value = false
  }
}

function goBack() {
  router.push({ name: 'community' })
}

onMounted(refresh)
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('community.title') }}</h1>
        <div class="subtitle">{{ $t('community.subtitle') }}</div>
      </div>
      <button class="btn" @click="goBack">{{ $t('community.back') }}</button>
    </div>

    <div v-if="error" class="error-banner">{{ error }}</div>
    <div v-if="notFound" class="empty">{{ $t('community.notFound') }}</div>

    <div v-if="loading" class="loading"><div class="spinner"></div></div>

    <div v-else-if="post" class="card detail-card">
      <h2 class="detail-title">{{ post.title }}</h2>
      <div class="detail-meta">
        <span>{{ $t('community.author') }}: {{ post.author_user_id || '-' }}</span>
        <span>{{ post.created_at }}</span>
      </div>
      <div class="detail-body">{{ post.body_md }}</div>
      <div class="detail-actions">
        <span class="like-count">👍 {{ post.likes }}</span>
        <button class="btn btn-primary btn-sm" :disabled="busy" @click="like">
          {{ $t('community.like') }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.detail-card {
  padding: var(--space-4);
}

.detail-title {
  margin: 0 0 var(--space-1);
}

.detail-meta {
  display: flex;
  gap: var(--space-3);
  font-size: var(--text-xs);
  color: var(--text-muted);
  flex-wrap: wrap;
  padding-bottom: var(--space-3);
  border-bottom: 1px solid var(--border);
  margin-bottom: var(--space-4);
}

.detail-body {
  white-space: pre-wrap;
  word-break: break-word;
  color: var(--text);
  font-size: var(--text-sm);
  line-height: 1.7;
  margin-bottom: var(--space-4);
}

.detail-actions {
  display: flex;
  align-items: center;
  gap: var(--space-3);
}

.like-count {
  font-size: var(--text-sm);
  color: var(--text-secondary);
}
</style>
