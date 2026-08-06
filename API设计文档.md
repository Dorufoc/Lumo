# Lumo AI V2.0 API 设计文档

> 版本：V1.0  
> 适用范围：Wails v3 桌面绑定、应用事件、可选云同步 HTTP API  
> 关联设计：[完整设计文档.md](完整设计文档.md)

---

## 1. 设计约定

### 1.1 接口分层

| 层级 | 用途 | 传输方式 |
|---|---|---|
| 桌面绑定 | Vue 调用 Go 应用服务 | Wails binding |
| 应用事件 | 流式 AI、导入、异步评分进度 | Wails event |
| 云同步 API | 设备注册、变更推拉、备份 | HTTPS JSON |

> **V2.0 实施形态（2026-08 基线）**：项目以 Web 形态实现——Go 应用服务通过 `POST /api/v1/{Method}` 方法名式 RPC 暴露（请求体为方法参数对象，响应为统一信封），事件流通过 `GET /api/v1/events`（SSE）推送，文件通过 `GET /api/v1/files` 下载；Vue3 前端以同一套方法名调用。服务层（`internal/service`）不依赖 HTTP，未来桌面端以 WebView 独立窗口渲染同一前端，或替换传输层为 Wails 绑定时零改动复用。

前端不得访问 SQLite、文件系统路径、模型密钥或第三方模型 API。所有命令型操作由 Go 应用服务校验工作区归属、资源状态、乐观锁版本和幂等键。

### 1.2 通用响应

所有桌面绑定与 HTTP 成功响应均采用以下形式：

```json
{
  "data": {},
  "error": null,
  "request_id": "01JABCDEF0123456789ABCDEFG"
}
```

失败响应：

```json
{
  "data": null,
  "error": {
    "code": "INVALID_STATE",
    "message": "当前练习会话不能提交",
    "retryable": false,
    "details": {"current_status": "graded"}
  },
  "request_id": "01JABCDEF0123456789ABCDEFG"
}
```

### 1.3 通用字段

| 字段 | 规则 |
|---|---|
| `id` | UUID 或 ULID 字符串 |
| `workspace_id` | 本地工作区 ID，所有资源请求必填 |
| `request_id` | 客户端可选传入；服务端始终回传 |
| `version` | 可编辑资源的乐观锁版本；更新命令必填 |
| `idempotency_key` | 创建、提交、导入、同步等命令必填 |
| `created_at` / `updated_at` | UTC RFC 3339 时间字符串 |
| `deleted_at` | 软删除时间；正常资源为 `null` |

请求中日期使用 RFC 3339；时长单位为分钟；金额为最小货币单位；枚举使用小写下划线。未知字段必须忽略，未知枚举必须返回 `INVALID_ARGUMENT`。

### 1.4 错误码

| 错误码 | 含义 | 可重试 |
|---|---|---|
| `INVALID_ARGUMENT` | 参数、格式或枚举非法 | 否 |
| `UNAUTHORIZED` | 令牌失效或未认证 | 登录后 |
| `FORBIDDEN` | 工作区或资源无访问权 | 否 |
| `NOT_FOUND` | 资源不存在或已软删除 | 否 |
| `CONFLICT` | 版本、幂等键或同步冲突 | 处理后 |
| `INVALID_STATE` | 状态迁移不允许 | 否 |
| `DATABASE_UNAVAILABLE` | 数据库锁定或不可用 | 是 |
| `IMPORT_FAILED` | 题库或资料解析失败 | 修正后 |
| `PROVIDER_TIMEOUT` | AI Provider 超时 | 是 |
| `PROVIDER_RATE_LIMITED` | AI Provider 限流 | 延迟后 |
| `OUTPUT_INVALID` | 模型输出不符合 Schema | 是 |
| `SANDBOX_LIMIT` | 代码执行资源超限 | 修改后 |
| `REQUEST_CANCELLED` | 流式请求被用户取消 | 重新发起 |

> 补充：Web 传输层新增 `NETWORK`（前端无法连接本地服务）与 `STREAM_FAILED`（SSE 中断）两类前端侧错误标记，`retryable` 均为 true。

---

## 2. 桌面绑定 API

