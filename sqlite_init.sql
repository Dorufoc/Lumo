PRAGMA foreign_keys = ON;
PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 5000;
PRAGMA synchronous = NORMAL;

BEGIN IMMEDIATE;

CREATE TABLE IF NOT EXISTS schema_migrations (
  version TEXT PRIMARY KEY,
  applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  checksum TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS workspaces (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 120),
  owner_type TEXT NOT NULL CHECK (owner_type IN ('guest', 'local', 'cloud')),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  deleted_at TEXT,
  version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0)
);

CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  display_name TEXT NOT NULL CHECK (length(trim(display_name)) BETWEEN 1 AND 80),
  role TEXT NOT NULL DEFAULT 'student' CHECK (role IN ('student', 'teacher', 'admin')),
  preferences_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  deleted_at TEXT,
  version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0)
);

CREATE TABLE IF NOT EXISTS knowledge_nodes (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  parent_id TEXT REFERENCES knowledge_nodes(id) ON DELETE RESTRICT,
  name TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 160),
  node_path TEXT NOT NULL,
  level INTEGER NOT NULL DEFAULT 0 CHECK (level >= 0),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  deleted_at TEXT,
  version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
  UNIQUE (workspace_id, node_path)
);

CREATE TABLE IF NOT EXISTS learning_goals (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  name TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 160),
  subject TEXT NOT NULL DEFAULT '',
  exam_at TEXT,
  target_score REAL CHECK (target_score IS NULL OR target_score >= 0),
  daily_minutes INTEGER NOT NULL CHECK (daily_minutes BETWEEN 1 AND 1440),
  available_weekdays_json TEXT NOT NULL DEFAULT '[]',
  knowledge_ids_json TEXT NOT NULL DEFAULT '[]',
  status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'paused', 'completed', 'archived')),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  deleted_at TEXT,
  version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0)
);

CREATE TABLE IF NOT EXISTS plan_tasks (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  goal_id TEXT REFERENCES learning_goals(id) ON DELETE RESTRICT,
  task_type TEXT NOT NULL CHECK (task_type IN ('practice', 'review', 'read', 'exam')),
  due_at TEXT NOT NULL,
  duration_min INTEGER NOT NULL CHECK (duration_min BETWEEN 1 AND 1440),
  priority INTEGER NOT NULL DEFAULT 0 CHECK (priority BETWEEN 0 AND 100),
  status TEXT NOT NULL DEFAULT 'planned' CHECK (status IN ('planned', 'available', 'in_progress', 'completed', 'skipped')),
  reason_codes_json TEXT NOT NULL DEFAULT '[]',
  generated_reason TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  deleted_at TEXT,
  version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0)
);

CREATE TABLE IF NOT EXISTS questions (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  type TEXT NOT NULL CHECK (type IN ('single_choice', 'multiple_choice', 'fill_blank', 'short_answer', 'code')),
  status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'reviewed', 'published', 'archived')),
  source TEXT NOT NULL DEFAULT 'manual',
  tags_json TEXT NOT NULL DEFAULT '[]',
  content_hash TEXT NOT NULL CHECK (length(content_hash) = 64),
  current_version_id TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  deleted_at TEXT,
  version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
  UNIQUE (workspace_id, content_hash)
);

CREATE TABLE IF NOT EXISTS question_versions (
  id TEXT PRIMARY KEY,
  question_id TEXT NOT NULL REFERENCES questions(id) ON DELETE RESTRICT,
  version_no INTEGER NOT NULL CHECK (version_no > 0),
  payload_json TEXT NOT NULL CHECK (json_valid(payload_json)),
  generated_by_model TEXT,
  prompt_version TEXT,
  review_status TEXT NOT NULL DEFAULT 'pending' CHECK (review_status IN ('pending', 'approved', 'rejected')),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  UNIQUE (question_id, version_no)
);

CREATE TABLE IF NOT EXISTS question_knowledge (
  question_version_id TEXT NOT NULL REFERENCES question_versions(id) ON DELETE RESTRICT,
  knowledge_id TEXT NOT NULL REFERENCES knowledge_nodes(id) ON DELETE RESTRICT,
  weight REAL NOT NULL DEFAULT 1 CHECK (weight > 0 AND weight <= 1),
  PRIMARY KEY (question_version_id, knowledge_id)
);

