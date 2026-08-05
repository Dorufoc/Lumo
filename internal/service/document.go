package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"

	"lumo/internal/agent"
	"lumo/internal/domain"
	"lumo/internal/provider"
	"lumo/internal/repository"
)

// Document 是资料 DTO。
type Document struct {
	ID            string  `json:"id"`
	WorkspaceID   string  `json:"workspace_id"`
	FileName      string  `json:"file_name"`
	MimeType      string  `json:"mime_type"`
	ByteSize      int64   `json:"byte_size"`
	SHA256        string  `json:"sha256"`
	Status        string  `json:"status"`
	FailureReason *string `json:"failure_reason"`
	Version       int     `json:"version"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

// DocumentPage 是文档分页。
type DocumentPage struct {
	Items      []*Document `json:"items"`
	NextCursor string      `json:"next_cursor"`
	HasMore    bool        `json:"has_more"`
}

// ChunkHit 是检索命中。
type ChunkHit struct {
	ChunkID    string  `json:"chunk_id"`
	DocumentID string  `json:"document_id"`
	FileName   string  `json:"file_name"`
	Section    *string `json:"section"`
	Text       string  `json:"text"`
	Score      float64 `json:"score"`
}

// AgentRequestDTO 复用 agent.AgentRequest。
type AgentRequestDTO = agent.AgentRequest

// agentEmit 复用 agent.Emit。
type agentEmit = agent.Emit

// agentRow 将会话 DTO 转回仓储行。
func agentRow(s *agent.AgentSession) *repository.AgentSessionRow {
	return &repository.AgentSessionRow{
		ID: s.ID, WorkspaceID: s.WorkspaceID, UserID: s.UserID,
		Agent: s.Agent, Status: s.Status, RequestID: s.RequestID,
	}
}

// DocumentService 实现资料导入与 RAG 检索。
type DocumentService struct{ s *Services }

// DocumentImportReq 导入资料请求。
type DocumentImportReq struct {
	WorkspaceID    string `json:"workspace_id"`
	FilePath       string `json:"file_path"` // LibraryUpload 返回的相对路径
	IdempotencyKey string `json:"idempotency_key"`
}

// DocumentImport 导入并索引文档：哈希去重 → 解析 → 分块 → 嵌入 → indexed。
func (d *DocumentService) DocumentImport(ctx context.Context, req DocumentImportReq) (*Document, error) {
	if err := d.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.FilePath == "" || req.IdempotencyKey == "" {
		return nil, domain.InvalidArg("file_path 与 idempotency_key 必填")
	}
	return withIdempotency(d.s, ctx, req.WorkspaceID, req.IdempotencyKey, "DocumentImport", func() (*Document, error) {
		p, err := resolveLocalPath(req.FilePath, d.s.Cfg.DataDir)
		if err != nil {
			return nil, err
		}
		content, err := os.ReadFile(p)
		if err != nil {
			return nil, domain.NotFound("文件不存在或不可读")
		}
		if len(content) > 50<<20 {
			return nil, domain.InvalidArg("文档超过 50MB 上限")
		}
		sum := sha256.Sum256(content)
		hash := hex.EncodeToString(sum[:])
		if existing, err := d.s.Repo.GetDocumentByHash(ctx, req.WorkspaceID, hash); err != nil {
			return nil, err
		} else if existing != nil {
			return nil, domain.Conflict("相同内容的文档已导入")
		}

		fileName := sanitizeFileName(filepath.Base(req.FilePath))
		mime := mimeOf(fileName)
		text, ok := extractText(mime, content)
		if !ok {
			return nil, domain.InvalidArg("暂不支持该文件类型（支持 Markdown/TXT）")
		}
		relPath, err := d.copyToAttachments(req.WorkspaceID, fileName, content)
		if err != nil {
			return nil, err
		}
		doc := &repository.DocumentRow{
			ID: NewID(), WorkspaceID: req.WorkspaceID, RelativePath: relPath,
			FileName: fileName, MimeType: mime, ByteSize: int64(len(content)), SHA256: hash,
		}
		if err := d.s.Repo.CreateDocument(ctx, doc); err != nil {
			return nil, err
		}
		if err := d.indexDocument(ctx, doc, text); err != nil {
			reason := err.Error()
			_ = d.s.Repo.UpdateDocumentStatus(ctx, doc.ID, "failed", &reason)
			return d.docByID(ctx, req.WorkspaceID, doc.ID)
		}
		_ = d.s.Repo.UpdateDocumentStatus(ctx, doc.ID, "indexed", nil)
		d.s.audit(ctx, req.WorkspaceID, "document.import", "document", doc.ID,
			map[string]any{"file": fileName, "size": len(content)})
		return d.docByID(ctx, req.WorkspaceID, doc.ID)
	})
}

// DocumentListReq 文档列表请求。
type DocumentListReq struct {
	WorkspaceID string `json:"workspace_id"`
	Status      string `json:"status"`
	Cursor      string `json:"cursor"`
	Limit       int    `json:"limit"`
}

// DocumentList 分页列出文档。
func (d *DocumentService) DocumentList(ctx context.Context, req DocumentListReq) (*DocumentPage, error) {
	if err := d.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	rows, next, hasMore, err := d.s.Repo.ListDocuments(ctx, req.WorkspaceID, req.Status, req.Cursor, req.Limit)
	if err != nil {
		return nil, err
	}
	items := make([]*Document, 0, len(rows))
	for _, r := range rows {
		items = append(items, docFromRow(r))
	}
	return &DocumentPage{Items: items, NextCursor: next, HasMore: hasMore}, nil
}

// DocumentRetryReq 重试导入请求。
type DocumentRetryReq struct {
	WorkspaceID    string `json:"workspace_id"`
	DocumentID     string `json:"document_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

// DocumentRetry 重新解析索引失败文档。
func (d *DocumentService) DocumentRetry(ctx context.Context, req DocumentRetryReq) (*Document, error) {
	if err := d.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	return withIdempotency(d.s, ctx, req.WorkspaceID, req.IdempotencyKey, "DocumentRetry", func() (*Document, error) {
		doc, err := d.s.Repo.GetDocument(ctx, req.WorkspaceID, req.DocumentID)
		if err != nil {
			return nil, err
		}
		if doc == nil {
			return nil, domain.NotFound("文档不存在")
		}
		content, err := os.ReadFile(filepath.Join(d.s.Cfg.DataDir, doc.RelativePath))
		if err != nil {
			return nil, domain.NotFound("文档附件缺失")
		}
		text, _ := extractText(doc.MimeType, content)
		if err := d.indexDocument(ctx, doc, text); err != nil {
			reason := err.Error()
			_ = d.s.Repo.UpdateDocumentStatus(ctx, doc.ID, "failed", &reason)
			return d.docByID(ctx, req.WorkspaceID, doc.ID)
		}
		_ = d.s.Repo.UpdateDocumentStatus(ctx, doc.ID, "indexed", nil)
		return d.docByID(ctx, req.WorkspaceID, doc.ID)
	})
}

// DocumentDeleteReq 删除文档请求。
type DocumentDeleteReq struct {
	WorkspaceID string `json:"workspace_id"`
	DocumentID  string `json:"document_id"`
	Version     int    `json:"version"`
}

// DocumentDelete 软删除文档（分块由索引清理）。
func (d *DocumentService) DocumentDelete(ctx context.Context, req DocumentDeleteReq) (*DeleteResult, error) {
	if err := d.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if err := d.s.Repo.SoftDeleteDocument(ctx, req.WorkspaceID, req.DocumentID, req.Version); err != nil {
		return nil, err
	}
	if err := d.s.Repo.DeleteDocumentFTS(ctx, req.DocumentID); err != nil {
		return nil, err
	}
	d.s.audit(ctx, req.WorkspaceID, "document.delete", "document", req.DocumentID, nil)
	return &DeleteResult{Deleted: true, DeletedAt: Now()}, nil
}

// RAGAskReq 资料问答请求。
type RAGAskReq struct {
	WorkspaceID string   `json:"workspace_id"`
	UserID      string   `json:"user_id"`
	Question    string   `json:"question"`
	DocumentIDs []string `json:"document_ids"`
	RequestID   string   `json:"request_id"`
}

// RAGAsk 检索资料并流式回答（事件经 SSE 推送，含引用）。
func (d *DocumentService) RAGAsk(ctx context.Context, req RAGAskReq) (*AgentRequestDTO, error) {
	if err := d.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Question) == "" {
		return nil, domain.InvalidArg("question 不能为空")
	}
	hits, err := d.retrieve(ctx, req.WorkspaceID, req.Question, req.DocumentIDs, 6)
	if err != nil {
		return nil, err
	}
	// 无证据：直接降级提示
	if len(hits) == 0 {
		return d.answerNoEvidence(ctx, req)
	}

	// 组装片段上下文（带引用编号）
	var sb strings.Builder
	citations := make([]map[string]any, 0, len(hits))
	for i, h := range hits {
		sb.WriteString("\n[" + itoaStr(i+1) + "] 《" + h.FileName + "》")
		if h.Section != nil {
			sb.WriteString("（" + *h.Section + "）")
		}
		sb.WriteString("：\n" + h.Text + "\n")
		citations = append(citations, map[string]any{
			"document_id": h.DocumentID, "document_name": h.FileName,
			"section": h.Section, "snippet": truncateRunes(h.Text, 120),
		})
	}

	// 创建 librarian 会话并启动流式回答
	session, err := d.s.Agent.AgentChatCreate(ctx, agent.AgentChatCreateReq{
		WorkspaceID: req.WorkspaceID, UserID: req.UserID, Agent: "librarian",
		Context: "资料问答",
	})
	if err != nil {
		return nil, err
	}
	rid := req.RequestID
	if rid == "" {
		rid = NewID()
	}
	_ = d.s.Agent.AppendUserMessage(ctx, session.ID, req.Question)
	prompt := "资料片段（仅可引用这些片段回答）：\n" + sb.String() + "\n\n用户问题：" + req.Question

	go d.s.Agent.RunStream(ctx, agentRow(session), rid, func(ctx context.Context, emit agentEmit) error {
		// 先推送引用，再流式回答
		emit("agent:delta", map[string]any{"delta": "", "citations": citations})
		return d.answerWithLLM(ctx, req, prompt, citations, emit)
	})
	return &AgentRequestDTO{RequestID: rid, SessionID: session.ID}, nil
}

