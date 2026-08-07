// API 客户端：方法名式 RPC over HTTP + SSE 事件流。
// 后端统一信封 { data, error, request_id }；错误码与 API 文档一致。
// 错误消息显示路径（Todo 8 决策）：后端 error.message 默认中文、不本地化；
// 前端仅对「已知 error.code」做 i18n key 映射（en-US 下显示英文文案），
// 未映射的 code 原样回退显示后端 message（zh-CN 下即原文）。

import { t } from '@/i18n'

export interface ApiError {
  code: string
  message: string
  retryable: boolean
  details?: Record<string, unknown>
}

export interface Envelope<T = unknown> {
  data: T | null
  error: ApiError | null
  request_id: string
}

export class ApiException extends Error {
  code: string
  retryable: boolean
  details?: Record<string, unknown>
  requestId?: string

  constructor(err: ApiError, requestId?: string) {
    super(err.message)
    this.code = err.code
    this.retryable = err.retryable
    this.details = err.details
    this.requestId = requestId
  }

  /** 当前语言下的错误文案：已知 code → i18n key 文案；未映射 code → 后端原始 message。 */
  localizedMessage(): string {
    return localizeApiError({ code: this.code, message: this.message, retryable: this.retryable, details: this.details })
  }
}

// 已知错误码 → i18n key 映射表（与 internal/domain/errors.go 的 24 个稳定码 + 客户端生成码对应）。
// 未出现在此表中的 code 一律回退原始 message，绝不空白。
const ERROR_MESSAGE_KEYS: Record<string, string> = {
  FORBIDDEN: 'error.forbidden',
  UNAUTHORIZED: 'error.unauthorized',
  NOT_FOUND: 'error.notFound',
  CONFLICT: 'error.conflict',
  INVALID_ARGUMENT: 'error.invalidArgument',
  INVALID_STATE: 'error.invalidState',
  INTERNAL: 'error.internal',
  DATABASE_UNAVAILABLE: 'error.databaseUnavailable',
  PROVIDER_TIMEOUT: 'error.providerTimeout',
  PROVIDER_RATE_LIMITED: 'error.providerRateLimited',
  OUTPUT_INVALID: 'error.outputInvalid',
  IMPORT_FAILED: 'error.importFailed',
  SANDBOX_LIMIT: 'error.sandboxLimit',
  REQUEST_CANCELLED: 'error.requestCancelled',
  FEATURE_DISABLED: 'error.featureDisabled',
  QUOTA_EXCEEDED: 'error.quotaExceeded',
  EXAM_IN_PROGRESS: 'error.examInProgress',
  PLUGIN_ERROR: 'error.pluginError',
  WEBHOOK_FAILED: 'error.webhookFailed',
  SHARE_EXPIRED: 'error.shareExpired',
  FAMILY_BOUND: 'error.familyBound',
  REVIEW_REQUIRED: 'error.reviewRequired',
  WORKSPACE_LOCKED: 'error.workspaceLocked',
  STORAGE_FULL: 'error.storageFull',
  MIGRATION_BLOCKED: 'error.migrationBlocked',
  // 客户端生成的错误码（本地失败，非后端信封）
  NETWORK: 'error.network',
  STREAM_FAILED: 'error.streamFailed',
  STREAM_INTERRUPTED: 'error.streamInterrupted',
  UNKNOWN: 'error.unknown',
  NO_SESSION: 'error.noSession',
}

/** 已知 code → 当前语言下的 i18n 文案；未映射 → null（调用方回退原始 message）。 */
export function errorMessageKey(code: string): string | null {
  return ERROR_MESSAGE_KEYS[code] ?? null
}

/** 把 ApiError 本地化：已知 code 用 i18n key 文案；未映射 code 原样返回 err.message。 */
export function localizeApiError(err: ApiError): string {
  const key = errorMessageKey(err.code)
  return key ? t(key) : err.message
}

/** 把任意抛出的错误归一为展示文本：ApiException 走本地化映射，其余 Error 原样返回 message。 */
export function localizedMessageOf(e: unknown): string {
  if (e instanceof ApiException) return e.localizedMessage()
  return e instanceof Error ? e.message : String(e ?? '')
}

const BASE = '/api/v1'

