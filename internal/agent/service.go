package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"lumo/internal/domain"
	"lumo/internal/provider"
	"lumo/internal/repository"
)

// AgentSession 是 AI 会话 DTO。
type AgentSession struct {
	ID          string          `json:"id"`
	WorkspaceID string          `json:"workspace_id"`
	UserID      string          `json:"user_id"`
	Agent       string          `json:"agent"`
	Status      string          `json:"status"`
	RequestID   *string         `json:"request_id"`
	ContextVer  *string         `json:"context_version"`
	Messages    []*AgentMessage `json:"messages,omitempty"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
}

// AgentMessage 是消息 DTO。
type AgentMessage struct {
	ID         string `json:"id"`
	SessionID  string `json:"session_id"`
	Role       string `json:"role"`
	Content    string `json:"content"`
	SequenceNo int    `json:"sequence_no"`
	CreatedAt  string `json:"created_at"`
}

// AgentMemory 是长期记忆 DTO。
type AgentMemory struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	UserID      string  `json:"user_id"`
	MemoryType  string  `json:"memory_type"`
	Summary     string  `json:"summary"`
	SourceRef   *string `json:"source_ref"`
	Consent     bool    `json:"consent"`
	ExpiresAt   *string `json:"expires_at"`
	Version     int     `json:"version"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// CancelResult 是取消结果。
type CancelResult struct {
	Cancelled bool   `json:"cancelled"`
	RequestID string `json:"request_id"`
}

// LLMFactory 构造 LLM Provider（由 app 注入，读取 secrets 配置）。
type LLMFactory func() (provider.LLMProvider, error)

// Service 是 Agent 编排服务。
type Service struct {
	Repo       *repository.Repo
	Events     *Bus
	UserEvents *UserEventBus
	LLMFactory LLMFactory

	mu      sync.Mutex
	cancels map[string]context.CancelFunc // request_id → cancel
}

// New 构造 Agent 服务。
func New(repo *repository.Repo) *Service {
	return &Service{
		Repo:       repo,
		Events:     NewBus(),
		UserEvents: NewUserEventBus(repo),
		cancels:    map[string]context.CancelFunc{},
	}
}

// AgentChatCreateReq 创建会话请求。
type AgentChatCreateReq struct {
	WorkspaceID    string `json:"workspace_id"`
	UserID         string `json:"user_id"`
	Agent          string `json:"agent"` // tutor | grader | diagnoser | router | planner ...
	Context        string `json:"context"`
	IdempotencyKey string `json:"idempotency_key"`
}

// AgentChatCreate 创建会话。
func (a *Service) AgentChatCreate(ctx context.Context, req AgentChatCreateReq) (*AgentSession, error) {
	if req.WorkspaceID == "" || req.UserID == "" {
		return nil, domain.InvalidArg("workspace_id 与 user_id 必填")
	}
	if req.Agent == "" {
		req.Agent = "router"
	}
	valid := map[string]bool{"router": true, "planner": true, "profiler": true, "tutor": true,
		"grader": true, "diagnoser": true, "librarian": true, "ocr": true, "variator": true,
		"auditor": true, "interviewer": true, "coach": true, "summarizer": true, "quizgen": true,
		"debugger": true, "essaygrader": true}
	if !valid[req.Agent] {
		return nil, domain.InvalidArg("agent 类型非法")
	}
	session := &repository.AgentSessionRow{
		ID: newID(), WorkspaceID: req.WorkspaceID, UserID: req.UserID,
		Agent: req.Agent, ContextVer: nil,
	}
	if req.Context != "" {
		cv := req.Context
		session.ContextVer = &cv
	}
	if err := a.Repo.CreateAgentSession(ctx, session); err != nil {
		return nil, err
	}
	if req.Context != "" {
		_, _ = a.Repo.AppendAgentMessage(ctx, session.ID, "system", req.Context)
	}
	return a.sessionByID(ctx, req.WorkspaceID, session.ID)
}

// AgentChatSendReq 发送消息请求。
type AgentChatSendReq struct {
	WorkspaceID string `json:"workspace_id"`
	SessionID   string `json:"session_id"`
	Message     string `json:"message"`
	RequestID   string `json:"request_id"`
}

// AgentRequest 是流式请求句柄。
type AgentRequest struct {
	RequestID string `json:"request_id"`
	SessionID string `json:"session_id"`
}

// AgentChatSend 启动异步流式回复（事件经 SSE 推送）。
func (a *Service) AgentChatSend(ctx context.Context, req AgentChatSendReq) (*AgentRequest, error) {
	session, err := a.Repo.GetAgentSession(ctx, req.WorkspaceID, req.SessionID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, domain.NotFound("AI 会话不存在")
	}
	if req.Message == "" {
		return nil, domain.InvalidArg("message 不能为空")
	}
	if err := a.enforceProviderPolicy(ctx, session); err != nil {
		return nil, err
	}
	rid := req.RequestID
	if rid == "" {
		rid = newID()
	}
	if err := a.Repo.UpdateAgentSession(ctx, session.ID, "streaming", &rid); err != nil {
		return nil, err
	}
	_, _ = a.Repo.AppendAgentMessage(ctx, session.ID, "user", req.Message)

	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.cancels[rid] = cancel
	a.mu.Unlock()

	go a.runStream(ctx, session, rid, req.Message)
	return &AgentRequest{RequestID: rid, SessionID: session.ID}, nil
}

// agentPolicyProvider 是 Agent 对话的 Provider 策略键（LLM 类别，与 secrets 的 provider_llm 对应）。
const agentPolicyProvider = "llm"

// agentUsageEventType 是 Agent 对话用量事件类型（usage_events.event_type）。
const agentUsageEventType = "agent.chat"

// enforceProviderPolicy 在启动流式回复前执行 Provider 策略/配额门禁：
//   - 无策略：默认放行；
//   - allowed=false → FEATURE_DISABLED；
//   - daily_quota/monthly_budget 超限 → QUOTA_EXCEEDED；
//   - 放行时记录一条 usage_events（provider/model 随 payload 落库，供计数）。
func (a *Service) enforceProviderPolicy(ctx context.Context, session *repository.AgentSessionRow) error {
	model := a.currentModel()
	p, err := a.Repo.GetProviderPolicy(ctx, agentPolicyProvider, model)
	if err != nil {
		return err
	}
	if p != nil && !p.Allowed {
		return domain.FeatureDisabled("模型 %s 已被管理员禁用", model)
	}
	now := time.Now().UTC()
	if p != nil && p.DailyQuota != nil {
		since := startOfDayUTC(now)
		n, err := a.Repo.CountUsageEvents(ctx, session.WorkspaceID, agentUsageEventType,
			agentPolicyProvider, model, since)
		if err != nil {
			return err
		}
		if n >= *p.DailyQuota {
			return domain.QuotaExceeded("今日对话次数已达上限")
		}
	}
	if p != nil && p.MonthlyBudget != nil {
		since := startOfMonthUTC(now)
		n, err := a.Repo.CountUsageEvents(ctx, session.WorkspaceID, agentUsageEventType,
			agentPolicyProvider, model, since)
		if err != nil {
			return err
		}
		if n >= *p.MonthlyBudget {
			return domain.QuotaExceeded("本月对话次数已达上限")
		}
	}
	_ = a.Repo.AppendUsageEvent(ctx, session.WorkspaceID, session.UserID, agentUsageEventType,
		map[string]any{"provider": agentPolicyProvider, "model": model})
	return nil
}

// currentModel 解析当前生效的模型名：优先取 LLM 工厂返回的 Provider 名，
// 未配置（LLMFactory 为空或构造失败）时回退 "mock"（本地确定性降级）。
func (a *Service) currentModel() string {
	llm, err := a.llm()
	if err != nil || llm == nil {
		return "mock"
	}
	return llm.Name()
}

// startOfDayUTC 返回 UTC 当日零点（RFC3339，用于用量计数）。
func startOfDayUTC(now time.Time) string {
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
}

// startOfMonthUTC 返回 UTC 当月 1 日零点（RFC3339，用于月度预算计数）。
func startOfMonthUTC(now time.Time) string {
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
}

// RunStream 启动外部驱动的流式任务（RAGAsk 等复用）：发布事件、保存回复、维护会话状态。
func (a *Service) RunStream(ctx context.Context, session *repository.AgentSessionRow, requestID string, run func(ctx context.Context, emit Emit) error) {
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.cancels[requestID] = cancel
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.cancels, requestID)
		a.mu.Unlock()
	}()

	seq := 0
	var replyBuf strings.Builder
	emit := func(name string, payload map[string]any) {
		seq++
		payload["request_id"] = requestID
		payload["session_id"] = session.ID
		payload["sequence_no"] = seq
		payload["occurred_at"] = nowUTC()
		if name == "agent:delta" {
			if d, ok := payload["delta"].(string); ok {
				replyBuf.WriteString(d)
			}
		}
		a.Events.Publish(session.ID, Event{Name: name, Payload: payload})
	}

	if err := run(ctx, emit); err != nil {
		code := "AGENT_FAILED"
		if err == context.Canceled || ctx.Err() == context.Canceled {
			code = "REQUEST_CANCELLED"
		}
		emit("agent:error", map[string]any{
			"error": map[string]any{"code": code, "message": err.Error()},
		})
		status := "failed"
		if code == "REQUEST_CANCELLED" {
			status = "cancelled"
		}
		_ = a.Repo.UpdateAgentSession(ctx, session.ID, status, &requestID)
		return
	}
	if replyBuf.Len() > 0 {
		_, _ = a.Repo.AppendAgentMessage(ctx, session.ID, "assistant", replyBuf.String())
	}
	_ = a.Repo.UpdateAgentSession(ctx, session.ID, "completed", &requestID)
}

