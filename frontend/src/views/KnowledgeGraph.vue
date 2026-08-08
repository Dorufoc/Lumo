<script setup lang="ts">
// 知识图谱视图（完整设计文档 4.19）：自绘 Canvas 图谱 + 掌握度着色 + 交互。
// 无第三方图表库；Canvas 渲染失败时降级为缩进树列表。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { localizedMessageOf } from '@/api/client'
import { knowledgeGraphGet, masteryExplain } from '@/api/knowledgeGraph'
import type { KnowledgeGraph, KnowledgeGraphNode, MasteryExplanation } from '@/api/types'
import { useI18nStore } from '@/stores/i18n'
import { usePracticeStore } from '@/stores/practice'
import { useSessionStore } from '@/stores/session'

const i18n = useI18nStore()
const session = useSessionStore()
const practice = usePracticeStore()
const router = useRouter()

const containerEl = ref<HTMLDivElement | null>(null)
const canvasEl = ref<HTMLCanvasElement | null>(null)

const graph = ref<KnowledgeGraph | null>(null)
const loading = ref(false)
const error = ref('')
const canvasMode = ref(true)

const searchQuery = ref('')
const hoverId = ref<string | null>(null)
const selectedId = ref<string | null>(null)
const detail = ref<MasteryExplanation | null>(null)
const detailLoading = ref(false)
const detailError = ref('')
const practiceBusy = ref(false)
const practiceInfo = ref('')

// ---------- 布局 ----------
const NODE_R = 22
const COL_W = 200
const ROW_H = 88

interface Point {
  x: number
  y: number
}

const transform = ref<{ x: number; y: number; scale: number }>({ x: 40, y: 40, scale: 1 })

const layout = computed(() => {
  const nodes = graph.value?.nodes ?? []
  const edges = graph.value?.edges ?? []
  const byId = new Map(nodes.map((n) => [n.id, n]))
  const childrenOf = new Map<string, string[]>()
  const hasParentEdge = new Set<string>()
  for (const e of edges) {
    if (e.type !== 'parent') continue
    hasParentEdge.add(e.to)
    const arr = childrenOf.get(e.from) ?? []
    arr.push(e.to)
    childrenOf.set(e.from, arr)
  }
  const roots = nodes.filter((n) => !n.parent_id && !hasParentEdge.has(n.id))

  const depthOf = new Map<string, number>()
  const visited = new Set<string>()
  const assign = (id: string, depth: number) => {
    if (visited.has(id)) return
    visited.add(id)
    depthOf.set(id, depth)
    for (const c of childrenOf.get(id) ?? []) assign(c, depth + 1)
  }
  for (const r of roots) assign(r.id, 0)
  for (const n of nodes) if (!visited.has(n.id)) assign(n.id, 0)

  const byDepth = new Map<number, KnowledgeGraphNode[]>()
  for (const n of nodes) {
    const d = depthOf.get(n.id) ?? 0
    const arr = byDepth.get(d) ?? []
    arr.push(n)
    byDepth.set(d, arr)
  }
  const pos = new Map<string, Point>()
  for (const [d, arr] of byDepth) {
    arr.sort((a, b) => a.name.localeCompare(b.name, 'zh'))
    arr.forEach((n, i) => {
      pos.set(n.id, { x: d * COL_W, y: (i - (arr.length - 1) / 2) * ROW_H })
    })
  }
  return { pos, childrenOf, roots, byId }
})

// ---------- 掌握度着色 ----------
// Canvas 2D 无法直接消费 CSS var()：每次绘制时读取已解析的 token 值，主题切换后自动生效。
function tokenColor(name: string): string {
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim()
  return v || 'black'
}

function masteryColor(n: KnowledgeGraphNode): string {
  if (n.mastery === undefined) return tokenColor('--color-disabled') // 未学习
  if (n.mastery >= 0.8) return tokenColor('--color-success') // 已掌握
  if (n.mastery >= 0.4) return tokenColor('--color-warning') // 学习中
  return tokenColor('--color-error') // 薄弱
}

