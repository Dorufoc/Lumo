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

	"lumo/internal/crypto"
	"lumo/internal/domain"
	"lumo/internal/repository"
	"lumo/internal/service/importer"
)

// ImportPreview 是导入预检结果。
type ImportPreview struct {
	BatchID      string            `json:"batch_id"`
	FileName     string            `json:"file_name"`
	Format       string            `json:"format"`
	Status       string            `json:"status"`
	TotalCount   int               `json:"total_count"`
	ValidCount   int               `json:"valid_count"`
	ErrorCount   int               `json:"error_count"`
	Errors       []ImportItemError `json:"errors"`
	PreviewItems []json.RawMessage `json:"preview_items"`
}

// ImportItemError 是单题错误。
type ImportItemError struct {
	ItemNo int    `json:"item_no"`
	Error  string `json:"error"`
}

// ImportBatch 是导入批次 DTO。
type ImportBatch struct {
	ID             string       `json:"id"`
	WorkspaceID    string       `json:"workspace_id"`
	IdempotencyKey string       `json:"idempotency_key"`
	FileName       string       `json:"file_name"`
	FileHash       string       `json:"file_hash"`
	Format         string       `json:"format"`
	Status         string       `json:"status"`
	TotalCount     int          `json:"total_count"`
	ValidCount     int          `json:"valid_count"`
	ErrorCount     int          `json:"error_count"`
	CreatedAt      string       `json:"created_at"`
	UpdatedAt      string       `json:"updated_at"`
	Items          []*ImportItem `json:"items,omitempty"`
}

// ImportItem 是导入条目 DTO。
type ImportItem struct {
	ID         string          `json:"id"`
	ItemNo     int             `json:"item_no"`
	Payload    json.RawMessage `json:"payload"`
	Status     string          `json:"status"`
	Error      *string         `json:"error,omitempty"`
	QuestionID *string         `json:"question_id,omitempty"`
}

// ImportService 实现题库导入管线。
type ImportService struct{ s *Services }

// LibraryPreflightImportReq 预检导入请求。
type LibraryPreflightImportReq struct {
	WorkspaceID    string `json:"workspace_id"`
	FilePath       string `json:"file_path"` // 相对 uploads 目录或绝对路径
	Format         string `json:"format"`    // markdown | json | text
	IdempotencyKey string `json:"idempotency_key"`
}

// LibraryPreflightImport 解析、校验并创建导入批次。
func (im *ImportService) LibraryPreflightImport(ctx context.Context, req LibraryPreflightImportReq) (*ImportPreview, error) {
	if err := im.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.Format == "" {
		req.Format = "markdown"
	}
	if req.Format != "markdown" && req.Format != "json" && req.Format != "text" {
		return nil, domain.InvalidArg("format 仅允许 markdown/json/text")
	}
	if req.IdempotencyKey == "" {
		return nil, domain.InvalidArg("idempotency_key 必填")
	}
	content, err := im.readUpload(ctx, req.FilePath)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])

	// 文件哈希幂等：同一文件重复预检返回同一批次。
	if existing, err := im.s.Repo.GetImportBatchByHash(ctx, req.WorkspaceID, hash); err != nil {
		return nil, err
	} else if existing != nil {
		return im.previewOf(ctx, req.WorkspaceID, existing.ID, false)
	}
	// 幂等键幂等。
	if existing, err := im.s.Repo.GetImportBatchByKey(ctx, req.WorkspaceID, req.IdempotencyKey); err != nil {
		return nil, err
	} else if existing != nil {
		return im.previewOf(ctx, req.WorkspaceID, existing.ID, false)
	}

	parsed, err := importer.Parse(req.Format, content)
	if err != nil {
		return nil, err
	}

	batchID := NewID()
	if err := im.s.Repo.CreateImportBatch(ctx, &repository.ImportBatchRow{
		ID: batchID, WorkspaceID: req.WorkspaceID, IdempotencyKey: req.IdempotencyKey,
		FileName: filepath.Base(req.FilePath), FileHash: hash, Format: req.Format,
		TotalCount: len(parsed),
	}); err != nil {
		return nil, err
	}

	items := make([]*repository.ImportItemRow, 0, len(parsed))
	var errorsOut []ImportItemError
	validCount := 0
	for i, pq := range parsed {
		itemNo := i + 1
		payload := pq.Payload
		if len(payload) == 0 {
			payload = json.RawMessage("{}") // 解析失败条目的占位（满足 json_valid）
		}
		item := &repository.ImportItemRow{
			ID: NewID(), BatchID: batchID, ItemNo: itemNo, Payload: payload, Status: "valid",
		}
		if pq.Error != "" {
			item.Status = "invalid"
			errText := pq.Error
			item.ErrorJSON = jsonPtr(string(mustJSON(map[string]string{"reason": errText})))
			errorsOut = append(errorsOut, ImportItemError{ItemNo: itemNo, Error: errText})
		} else if verr := im.validateImportedPayload(ctx, req.WorkspaceID, pq.Payload); verr != nil {
			item.Status = "invalid"
			errText := verr.Error()
			item.ErrorJSON = jsonPtr(string(mustJSON(map[string]string{"reason": errText})))
			errorsOut = append(errorsOut, ImportItemError{ItemNo: itemNo, Error: errText})
		} else {
			validCount++
		}
		items = append(items, item)
	}
	if err := im.s.Repo.CreateImportItems(ctx, items); err != nil {
		return nil, err
	}
	if err := im.s.Repo.SetImportBatchReady(ctx, batchID, len(parsed), validCount, len(parsed)-validCount); err != nil {
		return nil, err
	}
	im.s.audit(ctx, req.WorkspaceID, "import.preflight", "import_batch", batchID,
		map[string]any{"file": req.FilePath, "total": len(parsed), "valid": validCount})
	return im.previewOf(ctx, req.WorkspaceID, batchID, true)
}