### 2.1 工作区、用户与数据管理

| 绑定方法 | 请求 | 返回 | 说明 |
|---|---|---|---|
| `WorkspaceCreate` | `name`, `owner_type`, `idempotency_key` | `Workspace` | 创建本地工作区（自动创建默认学生用户） |
| `WorkspaceGet` | `workspace_id` | `Workspace` | 获取工作区摘要 |
| `WorkspaceList` | - | `Workspace[]` | 列出全部工作区（引导页选择） |
| `WorkspaceDeletePrepare` | `workspace_id` | `ConfirmToken` | 生成删除确认令牌（HMAC 无状态，5 分钟窗口，格式 `ts.hex`） |
| `WorkspaceDelete` | `workspace_id`, `version`, `confirm_token` | `DeleteResult` | 软删除并进入清理队列（先调用 Prepare 取令牌） |
| `UserCreate` | `workspace_id`, `display_name`, `role` | `UserProfile` | 创建工作区内用户（默认用户在 WorkspaceCreate 时自动创建） |
| `UserList` | `workspace_id` | `UserProfile[]` | 列出工作区全部用户 |
| `UserGetProfile` | `workspace_id`, `user_id` | `UserProfile` | 获取学生资料 |
| `UserUpdateProfile` | `workspace_id`, `user_id`, `version`, `display_name`, `preferences` | `UserProfile` | 更新资料与偏好 |
| `BackupCreate` | `workspace_id`, `password`, `idempotency_key` | `BackupResult` | 创建加密备份（一致性快照 + AES-256-GCM） |
| `BackupRestore` | `backup_path`, `password`, `target_workspace_id` | `RestoreResult` | 恢复到工作区（完整性校验 + 保护性备份 + 原子替换） |
| `DataExport` | `workspace_id`, `scope`, `format` | `ExportResult` | 导出用户数据（下载经 `GET /api/v1/files`） |

`Workspace`：`id`、`name`、`owner_type`、`created_at`、`updated_at`、`version`。`scope` 允许 `all`、`questions`、`learning_records`、`documents`；导出结果只返回本地相对路径，不返回绝对路径。

### 2.2 学习目标与计划

| 绑定方法 | 请求 | 返回 |
|---|---|---|
| `GoalCreate` | `workspace_id`, `user_id`, `name`, `subject`, `exam_at`, `target_score`, `daily_minutes`, `available_weekdays`, `knowledge_ids`, `idempotency_key` | `LearningGoal` |
| `GoalList` | `workspace_id`, `user_id`, `status` | `LearningGoal[]` |
| `GoalUpdate` | `workspace_id`, `id`, `version`, 可编辑目标字段 | `LearningGoal` |
| `GoalTransition` | `workspace_id`, `id`, `version`, `action` | `LearningGoal` |
| `PlanGenerate` | `workspace_id`, `goal_id`, `range_start`, `range_end`, `idempotency_key` | `PlanTask[]` |
| `PlanListToday` | `workspace_id`, `user_id`, `date` | `PlanTask[]` |
| `PlanTaskTransition` | `workspace_id`, `id`, `version`, `action`, `reason` | `PlanTask` |

`GoalTransition.action` 允许 `activate`、`pause`、`complete`、`archive`。`PlanTaskTransition.action` 允许 `start`、`complete`、`skip`、`restore`。计划生成返回的每项任务必须带 `reason_codes` 与 `generated_reason`。

### 2.3 题库与知识点

