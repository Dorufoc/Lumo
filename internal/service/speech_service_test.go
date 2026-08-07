package service

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lumo/internal/agent"
	"lumo/internal/domain"
	"lumo/internal/provider"
)

// TestMockTTSWAVHeader 校验 MockTTS 输出真实可播放的 16-bit PCM WAV。
func TestMockTTSWAVHeader(t *testing.T) {
	m := &provider.MockTTS{}
	audio, format, err := m.Synthesize(context.Background(), "你好世界", 1.0)
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if format != "wav" {
		t.Fatalf("expected wav, got %s", format)
	}
	if len(audio) < 44 {
		t.Fatalf("wav too short: %d", len(audio))
	}
	// RIFF / WAVE / fmt / data 四段
	if string(audio[0:4]) != "RIFF" || string(audio[8:12]) != "WAVE" {
		t.Fatal("missing RIFF/WAVE signature")
	}
	if string(audio[12:16]) != "fmt " || string(audio[36:40]) != "data" {
		t.Fatal("missing fmt/data chunks")
	}
	if pcm := binary.LittleEndian.Uint16(audio[20:22]); pcm != 1 {
		t.Fatalf("expected PCM=1, got %d", pcm)
	}
	if ch := binary.LittleEndian.Uint16(audio[22:24]); ch != 1 {
		t.Fatalf("expected mono, got %d", ch)
	}
	if sr := binary.LittleEndian.Uint32(audio[24:28]); sr != 16000 {
		t.Fatalf("expected 16kHz, got %d", sr)
	}
	if d := provider.WAVDurationMs(audio); d <= 0 {
		t.Fatalf("expected positive duration, got %d", d)
	}
	// 同文本同参数 → 确定性输出（同哈希同频率）
	audio2, _, _ := m.Synthesize(context.Background(), "你好世界", 1.0)
	if string(audio) != string(audio2) {
		t.Fatal("mock TTS not deterministic for same text")
	}
	// 非法头返回 0
	if d := provider.WAVDurationMs([]byte("not a wav")); d != 0 {
		t.Fatalf("expected 0 for invalid header, got %d", d)
	}
}

// TestMockASRResult 校验 MockASR 返回固定转写与 0–10 分维度评分。
func TestMockASRResult(t *testing.T) {
	m := &provider.MockASR{}
	res, err := m.Transcribe(context.Background(), "whatever.wav")
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if res.Transcript == "" {
		t.Fatal("expected non-empty transcript")
	}
	want := []string{provider.ASRScorePronunciation, provider.ASRScoreFluency,
		provider.ASRScoreCompleteness, provider.ASRScoreGrammar}
	if len(res.Scores) != len(want) {
		t.Fatalf("expected %d scores, got %d", len(want), len(res.Scores))
	}
	for _, k := range want {
		v, ok := res.Scores[k]
		if !ok {
			t.Fatalf("missing score key %s", k)
		}
		if v < 0 || v > 10 {
			t.Fatalf("score %s out of range: %v", k, v)
		}
	}
}

// TestSpeechProviderConfig 校验 tts/asr mock 无需 api_key 即视为已配置。
func TestSpeechProviderConfig(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	ctx := context.Background()

	// 未配置时不可用
	if _, ok := s.speechProviderConfig("tts"); ok {
		t.Fatal("tts should be unconfigured initially")
	}
	if _, ok := s.speechProviderConfig("asr"); ok {
		t.Fatal("asr should be unconfigured initially")
	}
	// 配置 mock tts/asr（无需密钥）
	status, err := s.ProviderConfigure(ctx, ProviderConfigureReq{
		WorkspaceID: ws.ID, Provider: "tts", Kind: "mock", Enabled: true,
	})
	if err != nil {
		t.Fatalf("configure tts mock: %v", err)
	}
	if st, ok := status["tts"]; !ok || !st.Configured {
		t.Fatalf("tts should be configured after mock configure: %+v", status)
	}
	cfg, ok := s.speechProviderConfig("tts")
	if !ok {
		t.Fatal("speechProviderConfig should see tts mock as configured")
	}
	if cfg["kind"] != "mock" {
		t.Fatalf("expected kind mock, got %v", cfg["kind"])
	}
}

// TestTTSPlayFeatureDisabled 校验未配置 TTS Provider 时返回 FEATURE_DISABLED。
func TestTTSPlayFeatureDisabled(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	q := publishedQuestion(t, s, ws.ID, scPayload("朗读测试", "B"))

	_, err := s.Speech.TTSPlay(ctx(), TTSPlayReq{
		WorkspaceID: ws.ID, RefType: "question", RefID: q.ID, Speed: 1.0,
	})
	e := domain.AsError(err)
	if e == nil || e.Code != domain.CodeFeatureDisabled {
		t.Fatalf("expected FEATURE_DISABLED, got %v", err)
	}
}

