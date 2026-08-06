package service

import (
	"context"
	"encoding/json"
	"strings"

	"lumo/internal/domain"
	"lumo/internal/provider"
	"lumo/internal/repository"
)

// readLaterQueueLimit 稍后读队列上限（4.15：每用户 500 条）。
const readLaterQueueLimit = 500

// summaryPromptVersion 摘要提示词版本（document_summaries.prompt_version）。
const summaryPromptVersion = "1"

// FavoritesService 收藏 / 稍后读 / 文档摘要用例（4.15 / API 7.8）。
type FavoritesService struct {
	s *Services
}

// Favorite 是收藏 DTO。
type Favorite struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	RefType   string `json:"ref_type"`
	RefID     string `json:"ref_id"`
	GroupName string `json:"group_name"`
	Note      string `json:"note"`
	Version   int    `json:"version"`
	Favorited bool   `json:"favorited"` // 当前是否处于收藏状态（toggle 结果）
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// FavoriteToggleReq 收藏切换请求（API 7.8 FavoriteToggle）。
type FavoriteToggleReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	RefType     string `json:"ref_type"`
	RefID       string `json:"ref_id"`
	GroupName   string `json:"group_name"`
	Note        string `json:"note"`
	Version     int    `json:"version"` // 乐观锁：取消收藏/改分组时校验
}

// FavoriteListReq 收藏列表请求（API 7.8 FavoriteList）。
type FavoriteListReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	GroupName   string `json:"group_name"`
	RefType     string `json:"ref_type"`
	Keyword     string `json:"keyword"`
	Cursor      string `json:"cursor"`
	Limit       int    `json:"limit"`
}

// FavoritePage 收藏分页。
type FavoritePage struct {
	Items      []*Favorite `json:"items"`
	NextCursor string      `json:"next_cursor"`
	HasMore    bool        `json:"has_more"`
}

// ReadLaterItem 是稍后读 DTO。
type ReadLaterItem struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	DocumentID  string `json:"document_id"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// ReadLaterAddReq 稍后读入队请求（API 7.8 ReadLaterAdd）。
type ReadLaterAddReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	DocumentID  string `json:"document_id"`
}

// ReadLaterTransitionReq 稍后读状态流转请求（API 7.8 ReadLaterTransition）。
type ReadLaterTransitionReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	ItemID      string `json:"item_id"`
	Action      string `json:"action"` // read | skip | requeue
}

// DocumentSummary 是文档摘要 DTO。
type DocumentSummary struct {
	ID            string          `json:"id"`
	DocumentID    string          `json:"document_id"`
	SummaryJSON   json.RawMessage `json:"summary_json"`
	Model         string          `json:"model"`
	PromptVersion *string         `json:"prompt_version"`
	Status        string          `json:"status"` // pending | ready | failed
	CreatedAt     string          `json:"created_at"`
	UpdatedAt     string          `json:"updated_at"`
}

// DocumentSummarizeReq 文档摘要请求（API 7.8 DocumentSummarize，异步）。
type DocumentSummarizeReq struct {
	WorkspaceID    string `json:"workspace_id"`
	DocumentID     string `json:"document_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

func favoriteFromRow(f *repository.FavoriteRow, favorited bool) *Favorite {
	return &Favorite{
		ID: f.ID, UserID: f.UserID, RefType: f.RefType, RefID: f.RefID,
		GroupName: f.GroupName, Note: f.Note, Version: f.Version,
		Favorited: favorited, CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt,
	}
}

func readLaterFromRow(x *repository.ReadLaterRow) *ReadLaterItem {
	return &ReadLaterItem{
		ID: x.ID, WorkspaceID: x.WorkspaceID, UserID: x.UserID,
		DocumentID: x.DocumentID, Status: x.Status,
		CreatedAt: x.CreatedAt, UpdatedAt: x.UpdatedAt,
	}
}

func summaryFromRow(s *repository.DocumentSummaryRow) *DocumentSummary {
	return &DocumentSummary{
		ID: s.ID, DocumentID: s.DocumentID,
		SummaryJSON: json.RawMessage(s.SummaryJSON), Model: s.Model,
		PromptVersion: s.PromptVersion, Status: s.Status,
		CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}
}