| 绑定方法 | 请求 | 返回 |
|---|---|---|
| `LibraryUpload` | multipart：`file` 字段 | `UploadedFile`（`path`/`file_name`/`size`/`sha256`） |
| `LibraryPreflightImport` | `workspace_id`, `file_path`, `format`, `idempotency_key` | `ImportPreview` |
| `LibraryCommitImport` | `workspace_id`, `batch_id`, `idempotency_key` | `ImportBatch` |
| `LibraryGetImportBatch` | `workspace_id`, `batch_id` | `ImportBatch` |
| `KnowledgeCreate` | `workspace_id`, `name`, `parent_id` | `KnowledgeNode` |
| `KnowledgeUpdate` | `workspace_id`, `knowledge_id`, `version`, `name` | `KnowledgeNode` |
| `KnowledgeDelete` | `workspace_id`, `knowledge_id`, `version` | `DeleteResult`（有子节点/被引用时拒绝） |
| `KnowledgeTreeGet` | `workspace_id` | `KnowledgeNode[]` |
| `QuestionList` | `workspace_id`, `type`, `status`, `knowledge_id`, `keyword`, `cursor`, `limit` | `QuestionPage` |
| `QuestionGet` | `workspace_id`, `question_id` | `Question` |
| `QuestionCreateDraft` | `workspace_id`, `payload`, `idempotency_key` | `Question` |
| `QuestionCreateVersion` | `workspace_id`, `question_id`, `version`, `payload`, `idempotency_key` | `QuestionVersion` |
| `QuestionTransition` | `workspace_id`, `question_id`, `version`, `action` | `Question` |

`payload` 包含 `type`、`stem`、`options`、`answer`、`analysis`、`difficulty`、`knowledge_ids`、`grading_config`、`rubric`、`source`、`tags`。判断题必须保存为 `single_choice`，选项固定为 `A 正确` 和 `B 错误`。`QuestionTransition.action` 允许 `review`、`publish`、`archive`；发布后不得更新现有版本。

### 2.4 练习、提交与评分

| 绑定方法 | 请求 | 返回 |
|---|---|---|
| `PracticeStart` | `workspace_id`, `user_id`, `mode`, `question_ids`, `time_limit_sec`, `idempotency_key` | `PracticeSession` |
| `PracticeGet` | `workspace_id`, `session_id` | `PracticeSession` |
| `PracticeSaveAnswer` | `workspace_id`, `session_id`, `question_version_id`, `answer`, `client_sequence`, `idempotency_key` | `SubmissionDraft` |
| `PracticeSkipQuestion` | `workspace_id`, `session_id`, `question_version_id` | `PracticeSession` |
| `PracticeSubmit` | `workspace_id`, `session_id`, `version`, `idempotency_key` | `PracticeResult` |
| `PracticeGetResult` | `workspace_id`, `session_id` | `PracticeResult` |
| `GradingGet` | `workspace_id`, `grading_id` | `GradingResult` |
| `GradingRequestReview` | `workspace_id`, `grading_id`, `reason`, `idempotency_key` | `GradingResult` |

`PracticeSession` 固定题目版本快照和题目顺序。`PracticeSubmit` 只允许 `answering` 状态；客观题立即返回评分，主观题和代码题返回 `pending` 评分结果。`PracticeResult` 包含总分、每题提交、评分、错题、到期复习动作和解析可见性。

### 2.5 错题与复习

| 绑定方法 | 请求 | 返回 |
|---|---|---|
| `WrongAnswerList` | `workspace_id`, `user_id`, `status`, `cause`, `knowledge_id`, `cursor`, `limit` | `WrongAnswerPage` |
| `WrongAnswerUpdateCause` | `workspace_id`, `id`, `version`, `cause` | `WrongAnswer` |
| `ReviewListDue` | `workspace_id`, `user_id`, `due_before`, `limit` | `ReviewCard[]` |
| `ReviewSubmit` | `workspace_id`, `review_card_id`, `rating`, `idempotency_key` | `ReviewCard` |
| `ReviewHistoryList` | `workspace_id`, `review_card_id`, `cursor`, `limit` | `ReviewEvent[]` |

`rating` 允许 `again`、`hard`、`good`；服务端返回更新后的 `repetition`、`interval_days`、`ease_factor`、`due_at`，不得由前端计算间隔。

### 2.6 Agent、资料与设置