CREATE TABLE IF NOT EXISTS import_batches (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  idempotency_key TEXT NOT NULL,
  file_name TEXT NOT NULL,
  file_hash TEXT NOT NULL CHECK (length(file_hash) = 64),
  format TEXT NOT NULL CHECK (format IN ('markdown', 'json', 'text')),
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'validating', 'ready', 'committed', 'failed')),
  total_count INTEGER NOT NULL DEFAULT 0 CHECK (total_count >= 0),
  valid_count INTEGER NOT NULL DEFAULT 0 CHECK (valid_count >= 0),
  error_count INTEGER NOT NULL DEFAULT 0 CHECK (error_count >= 0),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  UNIQUE (workspace_id, idempotency_key),
  UNIQUE (workspace_id, file_hash)
);

CREATE TABLE IF NOT EXISTS import_batch_items (
  id TEXT PRIMARY KEY,
  batch_id TEXT NOT NULL REFERENCES import_batches(id) ON DELETE RESTRICT,
  item_no INTEGER NOT NULL CHECK (item_no > 0),
  payload_json TEXT NOT NULL CHECK (json_valid(payload_json)),
  status TEXT NOT NULL CHECK (status IN ('valid', 'invalid', 'imported')),
  error_json TEXT,
  question_id TEXT REFERENCES questions(id) ON DELETE RESTRICT,
  UNIQUE (batch_id, item_no)
);

CREATE TABLE IF NOT EXISTS practice_sessions (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  mode TEXT NOT NULL CHECK (mode IN ('practice', 'review', 'exam')),
  status TEXT NOT NULL DEFAULT 'created' CHECK (status IN ('created', 'answering', 'submitted', 'graded', 'reviewed', 'abandoned')),
  question_snapshot_json TEXT NOT NULL CHECK (json_valid(question_snapshot_json)),
  time_limit_sec INTEGER CHECK (time_limit_sec IS NULL OR time_limit_sec > 0),
  started_at TEXT,
  submitted_at TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0)
);

CREATE TABLE IF NOT EXISTS submissions (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES practice_sessions(id) ON DELETE RESTRICT,
  question_version_id TEXT NOT NULL REFERENCES question_versions(id) ON DELETE RESTRICT,
  attempt_no INTEGER NOT NULL DEFAULT 1 CHECK (attempt_no > 0),
  answer_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(answer_json)),
  status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'submitted', 'withdrawn')),
  client_sequence INTEGER NOT NULL DEFAULT 0 CHECK (client_sequence >= 0),
  submitted_at TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  UNIQUE (session_id, question_version_id, attempt_no)
);

CREATE TABLE IF NOT EXISTS grading_results (
  id TEXT PRIMARY KEY,
  submission_id TEXT NOT NULL REFERENCES submissions(id) ON DELETE RESTRICT,
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'completed', 'failed', 'needs_review')),
  score REAL CHECK (score IS NULL OR score >= 0),
  max_score REAL NOT NULL CHECK (max_score > 0),
  method TEXT NOT NULL CHECK (method IN ('rule', 'rubric_llm', 'code_runner', 'manual')),
  confidence REAL CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1)),
  rule_version TEXT,
  matched_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(matched_json)),
  reason TEXT NOT NULL DEFAULT '',
  needs_review INTEGER NOT NULL DEFAULT 0 CHECK (needs_review IN (0, 1)),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  CHECK (score IS NULL OR score <= max_score)
);

CREATE TABLE IF NOT EXISTS wrong_answers (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  submission_id TEXT NOT NULL REFERENCES submissions(id) ON DELETE RESTRICT,
  question_version_id TEXT NOT NULL REFERENCES question_versions(id) ON DELETE RESTRICT,
  answer_json TEXT NOT NULL CHECK (json_valid(answer_json)),
  cause TEXT NOT NULL DEFAULT 'unknown' CHECK (cause IN ('unknown', 'concept', 'reading', 'calculation', 'memory', 'method', 'expression')),
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'mastered', 'archived')),
  latest_grading_id TEXT REFERENCES grading_results(id) ON DELETE RESTRICT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  deleted_at TEXT,
  version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
  UNIQUE (submission_id)
);

