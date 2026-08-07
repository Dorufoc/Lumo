package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lumo/internal/domain"
	"lumo/internal/provider"
	"lumo/internal/repository"
)

// 求题闭环测试（完整设计文档 4.20 / Todo 36）。
// 状态机严格对齐文档枚举 content_requests.status[open|fulfilled|closed]：
//   open      = 唯一中间态（生成/审核均在此推进，经 QuizGen 与 review_queue_items）
//   fulfilled = 审核通过且题目入库（草稿管线 status='draft'、source=ai_assisted）
//   closed    = 审核拒绝或用户取消
// 终态不可迁移（closed→fulfilled 等 → INVALID_STATE）。
//
// 生成草稿存储决策（本模块自选，TDD 说明）：review_queue_items 表无载荷列，
// 生成的题目草稿持久化为 <DataDir>/requests/<request_id>.json（与数据库同根，
// 参照社区模块本地 JSON 决策）；审核通过时读文件经 questions 管线入库，
// 审核拒绝时题目不入库。文件仅作草稿缓存，不入题题库。

// createRequest 创建一条求题请求并返回 DTO。
func createRequest(t *testing.T, s *Services, wsID, userID string, description string, knowledgeIDs []string) *ContentRequest {
	t.Helper()
	req, err := s.Request.ContentRequestCreate(context.Background(), ContentRequestCreateReq{
		WorkspaceID: wsID, UserID: userID,
		KnowledgeIDs: knowledgeIDs, Description: description,
		IdempotencyKey: "cr-" + NewID(),
	})
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	return req
}

// requestRowStatus 直接读取 content_requests 行状态。
func requestRowStatus(t *testing.T, s *Services, id string) string {
	t.Helper()
	var st string
	if err := s.Repo.DB().QueryRowContext(context.Background(),
		`SELECT status FROM content_requests WHERE id = ?`, id).Scan(&st); err != nil {
		t.Fatalf("read request status: %v", err)
	}
	return st
}

