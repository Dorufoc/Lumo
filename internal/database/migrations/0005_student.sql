-- 0005_student: 学生全场景扩展表（完整设计文档 6.2.1 扩展表，逐字对齐）
-- 约定：6.4.2 字段规范（id TEXT PRIMARY KEY / FK ON DELETE RESTRICT / 时间 TEXT ISO 8601 UTC
--       / version 手动增量 / 状态枚举 CHECK / JSON 字段 json_valid / 软删 deleted_at / secret_ref）
-- 索引：6.3.1 扩展索引与唯一约束（仅作用于 0005 新表；settings/import_batches 保持旧结构不适用）
-- 冲突处理：review_events(0001) 用 ALTER 追加列；settings(0002)/import_batches(0001) 保持原样。

-- ============ 4.8 学习笔记 ============

CREATE TABLE IF NOT EXISTS notes (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  kind TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  body_md TEXT NOT NULL DEFAULT '',
  source_ref TEXT,
  knowledge_ids TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(knowledge_ids)),
  tags TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(tags)),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  deleted_at TEXT,
  version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0)
);

CREATE TABLE IF NOT EXISTS note_annotations (
  id TEXT PRIMARY KEY,
  note_id TEXT NOT NULL REFERENCES notes(id) ON DELETE RESTRICT,
  document_id TEXT REFERENCES documents(id) ON DELETE RESTRICT,
  anchor_hash TEXT NOT NULL,
  offset_start INTEGER NOT NULL CHECK (offset_start >= 0),
  offset_end INTEGER NOT NULL CHECK (offset_end >= offset_start),
  highlight_color TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- ============ 4.9 闪卡 ============

CREATE TABLE IF NOT EXISTS flashcards (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  source TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('knowledge', 'note', 'document', 'manual')),
  source_ref TEXT,
  front TEXT NOT NULL,
  back TEXT NOT NULL,
  card_type TEXT NOT NULL DEFAULT 'basic',
  state TEXT NOT NULL DEFAULT 'learning' CHECK (state IN ('learning', 'review', 'mastered', 'archived')),
  repetition INTEGER NOT NULL DEFAULT 0 CHECK (repetition >= 0),
  interval_days INTEGER NOT NULL DEFAULT 0 CHECK (interval_days >= 0),
  ease_factor REAL NOT NULL DEFAULT 2.5 CHECK (ease_factor >= 1.3 AND ease_factor <= 3.5),
  due_at TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  deleted_at TEXT,
  version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0)
);

-- 只追加：复习行为事件（6.3.1 只追加约定），reviewed_at 即时间戳。
CREATE TABLE IF NOT EXISTS flashcard_reviews (
  id TEXT PRIMARY KEY,
  flashcard_id TEXT NOT NULL REFERENCES flashcards(id) ON DELETE RESTRICT,
  rating TEXT NOT NULL CHECK (rating IN ('again', 'hard', 'good')),
  reviewed_at TEXT NOT NULL,
  next_due_at TEXT NOT NULL
);

-- ============ 4.10 模拟考试 ============

CREATE TABLE IF NOT EXISTS exam_papers (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  title TEXT NOT NULL CHECK (length(trim(title)) BETWEEN 1 AND 200),
  config_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(config_json)),
  status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published', 'archived')),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0)
);

CREATE TABLE IF NOT EXISTS exam_paper_sections (
  id TEXT PRIMARY KEY,
  paper_id TEXT NOT NULL REFERENCES exam_papers(id) ON DELETE RESTRICT,
  title TEXT NOT NULL DEFAULT '',
  order_no INTEGER NOT NULL CHECK (order_no > 0),
  question_version_ids TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(question_version_ids)),
  score INTEGER NOT NULL DEFAULT 0 CHECK (score >= 0),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS exams (
  id TEXT PRIMARY KEY,
  paper_id TEXT NOT NULL REFERENCES exam_papers(id) ON DELETE RESTRICT,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  status TEXT NOT NULL DEFAULT 'created',
  started_at TEXT,
  ended_at TEXT,
  score_summary_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(score_summary_json)),
  suspicious_events_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(suspicious_events_json)),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- ============ 4.11 打卡与成就 ============

CREATE TABLE IF NOT EXISTS checkins (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  date TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'normal' CHECK (kind IN ('normal', 'makeup')),
  minutes INTEGER NOT NULL DEFAULT 0 CHECK (minutes >= 0),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  UNIQUE (user_id, date)
);