function masteryText(n: KnowledgeGraphNode): string {
  return n.mastery === undefined ? '' : `${Math.round(n.mastery * 100)}%`
}

// ---------- 画布渲染 ----------
function truncate(s: string, max: number): string {
  return s.length > max ? s.slice(0, max) + '…' : s
}

function draw() {
  const canvas = canvasEl.value
  if (!canvas || !graph.value) return
  const ctx = canvas.getContext('2d')
  if (!ctx) {
    canvasMode.value = false
    return
  }
  const dpr = window.devicePixelRatio || 1
  const cw = canvas.clientWidth
  const ch = canvas.clientHeight
  if (canvas.width !== Math.round(cw * dpr) || canvas.height !== Math.round(ch * dpr)) {
    canvas.width = Math.round(cw * dpr)
    canvas.height = Math.round(ch * dpr)
  }
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
  ctx.clearRect(0, 0, cw, ch)

  const { pos } = layout.value
  const t = transform.value
  const q = searchQuery.value.trim().toLowerCase()
  ctx.save()
  ctx.translate(t.x, t.y)
  ctx.scale(t.scale, t.scale)

  // 边
  for (const e of graph.value.edges ?? []) {
    const a = pos.get(e.from)
    const b = pos.get(e.to)
    if (!a || !b) continue
    ctx.globalAlpha = 0.55
    ctx.strokeStyle = tokenColor('--border')
    ctx.lineWidth = 1.5 / t.scale
    ctx.beginPath()
    ctx.moveTo(a.x, a.y)
    ctx.quadraticCurveTo((a.x + b.x) / 2, (a.y + b.y) / 2 + 26 / t.scale, b.x, b.y)
    ctx.stroke()
    ctx.globalAlpha = 1
  }

  // 节点
  for (const n of graph.value.nodes) {
    const p = pos.get(n.id)
    if (!p) continue
    const matched = q === '' || n.name.toLowerCase().includes(q)
    const alpha = q !== '' && !matched ? 0.22 : 1
    const isHover = hoverId.value === n.id
    const isSel = selectedId.value === n.id
    const isHighlight = q !== '' && matched

    ctx.globalAlpha = alpha
    ctx.beginPath()
    ctx.arc(p.x, p.y, NODE_R, 0, Math.PI * 2)
    ctx.fillStyle = masteryColor(n)
    ctx.fill()
    ctx.lineWidth = (isHover || isSel || isHighlight ? 3 : 1.5) / t.scale
    ctx.strokeStyle = isHighlight
      ? tokenColor('--color-primary')
      : isHover || isSel
        ? tokenColor('--text')
        : tokenColor('--border-strong')
    if (!isHighlight && !isHover && !isSel) ctx.globalAlpha = alpha * 0.4
    ctx.stroke()
    ctx.globalAlpha = alpha
    if (isHighlight) {
      ctx.beginPath()
      ctx.arc(p.x, p.y, NODE_R + 4 / t.scale, 0, Math.PI * 2)
      ctx.globalAlpha = alpha * 0.55
      ctx.strokeStyle = tokenColor('--color-primary')
      ctx.lineWidth = 2 / t.scale
      ctx.stroke()
      ctx.globalAlpha = alpha
    }
    const label = masteryText(n)
    if (label) {
      ctx.fillStyle = tokenColor('--on-accent')
      ctx.font = `600 ${11 / t.scale}px sans-serif`
      ctx.textAlign = 'center'
      ctx.textBaseline = 'middle'
      ctx.fillText(label, p.x, p.y + 1)
    }
    ctx.globalAlpha = alpha
    ctx.fillStyle = tokenColor('--text-secondary')
    ctx.font = `500 ${12 / t.scale}px sans-serif`
    ctx.textAlign = 'center'
    ctx.textBaseline = 'top'
    ctx.fillText(truncate(n.name, 9), p.x, p.y + NODE_R + 7 / t.scale)
    ctx.globalAlpha = 1
  }
  ctx.restore()
}

