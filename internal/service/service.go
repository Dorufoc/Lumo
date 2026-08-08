// Package service 实现应用用例：校验、编排、事务与错误映射。
// 方法签名与 API 文档绑定方法一一对应；本包不依赖 HTTP 与 Wails。
package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"

	"lumo/internal/agent"
	"lumo/internal/config"
	"lumo/internal/domain"
	"lumo/internal/provider"
	"lumo/internal/repository"
)

// Services 聚合全部业务服务。
type Services struct {
	Repo           *repository.Repo
	Cfg            *config.Config
	Workspace      *WorkspaceService
	Settings       *SettingsService
	Backup         *BackupService
	Knowledge      *KnowledgeService
	Import         *ImportService
	Goal           *GoalService
	Practice       *PracticeService
	Review         *ReviewService
	Dashboard      *DashboardService
	Document       *DocumentService
	Sync           *SyncService
	Flashcard      *FlashcardService
	Note           *NoteService
	Exam           *ExamService
	Checkin        *CheckinService
	Focus          *FocusService
	Reminder       *ReminderService
	Health         *HealthService
	Report         *ReportService
	Favorites      *FavoritesService
	Calendar       *CalendarService
	AgentTasks     *AgentTasksService
	Classes        *ClassesService
	Assignments    *AssignmentsService
	Appeals        *AppealsService
	Stats          *StatsService
	Admin          *AdminService
	Org            *OrgService
	Family         *FamilyService
	KnowledgeGraph *KnowledgeGraphService
	Share          *ShareService
	Plugins        *PluginService
	Webhooks       *WebhookService
	Speech         *SpeechService
	Community      *CommunityService
	Request        *RequestService

	// UserEvents 用户级领域事件总线：领域事件（reminder:triggered 等）经此持久化通知并广播。
	// app 层将其与 agent.Service.UserEvents 指向同一实例（SSE 订阅端复用），见 app.go。
	UserEvents *agent.UserEventBus

	// SwapDB 由 app 层注入：关闭旧连接、以 newPath 替换数据库主文件、
	// 打开新连接并返回（BackupRestore 使用）。
	SwapDB func(newPath string) (*sql.DB, error)

	// Grader 异步评分器（简答/代码题；P2 由 agent 模块实现，未配置时降级 failed）。
	Grader Grader

	// Agent AI 会话编排（由 app 装配）。
	Agent *agent.Service

	// EmbeddingFactory 构造向量化 Provider（由 app 注入，读取 secrets 配置）。
	EmbeddingFactory func() (provider.EmbeddingProvider, error)

	// RestoreMu 备份恢复互斥（app 层注入；防止并发替换数据库）。
	RestoreMu *sync.Mutex

	secretMu sync.Mutex
	secret   []byte
}

// Grader 是异步评分接口。
type Grader interface {
	Grade(ctx context.Context, workspaceID, gradingID string) error
}

