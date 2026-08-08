<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { localizedMessageOf } from '@/api/client'
import type { InsightDimension, ReportPayload, ReportPeriod } from '@/api/types'
import { useI18nStore } from '@/stores/i18n'
import { useReportStore } from '@/stores/report'

const i18n = useI18nStore()
const store = useReportStore()

const loading = ref(true)
const error = ref('')
const info = ref('')
const exporting = ref('')

// 生成参数
const period = ref<ReportPeriod>('weekly')
const startDate = ref(defaultDate(-6))
const endDate = ref(defaultDate(0))
const insightDim = ref<InsightDimension>('knowledge')

function defaultDate(daysAgo: number): string {
  const d = new Date()
  d.setDate(d.getDate() + daysAgo)
  const m = String(d.getMonth() + 1).padStart(2, '0')
  const day = String(d.getDate()).padStart(2, '0')
  return `${d.getFullYear()}-${m}-${day}`
}

const latestPayload = computed<ReportPayload | null>(() => {
  const rep = store.latest
  return rep && rep.status === 'ready' ? rep.payload : null
})

const summary = computed(() => latestPayload.value?.summary ?? null)
const distribution = computed(() => latestPayload.value?.time_distribution ?? null)
const interruptReasons = computed<[string, number][]>(() =>
  Object.entries(latestPayload.value?.interrupt_reasons ?? {}).sort((a, b) => b[1] - a[1]),
)

async function load() {
  loading.value = true
  error.value = ''
  info.value = ''
  try {
    await store.load()
    if (store.items.length > 0) {
      store.latest = store.items[0]
      await loadInsight()
    }
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    loading.value = false
  }
}

async function onGenerate() {
  error.value = ''
  info.value = ''
  try {
    const rep = await store.generate({
      period: period.value,
      period_start: startDate.value,
      period_end: endDate.value,
    })
    info.value = i18n.t('report.generated', { id: rep.id.slice(0, 8) })
    await store.load()
    await loadInsight()
  } catch (e) {
    error.value = localizedMessageOf(e)
  }
}

async function loadInsight() {
  try {
    await store.loadInsight(insightDim.value)
  } catch {
    // 洞察非阻塞：失败不阻断报告查看
  }
}

async function onInsightTab(dim: InsightDimension) {
  insightDim.value = dim
  await loadInsight()
}

async function doExport(format: 'pdf' | 'json') {
  const rep = store.latest
  if (!rep || rep.status !== 'ready') return
  exporting.value = format
  error.value = ''
  info.value = ''
  try {
    const res = await store.exportReport(rep.id, format)
    info.value = i18n.t('report.exportDone', { name: res.file_name })
    window.location.href = `/api/v1/files?path=${encodeURIComponent(res.path)}`
  } catch (e) {
    error.value = localizedMessageOf(e)
  } finally {
    exporting.value = ''
  }
}

