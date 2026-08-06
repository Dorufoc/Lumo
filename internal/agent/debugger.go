package agent

import (
	"context"
	"encoding/json"
	"strings"

	"lumo/internal/domain"
)

// ---------- Debugger（10.12 代码调试助手） ----------

// DebugInput 是调试输入（handler 消息载荷与 service 层共用）。
type DebugInput struct {
	Language    string `json:"language"`
	Code        string `json:"code"`
	ErrorOutput string `json:"error_output"` // 沙箱真实 stderr/用户提供
	TestCases   string `json:"test_cases"`
}

// FixSuggestion 是修复建议条目。
type FixSuggestion struct {
	Description string `json:"description"`
	Example     string `json:"example"`
}

// DebugStep 是分步解析条目。
type DebugStep struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

// DebugOutput 是设计文档 10.12 的调试输出：
// 错误定位/原因/分步解析/修复建议（不直接改用户代码）。
type DebugOutput struct {
	Summary        string          `json:"summary"`
	ErrorLocation  string          `json:"error_location"`
	Cause          string          `json:"cause"`
	Steps          []DebugStep     `json:"steps"`
	FixSuggestions []FixSuggestion `json:"fix_suggestions"`
}

// BuildDebugPrompt 构造调试 system+user 提示词（设计文档 10.12 契约）。
func BuildDebugPrompt(in DebugInput) (system, prompt string) {
	system = `你是 Lumo AI 的代码调试助手。基于用户提供的代码、错误输出与测试用例，输出结构化调试结果，只输出 JSON，不要额外说明。输出格式：
{"summary":"总体结论","error_location":"错误定位（文件/行/函数或代码片段）","cause":"错误原因","steps":[{"title":"步骤标题","content":"步骤说明"}],"fix_suggestions":[{"description":"修改建议","example":"最小示例代码"}]}
要求：
1. 只诊断与建议，绝不直接改写用户代码（不输出用户代码的完整修正版，仅给出最小示例）；
2. steps 为分步解析（执行流程/出错点/验证思路）；
3. 无明确错误时定位到可能出错的代码段并说明验证方法；
4. 使用简体中文。`
	var b strings.Builder
	b.WriteString("语言：" + in.Language + "\n")
	b.WriteString("用户代码：\n" + in.Code + "\n")
	if in.ErrorOutput != "" {
		b.WriteString("错误输出（沙箱/运行时）：\n" + in.ErrorOutput + "\n")
	}
	if in.TestCases != "" {
		b.WriteString("测试用例：\n" + in.TestCases + "\n")
	}
	return system, b.String()
}

// DebuggerHandler 代码调试处理器（会话流：输入 JSON → 输出结构化调试 JSON）。
type DebuggerHandler struct{}

// Run 解析消息 JSON 并输出结构化调试结果。
func (h *DebuggerHandler) Run(ctx context.Context, emit Emit, in *RunInput) error {
	var di DebugInput
	if err := json.Unmarshal([]byte(in.Message), &di); err != nil {
		return domain.InvalidArg("调试输入格式非法（需要 JSON {language, code, error_output, test_cases}）")
	}
	system, prompt := BuildDebugPrompt(di)
	if in.LLMErr == nil && in.LLM != nil {
		var out DebugOutput
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

var _ Handler = (*DebuggerHandler)(nil)