// New 装配服务。
func New(repo *repository.Repo, cfg *config.Config) *Services {
	s := &Services{Repo: repo, Cfg: cfg}
	s.Workspace = &WorkspaceService{s: s}
	s.Settings = &SettingsService{s: s}
	s.Backup = &BackupService{s: s}
	s.Knowledge = &KnowledgeService{s: s}
	s.Import = &ImportService{s: s}
	s.Goal = &GoalService{s: s}
	s.Practice = &PracticeService{s: s}
	s.Review = &ReviewService{s: s}
	s.Dashboard = &DashboardService{s: s}
	s.Document = &DocumentService{s: s}
	s.Sync = &SyncService{s: s}
	s.Flashcard = &FlashcardService{s: s}
	s.Note = &NoteService{s: s}
	s.Exam = &ExamService{s: s, Now: time.Now}
	s.Checkin = &CheckinService{s: s, Now: time.Now}
	s.Focus = &FocusService{s: s, Now: time.Now}
	s.Reminder = &ReminderService{s: s, Now: time.Now}
	s.Health = &HealthService{s: s, Now: time.Now}
	s.Report = &ReportService{s: s, Now: time.Now}
	s.Favorites = &FavoritesService{s: s}
	s.Calendar = &CalendarService{s: s}
	s.AgentTasks = &AgentTasksService{s: s}
	s.Classes = &ClassesService{s: s}
	s.Assignments = &AssignmentsService{s: s, Now: time.Now}
	s.Appeals = &AppealsService{s: s}
	s.Stats = &StatsService{s: s}
	s.Admin = &AdminService{s: s}
	s.Org = &OrgService{s: s}
	s.Family = &FamilyService{s: s}
	s.KnowledgeGraph = &KnowledgeGraphService{s: s}
	s.Share = &ShareService{s: s}
	s.Plugins = &PluginService{s: s}
	s.Webhooks = &WebhookService{s: s, Now: time.Now}
	s.Speech = &SpeechService{s: s}
	s.Community = &CommunityService{s: s}
	s.Request = &RequestService{s: s}
	// 用户级事件总线由服务层持有；app.New 会把 a.Agent.UserEvents 指向同一实例，
	// 使服务层发布的事件能被 SSE 订阅者接收（见 internal/app/app.go 注释）。
	s.UserEvents = agent.NewUserEventBus(repo)
	return s
}

// NewID 生成 UUID。
func NewID() string { return uuid.NewString() }

// Now 返回 UTC 时间字符串。
func Now() string { return domain.NowUTC() }

// hmacSecret 加载或生成 HMAC secret（本地 secrets 文件，0600）。
func (s *Services) hmacSecret() ([]byte, error) {
	s.secretMu.Lock()
	defer s.secretMu.Unlock()
	if s.secret != nil {
		return s.secret, nil
	}
	b, err := os.ReadFile(s.Cfg.SecretsPath)
	if err == nil {
		var m map[string]string
		if json.Unmarshal(b, &m) == nil && m["hmac_secret"] != "" {
			s.secret = []byte(m["hmac_secret"])
			return s.secret, nil
		}
	}
	secret := []byte(uuid.NewString() + uuid.NewString())
	_ = os.MkdirAll(s.Cfg.DataDir, 0o700)
	_ = os.WriteFile(s.Cfg.SecretsPath, []byte(`{"hmac_secret":"`+string(secret)+`"}`), 0o600)
	s.secret = secret
	return s.secret, nil
}

// withIdempotency 执行幂等命令：原子占位（INSERT ... ON CONFLICT DO NOTHING）消除 TOCTOU。
// 命中已完成键返回历史响应；命中处理中键返回 CONFLICT；fn 失败释放占位允许重试。
func withIdempotency[T any](s *Services, ctx context.Context, workspaceID, key, method string, fn func() (T, error)) (T, error) {
	var zero T
	if key == "" {
		return fn()
	}
	if !domain.ValidIdempotencyKey(key) {
		return zero, domain.InvalidArg("idempotency_key 长度须为 8-128 且仅含字母数字-_")
	}
	claimed, err := s.Repo.ClaimIdempotency(ctx, workspaceID, key, method)
	if err != nil {
		return zero, err
	}
	if !claimed {
		// 键已存在：处理中或已完成
		if resp, err := s.Repo.GetIdempotentResponse(ctx, workspaceID, key); err == nil {
			var data T
			if json.Unmarshal(resp, &data) == nil {
				return data, nil
			}
		}
		return zero, domain.Conflict("相同请求正在处理中，请稍后重试")
	}
	data, err := fn()
	if err != nil {
		// 释放占位，允许换参重试
		_ = s.Repo.ReleaseIdempotency(ctx, workspaceID, key)
		return zero, err
	}
	raw, _ := json.Marshal(data)
	_ = s.Repo.CompleteIdempotency(ctx, workspaceID, key, raw)
	return data, nil
}

