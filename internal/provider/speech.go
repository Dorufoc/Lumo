package provider

import (
	"context"
	"fmt"
)

// TTSProvider 是语音合成抽象（完整设计文档 4.18 / 10.11）。
// Synthesize 返回音频字节与格式（wav/mp3/m4a）；speed 为 0.5–2.0 倍语速。
type TTSProvider interface {
	Name() string
	Synthesize(ctx context.Context, text string, speed float64) (audio []byte, format string, err error)
}

// ASRScoreKey 是口语评分维度键。
const (
	ASRScorePronunciation = "pronunciation"
	ASRScoreFluency       = "fluency"
	ASRScoreCompleteness  = "completeness"
	ASRScoreGrammar       = "grammar"
)

// ASRResult 是语音识别结果：转写 + 分维度评分（分数 0–10）。
type ASRResult struct {
	Transcript string             `json:"transcript"`
	Scores     map[string]float64 `json:"scores"`
}

// ASRProvider 是语音识别抽象：读取音频文件并返回转写与分维度评分。
type ASRProvider interface {
	Name() string
	Transcribe(ctx context.Context, audioPath string) (*ASRResult, error)
}

// NewTTS 构造 TTS Provider（注册名 tts-openai / tts-mock）。
func NewTTS(name string, cfg map[string]any) (TTSProvider, error) {
	global.mu.RLock()
	f, ok := global.factories[name]
	global.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown tts provider %q", name)
	}
	v, err := f(cfg)
	if err != nil {
		return nil, err
	}
	p, ok := v.(TTSProvider)
	if !ok {
		return nil, fmt.Errorf("provider %q is not a TTS provider", name)
	}
	return p, nil
}

// NewASR 构造 ASR Provider（注册名 asr-openai / asr-mock）。
func NewASR(name string, cfg map[string]any) (ASRProvider, error) {
	global.mu.RLock()
	f, ok := global.factories[name]
	global.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown asr provider %q", name)
	}
	v, err := f(cfg)
	if err != nil {
		return nil, err
	}
	p, ok := v.(ASRProvider)
	if !ok {
		return nil, fmt.Errorf("provider %q is not an ASR provider", name)
	}
	return p, nil
}