| 绑定方法 | 请求 | 返回 |
|---|---|---|
| `AgentChatCreate` | `workspace_id`, `user_id`, `agent`, `context`, `idempotency_key` | `AgentSession` |
| `AgentChatSend` | `workspace_id`, `session_id`, `message`, `request_id` | `AgentRequest` |
| `AgentChatCancel` | `workspace_id`, `session_id`, `request_id` | `CancelResult` |
| `AgentSessionGet` | `workspace_id`, `session_id` | `AgentSession` |
| `AgentMemoryList` | `workspace_id`, `user_id` | `AgentMemory[]` |
| `AgentMemoryDelete` | `workspace_id`, `memory_id`, `version` | `DeleteResult` |
| `AgentSummarize` | `workspace_id`, `user_id`, `document_id`, `preferences`, `idempotency_key` | `AgentSummarizeResult` |
| `AgentQuizGen` | `workspace_id`, `user_id`, `document_ids`, `types`, `count`, `knowledge_ids`, `idempotency_key` | `AgentQuizGenResult` |
| `AgentDebug` | `workspace_id`, `user_id`, `language`, `code`, `error_output`, `test_cases`, `idempotency_key` | `AgentDebugResult` |
| `AgentEssayGrade` | `workspace_id`, `user_id`, `stem`, `rubric`, `essay`, `max_score`, `idempotency_key` | `AgentEssayGradeResult` |
| `DocumentImport` | `workspace_id`, `file_path`, `idempotency_key` | `Document` |
| `DocumentList` | `workspace_id`, `status`, `cursor`, `limit` | `DocumentPage` |
| `DocumentRetry` | `workspace_id`, `document_id`, `idempotency_key` | `Document` |
| `DocumentDelete` | `workspace_id`, `document_id`, `version` | `DeleteResult` |
| `RAGAsk` | `workspace_id`, `user_id`, `question`, `document_ids`, `request_id` | `AgentRequest` |
| `SettingsGet` | `workspace_id` | `Settings` |
| `SettingsUpdate` | `workspace_id`, `version`, `settings` | `Settings` |
| `ProviderTest` | `workspace_id`, `provider`, `model` | `ProviderHealth` |

`SettingsUpdate` 不接受 API 密钥回读字段。密钥仅通过系统凭据库写入；任何读取接口只能返回 `configured: true/false`。

### 2.7 统计、模型配置与同步（新增）

| 绑定方法 | 请求 | 返回 | 说明 |
|---|---|---|---|
| `DashboardGet` | `workspace_id`, `user_id` | `Dashboard` | 今日任务/到期复习/连续天数/近 7 天正确率/薄弱知识点/AI 建议 |
| `ProviderConfigure` | `workspace_id`, `provider`(`llm`/`embedding`), `kind`(`openai`/`mock`), `base_url`, `api_key`, `model`, `enabled` | `provider_status` | 写入 Provider 配置（密钥仅存本地 secrets 文件，不回读；api_key 留空保留旧值） |
| `ProviderClear` | `workspace_id`, `provider` | `provider_status` | 删除 Provider 配置 |
| `SyncDeviceRegister` | `device_id`, `device_name`, `platform`, `app_version` | `DeviceStatus` | 设备注册（本地模拟服务端，幂等） |
| `SyncPush` | `workspace_id` | `PushResult` | 推送本地 pending 变更队列（逐项 `accepted`/`duplicate`/`conflict`，冲突返回冲突副本） |
| `SyncPull` | `workspace_id`, `cursor`, `limit` | `PullResult` | 按游标拉取变更（客户端先应用再保存游标） |
| `SyncStatusGet` | `workspace_id` | `SyncStatus`（`pending_count`/`state`/`last_error`） | 同步状态 |

### 2.8 传输层端点（新增）

| 端点 | 说明 |
|---|---|
| `GET /api/v1/events?session_id=&request_id=` | SSE 事件流（`agent:delta` 等，`sequence_no` 严格递增，15s 心跳） |
| `GET /api/v1/files?path=` | 受限文件下载（仅 `exports/` 与 `uploads/` 相对路径，禁止 `..` 与绝对路径） |
| `POST /api/v1/{Method}` | 全部业务方法（`Content-Type: application/json`；multipart 用于 `LibraryUpload`） |

---

## 3. 应用事件

所有事件载荷包含 `request_id`、`session_id`、`sequence_no`、`occurred_at`。同一会话内 `sequence_no` 严格递增；前端按序合并，忽略过期序号。

