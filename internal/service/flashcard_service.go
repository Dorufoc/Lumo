package service

import (
	"archive/zip"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // 注册 sqlite 驱动（Anki collection.anki2 构建）

	"lumo/internal/agent"
	"lumo/internal/crypto"
	"lumo/internal/domain"
	"lumo/internal/repository"
)

// Flashcard 是闪卡 DTO（4.9 数据模型）。
type Flashcard struct {
	ID             string   `json:"id"`
	WorkspaceID    string   `json:"workspace_id"`
	Source         string   `json:"source"`
	SourceRef      string   `json:"source_ref,omitempty"`
	Front          string   `json:"front"`
	Back           string   `json:"back"`
	CardType       string   `json:"card_type"`
	State          string   `json:"state"`
	Repetition     int      `json:"repetition"`
	IntervalDays   int      `json:"interval_days"`
	EaseFactor     float64  `json:"ease_factor"`
	DueAt          string   `json:"due_at"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
	Version        int      `json:"version"`
}

// FlashcardCreateReq 手动创建闪卡请求。
type FlashcardCreateReq struct {
	WorkspaceID string   `json:"workspace_id"`
	UserID      string   `json:"user_id"`
	Source      string   `json:"source"` // knowledge | note | document | manual
	SourceRef   string   `json:"source_ref,omitempty"`
	Front       string   `json:"front"`
	Back        string   `json:"back"`
	CardType    string   `json:"card_type"` // basic | choice | cloze | code
}

// FlashcardGenerateReq 从题目版本快照/笔记/文档生成闪卡请求。
type FlashcardGenerateReq struct {
	WorkspaceID    string `json:"workspace_id"`
	UserID         string `json:"user_id"`
	SourceRef      string `json:"source_ref"`
	IdempotencyKey string `json:"idempotency_key"`
}

// FlashcardListDueReq 到期闪卡列表请求。
type FlashcardListDueReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	DueBefore   string `json:"due_before"` // 默认现在
	Limit       int    `json:"limit"`
}

// FlashcardReviewReq 评级复习请求（间隔只由服务端计算）。
type FlashcardReviewReq struct {
	WorkspaceID    string `json:"workspace_id"`
	FlashcardID    string `json:"flashcard_id"`
	Rating         string `json:"rating"` // again | hard | good
	IdempotencyKey string `json:"idempotency_key"`
}

// FlashcardBatchReq 批量操作请求。
type FlashcardBatchReq struct {
	WorkspaceID string   `json:"workspace_id"`
	Action      string   `json:"action"` // archive | delete | reset
	IDs         []string `json:"ids"`
}

// BatchError 是批量操作单项错误。
type BatchError struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

// BatchResult 是批量操作结果。
type BatchResult struct {
	SuccessCount int          `json:"success_count"`
	ErrorCount   int          `json:"error_count"`
	Errors       []BatchError `json:"errors,omitempty"`
}

// FlashcardImportCsvReq CSV 导入闪卡请求。
type FlashcardImportCsvReq struct {
	WorkspaceID    string `json:"workspace_id"`
	UserID         string `json:"user_id"`
	FilePath       string `json:"file_path"` // LibraryUpload 返回的路径
	IdempotencyKey string `json:"idempotency_key"`
}

// FlashcardExportAnkiReq 导出 Anki .apkg 请求。
type FlashcardExportAnkiReq struct {
	WorkspaceID    string `json:"workspace_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

// FlashcardService 实现闪卡用例（API 文档 7.2）。
type FlashcardService struct{ s *Services }

func flashcardFromRow(f *repository.FlashcardRow) *Flashcard {
	d := &Flashcard{
		ID: f.ID, WorkspaceID: f.WorkspaceID, Source: f.Source,
		Front: f.Front, Back: f.Back, CardType: f.CardType,
		State: f.State, Repetition: f.Repetition, IntervalDays: f.IntervalDays,
		EaseFactor: f.EaseFactor, DueAt: f.DueAt,
		CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt, Version: f.Version,
	}
	if f.SourceRef != nil {
		d.SourceRef = *f.SourceRef
	}
	return d
}

// FlashcardCreate 手动创建闪卡。
func (fc *FlashcardService) FlashcardCreate(ctx context.Context, req FlashcardCreateReq) (*Flashcard, error) {
	if err := fc.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if !domain.ValidateFlashcardSource(req.Source) {
		return nil, domain.InvalidArg("source 仅允许 knowledge/note/document/manual")
	}
	if strings.TrimSpace(req.Front) == "" {
		return nil, domain.InvalidArg("front 不能为空")
	}
	if strings.TrimSpace(req.Back) == "" {
		return nil, domain.InvalidArg("back 不能为空")
	}
	cardType := req.CardType
	if cardType == "" {
		cardType = domain.FlashcardTypeBasic
	}
	if !domain.ValidateFlashcardCardType(cardType) {
		return nil, domain.InvalidArg("card_type 仅允许 basic/choice/cloze/code")
	}
	row := &repository.FlashcardRow{
		ID: NewID(), WorkspaceID: req.WorkspaceID, UserID: req.UserID,
		Source: req.Source, Front: req.Front, Back: req.Back, CardType: cardType,
		State: domain.FlashcardStateLearning,
		Repetition: 0, IntervalDays: 0, EaseFactor: 2.5, DueAt: Now(), Version: 1,
	}
	if req.SourceRef != "" {
		row.SourceRef = &req.SourceRef
	}
	if err := fc.s.Repo.CreateFlashcard(ctx, row); err != nil {
		return nil, err
	}
	fc.s.audit(ctx, req.WorkspaceID, "flashcard.create", "flashcard", row.ID,
		map[string]any{"source": row.Source, "card_type": cardType})
	return flashcardFromRow(row), nil
}

// FlashcardGenerate 从题目版本快照/笔记/文档生成闪卡（同一来源去重，不重复生成）。
func (fc *FlashcardService) FlashcardGenerate(ctx context.Context, req FlashcardGenerateReq) ([]*Flashcard, error) {
	if err := fc.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if req.SourceRef == "" {
		return nil, domain.InvalidArg("source_ref 必填")
	}
	if req.IdempotencyKey == "" {
		return nil, domain.InvalidArg("idempotency_key 必填")
	}
	return withIdempotency(fc.s, ctx, req.WorkspaceID, req.IdempotencyKey, "FlashcardGenerate",
		func() ([]*Flashcard, error) {
			// 1) 题目版本快照（错题/题目批量生成；版本不可变）
			v, err := fc.s.Repo.GetQuestionVersion(ctx, req.SourceRef)
			if err != nil {
				return nil, err
			}
			if v != nil {
				card, err := fc.buildFromQuestionVersion(ctx, req.WorkspaceID, req.UserID, v)
				if err != nil {
					return nil, err
				}
				return []*Flashcard{card}, nil
			}
			// 2) 笔记
			title, body, found, err := fc.s.Repo.GetNoteForFlashcard(ctx, req.WorkspaceID, req.SourceRef)
			if err != nil {
				return nil, err
			}
			if found {
				card, err := fc.buildFromNote(ctx, req.WorkspaceID, req.UserID, req.SourceRef, title, body)
				if err != nil {
					return nil, err
				}
				return []*Flashcard{card}, nil
			}
			// 3) 文档
			doc, err := fc.s.Repo.GetDocument(ctx, req.WorkspaceID, req.SourceRef)
			if err != nil {
				return nil, err
			}
			if doc != nil {
				card, err := fc.buildFromDocument(ctx, req.WorkspaceID, req.UserID, req.SourceRef, doc)
				if err != nil {
					return nil, err
				}
				return []*Flashcard{card}, nil
			}
			return nil, domain.NotFound("生成来源不存在")
		})
}

// buildFromQuestionVersion 从题目版本快照生成闪卡（source_ref 固定为版本 ID）。
func (fc *FlashcardService) buildFromQuestionVersion(ctx context.Context, wsID, userID string,
	v *repository.QuestionVersionRow) (*Flashcard, error) {
	payload, err := parseQuestionPayload(v.Payload)
	if err != nil {
		return nil, err
	}
	front, back, cardType := flashcardFromPayload(payload)
	return fc.createGenerated(ctx, wsID, userID, domain.FlashcardSourceManual, v.ID, front, back, cardType)
}

// buildFromNote 从笔记生成闪卡（前=标题，后=正文）。
func (fc *FlashcardService) buildFromNote(ctx context.Context, wsID, userID, noteID, title, body string) (*Flashcard, error) {
	front := strings.TrimSpace(title)
	if front == "" {
		front = truncateText(body, 60)
	}
	back := strings.TrimSpace(body)
	if back == "" {
		back = "（笔记无正文）"
	}
	return fc.createGenerated(ctx, wsID, userID, domain.FlashcardSourceNote, noteID, front, back, domain.FlashcardTypeBasic)
}

// buildFromDocument 从文档生成闪卡（前=文件名，后=文档路径）。
func (fc *FlashcardService) buildFromDocument(ctx context.Context, wsID, userID, docID string,
	doc *repository.DocumentRow) (*Flashcard, error) {
	front := strings.TrimSpace(doc.FileName)
	if front == "" {
		front = doc.RelativePath
	}
	back := "文档：" + doc.RelativePath
	return fc.createGenerated(ctx, wsID, userID, domain.FlashcardSourceDocument, docID, front, back, domain.FlashcardTypeBasic)
}

// createGenerated 同一（source, source_ref）已存在则返回已有卡，否则创建。
func (fc *FlashcardService) createGenerated(ctx context.Context, wsID, userID, source, sourceRef, front, back, cardType string) (*Flashcard, error) {
	existing, err := fc.s.Repo.GetFlashcardBySource(ctx, wsID, source, sourceRef)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return flashcardFromRow(existing), nil
	}
	row := &repository.FlashcardRow{
		ID: NewID(), WorkspaceID: wsID, UserID: userID,
		Source: source, SourceRef: &sourceRef, Front: front, Back: back, CardType: cardType,
		State: domain.FlashcardStateLearning,
		Repetition: 0, IntervalDays: 0, EaseFactor: 2.5, DueAt: Now(), Version: 1,
	}
	if err := fc.s.Repo.CreateFlashcard(ctx, row); err != nil {
		return nil, err
	}
	fc.s.audit(ctx, wsID, "flashcard.generate", "flashcard", row.ID,
		map[string]any{"source": source, "source_ref": sourceRef})
	return flashcardFromRow(row), nil
}

