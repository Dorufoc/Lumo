package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"lumo/internal/domain"
	"lumo/internal/provider"
	"lumo/internal/repository"
)

func newID() string { return uuid.NewString() }
func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// Emit 发布事件。
type Emit func(name string, payload map[string]any)

// RunInput 是处理器输入。
type RunInput struct {
	Session *repository.AgentSessionRow
	Message string
	LLM     provider.LLMProvider
	LLMErr  error // Provider 未配置等原因
}

// Handler 是 Agent 处理器。
type Handler interface {
	Run(ctx context.Context, emit Emit, in *RunInput) error
}

// handlerFor 按 Agent 类型选择处理器。
func (a *Service) handlerFor(agentType string, llm provider.LLMProvider) Handler {
	switch agentType {
	case "grader":
		return &GraderHandler{}
	case "diagnoser":
		return &DiagnoserHandler{}
	case "tutor":
		return &TutorHandler{}
	case "librarian":
		return &LibrarianHandler{}
	case "summarizer":
		return &SummarizerHandler{}
	case "quizgen":
		return &QuizGenHandler{}
	default:
		return &RouterHandler{}
	}
}

// ---------- 通用 LLM 流 ----------

// streamLLM 流式调用 LLM；未配置时降级为模板回复。
func streamLLM(ctx context.Context, in *RunInput, emit Emit, system, prompt string) error {
	if in.LLMErr != nil || in.LLM == nil {
		// 未配置 Provider：确定性降级
		text := fallbackReply(in.Session.Agent, in.Message)
		emit("agent:delta", map[string]any{"delta": text, "citations": []any{}})
		emit("agent:completed", map[string]any{
			"message_id": newID(), "usage": map[string]any{}, "citations": []any{},
		})
		return nil
	}
	msgs := []provider.Message{{Role: "system", Content: system}, {Role: "user", Content: prompt}}
	_, err := in.LLM.Chat(ctx, provider.ChatRequest{
		Model: "", Messages: msgs, MaxTokens: 1024, Temperature: 0.4,
	}, func(delta string) {
		emit("agent:delta", map[string]any{"delta": delta, "citations": []any{}})
	})
	if err != nil {
		if ctx.Err() == context.Canceled {
			return context.Canceled
		}
		return err
	}
	emit("agent:completed", map[string]any{
		"message_id": newID(), "usage": map[string]any{}, "citations": []any{},
	})
	return nil
}

// chatJSON 调用 LLM 并要求 JSON 输出（非流式）。
func chatJSON(ctx context.Context, in *RunInput, system, prompt string, out any) error {
	if in.LLMErr != nil || in.LLM == nil {
		return provider.ErrNotConfigured
	}
	_, err := ChatJSON(ctx, in.LLM, system, prompt, out)
	return err
}