// requestQuestionCount 统计工作区题目数（题目入库证据）。
func requestQuestionCount(t *testing.T, s *Services, wsID string) int {
	t.Helper()
	var n int
	if err := s.Repo.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM questions WHERE workspace_id = ?`, wsID).Scan(&n); err != nil {
		t.Fatalf("count questions: %v", err)
	}
	return n
}

// requestReviewItems 读取某请求的全部审核条目。
func requestReviewItems(t *testing.T, s *Services, refID string) []*repository.ReviewQueueItemRow {
	t.Helper()
	rows, err := s.Repo.ListReviewQueueItemsByRef(context.Background(), "content_request", refID)
	if err != nil {
		t.Fatalf("list review items: %v", err)
	}
	return rows
}

// requestDraftFile 返回草稿文件路径。
func requestDraftFile(s *Services, reqID string) string {
	return filepath.Join(s.Cfg.DataDir, "requests", reqID+".json")
}

// requestDraftCount 读取草稿文件题数；文件不存在返回 0。
func requestDraftCount(t *testing.T, s *Services, reqID string) int {
	t.Helper()
	b, err := os.ReadFile(requestDraftFile(s, reqID))
	if err != nil {
		return 0
	}
	var out struct {
		Questions []json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("parse draft file: %v", err)
	}
	return len(out.Questions)
}

// seedQuizGenLLM 注入返回两题草稿的 stub LLM（含知识点 id 占位替换）。
func seedQuizGenLLM(t *testing.T, s *Services, kid string) {
	t.Helper()
	body := `{"questions":[` +
		`{"type":"single_choice","stem":"牛顿第二定律公式是？","options":[{"key":"A","text":"F=ma"},{"key":"B","text":"F=mv"}],"answer":"A","analysis":"牛顿第二定律","difficulty":2,"knowledge_ids":["KID"]},` +
		`{"type":"single_choice","stem":"速度的国际单位是？","options":[{"key":"A","text":"m/s"},{"key":"B","text":"km/h"}],"answer":"A","analysis":"速度单位","difficulty":1,"knowledge_ids":["KID"]}` +
		`]}`
	s.Agent.LLMFactory = func() (provider.LLMProvider, error) {
		return &stubLLM{content: strings.ReplaceAll(body, "KID", kid)}, nil
	}
}

// TestContentRequestCreate 覆盖提交求题：status=open、字段正确、幂等重放返回同一请求。
func TestContentRequestCreate(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	ctx := context.Background()
	kid := createKnowledgeNode(t, s, ws.ID, "牛顿力学")

	key := "cr-" + NewID()
	req, err := s.Request.ContentRequestCreate(ctx, ContentRequestCreateReq{
		WorkspaceID: ws.ID, UserID: userID,
		KnowledgeIDs: []string{kid}, Description: "求一组力学基础题",
		IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	if req.ID == "" || req.CreatedAt == "" || req.UpdatedAt == "" {
		t.Fatalf("request missing id/timestamps: %+v", req)
	}
	if req.Status != "open" {
		t.Fatalf("expected status=open, got %q", req.Status)
	}
	if len(req.KnowledgeIDs) != 1 || req.KnowledgeIDs[0] != kid {
		t.Fatalf("knowledge_ids mismatch: %+v", req.KnowledgeIDs)
	}
	if requestRowStatus(t, s, req.ID) != "open" {
		t.Fatal("expected persisted status=open")
	}

	// 幂等重放：同一 key → 同一请求
	replay, err := s.Request.ContentRequestCreate(ctx, ContentRequestCreateReq{
		WorkspaceID: ws.ID, UserID: userID,
		KnowledgeIDs: []string{kid}, Description: "求一组力学基础题",
		IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("replay create: %v", err)
	}
	if replay.ID != req.ID {
		t.Fatalf("idempotency violated: %s != %s", replay.ID, req.ID)
	}
}

// TestContentRequestCreateValidation 覆盖提交校验矩阵。
func TestContentRequestCreateValidation(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	ctx := context.Background()

	cases := []struct {
		name string
		req  ContentRequestCreateReq
	}{
		{"bad workspace", ContentRequestCreateReq{WorkspaceID: "nope", UserID: userID, Description: "x", IdempotencyKey: "cr-" + NewID()}},
		{"empty user", ContentRequestCreateReq{WorkspaceID: ws.ID, UserID: "", Description: "x", IdempotencyKey: "cr-" + NewID()}},
		{"blank description", ContentRequestCreateReq{WorkspaceID: ws.ID, UserID: userID, Description: "  ", IdempotencyKey: "cr-" + NewID()}},
		{"unknown knowledge", ContentRequestCreateReq{WorkspaceID: ws.ID, UserID: userID, KnowledgeIDs: []string{"no-such"}, Description: "x", IdempotencyKey: "cr-" + NewID()}},
		{"short idempotency key", ContentRequestCreateReq{WorkspaceID: ws.ID, UserID: userID, Description: "x", IdempotencyKey: "short"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := s.Request.ContentRequestCreate(ctx, c.req)
			if domain.AsError(err).Code != domain.CodeInvalidArgument {
				t.Fatalf("expected INVALID_ARGUMENT, got %v", err)
			}
		})
	}
}

// TestContentRequestGenerateFulfill 覆盖 happy 全路径：
// open → 生成（QuizGen，仍 open + pending 审核项）→ 审核通过 → fulfilled + 题目入库。
func TestContentRequestGenerateFulfill(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	ctx := context.Background()
	kid := createKnowledgeNode(t, s, ws.ID, "牛顿力学")
	seedQuizGenLLM(t, s, kid)

	req := createRequest(t, s, ws.ID, userID, "求一组牛顿力学基础题", []string{kid})

	gen, err := s.Request.ContentRequestGenerate(ctx, ContentRequestGenerateReq{
		WorkspaceID: ws.ID, UserID: userID, RequestID: req.ID,
		Count: 2, IdempotencyKey: "rg-" + NewID(),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	// 生成后仍在 open（中间态）
	if gen.Status != "open" {
		t.Fatalf("expected status=open after generate, got %q", gen.Status)
	}
	if gen.ReviewStatus != "pending" {
		t.Fatalf("expected review_status=pending, got %q", gen.ReviewStatus)
	}
	if gen.QuestionCount != 2 {
		t.Fatalf("expected 2 drafts, got %d", gen.QuestionCount)
	}
	if requestDraftCount(t, s, req.ID) != 2 {
		t.Fatal("expected drafts file with 2 questions")
	}
	items := requestReviewItems(t, s, req.ID)
	if len(items) != 1 || items[0].Status != "pending" {
		t.Fatalf("expected 1 pending review item, got %+v", items)
	}

	// 审核通过 → fulfilled + 题目入库草稿（source=ai_assisted）
	approved, err := s.Request.ContentRequestReview(ctx, ContentRequestReviewReq{
		WorkspaceID: ws.ID, UserID: userID, RequestID: req.ID,
		Decision: "approved", Reason: "内容合理",
	})
	if err != nil {
		t.Fatalf("review approve: %v", err)
	}
	if approved.Status != "fulfilled" {
		t.Fatalf("expected status=fulfilled, got %q", approved.Status)
	}
	if requestRowStatus(t, s, req.ID) != "fulfilled" {
		t.Fatal("expected persisted status=fulfilled")
	}
	if requestQuestionCount(t, s, ws.ID) != 2 {
		t.Fatalf("expected 2 questions in bank, got %d", requestQuestionCount(t, s, ws.ID))
	}
	items = requestReviewItems(t, s, req.ID)
	if len(items) != 1 || items[0].Status != "approved" {
		t.Fatalf("expected review item approved, got %+v", items)
	}

	// 入库题目为草稿管线：status=draft + source=ai_assisted + 版本 review=pending + 知识点关联
	var src, qStatus, source string
	var qid string
	if err := s.Repo.DB().QueryRowContext(ctx,
		`SELECT id, status, source FROM questions WHERE workspace_id = ?`, ws.ID).Scan(&qid, &qStatus, &source); err != nil {
		t.Fatalf("read question: %v", err)
	}
	_ = src
	if qStatus != "draft" {
		t.Fatalf("expected question status=draft, got %q", qStatus)
	}
	if source != "ai_assisted" {
		t.Fatalf("expected source=ai_assisted, got %q", source)
	}
}

// TestContentRequestGenerateDegraded 覆盖未配置 Provider 降级：确定性模板草稿，
// 仍可生成 → 审核通过 → 入库。
func TestContentRequestGenerateDegraded(t *testing.T) {
	s, _ := newTestServices(t) // 不设置 LLMFactory → Provider 未配置
	ws, userID := createWorkspace(t, s)
	ctx := context.Background()
	kid := createKnowledgeNode(t, s, ws.ID, "牛顿力学")

	req := createRequest(t, s, ws.ID, userID, "求一组力学题", []string{kid})
	gen, err := s.Request.ContentRequestGenerate(ctx, ContentRequestGenerateReq{
		WorkspaceID: ws.ID, UserID: userID, RequestID: req.ID,
		Count: 3, IdempotencyKey: "rg-" + NewID(),
	})
	if err != nil {
		t.Fatalf("generate degraded: %v", err)
	}
	if gen.Status != "open" || gen.ReviewStatus != "pending" {
		t.Fatalf("unexpected state after degraded generate: %+v", gen)
	}
	if gen.QuestionCount != 3 {
		t.Fatalf("expected 3 template drafts, got %d", gen.QuestionCount)
	}

	approved, err := s.Request.ContentRequestReview(ctx, ContentRequestReviewReq{
		WorkspaceID: ws.ID, UserID: userID, RequestID: req.ID, Decision: "approved",
	})
	if err != nil {
		t.Fatalf("approve degraded: %v", err)
	}
	if approved.Status != "fulfilled" {
		t.Fatalf("expected fulfilled, got %q", approved.Status)
	}
	if requestQuestionCount(t, s, ws.ID) != 3 {
		t.Fatalf("expected 3 template questions in bank, got %d", requestQuestionCount(t, s, ws.ID))
	}
}

// TestContentRequestReviewReject 覆盖 QA failure：审核拒绝 → closed 且题目不入库。
func TestContentRequestReviewReject(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	ctx := context.Background()
	kid := createKnowledgeNode(t, s, ws.ID, "力学")
	seedQuizGenLLM(t, s, kid)

	req := createRequest(t, s, ws.ID, userID, "求题", []string{kid})
	if _, err := s.Request.ContentRequestGenerate(ctx, ContentRequestGenerateReq{
		WorkspaceID: ws.ID, UserID: userID, RequestID: req.ID,
		Count: 2, IdempotencyKey: "rg-" + NewID(),
	}); err != nil {
		t.Fatalf("generate: %v", err)
	}

	rejected, err := s.Request.ContentRequestReview(ctx, ContentRequestReviewReq{
		WorkspaceID: ws.ID, UserID: userID, RequestID: req.ID,
		Decision: "rejected", Reason: "内容与资料不符",
	})
	if err != nil {
		t.Fatalf("review reject: %v", err)
	}
	if rejected.Status != "closed" {
		t.Fatalf("expected status=closed, got %q", rejected.Status)
	}
	if requestRowStatus(t, s, req.ID) != "closed" {
		t.Fatal("expected persisted status=closed")
	}
	// 题目不入库
	if n := requestQuestionCount(t, s, ws.ID); n != 0 {
		t.Fatalf("expected 0 questions after reject, got %d", n)
	}
	items := requestReviewItems(t, s, req.ID)
	if len(items) != 1 || items[0].Status != "rejected" {
		t.Fatalf("expected review item rejected, got %+v", items)
	}
}

// TestContentRequestStateMachineIllegalTransition 覆盖 QA failure：终态不可迁移。
// closed→fulfilled（对已拒绝请求再次审核通过）、fulfilled→closed（已入库后取消）、
// closed→generate、fulfilled→generate 均报 INVALID_STATE。
func TestContentRequestStateMachineIllegalTransition(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	ctx := context.Background()
	kid := createKnowledgeNode(t, s, ws.ID, "力学")
	seedQuizGenLLM(t, s, kid)

	// 用例 1：closed → fulfilled（closed 后再次审核通过 → INVALID_STATE）
	rejReq := createRequest(t, s, ws.ID, userID, "将被拒绝", []string{kid})
	if _, err := s.Request.ContentRequestGenerate(ctx, ContentRequestGenerateReq{
		WorkspaceID: ws.ID, UserID: userID, RequestID: rejReq.ID,
		Count: 1, IdempotencyKey: "rg-" + NewID(),
	}); err != nil {
		t.Fatalf("generate: %v", err)
	}
	if _, err := s.Request.ContentRequestReview(ctx, ContentRequestReviewReq{
		WorkspaceID: ws.ID, UserID: userID, RequestID: rejReq.ID, Decision: "rejected",
	}); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if requestRowStatus(t, s, rejReq.ID) != "closed" {
		t.Fatal("expected closed")
	}
	// closed → fulfilled（审核通过）
	_, err := s.Request.ContentRequestReview(ctx, ContentRequestReviewReq{
		WorkspaceID: ws.ID, UserID: userID, RequestID: rejReq.ID, Decision: "approved",
	})
	if domain.AsError(err).Code != domain.CodeInvalidState {
		t.Fatalf("closed→fulfilled: expected INVALID_STATE, got %v", err)
	}
	// closed → generate
	_, err = s.Request.ContentRequestGenerate(ctx, ContentRequestGenerateReq{
		WorkspaceID: ws.ID, UserID: userID, RequestID: rejReq.ID,
		Count: 1, IdempotencyKey: "rg-" + NewID(),
	})
	if domain.AsError(err).Code != domain.CodeInvalidState {
		t.Fatalf("closed→generate: expected INVALID_STATE, got %v", err)
	}

	// 用例 2：fulfilled → closed（已入库后取消 → INVALID_STATE）
	fulReq := createRequest(t, s, ws.ID, userID, "将被通过", []string{kid})
	if _, err := s.Request.ContentRequestGenerate(ctx, ContentRequestGenerateReq{
		WorkspaceID: ws.ID, UserID: userID, RequestID: fulReq.ID,
		Count: 1, IdempotencyKey: "rg-" + NewID(),
	}); err != nil {
		t.Fatalf("generate: %v", err)
	}
	if _, err := s.Request.ContentRequestReview(ctx, ContentRequestReviewReq{
		WorkspaceID: ws.ID, UserID: userID, RequestID: fulReq.ID, Decision: "approved",
	}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if requestRowStatus(t, s, fulReq.ID) != "fulfilled" {
		t.Fatal("expected fulfilled")
	}
	_, err = s.Request.ContentRequestCancel(ctx, ContentRequestCancelReq{
		WorkspaceID: ws.ID, UserID: userID, RequestID: fulReq.ID,
	})
	if domain.AsError(err).Code != domain.CodeInvalidState {
		t.Fatalf("fulfilled→cancel: expected INVALID_STATE, got %v", err)
	}
	_, err = s.Request.ContentRequestGenerate(ctx, ContentRequestGenerateReq{
		WorkspaceID: ws.ID, UserID: userID, RequestID: fulReq.ID,
		Count: 1, IdempotencyKey: "rg-" + NewID(),
	})
	if domain.AsError(err).Code != domain.CodeInvalidState {
		t.Fatalf("fulfilled→generate: expected INVALID_STATE, got %v", err)
	}
}

// TestContentRequestCancel 覆盖用户取消：open → closed；已有 pending 审核项一并拒绝；
// 取消后不可再生成/审核。
func TestContentRequestCancel(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	ctx := context.Background()
	kid := createKnowledgeNode(t, s, ws.ID, "力学")
	seedQuizGenLLM(t, s, kid)

	req := createRequest(t, s, ws.ID, userID, "将被取消", []string{kid})
	if _, err := s.Request.ContentRequestGenerate(ctx, ContentRequestGenerateReq{
		WorkspaceID: ws.ID, UserID: userID, RequestID: req.ID,
		Count: 2, IdempotencyKey: "rg-" + NewID(),
	}); err != nil {
		t.Fatalf("generate: %v", err)
	}

	cancelled, err := s.Request.ContentRequestCancel(ctx, ContentRequestCancelReq{
		WorkspaceID: ws.ID, UserID: userID, RequestID: req.ID,
	})
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled.Status != "closed" {
		t.Fatalf("expected closed after cancel, got %q", cancelled.Status)
	}
	// pending 审核项一并拒绝
	items := requestReviewItems(t, s, req.ID)
	if len(items) != 1 || items[0].Status != "rejected" {
		t.Fatalf("expected review item rejected on cancel, got %+v", items)
	}
	// 取消后不可再生成/审核
	_, err = s.Request.ContentRequestGenerate(ctx, ContentRequestGenerateReq{
		WorkspaceID: ws.ID, UserID: userID, RequestID: req.ID,
		Count: 1, IdempotencyKey: "rg-" + NewID(),
	})
	if domain.AsError(err).Code != domain.CodeInvalidState {
		t.Fatalf("generate after cancel: expected INVALID_STATE, got %v", err)
	}
}

// TestContentRequestReviewScanFailure 覆盖安全扫描：生成的题目含 <script> 正文 →
// 审核通过被拒（INVALID_ARGUMENT），请求保持 open，题目不入库，审核项仍 pending。
func TestContentRequestReviewScanFailure(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	ctx := context.Background()
	kid := createKnowledgeNode(t, s, ws.ID, "力学")
	body := `{"questions":[` +
		`{"type":"single_choice","stem":"<script>alert(1)</script> 请选择正确答案","options":[{"key":"A","text":"A"},{"key":"B","text":"B"}],"answer":"A","analysis":"x","difficulty":2,"knowledge_ids":["KID"]}` +
		`]}`
	s.Agent.LLMFactory = func() (provider.LLMProvider, error) {
		return &stubLLM{content: strings.ReplaceAll(body, "KID", kid)}, nil
	}

	req := createRequest(t, s, ws.ID, userID, "恶意内容", []string{kid})
	if _, err := s.Request.ContentRequestGenerate(ctx, ContentRequestGenerateReq{
		WorkspaceID: ws.ID, UserID: userID, RequestID: req.ID,
		Count: 1, IdempotencyKey: "rg-" + NewID(),
	}); err != nil {
		t.Fatalf("generate: %v", err)
	}

	_, err := s.Request.ContentRequestReview(ctx, ContentRequestReviewReq{
		WorkspaceID: ws.ID, UserID: userID, RequestID: req.ID, Decision: "approved",
	})
	de := domain.AsError(err)
	if de.Code != domain.CodeInvalidArgument || !strings.Contains(de.Message, "安全扫描") {
		t.Fatalf("expected INVALID_ARGUMENT with scan hint, got %v", err)
	}
	if requestRowStatus(t, s, req.ID) != "open" {
		t.Fatalf("expected request still open after scan failure, got %q", requestRowStatus(t, s, req.ID))
	}
	if n := requestQuestionCount(t, s, ws.ID); n != 0 {
		t.Fatalf("expected 0 questions after scan failure, got %d", n)
	}
	items := requestReviewItems(t, s, req.ID)
	if len(items) != 1 || items[0].Status != "pending" {
		t.Fatalf("expected review item still pending, got %+v", items)
	}
}

// TestContentRequestReviewPermission 覆盖权限：非请求所有者不能生成/审核/取消（FORBIDDEN）。
func TestContentRequestReviewPermission(t *testing.T) {
	s, _ := newTestServices(t)
	ws, ownerID := createWorkspace(t, s)
	ctx := context.Background()
	kid := createKnowledgeNode(t, s, ws.ID, "力学")

	other, err := s.Workspace.UserCreate(ctx, UserCreateReq{WorkspaceID: ws.ID, DisplayName: "学生B", Role: "student"})
	if err != nil {
		t.Fatalf("create other user: %v", err)
	}
	req := createRequest(t, s, ws.ID, ownerID, "求题", []string{kid})

	_, err = s.Request.ContentRequestGenerate(ctx, ContentRequestGenerateReq{
		WorkspaceID: ws.ID, UserID: other.ID, RequestID: req.ID,
		Count: 1, IdempotencyKey: "rg-" + NewID(),
	})
	if domain.AsError(err).Code != domain.CodeForbidden {
		t.Fatalf("non-owner generate: expected FORBIDDEN, got %v", err)
	}
	_, err = s.Request.ContentRequestReview(ctx, ContentRequestReviewReq{
		WorkspaceID: ws.ID, UserID: other.ID, RequestID: req.ID, Decision: "approved",
	})
	if domain.AsError(err).Code != domain.CodeForbidden {
		t.Fatalf("non-owner review: expected FORBIDDEN, got %v", err)
	}
	_, err = s.Request.ContentRequestCancel(ctx, ContentRequestCancelReq{
		WorkspaceID: ws.ID, UserID: other.ID, RequestID: req.ID,
	})
	if domain.AsError(err).Code != domain.CodeForbidden {
		t.Fatalf("non-owner cancel: expected FORBIDDEN, got %v", err)
	}
}

// TestContentRequestReviewNotPending 覆盖审核前置校验：
// 未生成草稿就审核 → INVALID_STATE；同一请求重复生成（pending 已存在）→ CONFLICT。
func TestContentRequestReviewNotPending(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	ctx := context.Background()
	kid := createKnowledgeNode(t, s, ws.ID, "力学")
	seedQuizGenLLM(t, s, kid)

	// 未生成即审核
	req := createRequest(t, s, ws.ID, userID, "未生成就审核", []string{kid})
	_, err := s.Request.ContentRequestReview(ctx, ContentRequestReviewReq{
		WorkspaceID: ws.ID, UserID: userID, RequestID: req.ID, Decision: "approved",
	})
	if domain.AsError(err).Code != domain.CodeInvalidState {
		t.Fatalf("review without drafts: expected INVALID_STATE, got %v", err)
	}

	// 已生成后重复生成（不同幂等键）→ CONFLICT
	if _, err := s.Request.ContentRequestGenerate(ctx, ContentRequestGenerateReq{
		WorkspaceID: ws.ID, UserID: userID, RequestID: req.ID,
		Count: 1, IdempotencyKey: "rg-" + NewID(),
	}); err != nil {
		t.Fatalf("first generate: %v", err)
	}
	_, err = s.Request.ContentRequestGenerate(ctx, ContentRequestGenerateReq{
		WorkspaceID: ws.ID, UserID: userID, RequestID: req.ID,
		Count: 1, IdempotencyKey: "rg-" + NewID(),
	})
	if domain.AsError(err).Code != domain.CodeConflict {
		t.Fatalf("duplicate generate: expected CONFLICT, got %v", err)
	}
}

// TestContentRequestGenerateValidation 覆盖生成入参校验与请求不存在。
func TestContentRequestGenerateValidation(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	ctx := context.Background()
	kid := createKnowledgeNode(t, s, ws.ID, "力学")
	seedQuizGenLLM(t, s, kid)
	req := createRequest(t, s, ws.ID, userID, "求题", []string{kid})

	// count 越界
	_, err := s.Request.ContentRequestGenerate(ctx, ContentRequestGenerateReq{
		WorkspaceID: ws.ID, UserID: userID, RequestID: req.ID,
		Count: 0, IdempotencyKey: "rg-" + NewID(),
	})
	if domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("count=0: expected INVALID_ARGUMENT, got %v", err)
	}
	// 请求不存在
	_, err = s.Request.ContentRequestGenerate(ctx, ContentRequestGenerateReq{
		WorkspaceID: ws.ID, UserID: userID, RequestID: NewID(),
		Count: 1, IdempotencyKey: "rg-" + NewID(),
	})
	if domain.AsError(err).Code != domain.CodeNotFound {
		t.Fatalf("missing request: expected NOT_FOUND, got %v", err)
	}
}

// TestContentRequestList 覆盖我的请求列表：按 user_id 过滤；空过滤返回全部。
func TestContentRequestList(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	ctx := context.Background()
	kid := createKnowledgeNode(t, s, ws.ID, "力学")

	other, err := s.Workspace.UserCreate(ctx, UserCreateReq{WorkspaceID: ws.ID, DisplayName: "学生B", Role: "student"})
	if err != nil {
		t.Fatalf("create other user: %v", err)
	}
	r1 := createRequest(t, s, ws.ID, userID, "我的求题", []string{kid})
	r2 := createRequest(t, s, ws.ID, other.ID, "别人的求题", []string{kid})

	// 我的请求列表
	mine, err := s.Request.ContentRequestList(ctx, ContentRequestListReq{WorkspaceID: ws.ID, UserID: userID})
	if err != nil {
		t.Fatalf("list mine: %v", err)
	}
	if len(mine) != 1 || mine[0].ID != r1.ID || mine[0].Description != "我的求题" {
		t.Fatalf("expected only my request, got %+v", mine)
	}
	if mine[0].Status != "open" {
		t.Fatalf("expected status=open, got %q", mine[0].Status)
	}

	// 全部
	all, err := s.Request.ContentRequestList(ctx, ContentRequestListReq{WorkspaceID: ws.ID})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(all))
	}
	ids := map[string]bool{}
	for _, r := range all {
		ids[r.ID] = true
	}
	if !ids[r1.ID] || !ids[r2.ID] {
		t.Fatalf("expected both requests, got %+v", all)
	}
}

// TestContentRequestDomainTransition 覆盖领域状态机函数本身（纯函数）。
func TestContentRequestDomainTransition(t *testing.T) {
	cases := []struct {
		from, to string
		ok       bool
	}{
		{"open", "fulfilled", true},
		{"open", "closed", true},
		{"closed", "fulfilled", false},
		{"fulfilled", "open", false},
		{"fulfilled", "closed", false},
		{"closed", "open", false},
		{"open", "open", false},
		{"", "fulfilled", false},
	}
	for _, c := range cases {
		if got := domain.ContentRequestCanTransition(c.from, c.to); got != c.ok {
			t.Fatalf("ContentRequestCanTransition(%q, %q) = %v, want %v", c.from, c.to, got, c.ok)
		}
	}
	if !domain.ValidContentRequestStatus("open") || !domain.ValidContentRequestStatus("fulfilled") ||
		!domain.ValidContentRequestStatus("closed") {
		t.Fatal("expected all 4.20 statuses valid")
	}
	if domain.ValidContentRequestStatus("pending") {
		t.Fatal("pending is NOT a content_requests status (4.20 enum has no such value)")
	}
}
