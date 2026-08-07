package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lumo/internal/domain"
)

// shareQuestionSetup 建服务 + 工作区 + 默认用户 + 一道已发布题目。
func shareQuestionSetup(t *testing.T) (*Services, string, string, string) {
	t.Helper()
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	q := publishedQuestion(t, s, ws.ID, scPayload("分享-"+NewID(), "B"))
	return s, ws.ID, userID, q.ID
}

// countShareRows 直接统计 shares 行数。
func countShareRows(t *testing.T, s *Services) int {
	t.Helper()
	var n int
	if err := s.Repo.DB().QueryRowContext(ctx(), `SELECT count(*) FROM shares`).Scan(&n); err != nil {
		t.Fatalf("count shares: %v", err)
	}
	return n
}

// TestShareCreateHappyDefaultTTL：合法题目引用 → Share（token 32 位 hex + 默认 7 天过期 + scan_result=clean）。
func TestShareCreateHappyDefaultTTL(t *testing.T) {
	s, wsID, userID, qID := shareQuestionSetup(t)

	sh, err := s.Share.ShareCreate(ctx(), ShareCreateReq{
		WorkspaceID: wsID, UserID: userID, RefType: "question", RefID: qID,
		IdempotencyKey: "share-happy-0001",
	})
	if err != nil {
		t.Fatalf("create share: %v", err)
	}
	if sh.ID == "" || sh.WorkspaceID != wsID || sh.UserID != userID ||
		sh.RefType != "question" || sh.RefID != qID {
		t.Fatalf("unexpected share: %+v", sh)
	}
	if len(sh.Token) != 32 {
		t.Fatalf("token 应为 32 位 hex，got %q", sh.Token)
	}
	if sh.ExpiresAt == nil {
		t.Fatalf("默认 ttl 应有 expires_at")
	}
	exp, err := time.Parse(time.RFC3339, *sh.ExpiresAt)
	if err != nil {
		t.Fatalf("bad expires_at: %v", err)
	}
	delta := time.Until(exp)
	if delta < 6*24*time.Hour || delta > 8*24*time.Hour {
		t.Fatalf("默认有效期应约 7 天，实际 %v", delta)
	}
	if sh.ScanResult == nil || *sh.ScanResult != "clean" {
		t.Fatalf("scan_result 应为 clean，got %+v", sh.ScanResult)
	}
	if sh.RevokedAt != nil || sh.CreatedAt == "" {
		t.Fatalf("新分享 revoked_at 应为空且 created_at 非空，got %+v", sh)
	}
}

// TestShareCreateTTLVariants：ttl=1 → +1 天；0/-1 → 永久（expires_at NULL）；非法值 → INVALID_ARGUMENT。
func TestShareCreateTTLVariants(t *testing.T) {
	s, wsID, userID, qID := shareQuestionSetup(t)

	sh, err := s.Share.ShareCreate(ctx(), ShareCreateReq{
		WorkspaceID: wsID, UserID: userID, RefType: "question", RefID: qID,
		TTLDays: intPtr(1), IdempotencyKey: "share-ttl-1d-001",
	})
	if err != nil {
		t.Fatalf("ttl=1: %v", err)
	}
	if sh.ExpiresAt == nil {
		t.Fatalf("ttl=1 应有 expires_at")
	}
	exp, _ := time.Parse(time.RFC3339, *sh.ExpiresAt)
	if delta := time.Until(exp); delta < 20*time.Hour || delta > 28*time.Hour {
		t.Fatalf("ttl=1 有效期应约 1 天，实际 %v", delta)
	}

	for _, perm := range []int{0, -1} {
		ps, err := s.Share.ShareCreate(ctx(), ShareCreateReq{
			WorkspaceID: wsID, UserID: userID, RefType: "question", RefID: qID,
			TTLDays: intPtr(perm), IdempotencyKey: "share-perm-" + NewID(),
		})
		if err != nil {
			t.Fatalf("permanent ttl=%d: %v", perm, err)
		}
		if ps.ExpiresAt != nil {
			t.Fatalf("ttl=%d 应为永久（expires_at NULL），got %+v", perm, ps.ExpiresAt)
		}
	}

	// 非法 ttl → INVALID_ARGUMENT，且不创建分享行。
	if _, err := s.Share.ShareCreate(ctx(), ShareCreateReq{
		WorkspaceID: wsID, UserID: userID, RefType: "question", RefID: qID,
		TTLDays: intPtr(2), IdempotencyKey: "share-badttl-001",
	}); err == nil || domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("ttl=2 应 INVALID_ARGUMENT，got %v", err)
	}
}