// answerWithLLM 用 Librarian 提示词回答（未配置降级为片段摘要模板）。
func (d *DocumentService) answerWithLLM(ctx context.Context, req RAGAskReq, prompt string, citations []map[string]any, emit agentEmit) error {
	llm, err := d.s.Agent.LLMFactoryFunc()
	if err != nil {
		// 降级：列出证据并引导
		text := "（未配置 AI 模型）\n\n基于检索到的资料片段，我暂不能生成完整回答。以下是与问题相关的片段位置，请查阅：\n"
		for i, c := range citations {
			text += itoaStr(i+1) + ". " + c["document_name"].(string)
			if c["section"] != nil {
				text += "（" + c["section"].(string) + "）"
			}
			text += "\n"
		}
		text += "\n配置模型 Provider 后，我可以基于这些片段为你总结答案。"
		emit("agent:delta", map[string]any{"delta": text, "citations": citations})
		emit("agent:completed", map[string]any{"message_id": NewID(), "usage": map[string]any{}, "citations": citations})
		return nil
	}
	system := `你是 Lumo AI 的资料问答助手。只依据提供的资料片段回答，并标注引用编号如 [1]。片段中无证据时明确说明"资料中未找到相关依据"，绝不编造。使用简体中文。`
	_, err = llm.Chat(ctx, provider.ChatRequest{
		Messages: []provider.Message{
			{Role: "system", Content: system},
			{Role: "user", Content: prompt},
		}, MaxTokens: 1024, Temperature: 0.3,
	}, func(delta string) {
		emit("agent:delta", map[string]any{"delta": delta, "citations": citations})
	})
	if err != nil {
		return err
	}
	emit("agent:completed", map[string]any{"message_id": NewID(), "usage": map[string]any{}, "citations": citations})
	return nil
}

