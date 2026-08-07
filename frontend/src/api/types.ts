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
  cloud_server: { configured: boolean; mode: 'inprocess' | 'cloud' }
  version: number
}

export interface SyncCloudPushReq {
  workspace_id: string
  user_id?: string
}

export interface SyncPushItemResult {
  operation_id: string
  result: 'accepted' | 'duplicate' | 'conflict' | 'rejected'
  server_sequence?: number
  server_version?: number
  conflict_copy?: unknown
}

export interface SyncPushResult {
  workspace_id: string
  items: SyncPushItemResult[]
  server_time: string
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

// ---------- 4.8 笔记与标注（API 设计文档 7.1） ----------

export type NoteKind = 'question' | 'document' | 'agent' | 'free'

export interface Note {
  id: string
  workspace_id: string
  user_id: string
  kind: NoteKind
  title: string
  body_md: string
  source_ref?: string
  knowledge_ids: string[]
  tags: string[]
  created_at: string
  updated_at: string
  version: number
}

export interface NotePage {
  items: Note[]
  next_cursor: string
  has_more: boolean
}

export interface Annotation {
  id: string
  note_id: string
  document_id?: string
  anchor_hash: string
  offset_start: number
  offset_end: number
  highlight_color: string
  created_at: string
}

export interface NoteCreateReq {
  workspace_id: string
  user_id: string
  kind?: NoteKind
  title: string
  body_md?: string
  source_ref?: string
  knowledge_ids?: string[]
  tags?: string[]
}

export interface NoteUpdateReq {
  workspace_id: string
  note_id: string
  version: number
  kind?: NoteKind
  title: string
  body_md?: string
  source_ref?: string
  knowledge_ids?: string[]
  tags?: string[]
}

export interface NoteListReq {
  workspace_id: string
  kind?: NoteKind
  knowledge_id?: string
  tag?: string
  keyword?: string
  cursor?: string
  limit?: number
}

export interface NoteDeleteReq {
  workspace_id: string
  note_id: string
  version: number
}

export interface NoteToFlashcardReq {
  workspace_id: string
  user_id: string
  note_id: string
  idempotency_key: string
}

export interface AnnotationCreateReq {
  workspace_id: string
  note_id: string
  document_id?: string
  anchor_hash: string
  offset_start: number
  offset_end: number
  highlight_color?: string
}

export interface DeleteResult {
  deleted: boolean
  deleted_at: string
}

// ---------- 4.10 组卷考试模块（API 设计文档 7.3） ----------

export interface ExamPaperSection {
  id: string
  paper_id: string
  title: string
  order_no: number
  question_version_ids: string[]
  score: number
}

export interface ExamPaper {
  id: string
  workspace_id: string
  user_id: string
  title: string
  config_json: Record<string, unknown>
  status: 'draft' | 'published' | 'archived'
  sections: ExamPaperSection[]
  version: number
  created_at: string
  updated_at: string
}

export interface Exam {
  id: string
  paper_id: string
  user_id: string
  status: 'created' | 'answering' | 'graded'
  duration_min: number
  started_at: string | null
  ended_at: string | null
  questions: PracticeQuestion[]
  created_at: string
  updated_at: string
}

export interface ExamResult {
  exam_id: string
  paper_id: string
  status: string
  total_score: number
  max_score: number
  duration_min: number
  started_at: string | null
  ended_at: string | null
  questions: ResultQuestion[]
  wrong_answers: { id: string; question_version_id: string; cause: string; status: string }[]
  review_actions: { review_card_id: string; wrong_answer_id: string; due_at: string }[]
}

export interface ExamAutoGenerateConfig {
  knowledge_ratio: Record<string, number>
  difficulty_dist: Record<string, number>
  count: number
  types: string[]
  duration_min: number
}

export interface ExamPaperCreateReq {
  workspace_id: string
  user_id: string
  title: string
  config_json: Record<string, unknown>
  idempotency_key: string
}

export interface ExamPaperAutoGenerateReq {
  workspace_id: string
  user_id: string
  title: string
  config: ExamAutoGenerateConfig
  idempotency_key: string
}

export interface ExamPaperPublishReq {
  workspace_id: string
  paper_id: string
  version: number
}

export interface ExamStartReq {
  workspace_id: string
  user_id: string
  paper_id: string
  idempotency_key: string
}

export interface ExamAutoSubmitReq {
  workspace_id: string
  exam_id: string
}

export interface ExamGetResultReq {
  workspace_id: string
  exam_id: string
}

// ---------- 4.11 打卡与成就（API 设计文档 7.4） ----------

export interface Checkin {
  id: string
  user_id: string
  date: string
  kind: 'normal' | 'makeup'
  minutes: number
  created_at: string
}

export interface CheckinCreateReq {
  workspace_id: string
  user_id: string
  minutes?: number
  idempotency_key: string
}

export interface CheckinMakeupReq {
  workspace_id: string
  user_id: string
  date: string
  minutes?: number
  idempotency_key: string
}

export interface Streak {
  user_id: string
  date: string
  streak: number
  total_checkins: number
}

export interface AchievementView {
  id: string
  code: string
  title_key: string
  description_key: string
  icon: string
  is_unlocked: boolean
  awarded_at: string | null
}

export interface AchievementListReq {
  workspace_id: string
  user_id: string
}

export interface StreakGetReq {
  workspace_id: string
  user_id: string
}

// ---------- 4.13 专注计时（API 设计文档 7.6） ----------

export interface TimerSession {
  id: string
  workspace_id: string
  user_id: string
  mode: 'pomodoro' | 'free'
  planned_minutes: number
  actual_seconds: number
  task_id: string | null
  status: 'completed' | 'interrupted' | 'abandoned'
  interrupt_reason: string | null
  started_at: string | null
  ended_at: string | null
  created_at: string
  updated_at: string
}

export interface TimerStats {
  total_sessions: number
  total_seconds: number
  completed_sessions: number
  interrupted_sessions: number
  abandoned_sessions: number
  interruption_rate: number
}

export interface TimerStartReq {
  workspace_id: string
  user_id: string
  mode: 'pomodoro' | 'free'
  planned_minutes: number
  task_id?: string
  idempotency_key: string
}

export interface TimerEndReq {
  workspace_id: string
  user_id: string
  session_id: string
  interrupt_reason?: string
}

export interface TimerStatsReq {
  workspace_id: string
  user_id: string
  start_date?: string
  end_date?: string
}

// ---------- 4.14 提醒与通知（API 设计文档 7.7） ----------

export type ReminderKind = 'review' | 'goal' | 'exam' | 'streak' | 'health'

export interface Reminder {
  id: string
  workspace_id: string
  user_id: string
  kind: ReminderKind
  rule_json: string
  enabled: boolean
  next_trigger_at: string
  created_at: string
  updated_at: string
}

export interface TestResult {
  ok: boolean
  kind: string
}

export interface Notification {
  id: string
  kind: string
  title_key: string
  body_args: Record<string, unknown>
  ref_type: string | null
  ref_id: string | null
  read_at: string | null
  created_at: string
}

export interface NotificationPage {
  items: Notification[]
  next_cursor: string
  has_more: boolean
}

/** NotificationMarkRead 返回（API 文档 7.7 BatchResult 契约：{updated}）。 */
export interface MarkReadResult {
  updated: number
}

export interface ReminderUpsertReq {
  workspace_id: string
  user_id: string
  kind: ReminderKind
  rule_json: string
  enabled: boolean
}

export interface ReminderTestSendReq {
  workspace_id: string
  user_id: string
  kind: ReminderKind
}

export interface NotificationListReq {
  workspace_id: string
  user_id: string
  unread_only?: boolean
  cursor?: string
  limit?: number
}

export interface NotificationMarkReadReq {
  workspace_id: string
  user_id: string
  ids: string[]
}

// ---------- 7.5 学习报告与数据洞察 ----------

export type ReportPeriod = 'daily' | 'weekly' | 'monthly'
export type ReportStatus = 'generating' | 'ready' | 'failed'
export type InsightDimension = 'knowledge' | 'time' | 'trend'

export interface ReportSummary {
  practice_count: number
  correct_count: number
  accuracy: number
  accuracy_samples: number
  review_done: number
  review_due: number
  focus_minutes: number
  focus_sessions: number
  checkin_days: number
  task_done: number
  task_total: number
  sample_insufficient: boolean
}

export interface WeakKnowledgeItem {
  knowledge_id: string
  name: string
  wrong_count: number
}

export interface TrendPoint {
  date: string
  practice_count: number
  correct_count: number
  accuracy: number
}

export interface TimeDistribution {
  morning: number
  afternoon: number
  evening: number
}

export interface ReportPayload {
  period: ReportPeriod
  period_start: string
  period_end: string
  generated_at: string
  schema_version: string
  summary: ReportSummary
  weak_knowledge: WeakKnowledgeItem[]
  trend: TrendPoint[]
  time_distribution: TimeDistribution
  interrupt_reasons: Record<string, number>
  suggestions: string[]
}

export interface Report {
  id: string
  workspace_id: string
  user_id: string
  period: ReportPeriod
  period_start: string
  period_end: string
  payload: ReportPayload
  status: ReportStatus
  created_at: string
  updated_at: string
}

export interface ReportPage {
  items: Report[]
  next_cursor: string
  has_more: boolean
}

export interface KnowledgeInsight {
  knowledge_id: string
  name: string
  practice_count: number
  correct_count: number
  accuracy: number
  last_reviewed_at: string | null
}

export interface TimeInsight {
  distribution: TimeDistribution
  avg_session_min: number
  interrupt_reasons: Record<string, number>
}

export interface TrendInsight {
  points: TrendPoint[]
}

export interface Insight {
  dimension: InsightDimension
  knowledge?: KnowledgeInsight[]
  time?: TimeInsight
  trend?: TrendInsight
}

// ---------- 4.15 收藏 / 稍后读 / 文档摘要（API 设计文档 7.8） ----------

export type FavoriteRefType = 'question' | 'document' | 'agent_message' | 'note'
export type ReadLaterStatus = 'queued' | 'read' | 'skipped'
export type ReadLaterAction = 'read' | 'skip' | 'requeue'
export type SummaryStatus = 'pending' | 'ready' | 'failed'

export interface Favorite {
  id: string
  user_id: string
  ref_type: FavoriteRefType
  ref_id: string
  group_name: string
  note: string
  version: number
  favorited: boolean
  created_at: string
  updated_at: string
}

export interface FavoritePage {
  items: Favorite[]
  next_cursor: string
  has_more: boolean
}

export interface FavoriteToggleReq {
  workspace_id: string
  user_id: string
  ref_type: string
  ref_id: string
  group_name?: string
  note?: string
  version?: number
}

export interface FavoriteListReq {
  workspace_id: string
  user_id: string
  group_name?: string
  ref_type?: string
  keyword?: string
  cursor?: string
  limit?: number
}

export interface ReadLaterItem {
  id: string
  workspace_id: string
  user_id: string
  document_id: string
  status: ReadLaterStatus
  created_at: string
  updated_at: string
}

export interface ReadLaterAddReq {
  workspace_id: string
  user_id: string
  document_id: string
}

export interface ReadLaterTransitionReq {
  workspace_id: string
  user_id: string
  item_id: string
  action: ReadLaterAction
}

export interface SummaryPayload {
  points: string[]
  structure: string[]
  terms: string[]
  note?: string
}

export interface DocumentSummary {
  id: string
  document_id: string
  summary_json: string | SummaryPayload
  model: string
  prompt_version?: string | null
  status: SummaryStatus
  created_at: string
  updated_at: string
}

export interface DocumentSummarizeReq {
  workspace_id: string
  document_id: string
  idempotency_key: string
}

// ---------- 日历与里程碑（API 文档 7.9 / 完整设计文档 4.16） ----------

/** 日历月视图单日投影条目（任务/复习/考试/打卡/专注/个人事件）。 */
export interface CalendarEntry {
  kind: 'task' | 'review' | 'exam' | 'checkin' | 'focus' | 'personal'
  ref_id: string
  title: string
  event_date: string
  start_time: string | null
  duration_min: number
}

/** CalendarGetMonth 响应。 */
export interface CalendarMonth {
  month: string
  entries: CalendarEntry[]
}

/** 个人日历事件 DTO。 */
export interface CalendarEvent {
  id: string
  workspace_id: string
  user_id: string
  kind: string
  ref_id: string | null
  event_date: string
  start_time: string | null
  duration_min: number
  title: string
  note: string
  created_at: string
  updated_at: string
}

/** 目标里程碑 DTO。 */
export interface Milestone {
  id: string
  goal_id: string
  title: string
  due_at: string
  criteria_json: MilestoneCriteria
  status: 'pending' | 'achieved' | 'not_met'
  achieved_at: string | null
  created_at: string
  updated_at: string
}

/** 里程碑验收条件（服务端 ParseMilestoneCriteria）。 */
export interface MilestoneCriteria {
  type: 'practice' | 'tasks'
  count: number
  min_accuracy?: number
}

export interface CalendarGetMonthReq {
  workspace_id: string
  user_id: string
  month: string
}

export interface CalendarEventUpsertReq {
  workspace_id: string
  user_id: string
  event_id?: string
  kind: 'personal'
  ref_id?: string | null
  event_date: string
  start_time?: string | null
  duration_min: number
  title: string
  note?: string
  idempotency_key: string
}

export interface MilestoneCreateReq {
  workspace_id: string
  user_id: string
  goal_id: string
  title: string
  due_at: string
  criteria_json: MilestoneCriteria
  idempotency_key: string
}

export interface MilestoneEvaluateReq {
  workspace_id: string
  user_id: string
  milestone_id: string
}

// ---------- 4.17 健康与专注辅助（API 设计文档 7.16） ----------

export interface HealthSettings {
  workspace_id: string
  user_id: string
  sedentary_enabled: boolean
  eye_enabled: boolean
  night_mode: 'auto' | 'light' | 'dark' | 'custom'
  blue_light_filter: boolean
  stats_enabled: boolean
  updated_at: string
}

export interface HealthSettingsUpdateReq {
  workspace_id: string
  user_id: string
  sedentary_enabled: boolean
  eye_enabled: boolean
  night_mode: 'auto' | 'light' | 'dark' | 'custom'
  blue_light_filter: boolean
  stats_enabled: boolean
}

export interface HealthStatsGetReq {
  workspace_id: string
  user_id: string
  start_date?: string
  end_date?: string
}

export interface HealthStats {
  stats_enabled: boolean
  sedentary_count: number
  rest_completion_rate: number
}

// ---------- 4.22 班级管理（API 文档 7.11） ----------

/** 班级 DTO。 */
export interface Class {
  id: string
  workspace_id: string
  owner_user_id: string
  name: string
  subject: string
  semester: string
  status: 'active' | 'archived'
  invite_code: string
  member_count: number
  created_at: string
  updated_at: string
}

/** 班级邀请码 DTO。 */
export interface InviteCode {
  class_id: string
  code: string
}

/** 班级成员 DTO。 */
export interface ClassMember {
  id: string
  class_id: string
  student_user_id: string
  display_name: string
  status: 'active' | 'removed'
  joined_at: string
}

export interface ClassCreateReq {
  workspace_id: string
  user_id: string
  name: string
  subject?: string
  semester?: string
  idempotency_key: string
}

export interface ClassListReq {
  workspace_id: string
  user_id: string
}

export interface ClassGetReq {
  workspace_id: string
  user_id: string
  class_id: string
}

export interface ClassUpdateReq {
  workspace_id: string
  user_id: string
  class_id: string
  name?: string
  subject?: string
  semester?: string
}

export interface ClassArchiveReq {
  workspace_id: string
  user_id: string
  class_id: string
}

export interface ClassInviteReq {
  workspace_id: string
  user_id: string
  class_id: string
}

export interface ClassMemberAddReq {
  workspace_id: string
  user_id: string
  class_id: string
  student_user_id: string
  idempotency_key: string
}

export interface ClassMemberRemoveReq {
  workspace_id: string
  user_id: string
  class_id: string
  student_user_id: string
}

export interface ClassMemberListReq {
  workspace_id: string
  user_id: string
  class_id: string
}

// ---------- 作业（API 文档 7.11 / 完整设计文档 4.22） ----------

/** 作业 DTO。 */
export interface Assignment {
  id: string
  class_id: string
  paper_id: string
  title: string
  due_at: string
  grading_rule: 'auto' | 'teacher' | 'hybrid'
  status: 'draft' | 'published' | 'closed'
  version: number
  created_at: string
  updated_at: string
  /** 学生附带本人提交（可见批阅状态/得分）；教师为 undefined。 */
  submission?: AssignmentSubmission
  /** 学生对该作业的申诉（学生视角；无申诉为 undefined）。 */
  appeal?: Appeal
}

/** 作业单题作答。 */
export interface AssignmentAnswer {
  question_version_id: string
  answer: unknown
}

/** 作业提交记录 DTO。 */
export interface AssignmentSubmission {
  id: string
  assignment_id: string
  student_user_id: string
  display_name: string
  submission_id: string | null
  grade_json: Record<string, unknown>
  graded_at: string | null
  created_at: string
  /** 提交所在练习会话（教师批阅时据此取作答）。 */
  session_id?: string | null
  /** 预批提示（EssayGrader 降级等），非预批调用为空。 */
  message?: string
}

export interface AssignmentCreateReq {
  workspace_id: string
  user_id: string
  class_id: string
  paper_id: string
  title: string
  due_at: string
  grading_rule: 'auto' | 'teacher' | 'hybrid'
  idempotency_key: string
}

export interface AssignmentPublishReq {
  workspace_id: string
  user_id: string
  assignment_id: string
  version: number
}

export interface AssignmentSubmitReq {
  workspace_id: string
  user_id: string
  assignment_id: string
  answers: AssignmentAnswer[]
  idempotency_key: string
}

export interface AssignmentListReq {
  workspace_id: string
  user_id: string
}

export interface AssignmentSubmissionListReq {
  workspace_id: string
  user_id: string
  assignment_id: string
}

/** 批阅作业请求（教师，班级创建者）。Version 为 grade_json.version 乐观锁；pre_grade=true 仅预批预览。 */
export interface AssignmentGradeReq {
  workspace_id: string
  user_id: string
  submission_id: string
  /** 不含 version 字段（服务端维护版本号）。 */
  grade_json?: Record<string, unknown>
  version: number
  pre_grade?: boolean
}

// ---------- 申诉复议（API 文档 7.11 / 完整设计文档 4.22 C7） ----------

export type AppealStatus = 'pending' | 'accepted' | 'rejected' | 'resolved'
export type AppealDecision = 'accepted' | 'rejected'

/** 申诉 DTO。 */
export interface Appeal {
  id: string
  /** 即作业提交 id（assignment_submissions.id）。 */
  grading_id: string
  student_user_id: string
  reason: string
  status: AppealStatus
  teacher_note: string
  created_at: string
  updated_at: string
}

/** 学生提交申诉。 */
export interface AppealCreateReq {
  workspace_id: string
  user_id: string
  grading_id: string
  reason: string
}

/** 教师处理申诉：decision ∈ accepted|rejected；accepted 时可带 new_score 复议改分。 */
export interface AppealResolveReq {
  workspace_id: string
  user_id: string
  appeal_id: string
  decision: AppealDecision
  new_score?: number
  teacher_note?: string
}

/** 教师复议视图：列出某作业全部申诉。 */
export interface AppealListReq {
  workspace_id: string
  user_id: string
  assignment_id: string
}

// ---------- 教师统计 ClassStats（API 文档 7.11 / 完整设计文档 4.22 C6） ----------

/** ClassStats 请求（教师/班级创建者；assignment_id 可选按作业过滤）。 */
export interface ClassStatsReq {
  workspace_id: string
  user_id: string
  class_id: string
  assignment_id?: string
}

/** 班级统计响应（完成率/均分/正确率/薄弱知识点 Top）。 */
export interface ClassStats {
  class_id: string
  assignment_id?: string
  /** 班级活跃学生数。 */
  student_total: number
  /** 统计范围作业数。 */
  assignment_total: number
  /** 已提交份数。 */
  submission_total: number
  /** 已判分份数。 */
  graded_total: number
  /** 完成率 = 提交份数/(学生数×作业数)，0~1。 */
  completion_rate: number
  /** 均分 = 已判分提交 overall 均值。 */
  avg_score: number
  /** 满分 = 已判分提交满分均值。 */
  max_score: number
  /** 正确率 = 得分/满分（客观题），0~1。 */
  accuracy: number
  /** 薄弱知识点 Top5（按答错次数降序）。 */
  weak_top: WeakKnowledgeItem[]
}

// ---------- 管理端 Admin（Todo 26：审核队列 / Provider 策略 / 功能开关 / 用户禁用 / 审计查询） ----------

/** AdminReviewListReq 审核队列请求（status 空=全部）。 */
export interface AdminReviewListReq {
  workspace_id: string
  status?: string
}

/** 审核条目 DTO。 */
export interface ReviewQueueItem {
  id: string
  ref_type: string
  ref_id: string
  status: string
  reason: string
  reviewed_at?: string
}

/** 审核队列页。 */
export interface ReviewQueuePage {
  total: number
  items: ReviewQueueItem[]
}

/** AdminReviewDecideReq 审核决策请求。 */
export interface AdminReviewDecideReq {
  workspace_id: string
  item_id: string
  decision: 'approved' | 'rejected' | 'taken_down'
  reason?: string
}

/** AdminProviderPolicySetReq 设置 Provider 策略请求。 */
export interface AdminProviderPolicySetReq {
  workspace_id: string
  provider: string
  model: string
  allowed: boolean
  daily_quota?: number
  monthly_budget?: number
}

/** Provider 策略 DTO。 */
export interface ProviderPolicy {
  provider: string
  model: string
  allowed: boolean
  daily_quota?: number
  monthly_budget?: number
}

/** AdminFeatureFlagSetReq 设置功能开关请求。 */
export interface AdminFeatureFlagSetReq {
  workspace_id: string
  key: string
  enabled: boolean
  rollout_percent: number
}

/** 功能开关 DTO。 */
export interface FeatureFlag {
  key: string
  enabled: boolean
  rollout_percent: number
}

/** AdminUserDisableReq 禁用用户请求。 */
export interface AdminUserDisableReq {
  workspace_id: string
  user_id: string
  reason?: string
}

/** 用户状态 DTO。 */
export interface UserStatus {
  disabled: boolean
  disabled_at?: string
}

/** AdminAuditListReq 审计列表请求。 */
export interface AdminAuditListReq {
  workspace_id: string
  actor_id?: string
  action?: string
}

/** 审计条目 DTO。 */
export interface AuditEntry {
  id: string
  actor_id?: string
  actor_role?: string
  action: string
  entity_type: string
  entity_id?: string
  payload: string
  before_json: string
  after_json: string
  created_at: string
}

/** 审计页。 */
export interface AuditPage {
  total: number
  items: AuditEntry[]
}

// ---------- 家庭绑定与家长模式（API 文档 7.10 / 完整设计文档 4.21） ----------

/** 家庭绑定 DTO。 */
export interface FamilyBinding {
  id: string
  student_user_id: string
  parent_user_id: string
  parent_display_name: string
  status: string
  bound_at: string | null
  revoked_at: string | null
  created_at: string
}

/** 家庭邀请码 DTO（24h 有效）。 */
export interface FamilyInvite {
  binding_id: string
  code: string
  status: string
  expires_at: string
  created_at: string
}

/** 学生端家庭面板（邀请码 + 绑定列表）。 */
export interface FamilyOverview {
  invite: FamilyInvite | null
  active_parents: number
  bindings: FamilyBinding[]
}

/** 家长限制设置 DTO。 */
export interface ParentSettings {
  parent_user_id: string
  student_user_id: string
  daily_limit_min: number
  ai_disabled: boolean
  report_enabled: boolean
  updated_at: string
}

/** 家长视图中的学生信息。 */
export interface FamilyStudent {
  user_id: string
  display_name: string
}

/** 学习时长聚合。 */
export interface FamilyMinutes {
  today: number
  week: number
}

/** 家长视图单学生聚合（G2：时长/打卡/完成率/正确率/薄弱知识点；G4：不含隐私明细）。 */
export interface FamilyViewItem {
  binding_id: string
  student: FamilyStudent
  study_minutes: FamilyMinutes
  streak_days: number
  total_checkins: number
  task_summary: { total: number; completed: number; pending: number }
  accuracy: { correct: number; total: number; rate: number }
  weak_knowledge: { knowledge_id: string; name: string; wrong_count: number }[]
  settings: ParentSettings
}

export interface FamilyInviteCreateReq {
  workspace_id: string
  user_id: string
  idempotency_key: string
}

export interface FamilyInviteGetReq {
  workspace_id: string
  user_id: string
}

export interface FamilyBindReq {
  workspace_id: string
  user_id: string
  invite_code: string
}

export interface FamilyUnbindReq {
  workspace_id: string
  user_id: string
  binding_id: string
  version: number
}

export interface ParentSettingsUpdateReq {
  workspace_id: string
  user_id: string
  student_user_id: string
  daily_limit_min: number
  ai_disabled: boolean
  report_enabled: boolean
}

export interface FamilyViewReq {
  workspace_id: string
  user_id: string
  student_user_id?: string
}

// ---------- 知识图谱（API 文档 7.16 / 完整设计文档 4.19） ----------

export interface KnowledgeGraphGetReq {
  workspace_id: string
  user_id?: string
}

export interface KnowledgeGraphNode {
  id: string
  name: string
  level: number
  parent_id?: string
  /** 掌握度 0-1；无证据时不返回。 */
  mastery?: number
  sample_size?: number
}

export interface KnowledgeGraphEdge {
  from: string
  to: string
  /** parent | prerequisite | related */
  type: string
  /** manual | ai */
  source: string
}

export interface KnowledgeGraph {
  nodes: KnowledgeGraphNode[]
  edges: KnowledgeGraphEdge[]
  truncated: boolean
}

export interface MasterySnapshotListReq {
  user_id: string
  knowledge_id?: string
  cursor?: string
  limit?: number
}

export interface MasterySnapshotListItem {
  id: string
  user_id: string
  knowledge_id: string
  knowledge_name: string
  mastery_score: number
  sample_size: number
  computed_at: string
}

export interface MasterySnapshotPage {
  items: MasterySnapshotListItem[]
  next_cursor: string
  has_more: boolean
}

export interface MasteryExplainReq {
  user_id: string
  knowledge_id: string
}

export interface MasteryEvidence {
  knowledge_id: string
  /** grading | review */
  type: string
  value: number
  weight: number
  occurred_at: string
}

export interface MasteryExplanation {
  knowledge_id: string
  knowledge_name: string
  mastery_score: number
  sample_size: number
  formula_version: string
  formula_description: string
  evidence: MasteryEvidence[]
}

// ── 创作分享 Share（API 文档 7.13 / 完整设计文档 4.20） ──

/** 分享链接（ref_type: question | paper | flashcard_pack | note）。 */
export interface Share {
  id: string
  workspace_id: string
  user_id: string
  ref_type: string
  ref_id: string
  token: string
  /** ISO 时间；永久分享为 null。 */
  expires_at: string | null
  /** 已撤销时间；未撤销为 null。 */
  revoked_at: string | null
  /** 安全扫描结果；默认 "clean"。 */
  scan_result: string | null
  created_at: string
}

export interface ShareCreateReq {
  workspace_id: string
  user_id: string
  ref_type: string
  ref_id: string
  /** 缺省=默认 7 天；0/-1=永久；合法值 {1,7,30}。 */
  ttl_days?: number
  idempotency_key: string
}

export interface ShareRevokeReq {
  workspace_id: string
  user_id: string
  share_id: string
}

export interface ShareResolveReq {
  token: string
}

export interface ShareResolveResult {
  share: Share
  /** 受限通道相对下载路径（exports/share-<token>.json）。 */
  download_path: string
}

// ── 插件 Plugins（API 文档 7.13 / 完整设计文档 4.24） ──

/** 插件清单（manifest.json 解析结果，api_version 固定 "1"）。 */
export interface PluginManifest {
  name: string
  version: string
  description?: string
  entrypoint: string[]
  /** manifest 声明的全部权限（KnownPermissions）。 */
  permissions: string[]
  api_version: string
}

/** 插件 DTO（全局资源，无 workspace_id；permissions = 用户已确认的权限）。 */
export interface Plugin {
  id: string
  name: string
  version: string
  manifest: PluginManifest
  enabled: boolean
  permissions: string[]
  installed_at: string
  updated_at: string
}

export interface PluginInstallReq {
  /** 插件包路径：目录（读取 <dir>/manifest.json）或单个 manifest 文件。 */
  path: string
  /** 64 字节 Ed25519 签名的十六进制串（覆盖 manifest 原始字节）。 */
  signature: string
}

export interface PluginSetEnabledReq {
  plugin_id: string
  enabled: boolean
}

export interface PluginUninstallReq {
  plugin_id: string
}

export interface PluginConfirmPermissionsReq {
  plugin_id: string
  /** 须为 manifest 已声明权限的子集。 */
  permissions: string[]
}

export interface PluginInvokeReq {
  plugin_id: string
  /** 缺省 "run"。 */
  method?: string
  /** 任意 JSON 参数（透传 stdin JSON-RPC params）。 */
  params?: unknown
}

/** 插件运行结果：ok=false = 插件自身失败（error 为 stderr 诊断，非服务错误）。 */
export interface PluginInvokeResult {
  ok: boolean
  result?: unknown
  error?: string
}

// ── Webhook 出站分发（Todo 31 / 完整设计文档 4.23） ──

/** 用户级领域事件白名单（与后端 agent.UserEventBus 一致，7 个）。 */
export const WEBHOOK_EVENTS = [
  'report:ready',
  'exam:auto_submitted',
  'flashcard:due',
  'reminder:triggered',
  'grading:appeal',
  'sync:extended',
  'grading:updated',
] as const

export type WebhookEventType = (typeof WEBHOOK_EVENTS)[number]

/** Webhook 订阅 DTO。secret_ref 仅存 secrets.json 键名，密钥值从不返回。 */
export interface WebhookSubscription {
  id: string
  workspace_id: string
  url: string
  event_types: WebhookEventType[]
  secret_ref: string | null
  enabled: boolean
  created_at: string
  updated_at: string
}

export interface WebhookSubscribeReq {
  workspace_id: string
  url: string
  event_types: WebhookEventType[]
  secret_ref?: string | null
  idempotency_key: string
}

export interface WebhookTestSendReq {
  workspace_id: string
  subscription_id: string
}

/** 测试发送结果（不落库投递记录、不进重试队列）。 */
export interface WebhookTestSendResp {
  ok: boolean
  status_code: number
  error: string
}

export interface WebhookDeleteReq {
  workspace_id: string
  subscription_id: string
}

export interface WebhookListReq {
  workspace_id: string
}

// ── 口语练习与语音合成 Speaking/TTS（API 文档 7.16 / 完整设计文档 4.18） ──

/** 文件上传结果（SpeakingUpload 等返回；path 相对 uploads 目录）。 */
export interface UploadedFile {
  path: string
  file_name: string
  size: number
  sha256: string
}

/** 语音合成结果（音频经 /api/v1/files?path=... 下载）。 */
export interface TTSResult {
  audio_path: string
  format: string // wav | mp3 | m4a
  duration_ms: number
}

export interface TTSPlayReq {
  workspace_id: string
  /** question | note | flashcard | document */
  ref_type: string
  ref_id: string
  /** 0.5–2.0，缺省 1.0 */
  speed?: number
}

/** 口语分维度评分（0–10）。 */
export interface SpeakingScores {
  pronunciation: number
  fluency: number
  completeness: number
  grammar: number
}

/** 口语测评结果。 */
export interface SpeakingResult {
  id: string
  submission_id: string
  transcript: string
  scores: SpeakingScores
  /** pending | graded | failed */
  status: string
  created_at: string
  updated_at: string
}

export interface SpeakingSubmitReq {
  workspace_id: string
  submission_id: string
  /** SpeakingUpload 返回的相对 uploads 路径 */
  audio_path: string
  idempotency_key: string
}

export interface SpeakingResultGetReq {
  workspace_id: string
  submission_id: string
}


