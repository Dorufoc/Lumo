package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"lumo/internal/domain"
)

// payload 构造单选 payload。
func scPayload(stem string, answer string) json.RawMessage {
	return mustJSON(map[string]any{
		"type": "single_choice",
		"stem": stem,
		"options": []map[string]any{
			{"key": "A", "text": "选项A"},
			{"key": "B", "text": "选项B"},
			{"key": "C", "text": "选项C"},
		},
		"answer":     answer,
		"difficulty": 3,
	})
}

func mcPayload(stem string, answer []string) json.RawMessage {
	return mustJSON(map[string]any{
		"type": "multiple_choice",
		"stem": stem,
		"options": []map[string]any{
			{"key": "A", "text": "A"}, {"key": "B", "text": "B"}, {"key": "C", "text": "C"},
		},
		"answer": answer,
	})
}

func TestKnowledgeTree(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)

	root, err := s.Knowledge.KnowledgeCreate(ctx, KnowledgeCreateReq{
		WorkspaceID: ws.ID, Name: "高等数学",
	})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	child, err := s.Knowledge.KnowledgeCreate(ctx, KnowledgeCreateReq{
		WorkspaceID: ws.ID, Name: "微积分", ParentID: &root.ID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if child.Level != 1 {
		t.Fatalf("expected level 1, got %d", child.Level)
	}

	tree, err := s.Knowledge.KnowledgeTreeGet(ctx, KnowledgeTreeGetReq{WorkspaceID: ws.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(tree) != 1 || len(tree[0].Children) != 1 {
		t.Fatalf("unexpected tree: %+v", tree)
	}

	// 删除有子节点的根应被拒绝
	if _, err := s.Knowledge.KnowledgeDelete(ctx, KnowledgeDeleteReq{
		WorkspaceID: ws.ID, KnowledgeID: root.ID, Version: root.Version,
	}); err == nil {
		t.Fatal("expected INVALID_STATE deleting node with children")
	}

	// 重命名
	renamed, err := s.Knowledge.KnowledgeUpdate(ctx, KnowledgeUpdateReq{
		WorkspaceID: ws.ID, KnowledgeID: child.ID, Version: child.Version, Name: "微积分二",
	})
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "微积分二" {
		t.Fatalf("rename failed: %s", renamed.Name)
	}
}

func TestQuestionCreateDraftAndDedup(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)

	q1, err := s.Knowledge.QuestionCreateDraft(ctx, QuestionCreateDraftReq{
		WorkspaceID: ws.ID, Payload: scPayload("1+1=?", "A"), IdempotencyKey: "q-" + NewID(),
	})
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	if q1.Status != "draft" || q1.Type != "single_choice" {
		t.Fatalf("unexpected question: %+v", q1)
	}
	if q1.CurrentVersion == nil || q1.CurrentVersion.VersionNo != 1 {
		t.Fatal("current version missing")
	}

	// 相同内容（格式化不同但语义相同）→ 去重冲突
	dup, err := s.Knowledge.QuestionCreateDraft(ctx, QuestionCreateDraftReq{
		WorkspaceID: ws.ID, Payload: scPayload("1+1=? ", "A"), IdempotencyKey: "q-" + NewID(),
	})
	if err == nil {
		t.Fatalf("expected dedup conflict, got %+v", dup)
	} else if domain.AsError(err).Code != domain.CodeConflict {
		t.Fatalf("expected CONFLICT, got %s", domain.AsError(err).Code)
	}

	// 相同幂等键 → 返回同一题目
	idemKey := "q-idem-" + NewID()
	q2, err := s.Knowledge.QuestionCreateDraft(ctx, QuestionCreateDraftReq{
		WorkspaceID: ws.ID, Payload: scPayload("另一个题目", "B"), IdempotencyKey: idemKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	q3, err := s.Knowledge.QuestionCreateDraft(ctx, QuestionCreateDraftReq{
		WorkspaceID: ws.ID, Payload: scPayload("另一个题目", "B"), IdempotencyKey: idemKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	if q2.ID != q3.ID {
		t.Fatal("idempotency violated")
	}
}

func TestQuestionPayloadValidation(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)

	cases := []struct {
		name    string
		payload json.RawMessage
	}{
		{"bad type", mustJSON(map[string]any{"type": "essay", "stem": "x"})},
		{"empty stem", mustJSON(map[string]any{"type": "single_choice", "stem": "  ", "options": []map[string]any{{"key": "A", "text": "a"}}, "answer": "A"})},
		{"answer not in options", scPayload("题", "Z")},
		{"single choice multiple answers", mustJSON(map[string]any{
			"type": "single_choice", "stem": "s",
			"options": []map[string]any{{"key": "A", "text": "a"}, {"key": "B", "text": "b"}},
			"answer":  []string{"A", "B"},
		})},
		{"mc empty answer", mcPayload("m", []string{})},
		{"fill blank bad answer", mustJSON(map[string]any{"type": "fill_blank", "stem": "f", "answer": map[string]any{"x": 1}})},
		{"code missing config", mustJSON(map[string]any{"type": "code", "stem": "c", "answer": "def f(): pass"})},
		{"duplicate option key", mustJSON(map[string]any{
			"type": "single_choice", "stem": "s",
			"options": []map[string]any{{"key": "A", "text": "a"}, {"key": "A", "text": "b"}},
			"answer":  "A",
		})},
	}
	for _, c := range cases {
		if _, err := s.Knowledge.QuestionCreateDraft(ctx, QuestionCreateDraftReq{
			WorkspaceID: ws.ID, Payload: c.payload, IdempotencyKey: "qv-" + NewID(),
		}); err == nil {
			t.Errorf("%s: expected error", c.name)
		} else if domain.AsError(err).Code != domain.CodeInvalidArgument {
			t.Errorf("%s: expected INVALID_ARGUMENT, got %s", c.name, domain.AsError(err).Code)
		}
	}

	// 合法多选
	if _, err := s.Knowledge.QuestionCreateDraft(ctx, QuestionCreateDraftReq{
		WorkspaceID: ws.ID, Payload: mcPayload("多选题", []string{"A", "C"}), IdempotencyKey: "qv-" + NewID(),
	}); err != nil {
		t.Fatalf("valid mc rejected: %v", err)
	}
	// 合法代码题
	if _, err := s.Knowledge.QuestionCreateDraft(ctx, QuestionCreateDraftReq{
		WorkspaceID: ws.ID,
		Payload: mustJSON(map[string]any{
			"type": "code", "stem": "实现加法", "answer": "def add(a,b): return a+b",
			"grading_config": map[string]any{"language": "python", "time_limit": 5, "memory_limit": 256},
		}),
		IdempotencyKey: "qv-" + NewID(),
	}); err != nil {
		t.Fatalf("valid code rejected: %v", err)
	}
}

func TestQuestionVersionImmutable(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)

	q, err := s.Knowledge.QuestionCreateDraft(ctx, QuestionCreateDraftReq{
		WorkspaceID: ws.ID, Payload: scPayload("版本测试", "A"), IdempotencyKey: "qv-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	// 直接改版本（绕过 service）应被触发器拒绝
	if _, err := s.Repo.DB().ExecContext(ctx,
		`UPDATE question_versions SET payload_json = '{}' WHERE id = ?`, q.CurrentVersion.ID); err == nil {
		t.Fatal("expected trigger to block version update")
	}
	if _, err := s.Repo.DB().ExecContext(ctx,
		`DELETE FROM question_versions WHERE id = ?`, q.CurrentVersion.ID); err == nil {
		t.Fatal("expected trigger to block version delete")
	}

	// 创建 v2
	v2, err := s.Knowledge.QuestionCreateVersion(ctx, QuestionCreateVersionReq{
		WorkspaceID: ws.ID, QuestionID: q.ID, Version: q.Version,
		Payload: scPayload("版本测试2", "B"), IdempotencyKey: "qv-" + NewID(),
	})
	if err != nil {
		t.Fatalf("create version: %v", err)
	}
	if v2.VersionNo != 2 {
		t.Fatalf("expected version_no 2, got %d", v2.VersionNo)
	}
	got, err := s.Knowledge.QuestionGet(ctx, QuestionGetReq{WorkspaceID: ws.ID, QuestionID: q.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got.CurrentVersion.VersionNo != 2 {
		t.Fatalf("current version should be 2, got %d", got.CurrentVersion.VersionNo)
	}
}

func TestQuestionTransition(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)

	q, err := s.Knowledge.QuestionCreateDraft(ctx, QuestionCreateDraftReq{
		WorkspaceID: ws.ID, Payload: scPayload("状态机", "A"), IdempotencyKey: "qt-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// 非法：draft 直接 publish
	if _, err := s.Knowledge.QuestionTransition(ctx, QuestionTransitionReq{
		WorkspaceID: ws.ID, QuestionID: q.ID, Version: q.Version, Action: "publish",
	}); err == nil {
		t.Fatal("expected INVALID_STATE for draft->publish")
	}

	// 合法路径
	q, err = s.Knowledge.QuestionTransition(ctx, QuestionTransitionReq{
		WorkspaceID: ws.ID, QuestionID: q.ID, Version: q.Version, Action: "review",
	})
	if err != nil {
		t.Fatal(err)
	}
	if q.Status != "reviewed" {
		t.Fatalf("expected reviewed, got %s", q.Status)
	}
	q, err = s.Knowledge.QuestionTransition(ctx, QuestionTransitionReq{
		WorkspaceID: ws.ID, QuestionID: q.ID, Version: q.Version, Action: "publish",
	})
	if err != nil {
		t.Fatal(err)
	}
	if q.Status != "published" {
		t.Fatalf("expected published, got %s", q.Status)
	}
	// 已发布再 review 应拒绝
	if _, err := s.Knowledge.QuestionTransition(ctx, QuestionTransitionReq{
		WorkspaceID: ws.ID, QuestionID: q.ID, Version: q.Version, Action: "review",
	}); err == nil {
		t.Fatal("expected INVALID_STATE for published->review")
	}
	// 发布后创建新版本仍允许
	if _, err := s.Knowledge.QuestionCreateVersion(ctx, QuestionCreateVersionReq{
		WorkspaceID: ws.ID, QuestionID: q.ID, Version: q.Version,
		Payload: scPayload("状态机2", "B"), IdempotencyKey: "qt-" + NewID(),
	}); err != nil {
		t.Fatalf("version after publish: %v", err)
	}
}

func TestQuestionListFilter(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)

	for i := 0; i < 3; i++ {
		if _, err := s.Knowledge.QuestionCreateDraft(ctx, QuestionCreateDraftReq{
			WorkspaceID: ws.ID, Payload: scPayload("列表题目"+string(rune('A'+i)), "A"),
			IdempotencyKey: "ql-" + NewID(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.Knowledge.QuestionList(ctx, QuestionListReq{WorkspaceID: ws.ID, Type: "single_choice", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || !page.HasMore {
		t.Fatalf("unexpected page: %d items, hasMore=%v", len(page.Items), page.HasMore)
	}
	page2, err := s.Knowledge.QuestionList(ctx, QuestionListReq{
		WorkspaceID: ws.ID, Type: "single_choice", Cursor: page.NextCursor, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2.Items) != 1 || page2.HasMore {
		t.Fatalf("unexpected second page: %d items, hasMore=%v", len(page2.Items), page2.HasMore)
	}
	// 关键词搜索
	kw, err := s.Knowledge.QuestionList(ctx, QuestionListReq{WorkspaceID: ws.ID, Keyword: "列表题目B"})
	if err != nil {
		t.Fatal(err)
	}
	if len(kw.Items) != 1 {
		t.Fatalf("expected 1 keyword hit, got %d", len(kw.Items))
	}
}

func TestKnowledgeDeleteReferenced(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)

	n, err := s.Knowledge.KnowledgeCreate(ctx, KnowledgeCreateReq{WorkspaceID: ws.ID, Name: "被引用知识点"})
	if err != nil {
		t.Fatal(err)
	}
	payload := mustJSON(map[string]any{
		"type": "single_choice", "stem": "引用知识点",
		"options": []map[string]any{{"key": "A", "text": "a"}, {"key": "B", "text": "b"}},
		"answer": "A", "knowledge_ids": []string{n.ID},
	})
	if _, err := s.Knowledge.QuestionCreateDraft(ctx, QuestionCreateDraftReq{
		WorkspaceID: ws.ID, Payload: payload, IdempotencyKey: "kd-" + NewID(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Knowledge.KnowledgeDelete(ctx, KnowledgeDeleteReq{
		WorkspaceID: ws.ID, KnowledgeID: n.ID, Version: n.Version,
	}); err == nil {
		t.Fatal("expected INVALID_STATE deleting referenced knowledge")
	}
}

// ---- Todo 37：题目事件 Webhook Dispatch 调用点 ----
// question:published → QuestionTransition action=publish 成功后（audit 前）Dispatch；
// question:changed   → 题目内容变更点 Dispatch（版本创建）。

// setupQuestionWebhook 构造带 webhook 桩的服务：返回 (s, wsID, captured)。
func setupQuestionWebhook(t *testing.T) (*Services, string, *capturedWebhook) {
	t.Helper()
	s, _ := newTestServices(t)
	s.Webhooks.Now = func() time.Time { return webhookFixedNow }
	ws, _ := createWorkspace(t, s)
	srv, captured := webhookStub(t, http.StatusOK)
	subscribeWebhook(t, s, ws.ID, srv.URL, []string{"question:published", "question:changed"}, nil, "wh-sub-qcall-0001")
	return s, ws.ID, captured
}

// 发布成功 → question:published 投递一次（载荷含 question_id/version_id/status）。
func TestQuestionPublishDispatchesWebhook(t *testing.T) {
	s, wsID, captured := setupQuestionWebhook(t)
	ctx := context.Background()

	q, err := s.Knowledge.QuestionCreateDraft(ctx, QuestionCreateDraftReq{
		WorkspaceID: wsID, Payload: scPayload("发布投递", "A"), IdempotencyKey: "qp-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	q, err = s.Knowledge.QuestionTransition(ctx, QuestionTransitionReq{
		WorkspaceID: wsID, QuestionID: q.ID, Version: q.Version, Action: "review",
	})
	if err != nil {
		t.Fatal(err)
	}
	q, err = s.Knowledge.QuestionTransition(ctx, QuestionTransitionReq{
		WorkspaceID: wsID, QuestionID: q.ID, Version: q.Version, Action: "publish",
	})
	if err != nil {
		t.Fatal(err)
	}

	if captured.requests() != 1 {
		t.Fatalf("publish 应投递 1 次，got %d", captured.requests())
	}
	var body struct {
		EventType string         `json:"event_type"`
		Payload   map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(captured.body, &body); err != nil {
		t.Fatal(err)
	}
	if body.EventType != "question:published" {
		t.Fatalf("期望 question:published，got %q", body.EventType)
	}
	if body.Payload["question_id"] != q.ID || body.Payload["status"] != "published" {
		t.Fatalf("载荷异常: %+v", body.Payload)
	}
	if vid, ok := body.Payload["version_id"].(string); !ok || vid == "" {
		t.Fatalf("载荷缺 version_id: %+v", body.Payload)
	}
}

// 发布失败（非法迁移）不投递。
func TestQuestionPublishRejectedNoDispatch(t *testing.T) {
	s, wsID, captured := setupQuestionWebhook(t)
	ctx := context.Background()

	q, err := s.Knowledge.QuestionCreateDraft(ctx, QuestionCreateDraftReq{
		WorkspaceID: wsID, Payload: scPayload("不投递", "A"), IdempotencyKey: "qn-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	// draft 直接 publish → INVALID_STATE，不得投递。
	if _, err := s.Knowledge.QuestionTransition(ctx, QuestionTransitionReq{
		WorkspaceID: wsID, QuestionID: q.ID, Version: q.Version, Action: "publish",
	}); err == nil {
		t.Fatal("expected INVALID_STATE")
	}
	if captured.requests() != 0 {
		t.Fatalf("非法迁移不应投递，got %d", captured.requests())
	}
}

// 版本创建（内容变更）→ question:changed 投递一次（载荷含 question_id/version_id）。
func TestQuestionVersionChangeDispatchesWebhook(t *testing.T) {
	s, wsID, captured := setupQuestionWebhook(t)
	ctx := context.Background()

	q, err := s.Knowledge.QuestionCreateDraft(ctx, QuestionCreateDraftReq{
		WorkspaceID: wsID, Payload: scPayload("版本变更", "A"), IdempotencyKey: "qvw-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Knowledge.QuestionCreateVersion(ctx, QuestionCreateVersionReq{
		WorkspaceID: wsID, QuestionID: q.ID, Version: q.Version,
		Payload: scPayload("版本变更2", "B"), IdempotencyKey: "qvw2-" + NewID(),
	}); err != nil {
		t.Fatal(err)
	}

	if captured.requests() != 1 {
		t.Fatalf("版本创建应投递 1 次，got %d", captured.requests())
	}
	var body struct {
		EventType string         `json:"event_type"`
		Payload   map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(captured.body, &body); err != nil {
		t.Fatal(err)
	}
	if body.EventType != "question:changed" {
		t.Fatalf("期望 question:changed，got %q", body.EventType)
	}
	if body.Payload["question_id"] != q.ID {
		t.Fatalf("载荷缺 question_id: %+v", body.Payload)
	}
	if vid, ok := body.Payload["version_id"].(string); !ok || vid == "" {
		t.Fatalf("载荷缺 version_id: %+v", body.Payload)
	}
}