function accuracyText(rate: number): string {
  return `${Math.round(rate * 100)}%`
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-header">
      <div>
        <h1>{{ $t('report.title') }}</h1>
        <div class="subtitle">{{ $t('report.subtitle') }}</div>
      </div>
      <button class="btn btn-sm" :disabled="loading" @click="load">{{ $t('common.refresh') }}</button>
    </div>

    <div v-if="error" class="error-banner">{{ error }}</div>
    <div v-if="info" class="offline-banner">{{ info }}</div>
    <div v-if="loading" class="loading"><div class="spinner"></div></div>

    <!-- 生成报告 -->
    <div class="card mb-4">
      <div class="card-title">{{ $t('report.generateTitle') }}</div>
      <div class="form-row">
        <div class="field">
          <label>{{ $t('report.periodLabel') }}</label>
          <select v-model="period" class="input">
            <option value="daily">{{ $t('report.periodDaily') }}</option>
            <option value="weekly">{{ $t('report.periodWeekly') }}</option>
            <option value="monthly">{{ $t('report.periodMonthly') }}</option>
          </select>
        </div>
        <div class="field">
          <label>{{ $t('report.startLabel') }}</label>
          <input v-model="startDate" type="date" class="input" />
        </div>
        <div class="field">
          <label>{{ $t('report.endLabel') }}</label>
          <input v-model="endDate" type="date" class="input" />
        </div>
        <div class="field">
          <label>&nbsp;</label>
          <button class="btn btn-primary" :disabled="store.generating" @click="onGenerate">
            {{ store.generating ? $t('common.submitting') : $t('report.generateBtn') }}
          </button>
        </div>
      </div>
      <div class="hint mt-2">{{ $t('report.generateHint') }}</div>
    </div>

    <!-- 最新报告汇总 -->
    <template v-if="latestPayload">
      <div class="card mb-4">
        <div class="flex-between">
          <div class="card-title">{{ $t('report.summaryTitle') }}</div>
          <div class="flex gap-2">
            <button
              v-if="store.latest?.status === 'ready'"
              class="btn btn-sm"
              :disabled="exporting === 'pdf'"
              @click="doExport('pdf')"
            >
              {{ exporting === 'pdf' ? $t('common.submitting') : $t('report.exportPdf') }}
            </button>
            <button
              v-if="store.latest?.status === 'ready'"
              class="btn btn-sm"
              :disabled="exporting === 'json'"
              @click="doExport('json')"
            >
              {{ exporting === 'json' ? $t('common.submitting') : $t('report.exportJson') }}
            </button>
          </div>
        </div>

        <div class="grid grid-4">
          <div class="stat-card">
            <div class="stat-value">{{ summary?.practice_count ?? 0 }}</div>
            <div class="stat-label">{{ $t('report.practiceCount') }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-value">
              {{
                summary && !summary.sample_insufficient && summary.accuracy_samples > 0
                  ? accuracyText(summary.accuracy)
                  : '—'
              }}
            </div>
            <div class="stat-label">{{ $t('report.accuracy') }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-value">{{ summary?.review_done ?? 0 }}</div>
            <div class="stat-label">{{ $t('report.reviewDone') }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-value">{{ summary?.focus_minutes ?? 0 }}</div>
            <div class="stat-label">{{ $t('report.focusMinutes') }}</div>
          </div>
        </div>

        <div v-if="summary?.sample_insufficient" class="hint mt-2">{{ $t('report.sampleInsufficient') }}</div>
      </div>

      <!-- 薄弱知识点 + 建议 -->
      <div class="grid grid-2 mb-4">
        <div class="card">
          <div class="card-title">{{ $t('report.weakTitle') }}</div>
          <div v-if="(latestPayload.weak_knowledge ?? []).length === 0" class="empty">{{ $t('report.weakEmpty') }}</div>
          <table v-else class="table">
            <tbody>
              <tr v-for="w in latestPayload.weak_knowledge ?? []" :key="w.knowledge_id">
                <td>{{ w.name }}</td>
                <td class="right">
                  <span class="badge badge-error">{{ w.wrong_count }}</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div class="card">
          <div class="card-title">{{ $t('report.suggestTitle') }}</div>
          <ul v-if="(latestPayload.suggestions ?? []).length > 0" class="text-secondary" style="padding-left: 20px">
            <li v-for="(sg, i) in latestPayload.suggestions ?? []" :key="i">{{ sg }}</li>
          </ul>
          <div v-else class="empty">{{ $t('report.suggestEmpty') }}</div>
        </div>
      </div>

      <!-- 时段分布 + 中断原因 -->
      <div v-if="distribution" class="grid grid-2 mb-4">
        <div class="card">
          <div class="card-title">{{ $t('report.timeDistTitle') }}</div>
          <div class="grid grid-3">
            <div class="stat-card">
              <div class="stat-value">{{ distribution.morning }}</div>
              <div class="stat-label">{{ $t('report.morning') }}</div>
            </div>
            <div class="stat-card">
              <div class="stat-value">{{ distribution.afternoon }}</div>
              <div class="stat-label">{{ $t('report.afternoon') }}</div>
            </div>
            <div class="stat-card">
              <div class="stat-value">{{ distribution.evening }}</div>
              <div class="stat-label">{{ $t('report.evening') }}</div>
            </div>
          </div>
        </div>
        <div class="card">
          <div class="card-title">{{ $t('report.interruptTitle') }}</div>
          <div v-if="interruptReasons.length === 0" class="empty">{{ $t('report.interruptEmpty') }}</div>
          <table v-else class="table">
            <tbody>
              <tr v-for="[reason, n] in interruptReasons" :key="reason">
                <td>{{ reason }}</td>
                <td class="right"><span class="badge">{{ n }}</span></td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- 洞察 -->
      <div class="card mb-4">
        <div class="card-title">{{ $t('report.insightTitle') }}</div>
        <div class="tabs">
          <div class="tab" :class="{ active: insightDim === 'knowledge' }" @click="onInsightTab('knowledge')">
            {{ $t('report.dimKnowledge') }}
          </div>
          <div class="tab" :class="{ active: insightDim === 'time' }" @click="onInsightTab('time')">
            {{ $t('report.dimTime') }}
          </div>
          <div class="tab" :class="{ active: insightDim === 'trend' }" @click="onInsightTab('trend')">
            {{ $t('report.dimTrend') }}
          </div>
        </div>

        <!-- knowledge -->
        <div v-if="insightDim === 'knowledge'">
          <div v-if="!store.insight?.knowledge?.length" class="empty">{{ $t('report.insightKnowledgeEmpty') }}</div>
          <table v-else class="table">
            <tbody>
              <tr v-for="k in store.insight?.knowledge ?? []" :key="k.knowledge_id">
                <td>
                  <div>{{ k.name }}</div>
                  <div class="hint">{{ $t('report.knowledgeStats', { correct: k.correct_count, total: k.practice_count }) }}</div>
                </td>
                <td class="right">
                  <span class="badge">{{ k.practice_count > 0 ? accuracyText(k.accuracy) : '—' }}</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <!-- time -->
        <div v-else-if="insightDim === 'time'">
          <div class="grid grid-3">
            <div class="stat-card">
              <div class="stat-value">{{ store.insight?.time?.avg_session_min ?? 0 }}</div>
              <div class="stat-label">{{ $t('report.avgSession') }}</div>
            </div>
            <div class="stat-card">
              <div class="stat-value">{{ store.insight?.time?.distribution.morning ?? 0 }}</div>
              <div class="stat-label">{{ $t('report.morning') }}</div>
            </div>
            <div class="stat-card">
              <div class="stat-value">{{ store.insight?.time?.distribution.evening ?? 0 }}</div>
              <div class="stat-label">{{ $t('report.evening') }}</div>
            </div>
          </div>
        </div>

        <!-- trend -->
        <div v-else>
          <div v-if="!store.insight?.trend?.points?.length" class="empty">{{ $t('report.insightTrendEmpty') }}</div>
          <table v-else class="table">
            <tbody>
              <tr v-for="p in store.insight?.trend?.points ?? []" :key="p.date">
                <td>{{ p.date }}</td>
                <td class="right">
                  <span class="badge">{{ $t('report.trendStats', { count: p.practice_count }) }}</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>

    <div v-else-if="!loading" class="card">
      <div class="empty">{{ $t('report.noLatest') }}</div>
    </div>

    <!-- 报告列表 -->
    <div class="card">
      <div class="card-title">{{ $t('report.listTitle') }}</div>
      <div v-if="store.items.length === 0" class="empty">{{ $t('report.listEmpty') }}</div>
      <table v-else class="table">
        <thead>
          <tr>
            <th>{{ $t('report.colPeriod') }}</th>
            <th>{{ $t('report.colRange') }}</th>
            <th>{{ $t('report.colStatus') }}</th>
            <th>{{ $t('report.colCreated') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="r in store.items" :key="r.id">
            <td>{{ $t(`report.period${r.period.charAt(0).toUpperCase()}${r.period.slice(1)}`) }}</td>
            <td class="hint">{{ r.period_start.slice(0, 10) }} ~ {{ r.period_end.slice(0, 10) }}</td>
            <td>
              <span v-if="r.status === 'ready'" class="badge badge-success">{{ $t('report.statusReady') }}</span>
              <span v-else-if="r.status === 'failed'" class="badge badge-error">{{ $t('report.statusFailed') }}</span>
              <span v-else class="badge badge-offline">{{ $t('report.statusGenerating') }}</span>
            </td>
            <td class="hint">{{ r.created_at.slice(0, 16).replace('T', ' ') }}</td>
          </tr>
        </tbody>
      </table>
      <button v-if="store.hasMore" class="btn mt-3" :disabled="store.busy" @click="store.loadMore()">
        {{ $t('common.loadMore') }}
      </button>
    </div>
  </div>
</template>
