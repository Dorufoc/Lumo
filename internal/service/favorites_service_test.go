package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"lumo/internal/domain"
	"lumo/internal/provider"
	"lumo/internal/repository"
)

// ---- 辅助 ----

// favImportDoc 上传并导入一份索引成功的文档。
func favImportDoc(t *testing.T, s *Services, ws *Workspace, name, content string) *Document {
	t.Helper()
	up, err := s.Import.UploadFile(name, []byte(content))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	doc, err := s.Document.DocumentImport(context.Background(), DocumentImportReq{
		WorkspaceID: ws.ID, FilePath: up.Path, IdempotencyKey: "doc-" + NewID(),
	})
	if err != nil {
		t.Fatalf("import doc: %v", err)
	}
	if doc.Status != "indexed" {
		t.Fatalf("expected indexed, got %s (failure: %v)", doc.Status, doc.FailureReason)
	}
	return doc
}

// favRowCount 统计收藏行数。
func favRowCount(t *testing.T, s *Services, userID string) int {
	t.Helper()
	var n int
	if err := s.Repo.DB().QueryRowContext(ctx(),
		`SELECT COUNT(*) FROM favorites WHERE user_id = ?`, userID).Scan(&n); err != nil {
		t.Fatalf("count favorites: %v", err)
	}
	return n
}

// readLaterRowCount 统计稍后读行数。
func readLaterRowCount(t *testing.T, s *Services, userID string) int {
	t.Helper()
	var n int
	if err := s.Repo.DB().QueryRowContext(ctx(),
		`SELECT COUNT(*) FROM read_later WHERE user_id = ?`, userID).Scan(&n); err != nil {
		t.Fatalf("count read_later: %v", err)
	}
	return n
}

// stubLLM 是确定性 LLM 桩（返回固定内容或固定错误）。
type stubLLM struct {
	content string
	err     error
}

func (m *stubLLM) Name() string { return "stub" }

func (m *stubLLM) Chat(ctx context.Context, req provider.ChatRequest, onDelta func(string)) (*provider.ChatResult, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &provider.ChatResult{Content: m.content, Model: "stub"}, nil
}

// ---- 场景 1：FavoriteToggle 创建 / 取消 ----

func TestFavoriteToggleCreateRemove(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)

	f, err := s.Favorites.FavoriteToggle(ctx(), FavoriteToggleReq{
		WorkspaceID: ws.ID, UserID: userID, RefType: domain.FavoriteRefTypeQuestion, RefID: "q-1",
	})
	if err != nil {
		t.Fatalf("toggle on: %v", err)
	}
	if !f.Favorited || f.Version != 1 || f.RefType != domain.FavoriteRefTypeQuestion || f.RefID != "q-1" {
		t.Fatalf("unexpected favorite after toggle on: %+v", f)
	}
	if n := favRowCount(t, s, userID); n != 1 {
		t.Fatalf("expected 1 favorite row, got %d", n)
	}

	// 再点一次 → 取消收藏
	off, err := s.Favorites.FavoriteToggle(ctx(), FavoriteToggleReq{
		WorkspaceID: ws.ID, UserID: userID, RefType: domain.FavoriteRefTypeQuestion, RefID: "q-1",
		Version: f.Version,
	})
	if err != nil {
		t.Fatalf("toggle off: %v", err)
	}
	if off.Favorited {
		t.Fatal("expected Favorited=false after toggle off")
	}
	if n := favRowCount(t, s, userID); n != 0 {
		t.Fatalf("expected 0 favorite rows, got %d", n)
	}

	// 再点一次 → 重新收藏（新行）
	on2, err := s.Favorites.FavoriteToggle(ctx(), FavoriteToggleReq{
		WorkspaceID: ws.ID, UserID: userID, RefType: domain.FavoriteRefTypeQuestion, RefID: "q-1",
	})
	if err != nil {
		t.Fatalf("re-toggle on: %v", err)
	}
	if !on2.Favorited || on2.Version != 1 {
		t.Fatalf("unexpected re-toggle result: %+v", on2)
	}
	if n := favRowCount(t, s, userID); n != 1 {
		t.Fatalf("expected 1 favorite row after re-toggle, got %d", n)
	}
}