// LibraryCommitImportReq 提交导入请求。
type LibraryCommitImportReq struct {
	WorkspaceID    string `json:"workspace_id"`
	BatchID        string `json:"batch_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

// LibraryCommitImport 将合法题目写入题库（幂等；重复题目跳过）。
func (im *ImportService) LibraryCommitImport(ctx context.Context, req LibraryCommitImportReq) (*ImportBatch, error) {
	if err := im.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.BatchID == "" || req.IdempotencyKey == "" {
		return nil, domain.InvalidArg("batch_id 与 idempotency_key 必填")
	}
	return withIdempotency(im.s, ctx, req.WorkspaceID, req.IdempotencyKey, "LibraryCommitImport", func() (*ImportBatch, error) {
		batch, err := im.s.Repo.GetImportBatch(ctx, req.WorkspaceID, req.BatchID)
		if err != nil {
			return nil, err
		}
		if batch == nil {
			return nil, domain.NotFound("导入批次不存在")
		}
		items, err := im.s.Repo.ListImportItems(ctx, batch.ID)
		if err != nil {
			return nil, err
		}
		imported := 0
		for _, item := range items {
			if item.Status != "valid" {
				continue
			}
			payload, perr := parseQuestionPayload(item.Payload)
			if perr != nil {
				_ = im.s.Repo.MarkImportItemInvalid(ctx, item.ID, perr.Error())
				continue
			}
			hash := questionContentHash(payload)
			if existing, err := im.s.Repo.GetQuestionByContentHash(ctx, req.WorkspaceID, hash); err != nil {
				return nil, err
			} else if existing != nil {
				_ = im.s.Repo.MarkImportItemInvalid(ctx, item.ID, "重复题目，已跳过")
				continue
			}
			qid, vid, err := im.createQuestionFromPayload(ctx, req.WorkspaceID, payload)
			if err != nil {
				return nil, err
			}
			if err := im.s.Repo.MarkImportItemImported(ctx, item.ID, qid); err != nil {
				return nil, err
			}
			_ = vid
			imported++
		}
		if err := im.s.Repo.SetImportBatchCommitted(ctx, batch.ID); err != nil {
			return nil, err
		}
		im.s.audit(ctx, req.WorkspaceID, "import.commit", "import_batch", batch.ID,
			map[string]any{"imported": imported})
		return im.batchOf(ctx, req.WorkspaceID, batch.ID)
	})
}

// LibraryGetImportBatchReq 获取批次请求。
type LibraryGetImportBatchReq struct {
	WorkspaceID string `json:"workspace_id"`
	BatchID     string `json:"batch_id"`
}

// LibraryGetImportBatch 获取批次与条目详情。
func (im *ImportService) LibraryGetImportBatch(ctx context.Context, req LibraryGetImportBatchReq) (*ImportBatch, error) {
	if err := im.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	return im.batchOf(ctx, req.WorkspaceID, req.BatchID)
}

// UploadedFile 是上传结果。
type UploadedFile struct {
	Path     string `json:"path"` // 相对 uploads 目录
	FileName string `json:"file_name"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}

// UploadFile 保存上传文件到 uploads 目录（安全文件名）。
func (im *ImportService) UploadFile(fileName string, content []byte) (*UploadedFile, error) {
	if len(content) == 0 {
		return nil, domain.InvalidArg("文件内容为空")
	}
	if len(content) > 32<<20 {
		return nil, domain.InvalidArg("文件超过 32MB 上限")
	}
	dir := filepath.Join(im.s.Cfg.DataDir, "uploads")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(content)
	rand, _ := crypto.RandomUint64()
	name := fmt.Sprintf("upload-%s-%x%s", safeTimestamp(), rand, filepath.Ext(sanitizeFileName(fileName)))
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return nil, err
	}
	return &UploadedFile{
		Path:     filepath.Join("uploads", name),
		FileName: sanitizeFileName(fileName),
		Size:     int64(len(content)),
		SHA256:   hex.EncodeToString(sum[:]),
	}, nil
}

