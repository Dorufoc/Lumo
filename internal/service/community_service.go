// community_service.go 本地内容社区用例（完整设计文档 4.20 社区 / Todo 35）。
// 帖子存储决策（明确，无分叉）：社区帖子存本地 JSON 文件——根路径为应用数据目录下的
// community/ 子目录，即 <DataDir>/community/<post_id>.json（与数据库同根）。
// 点赞计数维护在本地 JSON 的 likes 字段；不新增 community_posts 表，
// 也不使用 shares.ref_type=community_post（该值不在 4.20 文档枚举内，避免 schema 越界）。
// content_requests 仅用于求题闭环（Todo 36），与本模块无关。
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"lumo/internal/domain"
)

// CommunityService 实现本地内容社区用例。
type CommunityService struct {
	s *Services

	mu sync.Mutex // 保护 community 目录 JSON 读写（目录级互斥即可，参照 SyncService）
}

// CommunityPost 是社区帖子 DTO（逐帖持久化为一个 JSON 文件）。
type CommunityPost struct {
	ID           string `json:"id"`
	AuthorUserID string `json:"author_user_id"`
	Title        string `json:"title"`
	BodyMD       string `json:"body_md"`
	CreatedAt    string `json:"created_at"`
	Likes        int    `json:"likes"`
}

// CommunityPostCreateReq 发布帖子请求。
type CommunityPostCreateReq struct {
	WorkspaceID  string `json:"workspace_id"`
	AuthorUserID string `json:"author_user_id"`
	Title        string `json:"title"`
	BodyMD       string `json:"body_md"`
}

// CommunityPostListReq 帖子列表请求。
type CommunityPostListReq struct {
	WorkspaceID string `json:"workspace_id"`
}

// CommunityPostGetReq 帖子详情请求。
type CommunityPostGetReq struct {
	WorkspaceID string `json:"workspace_id"`
	PostID      string `json:"post_id"`
}

// CommunityPostLikeReq 点赞请求。
type CommunityPostLikeReq struct {
	WorkspaceID string `json:"workspace_id"`
	PostID      string `json:"post_id"`
}

// CommunityPostLikeResp 点赞响应（返回新计数）。
type CommunityPostLikeResp struct {
	PostID string `json:"post_id"`
	Likes  int    `json:"likes"`
}

// communityDir 返回社区帖子存储目录（<DataDir>/community，与数据库同根）。
func (c *CommunityService) communityDir() string {
	return filepath.Join(c.s.Cfg.DataDir, "community")
}

// CommunityPostCreate 发布帖子：强制安全扫描（复用 scanContent，扫描对象为
// {title, body_md} 的 JSON 序列化，marshalShareContent 不做 HTML 转义保证 <script> 真实呈现）；
// 扫描不 clean 拒绝发布（错误提示含 findings）；通过后持久化为
// <DataDir>/community/<post_id>.json（likes 初始 0）。
func (c *CommunityService) CommunityPostCreate(ctx context.Context, req CommunityPostCreateReq) (*CommunityPost, error) {
	if err := c.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	title := strings.TrimSpace(req.Title)
	body := strings.TrimSpace(req.BodyMD)
	if title == "" {
		return nil, domain.InvalidArg("title 必填")
	}
	if body == "" {
		return nil, domain.InvalidArg("body_md 必填")
	}
	content, err := marshalShareContent(map[string]any{"title": title, "body_md": body})
	if err != nil {
		return nil, domain.NewError(domain.CodeInternal, fmt.Sprintf("序列化帖子内容失败: %v", err))
	}
	// 强制安全扫描（复用 Todo 29 的 scanContent，同一套 10 条规则）。
	res := scanContent(content)
	if !res.Clean {
		return nil, domain.InvalidArg("内容未通过安全扫描，禁止发布：%s", strings.Join(res.Findings, ", "))
	}
	post := &CommunityPost{
		ID:           NewID(),
		AuthorUserID: req.AuthorUserID,
		Title:        title,
		BodyMD:       body,
		CreatedAt:    Now(),
		Likes:        0,
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.savePost(post); err != nil {
		return nil, err
	}
	c.s.audit(ctx, req.WorkspaceID, "community.post.create", "community_post", post.ID,
		map[string]any{"title": post.Title})
	return post, nil
}

// CommunityPostList 读取 community 目录下所有 *.json 帖子，按 created_at 倒序返回。
func (c *CommunityService) CommunityPostList(ctx context.Context, req CommunityPostListReq) ([]CommunityPost, error) {
	if err := c.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entries, err := os.ReadDir(c.communityDir())
	if err != nil {
		if os.IsNotExist(err) {
			return []CommunityPost{}, nil
		}
		return nil, err
	}
	posts := make([]CommunityPost, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		post, err := c.loadPost(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		if post != nil {
			posts = append(posts, *post)
		}
	}
	sort.Slice(posts, func(i, j int) bool { return posts[i].CreatedAt > posts[j].CreatedAt })
	return posts, nil
}

// CommunityPostGet 返回单帖详情；帖子不存在 → NOT_FOUND。
func (c *CommunityService) CommunityPostGet(ctx context.Context, req CommunityPostGetReq) (*CommunityPost, error) {
	if err := c.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	post, err := c.loadPost(req.PostID)
	if err != nil {
		return nil, err
	}
	if post == nil {
		return nil, domain.NotFound("帖子不存在")
	}
	return post, nil
}

// CommunityPostLike 帖子点赞：likes 计数 +1 并持久化（读写本地 JSON 的 likes 字段）；
// 帖子不存在 → NOT_FOUND。
func (c *CommunityService) CommunityPostLike(ctx context.Context, req CommunityPostLikeReq) (*CommunityPostLikeResp, error) {
	if err := c.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	post, err := c.loadPost(req.PostID)
	if err != nil {
		return nil, err
	}
	if post == nil {
		return nil, domain.NotFound("帖子不存在")
	}
	post.Likes++
	if err := c.savePost(post); err != nil {
		return nil, err
	}
	c.s.audit(ctx, req.WorkspaceID, "community.post.like", "community_post", post.ID,
		map[string]any{"likes": post.Likes})
	return &CommunityPostLikeResp{PostID: post.ID, Likes: post.Likes}, nil
}

// savePost 将帖子写入 <DataDir>/community/<post_id>.json（0600）。
func (c *CommunityService) savePost(post *CommunityPost) error {
	dir := c.communityDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(post, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, post.ID+".json"), b, 0o600)
}

// loadPost 读取单帖；文件不存在或 post_id 非法 → nil（由调用方决定 NOT_FOUND）。
func (c *CommunityService) loadPost(postID string) (*CommunityPost, error) {
	if postID == "" || !domain.ValidID(postID) {
		return nil, nil
	}
	b, err := os.ReadFile(filepath.Join(c.communityDir(), postID+".json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var post CommunityPost
	if err := json.Unmarshal(b, &post); err != nil {
		return nil, err
	}
	return &post, nil
}