// ---- 场景 2：同一 (user, ref_type, ref_id) 唯一；不同用户独立 ----

func TestFavoriteToggleUniquePerUserAndRef(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)

	if _, err := s.Favorites.FavoriteToggle(ctx(), FavoriteToggleReq{
		WorkspaceID: ws.ID, UserID: userID, RefType: domain.FavoriteRefTypeQuestion, RefID: "q-1",
	}); err != nil {
		t.Fatalf("toggle q1: %v", err)
	}
	// 同一 ref 不同 ref_type 互不影响
	if _, err := s.Favorites.FavoriteToggle(ctx(), FavoriteToggleReq{
		WorkspaceID: ws.ID, UserID: userID, RefType: domain.FavoriteRefTypeNote, RefID: "q-1",
	}); err != nil {
		t.Fatalf("toggle note q1: %v", err)
	}
	if n := favRowCount(t, s, userID); n != 2 {
		t.Fatalf("expected 2 favorite rows, got %d", n)
	}

	// 另一用户收藏同一 ref → 独立行
	second, err := s.Workspace.UserCreate(ctx(), UserCreateReq{WorkspaceID: ws.ID, DisplayName: "第二用户"})
	if err != nil {
		t.Fatalf("create second user: %v", err)
	}
	if _, err := s.Favorites.FavoriteToggle(ctx(), FavoriteToggleReq{
		WorkspaceID: ws.ID, UserID: second.ID, RefType: domain.FavoriteRefTypeQuestion, RefID: "q-1",
	}); err != nil {
		t.Fatalf("toggle second user: %v", err)
	}
	if n := favRowCount(t, s, second.ID); n != 1 {
		t.Fatalf("expected 1 row for second user, got %d", n)
	}
}

// ---- 场景 3：FavoriteToggle 乐观锁（stale version → CONFLICT） ----

func TestFavoriteToggleStaleVersionConflict(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)

	f, err := s.Favorites.FavoriteToggle(ctx(), FavoriteToggleReq{
		WorkspaceID: ws.ID, UserID: userID, RefType: domain.FavoriteRefTypeQuestion, RefID: "q-1",
	})
	if err != nil {
		t.Fatalf("toggle on: %v", err)
	}
	if _, err := s.Favorites.FavoriteToggle(ctx(), FavoriteToggleReq{
		WorkspaceID: ws.ID, UserID: userID, RefType: domain.FavoriteRefTypeQuestion, RefID: "q-1",
		Version: f.Version + 99,
	}); err == nil || domain.AsError(err).Code != domain.CodeConflict {
		t.Fatalf("expected CONFLICT on stale version, got %v", err)
	}
	// 仍处于收藏状态
	if n := favRowCount(t, s, userID); n != 1 {
		t.Fatalf("expected favorite still present, got %d rows", n)
	}
}

// ---- 场景 4：FavoriteToggle 分组更新（仍保持收藏） ----

func TestFavoriteToggleGroupUpdate(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)

	f, err := s.Favorites.FavoriteToggle(ctx(), FavoriteToggleReq{
		WorkspaceID: ws.ID, UserID: userID, RefType: domain.FavoriteRefTypeQuestion, RefID: "q-1",
	})
	if err != nil {
		t.Fatalf("toggle on: %v", err)
	}
	updated, err := s.Favorites.FavoriteToggle(ctx(), FavoriteToggleReq{
		WorkspaceID: ws.ID, UserID: userID, RefType: domain.FavoriteRefTypeQuestion, RefID: "q-1",
		GroupName: "数学", Version: f.Version,
	})
	if err != nil {
		t.Fatalf("toggle with group: %v", err)
	}
	if !updated.Favorited || updated.GroupName != "数学" || updated.Version != f.Version+1 {
		t.Fatalf("unexpected group update result: %+v", updated)
	}
	if n := favRowCount(t, s, userID); n != 1 {
		t.Fatalf("expected single favorite row, got %d", n)
	}
}

