package service

import (
	"archive/zip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lumo/internal/domain"
)

// helperFlashcard 创建一个手动来源的闪卡。
func helperFlashcard(t *testing.T, s *Services, ws *Workspace, userID, front, back string) *Flashcard {
	t.Helper()
	f, err := s.Flashcard.FlashcardCreate(context.Background(), FlashcardCreateReq{
		WorkspaceID: ws.ID, UserID: userID, Source: "manual", Front: front, Back: back,
		CardType: "basic",
	})
	if err != nil {
		t.Fatalf("create flashcard: %v", err)
	}
	return f
}

func TestFlashcardCreateValidation(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)

	f, err := s.Flashcard.FlashcardCreate(ctx, FlashcardCreateReq{
		WorkspaceID: ws.ID, UserID: userID, Source: "manual", Front: "什么是闭包？", Back: "函数与其词法环境的组合",
		CardType: "basic",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if f.ID == "" || f.WorkspaceID != ws.ID || f.Source != "manual" {
		t.Fatalf("bad create result: %+v", f)
	}
	if f.State != "learning" || f.Repetition != 0 || f.IntervalDays != 0 || f.EaseFactor != 2.5 {
		t.Fatalf("fresh card scheduling wrong: %+v", f)
	}
	if f.DueAt == "" || f.Version != 1 {
		t.Fatalf("fresh card meta wrong: %+v", f)
	}

	cases := []FlashcardCreateReq{
		{WorkspaceID: ws.ID, UserID: userID, Source: "video", Front: "x", Back: "y"},                       // 非法来源
		{WorkspaceID: ws.ID, UserID: userID, Source: "manual", Front: "", Back: "y"},                       // 空 front
		{WorkspaceID: ws.ID, UserID: userID, Source: "manual", Front: "x", Back: ""},                       // 空 back
		{WorkspaceID: ws.ID, UserID: userID, Source: "manual", Front: "x", Back: "y", CardType: "video"},   // 非法题型
		{WorkspaceID: ws.ID, Source: "manual", Front: "x", Back: "y"},                                      // 缺 user_id
	}
	for i, req := range cases {
		if _, err := s.Flashcard.FlashcardCreate(ctx, req); err == nil {
			t.Fatalf("case %d: expected error, got nil", i)
		} else if domain.AsError(err).Code != domain.CodeInvalidArgument {
			t.Fatalf("case %d: want INVALID_ARGUMENT, got %s", i, domain.AsError(err).Code)
		}
	}
}

func TestFlashcardReviewSM2AndRatingEnum(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)
	f := helperFlashcard(t, s, ws, userID, "1+1=?", "2")

	// 非法评级 → INVALID_ARGUMENT
	if _, err := s.Flashcard.FlashcardReview(ctx, FlashcardReviewReq{
		WorkspaceID: ws.ID, FlashcardID: f.ID, Rating: "easy", IdempotencyKey: "r-" + NewID(),
	}); err == nil {
		t.Fatal("expected INVALID_ARGUMENT for rating easy")
	} else if domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("want INVALID_ARGUMENT, got %s", domain.AsError(err).Code)
	}

	// again → repetition 0, 间隔 1 天
	a, err := s.Flashcard.FlashcardReview(ctx, FlashcardReviewReq{
		WorkspaceID: ws.ID, FlashcardID: f.ID, Rating: "again", IdempotencyKey: "r-" + NewID(),
	})
	if err != nil {
		t.Fatalf("review again: %v", err)
	}
	if a.Repetition != 0 || a.IntervalDays != 1 || a.State != "learning" {
		t.Fatalf("again result wrong: %+v", a)
	}
	if a.Version != f.Version+1 {
		t.Fatalf("version should bump, got %d", a.Version)
	}

	// good → repetition 1, 间隔 1 天, 进入 review
	g1, err := s.Flashcard.FlashcardReview(ctx, FlashcardReviewReq{
		WorkspaceID: ws.ID, FlashcardID: f.ID, Rating: "good", IdempotencyKey: "r-" + NewID(),
	})
	if err != nil {
		t.Fatalf("review good: %v", err)
	}
	if g1.Repetition != 1 || g1.IntervalDays != 1 || g1.State != "review" {
		t.Fatalf("good result wrong: %+v", g1)
	}

	// 第二次 good → repetition 2, 间隔 3 天
	g2, err := s.Flashcard.FlashcardReview(ctx, FlashcardReviewReq{
		WorkspaceID: ws.ID, FlashcardID: f.ID, Rating: "good", IdempotencyKey: "r-" + NewID(),
	})
	if err != nil {
		t.Fatalf("review good 2: %v", err)
	}
	if g2.Repetition != 2 || g2.IntervalDays != 3 {
		t.Fatalf("good2 result wrong: %+v", g2)
	}

	// 复习事件只追加
	var cnt int
	if err := s.Repo.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM flashcard_reviews WHERE flashcard_id = ?`, f.ID).Scan(&cnt); err != nil {
		t.Fatalf("count reviews: %v", err)
	}
	if cnt != 3 {
		t.Fatalf("expected 3 review rows, got %d", cnt)
	}
}

func TestFlashcardReviewIdempotent(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)
	f := helperFlashcard(t, s, ws, userID, "前端卡", "后端")

	key := "r-idem-" + NewID()
	a1, err := s.Flashcard.FlashcardReview(ctx, FlashcardReviewReq{
		WorkspaceID: ws.ID, FlashcardID: f.ID, Rating: "good", IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("first review: %v", err)
	}
	a2, err := s.Flashcard.FlashcardReview(ctx, FlashcardReviewReq{
		WorkspaceID: ws.ID, FlashcardID: f.ID, Rating: "good", IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("replay review: %v", err)
	}
	if a1.ID != a2.ID || a1.Repetition != a2.Repetition || a1.IntervalDays != a2.IntervalDays {
		t.Fatalf("idempotent replay diverged: %+v vs %+v", a1, a2)
	}
	var cnt int
	if err := s.Repo.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM flashcard_reviews WHERE flashcard_id = ?`, f.ID).Scan(&cnt); err != nil {
		t.Fatalf("count: %v", err)
	}
	if cnt != 1 {
		t.Fatalf("expected 1 review row after replay, got %d", cnt)
	}
}

