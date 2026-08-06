package service

import (
	"context"
	"encoding/json"
	"strings"

	"lumo/internal/domain"
	"lumo/internal/repository"
)

// Note 是笔记 DTO（4.8 数据模型）。
type Note struct {
	ID           string   `json:"id"`
	WorkspaceID  string   `json:"workspace_id"`
	UserID       string   `json:"user_id"`
	Kind         string   `json:"kind"`
	Title        string   `json:"title"`
	BodyMD       string   `json:"body_md"`
	SourceRef    string   `json:"source_ref,omitempty"`
	KnowledgeIDs []string `json:"knowledge_ids"`
	Tags         []string `json:"tags"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
	Version      int      `json:"version"`
}

// NotePage 是笔记分页。
type NotePage struct {
	Items      []*Note `json:"items"`
	NextCursor string  `json:"next_cursor"`
	HasMore    bool    `json:"has_more"`
}

// Annotation 是标注 DTO。
type Annotation struct {
	ID             string `json:"id"`
	NoteID         string `json:"note_id"`
	DocumentID     string `json:"document_id,omitempty"`
	AnchorHash     string `json:"anchor_hash"`
	OffsetStart    int    `json:"offset_start"`
	OffsetEnd      int    `json:"offset_end"`
	HighlightColor string `json:"highlight_color"`
	CreatedAt      string `json:"created_at"`
}

// NoteService 实现笔记与标注用例（API 文档 7.1）。
type NoteService struct{ s *Services }

// NoteCreateReq 创建笔记请求。
type NoteCreateReq struct {
	WorkspaceID  string   `json:"workspace_id"`
	UserID       string   `json:"user_id"`
	Kind         string   `json:"kind"` // question | document | agent | free（默认 free）
	Title        string   `json:"title"`
	BodyMD       string   `json:"body_md"`
	SourceRef    string   `json:"source_ref,omitempty"`
	KnowledgeIDs []string `json:"knowledge_ids,omitempty"`
	Tags         []string `json:"tags,omitempty"`
}

// NoteUpdateReq 更新笔记请求（乐观锁：version 不匹配 → CONFLICT）。
type NoteUpdateReq struct {
	WorkspaceID  string   `json:"workspace_id"`
	NoteID       string   `json:"note_id"`
	Version      int      `json:"version"`
	Kind         string   `json:"kind"`
	Title        string   `json:"title"`
	BodyMD       string   `json:"body_md"`
	SourceRef    string   `json:"source_ref,omitempty"`
	KnowledgeIDs []string `json:"knowledge_ids,omitempty"`
	Tags         []string `json:"tags,omitempty"`
}

// NoteListReq 笔记列表请求（keyword 非空时走 FTS5 全文检索）。
type NoteListReq struct {
	WorkspaceID string `json:"workspace_id"`
	Kind        string `json:"kind"`
	KnowledgeID string `json:"knowledge_id"`
	Tag         string `json:"tag"`
	Keyword     string `json:"keyword"`
	Cursor      string `json:"cursor"`
	Limit       int    `json:"limit"`
}

// NoteDeleteReq 删除笔记请求（乐观锁软删除）。
type NoteDeleteReq struct {
	WorkspaceID string `json:"workspace_id"`
	NoteID      string `json:"note_id"`
	Version     int    `json:"version"`
}

// NoteToFlashcardReq 笔记转闪卡请求（复用闪卡模块 FlashcardGenerate）。
type NoteToFlashcardReq struct {
	WorkspaceID    string `json:"workspace_id"`
	UserID         string `json:"user_id"`
	NoteID         string `json:"note_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

// AnnotationCreateReq 创建标注请求。
type AnnotationCreateReq struct {
	WorkspaceID    string `json:"workspace_id"`
	NoteID         string `json:"note_id"`
	DocumentID     string `json:"document_id,omitempty"`
	AnchorHash     string `json:"anchor_hash"`
	OffsetStart    int    `json:"offset_start"`
	OffsetEnd      int    `json:"offset_end"`
	HighlightColor string `json:"highlight_color"`
}

func noteFromRow(n *repository.NoteRow) *Note {
	d := &Note{
		ID: n.ID, WorkspaceID: n.WorkspaceID, UserID: n.UserID, Kind: n.Kind,
		Title: n.Title, BodyMD: n.BodyMD,
		CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt, Version: n.Version,
	}
	if n.SourceRef != nil {
		d.SourceRef = *n.SourceRef
	}
	_ = json.Unmarshal([]byte(n.KnowledgeIDs), &d.KnowledgeIDs)
	_ = json.Unmarshal([]byte(n.Tags), &d.Tags)
	return d
}

// normalizeNoteKind 归一化笔记类型：空 → free；非法 → INVALID_ARGUMENT。
func normalizeNoteKind(kind string) (string, error) {
	if kind == "" {
		kind = domain.NoteKindFree
	}
	if !domain.ValidateNoteKind(kind) {
		return "", domain.InvalidArg("kind 仅允许 question/document/agent/free")
	}
	return kind, nil
}

// validateNoteTitle 校验笔记标题。
func validateNoteTitle(title string) error {
	if strings.TrimSpace(title) == "" {
		return domain.InvalidArg("title 不能为空")
	}
	if len([]rune(title)) > 200 {
		return domain.InvalidArg("title 过长（上限 200 字符）")
	}
	return nil
}

// NoteCreate 创建笔记（notes_fts 由触发器自动同步）。
func (n *NoteService) NoteCreate(ctx context.Context, req NoteCreateReq) (*Note, error) {
	if err := n.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	kind, err := normalizeNoteKind(req.Kind)
	if err != nil {
		return nil, err
	}
	if err := validateNoteTitle(req.Title); err != nil {
		return nil, err
	}
	var sourceRef *string
	if req.SourceRef != "" {
		sourceRef = &req.SourceRef
	}
	row := &repository.NoteRow{
		ID: NewID(), WorkspaceID: req.WorkspaceID, UserID: req.UserID, Kind: kind,
		Title: req.Title, BodyMD: req.BodyMD, SourceRef: sourceRef,
		KnowledgeIDs: repository.MarshalJSON(req.KnowledgeIDs),
		Tags:         repository.MarshalJSON(req.Tags),
	}
	if err := n.s.Repo.CreateNote(ctx, row); err != nil {
		return nil, err
	}
	created, err := n.s.Repo.GetNote(ctx, req.WorkspaceID, row.ID)
	if err != nil {
		return nil, err
	}
	n.s.audit(ctx, req.WorkspaceID, "note.create", "note", row.ID,
		map[string]any{"kind": kind})
	return noteFromRow(created), nil
}

// NoteUpdate 乐观锁更新笔记可编辑字段。
func (n *NoteService) NoteUpdate(ctx context.Context, req NoteUpdateReq) (*Note, error) {
	if err := n.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.NoteID == "" || req.Version <= 0 {
		return nil, domain.InvalidArg("note_id 与 version 必填")
	}
	kind, err := normalizeNoteKind(req.Kind)
	if err != nil {
		return nil, err
	}
	if err := validateNoteTitle(req.Title); err != nil {
		return nil, err
	}
	var sourceRef *string
	if req.SourceRef != "" {
		sourceRef = &req.SourceRef
	}
	row := &repository.NoteRow{
		Kind: kind, Title: req.Title, BodyMD: req.BodyMD, SourceRef: sourceRef,
		KnowledgeIDs: repository.MarshalJSON(req.KnowledgeIDs),
		Tags:         repository.MarshalJSON(req.Tags),
	}
	updated, err := n.s.Repo.UpdateNote(ctx, req.WorkspaceID, req.NoteID, req.Version, row)
	if err != nil {
		return nil, err
	}
	n.s.audit(ctx, req.WorkspaceID, "note.update", "note", req.NoteID,
		map[string]any{"version": req.Version})
	return noteFromRow(updated), nil
}

// NoteList 分页列出笔记；keyword 非空时经 SearchFTS（自动 SpaceCJK）全文检索。
func (n *NoteService) NoteList(ctx context.Context, req NoteListReq) (*NotePage, error) {
	if err := n.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.Kind != "" {
		if _, err := normalizeNoteKind(req.Kind); err != nil {
			return nil, err
		}
	}
	limit := req.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if strings.TrimSpace(req.Keyword) != "" {
		hits, err := n.s.Repo.SearchFTS(ctx, repository.FTSTableNotes, req.WorkspaceID, req.Keyword, limit)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(hits))
		for _, h := range hits {
			ids = append(ids, h.BusinessID)
		}
		rows, err := n.s.Repo.GetNotesByIDs(ctx, req.WorkspaceID, ids)
		if err != nil {
			return nil, err
		}
		items := make([]*Note, 0, len(rows))
		for _, r := range rows {
			items = append(items, noteFromRow(r))
		}
		return &NotePage{Items: items}, nil
	}
	rows, next, hasMore, err := n.s.Repo.ListNotes(ctx, req.WorkspaceID, req.Kind,
		req.KnowledgeID, req.Tag, req.Cursor, limit)
	if err != nil {
		return nil, err
	}
	items := make([]*Note, 0, len(rows))
	for _, r := range rows {
		items = append(items, noteFromRow(r))
	}
	return &NotePage{Items: items, NextCursor: next, HasMore: hasMore}, nil
}