// runStream 执行 Agent 处理并发布事件（委托 RunStream）。
func (a *Service) runStream(ctx context.Context, session *repository.AgentSessionRow, requestID, message string) {
	llm, llmErr := a.llm()
	handler := a.handlerFor(session.Agent, llm)
	a.RunStream(ctx, session, requestID, func(ctx context.Context, emit Emit) error {
		return handler.Run(ctx, emit, &RunInput{
			Session: session, Message: message, LLM: llm, LLMErr: llmErr,
		})
	})
}

// AgentChatCancelReq 取消请求。
type AgentChatCancelReq struct {
	WorkspaceID string `json:"workspace_id"`
	SessionID   string `json:"session_id"`
	RequestID   string `json:"request_id"`
}

// AppendUserMessage 追加用户消息（外部驱动流程使用）。
func (a *Service) AppendUserMessage(ctx context.Context, sessionID, content string) error {
	_, err := a.Repo.AppendAgentMessage(ctx, sessionID, "user", content)
	return err
}

// LLMFactoryFunc 公开 LLM 工厂（RAGAsk 等复用）。
func (a *Service) LLMFactoryFunc() (provider.LLMProvider, error) {
	return a.llm()
} // AgentChatCancel 取消进行中的流式请求。
func (a *Service) AgentChatCancel(ctx context.Context, req AgentChatCancelReq) (*CancelResult, error) {
	if _, err := a.Repo.GetAgentSession(ctx, req.WorkspaceID, req.SessionID); err != nil {
		return nil, err
	}
	a.mu.Lock()
	cancel, ok := a.cancels[req.RequestID]
	a.mu.Unlock()
	if ok {
		cancel()
		return &CancelResult{Cancelled: true, RequestID: req.RequestID}, nil
	}
	return &CancelResult{Cancelled: false, RequestID: req.RequestID}, nil
}