// FavoriteToggle 切换收藏：
// - 未收藏 → 创建（favorited=true, version=1）
// - 已收藏且版本匹配 → 若 group_name 不同则更新分组（保持收藏），否则取消收藏（favorited=false）
// - 版本不匹配 → CONFLICT
func (f *FavoritesService) FavoriteToggle(ctx context.Context, req FavoriteToggleReq) (*Favorite, error) {
	if err := f.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if !domain.ValidateFavoriteRefType(req.RefType) {
		return nil, domain.InvalidArg("ref_type 仅允许 question/document/agent_message/note")
	}
	if req.RefID == "" {
		return nil, domain.InvalidArg("ref_id 必填")
	}

	existing, err := f.s.Repo.GetFavorite(ctx, req.UserID, req.RefType, req.RefID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		row := &repository.FavoriteRow{
			ID: NewID(), WorkspaceID: req.WorkspaceID, UserID: req.UserID,
			RefType: req.RefType, RefID: req.RefID, GroupName: req.GroupName, Note: req.Note,
		}
		if err := f.s.Repo.CreateFavorite(ctx, row); err != nil {
			return nil, err
		}
		f.s.audit(ctx, req.WorkspaceID, "favorite.toggle_on", req.RefType, req.RefID, nil)
		created, err := f.s.Repo.GetFavorite(ctx, req.UserID, req.RefType, req.RefID)
		if err != nil {
			return nil, err
		}
		return favoriteFromRow(created, true), nil
	}

	if req.Version > 0 && req.Version != existing.Version {
		return nil, domain.Conflict("收藏已被修改，请刷新后重试")
	}

	// 分组变化 → 保持收藏并更新
	if req.GroupName != "" && req.GroupName != existing.GroupName {
		updated, err := f.s.Repo.UpdateFavorite(ctx, req.UserID, req.RefType, req.RefID, existing.Version,
			&repository.FavoriteRow{GroupName: req.GroupName, Note: req.Note})
		if err != nil {
			return nil, err
		}
		f.s.audit(ctx, req.WorkspaceID, "favorite.update_group", req.RefType, req.RefID,
			map[string]any{"group_name": req.GroupName})
		return favoriteFromRow(updated, true), nil
	}

	// 取消收藏
	if err := f.s.Repo.DeleteFavorite(ctx, req.UserID, req.RefType, req.RefID, existing.Version); err != nil {
		return nil, err
	}
	f.s.audit(ctx, req.WorkspaceID, "favorite.toggle_off", req.RefType, req.RefID, nil)
	return favoriteFromRow(existing, false), nil
}

// FavoriteList 分页列出收藏（newest-first）。
func (f *FavoritesService) FavoriteList(ctx context.Context, req FavoriteListReq) (*FavoritePage, error) {
	if err := f.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if req.RefType != "" && !domain.ValidateFavoriteRefType(req.RefType) {
		return nil, domain.InvalidArg("ref_type 仅允许 question/document/agent_message/note")
	}
	limit := req.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, next, hasMore, err := f.s.Repo.ListFavorites(ctx, req.UserID,
		req.GroupName, req.RefType, req.Keyword, req.Cursor, limit)
	if err != nil {
		return nil, err
	}
	items := make([]*Favorite, 0, len(rows))
	for _, r := range rows {
		items = append(items, favoriteFromRow(r, true))
	}
	return &FavoritePage{Items: items, NextCursor: next, HasMore: hasMore}, nil
}

// ReadLaterAdd 将文档加入稍后读队列（自然幂等：同用户同文档只入队一次）。
func (f *FavoritesService) ReadLaterAdd(ctx context.Context, req ReadLaterAddReq) (*ReadLaterItem, error) {
	if err := f.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if req.DocumentID == "" {
		return nil, domain.InvalidArg("document_id 必填")
	}
	doc, err := f.s.Repo.GetDocument(ctx, req.WorkspaceID, req.DocumentID)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, domain.NotFound("文档不存在")
	}

	// 自然幂等：已入队直接返回
	existing, err := f.s.Repo.GetReadLaterByDocument(ctx, req.UserID, req.DocumentID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return readLaterFromRow(existing), nil
	}

	// 队列上限 500/用户
	n, err := f.s.Repo.CountReadLater(ctx, req.UserID)
	if err != nil {
		return nil, err
	}
	if n >= readLaterQueueLimit {
		return nil, domain.QuotaExceeded("稍后读队列已满（上限 %d 条）", readLaterQueueLimit)
	}

	row := &repository.ReadLaterRow{
		ID: NewID(), WorkspaceID: req.WorkspaceID, UserID: req.UserID,
		DocumentID: req.DocumentID, Status: domain.ReadLaterStatusQueued,
	}
	if err := f.s.Repo.CreateReadLater(ctx, row); err != nil {
		return nil, err
	}
	f.s.audit(ctx, req.WorkspaceID, "read_later.add", "document", req.DocumentID, nil)
	created, err := f.s.Repo.GetReadLater(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	return readLaterFromRow(created), nil
}