function fitView() {
  const canvas = canvasEl.value
  if (!canvas || !graph.value || graph.value.nodes.length === 0) return
  const { pos } = layout.value
  let minX = Infinity
  let minY = Infinity
  let maxX = -Infinity
  let maxY = -Infinity
  for (const n of graph.value.nodes) {
    const p = pos.get(n.id)
    if (!p) continue
    minX = Math.min(minX, p.x)
    maxX = Math.max(maxX, p.x)
    minY = Math.min(minY, p.y)
    maxY = Math.max(maxY, p.y)
  }
  if (!isFinite(minX)) return
  const pad = 70
  const bbW = Math.max(1, maxX - minX) + pad * 2
  const bbH = Math.max(1, maxY - minY) + pad * 2
  const cw = canvas.clientWidth
  const ch = canvas.clientHeight
  const scale = Math.min(1, Math.min(cw / bbW, ch / bbH))
  transform.value = {
    x: cw / 2 - ((minX + maxX) / 2) * scale,
    y: ch / 2 - ((minY + maxY) / 2) * scale,
    scale,
  }
  draw()
}

function clamp(v: number, lo: number, hi: number): number {
  return Math.min(hi, Math.max(lo, v))
}

function onWheel(e: WheelEvent) {
  const canvas = canvasEl.value
  if (!canvas) return
  const rect = canvas.getBoundingClientRect()
  const mx = e.clientX - rect.left
  const my = e.clientY - rect.top
  const t = transform.value
  const factor = e.deltaY < 0 ? 1.1 : 1 / 1.1
  const ns = clamp(t.scale * factor, 0.15, 4)
  const k = ns / t.scale
  transform.value = { x: mx - (mx - t.x) * k, y: my - (my - t.y) * k, scale: ns }
  draw()
}

interface DragState {
  startX: number
  startY: number
  tX: number
  tY: number
  moved: boolean
}

let drag: DragState | null = null

function hitTest(clientX: number, clientY: number): string | null {
  const canvas = canvasEl.value
  if (!canvas || !graph.value) return null
  const rect = canvas.getBoundingClientRect()
  const mx = clientX - rect.left
  const my = clientY - rect.top
  const t = transform.value
  const wx = (mx - t.x) / t.scale
  const wy = (my - t.y) / t.scale
  const r = NODE_R + 8 / t.scale
  let best: string | null = null
  let bestD = Infinity
  for (const n of graph.value.nodes) {
    const p = layout.value.pos.get(n.id)
    if (!p) continue
    const d = Math.hypot(p.x - wx, p.y - wy)
    if (d <= r && d < bestD) {
      bestD = d
      best = n.id
    }
  }
  return best
}

function onPointerDown(e: PointerEvent) {
  const canvas = canvasEl.value
  if (!canvas) return
  canvas.setPointerCapture(e.pointerId)
  drag = { startX: e.clientX, startY: e.clientY, tX: transform.value.x, tY: transform.value.y, moved: false }
}

function onPointerMove(e: PointerEvent) {
  if (drag) {
    if (Math.abs(e.clientX - drag.startX) + Math.abs(e.clientY - drag.startY) > 5) drag.moved = true
    if (drag.moved) {
      transform.value = { ...transform.value, x: drag.tX + e.clientX - drag.startX, y: drag.tY + e.clientY - drag.startY }
      draw()
    }
    return
  }
  const hit = hitTest(e.clientX, e.clientY)
  if (hit !== hoverId.value) {
    hoverId.value = hit
    draw()
  }
}

function onPointerUp() {
  if (!drag) return
  const moved = drag.moved
  drag = null
  if (!moved) {
    void selectNode(hoverId.value)
  }
}

async function selectNode(id: string | null) {
  selectedId.value = id
  detail.value = null
  detailError.value = ''
  practiceInfo.value = ''
  if (!id) return
  detailLoading.value = true
  try {
    detail.value = await masteryExplain({ user_id: session.userId, knowledge_id: id })
  } catch (e) {
    detailError.value = localizedMessageOf(e)
  } finally {
    detailLoading.value = false
    draw()
  }
}