CREATE TABLE IF NOT EXISTS review_cards (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  wrong_answer_id TEXT NOT NULL REFERENCES wrong_answers(id) ON DELETE RESTRICT,
  repetition INTEGER NOT NULL DEFAULT 0 CHECK (repetition >= 0),
  interval_days INTEGER NOT NULL DEFAULT 0 CHECK (interval_days >= 0),
  ease_factor REAL NOT NULL DEFAULT 2.5 CHECK (ease_factor >= 1.3 AND ease_factor <= 3.5),
  due_at TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'completed')),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
  UNIQUE (wrong_answer_id)
);

CREATE TABLE IF NOT EXISTS review_events (
  id TEXT PRIMARY KEY,
  review_card_id TEXT NOT NULL REFERENCES review_cards(id) ON DELETE RESTRICT,
  rating TEXT NOT NULL CHECK (rating IN ('again', 'hard', 'good')),
  previous_json TEXT NOT NULL CHECK (json_valid(previous_json)),
  current_json TEXT NOT NULL CHECK (json_valid(current_json)),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS agent_sessions (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  agent TEXT NOT NULL CHECK (agent IN ('router', 'planner', 'profiler', 'tutor', 'grader', 'diagnoser', 'librarian', 'ocr', 'variator', 'auditor', 'interviewer', 'coach')),
  status TEXT NOT NULL DEFAULT 'created' CHECK (status IN ('created', 'streaming', 'completed', 'failed', 'cancelled')),
  request_id TEXT,
  context_version TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS agent_messages (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES agent_sessions(id) ON DELETE RESTRICT,
  role TEXT NOT NULL CHECK (role IN ('system', 'user', 'assistant', 'tool')),
  content_summary TEXT NOT NULL DEFAULT '',
  content_ref TEXT,
  sequence_no INTEGER NOT NULL CHECK (sequence_no >= 0),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  UNIQUE (session_id, sequence_no)
);

CREATE TABLE IF NOT EXISTS agent_memory (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  memory_type TEXT NOT NULL CHECK (memory_type IN ('preference', 'learning_pattern', 'knowledge_gap', 'summary')),
  summary TEXT NOT NULL,
  source_ref TEXT,
  consent INTEGER NOT NULL DEFAULT 0 CHECK (consent IN (0, 1)),
  expires_at TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  deleted_at TEXT,
  version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0)
);

CREATE TABLE IF NOT EXISTS documents (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  relative_path TEXT NOT NULL CHECK (relative_path NOT LIKE '/%' AND relative_path NOT LIKE '%..%'),
  file_name TEXT NOT NULL,
  mime_type TEXT NOT NULL,
  byte_size INTEGER NOT NULL CHECK (byte_size >= 0),
  sha256 TEXT NOT NULL CHECK (length(sha256) = 64),
  encrypted INTEGER NOT NULL DEFAULT 0 CHECK (encrypted IN (0, 1)),
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'parsing', 'indexed', 'failed', 'deleted')),
  failure_reason TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  deleted_at TEXT,
  version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
  UNIQUE (workspace_id, sha256)
);

CREATE TABLE IF NOT EXISTS document_chunks (
  id TEXT PRIMARY KEY,
  document_id TEXT NOT NULL REFERENCES documents(id) ON DELETE RESTRICT,
  text_ref TEXT NOT NULL,
  section_name TEXT,
  page_no INTEGER CHECK (page_no IS NULL OR page_no > 0),
  paragraph_no INTEGER CHECK (paragraph_no IS NULL OR paragraph_no >= 0),
  start_offset INTEGER NOT NULL CHECK (start_offset >= 0),
  end_offset INTEGER NOT NULL CHECK (end_offset >= start_offset),
  embedding_version TEXT NOT NULL,
  vector_ref TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS provider_calls (
  id TEXT PRIMARY KEY,
  request_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  provider TEXT NOT NULL,
  model TEXT NOT NULL,
  prompt_version TEXT,
  input_hash TEXT NOT NULL,
  input_tokens INTEGER NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
  output_tokens INTEGER NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
  cost_micros INTEGER NOT NULL DEFAULT 0 CHECK (cost_micros >= 0),
  duration_ms INTEGER NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
  result_code TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  UNIQUE (request_id)
);

CREATE TABLE IF NOT EXISTS audit_events (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  actor_id TEXT REFERENCES users(id) ON DELETE RESTRICT,
  action TEXT NOT NULL,
  entity_type TEXT NOT NULL,
  entity_id TEXT,
  request_id TEXT,
  payload_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(payload_json)),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS sync_operations (
  operation_id TEXT PRIMARY KEY,
  device_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  entity_type TEXT NOT NULL,
  entity_id TEXT NOT NULL,
  base_version INTEGER NOT NULL CHECK (base_version >= 0),
  operation TEXT NOT NULL CHECK (operation IN ('create', 'update', 'delete')),
  payload_json TEXT NOT NULL CHECK (json_valid(payload_json)),
  state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'pushing', 'accepted', 'conflict', 'rejected')),
  server_sequence INTEGER,
  retry_count INTEGER NOT NULL DEFAULT 0 CHECK (retry_count >= 0),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_users_workspace ON users(workspace_id, deleted_at);
