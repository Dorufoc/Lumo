package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"lumo/internal/domain"
)

// ---------- QuizGen（10.10 资料出题与知识点扫盲） ----------

// DraftOption 是题目草稿选项（与 service.QuestionOption JSON 结构一致）。
type DraftOption struct {
	Key  string `json:"key"`
	Text string `json:"text"`
}

// QuizDraft 是 LLM 输出的单题草稿。
// payload 字段与 ExamPaperAutoGenerate 的题目载荷契约一致（stem/options/answer/analysis/difficulty/knowledge_ids），
// 使草稿经审核发布后可被组卷选取。
type QuizDraft struct {
	Type         string          `json:"type"`
	Stem         string          `json:"stem"`
	Options      []DraftOption   `json:"options,omitempty"`
	Answer       json.RawMessage `json:"answer"`
	Analysis     string          `json:"analysis,omitempty"`
	Difficulty   int             `json:"difficulty"`
	KnowledgeIDs []string        `json:"knowledge_ids,omitempty"`
}

// QuizGenOutput 是 LLM 输出的题目草稿列表。
type QuizGenOutput struct {
	Questions []QuizDraft `json:"questions"`
}

// QuizGenInput 是出题输入（handler 消息载荷与 service 层共用）。
type QuizGenInput struct {
	Types     []string // 题型偏好（默认客观题）
	Count     int      // 题量 1-10
	Knowledge string   // 知识点说明文本
	Material  string   // 资料正文（分块）
}

// BuildQuizGenPrompt 构造出题 system+user 提示词（设计文档 10.10 契约）。
func BuildQuizGenPrompt(in QuizGenInput) (system, prompt string) {
	system = `你是 Lumo AI 的出题助手。基于提供的知识点与资料生成题目草稿，只输出 JSON，不要额外说明。输出格式：
{"questions":[{"type":"single_choice|multiple_choice|fill_blank|short_answer|code","stem":"题干","options":[{"key":"A","text":"选项"}],"answer":"单选填选项key，多选填key数组，填空/简答填答案文本","analysis":"解析","difficulty":1-5,"knowledge_ids":["知识点id"]}]}
要求：
1. 答案与解析必须基于资料原文，与资料冲突的题目用 analysis 标注"与资料待核实"；
2. 知识点知识点尽量使用输入中给出的 knowledge_ids；
3. 使用简体中文。`
	var b strings.Builder
	b.WriteString(fmt.Sprintf("题型偏好：%v\n题量：%d 道\n", in.Types, in.Count))
	if in.Knowledge != "" {
		b.WriteString("知识点：\n" + in.Knowledge + "\n")
	}
	if in.Material != "" {
		b.WriteString("资料正文（分块）：\n" + in.Material + "\n")
	}
	return system, b.String()
}

// QuizGenHandler 出题处理器（会话流：输入 JSON → 输出题目草稿 JSON 列表）。
type QuizGenHandler struct{}

// Run 解析消息 JSON 并输出题目草稿列表。
func (h *QuizGenHandler) Run(ctx context.Context, emit Emit, in *RunInput) error {
	var gi QuizGenInput
	if err := json.Unmarshal([]byte(in.Message), &gi); err != nil {
		return domain.InvalidArg("出题输入格式非法（需要 JSON）")
	}
	system, prompt := BuildQuizGenPrompt(gi)
	if in.LLMErr == nil && in.LLM != nil {
		var out QuizGenOutput
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

var _ Handler = (*QuizGenHandler)(nil)