// flashcardFromPayload 将题目载荷映射为闪卡前/背面与题型（F2）。
func flashcardFromPayload(p *QuestionPayload) (front, back, cardType string) {
	switch p.Type {
	case "single_choice", "multiple_choice":
		cardType = domain.FlashcardTypeChoice
		front = strings.TrimSpace(p.Stem)
		if len(p.Options) > 0 {
			var b strings.Builder
			for _, o := range p.Options {
				b.WriteString("\n" + o.Key + ". " + o.Text)
			}
			front += b.String()
		}
		back = "答案：" + answerText(p.Answer)
	case "fill_blank":
		cardType = domain.FlashcardTypeCloze
		front = strings.TrimSpace(p.Stem)
		back = "答案：" + answerText(p.Answer)
	case "code":
		cardType = domain.FlashcardTypeCode
		front = strings.TrimSpace(p.Stem)
		back = "答案：\n" + answerText(p.Answer)
	default: // short_answer
		cardType = domain.FlashcardTypeBasic
		front = strings.TrimSpace(p.Stem)
		back = "答案：" + answerText(p.Answer)
	}
	if strings.TrimSpace(p.Analysis) != "" {
		back += "\n解析：" + strings.TrimSpace(p.Analysis)
	}
	return front, back, cardType
}