// readUpload 读取上传/绝对路径文件（Path 为相对数据目录的路径）。
func (im *ImportService) readUpload(ctx context.Context, filePath string) ([]byte, error) {
	if filePath == "" {
		return nil, domain.InvalidArg("file_path 必填（先通过 LibraryUpload 上传）")
	}
	p, err := resolveLocalPath(filePath, im.s.Cfg.DataDir)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, domain.NotFound("文件不存在或不可读")
	}
	return b, nil
}

// validateImportedPayload 校验导入载荷（复用题目校验逻辑）。
func (im *ImportService) validateImportedPayload(ctx context.Context, wsID string, raw json.RawMessage) error {
	payload, err := parseQuestionPayload(raw)
	if err != nil {
		return err
	}
	return im.s.Knowledge.validatePayload(ctx, wsID, payload)
}

// createQuestionFromPayload 从载荷创建题目并返回 question/version ID。
func (im *ImportService) createQuestionFromPayload(ctx context.Context, wsID string, payload *QuestionPayload) (string, string, error) {
	qid := NewID()
	vid := NewID()
	if err := im.s.Repo.CreateQuestion(ctx, &repository.QuestionRow{
		ID: qid, WorkspaceID: wsID, Type: payload.Type,
		Source: payload.Source, Tags: mustJSON(payload.Tags), ContentHash: questionContentHash(payload),
	}); err != nil {
		return "", "", err
	}
	if err := im.s.Repo.CreateQuestionVersion(ctx, &repository.QuestionVersionRow{
		ID: vid, QuestionID: qid, VersionNo: 1, Payload: mustJSON(payload), Review: "pending",
	}); err != nil {
		return "", "", err
	}
	if len(payload.KnowledgeIDs) > 0 {
		if err := im.s.Repo.SetQuestionKnowledge(ctx, vid, payload.KnowledgeIDs); err != nil {
			return "", "", err
		}
	}
	return qid, vid, nil
}

// previewOf 构造预览。
func (im *ImportService) previewOf(ctx context.Context, wsID, batchID string, withItems bool) (*ImportPreview, error) {
	batch, err := im.s.Repo.GetImportBatch(ctx, wsID, batchID)
	if err != nil {
		return nil, err
	}
	if batch == nil {
		return nil, domain.NotFound("导入批次不存在")
	}
	items, err := im.s.Repo.ListImportItems(ctx, batch.ID)
	if err != nil {
		return nil, err
	}
	p := &ImportPreview{
		BatchID: batch.ID, FileName: batch.FileName, Format: batch.Format,
		Status: batch.Status, TotalCount: batch.TotalCount,
		ValidCount: batch.ValidCount, ErrorCount: batch.ErrorCount,
	}
	for _, it := range items {
		if it.Status == "invalid" {
			reason := ""
			if it.ErrorJSON != nil {
				var m map[string]any
				if json.Unmarshal([]byte(*it.ErrorJSON), &m) == nil {
					reason, _ = m["reason"].(string)
				}
			}
			p.Errors = append(p.Errors, ImportItemError{ItemNo: it.ItemNo, Error: reason})
		} else if withItems && len(p.PreviewItems) < 10 {
			p.PreviewItems = append(p.PreviewItems, it.Payload)
		}
	}
	if len(p.Errors) > 50 {
		p.Errors = p.Errors[:50]
	}
	return p, nil
}

// batchOf 构造批次 DTO。
func (im *ImportService) batchOf(ctx context.Context, wsID, batchID string) (*ImportBatch, error) {
	batch, err := im.s.Repo.GetImportBatch(ctx, wsID, batchID)
	if err != nil {
		return nil, err
	}
	if batch == nil {
		return nil, domain.NotFound("导入批次不存在")
	}
	items, err := im.s.Repo.ListImportItems(ctx, batch.ID)
	if err != nil {
		return nil, err
	}
	out := &ImportBatch{
		ID: batch.ID, WorkspaceID: batch.WorkspaceID, IdempotencyKey: batch.IdempotencyKey,
		FileName: batch.FileName, FileHash: batch.FileHash, Format: batch.Format,
		Status: batch.Status, TotalCount: batch.TotalCount,
		ValidCount: batch.ValidCount, ErrorCount: batch.ErrorCount,
		CreatedAt: batch.CreatedAt, UpdatedAt: batch.UpdatedAt,
	}
	for _, it := range items {
		item := &ImportItem{ID: it.ID, ItemNo: it.ItemNo, Payload: it.Payload, Status: it.Status, QuestionID: it.QuestionID}
		if it.ErrorJSON != nil {
			var m map[string]any
			if json.Unmarshal([]byte(*it.ErrorJSON), &m) == nil {
				if reason, ok := m["reason"].(string); ok {
					item.Error = &reason
				}
			}
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

func jsonPtr(s string) *string { return &s }

func sanitizeFileName(name string) string {
	name = filepath.Base(name)
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_")
	return replacer.Replace(name)
}