/** 调用后端绑定方法（POST /api/v1/{Method}，body 为参数对象）。 */
export async function call<T = unknown>(method: string, params: Record<string, unknown> = {}): Promise<T> {
  let res: Response
  try {
    res = await fetch(`${BASE}/${method}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(params),
    })
  } catch {
    throw new ApiException({ code: 'NETWORK', message: t('error.network'), retryable: true })
  }
  let envelope: Envelope<T>
  try {
    envelope = await res.json()
  } catch {
    throw new ApiException({ code: 'INTERNAL', message: t('error.internal'), retryable: true })
  }
  if (envelope.error) {
    throw new ApiException(envelope.error, envelope.request_id)
  }
  return envelope.data as T
}

/** multipart 文件上传（返回标准信封）。 */
export async function upload<T = unknown>(method: string, file: File, extra: Record<string, string> = {}): Promise<T> {
  const form = new FormData()
  form.append('file', file)
  for (const [k, v] of Object.entries(extra)) form.append(k, v)
  const res = await fetch(`${BASE}/${method}`, { method: 'POST', body: form })
  const envelope = (await res.json()) as Envelope<T>
  if (envelope.error) throw new ApiException(envelope.error, envelope.request_id)
  return envelope.data as T
}

// ---------- SSE 事件流 ----------

export type AgentEvent =
  | { event: 'agent:delta'; data: { delta: string; citations?: Citation[]; request_id: string; session_id: string; sequence_no: number } }
  | { event: 'agent:tool'; data: { tool_name: string; status: string; safe_summary: string; request_id: string; session_id: string; sequence_no: number } }
  | { event: 'agent:completed'; data: { message_id: string; usage?: unknown; citations?: Citation[]; request_id: string; session_id: string; sequence_no: number } }
  | { event: 'agent:error'; data: { error: { code: string; message: string }; request_id: string; session_id: string; sequence_no: number } }
  | { event: 'grading:updated'; data: { grading_id: string; status: string; score?: number | null; request_id: string; sequence_no: number } }
  | { event: 'import:progress'; data: { batch_id: string; processed: number; total: number; stage: string; sequence_no: number } }

export interface Citation {
  document_id: string
  document_name: string
  section?: string
  snippet: string
}

export interface StreamHandlers {
  onDelta?: (text: string, citations: Citation[], meta: { request_id: string; session_id: string; sequence_no: number }) => void
  onTool?: (tool: { tool_name: string; status: string; safe_summary: string }) => void
  onCompleted?: (meta: { message_id: string; request_id: string; session_id: string; sequence_no: number }) => void
  onError?: (err: ApiError, meta: { request_id: string; session_id: string }) => void
  /** 用户级领域事件：口语/简答等异步评分完成（grading:updated）。 */
  onGradingUpdated?: (data: { grading_id: string; submission_id?: string; status: string; score?: number | null }) => void
}

/** 打开 SSE 流：GET /api/v1/events?request_id=...&session_id=... 或 ?user_id=...
 *  传 sessionId 订阅 Agent 会话流式事件；传 userId 订阅用户级领域事件
 *  （report:ready / exam:auto_submitted / flashcard:due / reminder:triggered /
 *   grading:appeal / sync:extended / grading:updated）。两者并存时后端以 user_id 优先。
 */
export function openEventStream(
  requestId: string,
  sessionId: string,
  handlers: StreamHandlers,
  userId?: string,
): { close: () => void } {
  const controller = new AbortController()
  let lastSeq = -1

  const consume = (raw: string, eventName: string, meta: { request_id: string; session_id: string; sequence_no: number }) => {
    if (raw === '[DONE]') return
    if (meta.sequence_no <= lastSeq) return // 忽略过期序号
    lastSeq = meta.sequence_no
    let data: Record<string, unknown>
    try {
      data = JSON.parse(raw)
    } catch {
      return
    }
    switch (eventName) {
      case 'agent:delta':
        handlers.onDelta?.(String(data.delta ?? ''), (data.citations as Citation[]) ?? [], meta)
        break
      case 'agent:tool':
        handlers.onTool?.({ tool_name: String(data.tool_name ?? ''), status: String(data.status ?? ''), safe_summary: String(data.safe_summary ?? '') })
        break
      case 'agent:completed':
        handlers.onCompleted?.({ message_id: String(data.message_id ?? ''), request_id: meta.request_id, session_id: meta.session_id, sequence_no: meta.sequence_no })
        break
      case 'agent:error':
        handlers.onError?.(
          { code: String((data.error as { code?: string })?.code ?? 'UNKNOWN'), message: String((data.error as { message?: string })?.message ?? t('error.unknown')), retryable: true },
          meta,
        )
        break
      case 'grading:updated':
        handlers.onGradingUpdated?.({
          grading_id: String(data.grading_id ?? ''),
          submission_id: data.submission_id !== undefined ? String(data.submission_id) : undefined,
          status: String(data.status ?? ''),
          score: typeof data.score === 'number' ? data.score : null,
        })
        break
    }
  }

  const run = async () => {
    try {
      const params = new URLSearchParams({ request_id: requestId })
      if (sessionId) params.set('session_id', sessionId)
      if (userId) params.set('user_id', userId)
      const res = await fetch(`${BASE}/events?${params}`, {
        signal: controller.signal,
        headers: { Accept: 'text/event-stream' },
      })
      if (!res.ok || !res.body) {
        handlers.onError?.({ code: 'STREAM_FAILED', message: t('error.streamFailed'), retryable: true }, { request_id: requestId, session_id: sessionId })
        return
      }
      const reader = res.body.getReader()
      const decoder = new TextDecoder()
      let buffer = ''
      for (;;) {
        const { done, value } = await reader.read()
        if (done) break
        buffer += decoder.decode(value, { stream: true })
        let idx: number
        while ((idx = buffer.indexOf('\n\n')) >= 0) {
          const chunk = buffer.slice(0, idx)
          buffer = buffer.slice(idx + 2)
          let eventName = 'message'
          const dataLines: string[] = []
          for (const line of chunk.split('\n')) {
            if (line.startsWith('event:')) eventName = line.slice(6).trim()
            else if (line.startsWith('data:')) dataLines.push(line.slice(5).trim())
          }
          if (dataLines.length === 0) continue
          const raw = dataLines.join('\n')
          let meta: { request_id: string; session_id: string; sequence_no: number }
          try {
            const parsed = JSON.parse(raw)
            meta = {
              request_id: String(parsed.request_id ?? requestId),
              session_id: String(parsed.session_id ?? sessionId),
              sequence_no: Number(parsed.sequence_no ?? 0),
            }
          } catch {
            meta = { request_id: requestId, session_id: sessionId, sequence_no: lastSeq + 1 }
          }
          consume(raw, eventName, meta)
        }
      }
    } catch (e) {
      if (!controller.signal.aborted) {
        handlers.onError?.({ code: 'STREAM_INTERRUPTED', message: t('error.streamInterrupted'), retryable: true }, { request_id: requestId, session_id: sessionId })
      }
    }
  }
  void run()
  return { close: () => controller.abort() }
}