| 事件 | 载荷 | 说明 |
|---|---|---|
| `agent:delta` | `delta`, `citations` | 文本增量与新增引用 |
| `agent:tool` | `tool_name`, `status`, `safe_summary` | 已授权工具执行状态 |
| `agent:completed` | `message_id`, `usage`, `citations` | 正常结束 |
| `agent:error` | `error` | 流式请求失败 |
| `import:progress` | `batch_id`, `processed`, `total`, `stage` | 题库或资料导入进度 |
| `grading:updated` | `grading_id`, `status`, `score` | 异步评分完成或失败 |
| `sync:status` | `pending_count`, `state`, `last_error` | 同步状态变化 |

`agent:tool` 不得包含检索全文、密钥、绝对路径或其他用户数据。用户取消后必须发送 `agent:error`，错误码为 `REQUEST_CANCELLED`，并停止后续增量。

---

## 4. 云同步 HTTP API

### 4.1 认证与版本

基础路径为 `/v1`。请求头包含：

```http
Authorization: Bearer <access_token>
X-Device-ID: <device_id>
X-Idempotency-Key: <operation_id>
Content-Type: application/json
```

所有响应附带 `X-Request-ID` 和 `Date`。服务端时间是冲突诊断依据，不使用客户端时钟决定覆盖顺序。

### 4.2 设备注册

`POST /v1/devices`

```json
{
  "device_id": "device-uuid",
  "device_name": "Windows Desktop",
  "platform": "windows",
  "app_version": "2.0.0"
}
```

响应返回设备状态、服务端时间和工作区同步游标。

### 4.3 推送变更

`POST /v1/sync/push`

```json
{
  "workspace_id": "workspace-uuid",
  "operations": [
    {
      "operation_id": "01JABCDEF0123456789ABCDEFG",
      "entity_type": "review_card",
      "entity_id": "card-uuid",
      "base_version": 3,
      "operation": "update",
      "payload": {},
      "created_at": "2026-08-04T00:00:00Z"
    }
  ]
}
```

响应逐项返回 `accepted`、`duplicate`、`conflict` 或 `rejected`，以及 `server_sequence`、`server_version` 和冲突副本。任一操作失败不能回滚已接受操作。

### 4.4 拉取变更

`GET /v1/sync/pull?workspace_id={id}&cursor={cursor}&limit=200`

响应返回 `operations`、`next_cursor`、`has_more` 与 `server_time`。客户端必须先在本地事务应用操作，再保存新游标。

### 4.5 备份与删除

`POST /v1/backups` 上传客户端已加密备份的元数据或分片；服务器不解密客户端端到端加密内容。`DELETE /v1/workspaces/{id}` 启动延迟删除，响应返回可撤销截止时间。删除期内同步接口必须拒绝新的写操作。

---

## 5. 核心数据结构

```json
{
  "LearningGoal": {
    "id": "goal-uuid",
    "workspace_id": "workspace-uuid",
    "user_id": "user-uuid",
    "name": "高等数学期末复习",
    "subject": "math",
    "exam_at": "2026-12-20T00:00:00Z",
    "target_score": 85,
    "daily_minutes": 60,
    "available_weekdays": [1, 2, 3, 4, 5],
    "status": "active",
    "version": 1
  }
}
```

```json
{
  "GradingResult": {
    "id": "grading-uuid",
    "submission_id": "submission-uuid",
    "status": "completed",
    "score": 8,
    "max_score": 10,
    "method": "rubric_llm",
    "confidence": 0.86,
    "rule_version": "rubric-v3",
    "reason": "已覆盖主要步骤，但缺少边界条件说明",
    "needs_review": false
  }
}
```

## 6. API 验收标准

- 每个命令型接口具备工作区归属、幂等和状态校验。
- 练习会话固定题目版本，重复提交不产生重复成绩或错题。
- 流式事件可取消、可排序，且不泄漏资料全文、密钥和绝对路径。
- 云同步支持重复投递、分页拉取和逐项冲突结果。
- API 返回稳定错误码，前端可根据 `retryable` 展示恢复动作。

---

## 7. 扩展 API 契约（V2.1 规划，🔜 P5–P6）

> 与《完整设计文档.md》V2.1 的 4.8–4.24 模块一一对应。所有命令型方法沿用 1.2 统一信封、1.3 通用字段（`workspace_id`/`version`/`idempotency_key`）与 1.4 错误码（含附录 A 新增码）。接口形态沿用 `POST /api/v1/{Method}`。

