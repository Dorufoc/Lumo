// Package app 是应用容器：装配仓储、服务与传输层处理器。
// 依赖方向：app -> service -> repository/domain，service 不依赖 HTTP 或 SQLite 驱动。
package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"lumo/internal/agent"
	"lumo/internal/config"
	"lumo/internal/database"
	"lumo/internal/domain"
	apphttp "lumo/internal/platform/http"
	"lumo/internal/provider"
	"lumo/internal/repository"
	"lumo/internal/service"
)

// App 持有所有依赖。
type App struct {
	Cfg   *config.Config
	DB    *sql.DB
	Svc   *service.Services
	Agent *agent.Service
}

// New 构造应用容器。
func New(cfg *config.Config, db *sql.DB) *App {
	repo := repository.New(db)
	a := &App{Cfg: cfg, DB: db, Svc: service.New(repo, cfg)}
	a.Agent = agent.New(repo)
	// 用户级领域事件总线共享：服务层（ReminderService 等）经 s.Svc.UserEvents 发布事件，
	// SSE 订阅端（srv.RegisterUserSSE(a.Agent.UserEvents)）读取 a.Agent.UserEvents。
	// 两个字段必须指向同一实例，否则调度器/测试发送发布的 reminder:triggered 等事件
	// 无法到达 SSE 订阅者。这里以服务层实例为准，agent 内部新建的实例被替换丢弃。
	a.Agent.UserEvents = a.Svc.UserEvents
	// Agent LLM 工厂：读取 secrets 配置（未配置返回 ErrNotConfigured）。
	// 回退兜底：主模型不可用 → mock 确定性模板，不阻塞本地学习闭环。
	a.Agent.LLMFactory = func() (provider.LLMProvider, error) {
		c, ok := a.Svc.ProviderConfigOf("llm")
		if !ok {
			return nil, provider.ErrNotConfigured
		}
		kind, _ := c["kind"].(string)
		if kind == "" {
			kind = "openai"
		}
		return buildLLMFallback(kind, c)
	}
	// Agent 发送前门禁：家长关闭 AI → FEATURE_DISABLED（Tutor/RAG 等 AI 入口统一生效）。
	a.Agent.BeforeChat = func(ctx context.Context, session *repository.AgentSessionRow) error {
		return a.Svc.EnforceParentAI(ctx, session.UserID)
	}
	// 简答/代码题异步评分接入 Agent。
	a.Svc.Grader = &agent.GradeSubmission{Repo: repo, Agent: a.Agent}
	a.Svc.Agent = a.Agent
	// Embedding 工厂：读取 secrets 配置（未配置返回 ErrNotConfigured）。
	// 回退兜底：主向量模型不可用 → mock 确定性哈希向量。
	a.Svc.EmbeddingFactory = func() (provider.EmbeddingProvider, error) {
		c, ok := a.Svc.ProviderConfigOf("embedding")
		if !ok {
			return nil, provider.ErrNotConfigured
		}
		kind, _ := c["kind"].(string)
		if kind == "" {
			kind = "openai"
		}
		return buildEmbeddingFallback(kind, c)
	}
	// 注入数据库替换回调（备份恢复用）：关闭旧连接 → 替换文件 → 打开新连接 → 迁移。
	a.Svc.SwapDB = func(newPath string) (*sql.DB, error) {
		if a.DB != nil {
			a.DB.Close()
		}
		if err := os.Rename(newPath, a.Cfg.DBPath); err != nil {
			// 替换失败：尝试恢复原连接，避免数据库不可用。
			if db2, e2 := database.Open(a.Cfg.DBPath); e2 == nil {
				a.DB = db2
			}
			return nil, err
		}
		_ = os.Remove(a.Cfg.DBPath + "-wal")
		_ = os.Remove(a.Cfg.DBPath + "-shm")
		db, err := database.Open(a.Cfg.DBPath)
		if err != nil {
			return nil, err
		}
		if err := database.Migrate(context.Background(), db); err != nil {
			db.Close()
			return nil, err
		}
		a.DB = db
		return db, nil
	}
	// 备份恢复互斥：阻止并发恢复/写入期间替换数据库。
	a.Svc.RestoreMu = &sync.Mutex{}
	return a
}

