package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// capturedRequest 记录本地端点收到的请求，用于协议断言。
type capturedRequest struct {
	mu     sync.Mutex
	method string
	path   string
	body   map[string]json.RawMessage
}

func captureHandler(c *capturedRequest, respond func(w http.ResponseWriter)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.method = r.Method
		c.path = r.URL.Path
		if r.Body != nil {
			var m map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&m)
			c.body = m
		}
		c.mu.Unlock()
		respond(w)
	}
}

func fieldString(t *testing.T, c *capturedRequest, key string) string {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	raw, ok := c.body[key]
	if !ok {
		t.Fatalf("request body missing key %q (have %d keys)", key, len(c.body))
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("key %q not a string: %v", key, err)
	}
	return s
}

func fieldBool(t *testing.T, c *capturedRequest, key string) bool {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	raw, ok := c.body[key]
	if !ok {
		t.Fatalf("request body missing key %q", key)
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatalf("key %q not a bool: %v", key, err)
	}
	return b
}

// TestLocalLLMFactory 校验 local 注册与默认端点/模型。
func TestLocalLLMFactory(t *testing.T) {
	p, err := NewLLM("local", map[string]any{})
	if err != nil {
		t.Fatalf("new local llm: %v", err)
	}
	llm, ok := p.(*LocalLLM)
	if !ok {
		t.Fatalf("expected *LocalLLM, got %T", p)
	}
	if llm.Name() != "local" {
		t.Fatalf("unexpected name: %s", llm.Name())
	}
	if llm.BaseURL != "http://localhost:11434/v1" {
		t.Fatalf("unexpected default base_url: %s", llm.BaseURL)
	}
	if llm.Model == "" {
		t.Fatal("default model should not be empty")
	}
}

// TestLocalChatRequestProtocol 校验 chat/completions 请求协议：
// model/messages/stream 字段、JSONMode 透传、base_url 尾斜杠裁剪。
func TestLocalChatRequestProtocol(t *testing.T) {
	var c capturedRequest
	srv := httptest.NewServer(captureHandler(&c, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	p, err := NewLLM("local", map[string]any{"base_url": srv.URL + "/", "model": "qwen2.5"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Chat(context.Background(), ChatRequest{
		Model: "qwen2.5",
		Messages: []Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "hi"},
		},
		MaxTokens: 64, Temperature: 0.5,
	}, nil)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	c.mu.Lock()
	if c.method != http.MethodPost {
		t.Fatalf("expected POST, got %s", c.method)
	}
	if c.path != "/chat/completions" {
		t.Fatalf("trailing slash not trimmed / wrong path: %s", c.path)
	}
	c.mu.Unlock()
	if model := fieldString(t, &c, "model"); model != "qwen2.5" {
		t.Fatalf("model mismatch: %s", model)
	}
	if fieldBool(t, &c, "stream") {
		t.Fatal("stream should be false for non-stream chat")
	}
	var msgs []openAIChatMsg
	raw, _ := json.Marshal(c.body["messages"])
	if err := json.Unmarshal(raw, &msgs); err != nil {
		t.Fatalf("messages not array: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Role != "system" || msgs[1].Content != "hi" {
		t.Fatalf("messages mismatch: %+v", msgs)
	}
	if res.Content != "pong" {
		t.Fatalf("content mismatch: %q", res.Content)
	}
	if res.Usage.InputTokens != 3 || res.Usage.OutputTokens != 1 {
		t.Fatalf("usage mismatch: %+v", res.Usage)
	}
	if res.Model != "qwen2.5" {
		t.Fatalf("result model mismatch: %q", res.Model)
	}
}

// TestLocalChatJSONMode 校验 JSONMode 透传 response_format。
func TestLocalChatJSONMode(t *testing.T) {
	var c capturedRequest
	srv := httptest.NewServer(captureHandler(&c, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}]}`))
	}))
	defer srv.Close()

	p, _ := NewLLM("local", map[string]any{"base_url": srv.URL})
	_, err := p.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "x"}}, JSONMode: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var rf map[string]string
	raw, _ := json.Marshal(c.body["response_format"])
	if err := json.Unmarshal(raw, &rf); err != nil {
		t.Fatalf("response_format not an object: %v", err)
	}
	if rf["type"] != "json_object" {
		t.Fatalf("response_format.type mismatch: %+v", rf)
	}
}

// TestLocalChatStreamResponse 校验 SSE 流式响应解析（choices[0].delta.content + usage）。
func TestLocalChatStreamResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"你\"}}]}\n\n"))
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"好\"}}]}\n\n"))
		w.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2}}\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	p, _ := NewLLM("local", map[string]any{"base_url": srv.URL})
	var deltas []string
	res, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	}, func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatalf("stream chat: %v", err)
	}
	if joined := strings.Join(deltas, ""); joined != "你好" {
		t.Fatalf("stream deltas mismatch: %q", joined)
	}
	if res.Content != "你好" {
		t.Fatalf("aggregated content mismatch: %q", res.Content)
	}
	if res.Usage.InputTokens != 4 || res.Usage.OutputTokens != 2 {
		t.Fatalf("stream usage mismatch: %+v", res.Usage)
	}
}

// TestLocalChatErrorStatus 校验非 2xx 响应返回明确错误。
func TestLocalChatErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not loaded", http.StatusBadRequest)
	}))
	defer srv.Close()

	p, _ := NewLLM("local", map[string]any{"base_url": srv.URL})
	_, err := p.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "x"}}}, nil)
	if err == nil {
		t.Fatal("expected error for non-2xx")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Fatalf("error should mention status code: %v", err)
	}
}

// TestLocalChatConnectionError 校验连接失败返回明确错误。
func TestLocalChatConnectionError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // 端口立即不可达

	p, _ := NewLLM("local", map[string]any{"base_url": url})
	_, err := p.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "x"}}}, nil)
	if err == nil {
		t.Fatal("expected connection error")
	}
	if !strings.Contains(err.Error(), "llm request") {
		t.Fatalf("error should be wrapped with context: %v", err)
	}
}

// TestLocalEmbeddingRequestResponse 校验 embeddings 请求体与响应向量解析。
func TestLocalEmbeddingRequestResponse(t *testing.T) {
	var c capturedRequest
	srv := httptest.NewServer(captureHandler(&c, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3]},{"embedding":[0.4,0.5,0.6]}]}`))
	}))
	defer srv.Close()

	p, err := NewEmbedding("embedding-local", map[string]any{"base_url": srv.URL, "model": "nomic-embed-text"})
	if err != nil {
		t.Fatalf("new local embedding: %v", err)
	}
	ep := p.(*LocalEmbedding)
	if ep.Name() != "embedding-local" {
		t.Fatalf("unexpected name: %s", ep.Name())
	}
	vecs, err := ep.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if len(vecs) != 2 || len(vecs[0]) != 3 || len(vecs[1]) != 3 {
		t.Fatalf("vector dims mismatch: %+v", vecs)
	}
	if vecs[0][0] != 0.1 || vecs[1][2] != 0.6 {
		t.Fatalf("vector values mismatch: %+v", vecs)
	}
	c.mu.Lock()
	if c.path != "/embeddings" {
		t.Fatalf("path mismatch: %s", c.path)
	}
	c.mu.Unlock()
	if model := fieldString(t, &c, "model"); model != "nomic-embed-text" {
		t.Fatalf("model mismatch: %s", model)
	}
	var input []string
	raw, _ := json.Marshal(c.body["input"])
	if err := json.Unmarshal(raw, &input); err != nil {
		t.Fatalf("input not array: %v", err)
	}
	if len(input) != 2 || input[0] != "a" {
		t.Fatalf("input mismatch: %+v", input)
	}
}