// assertWorkspace 校验工作区存在。
func (s *Services) assertWorkspace(ctx context.Context, id string) error {
	if id == "" || !domain.ValidID(id) {
		return domain.InvalidArg("workspace_id 无效")
	}
	return s.Repo.AssertWorkspace(ctx, id)
}

// assertUserActive 校验用户未被禁用（仅命令型方法入口调用；空 user_id 跳过）。
// 已禁用用户返回 UNAUTHORIZED；未知用户视为活跃（不阻断）。
func (s *Services) assertUserActive(ctx context.Context, userID string) error {
	if userID == "" {
		return nil
	}
	disabledAt, err := s.Repo.GetUserDisabledAt(ctx, userID)
	if err != nil {
		return err
	}
	if disabledAt != nil {
		return domain.Unauthorized("用户已被禁用，请联系管理员")
	}
	return nil
}

// AssertUserActive 是 assertUserActive 的导出包装（HTTP 上传等跨层入口使用；空 user_id 跳过）。
func (s *Services) AssertUserActive(ctx context.Context, userID string) error {
	return s.assertUserActive(ctx, userID)
}

// enforceParentAI 家长 AI 禁用门禁：任一 active 绑定家长关闭 AI → FEATURE_DISABLED。
// 供学生端 AI 入口（agent 会话/任务、RAG 问答）调用；不影响本地判分与复习。
func (s *Services) enforceParentAI(ctx context.Context, userID string) error {
	if userID == "" {
		return nil
	}
	disabled, err := s.Repo.GetStudentAIDisabled(ctx, userID)
	if err != nil {
		return err
	}
	if disabled {
		return domain.FeatureDisabled("AI 辅导已被家长关闭")
	}
	return nil
}

// EnforceParentAI 导出包装（app 组装层注入 agent.BeforeChat 用）。
func (s *Services) EnforceParentAI(ctx context.Context, userID string) error {
	return s.enforceParentAI(ctx, userID)
}

// enforceParentDailyLimit 家长每日时长门禁：达到/超过上限 → QUOTA_EXCEEDED。
// 供学生端练习入口（PracticeStart）调用；0=不限制。
func (s *Services) enforceParentDailyLimit(ctx context.Context, userID string) error {
	if userID == "" {
		return nil
	}
	limit, err := s.Repo.GetStudentDailyLimitMin(ctx, userID)
	if err != nil {
		return err
	}
	if limit <= 0 {
		return nil
	}
	dayStart := startOfDayUTC(time.Now())
	used, err := s.Repo.CountTodayStudyMinutes(ctx, userID, dayStart)
	if err != nil {
		return err
	}
	if used >= limit {
		return domain.QuotaExceeded("今日学习时长已达家长设置上限（%d 分钟），明日再继续吧", limit)
	}
	return nil
}

// assertNotParent 校验调用者不是家长角色（家长端只读；写操作 FORBIDDEN，除家庭设置外）。
func (s *Services) assertNotParent(ctx context.Context, wsID, userID, action string) error {
	if userID == "" {
		return nil
	}
	u, err := s.Repo.GetUser(ctx, wsID, userID)
	if err != nil {
		return err
	}
	if u != nil && u.Role == "parent" {
		s.audit(ctx, wsID, action, "user", userID, map[string]any{"forbidden": true, "role": "parent"})
		return domain.Forbidden("家长角色仅可查看家庭视图与设置使用限制")
	}
	return nil
}

// audit 追加审计事件。
func (s *Services) audit(ctx context.Context, wsID string, action, entityType string, entityID string, payload any) {
	e := &repository.AuditEvent{
		ID: NewID(), WorkspaceID: wsID, Action: action,
		EntityType: entityType, EntityID: &entityID, Payload: json.RawMessage(repository.MarshalJSON(payload)),
	}
	_ = s.Repo.AppendAudit(ctx, e)
}
