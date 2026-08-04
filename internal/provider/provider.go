// Package provider 提供可替换的 LLM / Embedding 适配器。
// 业务层只依赖本包接口，不依赖具体供应商 SDK。
package provider

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Message 是对话消息。
type Message struct {
	Role    string `json:"role"` // system | user | assistant
	Content string `json:"content"`
}

// Usage 是 Token 用量。
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// ChatRequest 是对话请求。
type ChatRequest struct {
	Model       string
	Messages    []Message
	MaxTokens   int
	Temperature float64
	// JSONMode 要求模型输出纯 JSON（结构化输出）。
	JSONMode bool
}

// ChatResult 是对话结果。
type ChatResult struct {
	Content string
	Usage   Usage
	Model   string
}

// LLMProvider 是对话模型抽象。
type LLMProvider interface {
	Name() string
	// Chat 支持流式回调（onDelta 可为 nil）。
	Chat(ctx context.Context, req ChatRequest, onDelta func(string)) (*ChatResult, error)
}

// EmbeddingProvider 是向量化抽象。
type EmbeddingProvider interface {
	Name() string
	Dim() int
	Embed(ctx context.Context, texts []string) ([][]float64, error)
}

// ErrNotConfigured 表示 Provider 未配置。
var ErrNotConfigured = errors.New("provider not configured")

// Registry 按名称注册 Provider 工厂。
type Registry struct {
	mu      sync.RWMutex
	factories map[string]func(map[string]any) (any, error)
}

var global = &Registry{factories: map[string]func(map[string]any) (any, error){}}

// Register 注册工厂（openai / mock / embedding-openai / embedding-mock）。
func Register(name string, f func(map[string]any) (any, error)) {
	global.mu.Lock()
	defer global.mu.Unlock()
	global.factories[name] = f
}

// NewLLM 构造 LLM Provider。
func NewLLM(name string, cfg map[string]any) (LLMProvider, error) {
	global.mu.RLock()
	f, ok := global.factories[name]
	global.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown llm provider %q", name)
	}
	v, err := f(cfg)
	if err != nil {
		return nil, err
	}
	p, ok := v.(LLMProvider)
	if !ok {
		return nil, fmt.Errorf("provider %q is not an LLM provider", name)
	}
	return p, nil
}

// NewEmbedding 构造 Embedding Provider。
func NewEmbedding(name string, cfg map[string]any) (EmbeddingProvider, error) {
	global.mu.RLock()
	f, ok := global.factories[name]
	global.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown embedding provider %q", name)
	}
	v, err := f(cfg)
	if err != nil {
		return nil, err
	}
	p, ok := v.(EmbeddingProvider)
	if !ok {
		return nil, fmt.Errorf("provider %q is not an embedding provider", name)
	}
	return p, nil
}
