package service

// Todo 9 笔记模块服务层测试：NoteCreate 校验、NoteUpdate 乐观锁、NoteList FTS5 CJK 关键词检索、
// NoteDelete 软删除、NoteToFlashcard 复用闪卡模块 + 幂等重放、AnnotationCreate 校验。

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"lumo/internal/domain"
)

// helperNote 经服务层创建一条自由笔记。
func helperNote(t *testing.T, s *Services, ws *Workspace, userID, title, body string) *Note {
	t.Helper()
	n, err := s.Note.NoteCreate(context.Background(), NoteCreateReq{
		WorkspaceID: ws.ID, UserID: userID, Kind: "free", Title: title, BodyMD: body,
	})
	if err != nil {
		t.Fatalf("create note: %v", err)
	}
	return n
}

// TestNoteCreateValidation：创建成功时字段完整；空标题 / 非法 kind / 缺 user_id → INVALID_ARGUMENT。
func TestNoteCreateValidation(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)

	n, err := s.Note.NoteCreate(ctx, NoteCreateReq{
		WorkspaceID: ws.ID, UserID: userID, Kind: "question",
		Title: "量子力学复习", BodyMD: "薛定谔方程描述量子态的时间演化。",
		KnowledgeIDs: []string{"k-1"}, Tags: []string{"物理"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if n.ID == "" || n.WorkspaceID != ws.ID || n.UserID != userID || n.Kind != "question" {
		t.Fatalf("bad note fields: %+v", n)
	}
	if n.Version != 1 || n.CreatedAt == "" || n.UpdatedAt == "" {
		t.Fatalf("fresh note meta wrong: %+v", n)
	}
	if len(n.KnowledgeIDs) != 1 || n.KnowledgeIDs[0] != "k-1" || len(n.Tags) != 1 || n.Tags[0] != "物理" {
		t.Fatalf("json arrays round-trip wrong: %+v", n)
	}

	cases := []NoteCreateReq{
		{WorkspaceID: ws.ID, UserID: userID, Kind: "free", Title: "", BodyMD: "b"},            // 空标题
		{WorkspaceID: ws.ID, UserID: userID, Kind: "video", Title: "t", BodyMD: "b"},          // 非法 kind
		{WorkspaceID: ws.ID, Kind: "free", Title: "t", BodyMD: "b"},                           // 缺 user_id
		{WorkspaceID: ws.ID, UserID: userID, Kind: "free", Title: strings.Repeat("长", 300)},  // 超长标题
	}
	for i, req := range cases {
		if _, err := s.Note.NoteCreate(ctx, req); err == nil {
			t.Fatalf("case %d: expected error, got nil", i)
		} else if domain.AsError(err).Code != domain.CodeInvalidArgument {
			t.Fatalf("case %d: want INVALID_ARGUMENT, got %s", i, domain.AsError(err).Code)
		}
	}
}

// TestNoteEmptyTagsContract：无标签/无知识点笔记的 tags/knowledge_ids 必须是空切片而非 null（避免前端 .length 白屏）。
func TestNoteEmptyTagsContract(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)

	n := helperNote(t, s, ws, userID, "无标签笔记", "b")
	if n.Tags == nil {
		t.Fatal("tags should be non-nil empty slice, not null")
	}
	if n.KnowledgeIDs == nil {
		t.Fatal("knowledge_ids should be non-nil empty slice, not null")
	}
	if len(n.Tags) != 0 || len(n.KnowledgeIDs) != 0 {
		t.Fatalf("expected empty tags/knowledge_ids, got %+v / %+v", n.Tags, n.KnowledgeIDs)
	}
	raw, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"tags":[]`)) {
		t.Fatalf("tags should marshal to [] not null: %s", raw)
	}
	if !bytes.Contains(raw, []byte(`"knowledge_ids":[]`)) {
		t.Fatalf("knowledge_ids should marshal to [] not null: %s", raw)
	}

	// 列表路径同样保证空切片。
	page, err := s.Note.NoteList(ctx, NoteListReq{WorkspaceID: ws.ID, Limit: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if page.Items[0].Tags == nil || page.Items[0].KnowledgeIDs == nil {
		t.Fatalf("listed note arrays should be non-nil, got %+v", page.Items[0])
	}
}

// TestNoteUpdateOptimisticLock：正确版本更新成功且版本递增；旧版本 → CONFLICT。
func TestNoteUpdateOptimisticLock(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)
	n := helperNote(t, s, ws, userID, "旧标题", "旧正文")

	up, err := s.Note.NoteUpdate(ctx, NoteUpdateReq{
		WorkspaceID: ws.ID, NoteID: n.ID, Version: n.Version,
		Title: "新标题", BodyMD: "新正文", Tags: []string{"更新"},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if up.Title != "新标题" || up.BodyMD != "新正文" || up.Version != n.Version+1 {
		t.Fatalf("update result wrong: %+v", up)
	}
	if len(up.Tags) != 1 || up.Tags[0] != "更新" {
		t.Fatalf("update tags wrong: %+v", up)
	}

	// 旧版本重放 → CONFLICT
	if _, err := s.Note.NoteUpdate(ctx, NoteUpdateReq{
		WorkspaceID: ws.ID, NoteID: n.ID, Version: n.Version, Title: "再改", BodyMD: "x",
	}); err == nil {
		t.Fatal("stale version update should fail")
	} else if domain.AsError(err).Code != domain.CodeConflict {
		t.Fatalf("want CONFLICT, got %s", domain.AsError(err).Code)
	}
}

// TestNoteListKeywordFTSWithCJK：创建中文笔记后，2 字 CJK 关键词 "量子" 经 SearchFTS 命中正文含 "量子" 的笔记。
func TestNoteListKeywordFTSWithCJK(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)
	helperNote(t, s, ws, userID, "量子力学笔记", "薛定谔方程描述量子态的时间演化。")
	helperNote(t, s, ws, userID, "牛顿力学笔记", "F=ma 描述宏观物体的运动。")

	page, err := s.Note.NoteList(ctx, NoteListReq{WorkspaceID: ws.ID, Keyword: "量子", Limit: 10})
	if err != nil {
		t.Fatalf("keyword search: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("期望命中 1 条，得到 %d", len(page.Items))
	}
	if page.Items[0].Title != "量子力学笔记" {
		t.Fatalf("命中笔记错误：%+v", page.Items[0])
	}

	// 无关键词 → 返回全部（游标分页）
	all, err := s.Note.NoteList(ctx, NoteListReq{WorkspaceID: ws.ID, Limit: 10})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all.Items) != 2 {
		t.Fatalf("期望 2 条笔记，得到 %d", len(all.Items))
	}
}

// TestNoteListFilters：kind / knowledge_id / tag 过滤生效。
func TestNoteListFilters(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)
	helperNote(t, s, ws, userID, "自由笔记", "b")
	if _, err := s.Note.NoteCreate(ctx, NoteCreateReq{
		WorkspaceID: ws.ID, UserID: userID, Kind: "document", Title: "资料笔记",
		BodyMD: "b", KnowledgeIDs: []string{"k-9"}, Tags: []string{"高等数学"},
	}); err != nil {
		t.Fatalf("create doc note: %v", err)
	}

	byKind, err := s.Note.NoteList(ctx, NoteListReq{WorkspaceID: ws.ID, Kind: "document", Limit: 10})
	if err != nil {
		t.Fatalf("list by kind: %v", err)
	}
	if len(byKind.Items) != 1 || byKind.Items[0].Kind != "document" {
		t.Fatalf("kind filter wrong: %+v", byKind.Items)
	}

	byKnowledge, err := s.Note.NoteList(ctx, NoteListReq{WorkspaceID: ws.ID, KnowledgeID: "k-9", Limit: 10})
	if err != nil {
		t.Fatalf("list by knowledge: %v", err)
	}
	if len(byKnowledge.Items) != 1 || byKnowledge.Items[0].Title != "资料笔记" {
		t.Fatalf("knowledge filter wrong: %+v", byKnowledge.Items)
	}

	byTag, err := s.Note.NoteList(ctx, NoteListReq{WorkspaceID: ws.ID, Tag: "高等数学", Limit: 10})
	if err != nil {
		t.Fatalf("list by tag: %v", err)
	}
	if len(byTag.Items) != 1 || byTag.Items[0].Title != "资料笔记" {
		t.Fatalf("tag filter wrong: %+v", byTag.Items)
	}
}

// TestNoteDeleteSoftDelete：软删除成功返回 DeletedAt；再次查询 → NOT_FOUND；旧版本删除 → CONFLICT。
func TestNoteDeleteSoftDelete(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)
	n := helperNote(t, s, ws, userID, "待删除", "b")

	// 先更新使版本前进（1 → 2），旧版本删除 → CONFLICT
	if _, err := s.Note.NoteUpdate(ctx, NoteUpdateReq{
		WorkspaceID: ws.ID, NoteID: n.ID, Version: n.Version, Title: "改后", BodyMD: "x",
	}); err != nil {
		t.Fatalf("bump version: %v", err)
	}
	if _, err := s.Note.NoteDelete(ctx, NoteDeleteReq{WorkspaceID: ws.ID, NoteID: n.ID, Version: n.Version}); err == nil {
		t.Fatal("stale version delete should fail")
	} else if domain.AsError(err).Code != domain.CodeConflict {
		t.Fatalf("want CONFLICT, got %s", domain.AsError(err).Code)
	}

	// 当前版本删除 → 成功软删除
	res, err := s.Note.NoteDelete(ctx, NoteDeleteReq{WorkspaceID: ws.ID, NoteID: n.ID, Version: n.Version + 1})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !res.Deleted || res.DeletedAt == "" {
		t.Fatalf("delete result wrong: %+v", res)
	}

	// 软删除落库：deleted_at 非空，列表不再返回。
	var deletedAt string
	if err := s.Repo.DB().QueryRowContext(ctx,
		`SELECT deleted_at FROM notes WHERE id = ?`, n.ID).Scan(&deletedAt); err != nil {
		t.Fatalf("read deleted_at: %v", err)
	}
	if deletedAt == "" {
		t.Fatal("note 应被软删除（deleted_at 非空）")
	}
	page, err := s.Note.NoteList(ctx, NoteListReq{WorkspaceID: ws.ID, Limit: 10})
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("删除后的笔记不应出现在列表：%+v", page.Items)
	}

	// 已删除笔记再删除 → NOT_FOUND
	if _, err := s.Note.NoteDelete(ctx, NoteDeleteReq{WorkspaceID: ws.ID, NoteID: n.ID, Version: n.Version + 2}); err == nil {
		t.Fatal("delete of deleted note should fail")
	} else if domain.AsError(err).Code != domain.CodeNotFound {
		t.Fatalf("want NOT_FOUND, got %s", domain.AsError(err).Code)
	}
}

// TestNoteToFlashcard：NoteToFlashcard 调用闪卡模块生成 source=note 的卡（front=标题），
// 幂等键重放不产生重复卡。
func TestNoteToFlashcard(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)
	n := helperNote(t, s, ws, userID, "牛顿第二定律", "F=ma，力等于质量乘以加速度。")

	key := "note2fc-" + NewID()
	card, err := s.Note.NoteToFlashcard(ctx, NoteToFlashcardReq{
		WorkspaceID: ws.ID, UserID: userID, NoteID: n.ID, IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("to flashcard: %v", err)
	}
	if card == nil || card.Source != "note" || card.Front != "牛顿第二定律" {
		t.Fatalf("flashcard from note wrong: %+v", card)
	}
	if card.SourceRef != n.ID || card.Back != "F=ma，力等于质量乘以加速度。" {
		t.Fatalf("flashcard source/back wrong: %+v", card)
	}

	// 幂等重放 → 同一张卡，无重复
	card2, err := s.Note.NoteToFlashcard(ctx, NoteToFlashcardReq{
		WorkspaceID: ws.ID, UserID: userID, NoteID: n.ID, IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("replay to flashcard: %v", err)
	}
	if card2 == nil || card2.ID != card.ID {
		t.Fatalf("idempotent replay diverged: %+v vs %+v", card, card2)
	}
	var cnt int
	if err := s.Repo.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM flashcards WHERE source = 'note' AND source_ref = ? AND deleted_at IS NULL`,
		n.ID).Scan(&cnt); err != nil {
		t.Fatalf("count flashcards: %v", err)
	}
	if cnt != 1 {
		t.Fatalf("期望 1 张 note 来源闪卡，得到 %d", cnt)
	}
}

