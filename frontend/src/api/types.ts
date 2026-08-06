// 与后端 DTO 对应的类型定义（API 设计文档核心数据结构）。

export interface Workspace {
  id: string
  name: string
  owner_type: string
  created_at: string
  updated_at: string
  deleted_at: string | null
  version: number
}

export interface UserProfile {
  id: string
  workspace_id: string
  display_name: string
  role: string
  preferences: Record<string, unknown>
  created_at: string
  updated_at: string
  version: number
}

export interface Settings {
  workspace_id: string
  settings: Record<string, unknown>
  provider_status: Record<string, { configured: boolean; model?: string }>
  version: number
}

export interface KnowledgeNode {
  id: string
  name: string
  node_path: string
  level: number
  parent_id: string | null
  children?: KnowledgeNode[]
  version: number
  created_at: string
  updated_at: string
}

export interface QuestionOption {
  key: string
  text: string
}

export interface QuestionPayload {
  type: 'single_choice' | 'multiple_choice' | 'fill_blank' | 'short_answer' | 'code'
  stem: string
  options?: QuestionOption[]
  answer: string | string[]
  analysis?: string
  difficulty?: number
  knowledge_ids?: string[]
  grading_config?: Record<string, unknown>
  rubric?: { point: string; score: number; max: number }[]
  source?: string
  tags?: string[]
}

export interface QuestionVersion {
  id: string
  question_id: string
  version_no: number
  payload: QuestionPayload
  generated_by_model?: string | null
  prompt_version?: string | null
  review_status: string
  created_at: string
}

export interface Question {
  id: string
  workspace_id: string
  type: string
  status: 'draft' | 'reviewed' | 'published' | 'archived'
  source: string
  tags: string[]
  content_hash: string
  current_version?: QuestionVersion
  created_at: string
  updated_at: string
  version: number
}

export interface QuestionPage {
  items: Question[]
  next_cursor: string
  has_more: boolean
}

export interface LearningGoal {
  id: string
  workspace_id: string
  user_id: string
  name: string
  subject: string
  exam_at?: string | null
  target_score?: number | null
  daily_minutes: number
  available_weekdays: number[]
  knowledge_ids: string[]
  status: 'draft' | 'active' | 'paused' | 'completed' | 'archived'
  version: number
  created_at: string
  updated_at: string
}

export interface PlanTask {
  id: string
  workspace_id: string
  user_id: string
  goal_id?: string | null
  task_type: string
  due_at: string
  duration_min: number
  priority: number
  status: 'planned' | 'available' | 'in_progress' | 'completed' | 'skipped'
  reason_codes: string[]
  generated_reason: string
  version: number
  created_at: string
  updated_at: string
}

export interface PracticeQuestion {
  order_no: number
  question_id: string
  question_version_id: string
  type: string
  payload?: QuestionPayload
  max_score: number
}

export interface SubmissionDraft {
  id: string
  session_id: string
  question_version_id: string
  answer: string | string[] | Record<string, unknown>
  client_sequence: number
  status: string
  updated_at: string
}

export interface PracticeSession {
  id: string
  workspace_id: string
  user_id: string
  mode: string
  status: 'created' | 'answering' | 'submitted' | 'graded' | 'reviewed' | 'abandoned'
  questions: PracticeQuestion[]
  skipped: string[]
  time_limit_sec?: number | null
  started_at?: string | null
  submitted_at?: string | null
  drafts?: SubmissionDraft[]
  version: number
  created_at: string
  updated_at: string
}

export interface GradingResult {
  id: string
  submission_id: string
  status: 'pending' | 'completed' | 'failed' | 'needs_review'
  score?: number | null
  max_score: number
  method: string
  confidence?: number | null
  rule_version?: string | null
  reason: string
  needs_review: boolean
}

export interface ResultQuestion {
  order_no: number
  question_id: string
  question_version_id: string
  type: string
  payload: QuestionPayload
  submission?: SubmissionDraft | null
  grading?: GradingResult | null
  is_wrong: boolean
  skipped: boolean
}

export interface PracticeResult {
  session_id: string
  status: string
  total_score: number
  max_score: number
  questions: ResultQuestion[]
  wrong_answers: { id: string; question_version_id: string; cause: string; status: string }[]
  review_actions: { review_card_id: string; wrong_answer_id: string; due_at: string }[]
}

export interface WrongAnswer {
  id: string
  workspace_id: string
  user_id: string
  submission_id: string
  question_version_id: string
  answer: unknown
  cause: string
  status: string
  question?: Question
  version: number
  created_at: string
  updated_at: string
}