// RegisterHandlers 将全部业务方法注册到 HTTP 服务。
// 方法名与 API 设计文档绑定方法一一对应；未来 Wails 适配层可复用 Svc 直接绑定。
func (a *App) RegisterHandlers(srv *apphttp.Server) {
	bind0(srv, "AppInfo", func(ctx context.Context) (any, error) {
		return map[string]any{"name": "Lumo AI", "version": "2.0.0", "db": "ok"}, nil
	})

	// 2.1 工作区、用户与数据管理
	bind(srv, "WorkspaceCreate", a.Svc.Workspace.WorkspaceCreate)
	bind(srv, "WorkspaceGet", a.Svc.Workspace.WorkspaceGet)
	bind0(srv, "WorkspaceList", a.Svc.Workspace.WorkspaceList)
	bind(srv, "WorkspaceDeletePrepare", a.Svc.Workspace.WorkspaceDeletePrepare)
	bind(srv, "WorkspaceDelete", a.Svc.Workspace.WorkspaceDelete)
	bind(srv, "UserCreate", a.Svc.Workspace.UserCreate)
	bind(srv, "UserList", a.Svc.Workspace.UserList)
	bind(srv, "UserGetProfile", a.Svc.Workspace.UserGetProfile)
	bind(srv, "UserUpdateProfile", a.Svc.Workspace.UserUpdateProfile)
	bind(srv, "SettingsGet", a.Svc.Settings.SettingsGet)
	bind(srv, "SettingsUpdate", a.Svc.Settings.SettingsUpdate)
	bind(srv, "BackupCreate", a.Svc.Backup.BackupCreate)
	bind(srv, "BackupRestore", a.Svc.Backup.BackupRestore)
	bind(srv, "DataExport", a.Svc.Backup.DataExport)

	// 2.3 题库与知识点
	bind(srv, "KnowledgeCreate", a.Svc.Knowledge.KnowledgeCreate)
	bind(srv, "KnowledgeUpdate", a.Svc.Knowledge.KnowledgeUpdate)
	bind(srv, "KnowledgeDelete", a.Svc.Knowledge.KnowledgeDelete)
	bind(srv, "KnowledgeTreeGet", a.Svc.Knowledge.KnowledgeTreeGet)
	bind(srv, "QuestionCreateDraft", a.Svc.Knowledge.QuestionCreateDraft)
	bind(srv, "QuestionCreateVersion", a.Svc.Knowledge.QuestionCreateVersion)
	bind(srv, "QuestionTransition", a.Svc.Knowledge.QuestionTransition)
	bind(srv, "QuestionGet", a.Svc.Knowledge.QuestionGet)
	bind(srv, "QuestionList", a.Svc.Knowledge.QuestionList)

	// 2.3 题库导入（上传走 multipart）
	bind(srv, "LibraryPreflightImport", a.Svc.Import.LibraryPreflightImport)
	bind(srv, "LibraryCommitImport", a.Svc.Import.LibraryCommitImport)
	bind(srv, "LibraryGetImportBatch", a.Svc.Import.LibraryGetImportBatch)
	srv.RegisterUpload("LibraryUpload", a.handleLibraryUpload)

	// 2.2 学习目标与计划
	bind(srv, "GoalCreate", a.Svc.Goal.GoalCreate)
	bind(srv, "GoalList", a.Svc.Goal.GoalList)
	bind(srv, "GoalUpdate", a.Svc.Goal.GoalUpdate)
	bind(srv, "GoalTransition", a.Svc.Goal.GoalTransition)
	bind(srv, "PlanGenerate", a.Svc.Goal.PlanGenerate)
	bind(srv, "PlanListToday", a.Svc.Goal.PlanListToday)
	bind(srv, "PlanTaskTransition", a.Svc.Goal.PlanTaskTransition)

	// 2.4 练习、提交与评分
	bind(srv, "PracticeStart", a.Svc.Practice.PracticeStart)
	bind(srv, "PracticeGet", a.Svc.Practice.PracticeGet)
	bind(srv, "PracticeSaveAnswer", a.Svc.Practice.PracticeSaveAnswer)
	bind(srv, "PracticeSkipQuestion", a.Svc.Practice.PracticeSkipQuestion)
	bind(srv, "PracticeSubmit", a.Svc.Practice.PracticeSubmit)
	bind(srv, "PracticeGetResult", a.Svc.Practice.PracticeGetResult)
	bind(srv, "GradingGet", a.Svc.Practice.GradingGet)
	bind(srv, "GradingRequestReview", a.Svc.Practice.GradingRequestReview)

	// 2.5 错题与复习
	bind(srv, "WrongAnswerList", a.Svc.Review.WrongAnswerList)
	bind(srv, "WrongAnswerUpdateCause", a.Svc.Review.WrongAnswerUpdateCause)
	bind(srv, "ReviewListDue", a.Svc.Review.ReviewListDue)
	bind(srv, "ReviewSubmit", a.Svc.Review.ReviewSubmit)
	bind(srv, "ReviewHistoryList", a.Svc.Review.ReviewHistoryList)

	// 4.9 闪卡模块（API 文档 7.2）
	bind(srv, "FlashcardCreate", a.Svc.Flashcard.FlashcardCreate)
	bind(srv, "FlashcardGenerate", a.Svc.Flashcard.FlashcardGenerate)
	bind(srv, "FlashcardListDue", a.Svc.Flashcard.FlashcardListDue)
	bind(srv, "FlashcardReview", a.Svc.Flashcard.FlashcardReview)
	bind(srv, "FlashcardBatch", a.Svc.Flashcard.FlashcardBatch)
	bind(srv, "FlashcardImportCsv", a.Svc.Flashcard.FlashcardImportCsv)
	bind(srv, "FlashcardExportAnki", a.Svc.Flashcard.FlashcardExportAnki)

	// 组卷考试模块（API 文档 7.3）
	bind(srv, "ExamPaperList", a.Svc.Exam.ExamPaperList)
	bind(srv, "ExamPaperCreate", a.Svc.Exam.ExamPaperCreate)
	bind(srv, "ExamPaperAutoGenerate", a.Svc.Exam.ExamPaperAutoGenerate)
	bind(srv, "ExamPaperPublish", a.Svc.Exam.ExamPaperPublish)
	bind(srv, "ExamStart", a.Svc.Exam.ExamStart)
	bind(srv, "ExamAutoSubmit", a.Svc.Exam.ExamAutoSubmit)
	bind(srv, "ExamGetResult", a.Svc.Exam.ExamGetResult)

	// 打卡与成就（API 文档 7.4）
	bind(srv, "CheckinCreate", a.Svc.Checkin.CheckinCreate)
	bind(srv, "CheckinMakeup", a.Svc.Checkin.CheckinMakeup)
	bind(srv, "AchievementList", a.Svc.Checkin.AchievementList)
	bind(srv, "StreakGet", a.Svc.Checkin.StreakGet)

	// 专注计时（API 文档 7.6 / 完整设计文档 4.13）
	bind(srv, "TimerStart", a.Svc.Focus.TimerStart)
	bind(srv, "TimerEnd", a.Svc.Focus.TimerEnd)
	bind(srv, "TimerStats", a.Svc.Focus.TimerStats)

	// 日历与里程碑（API 文档 7.9 / 完整设计文档 4.16）
	bind(srv, "CalendarGetMonth", a.Svc.Calendar.CalendarGetMonth)
	bind(srv, "CalendarEventUpsert", a.Svc.Calendar.CalendarEventUpsert)
	bind(srv, "MilestoneCreate", a.Svc.Calendar.MilestoneCreate)
	bind(srv, "MilestoneEvaluate", a.Svc.Calendar.MilestoneEvaluate)

	// 家庭绑定与家长模式（API 文档 7.10 / 完整设计文档 4.21）
	bind(srv, "FamilyInviteCreate", a.Svc.Family.FamilyInviteCreate)
	bind(srv, "FamilyInviteGet", a.Svc.Family.FamilyInviteGet)
	bind(srv, "FamilyBind", a.Svc.Family.FamilyBind)
	bind(srv, "FamilyUnbind", a.Svc.Family.FamilyUnbind)
	bind(srv, "ParentSettingsUpdate", a.Svc.Family.ParentSettingsUpdate)
	bind(srv, "FamilyViewGet", a.Svc.Family.FamilyViewGet)

	// 知识图谱（API 文档 7.16 / 完整设计文档 4.19）
	bind(srv, "KnowledgeGraphGet", a.Svc.KnowledgeGraph.KnowledgeGraphGet)
	bind(srv, "MasterySnapshotList", a.Svc.KnowledgeGraph.MasterySnapshotList)
	bind(srv, "MasteryExplain", a.Svc.KnowledgeGraph.MasteryExplain)

	// 创作分享（API 文档 7.13 / 完整设计文档 4.20）
	bind(srv, "ShareCreate", a.Svc.Share.ShareCreate)
	bind(srv, "ShareRevoke", a.Svc.Share.ShareRevoke)
	bind(srv, "ShareResolve", a.Svc.Share.ShareResolve)

	// 班级管理（API 文档 7.11 / 完整设计文档 4.22）
	bind(srv, "ClassCreate", a.Svc.Classes.ClassCreate)
	bind(srv, "ClassList", a.Svc.Classes.ClassList)
	bind(srv, "ClassGet", a.Svc.Classes.ClassGet)
	bind(srv, "ClassUpdate", a.Svc.Classes.ClassUpdate)
	bind(srv, "ClassArchive", a.Svc.Classes.ClassArchive)
	bind(srv, "ClassInvite", a.Svc.Classes.ClassInvite)
	bind(srv, "ClassMemberAdd", a.Svc.Classes.ClassMemberAdd)
	bind(srv, "ClassMemberRemove", a.Svc.Classes.ClassMemberRemove)
	bind(srv, "ClassMemberList", a.Svc.Classes.ClassMemberList)

	// 作业（API 文档 7.11 / 完整设计文档 4.22）
	bind(srv, "AssignmentCreate", a.Svc.Assignments.AssignmentCreate)
	bind(srv, "AssignmentPublish", a.Svc.Assignments.AssignmentPublish)
	bind(srv, "AssignmentSubmit", a.Svc.Assignments.AssignmentSubmit)
	bind(srv, "AssignmentList", a.Svc.Assignments.AssignmentList)
	bind(srv, "AssignmentSubmissionList", a.Svc.Assignments.AssignmentSubmissionList)
	bind(srv, "AssignmentGrade", a.Svc.Assignments.AssignmentGrade)

	// 申诉复议（API 文档 7.11 / 设计文档 4.22 C7）
	bind(srv, "AppealCreate", a.Svc.Appeals.AppealCreate)
	bind(srv, "AppealResolve", a.Svc.Appeals.AppealResolve)
	bind(srv, "AppealList", a.Svc.Appeals.AppealList)

	// 教师统计（API 文档 7.11 / 设计文档 4.22 C6）
	bind(srv, "ClassStats", a.Svc.Stats.ClassStats)

	// 提醒与通知（API 文档 7.7 / 完整设计文档 4.14）
	bind(srv, "ReminderUpsert", a.Svc.Reminder.ReminderUpsert)
	bind(srv, "ReminderTestSend", a.Svc.Reminder.ReminderTestSend)
	bind(srv, "NotificationList", a.Svc.Reminder.NotificationList)
	bind(srv, "NotificationMarkRead", a.Svc.Reminder.NotificationMarkRead)

	// 健康与专注辅助（API 文档 7.16 / 完整设计文档 4.17）
	bind(srv, "HealthSettingsUpdate", a.Svc.Health.HealthSettingsUpdate)
	bind(srv, "HealthStatsGet", a.Svc.Health.HealthStatsGet)

	// 学习报告与洞察（API 文档 7.5 / 完整设计文档 4.12）
	bind(srv, "ReportGenerate", a.Svc.Report.ReportGenerate)
	bind(srv, "ReportList", a.Svc.Report.ReportList)
	bind(srv, "ReportExport", a.Svc.Report.ReportExport)
	bind(srv, "InsightGet", a.Svc.Report.InsightGet)

	// 收藏与稍后读（API 文档 7.8 / 完整设计文档 4.15）
	bind(srv, "FavoriteToggle", a.Svc.Favorites.FavoriteToggle)
	bind(srv, "FavoriteList", a.Svc.Favorites.FavoriteList)
	bind(srv, "ReadLaterAdd", a.Svc.Favorites.ReadLaterAdd)
	bind(srv, "ReadLaterTransition", a.Svc.Favorites.ReadLaterTransition)
	bind(srv, "DocumentSummarize", a.Svc.Favorites.DocumentSummarize)

	// 4.8 笔记与标注（API 文档 7.1）
	bind(srv, "NoteCreate", a.Svc.Note.NoteCreate)
	bind(srv, "NoteUpdate", a.Svc.Note.NoteUpdate)
	bind(srv, "NoteList", a.Svc.Note.NoteList)
	bind(srv, "NoteDelete", a.Svc.Note.NoteDelete)
	bind(srv, "NoteToFlashcard", a.Svc.Note.NoteToFlashcard)
	bind(srv, "AnnotationCreate", a.Svc.Note.AnnotationCreate)

	// 统计（新增 API：DashboardGet）
	bind(srv, "DashboardGet", a.Svc.Dashboard.DashboardGet)

	// 2.6 Agent 会话
	bind(srv, "AgentChatCreate", a.Agent.AgentChatCreate)
	bind(srv, "AgentChatSend", a.Agent.AgentChatSend)
	bind(srv, "AgentChatCancel", a.Agent.AgentChatCancel)
	bind(srv, "AgentSessionGet", a.Agent.AgentSessionGet)
	bind(srv, "AgentMemoryList", a.Agent.AgentMemoryList)
	bind(srv, "AgentMemoryDelete", a.Agent.AgentMemoryDelete)
	bind(srv, "AgentSummarize", a.Svc.AgentTasks.AgentSummarize)
	bind(srv, "AgentQuizGen", a.Svc.AgentTasks.AgentQuizGen)
	bind(srv, "AgentDebug", a.Svc.AgentTasks.AgentDebug)
	bind(srv, "AgentEssayGrade", a.Svc.AgentTasks.AgentEssayGrade)

	// Provider 配置（设置页）
	bind(srv, "ProviderConfigure", a.Svc.ProviderConfigure)
	bind(srv, "ProviderTest", a.Svc.ProviderTest)
	bind(srv, "ProviderClear", a.Svc.ProviderClear)

	// 管理端（审核队列 / Provider 策略 / 功能开关 / 用户禁用 / 审计查询）
	bind(srv, "AdminReviewList", a.Svc.Admin.AdminReviewList)
	bind(srv, "AdminReviewDecide", a.Svc.Admin.AdminReviewDecide)
	bind(srv, "AdminProviderPolicySet", a.Svc.Admin.AdminProviderPolicySet)
	bind(srv, "AdminFeatureFlagSet", a.Svc.Admin.AdminFeatureFlagSet)
	bind(srv, "AdminUserDisable", a.Svc.Admin.AdminUserDisable)
	bind(srv, "AdminAuditList", a.Svc.Admin.AdminAuditList)

	// 资料与 RAG
	bind(srv, "DocumentImport", a.Svc.Document.DocumentImport)
	bind(srv, "DocumentList", a.Svc.Document.DocumentList)
	bind(srv, "DocumentRetry", a.Svc.Document.DocumentRetry)
	bind(srv, "DocumentDelete", a.Svc.Document.DocumentDelete)
	bind(srv, "RAGAsk", a.Svc.Document.RAGAsk)

	// 口语练习 Speaking/TTS（API 文档 7.16 / 完整设计文档 4.18）
	bind(srv, "TTSPlay", a.Svc.Speech.TTSPlay)
	bind(srv, "SpeakingSubmit", a.Svc.Speech.SpeakingSubmit)
	bind(srv, "SpeakingResultGet", a.Svc.Speech.SpeakingResultGet)
	srv.RegisterUpload("SpeakingUpload", a.handleSpeakingUpload)

	// P4 同步（本地模拟服务端）
	bind(srv, "SyncDeviceRegister", a.Svc.Sync.SyncDeviceRegister)
	bind(srv, "SyncPush", a.Svc.Sync.SyncPushLocal)
	bind(srv, "SyncPull", a.Svc.Sync.SyncPull)
	bind(srv, "SyncStatusGet", a.Svc.Sync.SyncStatusGet)
	// 云同步（Todo 34）：推送到 cmd/cloud-server 独立二进制；未配置 token 时回退 in-process。
	bind(srv, "SyncCloudPush", a.Svc.Sync.SyncCloudPush)

	// 本地内容社区（Todo 35 / 完整设计文档 4.20 社区）：帖子存 <DataDir>/community/*.json。
	bind(srv, "CommunityPostCreate", a.Svc.Community.CommunityPostCreate)
	bind(srv, "CommunityPostList", a.Svc.Community.CommunityPostList)
	bind(srv, "CommunityPostGet", a.Svc.Community.CommunityPostGet)
	bind(srv, "CommunityPostLike", a.Svc.Community.CommunityPostLike)

	// 求题闭环（Todo 36 / 完整设计文档 4.20 P2 求题请求）：状态机 open|fulfilled|closed。
	bind(srv, "ContentRequestCreate", a.Svc.Request.ContentRequestCreate)
	bind(srv, "ContentRequestGenerate", a.Svc.Request.ContentRequestGenerate)
	bind(srv, "ContentRequestReview", a.Svc.Request.ContentRequestReview)
	bind(srv, "ContentRequestCancel", a.Svc.Request.ContentRequestCancel)
	bind(srv, "ContentRequestList", a.Svc.Request.ContentRequestList)

	// 插件（API 文档 7.13 / 完整设计文档 4.24）
	bind(srv, "PluginInstall", a.Svc.Plugins.PluginInstall)
	bind(srv, "PluginSetEnabled", a.Svc.Plugins.PluginSetEnabled)
	bind(srv, "PluginUninstall", a.Svc.Plugins.PluginUninstall)
	bind(srv, "PluginConfirmPermissions", a.Svc.Plugins.PluginConfirmPermissions)
	bind(srv, "PluginInvoke", a.Svc.Plugins.PluginInvoke)
	bind0(srv, "PluginList", a.Svc.Plugins.PluginList)
	bind0(srv, "PluginMarketList", a.Svc.Plugins.PluginMarketList)
	bind(srv, "PluginThemeGet", a.Svc.Plugins.PluginThemeGet)

	// Webhook（API 文档 7.13 / 完整设计文档 4.24）
	bind(srv, "WebhookSubscribe", a.Svc.Webhooks.WebhookSubscribe)
	bind(srv, "WebhookTestSend", a.Svc.Webhooks.WebhookTestSend)
	bind(srv, "WebhookDelete", a.Svc.Webhooks.WebhookDelete)
	bind(srv, "WebhookList", a.Svc.Webhooks.WebhookList)

	// AI 流式事件（GET /api/v1/events）
	srv.RegisterSSE(a.Agent.Events)
	srv.RegisterUserSSE(a.Agent.UserEvents)

	// 文件下载（导出/备份结果，仅限 exports 与 uploads 目录）
	srv.Mux().HandleFunc("/api/v1/files", a.handleFileDownload)
}

