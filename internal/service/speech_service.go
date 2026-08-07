package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"lumo/internal/agent"
	"lumo/internal/domain"
	"lumo/internal/provider"
	"lumo/internal/repository"
)

// TTSResult 是语音合成结果（音频经 /api/v1/files?path=uploads/... 下载）。
type TTSResult struct {
	AudioPath  string `json:"audio_path"` // 相对 uploads 目录
	Format     string `json:"format"`     // wav/mp3/m4a
	DurationMs int64  `json:"duration_ms"`
}

// SpeakingResult 是口语测评结果 DTO。
type SpeakingResult struct {
	ID           string             `json:"id"`
	SubmissionID string             `json:"submission_id"`
	Transcript   string             `json:"transcript"`
	Scores       map[string]float64 `json:"scores"`
	Status       string             `json:"status"` // pending | graded | failed
	CreatedAt    string             `json:"created_at"`
	UpdatedAt    string             `json:"updated_at"`
}

// SpeechService 实现语音合成与口语测评用例（完整设计文档 4.18 / API 设计文档 7.16）。
type SpeechService struct{ s *Services }

// TTSPlayReq 语音合成请求。
type TTSPlayReq struct {
	WorkspaceID string  `json:"workspace_id"`
	RefType     string  `json:"ref_type"` // question | note | flashcard | document
	RefID       string  `json:"ref_id"`
	Speed       float64 `json:"speed"` // 0.5–2.0，缺省 1.0
}

// TTSPlay 合成语音：解析引用文本 → TTS Provider 合成 → 落盘 uploads 返回引用。
func (sp *SpeechService) TTSPlay(ctx context.Context, req TTSPlayReq) (*TTSResult, error) {
	if err := sp.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	text, err := sp.resolveTTSContent(ctx, req.WorkspaceID, req.RefType, req.RefID)
	if err != nil {
		return nil, err
	}
	cfg, ok := sp.s.speechProviderConfig("tts")
	if !ok {
		return nil, domain.FeatureDisabled("语音合成未启用，请在设置中配置 TTS Provider")
	}
	kind, _ := cfg["kind"].(string)
	if kind == "" {
		kind = "openai"
	}
	p, err := provider.NewTTS("tts-"+kind, cfg)
	if err != nil {
		return nil, domain.InvalidArg("%v", err)
	}
	speed := req.Speed
	if speed == 0 {
		speed = 1.0
	}
	if speed < 0.5 || speed > 2.0 {
		return nil, domain.InvalidArg("speed 仅允许 0.5–2.0")
	}
	audio, format, err := p.Synthesize(ctx, text, speed)
	if err != nil {
		return nil, domain.InvalidState("语音合成失败: %v", err)
	}
	if len(audio) == 0 {
		return nil, domain.InvalidState("语音合成结果为空")
	}
	up, err := sp.saveAudio("tts", format, audio)
	if err != nil {
		return nil, err
	}
	return &TTSResult{AudioPath: up.Path, Format: format, DurationMs: provider.WAVDurationMs(audio)}, nil
}

// SpeakingSubmitReq 口语提交请求。
type SpeakingSubmitReq struct {
	WorkspaceID    string `json:"workspace_id"`
	SubmissionID   string `json:"submission_id"`
	AudioPath      string `json:"audio_path"` // 相对 uploads 目录
	IdempotencyKey string `json:"idempotency_key"`
}

