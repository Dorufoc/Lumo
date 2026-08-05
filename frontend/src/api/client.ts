// API 客户端：方法名式 RPC over HTTP + SSE 事件流。
// 后端统一信封 { data, error, request_id }；错误码与 API 文档一致。

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
    throw new ApiException({ code: 'NETWORK', message: '无法连接本地服务，请确认后端已启动', retryable: true })
  }
  let envelope: Envelope<T>
  try {
    envelope = await res.json()
  } catch {
    throw new ApiException({ code: 'INTERNAL', message: '服务响应异常', retryable: true })
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
          { code: String((data.error as { code?: string })?.code ?? 'UNKNOWN'), message: String((data.error as { message?: string })?.message ?? 'AI 请求失败'), retryable: true },
          meta,
        )
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
        handlers.onError?.({ code: 'STREAM_FAILED', message: '事件流连接失败', retryable: true }, { request_id: requestId, session_id: sessionId })
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
        handlers.onError?.({ code: 'STREAM_FAILED', message: '事件流中断', retryable: true }, { request_id: requestId, session_id: sessionId })
      }
    }
  }
  void run()
  return { close: () => controller.abort() }
}
