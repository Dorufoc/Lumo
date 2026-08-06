package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"lumo/internal/agent"
	"lumo/internal/domain"
	"lumo/internal/repository"
	"lumo/internal/sandbox"
)

// quizGenPromptVersion 出题提示词版本（question_versions.prompt_version）。
const quizGenPromptVersion = "1"

// quizGenMaxCount QuizGen 题量上限（设计文档 10.10：1-10）。
const quizGenMaxCount = 10

// agentTasksTextBudget 输入文本预算（rune 数，控制成本与 token）。
const agentTasksTextBudget = 4000

// quizValidTypes 题型白名单（与 QuestionPayload 契约一致）。
var quizValidTypes = map[string]bool{
	"single_choice": true, "multiple_choice": true,
	"fill_blank": true, "short_answer": true, "code": true,
}

// AgentTasksService Agent 扩展任务用例（Summarizer 10.9 / QuizGen 10.10 / Debugger+EssayGrader 10.12）。
type AgentTasksService struct {
	s   *Services
	sbx sandbox.Runner // 可注入沙箱执行器（测试替换为桩；nil 用默认子进程沙箱）
}

// sandbox 返回沙箱执行器（默认真实子进程沙箱）。
func (a *AgentTasksService) sandbox() sandbox.Runner {
	if a.sbx != nil {
		return a.sbx
	}
	return sandbox.DefaultRunner
}

// AgentSummarizeReq 文档摘要请求（Summarizer）。
type AgentSummarizeReq struct {
	WorkspaceID    string `json:"workspace_id"`
	UserID         string `json:"user_id"`
	DocumentID     string `json:"document_id"`
	NoteID         string `json:"note_id"` // 暂不支持笔记摘要持久化
	Preferences    string `json:"preferences"`
	IdempotencyKey string `json:"idempotency_key"`
}

// AgentSummarizeResult 摘要结果。
type AgentSummarizeResult struct {
	Summary  *DocumentSummary `json:"summary"`
	Degraded bool             `json:"degraded"` // Provider 未配置 → 确定性降级（status=failed 可重试）
}

// AgentSummarize 生成文档结构化摘要并持久化到 document_summaries（幂等 + 审计 + 降级）。
func (a *AgentTasksService) AgentSummarize(ctx context.Context, req AgentSummarizeReq) (*AgentSummarizeResult, error) {
	if err := a.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if req.DocumentID == "" {
		return nil, domain.InvalidArg("document_id 必填")
	}
	if req.NoteID != "" {
		return nil, domain.InvalidArg("笔记摘要暂不支持持久化，仅支持文档摘要")
	}
	return withIdempotency(a.s, ctx, req.WorkspaceID, req.IdempotencyKey, "AgentSummarize",
		func() (*AgentSummarizeResult, error) { return a.doSummarize(ctx, req) })
}

