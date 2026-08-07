package app

import (
	"context"
	"testing"

	"lumo/internal/provider"
)

// TestBuildLLMFallbackLocal 校验 kind=local 时主 Provider 为 LocalLLM 且装配 mock 兜底。
func TestBuildLLMFallbackLocal(t *testing.T) {
	p, err := buildLLMFallback("local", map[string]any{"base_url": "http://127.0.0.1:11434/v1"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	fb, ok := p.(*provider.FallbackLLM)
	if !ok {
		t.Fatalf("expected *provider.FallbackLLM, got %T", p)
	}
	if _, ok := fb.Primary.(*provider.LocalLLM); !ok {
		t.Fatalf("expected LocalLLM primary, got %T", fb.Primary)
	}
	if _, ok := fb.Fallback.(*provider.MockLLM); !ok {
		t.Fatalf("expected MockLLM fallback, got %T", fb.Fallback)
	}
	if fb.Name() != "local" {
		t.Fatalf("unexpected name: %s", fb.Name())
	}
}

// TestBuildLLMFallbackUnreachablePrimaryFallsToMock 校验主端点不可达时 Chat 回退 mock 成功。
func TestBuildLLMFallbackUnreachablePrimaryFallsToMock(t *testing.T) {
	// 端口不可达（未监听）→ 主 Provider 失败 → 回退 mock。
	p, err := buildLLMFallback("local", map[string]any{"base_url": "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	res, err := p.Chat(context.Background(), provider.ChatRequest{
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
	}, nil)
	if err != nil {
		t.Fatalf("chat should succeed via mock fallback: %v", err)
	}
	if res.Content == "" {
		t.Fatal("expected non-empty content from mock fallback")
	}
}

// TestBuildLLMFallbackMockDirect 校验 kind=mock 时不额外包装。
func TestBuildLLMFallbackMockDirect(t *testing.T) {
	p, err := buildLLMFallback("mock", nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if _, ok := p.(*provider.MockLLM); !ok {
		t.Fatalf("expected direct MockLLM, got %T", p)
	}
}

// TestBuildLLMFallbackOpenaiFallback 校验 kind=openai 时主 Provider 为 OpenAI 且装配 mock 兜底。
func TestBuildLLMFallbackOpenaiFallback(t *testing.T) {
	p, err := buildLLMFallback("openai", map[string]any{"base_url": "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	fb, ok := p.(*provider.FallbackLLM)
	if !ok {
		t.Fatalf("expected *provider.FallbackLLM, got %T", p)
	}
	if _, ok := fb.Primary.(*provider.OpenAI); !ok {
		t.Fatalf("expected OpenAI primary, got %T", fb.Primary)
	}
	// 主端点不可达 → Chat 回退 mock。
	res, err := p.Chat(context.Background(), provider.ChatRequest{
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
	}, nil)
	if err != nil {
		t.Fatalf("chat should succeed via mock fallback: %v", err)
	}
	if res.Content == "" {
		t.Fatal("expected non-empty content")
	}
}

// TestBuildEmbeddingFallbackLocal 校验 embedding-local 装配 mock 兜底。
func TestBuildEmbeddingFallbackLocal(t *testing.T) {
	p, err := buildEmbeddingFallback("local", map[string]any{"base_url": "http://127.0.0.1:11434/v1"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	fb, ok := p.(*provider.FallbackEmbedding)
	if !ok {
		t.Fatalf("expected *provider.FallbackEmbedding, got %T", p)
	}
	if _, ok := fb.Primary.(*provider.LocalEmbedding); !ok {
		t.Fatalf("expected LocalEmbedding primary, got %T", fb.Primary)
	}
	// 主端点不可达 → Embed 回退 mock（64 维确定性向量）。
	vecs, err := p.Embed(context.Background(), []string{"hi"})
	if err != nil {
		t.Fatalf("embed should succeed via mock fallback: %v", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != 64 {
		t.Fatalf("unexpected mock fallback vector: %+v", vecs)
	}
}

// TestBuildEmbeddingFallbackMockDirect 校验 kind=mock 时不额外包装。
func TestBuildEmbeddingFallbackMockDirect(t *testing.T) {
	p, err := buildEmbeddingFallback("mock", nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if _, ok := p.(*provider.MockEmbedding); !ok {
		t.Fatalf("expected direct MockEmbedding, got %T", p)
	}
}