CREATE TABLE IF NOT EXISTS achievement_defs (
  id TEXT PRIMARY KEY,
  code TEXT NOT NULL,
  title_key TEXT NOT NULL,
  description_key TEXT NOT NULL,
  trigger_rule_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(trigger_rule_json)),
  icon TEXT NOT NULL DEFAULT '',
  version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS user_achievements (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  achievement_id TEXT NOT NULL REFERENCES achievement_defs(id) ON DELETE RESTRICT,
  awarded_at TEXT NOT NULL,
  event_ref TEXT,
  idempotency_key TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- streak 为聚合投影，另存于 streak_snapshots（4.11）。
CREATE TABLE IF NOT EXISTS streak_snapshots (
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  date TEXT NOT NULL,
  streak INTEGER NOT NULL DEFAULT 0 CHECK (streak >= 0),
  total_checkins INTEGER NOT NULL DEFAULT 0 CHECK (total_checkins >= 0),
  computed_at TEXT NOT NULL,
  PRIMARY KEY (user_id, date)
);

-- ============ 4.12 学习报告 ============

CREATE TABLE IF NOT EXISTS reports (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  period TEXT NOT NULL CHECK (period IN ('daily', 'weekly', 'monthly')),
  period_start TEXT NOT NULL,
  period_end TEXT NOT NULL,
  payload_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(payload_json)),
  status TEXT NOT NULL DEFAULT 'generating' CHECK (status IN ('generating', 'ready', 'failed')),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS report_cache (
  period_key TEXT PRIMARY KEY,
  payload_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(payload_json)),
  computed_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- ============ 4.13 专注计时 ============
-- 决策 a：在 6.2.1 列基础上追加 started_at / ended_at（TEXT UTC ISO 8601，可空）。
-- 进行中的会话本地暂存，恢复后落库，故 status 为终态枚举。

CREATE TABLE IF NOT EXISTS timer_sessions (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  mode TEXT NOT NULL DEFAULT 'pomodoro' CHECK (mode IN ('pomodoro', 'free')),
  planned_minutes INTEGER NOT NULL DEFAULT 0 CHECK (planned_minutes >= 0),
  actual_seconds INTEGER NOT NULL DEFAULT 0 CHECK (actual_seconds >= 0),
  task_id TEXT,
  status TEXT NOT NULL DEFAULT 'completed' CHECK (status IN ('completed', 'interrupted', 'abandoned')),
  interrupt_reason TEXT,
  started_at TEXT,
  ended_at TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- ============ 4.14 提醒与通知 ============

CREATE TABLE IF NOT EXISTS reminders (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  kind TEXT NOT NULL DEFAULT 'review' CHECK (kind IN ('review', 'goal', 'exam', 'streak', 'health')),
  rule_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(rule_json)),
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  next_trigger_at TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS notifications (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  kind TEXT NOT NULL DEFAULT '',
  title_key TEXT NOT NULL,
  body_args_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(body_args_json)),
  ref_type TEXT,
  ref_id TEXT,
  read_at TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- ============ 4.15 收藏 / 稍后读 / 书签 / 摘要 ============

CREATE TABLE IF NOT EXISTS favorites (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  ref_type TEXT NOT NULL CHECK (ref_type IN ('question', 'document', 'agent_message', 'note')),
  ref_id TEXT NOT NULL,
  group_name TEXT NOT NULL DEFAULT '',
  note TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
  UNIQUE (user_id, ref_type, ref_id)
);

CREATE TABLE IF NOT EXISTS read_later (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  document_id TEXT NOT NULL REFERENCES documents(id) ON DELETE RESTRICT,
  status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'read', 'skipped')),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS document_bookmarks (
  id TEXT PRIMARY KEY,
  document_id TEXT NOT NULL REFERENCES documents(id) ON DELETE RESTRICT,
  anchor_hash TEXT NOT NULL,
  label TEXT NOT NULL DEFAULT '',
  offset INTEGER NOT NULL DEFAULT 0 CHECK (offset >= 0),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS document_summaries (
  id TEXT PRIMARY KEY,
  document_id TEXT NOT NULL REFERENCES documents(id) ON DELETE RESTRICT,
  summary_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(summary_json)),
  model TEXT NOT NULL DEFAULT '',
  prompt_version TEXT,
  status TEXT NOT NULL DEFAULT 'pending',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- ============ 4.16 学习日历与目标里程碑 ============

CREATE TABLE IF NOT EXISTS calendar_events (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  kind TEXT NOT NULL DEFAULT 'task' CHECK (kind IN ('task', 'review', 'exam', 'checkin', 'focus', 'personal')),
  ref_id TEXT,
  event_date TEXT NOT NULL,
  start_time TEXT,
  duration_min INTEGER NOT NULL DEFAULT 0 CHECK (duration_min >= 0),
  title TEXT NOT NULL DEFAULT '',
  note TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS goal_milestones (
  id TEXT PRIMARY KEY,
  goal_id TEXT NOT NULL REFERENCES learning_goals(id) ON DELETE RESTRICT,
  title TEXT NOT NULL CHECK (length(trim(title)) BETWEEN 1 AND 200),
  due_at TEXT NOT NULL,
  criteria_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(criteria_json)),
  status TEXT NOT NULL DEFAULT 'pending',
  achieved_at TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- ============ 4.17 健康与专注设置 ============

CREATE TABLE IF NOT EXISTS health_settings (
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  sedentary_enabled INTEGER NOT NULL DEFAULT 1 CHECK (sedentary_enabled IN (0, 1)),
  eye_enabled INTEGER NOT NULL DEFAULT 1 CHECK (eye_enabled IN (0, 1)),
  night_mode TEXT NOT NULL DEFAULT 'auto' CHECK (night_mode IN ('auto', 'light', 'dark', 'custom')),
  blue_light_filter INTEGER NOT NULL DEFAULT 0 CHECK (blue_light_filter IN (0, 1)),
  stats_enabled INTEGER NOT NULL DEFAULT 1 CHECK (stats_enabled IN (0, 1)),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  PRIMARY KEY (workspace_id, user_id)
);

-- ============ 4.18 口语测评 ============

CREATE TABLE IF NOT EXISTS speaking_results (
  id TEXT PRIMARY KEY,
  submission_id TEXT NOT NULL REFERENCES submissions(id) ON DELETE RESTRICT,
  transcript TEXT NOT NULL DEFAULT '',
  scores_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(scores_json)),
  audio_kept INTEGER NOT NULL DEFAULT 0 CHECK (audio_kept IN (0, 1)),
  status TEXT NOT NULL DEFAULT 'pending',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- ============ 4.19 知识关系与掌握度 ============

CREATE TABLE IF NOT EXISTS knowledge_relations (
  id TEXT PRIMARY KEY,
  from_knowledge_id TEXT NOT NULL REFERENCES knowledge_nodes(id) ON DELETE RESTRICT,
  to_knowledge_id TEXT NOT NULL REFERENCES knowledge_nodes(id) ON DELETE RESTRICT,
  rel_type TEXT NOT NULL DEFAULT 'related' CHECK (rel_type IN ('parent', 'prerequisite', 'related')),
  source TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'ai')),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS mastery_snapshots (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  knowledge_id TEXT NOT NULL REFERENCES knowledge_nodes(id) ON DELETE RESTRICT,
  mastery_score REAL NOT NULL DEFAULT 0 CHECK (mastery_score >= 0 AND mastery_score <= 1),
  sample_size INTEGER NOT NULL DEFAULT 0 CHECK (sample_size >= 0),
  computed_at TEXT NOT NULL
);

-- ============ 4.20 分享与内容请求 ============

CREATE TABLE IF NOT EXISTS shares (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  ref_type TEXT NOT NULL CHECK (ref_type IN ('question', 'paper', 'flashcard_pack', 'note')),
  ref_id TEXT NOT NULL,
  token TEXT NOT NULL,
  expires_at TEXT,
  revoked_at TEXT,
  scan_result TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS content_requests (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  knowledge_ids TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(knowledge_ids)),
  description TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'fulfilled', 'closed')),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- ============ 4.21 家长/监护模式 ============

CREATE TABLE IF NOT EXISTS family_bindings (
  id TEXT PRIMARY KEY,
  student_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  parent_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  invite_code TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'active', 'revoked')),
  bound_at TEXT,
  revoked_at TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS parent_settings (
  parent_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  student_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  daily_limit_min INTEGER NOT NULL DEFAULT 0 CHECK (daily_limit_min >= 0),
  ai_disabled INTEGER NOT NULL DEFAULT 0 CHECK (ai_disabled IN (0, 1)),
  report_enabled INTEGER NOT NULL DEFAULT 1 CHECK (report_enabled IN (0, 1)),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  PRIMARY KEY (parent_user_id, student_user_id)
);