// TestTTSPlayQuestion 校验对已发布题目 TTS 朗读：落盘 uploads 并返回时长。
func TestTTSPlayQuestion(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	ctx := context.Background()
	q := publishedQuestion(t, s, ws.ID, scPayload("朗读题干：hello", "B"))
	if _, err := s.ProviderConfigure(ctx, ProviderConfigureReq{
		WorkspaceID: ws.ID, Provider: "tts", Kind: "mock", Enabled: true,
	}); err != nil {
		t.Fatalf("configure tts: %v", err)
	}

	res, err := s.Speech.TTSPlay(ctx, TTSPlayReq{
		WorkspaceID: ws.ID, RefType: "question", RefID: q.ID, Speed: 1.0,
	})
	if err != nil {
		t.Fatalf("tts play: %v", err)
	}
	if res.Format != "wav" {
		t.Fatalf("expected wav, got %s", res.Format)
	}
	if res.DurationMs <= 0 {
		t.Fatalf("expected positive duration, got %d", res.DurationMs)
	}
	full := filepath.Join(s.Cfg.DataDir, res.AudioPath)
	if _, err := os.Stat(full); err != nil {
		t.Fatalf("audio file not saved: %v", err)
	}
	// 非法 speed 拒绝
	if _, err := s.Speech.TTSPlay(ctx, TTSPlayReq{
		WorkspaceID: ws.ID, RefType: "question", RefID: q.ID, Speed: 3.0,
	}); domain.AsError(err) == nil || domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("expected INVALID_ARG for speed 3.0, got %v", err)
	}
	// 非法 ref_type 拒绝
	if _, err := s.Speech.TTSPlay(ctx, TTSPlayReq{
		WorkspaceID: ws.ID, RefType: "bogus", RefID: q.ID, Speed: 1.0,
	}); domain.AsError(err) == nil || domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("expected INVALID_ARG for bogus ref_type, got %v", err)
	}
}

// TestTTSPlayNotFound 校验引用不存在的对象返回 NOT_FOUND。
func TestTTSPlayNotFound(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	if _, err := s.ProviderConfigure(ctx(), ProviderConfigureReq{
		WorkspaceID: ws.ID, Provider: "tts", Kind: "mock", Enabled: true,
	}); err != nil {
		t.Fatalf("configure tts: %v", err)
	}
	if _, err := s.Speech.TTSPlay(ctx(), TTSPlayReq{
		WorkspaceID: ws.ID, RefType: "question", RefID: "missing", Speed: 1.0,
	}); domain.AsError(err) == nil || domain.AsError(err).Code != domain.CodeNotFound {
		t.Fatalf("expected NOT_FOUND, got %v", err)
	}
}

// setupSpeaking 准备练习会话 + 保存答案（产生 submission）+ 上传录音文件。
func setupSpeaking(t *testing.T, s *Services, wsID, userID string) (sessionID, submissionID, audioPath string) {
	t.Helper()
	q := publishedQuestion(t, s, wsID, scPayload("口语题", "A"))
	session, err := s.Practice.PracticeStart(ctx(), PracticeStartReq{
		WorkspaceID: wsID, UserID: userID, Mode: "practice",
		QuestionIDs: []string{q.ID}, IdempotencyKey: "sp-" + NewID(),
	})
	if err != nil {
		t.Fatalf("practice start: %v", err)
	}
	draft, err := s.Practice.PracticeSaveAnswer(ctx(), PracticeSaveAnswerReq{
		WorkspaceID: wsID, SessionID: session.ID,
		QuestionVersionID: session.Questions[0].QuestionVersionID,
		Answer:            json.RawMessage(`"A"`), ClientSequence: 1,
	})
	if err != nil {
		t.Fatalf("save answer: %v", err)
	}
	// 生成一段合法 WAV 录音并上传
	m := &provider.MockTTS{}
	audio, _, err := m.Synthesize(ctx(), "录音内容", 1.0)
	if err != nil {
		t.Fatalf("synth probe: %v", err)
	}
	up, err := s.Speech.SaveAudio("recording.wav", audio)
	if err != nil {
		t.Fatalf("save audio: %v", err)
	}
	return session.ID, draft.ID, up.Path
}

