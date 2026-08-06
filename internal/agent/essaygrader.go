package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"lumo/internal/domain"
)

// ---------- EssayGrader（10.12 作文批改） ----------

// EssayGraderInput 是作文批改输入（handler 消息载荷与 service 层共用）。
type EssayGraderInput struct {
	Stem     string  `json:"stem"`
	Rubric   string  `json:"rubric"`
	Essay    string  `json:"essay"`
	MaxScore float64 `json:"max_score"`
}

// DimensionScores 是分维度得分（内容/结构/语言/规范）。
type DimensionScores struct {
	Content   float64 `json:"content"`
	Structure float64 `json:"structure"`
	Language  float64 `json:"language"`
	Standard  float64 `json:"standard"`
}

// EssayGraderOutput 是设计文档 10.12 的批改输出（供 P5B AssignmentGrade 预批）。
type EssayGraderOutput struct {
	Dimensions   DimensionScores `json:"dimensions"`
	OverallScore float64         `json:"overall_score"`
	Comment      string          `json:"comment"`
	Suggestions  []string        `json:"suggestions"`
}

// BuildEssayGraderPrompt 构造批改 system+user 提示词（设计文档 10.12 契约）。
func BuildEssayGraderPrompt(in EssayGraderInput) (system, prompt string) {
	system = `你是 Lumo AI 的作文批改老师。基于题目、量规与学生作文，从内容/结构/语言/规范四个维度评分，并给出总评与改进建议。只输出 JSON，不要额外说明。输出格式：
{"dimensions":{"content":0-满分,"structure":0-满分,"language":0-满分,"standard":0-满分},"overall_score":0-满分,"comment":"总评","suggestions":["改进建议"]}
要求：
1. 各维度得分与总评均不得超过满分；
2. 打分依据量规；语言规范扣分项（错别字/标点/格式）在 standard 维度体现；
3. 建议具体可执行，至少一条；
4. 使用简体中文。`
	var b strings.Builder
	b.WriteString("题目：" + in.Stem + "\n")
	if in.Rubric != "" {
		b.WriteString("量规：\n" + in.Rubric + "\n")
	}
	b.WriteString(fmt.Sprintf("满分：%.1f\n", in.MaxScore))
	b.WriteString("学生作文：\n" + in.Essay + "\n")
	return system, b.String()
}

// ValidEssayScores 校验各维度与总评均在 [0, max]。
func ValidEssayScores(out EssayGraderOutput, max float64) bool {
	if max <= 0 {
		return false
	}
	dims := []float64{out.Dimensions.Content, out.Dimensions.Structure,
		out.Dimensions.Language, out.Dimensions.Standard, out.OverallScore}
	for _, s := range dims {
		if s < 0 || s > max {
			return false
		}
	}
	return true
}

// EssayGraderHandler 作文批改处理器（会话流：输入 JSON → 输出结构化评分 JSON）。
type EssayGraderHandler struct{}

// Run 解析消息 JSON 并输出结构化评分。
func (h *EssayGraderHandler) Run(ctx context.Context, emit Emit, in *RunInput) error {
	var ei EssayGraderInput
	if err := json.Unmarshal([]byte(in.Message), &ei); err != nil {
		return domain.InvalidArg("批改输入格式非法（需要 JSON {stem, rubric, essay, max_score}）")
	}
	system, prompt := BuildEssayGraderPrompt(ei)
	if in.LLMErr == nil && in.LLM != nil {
		var out EssayGraderOutput
		if err := chatJSON(ctx, in, system, prompt, &out); err != nil {
			return err
		}
		if !ValidEssayScores(out, ei.MaxScore) {
			return domain.WrapError(domain.CodeOutputInvalid, "评分超出范围", nil)
		}
		b, _ := json.Marshal(out)
		emit("agent:delta", map[string]any{"delta": string(b), "citations": []any{}})
		emit("agent:completed", map[string]any{"message_id": newID(), "usage": map[string]any{}, "citations": []any{}})
		return nil
	}
	return streamLLM(ctx, in, emit, system, prompt)
}

var _ Handler = (*EssayGraderHandler)(nil)