// TestShareCreateScanRejected：内容含可执行脚本 → 拒绝发布，不创建 shares 行。
func TestShareCreateScanRejected(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := ctx()
	ws, userID := createWorkspace(t, s)
	q := publishedQuestion(t, s, ws.ID, scPayload("分享<script>alert(1)</script>", "B"))

	_, err := s.Share.ShareCreate(ctx, ShareCreateReq{
		WorkspaceID: ws.ID, UserID: userID, RefType: "question", RefID: q.ID,
		IdempotencyKey: "share-scanfail-1",
	})
	de := domain.AsError(err)
	if de == nil || de.Code != domain.CodeInvalidArgument {
		t.Fatalf("扫描失败应 INVALID_ARGUMENT，got %v", err)
	}
	if !strings.Contains(de.Message, "安全扫描") {
		t.Fatalf("错误消息应说明安全扫描，got %q", de.Message)
	}
	if n := countShareRows(t, s); n != 0 {
		t.Fatalf("扫描失败不应创建分享行，got %d rows", n)
	}
	// 无 token、无链接（QA failure 路径）。
	if rows, err := s.Repo.ListSharesByWorkspace(ctx, ws.ID); err != nil || len(rows) != 0 {
		t.Fatalf("工作区不应有分享行，got %d %v", len(rows), err)
	}
}

// TestShareCreateScanCache：同一引用 + 同一内容 → content_scan_results 只写一行，二次分享复用缓存。
func TestShareCreateScanCache(t *testing.T) {
	s, wsID, userID, qID := shareQuestionSetup(t)

	if _, err := s.Share.ShareCreate(ctx(), ShareCreateReq{
		WorkspaceID: wsID, UserID: userID, RefType: "question", RefID: qID,
		IdempotencyKey: "share-cache-0001",
	}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	var n int
	if err := s.Repo.DB().QueryRowContext(ctx(),
		`SELECT count(*) FROM content_scan_results WHERE ref_type='question' AND ref_id=?`, qID).Scan(&n); err != nil {
		t.Fatalf("count scan results: %v", err)
	}
	if n != 1 {
		t.Fatalf("首次分享应写入 1 行扫描缓存，got %d", n)
	}
	// 再次分享同一引用（不同幂等键）→ 缓存命中，不新增扫描行。
	if _, err := s.Share.ShareCreate(ctx(), ShareCreateReq{
		WorkspaceID: wsID, UserID: userID, RefType: "question", RefID: qID,
		TTLDays: intPtr(30), IdempotencyKey: "share-cache-0002",
	}); err != nil {
		t.Fatalf("second create: %v", err)
	}
	if err := s.Repo.DB().QueryRowContext(ctx(),
		`SELECT count(*) FROM content_scan_results WHERE ref_type='question' AND ref_id=?`, qID).Scan(&n); err != nil {
		t.Fatalf("count scan results: %v", err)
	}
	if n != 1 {
		t.Fatalf("缓存命中后仍只有 1 行扫描缓存，got %d", n)
	}
}

// TestShareCreateValidation：非法 ref_type / 不存在的 ref_id / 非法 ref_id 格式 → 对应错误。
func TestShareCreateValidation(t *testing.T) {
	s, wsID, userID, qID := shareQuestionSetup(t)

	if _, err := s.Share.ShareCreate(ctx(), ShareCreateReq{
		WorkspaceID: wsID, UserID: userID, RefType: "book", RefID: qID,
		IdempotencyKey: "share-badtype-1",
	}); err == nil || domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("非法 ref_type 应 INVALID_ARGUMENT，got %v", err)
	}
	if _, err := s.Share.ShareCreate(ctx(), ShareCreateReq{
		WorkspaceID: wsID, UserID: userID, RefType: "question", RefID: NewID(),
		IdempotencyKey: "share-noq-0001",
	}); err == nil || domain.AsError(err).Code != domain.CodeNotFound {
		t.Fatalf("不存在题目应 NOT_FOUND，got %v", err)
	}
	if _, err := s.Share.ShareCreate(ctx(), ShareCreateReq{
		WorkspaceID: wsID, UserID: userID, RefType: "question", RefID: "nope",
		IdempotencyKey: "share-badid-001",
	}); err == nil || domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("非法 ref_id 应 INVALID_ARGUMENT，got %v", err)
	}
	if _, err := s.Share.ShareCreate(ctx(), ShareCreateReq{
		WorkspaceID: wsID, UserID: "", RefType: "question", RefID: qID,
		IdempotencyKey: "share-nouid-001",
	}); err == nil || domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("缺 user_id 应 INVALID_ARGUMENT，got %v", err)
	}
}