// SpeakingSubmit 提交口语录音：ASR 转写 + 分维度评分，落库后发布 grading:updated。
func (sp *SpeechService) SpeakingSubmit(ctx context.Context, req SpeakingSubmitReq) (*SpeakingResult, error) {
	if err := sp.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.SubmissionID == "" {
		return nil, domain.InvalidArg("submission_id 必填")
	}
	if req.AudioPath == "" {
		return nil, domain.InvalidArg("audio_path 必填（先通过 SpeakingUpload 上传）")
	}
	if req.IdempotencyKey == "" {
		return nil, domain.InvalidArg("idempotency_key 必填")
	}
	sub, err := sp.s.Repo.GetSubmission(ctx, req.SubmissionID)
	if err != nil {
		return nil, err
	}
	if sub == nil {
		return nil, domain.NotFound("提交不存在")
	}
	sess, err := sp.s.Repo.GetSession(ctx, req.WorkspaceID, sub.SessionID)
	if err != nil {
		return nil, err
	}
	if sess == nil {
		return nil, domain.NotFound("提交所属会话不存在")
	}
	audioPath, err := resolveUploadPath(req.AudioPath, sp.s.Cfg.DataDir)
	if err != nil {
		return nil, domain.InvalidArg("%v", err)
	}
	if _, err := os.Stat(audioPath); err != nil {
		return nil, domain.InvalidArg("音频文件不存在，请先通过 SpeakingUpload 上传")
	}
	cfg, ok := sp.s.speechProviderConfig("asr")
	if !ok {
		return nil, domain.FeatureDisabled("语音识别未启用，请在设置中配置 ASR Provider")
	}
	return withIdempotency(sp.s, ctx, req.WorkspaceID, req.IdempotencyKey, "SpeakingSubmit",
		func() (*SpeakingResult, error) {
			kind, _ := cfg["kind"].(string)
			if kind == "" {
				kind = "openai"
			}
			p, err := provider.NewASR("asr-"+kind, cfg)
			if err != nil {
				return nil, domain.InvalidArg("%v", err)
			}
			asr, err := p.Transcribe(ctx, audioPath)
			if err != nil {
				return nil, domain.InvalidState("语音识别失败: %v", err)
			}
			scoresJSON, _ := json.Marshal(asr.Scores)
			row := &repository.SpeakingResultRow{
				ID:           stableID("speaking", req.SubmissionID),
				SubmissionID: req.SubmissionID,
				Transcript:   asr.Transcript,
				ScoresJSON:   string(scoresJSON),
				AudioKept:    true,
				Status:       "graded",
			}
			saved, err := sp.s.Repo.UpsertSpeakingResult(ctx, row)
			if err != nil {
				return nil, err
			}
			if err := sp.s.UserEvents.Publish(sess.UserID, agent.Event{
				Name: agent.EventGradingUpdated,
				Payload: map[string]any{
					"grading_id":    saved.ID,
					"submission_id": saved.SubmissionID,
					"status":        saved.Status,
					"score":         overallScore(asr.Scores),
				},
			}); err != nil {
				return nil, err
			}
			sp.s.audit(ctx, req.WorkspaceID, "speaking.submit", "submission", saved.SubmissionID,
				map[string]any{"status": saved.Status})
			return speakingFromRow(saved), nil
		})
}

// SpeakingResultGetReq 获取口语结果请求。
type SpeakingResultGetReq struct {
	WorkspaceID  string `json:"workspace_id"`
	SubmissionID string `json:"submission_id"`
}

// SpeakingResultGet 按提交获取口语测评结果。
func (sp *SpeechService) SpeakingResultGet(ctx context.Context, req SpeakingResultGetReq) (*SpeakingResult, error) {
	if err := sp.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	row, err := sp.s.Repo.GetSpeakingResultBySubmission(ctx, req.SubmissionID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, domain.NotFound("口语结果不存在")
	}
	return speakingFromRow(row), nil
}

// ---------- 内部辅助 ----------

// resolveTTSContent 解析 TTS 朗读文本（ref_type: question|note|flashcard|document）。
func (sp *SpeechService) resolveTTSContent(ctx context.Context, wsID, refType, refID string) (string, error) {
	switch refType {
	case "question":
		q, err := sp.s.Repo.GetQuestion(ctx, wsID, refID)
		if err != nil {
			return "", err
		}
		if q == nil || q.CurrentVersionID == nil {
			return "", domain.NotFound("题目不存在或没有可用版本")
		}
		v, err := sp.s.Repo.GetQuestionVersion(ctx, *q.CurrentVersionID)
		if err != nil {
			return "", err
		}
		if v == nil {
			return "", domain.NotFound("题目版本不存在")
		}
		var pl QuestionPayload
		if err := json.Unmarshal(v.Payload, &pl); err != nil {
			return "", domain.InvalidState("题目载荷解析失败")
		}
		var sb strings.Builder
		sb.WriteString(pl.Stem)
		for _, o := range pl.Options {
			sb.WriteString(" " + o.Text)
		}
		return truncateText(sb.String(), 4000), nil
	case "note":
		n, err := sp.s.Repo.GetNote(ctx, wsID, refID)
		if err != nil {
			return "", err
		}
		if n == nil || n.DeletedAt != nil {
			return "", domain.NotFound("笔记不存在")
		}
		return truncateText(n.Title+"\n"+n.BodyMD, 4000), nil
	case "flashcard":
		f, err := sp.s.Repo.GetFlashcard(ctx, wsID, refID)
		if err != nil {
			return "", err
		}
		if f == nil || f.DeletedAt != nil {
			return "", domain.NotFound("闪卡不存在")
		}
		return truncateText(f.Front+"\n"+f.Back, 4000), nil
	case "document":
		d, err := sp.s.Repo.GetDocument(ctx, wsID, refID)
		if err != nil {
			return "", err
		}
		if d == nil {
			return "", domain.NotFound("文档不存在")
		}
		chunks, err := sp.s.Repo.ListDocumentChunks(ctx, wsID, []string{refID})
		if err != nil {
			return "", err
		}
		var sb strings.Builder
		for _, c := range chunks {
			if c.Section != nil && *c.Section != "" {
				sb.WriteString(*c.Section + "\n")
			}
			sb.WriteString(c.TextRef + "\n")
		}
		return truncateText(sb.String(), 4000), nil
	default:
		return "", domain.InvalidArg("ref_type 仅允许 question/note/flashcard/document")
	}
}

