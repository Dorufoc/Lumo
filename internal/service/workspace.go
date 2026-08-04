package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"lumo/internal/domain"
	"lumo/internal/repository"
)

// Workspace 是工作区 DTO。
type Workspace struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	OwnerType string  `json:"owner_type"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
	DeletedAt *string `json:"deleted_at"`
	Version   int     `json:"version"`
}

// UserProfile 是用户资料 DTO。
type UserProfile struct {
	ID          string          `json:"id"`
	WorkspaceID string          `json:"workspace_id"`
	DisplayName string          `json:"display_name"`
	Role        string          `json:"role"`
	Preferences json.RawMessage `json:"preferences"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
	Version     int             `json:"version"`
}

// DeleteResult 是删除操作结果。
type DeleteResult struct {
	Deleted   bool   `json:"deleted"`
	DeletedAt string `json:"deleted_at"`
}

// WorkspaceService 实现工作区与用户用例。
type WorkspaceService struct{ s *Services }

// WorkspaceCreateReq 创建工作区请求。
type WorkspaceCreateReq struct {
	Name           string `json:"name"`
	OwnerType      string `json:"owner_type"`
	IdempotencyKey string `json:"idempotency_key"`
}

// WorkspaceCreate 创建本地工作区；同时创建默认学生用户。
func (w *WorkspaceService) WorkspaceCreate(ctx context.Context, req WorkspaceCreateReq) (*Workspace, error) {
	if req.Name == "" || len(req.Name) > 120 {
		return nil, domain.InvalidArg("工作区名称长度须为 1-120")
	}
	if req.OwnerType == "" {
		req.OwnerType = "local"
	}
	if req.OwnerType != "guest" && req.OwnerType != "local" && req.OwnerType != "cloud" {
		return nil, domain.InvalidArg("owner_type 仅允许 guest/local/cloud")
	}
	if req.IdempotencyKey == "" {
		return nil, domain.InvalidArg("idempotency_key 必填")
	}

	res, err := withIdempotency(w.s, ctx, "__new__", req.IdempotencyKey, "WorkspaceCreate", func() (*Workspace, error) {
		ws := &Workspace{
			ID: NewID(), Name: req.Name, OwnerType: req.OwnerType,
			CreatedAt: Now(), UpdatedAt: Now(), Version: 1,
		}
		if err := w.s.Repo.CreateWorkspace(ctx, &repository.WorkspaceRow{
			ID: ws.ID, Name: ws.Name, OwnerType: ws.OwnerType,
		}); err != nil {
			return nil, err
		}
		// 默认学生用户（引导流程必需）。
		userID := NewID()
		if err := w.s.Repo.CreateUser(ctx, &repository.UserRow{
			ID: userID, WorkspaceID: ws.ID, DisplayName: "学习者",
			Role: "student", Preferences: json.RawMessage("{}"),
		}); err != nil {
			return nil, err
		}
		w.s.audit(ctx, ws.ID, "workspace.create", "workspace", ws.ID, map[string]any{"name": ws.Name})
		return ws, nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// WorkspaceGetReq 获取工作区请求。
type WorkspaceGetReq struct {
	WorkspaceID string `json:"workspace_id"`
}

// WorkspaceGet 获取工作区。
func (w *WorkspaceService) WorkspaceGet(ctx context.Context, req WorkspaceGetReq) (*Workspace, error) {
	if err := w.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	row, err := w.s.Repo.GetWorkspace(ctx, req.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, domain.NotFound("工作区不存在或已被删除")
	}
	return workspaceFromRow(row), nil
}

// WorkspaceList 列出全部工作区（引导页选择工作区）。
func (w *WorkspaceService) WorkspaceList(ctx context.Context) ([]*Workspace, error) {
	rows, err := w.s.Repo.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*Workspace, 0, len(rows))
	for _, r := range rows {
		out = append(out, workspaceFromRow(r))
	}
	return out, nil
}

// WorkspaceDeletePrepareReq 准备删除请求。
type WorkspaceDeletePrepareReq struct {
	WorkspaceID string `json:"workspace_id"`
}

// ConfirmToken 是删除确认令牌。
type ConfirmToken struct {
	ConfirmToken string `json:"confirm_token"`
	ExpiresAt    string `json:"expires_at"`
}

// WorkspaceDeletePrepare 生成删除确认令牌（5 分钟窗口，HMAC 无状态，格式 ts.hex）。
func (w *WorkspaceService) WorkspaceDeletePrepare(ctx context.Context, req WorkspaceDeletePrepareReq) (*ConfirmToken, error) {
	if err := w.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	secret, err := w.s.hmacSecret()
	if err != nil {
		return nil, err
	}
	ts := time.Now().UTC().Unix()
	mac := hmac.New(sha256.New, secret)
	fmt.Fprintf(mac, "%d|%s|delete", ts, req.WorkspaceID)
	return &ConfirmToken{
		ConfirmToken: fmt.Sprintf("%d.%s", ts, hex.EncodeToString(mac.Sum(nil))),
		ExpiresAt:    time.Unix(ts+300, 0).UTC().Format(time.RFC3339),
	}, nil
}

// WorkspaceDeleteReq 删除工作区请求。
type WorkspaceDeleteReq struct {
	WorkspaceID  string `json:"workspace_id"`
	Version      int    `json:"version"`
	ConfirmToken string `json:"confirm_token"`
}

// WorkspaceDelete 校验令牌后软删除工作区。
func (w *WorkspaceService) WorkspaceDelete(ctx context.Context, req WorkspaceDeleteReq) (*DeleteResult, error) {
	if err := w.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.ConfirmToken == "" {
		return nil, domain.InvalidArg("confirm_token 必填（先调用 WorkspaceDeletePrepare）")
	}
	parts := strings.SplitN(req.ConfirmToken, ".", 2)
	if len(parts) != 2 {
		return nil, domain.InvalidArg("confirm_token 无效或已过期")
	}
	ts, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return nil, domain.InvalidArg("confirm_token 无效或已过期")
	}
	now := time.Now().UTC().Unix()
	if now < ts-300 || now > ts+300 {
		return nil, domain.InvalidArg("confirm_token 无效或已过期")
	}
	secret, err := w.s.hmacSecret()
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, secret)
	fmt.Fprintf(mac, "%d|%s|delete", ts, req.WorkspaceID)
	if !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(parts[1])) {
		return nil, domain.InvalidArg("confirm_token 无效或已过期")
	}
	ws, err := w.s.Repo.GetWorkspace(ctx, req.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if ws.Version != req.Version {
		return nil, domain.Conflict("工作区版本不一致，请刷新后重试")
	}
	if err := w.s.Repo.SoftDeleteWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	w.s.audit(ctx, req.WorkspaceID, "workspace.delete", "workspace", req.WorkspaceID, nil)
	return &DeleteResult{Deleted: true, DeletedAt: Now()}, nil
}

