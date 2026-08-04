package service

import (
	"context"
	"testing"

	"lumo/internal/domain"
)

func TestImportPipeline(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)

	content := []byte(`# 示例题库

## 1. 单选：1+1=?
A. 1
B. 2
答案：B

## 2. 判断：太阳从西边升起。
A. 正确
B. 错误
答案：B

## 3. 填空：中国的首都是____。
答案：北京
`)
	up, err := s.Import.UploadFile("示例题库.md", content)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	preview, err := s.Import.LibraryPreflightImport(ctx, LibraryPreflightImportReq{
		WorkspaceID: ws.ID, FilePath: up.Path, Format: "markdown", IdempotencyKey: "imp-" + NewID(),
	})
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if preview.TotalCount != 3 || preview.ValidCount != 3 || preview.ErrorCount != 0 {
		t.Fatalf("unexpected preview: %+v", preview)
	}
	if len(preview.PreviewItems) != 3 {
		t.Fatalf("expected 3 preview items, got %d", len(preview.PreviewItems))
	}

	batch, err := s.Import.LibraryCommitImport(ctx, LibraryCommitImportReq{
		WorkspaceID: ws.ID, BatchID: preview.BatchID, IdempotencyKey: "imp-" + NewID(),
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if batch.Status != "committed" {
		t.Fatalf("expected committed, got %s", batch.Status)
	}
	imported := 0
	for _, it := range batch.Items {
		if it.Status == "imported" {
			imported++
		}
	}
	if imported != 3 {
		t.Fatalf("expected 3 imported, got %d", imported)
	}

	// 题库应包含 3 道题
	page, err := s.Knowledge.QuestionList(ctx, QuestionListReq{WorkspaceID: ws.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 3 {
		t.Fatalf("expected 3 questions, got %d", len(page.Items))
	}
	// 判断题为 single_choice
	var ok bool
	for _, q := range page.Items {
		if q.Type == "single_choice" && q.Status == "draft" {
			ok = true
		}
	}
	if !ok {
		t.Fatal("judge question should be single_choice draft")
	}
}

func TestImportPartialInvalid(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)

	content := []byte(`## 1. 合法单选
A. 甲
B. 乙
答案：A

## 2. 答案不在选项中
A. 甲
B. 乙
答案：Z

## 3. 缺少答案
A. 甲
B. 乙
`)
	up, err := s.Import.UploadFile("bad.md", content)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := s.Import.LibraryPreflightImport(ctx, LibraryPreflightImportReq{
		WorkspaceID: ws.ID, FilePath: up.Path, Format: "markdown", IdempotencyKey: "imp-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if preview.ValidCount != 1 || preview.ErrorCount != 2 {
		t.Fatalf("expected 1 valid / 2 errors, got %+v", preview)
	}
	if len(preview.Errors) != 2 {
		t.Fatalf("expected 2 error entries, got %d", len(preview.Errors))
	}
}

func TestImportIdempotentByHash(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)

	content := []byte(`## 1. 幂等测试题
A. 甲
B. 乙
答案：A
`)
	up, err := s.Import.UploadFile("idem.md", content)
	if err != nil {
		t.Fatal(err)
	}
	p1, err := s.Import.LibraryPreflightImport(ctx, LibraryPreflightImportReq{
		WorkspaceID: ws.ID, FilePath: up.Path, Format: "markdown", IdempotencyKey: "imp-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	p2, err := s.Import.LibraryPreflightImport(ctx, LibraryPreflightImportReq{
		WorkspaceID: ws.ID, FilePath: up.Path, Format: "markdown", IdempotencyKey: "imp-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if p1.BatchID != p2.BatchID {
		t.Fatalf("same file should yield same batch: %s != %s", p1.BatchID, p2.BatchID)
	}
}

func TestImportDuplicateQuestionSkipped(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)

	// 第一次导入
	content := []byte(`## 1. 唯一题
A. 甲
B. 乙
答案：A
`)
	up, err := s.Import.UploadFile("a.md", content)
	if err != nil {
		t.Fatal(err)
	}
	p1, err := s.Import.LibraryPreflightImport(ctx, LibraryPreflightImportReq{
		WorkspaceID: ws.ID, FilePath: up.Path, Format: "markdown", IdempotencyKey: "imp-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Import.LibraryCommitImport(ctx, LibraryCommitImportReq{
		WorkspaceID: ws.ID, BatchID: p1.BatchID, IdempotencyKey: "imp-" + NewID(),
	}); err != nil {
		t.Fatal(err)
	}

	// 第二次导入相同题目但不同文件内容（内容哈希不同，避免命中文件级幂等）
	up2, err := s.Import.UploadFile("b.md", append([]byte("# 副本文件\n\n"), content...))
	if err != nil {
		t.Fatal(err)
	}
	p2, err := s.Import.LibraryPreflightImport(ctx, LibraryPreflightImportReq{
		WorkspaceID: ws.ID, FilePath: up2.Path, Format: "markdown", IdempotencyKey: "imp-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if p2.ValidCount != 1 {
		t.Fatalf("preflight should still be valid, got %+v", p2)
	}
	batch, err := s.Import.LibraryCommitImport(ctx, LibraryCommitImportReq{
		WorkspaceID: ws.ID, BatchID: p2.BatchID, IdempotencyKey: "imp-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range batch.Items {
		if it.Status == "imported" {
			t.Fatalf("duplicate question should be skipped, item: %+v", it)
		}
		if it.Status != "invalid" || it.Error == nil {
			t.Fatalf("expected invalid with reason, got %+v", it)
		}
	}
	// 题库仍只有 1 题
	page, err := s.Knowledge.QuestionList(ctx, QuestionListReq{WorkspaceID: ws.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("expected 1 question, got %d", len(page.Items))
	}
}

func TestImportCommitNotReady(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)

	// 不存在的批次
	if _, err := s.Import.LibraryCommitImport(ctx, LibraryCommitImportReq{
		WorkspaceID: ws.ID, BatchID: "no-such-batch", IdempotencyKey: "imp-" + NewID(),
	}); err == nil {
		t.Fatal("expected NOT_FOUND")
	} else if domain.AsError(err).Code != domain.CodeNotFound {
		t.Fatalf("expected NOT_FOUND, got %s", domain.AsError(err).Code)
	}

	// 上传不存在的文件
	if _, err := s.Import.LibraryPreflightImport(ctx, LibraryPreflightImportReq{
		WorkspaceID: ws.ID, FilePath: "uploads/no-such.md", Format: "markdown", IdempotencyKey: "imp-" + NewID(),
	}); err == nil {
		t.Fatal("expected error for missing file")
	}
}