// doSummarize 执行摘要生成并落库。
func (a *AgentTasksService) doSummarize(ctx context.Context, req AgentSummarizeReq) (*AgentSummarizeResult, error) {
	doc, err := a.s.Repo.GetDocument(ctx, req.WorkspaceID, req.DocumentID)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, domain.NotFound("文档不存在")
	}
	chunks, err := a.s.Repo.ListDocumentChunks(ctx, req.WorkspaceID, []string{req.DocumentID})
	if err != nil {
		return nil, err
	}
	text := summarizeChunkText(chunks)
	text = truncateRunes(text, agentTasksTextBudget)

	pv := summaryPromptVersion

	llm, llmErr := a.s.Agent.LLMFactoryFunc()
	if llmErr != nil {
		// 降级：模型不可用时不生成摘要，状态 failed 可重试（设计文档 10.9）
		row := &repository.DocumentSummaryRow{
			ID: NewID(), DocumentID: req.DocumentID,
			SummaryJSON: string(mustJSON(map[string]any{"note": "未配置 AI 模型，请配置模型 Provider 后重试"})),
			Model:       "", PromptVersion: &pv, Status: domain.SummaryStatusFailed,
		}
		if err := a.s.Repo.CreateDocumentSummary(ctx, row); err != nil {
			return nil, err
		}
		a.s.audit(ctx, req.WorkspaceID, "agent.summarize", "document", req.DocumentID,
			map[string]any{"status": "failed", "degraded": true, "reason": "provider not configured"})
		fetched, err := a.s.Repo.GetDocumentSummary(ctx, row.ID)
		if err != nil {
			return nil, err
		}
		return &AgentSummarizeResult{Summary: summaryFromRow(fetched), Degraded: true}, nil
	}

	system, prompt := agent.BuildSummarizePrompt(agent.SummarizeInput{
		Title: doc.FileName, Text: text, Preferences: req.Preferences,
	})
	var out agent.SummarizeOutput
	model, err := agent.ChatJSON(ctx, llm, system, prompt, &out)
	if err != nil {
		// 超时/Provider 失败/非法 JSON → 明确错误 + 审计失败原因
		a.s.audit(ctx, req.WorkspaceID, "agent.summarize", "document", req.DocumentID,
			map[string]any{"status": "failed", "reason": err.Error()})
		return nil, providerError(err)
	}
	row := &repository.DocumentSummaryRow{
		ID: NewID(), DocumentID: req.DocumentID,
		SummaryJSON: string(mustJSON(out)), Model: model, PromptVersion: &pv,
		Status: domain.SummaryStatusReady,
	}
	if err := a.s.Repo.CreateDocumentSummary(ctx, row); err != nil {
		return nil, err
	}
	a.s.audit(ctx, req.WorkspaceID, "agent.summarize", "document", req.DocumentID,
		map[string]any{"status": "ready", "model": model})
	// SQLite 无 INSERT..RETURNING：DDL 默认值（created_at/updated_at）需重读
	fetched, err := a.s.Repo.GetDocumentSummary(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	return &AgentSummarizeResult{Summary: summaryFromRow(fetched)}, nil
}

// summarizeChunkText 组装分块正文（带 [chunk:id] 定位标记）。
func summarizeChunkText(chunks []*repository.ChunkRow) string {
	var sb strings.Builder
	for _, c := range chunks {
		sb.WriteString("[chunk:" + c.ID + "]")
		if c.Section != nil && *c.Section != "" {
			sb.WriteString("## " + *c.Section + "\n")
		}
		sb.WriteString(c.TextRef + "\n")
	}
	return sb.String()
}