// TestLocalEmbeddingError 校验 embeddings 非 2xx 返回明确错误。
func TestLocalEmbeddingError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "embedding model missing", http.StatusNotFound)
	}))
	defer srv.Close()

	p, _ := NewEmbedding("embedding-local", map[string]any{"base_url": srv.URL})
	_, err := p.Embed(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("expected error for non-2xx")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error should mention status code: %v", err)
	}
}

// TestLocalEmbeddingDefaultModel 校验本地 embedding 默认模型非空。
func TestLocalEmbeddingDefaultModel(t *testing.T) {
	p, err := NewEmbedding("embedding-local", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Dim() < 0 {
		t.Fatal("dim should not be negative")
	}
}

// TestFallbackLLM 校验主 Provider 失败时回退备用 Provider。
func TestFallbackLLM(t *testing.T) {
	ctx := context.Background()
	failing := &errLLM{err: "boom"}
	ok := &MockLLM{Model: "mock-chat"}

	// 主 Provider 失败 → 回退
	f := &FallbackLLM{Primary: failing, Fallback: ok}
	res, err := f.Chat(ctx, ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}}, nil)
	if err != nil {
		t.Fatalf("fallback should succeed: %v", err)
	}
	if !strings.Contains(res.Content, "模拟回复") {
		t.Fatalf("expected fallback mock content, got: %s", res.Content)
	}
	if f.Name() != "local" {
		t.Fatalf("fallback name should come from primary: %s", f.Name())
	}

	// 主 Provider 成功 → 不回退
	f2 := &FallbackLLM{Primary: ok, Fallback: &errLLM{err: "should not be used"}}
	res2, err := f2.Chat(ctx, ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}}, nil)
	if err != nil {
		t.Fatalf("primary success should not fallback: %v", err)
	}
	if !strings.Contains(res2.Content, "模拟回复") {
		t.Fatalf("unexpected content: %s", res2.Content)
	}

	// 无备用 Provider → 透传错误
	f3 := &FallbackLLM{Primary: failing}
	if _, err := f3.Chat(ctx, ChatRequest{}, nil); err == nil {
		t.Fatal("expected error when no fallback")
	}
}

// TestFallbackEmbedding 校验 embedding 主 Provider 失败时回退。
func TestFallbackEmbedding(t *testing.T) {
	ctx := context.Background()
	ok := &MockEmbedding{}
	f := &FallbackEmbedding{Primary: &errEmbedding{}, Fallback: ok}
	vecs, err := f.Embed(ctx, []string{"hi"})
	if err != nil {
		t.Fatalf("fallback embed should succeed: %v", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != 64 {
		t.Fatalf("unexpected fallback vector: %+v", vecs)
	}
	if f.Dim() != ok.Dim() {
		t.Fatalf("dim should come from fallback when primary unknown: %d", f.Dim())
	}
}

// errLLM 始终失败的 LLM。
type errLLM struct{ err string }

func (e *errLLM) Name() string { return "local" }
func (e *errLLM) Chat(ctx context.Context, req ChatRequest, onDelta func(string)) (*ChatResult, error) {
	return nil, &errLLMError{msg: e.err}
}

type errLLMError struct{ msg string }

func (e *errLLMError) Error() string { return e.msg }

// errEmbedding 始终失败的 Embedding。
type errEmbedding struct{}

func (e *errEmbedding) Name() string { return "embedding-local" }
func (e *errEmbedding) Dim() int     { return 0 }
func (e *errEmbedding) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	return nil, &errLLMError{msg: "embed boom"}
}
