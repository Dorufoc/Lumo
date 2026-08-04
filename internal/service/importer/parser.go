// Package importer 提供题库文件解析（markdown / json / text）与题目载荷构造。
package importer

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"lumo/internal/domain"
)

// ParsedQuestion 是一道解析出的题目。
type ParsedQuestion struct {
	Payload json.RawMessage `json:"payload"`
	Error   string          `json:"error,omitempty"`
}

// Parse 按格式解析题库内容。
func Parse(format string, content []byte) ([]ParsedQuestion, error) {
	switch format {
	case "json":
		return parseJSON(content)
	case "markdown", "text":
		return parseLineBased(format, content)
	default:
		return nil, domain.InvalidArg("format 仅允许 markdown/json/text")
	}
}

// parseJSON 解析 JSON 题库：{"questions": [...]} 或裸数组。
func parseJSON(content []byte) ([]ParsedQuestion, error) {
	var raw any
	if err := json.Unmarshal(content, &raw); err != nil {
		return nil, domain.InvalidArg("JSON 解析失败: %v", err)
	}
	var items []any
	switch v := raw.(type) {
	case []any:
		items = v
	case map[string]any:
		if qs, ok := v["questions"].([]any); ok {
			items = qs
		} else {
			return nil, domain.InvalidArg("JSON 题库须为数组或包含 questions 数组的对象")
		}
	default:
		return nil, domain.InvalidArg("JSON 题库须为数组或包含 questions 数组的对象")
	}
	out := make([]ParsedQuestion, 0, len(items))
	for i, it := range items {
		b, err := json.Marshal(it)
		if err != nil {
			out = append(out, ParsedQuestion{Error: fmt.Sprintf("第 %d 题序列化失败: %v", i+1, err)})
			continue
		}
		out = append(out, ParsedQuestion{Payload: b})
	}
	return out, nil
}

var (
	reHeadingMD = regexp.MustCompile(`^\s*#{2,6}\s*(?:\d+[.、)）]\s*)?(.*\S)\s*$`)
	reNumbered  = regexp.MustCompile(`^\s*\d+[.、)）]\s*(.*\S)\s*$`)
	reOption    = regexp.MustCompile(`^\s*(?:[-*]\s*)?([A-H])[.、)）]\s*(.*\S)\s*$`)
	reAnswer    = regexp.MustCompile(`^\s*(?:正确)?答案\s*[:：]\s*(.+?)\s*$`)
	reAnalysis  = regexp.MustCompile(`^\s*解析\s*[:：]\s*(.+?)\s*$`)
	reKnowledge = regexp.MustCompile(`^\s*(?:知识点|考点)\s*[:：]\s*(.+?)\s*$`)
	reSource    = regexp.MustCompile(`^\s*来源\s*[:：]\s*(.+?)\s*$`)
)

// questionBlock 是行解析中间结构。
type questionBlock struct {
	stem       string
	options    []string // "A|text"
	answerLine string
	analysis   string
	knowledge  string
	source     string
	extra      []string
}

// parseLineBased 解析 markdown/text 题库。
// 约定：
//   - 题目以标题（## ）或数字序号（1. ）开头
//   - 选项：A. 文本 / - A. 文本 / A) 文本
//   - 答案：答案：A 或 正确答案：AB（多选可 A,B / A、B）
//   - 解析/知识点/来源：独立行
//   - 判断题：选项固定为“正确/错误”
func parseLineBased(format string, content []byte) ([]ParsedQuestion, error) {
	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	var blocks []*questionBlock
	var cur *questionBlock
	flush := func() {
		if cur != nil {
			blocks = append(blocks, cur)
			cur = nil
		}
	}
	newBlock := func(stem string) {
		flush()
		cur = &questionBlock{stem: stem}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// markdown：## 及以上为题目开头；text：数字序号为题目开头。
		if m := reHeadingMD.FindStringSubmatch(trimmed); m != nil && format == "markdown" {
			newBlock(m[1])
			continue
		}
		// 一级标题或纯标题行：文档标题，忽略。
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if m := reNumbered.FindStringSubmatch(trimmed); m != nil {
			newBlock(m[1])
			continue
		}
		if cur == nil {
			// 文件开头的说明行作为题干
			newBlock(trimmed)
			continue
		}
		switch {
		case reAnswer.MatchString(trimmed):
			cur.answerLine = reAnswer.FindStringSubmatch(trimmed)[1]
		case reAnalysis.MatchString(trimmed):
			cur.analysis = reAnalysis.FindStringSubmatch(trimmed)[1]
		case reKnowledge.MatchString(trimmed):
			cur.knowledge = reKnowledge.FindStringSubmatch(trimmed)[1]
		case reSource.MatchString(trimmed):
			cur.source = reSource.FindStringSubmatch(trimmed)[1]
		case reOption.MatchString(trimmed):
			m := reOption.FindStringSubmatch(trimmed)
			cur.options = append(cur.options, m[1]+"|"+m[2])
		default:
			if cur.stem == "" {
				cur.stem = trimmed
			} else {
				cur.extra = append(cur.extra, trimmed)
			}
		}
	}
	flush()

	out := make([]ParsedQuestion, 0, len(blocks))
	for i, b := range blocks {
		payload, err := buildPayload(b)
		if err != nil {
			out = append(out, ParsedQuestion{Error: fmt.Sprintf("第 %d 题: %v", i+1, err)})
			continue
		}
		out = append(out, ParsedQuestion{Payload: payload})
	}
	if len(out) == 0 {
		return nil, domain.InvalidArg("未解析到任何题目，请检查文件格式")
	}
	return out, nil
}