// TestShareCreateRefKinds：paper / flashcard_pack / note 引用均能创建分享（引用校验分派正确）。
func TestShareCreateRefKinds(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	ctx := ctx()

	// note 引用。
	n := helperNote(t, s, ws, userID, "分享笔记", "正文")
	ns, err := s.Share.ShareCreate(ctx, ShareCreateReq{
		WorkspaceID: ws.ID, UserID: userID, RefType: "note", RefID: n.ID,
		IdempotencyKey: "share-note-0001",
	})
	if err != nil || ns.RefType != "note" {
		t.Fatalf("note share: %+v %v", ns, err)
	}

	// flashcard_pack 引用（ref_id = 单张闪卡 ID 即构成包）。
	f := helperFlashcard(t, s, ws, userID, "正面", "背面")
	fs, err := s.Share.ShareCreate(ctx, ShareCreateReq{
		WorkspaceID: ws.ID, UserID: userID, RefType: "flashcard_pack", RefID: f.ID,
		IdempotencyKey: "share-pack-0001",
	})
	if err != nil || fs.RefType != "flashcard_pack" {
		t.Fatalf("flashcard_pack share: %+v %v", fs, err)
	}

	// paper 引用。
	q := publishedQuestion(t, s, ws.ID, scPayload("纸卷题", "A"))
	paper, err := s.Exam.ExamPaperCreate(ctx, ExamPaperCreateReq{
		WorkspaceID: ws.ID, UserID: userID, Title: "分享卷",
		ConfigJSON: examPaperConfig(30, []map[string]any{
			examSection("一", 1, []string{q.CurrentVersion.ID}, 10),
		}),
		IdempotencyKey: "ep-share-0001",
	})
	if err != nil {
		t.Fatalf("create paper: %v", err)
	}
	ps, err := s.Share.ShareCreate(ctx, ShareCreateReq{
		WorkspaceID: ws.ID, UserID: userID, RefType: "paper", RefID: paper.ID,
		IdempotencyKey: "share-paper-001",
	})
	if err != nil || ps.RefType != "paper" {
		t.Fatalf("paper share: %+v %v", ps, err)
	}

	// 已删除笔记不可分享。
	del, err := s.Note.NoteDelete(ctx, NoteDeleteReq{
		WorkspaceID: ws.ID, NoteID: n.ID, Version: n.Version,
	})
	if err != nil {
		t.Fatalf("delete note: %v", err)
	}
	_ = del
	if _, err := s.Share.ShareCreate(ctx, ShareCreateReq{
		WorkspaceID: ws.ID, UserID: userID, RefType: "note", RefID: n.ID,
		IdempotencyKey: "share-delnote-1",
	}); err == nil || domain.AsError(err).Code != domain.CodeInvalidState {
		t.Fatalf("已删除笔记应 INVALID_STATE，got %v", err)
	}
}

