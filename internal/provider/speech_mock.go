package provider

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"math"
	"unicode/utf8"
)

func init() {
	Register("tts-mock", func(cfg map[string]any) (any, error) {
		return &MockTTS{}, nil
	})
	Register("asr-mock", func(cfg map[string]any) (any, error) {
		return &MockASR{}, nil
	})
}

// MockTTS 是本地确定性模拟 TTS：用标准库生成真实可播放的 16-bit PCM WAV。
// 无网络、无外部依赖；用于测试与演示降级路径。
type MockTTS struct{}

func (m *MockTTS) Name() string { return "tts-mock" }

// mockTTSSampleRate 是 mock TTS 输出采样率（16kHz，满足语音识别基本要求）。
const mockTTSSampleRate = 16000

// Synthesize 生成一段时长与文本长度相关、频率由文本哈希决定的纯音 WAV。
// speed 超出 0.5–2.0 范围时回退 1.0。
func (m *MockTTS) Synthesize(ctx context.Context, text string, speed float64) ([]byte, string, error) {
	n := utf8.RuneCountInString(text)
	if n == 0 {
		n = 1
	}
	if speed < 0.5 || speed > 2.0 {
		speed = 1.0
	}
	// 每字符约 120ms（1x），时长随速度缩放；最短 250ms。
	dur := float64(n) * 0.12 / speed
	if dur < 0.25 {
		dur = 0.25
	}
	total := int(dur * float64(mockTTSSampleRate))
	h := sha256.Sum256([]byte(text))
	freq := 400.0 + float64(h[0]%150) // 400–550Hz，同文本同频（确定性）
	fade := int(float64(mockTTSSampleRate) * 0.02)

	samples := make([]int16, total)
	for i := 0; i < total; i++ {
		phase := 2 * math.Pi * freq * float64(i) / mockTTSSampleRate
		env := 0.7
		if i < fade {
			env *= float64(i) / float64(fade)
		} else if i > total-fade {
			env *= float64(total-i) / float64(fade)
		}
		samples[i] = int16(math.Sin(phase) * env * 32767)
	}
	return wavPCM16(samples, mockTTSSampleRate), "wav", nil
}

// wavPCM16 编码 16-bit PCM 单声道 WAV（44 字节 RIFF 头 + 数据）。
func wavPCM16(samples []int16, sampleRate int) []byte {
	dataSize := len(samples) * 2
	buf := make([]byte, 44+dataSize)
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], uint32(36+dataSize))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16) // fmt chunk 长度
	binary.LittleEndian.PutUint16(buf[20:22], 1)  // PCM
	binary.LittleEndian.PutUint16(buf[22:24], 1)  // 单声道
	binary.LittleEndian.PutUint32(buf[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(buf[28:32], uint32(sampleRate*2)) // byte rate
	binary.LittleEndian.PutUint16(buf[32:34], 2)                    // block align
	binary.LittleEndian.PutUint16(buf[34:36], 16)                   // bits per sample
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], uint32(dataSize))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(buf[44+i*2:], uint16(s))
	}
	return buf
}

// WAVDurationMs 从 44 字节 WAV 头解析音频时长（毫秒）；非法头返回 0。
func WAVDurationMs(data []byte) int64 {
	if len(data) < 44 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return 0
	}
	byteRate := binary.LittleEndian.Uint32(data[28:32])
	dataSize := binary.LittleEndian.Uint32(data[40:44])
	if byteRate == 0 {
		return 0
	}
	return int64(dataSize) * 1000 / int64(byteRate)
}

// MockASR 是确定性模拟 ASR：返回固定转写与分维度分数，不解析真实音频。
type MockASR struct{}

func (m *MockASR) Name() string { return "asr-mock" }

// mockTranscript 是 mock ASR 固定返回的转写（可朗读的完整句子）。
const mockTranscript = "我在练习英语口语，这是一个模拟的语音识别结果。"

// Transcribe 返回固定转写与分维度分数。
func (m *MockASR) Transcribe(ctx context.Context, audioPath string) (*ASRResult, error) {
	return &ASRResult{
		Transcript: mockTranscript,
		Scores: map[string]float64{
			ASRScorePronunciation: 8.5,
			ASRScoreFluency:       8.0,
			ASRScoreCompleteness:  9.0,
			ASRScoreGrammar:       7.5,
		},
	}, nil
}