// ---- 场景 5：FavoriteList 过滤（group_name / keyword / ref_type） ----

func TestFavoriteListFilters(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)

	mustFav := func(refType, refID, group, note string) {
		t.Helper()
		if _, err := s.Favorites.FavoriteToggle(ctx(), FavoriteToggleReq{
			WorkspaceID: ws.ID, UserID: userID, RefType: refType, RefID: refID,
			GroupName: group, Note: note,
		}); err != nil {
			t.Fatalf("favorite %s/%s: %v", refType, refID, err)
		}
	}
	mustFav(domain.FavoriteRefTypeQuestion, "q-1", "数学", "导数定义")
	mustFav(domain.FavoriteRefTypeQuestion, "q-2", "数学", "极限")
	mustFav(domain.FavoriteRefTypeDocument, "d-1", "物理", "牛顿定律")

	// group_name 过滤
	mathPage, err := s.Favorites.FavoriteList(ctx(), FavoriteListReq{
		WorkspaceID: ws.ID, UserID: userID, GroupName: "数学",
	})
	if err != nil {
		t.Fatalf("list by group: %v", err)
	}
	if len(mathPage.Items) != 2 {
		t.Fatalf("expected 2 math favorites, got %d", len(mathPage.Items))
	}

	// ref_type 过滤
	docPage, err := s.Favorites.FavoriteList(ctx(), FavoriteListReq{
		WorkspaceID: ws.ID, UserID: userID, RefType: domain.FavoriteRefTypeDocument,
	})
	if err != nil {
		t.Fatalf("list by ref_type: %v", err)
	}
	if len(docPage.Items) != 1 || docPage.Items[0].RefID != "d-1" {
		t.Fatalf("unexpected ref_type filter result: %+v", docPage.Items)
	}

	// keyword 过滤（命中 note）
	kw, err := s.Favorites.FavoriteList(ctx(), FavoriteListReq{
		WorkspaceID: ws.ID, UserID: userID, Keyword: "牛顿",
	})
	if err != nil {
		t.Fatalf("list by keyword: %v", err)
	}
	if len(kw.Items) != 1 || kw.Items[0].RefID != "d-1" {
		t.Fatalf("unexpected keyword filter result: %+v", kw.Items)
	}

	// 无匹配
	empty, err := s.Favorites.FavoriteList(ctx(), FavoriteListReq{
		WorkspaceID: ws.ID, UserID: userID, GroupName: "不存在",
	})
	if err != nil {
		t.Fatalf("list empty: %v", err)
	}
	if len(empty.Items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(empty.Items))
	}
}

// ---- 场景 6：FavoriteList 分页（cursor / has_more / 无跨页重复） ----

func TestFavoriteListPagination(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)

	for i := 0; i < 3; i++ {
		if _, err := s.Favorites.FavoriteToggle(ctx(), FavoriteToggleReq{
			WorkspaceID: ws.ID, UserID: userID,
			RefType: domain.FavoriteRefTypeQuestion, RefID: "q-" + string(rune('a'+i)),
		}); err != nil {
			t.Fatalf("favorite %d: %v", i, err)
		}
	}

	page1, err := s.Favorites.FavoriteList(ctx(), FavoriteListReq{
		WorkspaceID: ws.ID, UserID: userID, Limit: 2,
	})
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1.Items) != 2 || !page1.HasMore || page1.NextCursor == "" {
		t.Fatalf("unexpected page1: items=%d has_more=%v cursor=%q", len(page1.Items), page1.HasMore, page1.NextCursor)
	}

	page2, err := s.Favorites.FavoriteList(ctx(), FavoriteListReq{
		WorkspaceID: ws.ID, UserID: userID, Limit: 2, Cursor: page1.NextCursor,
	})
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2.Items) != 1 || page2.HasMore {
		t.Fatalf("unexpected page2: items=%d has_more=%v", len(page2.Items), page2.HasMore)
	}

	seen := map[string]bool{}
	for _, it := range append(page1.Items, page2.Items...) {
		if seen[it.RefID] {
			t.Fatalf("duplicate favorite across pages: %s", it.RefID)
		}
		seen[it.RefID] = true
	}
}