// providerError 将 LLM 调用/沙箱执行错误归一化为稳定错误码。
func providerError(err error) error {
	if de, ok := err.(*domain.Error); ok {
		return de // 已带稳定错误码（OUTPUT_INVALID 等）
	}
	var sl *sandbox.LimitError
	if errors.As(err, &sl) {
		// 沙箱超时/资源超限/执行失败 → SANDBOX_LIMIT 明确错误，不 panic
		return domain.WrapError(domain.CodeSandboxLimit, sl.Error(), err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return domain.WrapError(domain.CodeProviderTimeout, "AI Provider 调用超时", err)
	}
	return domain.WrapError(domain.CodeProviderTimeout, "AI Provider 调用失败: "+err.Error(), err)
}

// AgentQuizGenReq 出题请求（QuizGen）。
type AgentQuizGenReq struct {
	WorkspaceID    string   `json:"workspace_id"`
	UserID         string   `json:"user_id"`
	DocumentIDs    []string `json:"document_ids"`
	KnowledgeIDs   []string `json:"knowledge_ids"`
	Types          []string `json:"types"` // 题型偏好，空=默认客观题
	Count          int      `json:"count"` // 题量 1-10
	IdempotencyKey string   `json:"idempotency_key"`
}

// AgentQuizGenResult 出题结果。
type AgentQuizGenResult struct {
	Mode         string      `json:"mode"` // generated | recommended（降级）
	Questions    []*Question `json:"questions"`
	SkippedCount int         `json:"skipped_count"` // generated 模式下去重跳过数
	Message      string      `json:"message,omitempty"`
}

// AgentQuizGen 按资料/知识点生成题目草稿（幂等 + 审计；未配置 Provider 降级为已有题目筛选推荐）。
func (a *AgentTasksService) AgentQuizGen(ctx context.Context, req AgentQuizGenReq) (*AgentQuizGenResult, error) {
	if err := a.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if req.Count <= 0 || req.Count > quizGenMaxCount {
		return nil, domain.InvalidArg("题量（count）须为 1-%d", quizGenMaxCount)
	}
	types, err := normalizeQuizTypes(req.Types)
	if err != nil {
		return nil, err
	}
	if len(req.DocumentIDs) == 0 && len(req.KnowledgeIDs) == 0 {
		return nil, domain.InvalidArg("document_ids 与 knowledge_ids 至少提供一个")
	}
	for _, kid := range req.KnowledgeIDs {
		n, err := a.s.Repo.GetKnowledgeNode(ctx, req.WorkspaceID, kid)
		if err != nil {
			return nil, err
		}
		if n == nil {
			return nil, domain.InvalidArg("知识点 %s 不存在", kid)
		}
	}
	for _, did := range req.DocumentIDs {
		d, err := a.s.Repo.GetDocument(ctx, req.WorkspaceID, did)
		if err != nil {
			return nil, err
		}
		if d == nil {
			return nil, domain.NotFound("文档不存在")
		}
	}
	req.Types = types
	return withIdempotency(a.s, ctx, req.WorkspaceID, req.IdempotencyKey, "AgentQuizGen",
		func() (*AgentQuizGenResult, error) { return a.doQuizGen(ctx, req) })
}

// normalizeQuizTypes 题型偏好规范化：空 → 默认客观题（单选/多选）。
func normalizeQuizTypes(types []string) ([]string, error) {
	if len(types) == 0 {
		return []string{"single_choice", "multiple_choice"}, nil
	}
	seen := map[string]bool{}
	var out []string
	for _, t := range types {
		if !quizValidTypes[t] {
			return nil, domain.InvalidArg("题型仅允许 single_choice/multiple_choice/fill_blank/short_answer/code")
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out, nil
}

// doQuizGen 执行出题生成并落库题目草稿。
func (a *AgentTasksService) doQuizGen(ctx context.Context, req AgentQuizGenReq) (*AgentQuizGenResult, error) {
	knowledge, material, err := a.quizInputs(ctx, req)
	if err != nil {
		return nil, err
	}

	llm, llmErr := a.s.Agent.LLMFactoryFunc()
	if llmErr != nil {
		// 降级：无模型 → 基于已有题目的筛选推荐，不生成新题（设计文档 10.10）
		qs, err := a.recommendExisting(ctx, req)
		if err != nil {
			return nil, err
		}
		a.s.audit(ctx, req.WorkspaceID, "agent.quizgen", "question", req.WorkspaceID,
			map[string]any{"mode": "recommended", "count": len(qs), "reason": "provider not configured"})
		return &AgentQuizGenResult{
			Mode: "recommended", Questions: qs,
			Message: "AI 模型未配置，已降级为基于已有题目的筛选推荐",
		}, nil
	}

	system, prompt := agent.BuildQuizGenPrompt(agent.QuizGenInput{
		Types: req.Types, Count: req.Count, Knowledge: knowledge, Material: material,
	})
	var out agent.QuizGenOutput
	model, err := agent.ChatJSON(ctx, llm, system, prompt, &out)
	if err != nil {
		a.s.audit(ctx, req.WorkspaceID, "agent.quizgen", "question", req.WorkspaceID,
			map[string]any{"status": "failed", "reason": err.Error()})
		return nil, providerError(err)
	}
	if len(out.Questions) == 0 {
		err := domain.WrapError(domain.CodeOutputInvalid, "AI 未生成任何题目草稿", nil)
		a.s.audit(ctx, req.WorkspaceID, "agent.quizgen", "question", req.WorkspaceID,
			map[string]any{"status": "failed", "reason": err.Error()})
		return nil, err
	}

	pv := quizGenPromptVersion
	var created []*Question
	skipped := 0
	for i, d := range out.Questions {
		if i >= req.Count {
			break // 最多取 count 道
		}
		q, isSkip, err := a.createGeneratedQuestion(ctx, req, d, model, &pv)
		if err != nil {
			a.s.audit(ctx, req.WorkspaceID, "agent.quizgen", "question", req.WorkspaceID,
				map[string]any{"status": "failed", "reason": err.Error(), "index": i})
			return nil, err
		}
		if isSkip {
			skipped++
			continue
		}
		created = append(created, q)
	}
	a.s.audit(ctx, req.WorkspaceID, "agent.quizgen", "question", req.WorkspaceID,
		map[string]any{"status": "ready", "mode": "generated", "created": len(created), "skipped": skipped, "model": model})
	return &AgentQuizGenResult{Mode: "generated", Questions: created, SkippedCount: skipped}, nil
}

// quizInputs 组装知识点说明与资料正文。
func (a *AgentTasksService) quizInputs(ctx context.Context, req AgentQuizGenReq) (knowledge, material string, err error) {
	if len(req.KnowledgeIDs) > 0 {
		var names []string
		for _, kid := range req.KnowledgeIDs {
			n, err := a.s.Repo.GetKnowledgeNode(ctx, req.WorkspaceID, kid)
			if err != nil {
				return "", "", err
			}
			if n != nil {
				names = append(names, n.Name)
			}
		}
		knowledge = strings.Join(names, "、")
	}
	if len(req.DocumentIDs) > 0 {
		chunks, err := a.s.Repo.ListDocumentChunks(ctx, req.WorkspaceID, req.DocumentIDs)
		if err != nil {
			return "", "", err
		}
		material = truncateRunes(summarizeChunkText(chunks), agentTasksTextBudget)
	}
	return knowledge, material, nil
}

// createGeneratedQuestion 落库单题草稿；与已有题内容哈希冲突时返回 skip=true（不重复插入，不 panic）。
func (a *AgentTasksService) createGeneratedQuestion(ctx context.Context, req AgentQuizGenReq, d agent.QuizDraft, model string, pv *string) (*Question, bool, error) {
	payload := &QuestionPayload{
		Type:       d.Type,
		Stem:       d.Stem,
		Options:    draftOptionsToService(d.Options),
		Answer:     d.Answer,
		Analysis:   d.Analysis,
		Difficulty: d.Difficulty,
		Source:     "ai",
	}
	parsed, err := parseQuestionPayload(mustJSON(payload))
	if err != nil {
		return nil, false, err
	}
	parsed.Source = "ai"
	parsed.KnowledgeIDs = a.mergeKnowledgeIDs(ctx, req, d.KnowledgeIDs)
	if err := a.s.Knowledge.validatePayload(ctx, req.WorkspaceID, parsed); err != nil {
		return nil, false, err
	}
	hash := questionContentHash(parsed)
	existing, err := a.s.Repo.GetQuestionByContentHash(ctx, req.WorkspaceID, hash)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		return nil, true, nil // content_hash 冲突 → 跳过
	}

	id := NewID()
	versionID := NewID()
	if err := a.s.Repo.CreateQuestion(ctx, &repository.QuestionRow{
		ID: id, WorkspaceID: req.WorkspaceID, Type: parsed.Type,
		Source: parsed.Source, Tags: mustJSON(parsed.Tags), ContentHash: hash,
	}); err != nil {
		return nil, false, err
	}
	if err := a.s.Repo.CreateQuestionVersion(ctx, &repository.QuestionVersionRow{
		ID: versionID, QuestionID: id, VersionNo: 1,
		Payload: mustJSON(parsed), GeneratedBy: &model, PromptVer: pv, Review: "pending",
	}); err != nil {
		return nil, false, err
	}
	if len(parsed.KnowledgeIDs) > 0 {
		if err := a.s.Repo.SetQuestionKnowledge(ctx, versionID, parsed.KnowledgeIDs); err != nil {
			return nil, false, err
		}
	}
	q, err := a.s.Knowledge.questionByID(ctx, req.WorkspaceID, id)
	if err != nil {
		return nil, false, err
	}
	return q, false, nil
}

// mergeKnowledgeIDs 合并请求知识点与 LLM 建议知识点；LLM 建议仅并入工作区内已存在的节点。
func (a *AgentTasksService) mergeKnowledgeIDs(ctx context.Context, req AgentQuizGenReq, llmIDs []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	for _, id := range req.KnowledgeIDs {
		add(id)
	}
	for _, id := range llmIDs {
		n, err := a.s.Repo.GetKnowledgeNode(ctx, req.WorkspaceID, id)
		if err != nil || n == nil {
			continue
		}
		add(id)
	}
	return out
}

// recommendExisting 降级路径：基于已有 published 题目按知识点/题型筛选推荐，不生成新题。
func (a *AgentTasksService) recommendExisting(ctx context.Context, req AgentQuizGenReq) ([]*Question, error) {
	rows, _, _, err := a.s.Repo.ListQuestions(ctx, req.WorkspaceID, repository.QuestionFilter{Status: "published", Limit: 100})
	if err != nil {
		return nil, err
	}
	typeSet := map[string]bool{}
	for _, t := range req.Types {
		typeSet[t] = true
	}
	knowledgeSet := map[string]bool{}
	for _, id := range req.KnowledgeIDs {
		knowledgeSet[id] = true
	}
	var out []*Question
	for _, q := range rows {
		if len(out) >= req.Count {
			break
		}
		if q.CurrentVersionID == nil {
			continue
		}
		v, err := a.s.Repo.GetQuestionVersion(ctx, *q.CurrentVersionID)
		if err != nil || v == nil {
			continue
		}
		pl, err := parseQuestionPayload(v.Payload)
		if err != nil {
			continue
		}
		if len(typeSet) > 0 && !typeSet[pl.Type] {
			continue
		}
		if len(knowledgeSet) > 0 {
			has := false
			for _, kid := range pl.KnowledgeIDs {
				if knowledgeSet[kid] {
					has = true
					break
				}
			}
			if !has {
				continue
			}
		}
		dto, err := a.s.Knowledge.QuestionGet(ctx, QuestionGetReq{WorkspaceID: req.WorkspaceID, QuestionID: q.ID})
		if err != nil {
			return nil, err
		}
		out = append(out, dto)
	}
	return out, nil
}

func draftOptionsToService(opts []agent.DraftOption) []QuestionOption {
	out := make([]QuestionOption, 0, len(opts))
	for _, o := range opts {
		out = append(out, QuestionOption{Key: o.Key, Text: o.Text})
	}
	return out
}

// ---------- Debugger（10.12 代码调试助手） ----------

// sandboxCodeMax 送入沙箱的代码长度上限（避免超长 argv / 控制成本，超出则跳过沙箱）。
const sandboxCodeMax = 8000

// debuggerCommand 由语言推断"内联代码"执行命令；不支持的语言返回 ok=false（跳过沙箱，
// 仅用用户提供的 error_output）。
func debuggerCommand(lang string) ([]string, bool) {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "python", "py":
		return []string{"python", "-c"}, true
	case "python3":
		return []string{"python3", "-c"}, true
	case "node", "javascript", "js":
		return []string{"node", "-e"}, true
	default:
		return nil, false
	}
}

// AgentDebugReq 代码调试请求（Debugger）。
type AgentDebugReq struct {
	WorkspaceID    string `json:"workspace_id"`
	UserID         string `json:"user_id"`
	Language       string `json:"language"`
	Code           string `json:"code"`
	ErrorOutput    string `json:"error_output"`
	TestCases      string `json:"test_cases"`
	IdempotencyKey string `json:"idempotency_key"`
}

// AgentDebugResult 调试结果。
type AgentDebugResult struct {
	Result        *agent.DebugOutput `json:"result"`
	Degraded      bool               `json:"degraded"`
	SandboxUsed   bool               `json:"sandbox_used"`
	SandboxStderr string             `json:"sandbox_stderr,omitempty"`
	Message       string             `json:"message,omitempty"`
}

// AgentDebug 代码题答疑：隔离沙箱采集真实 stderr/exit → LLM 结构化解析
// （幂等 + 审计；沙箱失败映射 SANDBOX_LIMIT；未配置 Provider 确定性降级）。
func (a *AgentTasksService) AgentDebug(ctx context.Context, req AgentDebugReq) (*AgentDebugResult, error) {
	if err := a.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if req.Language == "" {
		return nil, domain.InvalidArg("language 必填")
	}
	if req.Code == "" {
		return nil, domain.InvalidArg("code 必填")
	}
	return withIdempotency(a.s, ctx, req.WorkspaceID, req.IdempotencyKey, "AgentDebug",
		func() (*AgentDebugResult, error) { return a.doDebug(ctx, req) })
}

// doDebug 执行代码调试（沙箱 → LLM → 结构化结果）。
func (a *AgentTasksService) doDebug(ctx context.Context, req AgentDebugReq) (*AgentDebugResult, error) {
	// 1. 隔离沙箱采集真实 stderr/exit（仅内联执行语言，代码超长跳过）
	stderr := req.ErrorOutput
	sandboxUsed := false
	if cmd, ok := debuggerCommand(req.Language); ok && len([]rune(req.Code)) <= sandboxCodeMax {
		res, err := a.sandbox().Run(ctx, sandbox.Spec{
			Args: append(cmd, req.Code),
			Env:  []string{"LUMO_SANDBOX=1"},
		})
		if err != nil {
			// 沙箱失败/超时 → SANDBOX_LIMIT 明确错误，不 panic
			a.s.audit(ctx, req.WorkspaceID, "agent.debug", "workspace", req.WorkspaceID,
				map[string]any{"status": "failed", "language": req.Language, "reason": err.Error()})
			return nil, providerError(err)
		}
		sandboxUsed = true
		if res.Stderr != "" {
			stderr = truncateRunes(res.Stderr, agentTasksTextBudget)
		}
		if res.ExitCode != 0 && stderr == "" {
			stderr = fmt.Sprintf("退出码 %d", res.ExitCode)
		}
	}

	llm, llmErr := a.s.Agent.LLMFactoryFunc()
	if llmErr != nil {
		// 降级：确定性模板解析（不 panic，可重试）
		out := debugFallback(req, stderr)
		a.s.audit(ctx, req.WorkspaceID, "agent.debug", "workspace", req.WorkspaceID,
			map[string]any{"status": "degraded", "language": req.Language, "sandbox_used": sandboxUsed, "reason": "provider not configured"})
		return &AgentDebugResult{Result: out, Degraded: true, SandboxUsed: sandboxUsed,
			SandboxStderr: truncateRunes(stderr, 2000), Message: "AI 模型未配置，已降级为模板解析"}, nil
	}

	system, prompt := agent.BuildDebugPrompt(agent.DebugInput{
		Language: req.Language, Code: truncateRunes(req.Code, agentTasksTextBudget),
		ErrorOutput: stderr, TestCases: truncateRunes(req.TestCases, agentTasksTextBudget),
	})
	var out agent.DebugOutput
	model, err := agent.ChatJSON(ctx, llm, system, prompt, &out)
	if err != nil {
		a.s.audit(ctx, req.WorkspaceID, "agent.debug", "workspace", req.WorkspaceID,
			map[string]any{"status": "failed", "language": req.Language, "reason": err.Error()})
		return nil, providerError(err)
	}
	a.s.audit(ctx, req.WorkspaceID, "agent.debug", "workspace", req.WorkspaceID,
		map[string]any{"status": "ready", "language": req.Language, "sandbox_used": sandboxUsed, "model": model})
	return &AgentDebugResult{Result: &out, SandboxUsed: sandboxUsed,
		SandboxStderr: truncateRunes(stderr, 2000)}, nil
}

// debugFallback 未配置 Provider 时的确定性调试模板。
func debugFallback(req AgentDebugReq, stderr string) *agent.DebugOutput {
	cause := "未配置 AI 模型，无法定位具体错误；请对照错误输出排查。"
	if stderr != "" {
		cause = "错误输出已捕获，请配置 AI 模型后获取完整定位与修复建议。"
	}
	return &agent.DebugOutput{
		Summary:       "AI 模型未配置，已降级为模板解析（不修改用户代码）",
		ErrorLocation: "（未配置 AI 模型，无法定位）",
		Cause:         cause,
		Steps: []agent.DebugStep{
			{Title: "复现", Content: "在本地运行代码，确认错误输出：" + truncateRunes(stderr, 200)},
			{Title: "定位", Content: "依据错误堆栈定位出错行，检查变量与类型。"},
			{Title: "验证", Content: "修改后用测试用例验证。"},
		},
		FixSuggestions: []agent.FixSuggestion{
			{Description: "配置模型 Provider 后重试", Example: "在「设置与数据」中配置 LLM Provider 后重新调试"},
		},
	}
}

// ---------- EssayGrader（10.12 作文批改） ----------

// AgentEssayGradeReq 作文批改请求（EssayGrader）。
type AgentEssayGradeReq struct {
	WorkspaceID    string  `json:"workspace_id"`
	UserID         string  `json:"user_id"`
	Stem           string  `json:"stem"`
	Rubric         string  `json:"rubric"`
	Essay          string  `json:"essay"`
	MaxScore       float64 `json:"max_score"`
	IdempotencyKey string  `json:"idempotency_key"`
}

// AgentEssayGradeResult 批改结果。
type AgentEssayGradeResult struct {
	Result   *agent.EssayGraderOutput `json:"result"`
	Degraded bool                     `json:"degraded"`
	Message  string                   `json:"message,omitempty"`
}

// AgentEssayGrade 作文批改：量规 + 作文 → 分维度评分（供 P5B AssignmentGrade 预批）。
// 幂等 + 审计；评分越界映射 OUTPUT_INVALID；未配置 Provider 确定性降级。
func (a *AgentTasksService) AgentEssayGrade(ctx context.Context, req AgentEssayGradeReq) (*AgentEssayGradeResult, error) {
	if err := a.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if req.Essay == "" {
		return nil, domain.InvalidArg("essay 必填")
	}
	if req.MaxScore <= 0 {
		return nil, domain.InvalidArg("max_score 须为正数")
	}
	return withIdempotency(a.s, ctx, req.WorkspaceID, req.IdempotencyKey, "AgentEssayGrade",
		func() (*AgentEssayGradeResult, error) { return a.doEssayGrade(ctx, req) })
}

// doEssayGrade 执行作文批改（LLM → 结构化评分 + 越界校验）。
func (a *AgentTasksService) doEssayGrade(ctx context.Context, req AgentEssayGradeReq) (*AgentEssayGradeResult, error) {
	llm, llmErr := a.s.Agent.LLMFactoryFunc()
	if llmErr != nil {
		// 降级：不伪造评分，输出占位结果（不落库，可重试）
		out := &agent.EssayGraderOutput{
			Comment:     "AI 模型未配置，无法评分；请配置 Provider 后重试",
			Suggestions: []string{"配置模型 Provider 后重新批改"},
		}
		a.s.audit(ctx, req.WorkspaceID, "agent.essaygrade", "workspace", req.WorkspaceID,
			map[string]any{"status": "degraded", "reason": "provider not configured"})
		return &AgentEssayGradeResult{Result: out, Degraded: true,
			Message: "AI 模型未配置，已降级（未生成有效评分）"}, nil
	}

	system, prompt := agent.BuildEssayGraderPrompt(agent.EssayGraderInput{
		Stem:     truncateRunes(req.Stem, agentTasksTextBudget),
		Rubric:   truncateRunes(req.Rubric, agentTasksTextBudget),
		Essay:    truncateRunes(req.Essay, agentTasksTextBudget),
		MaxScore: req.MaxScore,
	})
	var out agent.EssayGraderOutput
	model, err := agent.ChatJSON(ctx, llm, system, prompt, &out)
	if err != nil {
		a.s.audit(ctx, req.WorkspaceID, "agent.essaygrade", "workspace", req.WorkspaceID,
			map[string]any{"status": "failed", "reason": err.Error()})
		return nil, providerError(err)
	}
	if !agent.ValidEssayScores(out, req.MaxScore) {
		err := domain.WrapError(domain.CodeOutputInvalid, "评分超出范围", nil)
		a.s.audit(ctx, req.WorkspaceID, "agent.essaygrade", "workspace", req.WorkspaceID,
			map[string]any{"status": "failed", "reason": err.Error()})
		return nil, err
	}
	a.s.audit(ctx, req.WorkspaceID, "agent.essaygrade", "workspace", req.WorkspaceID,
		map[string]any{"status": "ready", "model": model})
	return &AgentEssayGradeResult{Result: &out}, nil
}