// fallbackReply 未配置 Provider 时的确定性回复模板。
func fallbackReply(agentType, message string) string {
	switch agentType {
	case "tutor":
		return "（未配置 AI 模型）\n\n作为引导式讲解，建议你先：\n1. 回忆题目的知识点并写下你的思路；\n2. 对照标准解析检查每一步；\n3. 把仍然困惑的部分告诉我。\n\n配置模型 Provider 后，我可以为你提供真正的苏格拉底式引导。"
	case "grader":
		return "（未配置 AI 评分）\n\n系统暂无法进行 AI 评分，请查看题目标准解析自评，或在「设置与数据」中配置模型 Provider 后重新评分。"
	case "diagnoser":
		return "（未配置 AI 错因诊断）\n\n请对照解析确认错因类型：概念不清 / 审题错误 / 计算错误 / 记忆混淆 / 方法不熟 / 表达不完整，然后到错题中标记。"
	default:
		return fmt.Sprintf("（未配置 AI 模型）\n\n已收到你的问题：「%s」。\n配置模型 Provider 后我将为你提供智能回答。", truncateRunes(message, 60))
	}
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ---------- Router ----------

// RouterHandler 意图路由（规则分类 + 提示）。
type RouterHandler struct{}

func (h *RouterHandler) Run(ctx context.Context, emit Emit, in *RunInput) error {
	system := `你是 Lumo AI 的意图路由器。用户消息可能涉及：题目讲解（tutor）、主观题评分（grader）、错因诊断（diagnoser）、学习资料问答（librarian）、学习计划建议（planner）。请用简洁中文确认你的角色并引导用户。`
	return streamLLM(ctx, in, emit, system, in.Message)
}

// ---------- Tutor ----------

// TutorHandler 苏格拉底引导（不直接给答案）。
type TutorHandler struct{}

func (h *TutorHandler) Run(ctx context.Context, emit Emit, in *RunInput) error {
	system := `你是 Lumo AI 的学习导师。遵循苏格拉底式引导：
1. 除非用户明确请求，不要直接给出最终答案；
2. 通过追问启发用户自己推理；
3. 涉及题目时区分"题目事实"与"你的推理建议"；
4. 若题目有标准解析，引导用户先看解析再对照；
5. 使用简体中文，语气鼓励、简洁。`
	return streamLLM(ctx, in, emit, system, in.Message)
}

// ---------- Diagnoser ----------

// DiagnoserHandler 错因诊断。
type DiagnoserHandler struct{}

func (h *DiagnoserHandler) Run(ctx context.Context, emit Emit, in *RunInput) error {
	system := `你是 Lumo AI 的错因诊断助手。基于用户提供的题目与错误答案，输出 JSON：
{"cause":"concept|reading|calculation|memory|method|expression","confidence":0.0-1.0,"suggestion":"纠正建议"}
错因分类：概念不清、审题错误、计算错误、记忆混淆、方法不熟、表达不完整。`
	if in.LLMErr == nil && in.LLM != nil {
		var result map[string]any
		err := chatJSON(ctx, in, system, "用户信息："+in.Message, &result)
		if err != nil {
			return err
		}
		b, _ := json.Marshal(result)
		emit("agent:delta", map[string]any{"delta": string(b), "citations": []any{}})
		emit("agent:completed", map[string]any{"message_id": newID(), "usage": map[string]any{}, "citations": []any{}})
		return nil
	}
	return streamLLM(ctx, in, emit, system, in.Message)
}

// ---------- Librarian ----------

// LibrarianHandler 资料问答（RAG 检索由调用方注入上下文）。
type LibrarianHandler struct{}

func (h *LibrarianHandler) Run(ctx context.Context, emit Emit, in *RunInput) error {
	system := `你是 Lumo AI 的资料问答助手。回答必须基于提供的资料片段，并标注引用（文档名与位置）。没有足够证据时明确说明不确定，绝不编造来源。使用简体中文。`
	return streamLLM(ctx, in, emit, system, in.Message)
}

// ---------- Grader ----------

// GraderHandler 主观题评分（结构化输出）。
type GraderHandler struct{}

// GradeInput 是评分输入。
type GradeInput struct {
	Stem     string `json:"stem"`
	Answer   string `json:"answer"`   // 学生答案
	Standard string `json:"standard"` // 参考答案
	Rubric   string `json:"rubric"`   // 量规
	MaxScore float64 `json:"max_score"`
}

// GradeOutput 是评分输出。
type GradeOutput struct {
	Score      float64 `json:"score"`
	Reason     string  `json:"reason"`
	Confidence float64 `json:"confidence"`
}

// Run 执行评分（返回 JSON 结果给调用方）。
func (h *GraderHandler) Run(ctx context.Context, emit Emit, in *RunInput) error {
	var gi GradeInput
	if err := json.Unmarshal([]byte(in.Message), &gi); err != nil {
		return domain.InvalidArg("评分输入格式非法（需要 JSON）")
	}
	system := `你是严格的作业评分员。根据题目、参考答案与量规为学生答案评分。只输出 JSON：
{"score":分数,"reason":"评分理由（简洁）","confidence":0.0-1.0}
分数不得超过满分。`
	prompt := fmt.Sprintf(`题目：%s
参考答案：%s
量规：%s
学生答案：%s
满分：%.1f`, gi.Stem, gi.Standard, gi.Rubric, gi.Answer, gi.MaxScore)

	if in.LLMErr == nil && in.LLM != nil {
		var out GradeOutput
		if err := chatJSON(ctx, in, system, prompt, &out); err != nil {
			return err
		}
		if out.Score < 0 || out.Score > gi.MaxScore {
			return domain.WrapError(domain.CodeOutputInvalid, "评分超出范围", nil)
		}
		b, _ := json.Marshal(out)
		emit("agent:delta", map[string]any{"delta": string(b), "citations": []any{}})
		emit("agent:completed", map[string]any{"message_id": newID(), "usage": map[string]any{}, "citations": []any{}})
		return nil
	}
	return streamLLM(ctx, in, emit, system, prompt)
}

// GradeSubmission 是 service.Grader 接口实现：读取提交并评分写库。
type GradeSubmission struct {
	Repo   *repository.Repo
	Agent  *Service
}

// Grade 异步评分入口（由 practice 提交时调用）。
func (g *GradeSubmission) Grade(ctx context.Context, workspaceID, gradingID string) error {
	grading, err := g.Repo.GetGrading(ctx, gradingID)
	if err != nil {
		return err
	}
	if grading == nil {
		return domain.NotFound("判分记录不存在")
	}
	sub, err := g.Repo.GetSubmission(ctx, grading.SubmissionID)
	if err != nil {
		return err
	}
	if sub == nil {
		return domain.NotFound("提交记录不存在")
	}
	version, err := g.Repo.GetQuestionVersion(ctx, sub.QuestionVersionID)
	if err != nil {
		return err
	}
	if version == nil {
		return domain.NotFound("题目版本不存在")
	}
	var payload map[string]any
	if err := json.Unmarshal(version.Payload, &payload); err != nil {
		return err
	}
	stem, _ := payload["stem"].(string)
	answer, _ := payload["answer"].(json.RawMessage)
	analysis, _ := payload["analysis"].(string)

	// 组装评分输入（会话：grader）
	session, err := g.Agent.AgentChatCreate(ctx, AgentChatCreateReq{
		WorkspaceID: workspaceID, UserID: "", Agent: "grader",
		Context: "主观题评分",
	})
	if err != nil {
		return err
	}
	input, _ := json.Marshal(GradeInput{
		Stem: stem, Answer: string(sub.Answer), Standard: string(answer),
		Rubric: analysis, MaxScore: grading.MaxScore,
	})

	var out GradeOutput
	llm, llmErr := g.Agent.llm()
	runIn := &RunInput{
		Session: &repository.AgentSessionRow{ID: session.ID, Agent: "grader"},
		Message: string(input), LLM: llm, LLMErr: llmErr,
	}
	var captured string
	emit := func(name string, payload map[string]any) {
		if name == "agent:delta" {
			if d, ok := payload["delta"].(string); ok {
				captured += d
			}
		}
	}

	// 同步执行评分（无订阅者也可运行，结果通过 emit 捕获）。
	if err := (&GraderHandler{}).Run(ctx, emit, runIn); err != nil {
		if err == provider.ErrNotConfigured {
			return g.Repo.UpdateGrading(ctx, gradingID, "failed", nil, nil, "未配置 AI 评分服务，请配置 Provider 后重新评分", false)
		}
		return err
	}
	if captured == "" {
		return g.Repo.UpdateGrading(ctx, gradingID, "failed", nil, nil, "AI 评分未返回有效结果", false)
	}
	if err := json.Unmarshal([]byte(captured), &out); err != nil {
		return g.Repo.UpdateGrading(ctx, gradingID, "failed", nil, nil, "AI 评分输出格式非法", false)
	}
	return g.Repo.UpdateGrading(ctx, gradingID, "completed", &out.Score, &out.Confidence, out.Reason, out.Confidence < 0.5)
}