-- ============ 4.22 教师/班级协作 ============

CREATE TABLE IF NOT EXISTS classes (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  owner_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  name TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 120),
  subject TEXT NOT NULL DEFAULT '',
  semester TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'active',
  invite_code TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS class_members (
  id TEXT PRIMARY KEY,
  class_id TEXT NOT NULL REFERENCES classes(id) ON DELETE RESTRICT,
  student_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'removed')),
  joined_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS assignments (
  id TEXT PRIMARY KEY,
  class_id TEXT NOT NULL REFERENCES classes(id) ON DELETE RESTRICT,
  paper_id TEXT NOT NULL REFERENCES exam_papers(id) ON DELETE RESTRICT,
  title TEXT NOT NULL CHECK (length(trim(title)) BETWEEN 1 AND 200),
  due_at TEXT NOT NULL,
  grading_rule TEXT NOT NULL DEFAULT 'auto' CHECK (grading_rule IN ('auto', 'teacher', 'hybrid')),
  status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published', 'closed')),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0)
);

CREATE TABLE IF NOT EXISTS assignment_submissions (
  id TEXT PRIMARY KEY,
  assignment_id TEXT NOT NULL REFERENCES assignments(id) ON DELETE RESTRICT,
  student_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  submission_id TEXT REFERENCES submissions(id) ON DELETE RESTRICT,
  grade_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(grade_json)),
  graded_at TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS grading_appeals (
  id TEXT PRIMARY KEY,
  grading_id TEXT NOT NULL REFERENCES grading_results(id) ON DELETE RESTRICT,
  student_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  reason TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'accepted', 'rejected', 'resolved')),
  teacher_note TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- ============ 4.23 内容审核与运营 ============