// handleFileDownload 受限文件下载：仅允许 exports/ 与 uploads/ 相对路径。
func (a *App) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		apphttp.WriteErrorJSON(w, apphttp.EnvelopeError(domain.InvalidArg("path 必填"), service.NewID()))
		return
	}
	clean := filepath.Clean(strings.ReplaceAll(p, "\\", "/"))
	if clean == ".." || strings.HasPrefix(clean, "../") || filepath.IsAbs(clean) {
		apphttp.WriteErrorJSON(w, apphttp.EnvelopeError(domain.InvalidArg("非法路径"), service.NewID()))
		return
	}
	base := a.Cfg.DataDir
	full := filepath.Join(base, clean)
	allowed := false
	for _, dir := range []string{"exports", "uploads"} {
		prefix := filepath.Join(base, dir) + string(filepath.Separator)
		if strings.HasPrefix(full, prefix) {
			allowed = true
			break
		}
	}
	if !allowed {
		apphttp.WriteErrorJSON(w, apphttp.EnvelopeError(domain.Forbidden("无权访问该路径"), service.NewID()))
		return
	}
	if st, err := os.Stat(full); err != nil || st.IsDir() {
		apphttp.WriteErrorJSON(w, apphttp.EnvelopeError(domain.NotFound("文件不存在"), service.NewID()))
		return
	}
	http.ServeFile(w, r, full)
}