// ---------- 搜索 ----------
const matchedCount = computed(() => {
  const q = searchQuery.value.trim().toLowerCase()
  if (!q || !graph.value) return 0
  return graph.value.nodes.filter((n) => n.name.toLowerCase().includes(q)).length
})

watch(searchQuery, (q) => {
  if (!q.trim()) {
    hoverId.value = null
    draw()
    return
  }
  if (!graph.value) return
  const hits = graph.value.nodes.filter((n) => n.name.toLowerCase().includes(q.trim().toLowerCase()))
  if (hits.length === 1) void selectNode(hits[0].id)
  draw()
})

// ---------- 数据加载 ----------
async function load() {
  loading.value = true
  error.value = ''
  try {
    graph.value = await knowledgeGraphGet({ workspace_id: session.workspaceId, user_id: session.userId })
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    loading.value = false
    if (canvasMode.value) fitView()
  }
}

// ---------- 降级：树列表 ----------
interface TreeRow {
  node: KnowledgeGraphNode
  depth: number
}

const treeRows = computed<TreeRow[]>(() => {
  const rows: TreeRow[] = []
  if (!graph.value) return rows
  const visited = new Set<string>()
  const walk = (id: string, depth: number) => {
    if (visited.has(id)) return
    visited.add(id)
    const n = layout.value.byId.get(id)
    if (!n) return
    rows.push({ node: n, depth })
    for (const c of layout.value.childrenOf.get(id) ?? []) walk(c, depth + 1)
  }
  for (const r of layout.value.roots) walk(r.id, 0)
  for (const n of graph.value.nodes) if (!visited.has(n.id)) walk(n.id, 0)
  return rows
})

// ---------- 从此节点开始练习 ----------
async function practiceFromNode() {
  const node = graph.value?.nodes.find((n) => n.id === selectedId.value)
  if (!node) return
  practiceBusy.value = true
  practiceInfo.value = ''
  try {
    await practice.loadLibrary()
    const ids = (practice.library?.items ?? [])
      .filter((q) => q.current_version?.payload.knowledge_ids?.includes(node.id))
      .map((q) => q.id)
    if (ids.length === 0) {
      practiceInfo.value = i18n.t('knowledgeGraph.practiceNoQuestions')
      return
    }
    const s = await practice.start(ids)
    router.push(`/practice/${s.id}`)
  } catch (e) {
    detailError.value = localizedMessageOf(e)
  } finally {
    practiceBusy.value = false
  }
}

let ro: ResizeObserver | null = null

onMounted(() => {
  void load()
  const el = containerEl.value
  if (el && typeof ResizeObserver !== 'undefined') {
    ro = new ResizeObserver(() => {
      if (canvasMode.value) fitView()
    })
    ro.observe(el)
  }
})