CREATE TABLE IF NOT EXISTS review_queue_items (
  id TEXT PRIMARY KEY,
  ref_type TEXT NOT NULL,
  ref_id TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'taken_down')),
  reviewer_id TEXT REFERENCES users(id) ON DELETE RESTRICT,
  reason TEXT NOT NULL DEFAULT '',
  reviewed_at TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS content_reports (
  id TEXT PRIMARY KEY,
  reporter_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  ref_type TEXT NOT NULL,
  ref_id TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'pending',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS feature_flags (
  id TEXT PRIMARY KEY,
  key TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
  rollout_percent INTEGER NOT NULL DEFAULT 100 CHECK (rollout_percent BETWEEN 0 AND 100),
  updated_by TEXT REFERENCES users(id) ON DELETE RESTRICT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS provider_policies (
  id TEXT PRIMARY KEY,
  provider TEXT NOT NULL,
  model TEXT NOT NULL,
  allowed INTEGER NOT NULL DEFAULT 1 CHECK (allowed IN (0, 1)),
  daily_quota INTEGER CHECK (daily_quota IS NULL OR daily_quota >= 0),
  monthly_budget INTEGER CHECK (monthly_budget IS NULL OR monthly_budget >= 0),
  updated_by TEXT REFERENCES users(id) ON DELETE RESTRICT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- ============ 4.24 插件与 Webhook 集成 ============

CREATE TABLE IF NOT EXISTS plugins (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  version TEXT NOT NULL,
  manifest_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(manifest_json)),
  enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
  permissions_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(permissions_json)),
  installed_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS webhook_subscriptions (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  url TEXT NOT NULL CHECK (length(trim(url)) BETWEEN 1 AND 2048),
  secret_ref TEXT,
  event_types TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(event_types)),
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
  id TEXT PRIMARY KEY,
  subscription_id TEXT NOT NULL REFERENCES webhook_subscriptions(id) ON DELETE RESTRICT,
  event_id TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'failed')),
  attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
  next_retry_at TEXT,
  last_error TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- ============ 学习/使用事件与 Agent 工具调用（只追加，6.3.1） ============