// TestAnnotationCreate：合法标注创建成功；锚点/偏移非法 → INVALID_ARGUMENT。
func TestAnnotationCreate(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)
	n := helperNote(t, s, ws, userID, "标注测试", "b")

	a, err := s.Note.AnnotationCreate(ctx, AnnotationCreateReq{
		WorkspaceID: ws.ID, NoteID: n.ID, AnchorHash: "h-abc",
		OffsetStart: 10, OffsetEnd: 20, HighlightColor: "#ff5928",
	})
	if err != nil {
		t.Fatalf("create annotation: %v", err)
	}
	if a.ID == "" || a.NoteID != n.ID || a.AnchorHash != "h-abc" {
		t.Fatalf("annotation fields wrong: %+v", a)
	}
	if a.OffsetStart != 10 || a.OffsetEnd != 20 || a.HighlightColor != "#ff5928" {
		t.Fatalf("annotation offsets wrong: %+v", a)
	}

	cases := []AnnotationCreateReq{
		{WorkspaceID: ws.ID, NoteID: n.ID, AnchorHash: "", OffsetStart: 0, OffsetEnd: 1},  // 空锚点
		{WorkspaceID: ws.ID, NoteID: n.ID, AnchorHash: "h", OffsetStart: 5, OffsetEnd: 2}, // end < start
		{WorkspaceID: ws.ID, NoteID: n.ID, AnchorHash: "h", OffsetStart: -1, OffsetEnd: 1}, // 负偏移
		{WorkspaceID: ws.ID, AnchorHash: "h", OffsetStart: 0, OffsetEnd: 1},                // 缺 note_id
	}
	for i, req := range cases {
		if _, err := s.Note.AnnotationCreate(ctx, req); err == nil {
			t.Fatalf("case %d: expected error, got nil", i)
		} else if domain.AsError(err).Code != domain.CodeInvalidArgument {
			t.Fatalf("case %d: want INVALID_ARGUMENT, got %s", i, domain.AsError(err).Code)
		}
	}

	// 笔记不存在 → NOT_FOUND
	if _, err := s.Note.AnnotationCreate(ctx, AnnotationCreateReq{
		WorkspaceID: ws.ID, NoteID: "n-missing-" + NewID(), AnchorHash: "h", OffsetStart: 0, OffsetEnd: 1,
	}); err == nil {
		t.Fatal("missing note should fail")
	} else if domain.AsError(err).Code != domain.CodeNotFound {
		t.Fatalf("want NOT_FOUND, got %s", domain.AsError(err).Code)
	}
}
