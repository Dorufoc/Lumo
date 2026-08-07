<script setup lang="ts">
// 内容社区广场视图（完整设计文档 4.20 社区 / Todo 35）。
// 发布表单（标题 + Markdown 正文，后端强制安全扫描）→ 帖子列表（标题/作者/时间/点赞数/点赞按钮）。
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { localizedMessageOf } from '@/api/client'
import { communityPostCreate, communityPostLike, communityPostList } from '@/api/community'
import type { CommunityPost } from '@/api/types'
import { useSessionStore } from '@/stores/session'

const session = useSessionStore()
const router = useRouter()

const loading = ref(false)
const busy = ref(false)
const error = ref('')
const info = ref('')
const posts = ref<CommunityPost[]>([])
const title = ref('')
const body = ref('')
const likeBusy = ref<Record<string, boolean>>({})

async function refresh() {
  if (!session.workspaceId) return
  loading.value = true
  error.value = ''
  try {
    posts.value = await communityPostList({ workspace_id: session.workspaceId })
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    loading.value = false
  }
}

async function submitPublish() {
  if (!session.workspaceId) return
  const t = title.value.trim()
  const b = body.value.trim()
  if (!t || !b) return
  busy.value = true
  error.value = ''
  info.value = ''
  try {
    await communityPostCreate({
      workspace_id: session.workspaceId,
      author_user_id: session.userId,
      title: t,
      body_md: b,
    })
    title.value = ''
    body.value = ''
    info.value = 'community.publishSuccess'
    await refresh()
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    busy.value = false
  }
}

async function like(post: CommunityPost) {
  if (!session.workspaceId || likeBusy.value[post.id]) return
  likeBusy.value = { ...likeBusy.value, [post.id]: true }
  error.value = ''
  try {
    const res = await communityPostLike({ workspace_id: session.workspaceId, post_id: post.id })
    const idx = posts.value.findIndex((p) => p.id === post.id)
    if (idx >= 0) posts.value[idx] = { ...posts.value[idx], likes: res.likes }
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    likeBusy.value = { ...likeBusy.value, [post.id]: false }
  }
}

function openDetail(post: CommunityPost) {
  router.push({ name: 'communityDetail', params: { postId: post.id } })
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
    </div>

    <div v-if="error" class="error-banner">{{ error }}</div>
    <div v-if="info" class="offline-banner">{{ $t(info) }}</div>

    <!-- 发布表单 -->
    <div class="card publish-card">
      <h3>{{ $t('community.publish') }}</h3>
      <div class="field">
        <input v-model="title" class="input" type="text" :placeholder="$t('community.titlePlaceholder')" />
      </div>
      <div class="field">
        <textarea v-model="body" class="input post-body-input" rows="5" :placeholder="$t('community.bodyPlaceholder')" />
      </div>
      <button
        class="btn btn-primary"
        :disabled="busy || !title.trim() || !body.trim()"
        @click="submitPublish"
      >
        {{ busy ? $t('community.publishing') : $t('community.publish') }}
      </button>
    </div>

    <div v-if="loading" class="loading"><div class="spinner"></div></div>
    <div v-else-if="posts.length === 0" class="empty">{{ $t('community.empty') }}</div>

    <div v-else class="feed">
      <div v-for="post in posts" :key="post.id" class="card post-card">
        <div class="post-head" @click="openDetail(post)">
          <h3 class="post-title">{{ post.title }}</h3>
          <div class="post-meta">
            <span>{{ $t('community.author') }}: {{ post.author_user_id || '-' }}</span>
            <span>{{ post.created_at }}</span>
          </div>
        </div>
        <div class="post-actions">
          <span class="like-count">👍 {{ post.likes }}</span>
          <button class="btn btn-sm" :disabled="likeBusy[post.id]" @click="like(post)">
            {{ $t('community.like') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.publish-card {
  padding: var(--space-4);
  margin-bottom: var(--space-4);
}

.post-body-input {
  resize: vertical;
}

.feed {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}

.post-card {
  padding: var(--space-4);
}

.post-head {
  cursor: pointer;
}

.post-title {
  margin: 0 0 var(--space-1);
}

.post-meta {
  display: flex;
  gap: var(--space-3);
  font-size: var(--text-xs);
  color: var(--text-muted);
  flex-wrap: wrap;
}

.post-actions {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  margin-top: var(--space-3);
}

.like-count {
  font-size: var(--text-sm);
  color: var(--text-secondary);
}
</style>