// answerNoEvidence 无证据时的明确提示。
func (d *DocumentService) answerNoEvidence(ctx context.Context, req RAGAskReq) (*AgentRequestDTO, error) {
	session, err := d.s.Agent.AgentChatCreate(ctx, agent.AgentChatCreateReq{
		WorkspaceID: req.WorkspaceID, UserID: req.UserID, Agent: "librarian", Context: "资料问答",
	})
	if err != nil {
		return nil, err
	}
	rid := req.RequestID
	if rid == "" {
		rid = NewID()
	}
	go d.s.Agent.RunStream(ctx, agentRow(session), rid, func(ctx context.Context, emit agentEmit) error {
		text := "当前资料中未找到与问题相关的依据。请补充资料或换一种问法；我不会编造来源。"
		emit("agent:delta", map[string]any{"delta": text, "citations": []any{}})
		emit("agent:completed", map[string]any{"message_id": NewID(), "usage": map[string]any{}, "citations": []any{}})
		return nil
	})
	return &AgentRequestDTO{RequestID: rid, SessionID: session.ID}, nil
}

// retrieve 混合检索：关键词打分 + 向量余弦（未配置 embedding 时仅关键词）。
func (d *DocumentService) retrieve(ctx context.Context, wsID, question string, docIDs []string, limit int) ([]ChunkHit, error) {
	chunks, err := d.s.Repo.ListDocumentChunks(ctx, wsID, docIDs)
	if err != nil {
		return nil, err
	}
	if len(chunks) == 0 {
		return nil, nil
	}
	qTokens := tokenize(question)
	type scored struct {
		hit  ChunkHit
		text string
	}
	var results []scored
	for _, c := range chunks {
		if c.TextRef == "" {
			continue
		}
		score := scoreText(qTokens, tokenize(c.TextRef))
		if score <= 0 {
			continue
		}
		results = append(results, scored{
			hit: ChunkHit{
				ChunkID: c.ID, DocumentID: c.DocID, Text: c.TextRef,
				Section: c.Section, Score: score,
			},
			text: c.TextRef,
		})
	}
	// 向量融合（配置了 embedding 时）
	if emb, err := d.embedding(); err == nil {
		vecHits, err := d.vectorHits(ctx, emb, chunks, qTokens)
		if err == nil {
			// 归一化合并：vector 分数映射到 0.5-1.0 后与关键词取 max
			for _, vh := range vecHits {
				found := false
				for i := range results {
					if results[i].hit.ChunkID == vh.ChunkID {
						fused := math.Max(results[i].hit.Score, 0.5+vh.Score*0.5)
						results[i].hit.Score = fused
						found = true
						break
					}
				}
				if !found {
					results = append(results, scored{hit: vh})
				}
			}
		}
	}
	// 排序取 top
	for i := 1; i < len(results); i++ {
		for j := i; j > 0 && results[j].hit.Score > results[j-1].hit.Score; j-- {
			results[j], results[j-1] = results[j-1], results[j]
		}
	}
	if len(results) > limit {
		results = results[:limit]
	}
	out := make([]ChunkHit, 0, len(results))
	for _, r := range results {
		out = append(out, r.hit)
	}
	return out, nil
}