// TestSpeakingSubmitFlow 校验口语提交全流程：ASR 评分 → 落库 → 事件广播 → 幂等。
func TestSpeakingSubmitFlow(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	ctx := context.Background()
	if _, err := s.ProviderConfigure(ctx, ProviderConfigureReq{
		WorkspaceID: ws.ID, Provider: "asr", Kind: "mock", Enabled: true,
	}); err != nil {
		t.Fatalf("configure asr: %v", err)
	}
	_, submissionID, audioPath := setupSpeaking(t, s, ws.ID, userID)

	ch, cancel := s.UserEvents.SubscribeUser(userID)
	defer cancel()

	key := "spk-" + NewID()
	res, err := s.Speech.SpeakingSubmit(ctx, SpeakingSubmitReq{
		WorkspaceID: ws.ID, SubmissionID: submissionID,
		AudioPath: audioPath, IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("speaking submit: %v", err)
	}
	if res.Status != "graded" {
		t.Fatalf("expected graded, got %s", res.Status)
	}
	if res.Transcript == "" {
		t.Fatal("expected transcript")
	}
	if len(res.Scores) != 4 {
		t.Fatalf("expected 4 scores, got %d", len(res.Scores))
	}
	if res.ID != stableID("speaking", submissionID) {
		t.Fatalf("expected deterministic id, got %s", res.ID)
	}
	// 事件广播：grading:updated
	select {
	case ev := <-ch:
		if ev.Name != agent.EventGradingUpdated {
			t.Fatalf("expected %s event, got %s", agent.EventGradingUpdated, ev.Name)
		}
		if ev.Payload["grading_id"] != res.ID {
			t.Fatalf("event grading_id mismatch: %v", ev.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no grading:updated event received")
	}

	// 幂等：同一幂等键重复提交返回相同结果
	res2, err := s.Speech.SpeakingSubmit(ctx, SpeakingSubmitReq{
		WorkspaceID: ws.ID, SubmissionID: submissionID,
		AudioPath: audioPath, IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("second submit: %v", err)
	}
	if res2.ID != res.ID || res2.Transcript != res.Transcript {
		t.Fatal("speaking submit not idempotent")
	}
}

// TestSpeakingSubmitValidation 校验缺失参数返回 INVALID_ARG。
func TestSpeakingSubmitValidation(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	cases := []SpeakingSubmitReq{
		{WorkspaceID: ws.ID, AudioPath: "x.wav", IdempotencyKey: "spk-" + NewID()},
		{WorkspaceID: ws.ID, SubmissionID: "s", IdempotencyKey: "spk-" + NewID()},
		{WorkspaceID: ws.ID, SubmissionID: "s", AudioPath: "x.wav"},
	}
	for i, req := range cases {
		if _, err := s.Speech.SpeakingSubmit(ctx(), req); domain.AsError(err) == nil ||
			domain.AsError(err).Code != domain.CodeInvalidArgument {
			t.Fatalf("case %d: expected INVALID_ARG, got %v", i, err)
		}
	}
}

// TestSpeakingSubmitAudioMissing 校验音频文件缺失时拒绝。
func TestSpeakingSubmitAudioMissing(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	if _, err := s.ProviderConfigure(ctx(), ProviderConfigureReq{
		WorkspaceID: ws.ID, Provider: "asr", Kind: "mock", Enabled: true,
	}); err != nil {
		t.Fatalf("configure asr: %v", err)
	}
	q := publishedQuestion(t, s, ws.ID, scPayload("口语题", "A"))
	session, err := s.Practice.PracticeStart(ctx(), PracticeStartReq{
		WorkspaceID: ws.ID, UserID: userID, Mode: "practice",
		QuestionIDs: []string{q.ID}, IdempotencyKey: "sp-" + NewID(),
	})
	if err != nil {
		t.Fatalf("practice start: %v", err)
	}
	draft, err := s.Practice.PracticeSaveAnswer(ctx(), PracticeSaveAnswerReq{
		WorkspaceID: ws.ID, SessionID: session.ID,
		QuestionVersionID: session.Questions[0].QuestionVersionID,
		Answer:            json.RawMessage(`"A"`), ClientSequence: 1,
	})
	if err != nil {
		t.Fatalf("save answer: %v", err)
	}
	_, err = s.Speech.SpeakingSubmit(ctx(), SpeakingSubmitReq{
		WorkspaceID: ws.ID, SubmissionID: draft.ID,
		AudioPath: "uploads/not-exist.wav", IdempotencyKey: "spk-" + NewID(),
	})
	if domain.AsError(err) == nil || domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("expected INVALID_ARG for missing audio, got %v", err)
	}
}

// TestSpeakingResultGet 校验按提交查询口语结果。
func TestSpeakingResultGet(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	ctx := context.Background()
	if _, err := s.ProviderConfigure(ctx, ProviderConfigureReq{
		WorkspaceID: ws.ID, Provider: "asr", Kind: "mock", Enabled: true,
	}); err != nil {
		t.Fatalf("configure asr: %v", err)
	}
	_, submissionID, audioPath := setupSpeaking(t, s, ws.ID, userID)

	// 未提交时 NOT_FOUND
	if _, err := s.Speech.SpeakingResultGet(ctx, SpeakingResultGetReq{
		WorkspaceID: ws.ID, SubmissionID: submissionID,
	}); domain.AsError(err) == nil || domain.AsError(err).Code != domain.CodeNotFound {
		t.Fatalf("expected NOT_FOUND before submit, got %v", err)
	}
	if _, err := s.Speech.SpeakingSubmit(ctx, SpeakingSubmitReq{
		WorkspaceID: ws.ID, SubmissionID: submissionID,
		AudioPath: audioPath, IdempotencyKey: "spk-" + NewID(),
	}); err != nil {
		t.Fatalf("speaking submit: %v", err)
	}
	got, err := s.Speech.SpeakingResultGet(ctx, SpeakingResultGetReq{
		WorkspaceID: ws.ID, SubmissionID: submissionID,
	})
	if err != nil {
		t.Fatalf("result get: %v", err)
	}
	if got.Status != "graded" || got.SubmissionID != submissionID {
		t.Fatalf("unexpected result: %+v", got)
	}
}

// TestSaveAudioValidation 校验录音上传的格式与大小限制。
func TestSaveAudioValidation(t *testing.T) {
	s, _ := newTestServices(t)
	// 非法扩展名
	if _, err := s.Speech.SaveAudio("evil.exe", []byte("x")); domain.AsError(err) == nil ||
		domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("expected INVALID_ARG for exe, got %v", err)
	}
	// 空内容
	if _, err := s.Speech.SaveAudio("a.wav", nil); domain.AsError(err) == nil ||
		domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("expected INVALID_ARG for empty, got %v", err)
	}
	// 超过 20MB
	big := make([]byte, 21<<20)
	if _, err := s.Speech.SaveAudio("big.wav", big); domain.AsError(err) == nil ||
		domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("expected INVALID_ARG for oversize, got %v", err)
	}
	// 合法 wav 保存成功，路径位于 uploads 下
	m := &provider.MockTTS{}
	audio, _, _ := m.Synthesize(ctx(), "ok", 1.0)
	up, err := s.Speech.SaveAudio("ok.wav", audio)
	if err != nil {
		t.Fatalf("save ok.wav: %v", err)
	}
	if !strings.HasPrefix(filepath.ToSlash(up.Path), "uploads/") {
		t.Fatalf("expected uploads path, got %s", up.Path)
	}
}