// answerText 将答案载荷转为展示文本（支持字符串与多选题数组）。
func answerText(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return strings.Join(arr, ", ")
	}
	return string(raw)
}

func truncateText(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

// FlashcardListDue 列出到期闪卡；存在到期卡时向 UserEventBus 发布 flashcard:due。
func (fc *FlashcardService) FlashcardListDue(ctx context.Context, req FlashcardListDueReq) ([]*Flashcard, error) {
	if err := fc.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	dueBefore := req.DueBefore
	if dueBefore == "" {
		dueBefore = Now()
	} else if _, err := domain.ParseTime(dueBefore); err != nil {
		return nil, domain.InvalidArg("due_before 时间格式非法")
	}
	rows, err := fc.s.Repo.ListDueFlashcards(ctx, req.WorkspaceID, req.UserID, dueBefore, req.Limit)
	if err != nil {
		return nil, err
	}
	out := make([]*Flashcard, 0, len(rows))
	for _, row := range rows {
		out = append(out, flashcardFromRow(row))
	}
	if len(out) > 0 && fc.s.Agent != nil && fc.s.Agent.UserEvents != nil {
		count, err := fc.s.Repo.CountDueFlashcards(ctx, req.WorkspaceID, req.UserID, dueBefore)
		if err != nil {
			return nil, err
		}
		_ = fc.s.Agent.UserEvents.Publish(req.UserID, agent.Event{
			Name:    agent.EventFlashcardDue,
			Payload: map[string]any{"due_count": count},
		})
	}
	return out, nil
}

// FlashcardReview 简化 SM-2 评级复习：间隔只由服务端计算。
func (fc *FlashcardService) FlashcardReview(ctx context.Context, req FlashcardReviewReq) (*Flashcard, error) {
	if err := fc.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if !domain.ValidateFlashcardRating(req.Rating) {
		return nil, domain.InvalidArg("rating 仅允许 again/hard/good")
	}
	if req.IdempotencyKey == "" {
		return nil, domain.InvalidArg("idempotency_key 必填")
	}
	return withIdempotency(fc.s, ctx, req.WorkspaceID, req.IdempotencyKey, "FlashcardReview",
		func() (*Flashcard, error) {
			card, err := fc.s.Repo.GetFlashcard(ctx, req.WorkspaceID, req.FlashcardID)
			if err != nil {
				return nil, err
			}
			if card == nil || card.DeletedAt != nil {
				return nil, domain.NotFound("闪卡不存在")
			}
			if card.State == domain.FlashcardStateArchived {
				return nil, domain.InvalidState("闪卡已归档")
			}
			if card.State == domain.FlashcardStateMastered {
				return nil, domain.InvalidState("闪卡已掌握，无需复习")
			}
			next := domain.ApplyFlashcardSM2(card.Repetition, card.IntervalDays, card.EaseFactor, req.Rating)
			prev, err := fc.s.Repo.LastFlashcardRatings(ctx, card.ID, domain.FlashcardMasteredStreak-1)
			if err != nil {
				return nil, err
			}
			state := domain.FlashcardNextState(req.Rating, next, prev)
			now := Now()
			updated, err := fc.s.Repo.UpdateFlashcardSM2(ctx, card.ID, next.Repetition, next.IntervalDays,
				next.EaseFactor, next.DueAt, state, card.Version)
			if err != nil {
				return nil, err
			}
			if err := fc.s.Repo.CreateFlashcardReview(ctx, &repository.FlashcardReviewRow{
				ID: NewID(), FlashcardID: card.ID, Rating: req.Rating,
				ReviewedAt: now, NextDueAt: next.DueAt,
			}); err != nil {
				return nil, err
			}
			fc.s.audit(ctx, req.WorkspaceID, "flashcard.review", "flashcard", card.ID,
				map[string]any{"rating": req.Rating, "interval_days": next.IntervalDays})
			// 变更记录（同步队列）
			_ = fc.s.Repo.RecordSyncOp(ctx, req.WorkspaceID, "flashcard", card.ID, "update", card.Version,
				map[string]any{"rating": req.Rating, "due_at": next.DueAt, "interval_days": next.IntervalDays})
			return flashcardFromRow(updated), nil
		})
}

// FlashcardBatch 批量操作：archive / delete（软删）/ reset（重置学习）。
func (fc *FlashcardService) FlashcardBatch(ctx context.Context, req FlashcardBatchReq) (*BatchResult, error) {
	if err := fc.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.Action != "archive" && req.Action != "delete" && req.Action != "reset" {
		return nil, domain.InvalidArg("action 仅允许 archive/delete/reset")
	}
	if len(req.IDs) == 0 {
		return nil, domain.InvalidArg("ids 不能为空")
	}
	var valid []string
	var errs []BatchError
	for _, id := range req.IDs {
		row, err := fc.s.Repo.GetFlashcard(ctx, req.WorkspaceID, id)
		if err != nil {
			return nil, err
		}
		if row == nil || row.DeletedAt != nil {
			errs = append(errs, BatchError{ID: id, Error: "闪卡不存在或已删除"})
			continue
		}
		valid = append(valid, id)
	}
	n, err := fc.s.Repo.BatchFlashcardAction(ctx, req.WorkspaceID, req.Action, valid)
	if err != nil {
		return nil, err
	}
	for _, id := range valid {
		fc.s.audit(ctx, req.WorkspaceID, "flashcard.batch."+req.Action, "flashcard", id, nil)
	}
	return &BatchResult{SuccessCount: n, ErrorCount: len(errs), Errors: errs}, nil
}

// flashcardCSVRow 是 CSV 导入的一行。
type flashcardCSVRow struct {
	front, back, cardType, rating string
}

// parseFlashcardCSV 解析 CSV（表头 front/back 必填；card_type/rating 可选）。
func parseFlashcardCSV(content []byte) ([]flashcardCSVRow, error) {
	r := csv.NewReader(strings.NewReader(string(content)))
	r.TrimLeadingSpace = true
	records, err := r.ReadAll()
	if err != nil {
		return nil, domain.InvalidArg("CSV 解析失败: %v", err)
	}
	if len(records) < 2 {
		return nil, domain.InvalidArg("CSV 至少需要表头和一行数据")
	}
	colIdx := map[string]int{}
	for i, h := range records[0] {
		colIdx[strings.ToLower(strings.TrimSpace(h))] = i
	}
	idx := func(name string) int {
		if i, ok := colIdx[name]; ok {
			return i
		}
		return -1
	}
	if idx("front") < 0 || idx("back") < 0 {
		return nil, domain.InvalidArg("CSV 表头必须包含 front 与 back")
	}
	out := make([]flashcardCSVRow, 0, len(records)-1)
	for _, rec := range records[1:] {
		cell := func(name string) string {
			j := idx(name)
			if j < 0 || j >= len(rec) {
				return ""
			}
			return strings.TrimSpace(rec[j])
		}
		out = append(out, flashcardCSVRow{
			front: cell("front"), back: cell("back"),
			cardType: cell("card_type"), rating: cell("rating"),
		})
	}
	return out, nil
}

// FlashcardImportCsv 解析并导入 CSV 闪卡；任一非法行 → INVALID_ARGUMENT 原子回滚。
func (fc *FlashcardService) FlashcardImportCsv(ctx context.Context, req FlashcardImportCsvReq) (*ImportBatch, error) {
	if err := fc.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if req.IdempotencyKey == "" {
		return nil, domain.InvalidArg("idempotency_key 必填")
	}
	return withIdempotency(fc.s, ctx, req.WorkspaceID, req.IdempotencyKey, "FlashcardImportCsv",
		func() (*ImportBatch, error) {
			content, err := fc.readCSV(ctx, req.FilePath)
			if err != nil {
				return nil, err
			}
			rows, err := parseFlashcardCSV(content)
			if err != nil {
				return nil, err
			}
			// 先全量校验（原子：任一非法行整批失败，不产生部分插入）
			type prepared struct {
				row     *repository.FlashcardRow
				payload json.RawMessage
			}
			preparedRows := make([]prepared, 0, len(rows))
			for i, r := range rows {
				line := i + 2
				front, back := r.front, r.back
				if front == "" || back == "" {
					return nil, domain.InvalidArg("第 %d 行 front/back 不能为空", line)
				}
				cardType := r.cardType
				if cardType == "" {
					cardType = domain.FlashcardTypeBasic
				}
				if !domain.ValidateFlashcardCardType(cardType) {
					return nil, domain.InvalidArg("第 %d 行 card_type 非法", line)
				}
				rating := r.rating
				if rating != "" && !domain.ValidateFlashcardRating(rating) {
					return nil, domain.InvalidArg("第 %d 行 rating 非法（仅允许 again/hard/good）", line)
				}
				row := &repository.FlashcardRow{
					ID: NewID(), WorkspaceID: req.WorkspaceID, UserID: req.UserID,
					Source: domain.FlashcardSourceManual, Front: front, Back: back, CardType: cardType,
					State: domain.FlashcardStateLearning,
					Repetition: 0, IntervalDays: 0, EaseFactor: 2.5, DueAt: Now(), Version: 1,
				}
				if rating != "" {
					// 初始调度：按评级先走一次简化 SM-2
					next := domain.ApplyFlashcardSM2(0, 0, 2.5, rating)
					row.Repetition = next.Repetition
					row.IntervalDays = next.IntervalDays
					row.EaseFactor = next.EaseFactor
					row.DueAt = next.DueAt
					row.State = domain.FlashcardNextState(rating, next, nil)
				}
				preparedRows = append(preparedRows, prepared{
					row: row,
					payload: mustJSON(map[string]any{
						"front": front, "back": back, "card_type": cardType, "rating": rating,
					}),
				})
			}
			// 全量合法后事务插入
			cards := make([]*repository.FlashcardRow, 0, len(preparedRows))
			for _, p := range preparedRows {
				cards = append(cards, p.row)
			}
			if err := fc.s.Repo.CreateFlashcardsBatch(ctx, cards); err != nil {
				return nil, err
			}
			items := make([]*ImportItem, 0, len(preparedRows))
			for i, p := range preparedRows {
				items = append(items, &ImportItem{
					ID: NewID(), ItemNo: i + 1, Payload: p.payload, Status: "valid",
				})
			}
			sum := sha256.Sum256(content)
			batch := &ImportBatch{
				ID: NewID(), WorkspaceID: req.WorkspaceID, IdempotencyKey: req.IdempotencyKey,
				FileName: filepath.Base(req.FilePath), FileHash: hex.EncodeToString(sum[:]),
				Format: "flashcard_csv", Status: "committed",
				TotalCount: len(preparedRows), ValidCount: len(preparedRows), ErrorCount: 0,
				CreatedAt: Now(), UpdatedAt: Now(), Items: items,
			}
			fc.s.audit(ctx, req.WorkspaceID, "flashcard.import_csv", "workspace", req.WorkspaceID,
				map[string]any{"file": batch.FileName, "count": batch.ValidCount})
			return batch, nil
		})
}

// readCSV 读取上传的 CSV 文件（与 import.go readUpload 同路径解析）。
func (fc *FlashcardService) readCSV(ctx context.Context, filePath string) ([]byte, error) {
	if filePath == "" {
		return nil, domain.InvalidArg("file_path 必填（通过 LibraryUpload 上传）")
	}
	p, err := resolveLocalPath(filePath, fc.s.Cfg.DataDir)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, domain.NotFound("文件不存在或不可读")
	}
	return b, nil
}