// handleLibraryUpload 处理 multipart 题库文件上传。
func (a *App) handleLibraryUpload(w http.ResponseWriter, r *http.Request) {
	rid := r.Header.Get("X-Request-ID")
	if rid == "" {
		rid = service.NewID()
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		apphttp.WriteErrorJSON(w, apphttp.EnvelopeError(domain.InvalidArg("上传解析失败: %v", err), rid))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		apphttp.WriteErrorJSON(w, apphttp.EnvelopeError(domain.InvalidArg("缺少 file 字段"), rid))
		return
	}
	defer file.Close()
	content, err := io.ReadAll(file)
	if err != nil {
		apphttp.WriteErrorJSON(w, apphttp.EnvelopeError(domain.InvalidArg("读取文件失败: %v", err), rid))
		return
	}
	// 上传入口按 user_id 校验用户活跃（表单缺省跳过；Preflight/CommitImport 在持久化时复核）。
	if uid := r.FormValue("user_id"); uid != "" {
		if err := a.Svc.AssertUserActive(r.Context(), uid); err != nil {
			apphttp.WriteErrorJSON(w, apphttp.EnvelopeError(err, rid))
			return
		}
	}
	result, err := a.Svc.Import.UploadFile(header.Filename, content)
	if err != nil {
		apphttp.WriteErrorJSON(w, apphttp.EnvelopeError(err, rid))
		return
	}
	apphttp.WriteJSON(w, http.StatusOK, apphttp.Envelope(result, rid))
}