// speechProviderConfig 读取 TTS/ASR Provider 配置。
// 与 providerConfig 不同：mock 无需 api_key 即视为已配置；openai 需要 api_key。
func (s *Services) speechProviderConfig(name string) (map[string]any, bool) {
	b, err := readSecretsFile(s.Cfg.SecretsPath)
	if err != nil {
		return nil, false
	}
	v, ok := b["provider_"+name]
	if !ok {
		return nil, false
	}
	var m map[string]any
	if json.Unmarshal([]byte(v), &m) != nil {
		return nil, false
	}
	kind, _ := m["kind"].(string)
	if kind == "mock" {
		return m, true
	}
	if m["api_key"] == nil || m["api_key"] == "" {
		return nil, false
	}
	return m, true
}

// SaveAudio 保存口语录音上传到 uploads 目录（≤20MB，wav/mp3/m4a）。
func (sp *SpeechService) SaveAudio(fileName string, content []byte) (*UploadedFile, error) {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(fileName), "."))
	if ext != "wav" && ext != "mp3" && ext != "m4a" {
		return nil, domain.InvalidArg("仅支持 wav/mp3/m4a 音频格式")
	}
	return sp.saveAudio("audio", ext, content)
}

// saveAudio 保存音频到 uploads 目录（安全文件名），返回引用路径。
func (sp *SpeechService) saveAudio(prefix, ext string, content []byte) (*UploadedFile, error) {
	if ext == "" {
		ext = "wav"
	}
	if len(content) == 0 {
		return nil, domain.InvalidArg("文件内容为空")
	}
	if len(content) > 20<<20 {
		return nil, domain.InvalidArg("音频超过 20MB 上限")
	}
	dir := filepath.Join(sp.s.Cfg.DataDir, "uploads")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(content)
	name := fmt.Sprintf("%s-%s-%x.%s", prefix, shortID(NewID()), sum[:6], ext)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return nil, err
	}
	return &UploadedFile{
		Path:     filepath.Join("uploads", name),
		FileName: name,
		Size:     int64(len(content)),
		SHA256:   hex.EncodeToString(sum[:]),
	}, nil
}

func speakingFromRow(row *repository.SpeakingResultRow) *SpeakingResult {
	var scores map[string]float64
	_ = json.Unmarshal([]byte(row.ScoresJSON), &scores)
	return &SpeakingResult{
		ID:           row.ID,
		SubmissionID: row.SubmissionID,
		Transcript:   row.Transcript,
		Scores:       scores,
		Status:       row.Status,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
}

func overallScore(scores map[string]float64) float64 {
	if len(scores) == 0 {
		return 0
	}
	var sum float64
	for _, v := range scores {
		sum += v
	}
	return sum / float64(len(scores))
}

// resolveUploadPath 解析用户提供的相对路径，且必须位于 uploads 目录内（安全读音频）。
func resolveUploadPath(p, dataDir string) (string, error) {
	full, err := resolveLocalPath(p, dataDir)
	if err != nil {
		return "", err
	}
	uploads := filepath.Join(dataDir, "uploads")
	if full != uploads && !strings.HasPrefix(full, uploads+string(filepath.Separator)) {
		return "", domain.InvalidArg("仅允许访问 uploads 目录内的文件")
	}
	return full, nil
}
