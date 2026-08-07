package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lumo/internal/domain"
)

// TestCommunityPostFlow 覆盖帖子流：发布（文件存在 + 字段正确 + likes=0）→
// 列表（多条倒序）→ 详情（一致）→ 点赞（计数 +1，持久化后重读仍 +1）。
func TestCommunityPostFlow(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	ctx := context.Background()

	// 发布两条帖子
	p1, err := s.Community.CommunityPostCreate(ctx, CommunityPostCreateReq{
		WorkspaceID: ws.ID, AuthorUserID: "user-1",
		Title: "第一篇分享", BodyMD: "今天学会了导数",
	})
	if err != nil {
		t.Fatalf("create post 1: %v", err)
	}
	p2, err := s.Community.CommunityPostCreate(ctx, CommunityPostCreateReq{
		WorkspaceID: ws.ID, AuthorUserID: "user-2",
		Title: "第二篇笔记", BodyMD: "整理了一道错题",
	})
	if err != nil {
		t.Fatalf("create post 2: %v", err)
	}

	// 发布后 JSON 文件存在且字段正确（likes 初始 0）
	path := filepath.Join(s.Cfg.DataDir, "community", p1.ID+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("post file should exist: %v", err)
	}
	if string(b) == "" {
		t.Fatal("post file empty")
	}
	if p1.ID == "" || p1.CreatedAt == "" {
		t.Fatalf("post missing id/created_at: %+v", p1)
	}
	if p1.Likes != 0 {
		t.Fatalf("expected likes=0 on publish, got %d", p1.Likes)
	}

	// 列表：多条按 created_at 倒序（同秒时间戳无先后语义，只断言排序不变式）
	list, err := s.Community.CommunityPostList(ctx, CommunityPostListReq{WorkspaceID: ws.ID})
	if err != nil {
		t.Fatalf("list posts: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 posts, got %d", len(list))
	}
	ids := map[string]bool{}
	for _, p := range list {
		ids[p.ID] = true
	}
	if !ids[p1.ID] || !ids[p2.ID] {
		t.Fatalf("expected both posts in list, got %+v", list)
	}
	for i := 1; i < len(list); i++ {
		if list[i-1].CreatedAt < list[i].CreatedAt {
			t.Fatalf("posts not sorted desc by created_at: %s < %s", list[i-1].CreatedAt, list[i].CreatedAt)
		}
	}

	// 详情：与发布一致
	got, err := s.Community.CommunityPostGet(ctx, CommunityPostGetReq{WorkspaceID: ws.ID, PostID: p1.ID})
	if err != nil {
		t.Fatalf("get post: %v", err)
	}
	if got.Title != "第一篇分享" || got.BodyMD != "今天学会了导数" ||
		got.AuthorUserID != "user-1" || got.CreatedAt != p1.CreatedAt {
		t.Fatalf("detail mismatch: %+v", got)
	}

	// 点赞：计数 +1 且持久化（重读仍 +1）
	like, err := s.Community.CommunityPostLike(ctx, CommunityPostLikeReq{WorkspaceID: ws.ID, PostID: p1.ID})
	if err != nil {
		t.Fatalf("like post: %v", err)
	}
	if like.Likes != 1 {
		t.Fatalf("expected likes=1, got %d", like.Likes)
	}
	got2, err := s.Community.CommunityPostGet(ctx, CommunityPostGetReq{WorkspaceID: ws.ID, PostID: p1.ID})
	if err != nil {
		t.Fatalf("re-get post: %v", err)
	}
	if got2.Likes != 1 {
		t.Fatalf("expected persisted likes=1, got %d", got2.Likes)
	}
	// 再次点赞 → 2
	like2, err := s.Community.CommunityPostLike(ctx, CommunityPostLikeReq{WorkspaceID: ws.ID, PostID: p1.ID})
	if err != nil {
		t.Fatalf("like post again: %v", err)
	}
	if like2.Likes != 2 {
		t.Fatalf("expected likes=2, got %d", like2.Likes)
	}
}