// TestShareRevokeInvalidatesLink：撤销后 ShareResolve 立即返回 SHARE_EXPIRED。
func TestShareRevokeInvalidatesLink(t *testing.T) {
	s, wsID, userID, qID := shareQuestionSetup(t)
	ctx := ctx()

	sh, err := s.Share.ShareCreate(ctx, ShareCreateReq{
		WorkspaceID: wsID, UserID: userID, RefType: "question", RefID: qID,
		IdempotencyKey: "share-revoke-001",
	})
	if err != nil {
		t.Fatalf("create share: %v", err)
	}
	del, err := s.Share.ShareRevoke(ctx, ShareRevokeReq{WorkspaceID: wsID, UserID: userID, ShareID: sh.ID})
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if !del.Deleted || del.DeletedAt == "" {
		t.Fatalf("revoke 应返回 deleted_at，got %+v", del)
	}
	if _, err := s.Share.ShareResolve(ctx, ShareResolveReq{Token: sh.Token}); err == nil ||
		domain.AsError(err).Code != domain.CodeShareExpired {
		t.Fatalf("撤销后 resolve 应 SHARE_EXPIRED，got %v", err)
	}
	// 重复撤销 → INVALID_STATE。
	if _, err := s.Share.ShareRevoke(ctx, ShareRevokeReq{WorkspaceID: wsID, UserID: userID, ShareID: sh.ID}); err == nil ||
		domain.AsError(err).Code != domain.CodeInvalidState {
		t.Fatalf("重复撤销应 INVALID_STATE，got %v", err)
	}
}

// TestShareResolveExpired：有效期 1 天，回拨 expires_at 后 resolve → SHARE_EXPIRED。
func TestShareResolveExpired(t *testing.T) {
	s, wsID, userID, qID := shareQuestionSetup(t)
	ctx := ctx()

	sh, err := s.Share.ShareCreate(ctx, ShareCreateReq{
		WorkspaceID: wsID, UserID: userID, RefType: "question", RefID: qID,
		TTLDays: intPtr(1), IdempotencyKey: "share-expire-001",
	})
	if err != nil {
		t.Fatalf("create share: %v", err)
	}
	// 把 expires_at 拨回 2 小时前模拟过期。
	past := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	if _, err := s.Repo.DB().ExecContext(ctx,
		`UPDATE shares SET expires_at = ? WHERE id = ?`, past, sh.ID); err != nil {
		t.Fatalf("backdate expires_at: %v", err)
	}
	if _, err := s.Share.ShareResolve(ctx, ShareResolveReq{Token: sh.Token}); err == nil ||
		domain.AsError(err).Code != domain.CodeShareExpired {
		t.Fatalf("过期后 resolve 应 SHARE_EXPIRED，got %v", err)
	}
}

// TestShareResolveValidExport：有效 token → Share + 受限通道下载路径；exports/ 下导出文件存在且内容为 JSON。
func TestShareResolveValidExport(t *testing.T) {
	s, wsID, userID, qID := shareQuestionSetup(t)
	ctx := ctx()

	sh, err := s.Share.ShareCreate(ctx, ShareCreateReq{
		WorkspaceID: wsID, UserID: userID, RefType: "question", RefID: qID,
		IdempotencyKey: "share-export-001",
	})
	if err != nil {
		t.Fatalf("create share: %v", err)
	}
	res, err := s.Share.ShareResolve(ctx, ShareResolveReq{Token: sh.Token})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.Share == nil || res.Share.Token != sh.Token {
		t.Fatalf("resolve 应返回 share，got %+v", res)
	}
	wantPath := "exports" + string(os.PathSeparator) + "share-" + sh.Token + ".json"
	if res.DownloadPath != wantPath {
		t.Fatalf("download_path 应为 %q，got %q", wantPath, res.DownloadPath)
	}
	fullPath := filepath.Join(s.Cfg.DataDir, res.DownloadPath)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatalf("导出文件应存在: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("导出文件应为 JSON: %v", err)
	}
	if out["id"] != qID {
		t.Fatalf("导出内容应为被分享题目，got %v", out["id"])
	}
}
