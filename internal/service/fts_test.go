package service

// Todo 2 全文检索（FTS5）服务层测试：CJK 查询命中、文档索引写入、索引损坏降级不 panic、
// 空索引不阻塞 RAG。

import (
	"context"
	"errors"
	"strings"
	"testing"

	"lumo/internal/domain"
	"lumo/internal/repository"
)

// TestFTSSearchNoteByCJK：插入中文笔记后，2 字 CJK 查询 "量子" 能命中含 "量子力学" 的正文。
func TestFTSSearchNoteByCJK(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)
	noteID := "n-" + NewID()
	if _, err := s.Repo.DB().ExecContext(ctx, `INSERT INTO notes (id, workspace_id, user_id, title, body_md)
		VALUES (?, ?, ?, '量子力学复习', '薛定谔方程描述量子态的时间演化。')`, noteID, ws.ID, userID); err != nil {
		t.Fatalf("insert note: %v", err)
	}

	hits, err := s.Repo.SearchFTS(ctx, repository.FTSTableNotes, ws.ID, "量子", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("期望命中 1 条，得到 %d", len(hits))
	}
	if hits[0].BusinessID != noteID {
		t.Errorf("命中业务主键应为 %s，得到 %s", noteID, hits[0].BusinessID)
	}
}

// TestFTSSearchDocumentIndexedOnImport：文档导入（索引路径）后，documents_fts 可被中文查询命中。
func TestFTSSearchDocumentIndexedOnImport(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)

	content := []byte("# 量子力学\n量子力学是研究微观粒子运动规律的物理学分支。薛定谔方程描述量子态的时间演化。\n\n本段与检索词无关。")
	up, err := s.Import.UploadFile("qm.md", content)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	doc, err := s.Document.DocumentImport(ctx, DocumentImportReq{
		WorkspaceID: ws.ID, FilePath: up.Path, IdempotencyKey: "doc-" + NewID(),
	})
	if err != nil {
		t.Fatalf("import doc: %v", err)
	}
	if doc.Status != "indexed" {
		t.Fatalf("期望 indexed，得到 %s (failure: %v)", doc.Status, doc.FailureReason)
	}

	hits, err := s.Repo.SearchFTS(ctx, repository.FTSTableDocuments, ws.ID, "量子", 10)
	if err != nil {
		t.Fatalf("search documents_fts: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("期望 documents_fts 命中导入文档")
	}
	// 命中 chunk 应属于该文档（通过 chunk_id 反查 document_chunks）
	var belongs bool
	err = s.Repo.DB().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM document_chunks WHERE id = ? AND document_id = ?)`,
		hits[0].BusinessID, doc.ID).Scan(&belongs)
	if err != nil {
		t.Fatalf("check chunk ownership: %v", err)
	}
	if !belongs {
		t.Errorf("命中 chunk %s 不属于文档 %s", hits[0].BusinessID, doc.ID)
	}
}

// TestFTSSearchDegradesWhenIndexDropped：FTS 表被 DROP 后，搜索返回明确领域错误而非 panic。
func TestFTSSearchDegradesWhenIndexDropped(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)
	if _, err := s.Repo.DB().ExecContext(ctx, `DROP TABLE notes_fts`); err != nil {
		t.Fatalf("drop notes_fts: %v", err)
	}

	_, err := s.Repo.SearchFTS(ctx, repository.FTSTableNotes, ws.ID, "量子", 10)
	if err == nil {
		t.Fatal("索引损坏时应返回明确错误")
	}
	if !strings.Contains(err.Error(), "全文检索索引不可用") {
		t.Errorf("错误信息应说明索引不可用：%v", err)
	}
	var de *domain.Error
	if !errors.As(err, &de) {
		t.Fatalf("应为领域错误，得到 %T: %v", err, err)
	}
	if de.Code != domain.CodeInternal {
		t.Errorf("错误码应为 INTERNAL，得到 %s", de.Code)
	}
}

// TestRAGAskWithEmptyFTSSearchNotBlocked：documents_fts 为空（未导入任何文档）时，
// RAG 问答应正常降级为"资料未找到"提示，不报错、不阻塞。
func TestRAGAskWithEmptyFTSSearchNotBlocked(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)

	resp, err := s.Document.RAGAsk(ctx, RAGAskReq{
		WorkspaceID: ws.ID, UserID: userID, Question: "什么是量子力学？",
	})
	if err != nil {
		t.Fatalf("空索引下 RAGAsk 不应报错：%v", err)
	}
	if resp == nil || resp.SessionID == "" {
		t.Fatal("应返回带会话的请求结果")
	}
}