// ---- 场景 7：ReadLaterAdd（幂等：同文档只入队一次） ----

func TestReadLaterAddIdempotent(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	doc := favImportDoc(t, s, ws, "r.pdf", "# 稍后读测试\n\n第一章内容。")

	first, err := s.Favorites.ReadLaterAdd(ctx(), ReadLaterAddReq{
		WorkspaceID: ws.ID, UserID: userID, DocumentID: doc.ID,
	})
	if err != nil {
		t.Fatalf("read later add: %v", err)
	}
	if first.Status != domain.ReadLaterStatusQueued {
		t.Fatalf("expected queued, got %s", first.Status)
	}

	again, err := s.Favorites.ReadLaterAdd(ctx(), ReadLaterAddReq{
		WorkspaceID: ws.ID, UserID: userID, DocumentID: doc.ID,
	})
	if err != nil {
		t.Fatalf("read later add again: %v", err)
	}
	if again.ID != first.ID {
		t.Fatalf("idempotency violated: %s != %s", again.ID, first.ID)
	}
	if n := readLaterRowCount(t, s, userID); n != 1 {
		t.Fatalf("expected 1 read_later row, got %d", n)
	}
}

// ---- 场景 8：ReadLaterTransition 状态流转 ----

func TestReadLaterTransitionFlow(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	doc := favImportDoc(t, s, ws, "r.pdf", "# 稍后读\n\n正文。")

	item, err := s.Favorites.ReadLaterAdd(ctx(), ReadLaterAddReq{
		WorkspaceID: ws.ID, UserID: userID, DocumentID: doc.ID,
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	// queued → read
	read, err := s.Favorites.ReadLaterTransition(ctx(), ReadLaterTransitionReq{
		WorkspaceID: ws.ID, UserID: userID, ItemID: item.ID, Action: domain.ReadLaterActionRead,
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if read.Status != domain.ReadLaterStatusRead {
		t.Fatalf("expected read, got %s", read.Status)
	}

	// read → requeue → queued
	req, err := s.Favorites.ReadLaterTransition(ctx(), ReadLaterTransitionReq{
		WorkspaceID: ws.ID, UserID: userID, ItemID: item.ID, Action: domain.ReadLaterActionRequeue,
	})
	if err != nil {
		t.Fatalf("requeue: %v", err)
	}
	if req.Status != domain.ReadLaterStatusQueued {
		t.Fatalf("expected requeued to queued, got %s", req.Status)
	}

	// queued → skip → skipped
	skip, err := s.Favorites.ReadLaterTransition(ctx(), ReadLaterTransitionReq{
		WorkspaceID: ws.ID, UserID: userID, ItemID: item.ID, Action: domain.ReadLaterActionSkip,
	})
	if err != nil {
		t.Fatalf("skip: %v", err)
	}
	if skip.Status != domain.ReadLaterStatusSkipped {
		t.Fatalf("expected skipped, got %s", skip.Status)
	}

	// skipped → requeue → queued
	req2, err := s.Favorites.ReadLaterTransition(ctx(), ReadLaterTransitionReq{
		WorkspaceID: ws.ID, UserID: userID, ItemID: item.ID, Action: domain.ReadLaterActionRequeue,
	})
	if err != nil {
		t.Fatalf("requeue 2: %v", err)
	}
	if req2.Status != domain.ReadLaterStatusQueued {
		t.Fatalf("expected requeued to queued again, got %s", req2.Status)
	}
}

// ---- 场景 9：ReadLaterTransition 非法流转 / 不存在 ----

func TestReadLaterIllegalTransition(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	doc := favImportDoc(t, s, ws, "r.pdf", "# 稍后读\n\n正文。")

	item, err := s.Favorites.ReadLaterAdd(ctx(), ReadLaterAddReq{
		WorkspaceID: ws.ID, UserID: userID, DocumentID: doc.ID,
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	cases := []struct {
		name   string
		action string
	}{
		{"queued → requeue", domain.ReadLaterActionRequeue},
	}
	for _, c := range cases {
		if _, err := s.Favorites.ReadLaterTransition(ctx(), ReadLaterTransitionReq{
			WorkspaceID: ws.ID, UserID: userID, ItemID: item.ID, Action: c.action,
		}); err == nil || domain.AsError(err).Code != domain.CodeInvalidState {
			t.Fatalf("%s: expected INVALID_STATE, got %v", c.name, err)
		}
	}

	// read → skip 非法
	if _, err := s.Favorites.ReadLaterTransition(ctx(), ReadLaterTransitionReq{
		WorkspaceID: ws.ID, UserID: userID, ItemID: item.ID, Action: domain.ReadLaterActionRead,
	}); err != nil {
		t.Fatalf("to read: %v", err)
	}
	if _, err := s.Favorites.ReadLaterTransition(ctx(), ReadLaterTransitionReq{
		WorkspaceID: ws.ID, UserID: userID, ItemID: item.ID, Action: domain.ReadLaterActionSkip,
	}); err == nil || domain.AsError(err).Code != domain.CodeInvalidState {
		t.Fatalf("read → skip: expected INVALID_STATE, got %v", err)
	}

	// 不存在的 item
	if _, err := s.Favorites.ReadLaterTransition(ctx(), ReadLaterTransitionReq{
		WorkspaceID: ws.ID, UserID: userID, ItemID: "no-such-item", Action: domain.ReadLaterActionRead,
	}); err == nil || domain.AsError(err).Code != domain.CodeNotFound {
		t.Fatalf("nonexistent item: expected NOT_FOUND, got %v", err)
	}
}

// ---- 场景 10：ReadLaterAdd 队列上限（500/用户） ----

func TestReadLaterQueueLimit(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)

	// 直接经 repo 批量种满 500 条（文档行 + 稍后读行）
	for i := 0; i < readLaterQueueLimit; i++ {
		docID := NewID()
		row := &repository.DocumentRow{
			ID: docID, WorkspaceID: ws.ID, FileName: "seed-" + docID + ".pdf",
			MimeType: "application/pdf", ByteSize: 10,
			SHA256: fmt.Sprintf("%064x", i),
		}
		if err := s.Repo.CreateDocument(ctx(), row); err != nil {
			t.Fatalf("seed doc %d: %v", i, err)
		}
		if err := s.Repo.CreateReadLater(ctx(), &repository.ReadLaterRow{
			ID: NewID(), WorkspaceID: ws.ID, UserID: userID, DocumentID: docID, Status: domain.ReadLaterStatusQueued,
		}); err != nil {
			t.Fatalf("seed read_later %d: %v", i, err)
		}
	}
	if n := readLaterRowCount(t, s, userID); n != readLaterQueueLimit {
		t.Fatalf("expected %d seeded rows, got %d", readLaterQueueLimit, n)
	}

	// 超限添加 → QUOTA_EXCEEDED
	extra := favImportDoc(t, s, ws, "extra.pdf", "# 溢出\n\n内容。")
	if _, err := s.Favorites.ReadLaterAdd(ctx(), ReadLaterAddReq{
		WorkspaceID: ws.ID, UserID: userID, DocumentID: extra.ID,
	}); err == nil || domain.AsError(err).Code != domain.CodeQuotaExceeded {
		t.Fatalf("expected QUOTA_EXCEEDED, got %v", err)
	}
}

// ---- 场景 11：DocumentSummarize 降级（未配置 LLM → ready + 确定性模板） ----

func TestDocumentSummarizeDegraded(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	doc := favImportDoc(t, s, ws, "notes.md", "# 高等数学\n\n导数定义。\n\n极限概念。")

	// newTestServices 中 s.Agent.LLMFactory 为 nil → llm() 返回 ErrNotConfigured
	sum, err := s.Favorites.DocumentSummarize(ctx(), DocumentSummarizeReq{
		WorkspaceID: ws.ID, DocumentID: doc.ID, IdempotencyKey: "sum-" + NewID(),
	})
	if err != nil {
		t.Fatalf("summarize degraded: %v", err)
	}
	if sum.Status != domain.SummaryStatusReady {
		t.Fatalf("expected degraded ready, got %s", sum.Status)
	}
	var pl domain.SummaryPayload
	if err := json.Unmarshal(sum.SummaryJSON, &pl); err != nil {
		t.Fatalf("unmarshal summary: %v", err)
	}
	if len(pl.Points) == 0 || !strings.Contains(pl.Points[0], "未配置") {
		t.Fatalf("expected degraded note in points, got %+v", pl.Points)
	}
	if sum.CreatedAt == "" || sum.UpdatedAt == "" {
		t.Fatalf("expected persisted timestamps, got CreatedAt=%q UpdatedAt=%q", sum.CreatedAt, sum.UpdatedAt)
	}
}

// ---- 场景 12：DocumentSummarize LLM 成功路径 ----

func TestDocumentSummarizeLLMPath(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	doc := favImportDoc(t, s, ws, "notes.md", "# 力学\n\n牛顿第二定律。")

	s.Agent.LLMFactory = func() (provider.LLMProvider, error) {
		return &stubLLM{content: `{"points":["力等于质量乘以加速度"],"structure":["力学"],"terms":["牛顿第二定律"]}`}, nil
	}
	sum, err := s.Favorites.DocumentSummarize(ctx(), DocumentSummarizeReq{
		WorkspaceID: ws.ID, DocumentID: doc.ID, IdempotencyKey: "sum-" + NewID(),
	})
	if err != nil {
		t.Fatalf("summarize llm: %v", err)
	}
	if sum.Status != domain.SummaryStatusReady {
		t.Fatalf("expected ready, got %s", sum.Status)
	}
	if sum.Model != "stub" {
		t.Fatalf("expected model stub, got %q", sum.Model)
	}
	var pl domain.SummaryPayload
	if err := json.Unmarshal(sum.SummaryJSON, &pl); err != nil {
		t.Fatalf("unmarshal summary: %v", err)
	}
	if len(pl.Points) != 1 || pl.Points[0] != "力等于质量乘以加速度" {
		t.Fatalf("unexpected points: %+v", pl.Points)
	}
	if len(pl.Terms) != 1 || pl.Terms[0] != "牛顿第二定律" {
		t.Fatalf("unexpected terms: %+v", pl.Terms)
	}
	if sum.CreatedAt == "" || sum.UpdatedAt == "" {
		t.Fatalf("expected persisted timestamps, got CreatedAt=%q UpdatedAt=%q", sum.CreatedAt, sum.UpdatedAt)
	}
}

// ---- 场景 13：DocumentSummarize LLM 失败 → failed ----

func TestDocumentSummarizeLLMFailure(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	doc := favImportDoc(t, s, ws, "notes.md", "# 失败路径\n\n内容。")

	s.Agent.LLMFactory = func() (provider.LLMProvider, error) {
		return &stubLLM{err: errors.New("provider down")}, nil
	}
	sum, err := s.Favorites.DocumentSummarize(ctx(), DocumentSummarizeReq{
		WorkspaceID: ws.ID, DocumentID: doc.ID, IdempotencyKey: "sum-" + NewID(),
	})
	if err != nil {
		t.Fatalf("summarize fail: %v", err)
	}
	if sum.Status != domain.SummaryStatusFailed {
		t.Fatalf("expected failed, got %s", sum.Status)
	}
	if sum.CreatedAt == "" || sum.UpdatedAt == "" {
		t.Fatalf("expected persisted timestamps, got CreatedAt=%q UpdatedAt=%q", sum.CreatedAt, sum.UpdatedAt)
	}
}

// ---- 场景 14：DocumentSummarize 幂等（同键重放返回同一摘要） ----

func TestDocumentSummarizeIdempotent(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	doc := favImportDoc(t, s, ws, "notes.md", "# 幂等\n\n内容。")

	key := "sum-" + NewID()
	first, err := s.Favorites.DocumentSummarize(ctx(), DocumentSummarizeReq{
		WorkspaceID: ws.ID, DocumentID: doc.ID, IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("first summarize: %v", err)
	}
	replay, err := s.Favorites.DocumentSummarize(ctx(), DocumentSummarizeReq{
		WorkspaceID: ws.ID, DocumentID: doc.ID, IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("replay summarize: %v", err)
	}
	if replay.ID != first.ID {
		t.Fatalf("idempotency violated: %s != %s", replay.ID, first.ID)
	}
	var n int
	if err := s.Repo.DB().QueryRowContext(ctx(),
		`SELECT COUNT(*) FROM document_summaries WHERE document_id = ?`, doc.ID).Scan(&n); err != nil {
		t.Fatalf("count summaries: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 summary row, got %d", n)
	}
}

// ---- 场景 15：校验矩阵 → INVALID_ARGUMENT ----

func TestFavoritesValidation(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	doc := favImportDoc(t, s, ws, "v.pdf", "# 校验\n\n内容。")

	cases := []struct {
		name string
		fn   func() error
	}{
		{"favorite empty user_id", func() error {
			_, err := s.Favorites.FavoriteToggle(ctx(), FavoriteToggleReq{
				WorkspaceID: ws.ID, RefType: domain.FavoriteRefTypeQuestion, RefID: "q-1",
			})
			return err
		}},
		{"favorite bad ref_type", func() error {
			_, err := s.Favorites.FavoriteToggle(ctx(), FavoriteToggleReq{
				WorkspaceID: ws.ID, UserID: userID, RefType: "unknown", RefID: "q-1",
			})
			return err
		}},
		{"favorite empty ref_id", func() error {
			_, err := s.Favorites.FavoriteToggle(ctx(), FavoriteToggleReq{
				WorkspaceID: ws.ID, UserID: userID, RefType: domain.FavoriteRefTypeQuestion,
			})
			return err
		}},
		{"read_later missing doc", func() error {
			_, err := s.Favorites.ReadLaterAdd(ctx(), ReadLaterAddReq{WorkspaceID: ws.ID, UserID: userID})
			return err
		}},
		{"read_later not found doc", func() error {
			_, err := s.Favorites.ReadLaterAdd(ctx(), ReadLaterAddReq{
				WorkspaceID: ws.ID, UserID: userID, DocumentID: "no-such-doc",
			})
			return err
		}},
		{"summarize missing key", func() error {
			_, err := s.Favorites.DocumentSummarize(ctx(), DocumentSummarizeReq{
				WorkspaceID: ws.ID, DocumentID: doc.ID,
			})
			return err
		}},
		{"summarize missing doc", func() error {
			_, err := s.Favorites.DocumentSummarize(ctx(), DocumentSummarizeReq{
				WorkspaceID: ws.ID, IdempotencyKey: "sum-" + NewID(),
			})
			return err
		}},
	}
	for _, c := range cases {
		if err := c.fn(); err == nil {
			t.Fatalf("%s: expected error, got nil", c.name)
		} else if domain.AsError(err).Code != domain.CodeInvalidArgument &&
			!strings.Contains(c.name, "not found doc") {
			t.Fatalf("%s: expected INVALID_ARGUMENT, got %s", c.name, domain.AsError(err).Code)
		} else if strings.Contains(c.name, "not found doc") && domain.AsError(err).Code != domain.CodeNotFound {
			t.Fatalf("%s: expected NOT_FOUND, got %s", c.name, domain.AsError(err).Code)
		}
	}
}
