package agent

import (
	"context"
	"encoding/json"
	"strings"

	"lumo/internal/domain"
	"lumo/internal/provider"
)

// ChatJSON 调用 LLM 并要求 JSON 输出（非流式，供 service 层与 handler 复用）。
// 与 chatJSON 同一套剥离 ```json 代码块包裹的逻辑（handlers.go L99-108）。
// 返回模型名（用于落库 model 字段）。
func ChatJSON(ctx context.Context, llm provider.LLMProvider, system, prompt string, out any) (string, error) {
	if llm == nil {
		return "", provider.ErrNotConfigured
	}
	res, err := llm.Chat(ctx, provider.ChatRequest{
		Model: "", Messages: []provider.Message{
			{Role: "system", Content: system},
			{Role: "user", Content: prompt},
		}, MaxTokens: 2048, Temperature: 0.2, JSONMode: true,
	}, nil)
	if err != nil {
		return "", err
	}
	content := res.Content
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)
	if err := json.Unmarshal([]byte(content), out); err != nil {
		return "", domain.WrapError(domain.CodeOutputInvalid, "模型输出不是合法 JSON: %v", err)
	}
	return res.Model, nil
}

// ---------- Summarizer（10.9 资料摘要与要点提取） ----------

// SummaryPoint 是摘要要点（每条带 chunk_id 定位）。
type SummaryPoint struct {
	Content string `json:"content"`
	ChunkID string `json:"chunk_id"`
}

// GlossaryTerm 是术语条目。
type GlossaryTerm struct {
	Term       string `json:"term"`
	Definition string `json:"definition"`
}

// SummarizeOutput 是设计文档 10.9 的摘要输出 JSON Schema：
// {title, key_points[], structure[], glossary[]{term, definition}, citation_refs[]}。
type SummarizeOutput struct {
	Title        string         `json:"title"`
	KeyPoints    []SummaryPoint `json:"key_points"`
	Structure    []string       `json:"structure"`
	Glossary     []GlossaryTerm `json:"glossary"`
	CitationRefs []string       `json:"citation_refs"`
}

// SummarizeInput 是摘要输入（handler 消息载荷与 service 层共用）。
type SummarizeInput struct {
	Title       string `json:"title"`
	Text        string `json:"text"`        // 分块正文（含 [chunk:xxx] 定位标记）
	Preferences string `json:"preferences"` // 用户要点偏好（可选）
}

// BuildSummarizePrompt 构造摘要 system+user 提示词（设计文档 10.9 契约）。
func BuildSummarizePrompt(in SummarizeInput) (system, prompt string) {
	system = `你是 Lumo AI 的文档摘要助手。将提供的文档压缩为结构化摘要，只输出 JSON，不要额外说明。输出格式：
{"title":"文档标题","key_points":[{"content":"要点内容","chunk_id":"来源分块id"}],"structure":["小节标题"],"glossary":[{"term":"术语","definition":"定义"}],"citation_refs":["引用来源"]}
要求：
1. 每条 key_points 必须带 chunk_id 定位，chunk_id 取自正文中的 [chunk:xxx] 标记；
2. 所有要点正文（key_points[].content）合计不超过 800 字；
3. 无证据支撑的要点丢弃，绝不编造来源；
4. 使用简体中文。`
	var b strings.Builder
	if in.Title != "" {
		b.WriteString("文档标题：" + in.Title + "\n")
	}
	if in.Preferences != "" {
		b.WriteString("用户要点偏好：" + in.Preferences + "\n")
	}
	b.WriteString("文档正文（分块）：\n")
	b.WriteString(in.Text)
	return system, b.String()
}

// SummarizerHandler 文档摘要处理器（会话流：输入 JSON → 输出结构化摘要 JSON）。
type SummarizerHandler struct{}

// Run 解析消息 JSON 并输出结构化摘要。
func (h *SummarizerHandler) Run(ctx context.Context, emit Emit, in *RunInput) error {
	var si SummarizeInput
	if err := json.Unmarshal([]byte(in.Message), &si); err != nil {
		return domain.InvalidArg("摘要输入格式非法（需要 JSON {title, text, preferences}）")
	}
	system, prompt := BuildSummarizePrompt(si)
	if in.LLMErr == nil && in.LLM != nil {
		var out SummarizeOutput
		if err := chatJSON(ctx, in, system, prompt, &out); err != nil {
			return err
		}
		b, _ := json.Marshal(out)
		emit("agent:delta", map[string]any{"delta": string(b), "citations": []any{}})
		emit("agent:completed", map[string]any{"message_id": newID(), "usage": map[string]any{}, "citations": []any{}})
		return nil
	}
	return streamLLM(ctx, in, emit, system, prompt)
}

var _ Handler = (*SummarizerHandler)(nil)