// ReadLaterTransition 稍后读状态流转（read|skip|requeue，requeue 落库为 queued）。
func (f *FavoritesService) ReadLaterTransition(ctx context.Context, req ReadLaterTransitionReq) (*ReadLaterItem, error) {
	if err := f.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if req.ItemID == "" {
		return nil, domain.InvalidArg("item_id 必填")
	}
	item, err := f.s.Repo.GetReadLater(ctx, req.ItemID)
	if err != nil {
		return nil, err
	}
	if item == nil || item.UserID != req.UserID || item.WorkspaceID != req.WorkspaceID {
		return nil, domain.NotFound("稍后读条目不存在")
	}
	next, err := domain.ReadLaterNextStatus(item.Status, req.Action)
	if err != nil {
		return nil, err
	}
	updated, err := f.s.Repo.UpdateReadLaterStatus(ctx, req.ItemID, next)
	if err != nil {
		return nil, err
	}
	f.s.audit(ctx, req.WorkspaceID, "read_later."+req.Action, "read_later", req.ItemID,
		map[string]any{"status": next})
	return readLaterFromRow(updated), nil
}

// DocumentSummarize 异步生成文档摘要（幂等；LLM 未配置时降级为确定性模板，不阻塞）。
func (f *FavoritesService) DocumentSummarize(ctx context.Context, req DocumentSummarizeReq) (*DocumentSummary, error) {
	if err := f.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.DocumentID == "" {
		return nil, domain.InvalidArg("document_id 必填")
	}
	if req.IdempotencyKey == "" {
		return nil, domain.InvalidArg("idempotency_key 必填")
	}
	return withIdempotency(f.s, ctx, req.WorkspaceID, req.IdempotencyKey, "DocumentSummarize",
		func() (*DocumentSummary, error) { return f.doSummarize(ctx, req) })
}

// doSummarize 执行摘要生成并落库（withIdempotency 保证重放返回同一行）。
func (f *FavoritesService) doSummarize(ctx context.Context, req DocumentSummarizeReq) (*DocumentSummary, error) {
	doc, err := f.s.Repo.GetDocument(ctx, req.WorkspaceID, req.DocumentID)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, domain.NotFound("文档不存在")
	}

	// 收集分块正文（含小节标题，截断到 4000 字符控制成本）
	chunks, err := f.s.Repo.ListDocumentChunks(ctx, req.WorkspaceID, []string{req.DocumentID})
	if err != nil {
		return nil, err
	}
	var sb strings.Builder
	for _, c := range chunks {
		if c.Section != nil && *c.Section != "" {
			sb.WriteString("## " + *c.Section + "\n")
		}
		sb.WriteString(c.TextRef + "\n")
	}
	text := sb.String()
	if runes := []rune(text); len(runes) > 4000 {
		text = string(runes[:4000])
	}

	status := domain.SummaryStatusReady
	var pl domain.SummaryPayload
	model := ""

	llm, llmErr := f.s.Agent.LLMFactoryFunc()
	if llmErr != nil {
		// 未配置 Provider → 确定性降级模板（不阻塞、不报错）
		pl = domain.SummaryPayload{
			Points: []string{"（未配置 AI 模型）暂不能生成智能摘要，请配置模型 Provider 后重试。"},
			Note:   "degraded: provider not configured",
		}
	} else {
		res, chatErr := llm.Chat(ctx, provider.ChatRequest{
			Messages: []provider.Message{
				{Role: "system", Content: "你是文档摘要助手。请阅读文档内容，输出 JSON：{\"points\":[要点列表],\"structure\":[结构大纲],\"terms\":[关键术语列表]}。只输出 JSON，不要额外说明。"},
				{Role: "user", Content: text},
			},
			JSONMode: true, MaxTokens: 1024, Temperature: 0.2,
		}, nil)
		if chatErr != nil {
			status = domain.SummaryStatusFailed
			pl = domain.SummaryPayload{Note: "AI 生成失败: " + chatErr.Error()}
		} else if res != nil {
			model = res.Model
			if jerr := json.Unmarshal([]byte(res.Content), &pl); jerr != nil {
				status = domain.SummaryStatusFailed
				pl = domain.SummaryPayload{Note: "AI 输出无法解析为摘要 JSON"}
			}
		}
	}

	raw, _ := json.Marshal(pl)
	pv := summaryPromptVersion
	row := &repository.DocumentSummaryRow{
		ID: NewID(), DocumentID: req.DocumentID,
		SummaryJSON: string(raw), Model: model, PromptVersion: &pv, Status: status,
	}
	if err := f.s.Repo.CreateDocumentSummary(ctx, row); err != nil {
		return nil, err
	}
	f.s.audit(ctx, req.WorkspaceID, "document.summarize", "document", req.DocumentID,
		map[string]any{"status": status, "model": model})
	// SQLite 无 INSERT..RETURNING：DDL 侧默认值（created_at/updated_at）需重读才可见
	fetched, err := f.s.Repo.GetDocumentSummary(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	return summaryFromRow(fetched), nil
}