// NoteDelete 乐观锁软删除笔记。
func (n *NoteService) NoteDelete(ctx context.Context, req NoteDeleteReq) (*DeleteResult, error) {
	if err := n.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.NoteID == "" || req.Version <= 0 {
		return nil, domain.InvalidArg("note_id 与 version 必填")
	}
	deletedAt, err := n.s.Repo.SoftDeleteNote(ctx, req.WorkspaceID, req.NoteID, req.Version)
	if err != nil {
		return nil, err
	}
	n.s.audit(ctx, req.WorkspaceID, "note.delete", "note", req.NoteID,
		map[string]any{"version": req.Version})
	return &DeleteResult{Deleted: true, DeletedAt: deletedAt}, nil
}

// NoteToFlashcard 复用闪卡模块：FlashcardGenerate(source_ref=note_id) →
// 内部经 GetNoteForFlashcard → buildFromNote（front=标题，back=正文，source=note）。
// 幂等与同源去重（withIdempotency + GetFlashcardBySource）由闪卡模块保证，不在此重复。
func (n *NoteService) NoteToFlashcard(ctx context.Context, req NoteToFlashcardReq) (*Flashcard, error) {
	if err := n.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if req.NoteID == "" {
		return nil, domain.InvalidArg("note_id 必填")
	}
	if req.IdempotencyKey == "" {
		return nil, domain.InvalidArg("idempotency_key 必填")
	}
	cards, err := n.s.Flashcard.FlashcardGenerate(ctx, FlashcardGenerateReq{
		WorkspaceID: req.WorkspaceID, UserID: req.UserID,
		SourceRef: req.NoteID, IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		return nil, err
	}
	if len(cards) == 0 {
		return nil, domain.NotFound("生成闪卡失败")
	}
	n.s.audit(ctx, req.WorkspaceID, "note.to_flashcard", "note", req.NoteID, nil)
	return cards[0], nil
}

// AnnotationCreate 创建资料标注（高亮锚点基于文档稳定哈希）。
func (n *NoteService) AnnotationCreate(ctx context.Context, req AnnotationCreateReq) (*Annotation, error) {
	if err := n.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.NoteID == "" || strings.TrimSpace(req.AnchorHash) == "" {
		return nil, domain.InvalidArg("note_id 与 anchor_hash 必填")
	}
	if req.OffsetStart < 0 || req.OffsetEnd < req.OffsetStart {
		return nil, domain.InvalidArg("偏移非法：offset_start >= 0 且 offset_end >= offset_start")
	}
	note, err := n.s.Repo.GetNote(ctx, req.WorkspaceID, req.NoteID)
	if err != nil {
		return nil, err
	}
	if note == nil || note.DeletedAt != nil {
		return nil, domain.NotFound("笔记不存在或已被删除")
	}
	var docID *string
	if req.DocumentID != "" {
		docID = &req.DocumentID
	}
	a := &repository.AnnotationRow{
		ID: NewID(), NoteID: req.NoteID, DocumentID: docID,
		AnchorHash: req.AnchorHash, OffsetStart: req.OffsetStart, OffsetEnd: req.OffsetEnd,
		HighlightColor: req.HighlightColor, CreatedAt: Now(),
	}
	if err := n.s.Repo.CreateAnnotation(ctx, a); err != nil {
		return nil, err
	}
	n.s.audit(ctx, req.WorkspaceID, "annotation.create", "annotation", a.ID,
		map[string]any{"note_id": req.NoteID})
	out := &Annotation{
		ID: a.ID, NoteID: a.NoteID, AnchorHash: a.AnchorHash,
		OffsetStart: a.OffsetStart, OffsetEnd: a.OffsetEnd, HighlightColor: a.HighlightColor,
		CreatedAt: a.CreatedAt,
	}
	if a.DocumentID != nil {
		out.DocumentID = *a.DocumentID
	}
	return out, nil
}