// handleSpeakingUpload 处理 multipart 口语录音上传（保存到 uploads 目录，返回相对路径）。
func (a *App) handleSpeakingUpload(w http.ResponseWriter, r *http.Request) {
	rid := r.Header.Get("X-Request-ID")
	if rid == "" {
		rid = service.NewID()
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		apphttp.WriteErrorJSON(w, apphttp.EnvelopeError(domain.InvalidArg("上传解析失败: %v", err), rid))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		apphttp.WriteErrorJSON(w, apphttp.EnvelopeError(domain.InvalidArg("缺少 file 字段"), rid))
		return
	}
	defer file.Close()
	content, err := io.ReadAll(file)
	if err != nil {
		apphttp.WriteErrorJSON(w, apphttp.EnvelopeError(domain.InvalidArg("读取文件失败: %v", err), rid))
		return
	}
	if uid := r.FormValue("user_id"); uid != "" {
		if err := a.Svc.AssertUserActive(r.Context(), uid); err != nil {
			apphttp.WriteErrorJSON(w, apphttp.EnvelopeError(err, rid))
			return
		}
	}
	result, err := a.Svc.Speech.SaveAudio(header.Filename, content)
	if err != nil {
		apphttp.WriteErrorJSON(w, apphttp.EnvelopeError(err, rid))
		return
	}
	apphttp.WriteJSON(w, http.StatusOK, apphttp.Envelope(result, rid))
}

