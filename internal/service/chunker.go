package service

import (
	"regexp"
	"strings"
	"unicode"
)

// TextChunk 是分块结果。
type TextChunk struct {
	Text      string
	Section   *string
	StartOff  int
	EndOff    int
	Paragraph int
}

var (
	reTitle   = regexp.MustCompile(`^#{1,6}\s+(.*)$`)
	reCodeFence = regexp.MustCompile("^```")
)

// chunkText 将文本按段落与标题分块（maxLen 默认 800，overlap 默认 80）。
func chunkText(text string, maxLen, overlap int) []TextChunk {
	if maxLen <= 0 {
		maxLen = 800
	}
	if overlap <= 0 || overlap >= maxLen {
		overlap = 80
	}
	// 按行处理，标题行记录 section
	type para struct {
		text    string
		section *string
		start   int
		no      int
	}
	var paras []para
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var buf strings.Builder
	startOff := 0
	paraNo := 0
	section := ""
	flush := func() {
		s := strings.TrimSpace(buf.String())
		if s != "" {
			var sec *string
			if section != "" {
				ss := section
				sec = &ss
			}
			paras = append(paras, para{text: s, section: sec, start: startOff, no: paraNo})
		}
		buf.Reset()
		paraNo++
	}
	inCode := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if reCodeFence.MatchString(trimmed) {
			inCode = !inCode
			buf.WriteString(line + "\n")
			startOff += len(line) + 1
			continue
		}
		if !inCode && trimmed == "" {
			flush()
			startOff += len(line) + 1
			continue
		}
		if !inCode {
			if m := reTitle.FindStringSubmatch(trimmed); m != nil {
				flush()
				section = m[1]
			}
		}
		buf.WriteString(line + "\n")
		startOff += len(line) + 1
	}
	flush()

	var out []TextChunk
	for _, p := range paras {
		runes := []rune(p.text)
		if len(runes) <= maxLen {
			out = append(out, TextChunk{Text: p.text, Section: p.section, StartOff: p.start, EndOff: p.start + len(p.text), Paragraph: p.no})
			continue
		}
		// 长段落按 maxLen 切分（带重叠）
		i := 0
		for i < len(runes) {
			end := i + maxLen
			if end > len(runes) {
				end = len(runes)
			}
			out = append(out, TextChunk{
				Text: string(runes[i:end]), Section: p.section,
				StartOff: p.start + len(string(runes[:i])),
				EndOff:   p.start + len(string(runes[:end])),
				Paragraph: p.no,
			})
			i = end - overlap
		}
	}
	return out
}

// extractText 按 MIME 提取文本（markdown/txt 支持；其他类型返回不支持）。
func extractText(mime string, content []byte) (string, bool) {
	switch mime {
	case "text/markdown", "text/x-markdown", "text/plain", "application/octet-stream":
		return string(content), true
	default:
		return "", false
	}
}

// tokenize 简单分词（中文按字 n-gram + 英文按词）。
func tokenize(s string) []string {
	var tokens []string
	lower := strings.ToLower(s)
	var word []rune
	for _, r := range lower {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			word = append(word, r)
			continue
		}
		if len(word) > 0 {
			tokens = append(tokens, string(word))
			word = nil
		}
	}
	if len(word) > 0 {
		tokens = append(tokens, string(word))
	}
	return tokens
}

// scoreText 关键词覆盖打分：命中词数/总词数（简化 BM25）。
func scoreText(queryTokens, textTokens []string) float64 {
	if len(queryTokens) == 0 || len(textTokens) == 0 {
		return 0
	}
	freq := map[string]int{}
	for _, t := range textTokens {
		freq[t]++
	}
	hits := 0.0
	seen := map[string]bool{}
	for _, q := range queryTokens {
		if seen[q] {
			continue
		}
		seen[q] = true
		if f, ok := freq[q]; ok {
			hits += 1.0 + 0.5*float64(f-1)
		}
	}
	return hits / float64(len(queryTokens))
}