// TestCommunityPostNotFound 覆盖 QA failure：对不存在的帖子点赞/详情 → NOT_FOUND。
func TestCommunityPostNotFound(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	ctx := context.Background()

	missing := NewID()
	_, err := s.Community.CommunityPostGet(ctx, CommunityPostGetReq{WorkspaceID: ws.ID, PostID: missing})
	if domain.AsError(err).Code != domain.CodeNotFound {
		t.Fatalf("get missing post: expected NOT_FOUND, got %v", err)
	}
	_, err = s.Community.CommunityPostLike(ctx, CommunityPostLikeReq{WorkspaceID: ws.ID, PostID: missing})
	if domain.AsError(err).Code != domain.CodeNotFound {
		t.Fatalf("like missing post: expected NOT_FOUND, got %v", err)
	}
}

// TestCommunityPostScan 覆盖安全扫描：含 <script> 的 body_md → 拒绝发布（错误含 findings）；
// 干净内容 → 发布成功。
func TestCommunityPostScan(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	ctx := context.Background()

	// 恶意内容：<script> 标签命中 script_tag 规则
	_, err := s.Community.CommunityPostCreate(ctx, CommunityPostCreateReq{
		WorkspaceID: ws.ID, AuthorUserID: "user-1",
		Title: "恶意帖子", BodyMD: "<script>alert(1)</script> 学习心得",
	})
	if err == nil {
		t.Fatal("expected scan rejection for <script> content")
	}
	de := domain.AsError(err)
	if de.Code != domain.CodeInvalidArgument {
		t.Fatalf("expected INVALID_ARGUMENT, got %v", de.Code)
	}
	if !strings.Contains(de.Message, "安全扫描") || !strings.Contains(de.Message, "script_tag") {
		t.Fatalf("error should contain scan findings, got: %s", de.Message)
	}

	// 拒绝的帖子不应落盘
	list, err := s.Community.CommunityPostList(ctx, CommunityPostListReq{WorkspaceID: ws.ID})
	if err != nil {
		t.Fatalf("list posts: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected no post persisted after scan rejection, got %d", len(list))
	}

	// 干净内容发布成功
	p, err := s.Community.CommunityPostCreate(ctx, CommunityPostCreateReq{
		WorkspaceID: ws.ID, AuthorUserID: "user-1",
		Title: "安全内容", BodyMD: "今天的学习总结",
	})
	if err != nil {
		t.Fatalf("clean content should publish: %v", err)
	}
	if p.ID == "" {
		t.Fatal("expected post id")
	}
}

// TestCommunityPostValidation 覆盖发布校验：缺 title/body_md → INVALID_ARGUMENT；
// 缺工作区 → 校验失败。
func TestCommunityPostValidation(t *testing.T) {
	s, _ := newTestServices(t)
	ws, _ := createWorkspace(t, s)
	ctx := context.Background()

	_, err := s.Community.CommunityPostCreate(ctx, CommunityPostCreateReq{
		WorkspaceID: ws.ID, AuthorUserID: "user-1", Title: "", BodyMD: "内容",
	})
	if domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("empty title: expected INVALID_ARGUMENT, got %v", err)
	}
	_, err = s.Community.CommunityPostCreate(ctx, CommunityPostCreateReq{
		WorkspaceID: ws.ID, AuthorUserID: "user-1", Title: "标题", BodyMD: "  ",
	})
	if domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("blank body: expected INVALID_ARGUMENT, got %v", err)
	}
	_, err = s.Community.CommunityPostCreate(ctx, CommunityPostCreateReq{
		WorkspaceID: "nope", AuthorUserID: "user-1", Title: "标题", BodyMD: "内容",
	})
	if domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("bad workspace: expected INVALID_ARGUMENT, got %v", err)
	}
}