CREATE INDEX IF NOT EXISTS idx_goals_user_status_exam ON learning_goals(user_id, status, exam_at) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_plan_tasks_user_due_status ON plan_tasks(user_id, due_at, status) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_questions_workspace_status_type ON questions(workspace_id, status, type) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_question_knowledge_knowledge ON question_knowledge(knowledge_id, question_version_id);
CREATE INDEX IF NOT EXISTS idx_import_items_batch_status ON import_batch_items(batch_id, status);
CREATE INDEX IF NOT EXISTS idx_sessions_user_status ON practice_sessions(user_id, status, updated_at);
CREATE INDEX IF NOT EXISTS idx_submissions_session_question ON submissions(session_id, question_version_id);
CREATE INDEX IF NOT EXISTS idx_grading_submission_created ON grading_results(submission_id, created_at);
CREATE INDEX IF NOT EXISTS idx_wrong_answers_user_status ON wrong_answers(user_id, status, question_version_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_review_cards_user_due ON review_cards(user_id, due_at) WHERE status = 'active';
CREATE INDEX IF NOT EXISTS idx_review_events_card_created ON review_events(review_card_id, created_at);
CREATE INDEX IF NOT EXISTS idx_agent_sessions_user_status ON agent_sessions(user_id, status, updated_at);
CREATE INDEX IF NOT EXISTS idx_agent_messages_session_sequence ON agent_messages(session_id, sequence_no);
CREATE INDEX IF NOT EXISTS idx_documents_workspace_status ON documents(workspace_id, status, updated_at) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_document_chunks_document_embedding ON document_chunks(document_id, embedding_version);
CREATE INDEX IF NOT EXISTS idx_audit_workspace_created ON audit_events(workspace_id, created_at);
CREATE INDEX IF NOT EXISTS idx_audit_actor_created ON audit_events(actor_id, created_at);
CREATE INDEX IF NOT EXISTS idx_sync_workspace_state_created ON sync_operations(workspace_id, state, created_at);

CREATE TRIGGER IF NOT EXISTS trg_questions_current_version_insert
AFTER INSERT ON question_versions
BEGIN
  UPDATE questions
  SET current_version_id = NEW.id,
      updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
      version = version + 1
  WHERE id = NEW.question_id;
END;

CREATE TRIGGER IF NOT EXISTS trg_question_versions_immutable_update
BEFORE UPDATE ON question_versions
BEGIN
  SELECT RAISE(ABORT, 'question_versions are immutable');
END;

CREATE TRIGGER IF NOT EXISTS trg_question_versions_immutable_delete
BEFORE DELETE ON question_versions
BEGIN
  SELECT RAISE(ABORT, 'question_versions cannot be deleted');
END;

CREATE TRIGGER IF NOT EXISTS trg_workspace_parent_knowledge
BEFORE INSERT ON knowledge_nodes
WHEN NEW.parent_id IS NOT NULL
 AND NOT EXISTS (
   SELECT 1 FROM knowledge_nodes parent
   WHERE parent.id = NEW.parent_id AND parent.workspace_id = NEW.workspace_id AND parent.deleted_at IS NULL
 )
BEGIN
  SELECT RAISE(ABORT, 'knowledge parent must belong to the same workspace');
END;

CREATE TRIGGER IF NOT EXISTS trg_score_not_over_max
BEFORE INSERT ON grading_results
WHEN NEW.score IS NOT NULL AND NEW.score > NEW.max_score
BEGIN
  SELECT RAISE(ABORT, 'score cannot exceed max_score');
END;

INSERT OR IGNORE INTO schema_migrations (version, checksum)
VALUES ('0001_initial', 'lumo-v2-initial-schema');

COMMIT;