export interface WrongAnswerPage {
  items: WrongAnswer[]
  next_cursor: string
  has_more: boolean
}

export interface ReviewCard {
  id: string
  workspace_id: string
  user_id: string
  wrong_answer_id: string
  question?: Question
  wrong_answer?: WrongAnswer
  repetition: number
  interval_days: number
  ease_factor: number
  due_at: string
  status: string
  version: number
  created_at: string
  updated_at: string
}

export interface Dashboard {
  today_tasks: { total: number; completed: number; pending: number }
  due_reviews: number
  streak_days: number
  recent_accuracy: { correct: number; total: number; rate: number }
  weak_knowledge: { knowledge_id: string; name: string; wrong_count: number }[]
  ai_advice: string
  has_empty_library: boolean
}

export interface ImportPreview {
  batch_id: string
  file_name: string
  format: string
  status: string
  total_count: number
  valid_count: number
  error_count: number
  errors: { item_no: number; error: string }[]
  preview_items: QuestionPayload[]
}

export interface ImportBatch {
  id: string
  workspace_id: string
  idempotency_key: string
  file_name: string
  file_hash: string
  format: string
  status: string
  total_count: number
  valid_count: number
  error_count: number
  created_at: string
  updated_at: string
  items: { id: string; item_no: number; payload: QuestionPayload; status: string; error?: string | null; question_id?: string | null }[]
}

export interface AgentSession {
  id: string
  workspace_id: string
  user_id: string
  agent: string
  status: string
  request_id?: string | null
  context_version?: string | null
  messages?: AgentMessage[]
  created_at: string
  updated_at: string
}

export interface AgentMessage {
  id: string
  session_id: string
  role: string
  content: string
  sequence_no: number
  created_at: string
}

export interface AgentMemory {
  id: string
  workspace_id: string
  user_id: string
  memory_type: string
  summary: string
  source_ref?: string | null
  consent: boolean
  expires_at?: string | null
  version: number
  created_at: string
  updated_at: string
}

export interface Document {
  id: string
  workspace_id: string
  file_name: string
  mime_type: string
  byte_size: number
  sha256: string
  status: 'pending' | 'parsing' | 'indexed' | 'failed' | 'deleted'
  failure_reason?: string | null
  version: number
  created_at: string
  updated_at: string
}

export interface DocumentPage {
  items: Document[]
  next_cursor: string
  has_more: boolean
}

// ---------- 4.9 闪卡模块（API 设计文档 7.2） ----------

export interface Flashcard {
  id: string
  workspace_id: string
  source: 'knowledge' | 'note' | 'document' | 'manual'
  source_ref?: string
  front: string
  back: string
  card_type: 'basic' | 'choice' | 'cloze' | 'code'
  state: 'learning' | 'review' | 'mastered' | 'archived'
  repetition: number
  interval_days: number
  ease_factor: number
  due_at: string
  created_at: string
  updated_at: string
  version: number
}

export interface FlashcardCreateReq {
  workspace_id: string
  user_id: string
  source: string
  source_ref?: string
  front: string
  back: string
  card_type?: string
}

export interface FlashcardGenerateReq {
  workspace_id: string
  user_id: string
  source_ref: string
  idempotency_key: string
}

export interface FlashcardListDueReq {
  workspace_id: string
  user_id: string
  due_before?: string
  limit?: number
}

export interface FlashcardReviewReq {
  workspace_id: string
  flashcard_id: string
  rating: 'again' | 'hard' | 'good'
  idempotency_key: string
}

export interface FlashcardBatchReq {
  workspace_id: string
  action: 'archive' | 'delete' | 'reset'
  ids: string[]
}

export interface BatchError {
  id: string
  error: string
}

export interface BatchResult {
  success_count: number
  error_count: number
  errors?: BatchError[]
}

export interface FlashcardImportCsvReq {
  workspace_id: string
  user_id: string
  file_path: string
  idempotency_key: string
}

export interface FlashcardImportBatch {
  id: string
  workspace_id: string
  idempotency_key: string
  file_name: string
  file_hash: string
  format: string
  status: string
  total_count: number
  valid_count: number
  error_count: number
  created_at: string
  updated_at: string
  items?: { id: string; item_no: number; payload: Record<string, unknown>; status: string }[]
}

export interface ExportResult {
  path: string
  file_name: string
  format: string
  size_bytes: number
}