// buildPayload 将行块构造成题目载荷（自动推断题型）。
func buildPayload(b *questionBlock) (json.RawMessage, error) {
	if strings.TrimSpace(b.stem) == "" {
		return nil, fmt.Errorf("题干为空")
	}
	// 判断题识别：两个选项且为 正确/错误
	if len(b.options) == 2 && isTrueFalse(b.options) {
		opts := []map[string]any{
			{"key": "A", "text": "正确"},
			{"key": "B", "text": "错误"},
		}
		answer := "A"
		if strings.Contains(strings.ToUpper(b.answerLine), "错") && !strings.Contains(strings.ToUpper(b.answerLine), "对") {
			answer = "B"
		}
		return marshal(map[string]any{
			"type": "single_choice", "stem": b.stem, "options": opts, "answer": answer,
			"analysis": b.analysis, "source": sourceOf(b.source),
		})
	}
	if len(b.options) > 0 {
		keys := make([]string, 0, len(b.options))
		opts := make([]map[string]any, 0, len(b.options))
		for _, o := range b.options {
			parts := strings.SplitN(o, "|", 2)
			keys = append(keys, parts[0])
			opts = append(opts, map[string]any{"key": parts[0], "text": parts[1]})
		}
		answer, multi, err := parseAnswer(b.answerLine, keys)
		if err != nil {
			return nil, err
		}
		qtype := "single_choice"
		if multi {
			qtype = "multiple_choice"
		}
		return marshal(map[string]any{
			"type": qtype, "stem": b.stem, "options": opts, "answer": answer,
			"analysis": b.analysis, "source": sourceOf(b.source),
		})
	}
	// 无选项：填空（题干含下划线/括号）或简答
	if strings.Contains(b.stem, "____") || strings.Contains(b.stem, "__") ||
		strings.Contains(b.stem, "（）") || strings.Contains(b.stem, "()") {
		answer := strings.SplitN(b.answerLine, "；", -1)
		if len(answer) == 1 && !strings.Contains(b.answerLine, ",") && !strings.Contains(b.answerLine, "、") {
			return marshal(map[string]any{
				"type": "fill_blank", "stem": b.stem, "answer": answer[0],
				"analysis": b.analysis, "source": sourceOf(b.source),
			})
		}
		var answers []string
		for _, a := range strings.FieldsFunc(b.answerLine, func(r rune) bool {
			return r == '，' || r == ',' || r == '、' || r == '；' || r == ';'
		}) {
			if a = strings.TrimSpace(a); a != "" {
				answers = append(answers, a)
			}
		}
		return marshal(map[string]any{
			"type": "fill_blank", "stem": b.stem, "answer": answers,
			"analysis": b.analysis, "source": sourceOf(b.source),
		})
	}
	return marshal(map[string]any{
		"type": "short_answer", "stem": b.stem, "answer": b.answerLine,
		"analysis": b.analysis, "source": sourceOf(b.source),
	})
}

func isTrueFalse(options []string) bool {
	texts := make([]string, 0, 2)
	for _, o := range options {
		parts := strings.SplitN(o, "|", 2)
		texts = append(texts, parts[1])
	}
	return (texts[0] == "正确" || texts[0] == "对") && (texts[1] == "错误" || texts[1] == "错") ||
		(texts[1] == "正确" || texts[1] == "对") && (texts[0] == "错误" || texts[0] == "错")
}

// parseAnswer 解析答案行：返回 answer 值（string 或 []string）与是否多选。
func parseAnswer(line string, keys []string) (any, bool, error) {
	line = strings.ToUpper(strings.TrimSpace(line))
	if line == "" {
		return nil, false, fmt.Errorf("缺少答案")
	}
	if strings.HasPrefix(line, "[") { // JSON 数组
		var arr []string
		if json.Unmarshal([]byte(line), &arr) == nil {
			return arr, true, validateKeys(arr, keys)
		}
	}
	// 紧凑多选：如 AC、ABD（全部为 A-H 字母且长度>1）
	if len(line) > 1 && allOptionLetters(line) {
		arr := strings.Split(line, "")
		return arr, true, validateKeys(arr, keys)
	}
	var single string
	if !strings.ContainsAny(line, ",，、;； ") {
		single = line
	} else {
		var arr []string
		for _, part := range strings.FieldsFunc(line, func(r rune) bool {
			return r == ',' || r == '，' || r == '、' || r == ';' || r == '；' || r == ' '
		}) {
			arr = append(arr, part)
		}
		return arr, true, validateKeys(arr, keys)
	}
	if err := validateKeys([]string{single}, keys); err != nil {
		return nil, false, err
	}
	return single, false, nil
}

func allOptionLetters(s string) bool {
	for _, r := range s {
		if r < 'A' || r > 'H' {
			return false
		}
	}
	return true
}

func validateKeys(ans, keys []string) error {
	set := map[string]bool{}
	for _, k := range keys {
		set[k] = true
	}
	for _, a := range ans {
		if !set[a] {
			return fmt.Errorf("答案 %s 不在选项 %s 中", a, strings.Join(keys, ","))
		}
	}
	return nil
}

func sourceOf(s string) string {
	if s == "" {
		return "import"
	}
	return "import:" + s
}

func marshal(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return b, nil
}
