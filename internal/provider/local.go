package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// 本地模型端点（Ollama / LM Studio / llama.cpp 兼容 OpenAI 协议，免密钥）。
const (
	defaultLocalBaseURL = "http://localhost:11434/v1"
	defaultLocalModel   = "llama3.2"
	defaultLocalEmbed   = "nomic-embed-text"
)

func init() {
	Register("local", func(cfg map[string]any) (any, error) {
		base, _ := cfg["base_url"].(string)
		model, _ := cfg["model"].(string)
		if base == "" {
			base = defaultLocalBaseURL
		}
		if model == "" {
			model = defaultLocalModel
		}
		return &LocalLLM{BaseURL: strings.TrimSuffix(base, "/"), Model: model}, nil
	})
	Register("embedding-local", func(cfg map[string]any) (any, error) {
		base, _ := cfg["base_url"].(string)
		model, _ := cfg["model"].(string)
		if base == "" {
			base = defaultLocalBaseURL
		}
		if model == "" {
			model = defaultLocalEmbed
		}
		return &LocalEmbedding{BaseURL: strings.TrimSuffix(base, "/"), Model: model}, nil
	})
}

// LocalLLM 是本地模型 Chat Completions 客户端（OpenAI 兼容协议，Ollama 等）。
// 与 OpenAI 客户端共享同一请求/响应结构，仅端点本地化且免密钥。
type LocalLLM struct {
	BaseURL string
	Model   string
}

func (l *LocalLLM) Name() string { return "local" }

// Chat 调用 {BaseURL}/chat/completions（onDelta 非 nil 时 SSE 流式）。
func (l *LocalLLM) Chat(ctx context.Context, req ChatRequest, onDelta func(string)) (*ChatResult, error) {
	model := req.Model
	if model == "" {
		model = l.Model
	}
	msgs := make([]openAIChatMsg, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, openAIChatMsg{Role: m.Role, Content: m.Content})
	}
	body := openAIChatBody{
		Model: model, Messages: msgs, Stream: onDelta != nil,
		MaxTokens: req.MaxTokens, Temperature: req.Temperature,
	}
	if req.JSONMode {
		body.ResponseFormat = &map[string]string{"type": "json_object"}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(httpCtx, http.MethodPost, l.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("llm request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("llm status %d: %s", resp.StatusCode, string(b))
	}

	result := &ChatResult{Model: model}
	if onDelta == nil {
		// 非流式：choices[0].message.content + usage。
		var full struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&full); err != nil {
			return nil, fmt.Errorf("llm decode: %w", err)
		}
		if len(full.Choices) > 0 {
			result.Content = full.Choices[0].Message.Content
		}
		if full.Usage != nil {
			result.Usage = Usage{InputTokens: full.Usage.PromptTokens, OutputTokens: full.Usage.CompletionTokens}
		}
		return result, nil
	}

	// SSE 流式解析。
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk openAIChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) > 0 {
			delta := chunk.Choices[0].Delta.Content
			if delta != "" {
				result.Content += delta
				onDelta(delta)
			}
		}
		if chunk.Usage != nil {
			result.Usage = Usage{InputTokens: chunk.Usage.PromptTokens, OutputTokens: chunk.Usage.CompletionTokens}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("llm stream: %w", err)
	}
	return result, nil
}

// LocalEmbedding 是本地模型 Embeddings 客户端（OpenAI 兼容协议，免密钥）。
type LocalEmbedding struct {
	BaseURL string
	Model   string
}

func (e *LocalEmbedding) Name() string { return "embedding-local" }

// Dim 返回 0：本地向量维度取决于所加载模型，构造时无法确定。
func (e *LocalEmbedding) Dim() int { return 0 }

// Embed 批量向量化（{BaseURL}/embeddings）。
func (e *LocalEmbedding) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body := map[string]any{"model": e.Model, "input": texts}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(httpCtx, http.MethodPost, e.BaseURL+"/embeddings", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("embedding request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("embedding status %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	result := make([][]float64, 0, len(out.Data))
	for _, d := range out.Data {
		result = append(result, d.Embedding)
	}
	return result, nil
}

// FallbackLLM 包装主 LLM Provider：Chat 失败时自动回退到备用 Provider。
// 用于 app 层装配"主模型不可用 → 回退本地或 mock"兜底链；Name 取自主 Provider。
type FallbackLLM struct {
	Primary  LLMProvider
	Fallback LLMProvider
}

func (f *FallbackLLM) Name() string {
	if f.Primary == nil {
		return "fallback"
	}
	return f.Primary.Name()
}

func (f *FallbackLLM) Chat(ctx context.Context, req ChatRequest, onDelta func(string)) (*ChatResult, error) {
	res, err := f.Primary.Chat(ctx, req, onDelta)
	if err == nil {
		return res, nil
	}
	if f.Fallback == nil {
		return nil, err
	}
	return f.Fallback.Chat(ctx, req, onDelta)
}

// FallbackEmbedding 包装主 Embedding Provider：Embed 失败时回退到备用 Provider。
type FallbackEmbedding struct {
	Primary  EmbeddingProvider
	Fallback EmbeddingProvider
}

func (f *FallbackEmbedding) Name() string {
	if f.Primary == nil {
		return "embedding-fallback"
	}
	return f.Primary.Name()
}

func (f *FallbackEmbedding) Dim() int {
	if f.Primary != nil && f.Primary.Dim() > 0 {
		return f.Primary.Dim()
	}
	if f.Fallback != nil {
		return f.Fallback.Dim()
	}
	return 0
}

func (f *FallbackEmbedding) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	vecs, err := f.Primary.Embed(ctx, texts)
	if err == nil {
		return vecs, nil
	}
	if f.Fallback == nil {
		return nil, err
	}
	return f.Fallback.Embed(ctx, texts)
}