// vectorHits 向量余弦检索。
func (d *DocumentService) vectorHits(ctx context.Context, emb provider.EmbeddingProvider, chunks []*repository.ChunkRow, qTokens []string) ([]ChunkHit, error) {
	// 收集带向量的 chunk
	type vec struct {
		row   *repository.ChunkRow
		embed []float64
	}
	var vecs []vec
	for _, c := range chunks {
		if c.Vector == nil {
			continue
		}
		var v []float64
		if err := json.Unmarshal([]byte(*c.Vector), &v); err != nil || len(v) == 0 {
			continue
		}
		vecs = append(vecs, vec{row: c, embed: v})
	}
	if len(vecs) == 0 {
		return nil, nil
	}
	q := strings.Join(qTokens, " ")
	qv, err := emb.Embed(ctx, []string{q})
	if err != nil || len(qv) == 0 {
		return nil, err
	}
	var out []ChunkHit
	for _, v := range vecs {
		cos := cosine(qv[0], v.embed)
		if cos < 0.3 {
			continue
		}
		out = append(out, ChunkHit{
			ChunkID: v.row.ID, DocumentID: v.row.DocID, Text: v.row.TextRef,
			Section: v.row.Section, Score: cos,
		})
	}
	return out, nil
}

func cosine(a, b []float64) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	dot, na, nb := 0.0, 0.0, 0.0
	for i := 0; i < n; i++ {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// embedding 构造 Embedding Provider（未配置返回 ErrNotConfigured）。
func (d *DocumentService) embedding() (provider.EmbeddingProvider, error) {
	if d.s.EmbeddingFactory == nil {
		return nil, provider.ErrNotConfigured
	}
	return d.s.EmbeddingFactory()
}

// indexDocument 分块 + 嵌入 + 写索引。
func (d *DocumentService) indexDocument(ctx context.Context, doc *repository.DocumentRow, text string) error {
	chunks := chunkText(text, 800, 80)
	rows := make([]*repository.DocumentChunkRow, 0, len(chunks))
	for _, c := range chunks {
		row := &repository.DocumentChunkRow{
			ID: NewID(), DocumentID: doc.ID, TextRef: c.Text,
			SectionName: c.Section, ParagraphNo: intPtr(c.Paragraph),
			StartOffset: c.StartOff, EndOffset: c.EndOff,
			EmbeddingVersion: "v1",
		}
		rows = append(rows, row)
	}
	// 向量化（未配置 embedding 时跳过，关键词检索可用）
	if emb, err := d.embedding(); err == nil {
		texts := make([]string, len(rows))
		for i, r := range rows {
			texts[i] = r.TextRef
		}
		vecs, err := emb.Embed(ctx, texts)
		if err == nil {
			for i, r := range rows {
				if i < len(vecs) {
					b, _ := json.Marshal(vecs[i])
					s := string(b)
					r.VectorRef = &s
				}
			}
		}
	}
	return d.s.Repo.ReplaceDocumentIndex(ctx, doc.ID, doc.WorkspaceID, rows)
}

// copyToAttachments 复制附件到工作区目录。
func (d *DocumentService) copyToAttachments(wsID, fileName string, content []byte) (string, error) {
	dir := filepath.Join(d.s.Cfg.AttachmentsDir, wsID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	rel := filepath.Join("attachments", wsID, fileName)
	if err := os.WriteFile(filepath.Join(d.s.Cfg.DataDir, rel), content, 0o600); err != nil {
		return "", err
	}
	return rel, nil
}

func (d *DocumentService) docByID(ctx context.Context, wsID, id string) (*Document, error) {
	row, err := d.s.Repo.GetDocument(ctx, wsID, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, domain.NotFound("文档不存在")
	}
	return docFromRow(row), nil
}

func docFromRow(r *repository.DocumentRow) *Document {
	return &Document{
		ID: r.ID, WorkspaceID: r.WorkspaceID, FileName: r.FileName, MimeType: r.MimeType,
		ByteSize: r.ByteSize, SHA256: r.SHA256, Status: r.Status,
		FailureReason: r.FailureReason, Version: r.Version,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func mimeOf(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".md"), strings.HasSuffix(lower, ".markdown"):
		return "text/markdown"
	case strings.HasSuffix(lower, ".txt"), strings.HasSuffix(lower, ".text"):
		return "text/plain"
	default:
		return "application/octet-stream"
	}
}

func intPtr(v int) *int { return &v }

func itoaStr(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