### 7.1 笔记与标注（4.8）

| 方法 | 请求要点 | 返回 |
|---|---|---|
| `NoteCreate` | `kind`, `title`, `body_md`, `source_ref`, `knowledge_ids`, `tags` | `Note` |
| `NoteUpdate` | `note_id`, `version`, 可编辑字段 | `Note` |
| `NoteList` | `kind`, `knowledge_id`, `tag`, `keyword`, `cursor`, `limit` | `NotePage` |
| `NoteDelete` | `note_id`, `version` | `DeleteResult` |
| `NoteToFlashcard` | `note_id`, `idempotency_key` | `Flashcard` |
| `AnnotationCreate` | `note_id`, `document_id`, `anchor_hash`, `offset_start/end`, `highlight_color` | `Annotation` |

### 7.2 闪卡（4.9）

| 方法 | 请求要点 | 返回 |
|---|---|---|
| `FlashcardCreate` | `source[knowledge|note|document|manual]`, `front`, `back`, `card_type`, `tags` | `Flashcard` |
| `FlashcardGenerate` | `source_ref`, `idempotency_key` | `Flashcard[]`（错题/笔记批量生成） |
| `FlashcardListDue` | `due_before`, `limit` | `Flashcard[]` |
| `FlashcardReview` | `flashcard_id`, `rating[again|hard|good]`, `idempotency_key` | `Flashcard`（服务端返回新间隔） |
| `FlashcardBatch` | `action[archive|delete|reset]`, `ids[]` | `BatchResult` |
| `FlashcardImportCsv` | `file_path`, `idempotency_key` | `ImportBatch`（行级错误） |
| `FlashcardExportAnki` | `idempotency_key` | `ExportResult`（`.apkg`） |

### 7.3 组卷与考试（4.10）

| 方法 | 请求要点 | 返回 |
|---|---|---|
| `ExamPaperCreate` | `title`, `config_json`, `idempotency_key` | `ExamPaper` |
| `ExamPaperAutoGenerate` | `title`, `config{knowledge_ratio, difficulty_dist, count, types}`, `idempotency_key` | `ExamPaper` |
| `ExamPaperPublish` | `paper_id`, `version` | `ExamPaper` |
| `ExamStart` | `paper_id`, `idempotency_key` | `Exam`（锁定题目顺序/时长） |
| `ExamAutoSubmit` | `exam_id` | `ExamResult`（倒计时到期自动提交） |
| `ExamGetResult` | `exam_id` | `ExamResult`（成绩/复盘/错题入队列） |

### 7.4 打卡、成就与习惯（4.11）

| 方法 | 请求要点 | 返回 |
|---|---|---|
| `CheckinCreate` | `idempotency_key` | `Checkin`（`user_id+date` 幂等） |
| `CheckinMakeup` | `date`（每月限 3 次） | `Checkin` |
| `AchievementList` | - | `AchievementView`（已解锁/未解锁） |
| `StreakGet` | - | `Streak`（连续天数/总打卡） |

### 7.5 报告（4.12）

| 方法 | 请求要点 | 返回 |
|---|---|---|
| `ReportGenerate` | `period[daily|weekly|monthly]`, `period_start`, `period_end`, `idempotency_key` | `Report`（异步，`report:ready` 事件） |
| `ReportList` | `period`, `cursor`, `limit` | `ReportPage` |
| `ReportExport` | `report_id`, `format[pdf|json]` | `ExportResult`（经 `GET /api/v1/files` 下载） |
| `InsightGet` | `user_id`, `dimension[knowledge|time|trend]` | `Insight` |

### 7.6 专注计时（4.13）

| 方法 | 请求要点 | 返回 |
|---|---|---|
| `TimerStart` | `mode[pomodoro|free]`, `planned_minutes`, `task_id`, `idempotency_key` | `TimerSession`（同用户单活动计时） |
| `TimerEnd` | `session_id`, `interrupt_reason` | `TimerSession` |
| `TimerStats` | `date_range` | `TimerStats`（时长/轮次/中断率） |

### 7.7 提醒与通知（4.14）