// bind 注册一个方法名式 handler：body map → 具体请求 DTO → service 调用。
func bind[T any, R any](srv *apphttp.Server, method string, fn func(ctx context.Context, req T) (R, error)) {
	srv.Register(method, func(ctx context.Context, body map[string]json.RawMessage) (any, error) {
		var req T
		if err := bindBody(body, &req); err != nil {
			return nil, err
		}
		return fn(ctx, req)
	})
}

// bind0 注册无请求体（或空请求体）的方法。
func bind0[R any](srv *apphttp.Server, method string, fn func(ctx context.Context) (R, error)) {
	srv.Register(method, func(ctx context.Context, body map[string]json.RawMessage) (any, error) {
		return fn(ctx)
	})
}

// bindBody 将原始字段 map 解码到请求 DTO（未知字段忽略，与 API 文档一致）。
func bindBody(body map[string]json.RawMessage, v any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return domain.InvalidArg("请求体解析失败: %v", err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return domain.InvalidArg("请求体解析失败: %v", err)
	}
	return nil
}

// buildLLMFallback 装配 LLM 回退链：主模型（openai/local）→ mock 确定性模板。
// 主模型不可用时自动回退 mock，保证 AI 功能不因端点故障中断（前端 ProviderTest 提示）。
func buildLLMFallback(kind string, c map[string]any) (provider.LLMProvider, error) {
	primary, err := provider.NewLLM(kind, c)
	if err != nil {
		return nil, err
	}
	if kind == "mock" {
		return primary, nil // mock 自身即兜底，无需再包装
	}
	fallback, err := provider.NewLLM("mock", nil)
	if err != nil {
		return primary, nil
	}
	return &provider.FallbackLLM{Primary: primary, Fallback: fallback}, nil
}

// buildEmbeddingFallback 装配 Embedding 回退链：主向量模型 → mock 确定性哈希向量。
// 注册名约定与 TTS/ASR 一致：kind 需加 "embedding-" 前缀。
func buildEmbeddingFallback(kind string, c map[string]any) (provider.EmbeddingProvider, error) {
	if kind != "" {
		kind = "embedding-" + kind
	}
	primary, err := provider.NewEmbedding(kind, c)
	if err != nil {
		return nil, err
	}
	if kind == "embedding-mock" {
		return primary, nil
	}
	fallback, err := provider.NewEmbedding("embedding-mock", nil)
	if err != nil {
		return primary, nil
	}
	return &provider.FallbackEmbedding{Primary: primary, Fallback: fallback}, nil
}
