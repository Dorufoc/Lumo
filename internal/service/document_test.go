package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lumo/internal/agent"
	"lumo/internal/domain"
)

func TestDocumentImportAndRetrieve(t *testing.T) {
	s, cfg := newTestServices(t)
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)

	content := []byte(`# 高等数学笔记

## 导数定义

导数是函数在某一点的变化率，记作 f'(x)。

## 极限

极限是微积分的基础概念。

## 牛顿第二定律

F = ma，力等于质量乘以加速度。
`)
	up, err := s.Import.UploadFile("notes.md", content)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := s.Document.DocumentImport(ctx, DocumentImportReq{
		WorkspaceID: ws.ID, FilePath: up.Path, IdempotencyKey: "doc-" + NewID(),
	})
	if err != nil {
		t.Fatalf("import doc: %v", err)
	}
	if doc.Status != "indexed" {
		t.Fatalf("expected indexed, got %s (failure: %v)", doc.Status, doc.FailureReason)
	}

	// 重复导入 → 冲突
	if _, err := s.Document.DocumentImport(ctx, DocumentImportReq{
		WorkspaceID: ws.ID, FilePath: up.Path, IdempotencyKey: "doc-" + NewID(),
	}); err == nil {
		t.Fatal("expected CONFLICT for duplicate document")
	} else if domain.AsError(err).Code != domain.CodeConflict {
		t.Fatalf("expected CONFLICT, got %s", domain.AsError(err).Code)
	}

	// 列表
	page, err := s.Document.DocumentList(ctx, DocumentListReq{WorkspaceID: ws.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("expected 1 document, got %d", len(page.Items))
	}

	// 附件已写入
	attachmentsDir := filepath.Join(cfg.DataDir, "attachments", ws.ID)
	entries, err := os.ReadDir(attachmentsDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("attachments missing: %v", err)
	}

	// 删除
	if _, err := s.Document.DocumentDelete(ctx, DocumentDeleteReq{
		WorkspaceID: ws.ID, DocumentID: doc.ID, Version: doc.Version,
	}); err != nil {
		t.Fatal(err)
	}
	page, _ = s.Document.DocumentList(ctx, DocumentListReq{WorkspaceID: ws.ID})
	if len(page.Items) != 0 {
		t.Fatal("document should be deleted")
	}
}

func TestRAGAskFallback(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)

	content := []byte(`# 物理笔记

## 万有引力

万有引力定律：任意两个质点之间都存在相互吸引力，F = G m1 m2 / r²。

## 电磁感应

法拉第电磁感应定律描述磁通量变化产生电动势。
`)
	up, err := s.Import.UploadFile("physics.md", content)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Document.DocumentImport(ctx, DocumentImportReq{
		WorkspaceID: ws.ID, FilePath: up.Path, IdempotencyKey: "doc-" + NewID(),
	}); err != nil {
		t.Fatal(err)
	}

	// 订阅事件流
	ch := make(chan struct{})
	_ = ch

	// 命中问题 → 降级回答（未配置 LLM）
	req, err := s.Document.RAGAsk(ctx, RAGAskReq{
		WorkspaceID: ws.ID, UserID: userID, Question: "万有引力定律是什么？",
	})
	if err != nil {
		t.Fatalf("ragask: %v", err)
	}
	if req.RequestID == "" || req.SessionID == "" {
		t.Fatal("missing request handle")
	}
	// 会话存在
	session, err := s.Agent.AgentSessionGet(ctx, agent.AgentSessionGetReq{WorkspaceID: ws.ID, SessionID: req.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if session.Agent != "librarian" {
		t.Fatalf("expected librarian, got %s", session.Agent)
	}
	// 等待完成（轮询）
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := s.Agent.AgentSessionGet(ctx, agent.AgentSessionGetReq{WorkspaceID: ws.ID, SessionID: req.SessionID})
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == "completed" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// 无证据问题 → 明确提示
	req2, err := s.Document.RAGAsk(ctx, RAGAskReq{
		WorkspaceID: ws.ID, UserID: userID, Question: "量子力学中的薛定谔方程",
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := s.Agent.AgentSessionGet(ctx, agent.AgentSessionGetReq{WorkspaceID: ws.ID, SessionID: req2.SessionID})
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == "completed" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
}