| 方法 | 请求要点 | 返回 |
|---|---|---|
| `ReminderUpsert` | `kind`, `rule_json`, `enabled` | `Reminder` |
| `ReminderTestSend` | `kind` | `TestResult` |
| `NotificationList` | `unread_only`, `cursor`, `limit` | `NotificationPage` |
| `NotificationMarkRead` | `ids[]` | `BatchResult` |

### 7.8 收藏与稍后读（4.15）

| 方法 | 请求要点 | 返回 |
|---|---|---|
| `FavoriteToggle` | `ref_type`, `ref_id`, `group_name` | `Favorite` |
| `FavoriteList` | `group_name`, `keyword`, `cursor`, `limit` | `FavoritePage` |
| `ReadLaterAdd` | `document_id` | `ReadLaterItem` |
| `ReadLaterTransition` | `item_id`, `action[read|skip|requeue]` | `ReadLaterItem` |
| `DocumentSummarize` | `document_id`, `idempotency_key` | `DocumentSummary`（异步） |

### 7.9 日历与目标拆解（4.16）

| 方法 | 请求要点 | 返回 |
|---|---|---|
| `CalendarGetMonth` | `month` | `CalendarMonth`（任务/复习/考试/打卡/专注/个人事件投影） |
| `CalendarEventUpsert` | `kind=personal`, 事件字段 | `CalendarEvent` |
| `MilestoneCreate` | `goal_id`, `title`, `due_at`, `criteria_json` | `Milestone` |
| `MilestoneEvaluate` | `milestone_id` | `Milestone`（服务端判定达成） |

### 7.10 家庭绑定（4.21）

| 方法 | 请求要点 | 返回 |
|---|---|---|
| `FamilyInviteCreate` | `idempotency_key` | `InviteCode`（24h 有效） |
| `FamilyBind` | `invite_code` | `FamilyBinding` |
| `FamilyUnbind` | `binding_id`, `version` | `DeleteResult` |
| `ParentSettingsUpdate` | `student_user_id`, `daily_limit_min`, `ai_disabled`, `report_enabled` | `ParentSettings` |

### 7.11 教师端（4.22）

| 方法 | 请求要点 | 返回 |
|---|---|---|
| `ClassCreate` | `name`, `subject`, `semester`, `idempotency_key` | `Class` |
| `ClassList` | `user_id` | `Class[]`（教师=创建班级；学生=加入班级） |
| `ClassGet` | `class_id`, `user_id` | `Class`（创建者或 active 成员） |
| `ClassUpdate` | `class_id`, `name?`, `subject?`, `semester?`（教师） | `Class` |
| `ClassArchive` | `class_id`（教师，active→archived） | `Class` |
| `ClassInvite` | `class_id` | `InviteCode`（每次调用重新生成 8 位码） |
| `ClassMemberAdd/Remove` | `class_id`, `student_user_id` | `Class` |
| `ClassMemberList` | `class_id`, `user_id` | `ClassMember[]`（含 display_name） |
| `AssignmentCreate` | `class_id`, `paper_id`, `title`, `due_at`, `grading_rule` | `Assignment` |
| `AssignmentPublish` | `assignment_id`, `version` | `Assignment`（版本冻结） |
| `AssignmentSubmit` | `assignment_id`, 答案（学生端） | `AssignmentSubmission` |
| `AssignmentList` | `workspace_id`, `user_id` | `Assignment[]`（教师=创建班级；学生=加入班级） |
| `AssignmentSubmissionList` | `workspace_id`, `user_id`, `assignment_id` | `AssignmentSubmission[]`（教师） |
| `AssignmentGrade` | `submission_id`, `grade_json`（教师） | `AssignmentSubmission` |
| `AppealCreate` | `grading_id`, `reason` | `Appeal` |
| `AppealResolve` | `appeal_id`, `decision` | `Appeal` |
| `ClassStats` | `class_id`, `assignment_id?` | `ClassStats`（完成率/均分/薄弱 Top） |

### 7.12 管理端（4.23）