// UserGetProfileReq 获取用户资料请求。
type UserGetProfileReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
}

// UserGetProfile 获取学生资料。
func (w *WorkspaceService) UserGetProfile(ctx context.Context, req UserGetProfileReq) (*UserProfile, error) {
	if err := w.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	u, err := w.s.Repo.GetUser(ctx, req.WorkspaceID, req.UserID)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, domain.NotFound("用户不存在")
	}
	return userFromRow(u), nil
}

// UserListReq 列出用户请求。
type UserListReq struct {
	WorkspaceID string `json:"workspace_id"`
}

// UserList 列出工作区全部用户。
func (w *WorkspaceService) UserList(ctx context.Context, req UserListReq) ([]*UserProfile, error) {
	if err := w.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	rows, err := w.s.Repo.ListUsers(ctx, req.WorkspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]*UserProfile, 0, len(rows))
	for _, r := range rows {
		out = append(out, userFromRow(r))
	}
	return out, nil
}

// UserCreateReq 创建用户请求（默认用户在 WorkspaceCreate 时自动创建）。
type UserCreateReq struct {
	WorkspaceID string `json:"workspace_id"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

// UserCreate 创建工作区内的学生用户。
func (w *WorkspaceService) UserCreate(ctx context.Context, req UserCreateReq) (*UserProfile, error) {
	if err := w.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.DisplayName == "" || len(req.DisplayName) > 80 {
		return nil, domain.InvalidArg("display_name 长度须为 1-80")
	}
	role := req.Role
	if role == "" {
		role = "student"
	}
	if role != "student" && role != "teacher" && role != "admin" {
		return nil, domain.InvalidArg("role 仅允许 student/teacher/admin")
	}
	u := &repository.UserRow{
		ID: NewID(), WorkspaceID: req.WorkspaceID, DisplayName: req.DisplayName,
		Role: role, Preferences: json.RawMessage("{}"),
	}
	if err := w.s.Repo.CreateUser(ctx, u); err != nil {
		return nil, err
	}
	w.s.audit(ctx, req.WorkspaceID, "user.create", "user", u.ID, map[string]any{"role": role})
	row, err := w.s.Repo.GetUser(ctx, req.WorkspaceID, u.ID)
	if err != nil {
		return nil, err
	}
	return userFromRow(row), nil
}

// UserUpdateProfileReq 更新用户资料请求。
type UserUpdateProfileReq struct {
	WorkspaceID string          `json:"workspace_id"`
	UserID      string          `json:"user_id"`
	Version     int             `json:"version"`
	DisplayName *string         `json:"display_name"`
	Preferences json.RawMessage `json:"preferences"`
}

// UserUpdateProfile 更新资料与偏好（乐观锁）。
func (w *WorkspaceService) UserUpdateProfile(ctx context.Context, req UserUpdateProfileReq) (*UserProfile, error) {
	if err := w.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	cur, err := w.s.Repo.GetUser(ctx, req.WorkspaceID, req.UserID)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, domain.NotFound("用户不存在")
	}
	name := cur.DisplayName
	if req.DisplayName != nil {
		name = *req.DisplayName
	}
	prefs := cur.Preferences
	if len(req.Preferences) > 0 {
		if !json.Valid(req.Preferences) {
			return nil, domain.InvalidArg("preferences 不是合法 JSON")
		}
		prefs = req.Preferences
	}
	row, err := w.s.Repo.UpdateUserProfile(ctx, req.WorkspaceID, req.UserID, req.Version, name, prefs)
	if err != nil {
		return nil, err
	}
	w.s.audit(ctx, req.WorkspaceID, "user.update", "user", req.UserID, nil)
	return userFromRow(row), nil
}

func workspaceFromRow(r *repository.WorkspaceRow) *Workspace {
	return &Workspace{
		ID: r.ID, Name: r.Name, OwnerType: r.OwnerType,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, DeletedAt: r.DeletedAt, Version: r.Version,
	}
}

func userFromRow(r *repository.UserRow) *UserProfile {
	return &UserProfile{
		ID: r.ID, WorkspaceID: r.WorkspaceID, DisplayName: r.DisplayName,
		Role: r.Role, Preferences: r.Preferences,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, Version: r.Version,
	}
}