onBeforeUnmount(() => {
  ro?.disconnect()
})
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('knowledgeGraph.title') }}</h1>
        <div class="subtitle">{{ $t('knowledgeGraph.subtitle') }}</div>
      </div>
      <button class="btn btn-sm" :disabled="loading" @click="load">{{ $t('common.refresh') }}</button>
    </div>

    <div v-if="error" class="error-banner">{{ error }}</div>
    <div v-if="graph?.truncated" class="offline-banner">{{ $t('knowledgeGraph.truncated') }}</div>

    <!-- 工具栏 -->
    <div v-if="graph && graph.nodes.length > 0" class="toolbar">
      <input v-model="searchQuery" class="input" style="max-width: 280px" :placeholder="$t('knowledgeGraph.searchPlaceholder')" />
      <span v-if="searchQuery.trim()" class="text-secondary text-sm">
        {{ matchedCount }} {{ $t('knowledgeGraph.matchCount') }}
      </span>
      <span class="legend">
        <span class="legend-item"><span class="dot dot-mastered"></span>{{ $t('knowledgeGraph.legendMastered') }}</span>
        <span class="legend-item"><span class="dot dot-learning"></span>{{ $t('knowledgeGraph.legendLearning') }}</span>
        <span class="legend-item"><span class="dot dot-weak"></span>{{ $t('knowledgeGraph.legendWeak') }}</span>
        <span class="legend-item"><span class="dot dot-unlearned"></span>{{ $t('knowledgeGraph.legendUnlearned') }}</span>
      </span>
    </div>

    <div v-if="loading" class="loading"><div class="spinner"></div></div>

    <div v-else-if="graph && graph.nodes.length === 0" class="card">
      <div class="empty">
        <div class="empty-icon">🕸️</div>
        <p>{{ $t('knowledgeGraph.empty') }}</p>
      </div>
    </div>

    <!-- Canvas 图谱 -->
    <div v-else-if="canvasMode" ref="containerEl" class="canvas-shell" @wheel.prevent="onWheel" @pointerdown="onPointerDown" @pointermove="onPointerMove" @pointerup="onPointerUp" @pointerleave="drag = null">
      <canvas ref="canvasEl" class="graph-canvas"></canvas>
      <div class="canvas-hint">{{ $t('knowledgeGraph.zoomHint') }}</div>
    </div>

    <!-- 降级：树列表 -->
    <div v-else class="card tree-card">
      <div class="offline-banner">{{ $t('knowledgeGraph.errorFallback') }}</div>
      <div
        v-for="row in treeRows"
        :key="row.node.id"
        class="tree-row"
        :style="{ paddingLeft: row.depth * 22 + 8 + 'px' }"
        :class="{ selected: selectedId === row.node.id }"
        @click="selectNode(row.node.id)"
      >
        <span class="dot" :style="{ background: masteryColor(row.node) }"></span>
        <span class="tree-name">{{ row.node.name }}</span>
        <span v-if="row.node.mastery !== undefined" class="tree-mastery">{{ Math.round(row.node.mastery * 100) }}%</span>
        <span v-else class="tree-mastery muted">{{ $t('knowledgeGraph.notLearned') }}</span>
      </div>
    </div>

    <!-- 节点详情抽屉 -->
    <div v-if="selectedId" class="detail-panel">
      <div class="detail-head">
        <div class="card-title" style="margin: 0">{{ $t('knowledgeGraph.detailTitle') }}</div>
        <button class="btn btn-sm" @click="selectNode(null)">{{ $t('common.close') }}</button>
      </div>

      <template v-if="detailLoading">
        <div class="loading"><div class="spinner"></div></div>
      </template>
      <template v-else-if="detail">
        <div class="detail-node">{{ detail.knowledge_name }}</div>
        <div class="stats-grid">
          <div class="stat">
            <div class="stat-value">{{ Math.round(detail.mastery_score * 100) }}%</div>
            <div class="stat-label">{{ $t('knowledgeGraph.masteryLabel') }}</div>
          </div>
          <div class="stat">
            <div class="stat-value">{{ detail.sample_size }}</div>
            <div class="stat-label">{{ $t('knowledgeGraph.sampleLabel') }}</div>
          </div>
        </div>

        <div class="formula-box">{{ detail.formula_description }}</div>

        <div class="mt-3">
          <div class="text-secondary mb-2">{{ $t('knowledgeGraph.evidenceTitle') }}（{{ (detail.evidence ?? []).length }}）</div>
          <table v-if="(detail.evidence ?? []).length > 0" class="table">
            <thead>
              <tr>
                <th>{{ $t('knowledgeGraph.evidenceType') }}</th>
                <th>{{ $t('knowledgeGraph.evidenceValue') }}</th>
                <th>{{ $t('knowledgeGraph.evidenceTime') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(ev, i) in (detail.evidence ?? [])" :key="i">
                <td>
                  <span class="badge" :class="ev.type === 'grading' ? 'badge-primary' : 'badge-warning'">
                    {{ ev.type === 'grading' ? $t('knowledgeGraph.typeGrading') : $t('knowledgeGraph.typeReview') }}
                  </span>
                </td>
                <td>{{ ev.value >= 0.5 ? '✓' : '✗' }}</td>
                <td class="text-secondary">{{ ev.occurred_at }}</td>
              </tr>
            </tbody>
          </table>
        </div>

        <div v-if="detailError" class="error-banner">{{ detailError }}</div>
        <div v-if="practiceInfo" class="offline-banner">{{ practiceInfo }}</div>

        <div class="detail-actions">
          <button class="btn btn-primary" :disabled="practiceBusy" @click="practiceFromNode">
            {{ practiceBusy ? $t('common.processing') : $t('knowledgeGraph.practiceBtn') }}
          </button>
        </div>
      </template>
      <div v-else-if="detailError" class="error-banner">{{ detailError }}</div>
    </div>
  </div>
</template>

<style scoped>
.toolbar {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  flex-wrap: wrap;
  margin-bottom: var(--space-2);
}

.legend {
  display: flex;
  gap: var(--space-3);
  flex-wrap: wrap;
  font-size: var(--text-xs);
  color: var(--text-secondary);
}

.legend-item {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.dot {
  display: inline-block;
  width: 10px;
  height: 10px;
  border-radius: 50%;
  flex-shrink: 0;
}

.dot-mastered {
  background: var(--color-success);
}

.dot-learning {
  background: var(--color-warning);
}

.dot-weak {
  background: var(--color-error);
}

.dot-unlearned {
  background: var(--color-disabled);
}

.canvas-shell {
  position: relative;
  border: 1px solid var(--border);
  border-radius: var(--radius-md);
  overflow: hidden;
  background: var(--bg-subtle);
  height: calc(100vh - 220px);
  min-height: 420px;
  touch-action: none;
}

.graph-canvas {
  width: 100%;
  height: 100%;
  display: block;
  cursor: grab;
}

.canvas-hint {
  position: absolute;
  left: 50%;
  bottom: 8px;
  transform: translateX(-50%);
  font-size: var(--text-xs);
  color: var(--text-secondary);
  pointer-events: none;
}

.tree-card {
  padding: var(--space-2);
}

.tree-row {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: 6px 8px;
  border-radius: var(--radius-sm);
  cursor: pointer;
}

.tree-row:hover {
  background: var(--bg-subtle);
}

.tree-row.selected {
  outline: 2px solid var(--accent);
}

.tree-name {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tree-mastery {
  font-weight: 600;
  color: var(--accent);
}

.tree-mastery.muted {
  color: var(--text-secondary);
  font-weight: 400;
}

.detail-panel {
  position: fixed;
  top: 0;
  right: 0;
  bottom: 0;
  width: min(420px, 92vw);
  background: var(--bg);
  border-left: 1px solid var(--border);
  padding: var(--space-3);
  overflow-y: auto;
  z-index: 40;
  box-shadow: var(--shadow-md);
}

.detail-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: var(--space-2);
}

.detail-node {
  font-size: var(--text-lg);
  font-weight: 700;
  margin-bottom: var(--space-2);
}

.stats-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(100px, 1fr));
  gap: var(--space-2);
}

.stat {
  padding: var(--space-2);
  background: var(--bg-subtle);
  border-radius: var(--radius-sm);
  text-align: center;
}

.stat-value {
  font-size: var(--text-xl);
  font-weight: 700;
}

.stat-label {
  font-size: var(--text-xs);
  color: var(--text-secondary);
  margin-top: 2px;
}

.formula-box {
  margin-top: var(--space-2);
  padding: var(--space-2);
  background: var(--bg-subtle);
  border-radius: var(--radius-sm);
  font-size: var(--text-sm);
  color: var(--text-secondary);
}

.detail-actions {
  margin-top: var(--space-3);
  display: flex;
  justify-content: flex-end;
}
</style>