func TestFlashcardReviewArchivedCard(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)
	f := helperFlashcard(t, s, ws, userID, "归档卡", "不可复习")

	res, err := s.Flashcard.FlashcardBatch(ctx, FlashcardBatchReq{
		WorkspaceID: ws.ID, Action: "archive", IDs: []string{f.ID},
	})
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if res.SuccessCount != 1 || res.ErrorCount != 0 {
		t.Fatalf("archive result: %+v", res)
	}

	if _, err := s.Flashcard.FlashcardReview(ctx, FlashcardReviewReq{
		WorkspaceID: ws.ID, FlashcardID: f.ID, Rating: "good", IdempotencyKey: "r-" + NewID(),
	}); err == nil {
		t.Fatal("expected error reviewing archived card")
	} else if domain.AsError(err).Code != domain.CodeInvalidState {
		t.Fatalf("want INVALID_STATE, got %s", domain.AsError(err).Code)
	}
}

func TestFlashcardListDuePublishesEvent(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)

	// 无到期卡 → 不发布事件
	items, err := s.Flashcard.FlashcardListDue(ctx, FlashcardListDueReq{WorkspaceID: ws.ID, UserID: userID})
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected 0 due, got %d", len(items))
	}
	var n0 int
	if err := s.Repo.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM notifications WHERE user_id = ? AND kind = 'flashcard:due'`, userID).Scan(&n0); err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	if n0 != 0 {
		t.Fatalf("expected no flashcard:due notification, got %d", n0)
	}

	// 两张到期卡 → 列表返回 + flashcard:due 事件落库
	helperFlashcard(t, s, ws, userID, "卡一", "面一")
	helperFlashcard(t, s, ws, userID, "卡二", "面二")
	items, err = s.Flashcard.FlashcardListDue(ctx, FlashcardListDueReq{WorkspaceID: ws.ID, UserID: userID, Limit: 10})
	if err != nil {
		t.Fatalf("list due 2: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 due, got %d", len(items))
	}

	var kind, args string
	if err := s.Repo.DB().QueryRowContext(ctx,
		`SELECT kind, body_args_json FROM notifications WHERE user_id = ? AND kind = 'flashcard:due' ORDER BY created_at DESC LIMIT 1`,
		userID).Scan(&kind, &args); err != nil {
		t.Fatalf("read notification: %v", err)
	}
	if kind != "flashcard:due" {
		t.Fatalf("kind = %q, want flashcard:due", kind)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(args), &payload); err != nil {
		t.Fatalf("parse body_args: %v", err)
	}
	if dc, ok := payload["due_count"].(float64); !ok || int(dc) != 2 {
		t.Fatalf("due_count = %v, want 2", payload["due_count"])
	}
}

func TestFlashcardBatchActions(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)

	f1 := helperFlashcard(t, s, ws, userID, "批一", "1")
	f2 := helperFlashcard(t, s, ws, userID, "批二", "2")

	// 复习 f1 到 rep 1 / interval 3，供 reset 检验
	_, err := s.Flashcard.FlashcardReview(ctx, FlashcardReviewReq{
		WorkspaceID: ws.ID, FlashcardID: f1.ID, Rating: "good", IdempotencyKey: "r-" + NewID(),
	})
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	_, err = s.Flashcard.FlashcardReview(ctx, FlashcardReviewReq{
		WorkspaceID: ws.ID, FlashcardID: f1.ID, Rating: "good", IdempotencyKey: "r-" + NewID(),
	})
	if err != nil {
		t.Fatalf("review 2: %v", err)
	}

	// archive
	res, err := s.Flashcard.FlashcardBatch(ctx, FlashcardBatchReq{
		WorkspaceID: ws.ID, Action: "archive", IDs: []string{f1.ID, "no-such-id"},
	})
	if err != nil {
		t.Fatalf("batch archive: %v", err)
	}
	if res.SuccessCount != 1 || res.ErrorCount != 1 {
		t.Fatalf("archive result: %+v", res)
	}
	row, err := s.Repo.GetFlashcard(ctx, ws.ID, f1.ID)
	if err != nil || row == nil {
		t.Fatalf("get f1: %v", err)
	}
	if row.State != "archived" {
		t.Fatalf("f1 state = %q, want archived", row.State)
	}

	// reset 恢复学习
	res, err = s.Flashcard.FlashcardBatch(ctx, FlashcardBatchReq{
		WorkspaceID: ws.ID, Action: "reset", IDs: []string{f1.ID},
	})
	if err != nil {
		t.Fatalf("batch reset: %v", err)
	}
	if res.SuccessCount != 1 {
		t.Fatalf("reset result: %+v", res)
	}
	row, err = s.Repo.GetFlashcard(ctx, ws.ID, f1.ID)
	if err != nil || row == nil {
		t.Fatalf("get f1 after reset: %v", err)
	}
	if row.State != "learning" || row.Repetition != 0 || row.IntervalDays != 0 || row.EaseFactor != 2.5 {
		t.Fatalf("f1 after reset: %+v", row)
	}

	// delete 软删除
	res, err = s.Flashcard.FlashcardBatch(ctx, FlashcardBatchReq{
		WorkspaceID: ws.ID, Action: "delete", IDs: []string{f2.ID},
	})
	if err != nil {
		t.Fatalf("batch delete: %v", err)
	}
	if res.SuccessCount != 1 {
		t.Fatalf("delete result: %+v", res)
	}
	row, err = s.Repo.GetFlashcard(ctx, ws.ID, f2.ID)
	if err != nil {
		t.Fatalf("get f2: %v", err)
	}
	if row == nil || row.DeletedAt == nil {
		t.Fatalf("f2 should be soft-deleted, got %+v", row)
	}
}

func TestFlashcardGenerateFromQuestionVersion(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)

	q, err := s.Knowledge.QuestionCreateDraft(ctx, QuestionCreateDraftReq{
		WorkspaceID: ws.ID, Payload: scPayload("1+1=?", "A"), IdempotencyKey: "q-" + NewID(),
	})
	if err != nil {
		t.Fatalf("create question: %v", err)
	}
	versionID := q.CurrentVersion.ID

	cards, err := s.Flashcard.FlashcardGenerate(ctx, FlashcardGenerateReq{
		WorkspaceID: ws.ID, UserID: userID, SourceRef: versionID, IdempotencyKey: "gen-" + NewID(),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(cards) != 1 {
		t.Fatalf("expected 1 card, got %d", len(cards))
	}
	c := cards[0]
	if c.SourceRef != versionID {
		t.Fatalf("source_ref = %q, want question version %q", c.SourceRef, versionID)
	}
	if c.Source != "manual" {
		t.Fatalf("question-sourced card source = %q, want manual", c.Source)
	}
	if c.CardType != "choice" {
		t.Fatalf("single_choice → card_type choice, got %q", c.CardType)
	}
	if !strings.Contains(c.Front, "1+1=?") {
		t.Fatalf("front missing stem: %q", c.Front)
	}
	if !strings.Contains(c.Back, "A") {
		t.Fatalf("back missing answer: %q", c.Back)
	}

	// 幂等：同 key 不重复生成
	cards2, err := s.Flashcard.FlashcardGenerate(ctx, FlashcardGenerateReq{
		WorkspaceID: ws.ID, UserID: userID, SourceRef: versionID, IdempotencyKey: "gen-" + NewID(),
	})
	if err != nil {
		t.Fatalf("generate 2: %v", err)
	}
	if len(cards2) != 1 || cards2[0].ID != c.ID {
		t.Fatalf("idempotent generate diverged: %+v", cards2)
	}
}

func TestFlashcardGenerateNotFound(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)

	_, err := s.Flashcard.FlashcardGenerate(ctx, FlashcardGenerateReq{
		WorkspaceID: ws.ID, UserID: userID, SourceRef: "no-such-version", IdempotencyKey: "gen-" + NewID(),
	})
	if err == nil {
		t.Fatal("expected error for unknown source_ref")
	} else if domain.AsError(err).Code != domain.CodeNotFound {
		t.Fatalf("want NOT_FOUND, got %s", domain.AsError(err).Code)
	}
}

func TestFlashcardImportCsvValid(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)

	content := "front,back,card_type,rating\n" +
		"什么是闭包？,函数与其词法环境的组合,basic,\n" +
		"什么是指针？,存储变量地址的变量,basic,good\n"
	up, err := s.Import.UploadFile("flashcards.csv", []byte(content))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	batch, err := s.Flashcard.FlashcardImportCsv(ctx, FlashcardImportCsvReq{
		WorkspaceID: ws.ID, UserID: userID, FilePath: up.Path, IdempotencyKey: "csv-" + NewID(),
	})
	if err != nil {
		t.Fatalf("import csv: %v", err)
	}
	if batch.TotalCount != 2 || batch.ValidCount != 2 || batch.ErrorCount != 0 {
		t.Fatalf("batch counts wrong: %+v", batch)
	}
	if batch.Format != "flashcard_csv" {
		t.Fatalf("format = %q, want flashcard_csv", batch.Format)
	}
	if batch.ID == "" || batch.FileName == "" || !strings.HasSuffix(batch.FileName, ".csv") {
		t.Fatalf("batch meta wrong: %+v", batch)
	}

	// rating=good 的行初始调度到 1 天后，用未来的 due_before 才能同时列出
	items, err := s.Flashcard.FlashcardListDue(ctx, FlashcardListDueReq{
		WorkspaceID: ws.ID, UserID: userID, DueBefore: time.Now().UTC().Add(time.Hour * 48).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 imported cards, got %d", len(items))
	}
	// rating=good 的行：初始调度按 good 一次（rep 1 / interval 1）
	var sawGood bool
	for _, it := range items {
		if it.Front == "什么是指针？" && (it.Repetition != 1 || it.IntervalDays != 1) {
			t.Fatalf("good-seeded card wrong: %+v", it)
		}
		if it.Front == "什么是指针？" {
			sawGood = true
		}
	}
	if !sawGood {
		t.Fatal("missing pointer card")
	}
}

func TestFlashcardImportCsvInvalidRollsBackAtomically(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)

	// 一行合法 + 一行 rating 非法 → 整批回滚
	content := "front,back,card_type,rating\n" +
		"合法卡,面,basic,good\n" +
		"非法卡,面,basic,easy\n"
	up, err := s.Import.UploadFile("bad.csv", []byte(content))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	_, err = s.Flashcard.FlashcardImportCsv(ctx, FlashcardImportCsvReq{
		WorkspaceID: ws.ID, UserID: userID, FilePath: up.Path, IdempotencyKey: "csv-" + NewID(),
	})
	if err == nil {
		t.Fatal("expected INVALID_ARGUMENT for invalid rating row")
	} else if domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("want INVALID_ARGUMENT, got %s", domain.AsError(err).Code)
	}

	items, err := s.Flashcard.FlashcardListDue(ctx, FlashcardListDueReq{WorkspaceID: ws.ID, UserID: userID})
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected atomic rollback (0 cards), got %d", len(items))
	}
}

func TestFlashcardExportAnki(t *testing.T) {
	s, cfg := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)
	helperFlashcard(t, s, ws, userID, "导出正面", "导出背面")

	res, err := s.Flashcard.FlashcardExportAnki(ctx, FlashcardExportAnkiReq{
		WorkspaceID: ws.ID, IdempotencyKey: "anki-" + NewID(),
	})
	if err != nil {
		t.Fatalf("export anki: %v", err)
	}
	if res.Format != "apkg" || !strings.HasSuffix(res.FileName, ".apkg") || res.SizeBytes <= 0 {
		t.Fatalf("export result wrong: %+v", res)
	}
	full := filepath.Join(cfg.DataDir, res.Path)
	if _, err := os.Stat(full); err != nil {
		t.Fatalf("export file missing: %s: %v", full, err)
	}

	zr, err := zip.OpenReader(full)
	if err != nil {
		t.Fatalf("open apkg zip: %v", err)
	}
	defer zr.Close()
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	if !names["collection.anki2"] {
		t.Fatal("apkg missing collection.anki2")
	}
	if !names["media"] {
		t.Fatal("apkg missing media manifest")
	}

	// collection.anki2 必须是合法 SQLite 库
	rc, err := zr.Open("collection.anki2")
	if err != nil {
		t.Fatalf("open collection.anki2: %v", err)
	}
	defer rc.Close()
	header := make([]byte, 16)
	if _, err := rc.Read(header); err != nil {
		t.Fatalf("read collection header: %v", err)
	}
	if string(header) != "SQLite format 3\x00" {
		t.Fatalf("collection.anki2 is not SQLite: %q", string(header))
	}
}
