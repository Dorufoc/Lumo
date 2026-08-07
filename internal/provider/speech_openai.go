package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

func init() {
	Register("tts-openai", func(cfg map[string]any) (any, error) {
		baseURL, _ := cfg["base_url"].(string)
		apiKey, _ := cfg["api_key"].(string)
		model, _ := cfg["model"].(string)
		if model == "" {
			model = "tts-1"
		}
		return &OpenAITTS{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, Model: model}, nil
	})
	Register("asr-openai", func(cfg map[string]any) (any, error) {
		baseURL, _ := cfg["base_url"].(string)
		apiKey, _ := cfg["api_key"].(string)
		model, _ := cfg["model"].(string)
		if model == "" {
			model = "whisper-1"
		}
		return &OpenAIASR{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, Model: model}, nil
	})
}

// OpenAITTS 是 OpenAI 兼容 TTS 客户端（/audio/speech）。
type OpenAITTS struct {
	BaseURL string
	APIKey  string
	Model   string
}

func (t *OpenAITTS) Name() string { return "tts-openai" }

// Synthesize 调用 OpenAI 兼容音频合成接口，输出 mp3。
func (t *OpenAITTS) Synthesize(ctx context.Context, text string, speed float64) ([]byte, string, error) {
	if t.APIKey == "" {
		return nil, "", ErrNotConfigured
	}
	if speed < 0.25 || speed > 4.0 {
		speed = 1.0
	}
	body := map[string]any{
		"model":           t.Model,
		"input":           text,
		"voice":           "alloy",
		"speed":           speed,
		"response_format": "mp3",
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, "", err
	}
	httpCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(httpCtx, http.MethodPost, t.BaseURL+"/audio/speech", bytes.NewReader(payload))
	if err != nil {
		return nil, "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+t.APIKey)
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, "", fmt.Errorf("tts request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, "", fmt.Errorf("tts status %d: %s", resp.StatusCode, string(b))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return nil, "", fmt.Errorf("tts read: %w", err)
	}
	return data, "mp3", nil
}

// OpenAIASR 是 OpenAI 兼容 ASR 客户端（/audio/transcriptions）。
// 转写来自 Whisper；分维度分数由转写文本确定性推导（本地兜底能力）。
type OpenAIASR struct {
	BaseURL string
	APIKey  string
	Model   string
}

func (a *OpenAIASR) Name() string { return "asr-openai" }

// Transcribe 上传音频并返回转写与推导分数。
func (a *OpenAIASR) Transcribe(ctx context.Context, audioPath string) (*ASRResult, error) {
	if a.APIKey == "" {
		return nil, ErrNotConfigured
	}
	f, err := os.Open(audioPath)
	if err != nil {
		return nil, fmt.Errorf("open audio: %w", err)
	}
	defer f.Close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("model", a.Model); err != nil {
		return nil, err
	}
	fw, err := mw.CreateFormFile("file", "audio")
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(fw, f); err != nil {
		return nil, fmt.Errorf("copy audio: %w", err)
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	httpCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(httpCtx, http.MethodPost, a.BaseURL+"/audio/transcriptions", &buf)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", mw.FormDataContentType())
	httpReq.Header.Set("Authorization", "Bearer "+a.APIKey)
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("asr request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("asr status %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("asr decode: %w", err)
	}
	return &ASRResult{Transcript: out.Text, Scores: deriveHeuristicScores(out.Text)}, nil
}

// deriveHeuristicScores 依据转写文本确定性推导分维度分数（0–10）。
// 供不支持直接评分能力的 ASR 后端作为本地兜底。
func deriveHeuristicScores(transcript string) map[string]float64 {
	n := utf8.RuneCountInString(transcript)
	if n == 0 {
		return map[string]float64{
			ASRScorePronunciation: 0,
			ASRScoreFluency:       0,
			ASRScoreCompleteness:  0,
			ASRScoreGrammar:       0,
		}
	}
	// 完整度：与目标长度（约 40 字）的接近程度。
	words := len(strings.Fields(transcript))
	completeness := 10.0 - mathAbs(float64(n)-40)/40.0*2.0
	// 语法：是否有常见标点结构（句号、逗号）。
	grammar := 7.0
	if strings.ContainsAny(transcript, "。，,.!?！？") {
		grammar = 8.5
	}
	return map[string]float64{
		ASRScorePronunciation: round1(clamp(8.0-mathAbs(float64(n)-60)/60.0*1.5, 1.0, 10.0)),
		ASRScoreFluency:       round1(clamp(9.0-mathAbs(float64(words)-15)/15.0, 1.0, 10.0)),
		ASRScoreCompleteness:  round1(clamp(completeness, 1.0, 10.0)),
		ASRScoreGrammar:       round1(clamp(grammar, 1.0, 10.0)),
	}
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func mathAbs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}