// FlashcardExportAnki 导出 Anki .apkg（zip 内含 collection.anki2 SQLite + media 清单）。
func (fc *FlashcardService) FlashcardExportAnki(ctx context.Context, req FlashcardExportAnkiReq) (*ExportResult, error) {
	if err := fc.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.IdempotencyKey == "" {
		return nil, domain.InvalidArg("idempotency_key 必填")
	}
	return withIdempotency(fc.s, ctx, req.WorkspaceID, req.IdempotencyKey, "FlashcardExportAnki",
		func() (*ExportResult, error) {
			cards, err := fc.s.Repo.ListFlashcards(ctx, req.WorkspaceID)
			if err != nil {
				return nil, err
			}
			exportsDir := filepath.Join(fc.s.Cfg.DataDir, "exports")
			if err := os.MkdirAll(exportsDir, 0o700); err != nil {
				return nil, err
			}
			rand, _ := crypto.RandomUint64()
			fileName := fmt.Sprintf("flashcards-%s-%x.apkg", safeTimestamp(), rand)
			path := filepath.Join(exportsDir, fileName)
			if err := buildAnkiApkg(path, cards); err != nil {
				return nil, err
			}
			st, err := os.Stat(path)
			if err != nil {
				return nil, err
			}
			fc.s.audit(ctx, req.WorkspaceID, "flashcard.export", "workspace", req.WorkspaceID,
				map[string]any{"format": "apkg", "file": fileName, "cards": len(cards)})
			return &ExportResult{
				Path: filepath.Join("exports", fileName), FileName: fileName,
				Format: "apkg", SizeBytes: st.Size(),
			}, nil
		})
}

