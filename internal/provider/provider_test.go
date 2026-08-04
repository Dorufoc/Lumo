package provider

import (
	"context"
	"strings"
	"testing"
)

func TestMockLLM(t *testing.T) {
	p, err := NewLLM("mock", map[string]any{})
	if err != nil {
		t.Fatalf("new mock llm: %v", err)
	}
	var deltas []string
	res, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "你好"}},
	}, func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "模拟回复") {
		t.Fatalf("unexpected mock content: %s", res.Content)
	}
	joined := strings.Join(deltas, "")
	if joined != res.Content {
		t.Fatalf("stream deltas mismatch: %q != %q", joined, res.Content)
	}

	// JSONMode
	res2, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "hi"}}, JSONMode: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res2.Content, `{"ok":true`) {
		t.Fatalf("json mode output invalid: %s", res2.Content)
	}
}

func TestMockEmbedding(t *testing.T) {
	p, err := NewEmbedding("embedding-mock", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	vecs, err := p.Embed(context.Background(), []string{"hello world", "hello world"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 || len(vecs[0]) != 64 {
		t.Fatalf("unexpected dims: %d x %d", len(vecs), len(vecs[0]))
	}
	// 相同文本 → 相同向量
	for i := range vecs[0] {
		if vecs[0][i] != vecs[1][i] {
			t.Fatal("deterministic embedding violated")
		}
	}
	// 不同文本 → 不同向量
	vecs2, _ := p.Embed(context.Background(), []string{"totally different"})
	different := false
	for i := range vecs[0] {
		if vecs[0][i] != vecs2[0][i] {
			different = true
			break
		}
	}
	if !different {
		t.Fatal("different texts should produce different vectors")
	}
}

func TestUnknownProvider(t *testing.T) {
	if _, err := NewLLM("nonexistent", nil); err == nil {
		t.Fatal("expected error for unknown provider")
	}
}
