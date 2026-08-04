package provider

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"strings"
)

func init() {
	Register("mock", func(cfg map[string]any) (any, error) {
		model, _ := cfg["model"].(string)
		if model == "" {
			model = "mock-chat"
		}
		return &MockLLM{Model: model}, nil
	})
	Register("embedding-mock", func(cfg map[string]any) (any, error) {
		return &MockEmbedding{}, nil
	})
}

// MockLLM 是本地确定性模拟 LLM（无网络；用于测试与演示降级路径）。
type MockLLM struct {
	Model string
}

func (m *MockLLM) Name() string { return "mock" }

// Chat 基于简单模板返回内容；JSONMode 时返回结构化 JSON。
func (m *MockLLM) Chat(ctx context.Context, req ChatRequest, onDelta func(string)) (*ChatResult, error) {
	var b strings.Builder
	last := ""
	if len(req.Messages) > 0 {
		last = req.Messages[len(req.Messages)-1].Content
	}
	if req.JSONMode {
		b.WriteString(`{"ok":true,"summary":"` + mockEscape(last) + `"}`)
	} else {
		b.WriteString("（本地模拟回复）已收到你的问题：")
		b.WriteString(mockEscape(truncate(last, 80)))
	}
	content := b.String()
	if onDelta != nil {
		// 模拟流式输出
		for _, r := range content {
			onDelta(string(r))
		}
	}
	return &ChatResult{
		Content: content,
		Usage:   Usage{InputTokens: len(last) / 2, OutputTokens: len(content) / 2},
		Model:   m.Model,
	}, nil
}

func mockEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// MockEmbedding 是确定性哈希向量（维度 64），用于本地检索测试。
type MockEmbedding struct{}

func (m *MockEmbedding) Name() string { return "embedding-mock" }
func (m *MockEmbedding) Dim() int     { return 64 }

// Embed 对每个文本生成确定性向量（字符 n-gram 哈希）。
func (m *MockEmbedding) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	out := make([][]float64, 0, len(texts))
	for _, t := range texts {
		vec := make([]float64, 64)
		norm := 0.0
		for i := 0; i+2 <= len(t); i++ {
			h := sha256.Sum256([]byte(t[i : i+2]))
			idx := binary.LittleEndian.Uint64(h[:8]) % 64
			vec[idx] += 1
		}
		for _, v := range vec {
			norm += v * v
		}
		if norm > 0 {
			s := 1.0 / sqrt(norm)
			for i := range vec {
				vec[i] *= s
			}
		}
		out = append(out, vec)
	}
	return out, nil
}

func sqrt(x float64) float64 {
	// 牛顿迭代
	if x <= 0 {
		return 0
	}
	z := x
	for i := 0; i < 20; i++ {
		z = (z + x/z) / 2
	}
	return z
}