// buildAnkiApkg 构建 Anki 2.1 兼容的 .apkg：SQLite collection.anki2 + media 清单。
func buildAnkiApkg(path string, cards []*repository.FlashcardRow) error {
	tmp, err := os.MkdirTemp("", "lumo-anki-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	colPath := filepath.Join(tmp, "collection.anki2")
	if err := buildAnkiCollection(colPath, cards); err != nil {
		return err
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	if err := addZipFile(zw, "collection.anki2", colPath); err != nil {
		f.Close()
		return err
	}
	// media 清单：无媒体文件
	if err := addZipBytes(zw, "media", []byte("{}")); err != nil {
		f.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// buildAnkiCollection 建库并写入 col/notes/cards（Anki 2.1 schema ver 11）。
func buildAnkiCollection(colPath string, cards []*repository.FlashcardRow) error {
	db, err := sql.Open("sqlite", colPath)
	if err != nil {
		return err
	}
	defer db.Close()
	schema := `
CREATE TABLE col (
  id integer primary key, crt integer not null, mod integer not null, scm integer not null,
  ver integer not null, dty integer not null, usn integer not null, ls integer not null,
  conf text not null, models text not null, decks text not null, dconf text not null, tags text not null
);
CREATE TABLE notes (
  id integer primary key, guid text not null, mid integer not null, mod integer not null,
  usn integer not null, tags text not null, flds text not null, sfld text not null,
  csum integer not null, flags integer not null, data text not null
);
CREATE TABLE cards (
  id integer primary key, nid integer not null, did integer not null, ord integer not null,
  mod integer not null, usn integer not null, type integer not null, queue integer not null,
  due integer not null, ivl integer not null, factor integer not null, reps integer not null,
  lapses integer not null, left integer not null, odue integer not null, odid integer not null,
  flags integer not null, data text not null
);
CREATE TABLE revlog (
  id integer primary key, cid integer not null, usn integer not null, ease integer not null,
  ivl integer not null, lastIvl integer not null, factor integer not null, time integer not null,
  type integer not null
);
CREATE TABLE graves ( usn integer not null, oid integer not null, type integer not null );`
	if _, err := db.Exec(schema); err != nil {
		return err
	}

	now := time.Now().Unix()
	modelID := "1660000000000"
	models, _ := json.Marshal(map[string]any{
		modelID: map[string]any{
			"id": modelID, "name": "Lumo 闪卡", "type": 0, "mod": now, "usn": -1,
			"sortf": 0, "did": 1,
			"tmpls": []map[string]any{{
				"name": "Card 1", "ord": 0, "qfmt": "{{Front}}",
				"afmt": "{{FrontSide}}<hr id=answer>{{Back}}",
				"bqfmt": "", "bafmt": "", "did": nil, "bfont": "", "bsize": 0,
			}},
			"flds": []map[string]any{
				{"name": "Front", "ord": 0, "sticky": false, "rtl": false, "font": "Arial", "size": 20, "media": []any{}},
				{"name": "Back", "ord": 1, "sticky": false, "rtl": false, "font": "Arial", "size": 20, "media": []any{}},
			},
			"css":       ".card { font-family: arial; font-size: 20px; text-align: center; }",
			"latexPre":  "", "latexPost": "", "latexsvg": false,
			"req":       [][]any{{0, "any", []int{0, 1}}},
			"tags":      []any{}, "vers": []any{},
		},
	})
	decks, _ := json.Marshal(map[string]any{
		"1": map[string]any{
			"id": 1, "name": "Default", "desc": "", "usn": -1, "collapsed": false, "conf": 1,
			"lrnToday": []int{0, 0}, "revToday": []int{0, 0}, "newToday": []int{0, 0}, "timeToday": []int{0, 0},
			"dyn": 0, "extendNew": 0, "extendRev": 0, "newLmt": 20, "revLmt": 200,
			"maxTm": 0, "maxTaken": 60, "bury": false,
		},
	})
	dconf, _ := json.Marshal(map[string]any{
		"1": map[string]any{
			"id": 1, "mod": 0, "usn": 0, "name": "Default",
			"new": map[string]any{
				"bury": false, "delays": []int{1, 10}, "initialFactor": 2500,
				"ints": []int{1, 4, 7}, "order": 1, "perDay": 20, "separate": true,
			},
			"rev": map[string]any{
				"bury": false, "ease4": 1.3, "fuzz": 0.05, "ivlFct": 1.0,
				"maxIvl": 36500, "minSpace": 1, "perDay": 200, "hardFactor": 1.2,
			},
			"lapse":      map[string]any{"delays": []int{10}, "leechAction": 0, "leechFails": 8, "minInt": 1, "mult": 0.0},
			"maxTaken":   60, "timer": 0, "autoplay": true, "replayq": true, "newMix": 0,
			"newSortOrder": 0, "newGatherPriority": 0, "newSortPosition": 0,
			"browseSortType": "noteFld", "browseSortBackwards": false, "browseSortNew": false, "browseCollapsed": false,
			"dayLearnFirst": false, "newPerDay": 20, "reviewsPerDay": 200,
		},
	})
	conf, _ := json.Marshal(map[string]any{
		"activeDecks": []int{1}, "addToCur": true, "curDeck": 1, "newSpread": 0,
		"collapseTime": 1200, "timeLimits": map[string]int{"0": 0, "1": 0, "2": 0},
		"estTimes": true, "dueCounts": true, "curModel": nil, "nextPos": 1,
		"sortType": "noteFld", "sortBackwards": false, "sortNew": false,
		"creationOffset": 0, "schedVer": 2, "rollover": 4, "localOffset": 0,
		"newGatherPriority": 0, "newSortPosition": 0, "newSortOrder": 0,
		"replayq": true, "rev": true, "newq": true, "spreadNew": 1, "spreadRev": 1,
	})

	if _, err := db.Exec(`INSERT INTO col
		(id, crt, mod, scm, ver, dty, usn, ls, conf, models, decks, dconf, tags)
		VALUES (1, ?, ?, ?, 11, 0, -1, 0, ?, ?, ?, ?, '{}')`,
		now, now, now, string(conf), string(models), string(decks), string(dconf)); err != nil {
		return err
	}

	for _, c := range cards {
		flds := c.Front + "\x1f" + c.Back
		sum := sha1.Sum([]byte(flds))
		csum := int64(sum[0])<<24 | int64(sum[1])<<16 | int64(sum[2])<<8 | int64(sum[3])
		nid := ankiID()
		if _, err := db.Exec(`INSERT INTO notes
			(id, guid, mid, mod, usn, tags, flds, sfld, csum, flags, data)
			VALUES (?, ?, ?, ?, -1, '', ?, ?, ?, 0, '')`,
			nid, ankiGUID(), modelID, now, flds, c.Front, csum); err != nil {
			return err
		}
		if _, err := db.Exec(`INSERT INTO cards
			(id, nid, did, ord, mod, usn, type, queue, due, ivl, factor, reps, lapses, left, odue, odid, flags, data)
			VALUES (?, ?, 1, 0, ?, -1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, '')`,
			ankiID(), nid, now); err != nil {
			return err
		}
	}
	return nil
}

func ankiID() int64 {
	n, _ := crypto.RandomUint64()
	return int64(n & 0x7FFFFFFFFFFFFFFF)
}

func ankiGUID() string {
	return strings.ReplaceAll(NewID(), "-", "")[:8]
}

func addZipFile(zw *zip.Writer, name, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return addZipBytes(zw, name, data)
}

func addZipBytes(zw *zip.Writer, name string, data []byte) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}