// AgentSessionGetReq 获取会话请求。
type AgentSessionGetReq struct {
	WorkspaceID string `json:"workspace_id"`
	SessionID   string `json:"session_id"`
}

// AgentSessionGet 获取会话与消息。
func (a *Service) AgentSessionGet(ctx context.Context, req AgentSessionGetReq) (*AgentSession, error) {
	return a.sessionByID(ctx, req.WorkspaceID, req.SessionID)
}

// AgentMemoryListReq 记忆列表请求。
type AgentMemoryListReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
}

// AgentMemoryList 列出长期记忆。
func (a *Service) AgentMemoryList(ctx context.Context, req AgentMemoryListReq) ([]*AgentMemory, error) {
	rows, err := a.Repo.ListAgentMemory(ctx, req.WorkspaceID, req.UserID)
	if err != nil {
		return nil, err
	}
	out := make([]*AgentMemory, 0, len(rows))
	for _, r := range rows {
		out = append(out, memoryFromRow(r))
	}
	return out, nil
}

// AgentMemoryDeleteReq 删除记忆请求。
type AgentMemoryDeleteReq struct {
	WorkspaceID string `json:"workspace_id"`
	MemoryID    string `json:"memory_id"`
	Version     int    `json:"version"`
}

// AgentMemoryDelete 删除记忆（需授权 consent 校验在调用方）。
func (a *Service) AgentMemoryDelete(ctx context.Context, req AgentMemoryDeleteReq) (*CancelResult, error) {
	if err := a.Repo.SoftDeleteAgentMemory(ctx, req.WorkspaceID, req.MemoryID, req.Version); err != nil {
		return nil, err
	}
	return &CancelResult{Cancelled: true, RequestID: req.MemoryID}, nil
}

// llm 构造 LLM（未配置返回 ErrNotConfigured）。
func (a *Service) llm() (provider.LLMProvider, error) {
	if a.LLMFactory == nil {
		return nil, provider.ErrNotConfigured
	}
	return a.LLMFactory()
}

func (a *Service) sessionByID(ctx context.Context, wsID, id string) (*AgentSession, error) {
	row, err := a.Repo.GetAgentSession(ctx, wsID, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, domain.NotFound("AI 会话不存在")
	}
	out := &AgentSession{
		ID: row.ID, WorkspaceID: row.WorkspaceID, UserID: row.UserID,
		Agent: row.Agent, Status: row.Status, RequestID: row.RequestID,
		ContextVer: row.ContextVer, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	msgs, err := a.Repo.ListAgentMessages(ctx, id, 100)
	if err != nil {
		return nil, err
	}
	for _, m := range msgs {
		content := ""
		if m.ContentRef != nil {
			content = *m.ContentRef
		}
		out.Messages = append(out.Messages, &AgentMessage{
			ID: m.ID, SessionID: m.SessionID, Role: m.Role,
			Content: content, SequenceNo: m.SequenceNo, CreatedAt: m.CreatedAt,
		})
	}
	return out, nil
}

func memoryFromRow(r *repository.AgentMemoryRow) *AgentMemory {
	return &AgentMemory{
		ID: r.ID, WorkspaceID: r.WorkspaceID, UserID: r.UserID,
		MemoryType: r.MemoryType, Summary: r.Summary, SourceRef: r.SourceRef,
		Consent: r.Consent, ExpiresAt: r.ExpiresAt,
		Version: r.Version, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

var _ = json.Marshal