// TestResolveUploadPath 校验路径穿越防护。
func TestResolveUploadPath(t *testing.T) {
	dir := t.TempDir()
	if _, err := resolveUploadPath("../evil.txt", dir); domain.AsError(err) == nil ||
		domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("expected INVALID_ARG for traversal, got %v", err)
	}
	if _, err := resolveUploadPath("uploads/../evil.txt", dir); domain.AsError(err) == nil ||
		domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("expected INVALID_ARG for nested traversal, got %v", err)
	}
	// uploads 目录内合法
	uploads := filepath.Join(dir, "uploads")
	if err := os.MkdirAll(uploads, 0o700); err != nil {
		t.Fatal(err)
	}
	full, err := resolveUploadPath("uploads/a.wav", dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if full != filepath.Join(uploads, "a.wav") {
		t.Fatalf("unexpected resolved path: %s", full)
	}
}

// TestProviderTestTTSASR 校验 tts/asr Provider 连通性测试（mock 无网络）。
func TestProviderTestTTSASR(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	ctx := context.Background()
	if _, err := s.ProviderConfigure(ctx, ProviderConfigureReq{
		WorkspaceID: ws.ID, Provider: "tts", Kind: "mock", Enabled: true,
	}); err != nil {
		t.Fatalf("configure tts: %v", err)
	}
	if _, err := s.ProviderConfigure(ctx, ProviderConfigureReq{
		WorkspaceID: ws.ID, Provider: "asr", Kind: "mock", Enabled: true,
	}); err != nil {
		t.Fatalf("configure asr: %v", err)
	}
	h, err := s.ProviderTest(ctx, ProviderTestReq{WorkspaceID: ws.ID, Provider: "tts"})
	if err != nil {
		t.Fatalf("tts test: %v", err)
	}
	if !h.OK {
		t.Fatalf("tts test not ok: %+v", h)
	}
	h, err = s.ProviderTest(ctx, ProviderTestReq{WorkspaceID: ws.ID, Provider: "asr"})
	if err != nil {
		t.Fatalf("asr test: %v", err)
	}
	if !h.OK {
		t.Fatalf("asr test not ok: %+v", h)
	}
}