| 方法 | 请求要点 | 返回 |
|---|---|---|
| `AdminReviewList` | `status`, `cursor`, `limit` | `ReviewQueuePage` |
| `AdminReviewDecide` | `item_id`, `decision[approved|rejected|taken_down]`, `reason`, `version` | `ReviewQueueItem` |
| `AdminProviderPolicySet` | `provider`, `model`, `allowed`, `daily_quota`, `monthly_budget` | `ProviderPolicy` |
| `AdminUserDisable` | `user_id`, `reason` | `UserStatus` |
| `AdminFeatureFlagSet` | `key`, `enabled`, `rollout_percent` | `FeatureFlag` |
| `AdminAuditList` | `actor_id?`, `action?`, `cursor`, `limit` | `AuditPage`（只追加、脱敏） |

### 7.13 插件、分享与 Webhook（4.20/4.24）

| 方法 | 请求要点 | 返回 |
|---|---|---|
| `PluginInstall` | `path` 或 `url`, `signature` | `Plugin`（权限声明确认） |
| `PluginSetEnabled` | `plugin_id`, `enabled` | `Plugin` |
| `PluginUninstall` | `plugin_id` | `DeleteResult` |
| `ShareCreate` | `ref_type`, `ref_id`, `ttl_days`, `idempotency_key` | `Share`（强制安全扫描） |
| `ShareRevoke` | `share_id` | `DeleteResult` |
| `WebhookSubscribe` | `url`, `event_types[]`, `idempotency_key` | `WebhookSubscription` |
| `WebhookTestSend` | `subscription_id` | `TestResult` |
| `WebhookDelete` | `subscription_id` | `DeleteResult` |

### 7.14 扩展事件（追加到第 3 节事件表）

| 事件 | 载荷 | 说明 |
|---|---|---|
| `report:ready` | `report_id`, `period`, `status` | 报告生成完成 |
| `exam:auto_submitted` | `exam_id`, `status` | 考试倒计时到期自动提交 |
| `flashcard:due` | `due_count` | 到期闪卡数变化（驱动提醒） |
| `reminder:triggered` | `kind`, `ref_type`, `ref_id` | 提醒触发（含桌面通知） |
| `grading:appeal` | `appeal_id`, `grading_id`, `status` | 申诉状态变化（教师端） |
| `sync:extended` | `entity_type`, `conflict_count` | 扩展对象同步冲突 |

### 7.15 扩展 API 验收标准

- 新增方法均遵循幂等、版本与工作区归属约定；`FEATURE_DISABLED`/`QUOTA_EXCEEDED`/`EXAM_IN_PROGRESS` 等新错误码在对应场景稳定返回。
- 教师/管理端方法仅对授权角色开放，越权返回 `FORBIDDEN` 且写入审计。
- 分享创建强制安全扫描，撤销后立即失效；Webhook 签名校验失败不投递。
- 报告/摘要等异步结果通过扩展事件通知，前端不轮询。

### 7.16 健康、语音与知识图谱（4.17–4.19）

| 方法 | 请求要点 | 返回 |
|---|---|---|
| `HealthSettingsUpdate` | `workspace_id`, `sedentary_enabled`, `eye_enabled`, `night_mode`, `blue_light_filter`, `stats_enabled` | `HealthSettings` |
| `HealthStatsGet` | `date_range` | `HealthStats`（久坐次数/休息完成率，仅开启时采集） |
| `TTSPlay` | `ref_type[question|note|flashcard|document]`, `ref_id`, `speed` | `TTSResult`（音频流或本地缓存引用） |
| `SpeakingSubmit` | `submission_id`, `audio_path`, `idempotency_key` | `SpeakingResult`（转写 + 分维度评分，异步 `grading:updated`） |
| `SpeakingResultGet` | `submission_id` | `SpeakingResult` |
| `KnowledgeGraphGet` | `workspace_id` | `KnowledgeGraph`（节点/边/掌握度；超限返回 Top-N） |
| `MasterySnapshotList` | `user_id`, `knowledge_id?`, `cursor` | `MasterySnapshotPage` |
| `MasteryExplain` | `user_id`, `knowledge_id` | `MasteryExplanation`（口径 + 证据样本） |

> `TTSPlay`/`SpeakingSubmit` 仅在启用对应 Provider 时可用；未配置时返回 `FEATURE_DISABLED` 且前端隐藏入口。
