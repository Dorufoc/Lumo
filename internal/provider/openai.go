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

func init() {
	Register("openai", func(cfg map[string]any) (any, error) {
		base, _ := cfg["base_url"].(string)
		key, _ := cfg["api_key"].(string)
		model, _ := cfg["model"].(string)
		if base == "" {
			base = "https://api.openai.com/v1"
		}
		if model == "" {
			model = "gpt-4o-mini"
		}
		return &OpenAI{BaseURL: strings.TrimSuffix(base, "/"), APIKey: key, Model: model}, nil
	})
	Register("embedding-openai", func(cfg map[string]any) (any, error) {
		base, _ := cfg["base_url"].(string)
		key, _ := cfg["api_key"].(string)
		model, _ := cfg["model"].(string)
		if base == "" {
			base = "https://api.openai.com/v1"
		}
		if model == "" {
			model = "text-embedding-3-small"
		}
		return &OpenAIEmbedding{BaseURL: strings.TrimSuffix(base, "/"), APIKey: key, Model: model}, nil
	})
}

// OpenAI 是 OpenAI 兼容 Chat Completions 客户端。
type OpenAI struct {
	BaseURL string
	APIKey  string
	Model   string
}

type openAIChatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatBody struct {
	Model       string          `json:"model"`
	Messages    []openAIChatMsg `json:"messages"`
	Stream      bool            `json:"stream"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature float64         `json:"temperature,omitempty"`
	// ResponseFormat 请求 JSON 输出（部分服务商支持）。
	ResponseFormat *map[string]string `json:"response_format,omitempty"`
}

type openAIChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (o *OpenAI) Name() string { return "openai" }

// Chat 调用 chat/completions（SSE 流式）。
func (o *OpenAI) Chat(ctx context.Context, req ChatRequest, onDelta func(string)) (*ChatResult, error) {
	model := req.Model
	if model == "" {
		model = o.Model
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
	httpReq, err := http.NewRequestWithContext(httpCtx, http.MethodPost, o.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.APIKey)
	}

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

	// SSE 流式解析
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

// OpenAIEmbedding 是 OpenAI 兼容 Embeddings 客户端。
type OpenAIEmbedding struct {
	BaseURL string
	APIKey  string
	Model   string
}

func (e *OpenAIEmbedding) Name() string { return "embedding-openai" }
func (e *OpenAIEmbedding) Dim() int     { return 1536 }

// Embed 批量向量化。
func (e *OpenAIEmbedding) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body := map[string]any{"model": e.Model, "input": texts}
	payload, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, e.BaseURL+"/embeddings", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if e.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+e.APIKey)
	}
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