CREATE TABLE IF NOT EXISTS usage_events (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  event_type TEXT NOT NULL,
  payload_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(payload_json)),
  occurred_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS agent_tool_calls (
  id TEXT PRIMARY KEY,
  request_id TEXT NOT NULL,
  session_id TEXT NOT NULL REFERENCES agent_sessions(id) ON DELETE RESTRICT,
  agent TEXT NOT NULL,
  tool TEXT NOT NULL,
  args_hash TEXT NOT NULL,
  result_hash TEXT,
  permission_level TEXT NOT NULL DEFAULT 'L1' CHECK (permission_level IN ('L1', 'L2', 'L3', 'L4')),
  reason TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- ============ 内容安全扫描缓存 ============

CREATE TABLE IF NOT EXISTS content_scan_results (
  id TEXT PRIMARY KEY,
  ref_type TEXT NOT NULL,
  ref_id TEXT NOT NULL,
  content_hash TEXT NOT NULL,
  result_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(result_json)),
  scanned_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- ============ 附件与设备（6.3.1：attachments.sha256 唯一） ============
-- 路径遵循 6.4.2：attachments/<sha256 前两位>/<sha256>，禁止 .. 与绝对路径。

CREATE TABLE IF NOT EXISTS attachments (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  sha256 TEXT NOT NULL CHECK (length(sha256) = 64),
  path TEXT NOT NULL CHECK (path NOT LIKE '/%' AND path NOT LIKE '%..%'),
  size_bytes INTEGER NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
  mime_type TEXT NOT NULL DEFAULT '',
  ref_count INTEGER NOT NULL DEFAULT 0 CHECK (ref_count >= 0),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  deleted_at TEXT,
  UNIQUE (sha256)
);

CREATE TABLE IF NOT EXISTS devices (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  device_name TEXT NOT NULL,
  platform TEXT NOT NULL DEFAULT '',
  last_seen_at TEXT,
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- ============ 既有表冲突处理（唯一）：review_events 列扩展 ============
-- 0001 结构 (id, review_card_id, rating, previous_json, current_json, created_at)
-- 6.2.1 期望 (id, user_id, review_card_id, rating, interval_days, due_at, reviewed_at)
-- 用 ALTER 追加列，历史行保持 NULL / 默认 0；不破坏 P0–P4 复习写入逻辑。
ALTER TABLE review_events ADD COLUMN user_id TEXT REFERENCES users(id);
ALTER TABLE review_events ADD COLUMN interval_days INTEGER NOT NULL DEFAULT 0;
ALTER TABLE review_events ADD COLUMN due_at TEXT;
ALTER TABLE review_events ADD COLUMN reviewed_at TEXT;

-- ============ 6.3.1 扩展索引（仅作用于 0005 新表与 review_events 扩展列） ============

CREATE INDEX IF NOT EXISTS idx_notes_user_kind ON notes(user_id, kind) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_notes_user_updated ON notes(user_id, updated_at) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_note_annotations_document ON note_annotations(document_id);
CREATE INDEX IF NOT EXISTS idx_flashcards_user_due_state ON flashcards(user_id, due_at, state) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_flashcards_user_source ON flashcards(user_id, source) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_flashcard_reviews_card_reviewed ON flashcard_reviews(flashcard_id, reviewed_at);
CREATE INDEX IF NOT EXISTS idx_exam_papers_user_status ON exam_papers(user_id, status);
CREATE INDEX IF NOT EXISTS idx_exams_user_status ON exams(user_id, status);
CREATE INDEX IF NOT EXISTS idx_exams_paper ON exams(paper_id);
CREATE INDEX IF NOT EXISTS idx_user_achievements_user ON user_achievements(user_id);
CREATE INDEX IF NOT EXISTS idx_reports_user_period ON reports(user_id, period, period_start);
CREATE INDEX IF NOT EXISTS idx_timer_sessions_user_started ON timer_sessions(user_id, started_at);
CREATE INDEX IF NOT EXISTS idx_reminders_user_kind_next ON reminders(user_id, kind, next_trigger_at);
CREATE INDEX IF NOT EXISTS idx_notifications_user_read ON notifications(user_id, read_at);
CREATE INDEX IF NOT EXISTS idx_favorites_user_group ON favorites(user_id, group_name);
CREATE INDEX IF NOT EXISTS idx_read_later_user_status ON read_later(user_id, status);
CREATE INDEX IF NOT EXISTS idx_classes_owner ON classes(owner_user_id);
CREATE INDEX IF NOT EXISTS idx_class_members_class ON class_members(class_id);
CREATE INDEX IF NOT EXISTS idx_assignments_class_status ON assignments(class_id, status);
CREATE INDEX IF NOT EXISTS idx_assignment_submissions_assignment ON assignment_submissions(assignment_id);
CREATE INDEX IF NOT EXISTS idx_grading_appeals_status ON grading_appeals(status);
CREATE INDEX IF NOT EXISTS idx_review_queue_items_status ON review_queue_items(status);
CREATE INDEX IF NOT EXISTS idx_usage_events_user_type_occurred ON usage_events(user_id, event_type, occurred_at);
CREATE INDEX IF NOT EXISTS idx_attachments_workspace_deleted ON attachments(workspace_id, deleted_at);
CREATE INDEX IF NOT EXISTS idx_devices_workspace_status ON devices(workspace_id, status);
-- review_events 新索引（与 0001 既有 idx_review_events_card_created 不同名，避免冲突）。
CREATE INDEX IF NOT EXISTS idx_review_events_user_due ON review_events(user_id, due_at);
CREATE INDEX IF NOT EXISTS idx_review_events_card_reviewed ON review_events(review_card_id, reviewed_at);
