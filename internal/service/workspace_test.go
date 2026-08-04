package service

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"lumo/internal/agent"
	"lumo/internal/config"
	"lumo/internal/database"
	"lumo/internal/domain"
	"lumo/internal/repository"
)

// newTestServices 构造带临时数据库的服务。
func newTestServices(t *testing.T) (*Services, *config.Config) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.DBPath = filepath.Join(dir, "test.db")
	cfg.SecretsPath = filepath.Join(dir, "secrets.json")
	cfg.AttachmentsDir = filepath.Join(dir, "attachments")
	cfg.BackupsDir = filepath.Join(dir, "backups")
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	db, err := database.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repo := repository.New(db)
	s := New(repo, &cfg)
	s.Agent = agent.New(repo)
	return s, &cfg
}

func createWorkspace(t *testing.T, s *Services) (*Workspace, string) {
	t.Helper()
	ws, err := s.Workspace.WorkspaceCreate(context.Background(), WorkspaceCreateReq{
		Name: "测试工作区", OwnerType: "local", IdempotencyKey: "ws-" + NewID(),
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	users, err := s.Repo.ListUsers(context.Background(), ws.ID)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("expected 1 default user, got %d", len(users))
	}
	return ws, users[0].ID
}

func TestWorkspaceLifecycle(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()

	ws, userID := createWorkspace(t, s)

	got, err := s.Workspace.WorkspaceGet(ctx, WorkspaceGetReq{WorkspaceID: ws.ID})
	if err != nil {
		t.Fatalf("get workspace: %v", err)
	}
	if got.ID != ws.ID || got.OwnerType != "local" || got.Version != 1 {
		t.Fatalf("unexpected workspace: %+v", got)
	}

	// 默认用户可读
	prof, err := s.Workspace.UserGetProfile(ctx, UserGetProfileReq{WorkspaceID: ws.ID, UserID: userID})
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	if prof.DisplayName == "" || prof.Role != "student" {
		t.Fatalf("unexpected profile: %+v", prof)
	}

	// 删除准备 + 删除
	tok, err := s.Workspace.WorkspaceDeletePrepare(ctx, WorkspaceDeletePrepareReq{WorkspaceID: ws.ID})
	if err != nil {
		t.Fatalf("prepare delete: %v", err)
	}
	res, err := s.Workspace.WorkspaceDelete(ctx, WorkspaceDeleteReq{
		WorkspaceID: ws.ID, Version: got.Version, ConfirmToken: tok.ConfirmToken,
	})
	if err != nil {
		t.Fatalf("delete workspace: %v", err)
	}
	if !res.Deleted {
		t.Fatal("expected deleted=true")
	}
	if _, err := s.Workspace.WorkspaceGet(ctx, WorkspaceGetReq{WorkspaceID: ws.ID}); err == nil {
		t.Fatal("expected NOT_FOUND after delete")
	} else if domain.AsError(err).Code != domain.CodeNotFound {
		t.Fatalf("expected NOT_FOUND, got %s", domain.AsError(err).Code)
	}
}

func TestWorkspaceCreateIdempotent(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	key := "idem-" + NewID()

	w1, err := s.Workspace.WorkspaceCreate(ctx, WorkspaceCreateReq{Name: "A", OwnerType: "local", IdempotencyKey: key})
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	w2, err := s.Workspace.WorkspaceCreate(ctx, WorkspaceCreateReq{Name: "A", OwnerType: "local", IdempotencyKey: key})
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if w1.ID != w2.ID {
		t.Fatalf("idempotency violated: %s != %s", w1.ID, w2.ID)
	}
	n, err := s.Repo.CountWorkspaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 workspace, got %d", n)
	}

	// 无效幂等键
	if _, err := s.Workspace.WorkspaceCreate(ctx, WorkspaceCreateReq{Name: "B", OwnerType: "local", IdempotencyKey: "short"}); err == nil {
		t.Fatal("expected INVALID_ARGUMENT for short idempotency key")
	}
}

func TestUserUpdateProfileOptimisticLock(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)

	name := "小明"
	prefs := json.RawMessage(`{"theme":"dark"}`)
	prof, err := s.Workspace.UserUpdateProfile(ctx, UserUpdateProfileReq{
		WorkspaceID: ws.ID, UserID: userID, Version: 1, DisplayName: &name, Preferences: prefs,
	})
	if err != nil {
		t.Fatalf("update profile: %v", err)
	}
	if prof.DisplayName != "小明" || prof.Version != 2 {
		t.Fatalf("unexpected profile: %+v", prof)
	}

	// 版本冲突
	if _, err := s.Workspace.UserUpdateProfile(ctx, UserUpdateProfileReq{
		WorkspaceID: ws.ID, UserID: userID, Version: 1, DisplayName: &name,
	}); err == nil {
		t.Fatal("expected CONFLICT on stale version")
	} else if domain.AsError(err).Code != domain.CodeConflict {
		t.Fatalf("expected CONFLICT, got %s", domain.AsError(err).Code)
	}
}

func TestSettingsLifecycle(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)

	st, err := s.Settings.SettingsGet(ctx, SettingsGetReq{WorkspaceID: ws.ID})
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	if st.Version != 0 {
		t.Fatalf("expected version 0 for missing settings, got %d", st.Version)
	}

	st, err = s.Settings.SettingsUpdate(ctx, SettingsUpdateReq{
		WorkspaceID: ws.ID, Version: 0, Settings: json.RawMessage(`{"daily_quota":50}`),
	})
	if err != nil {
		t.Fatalf("update settings: %v", err)
	}
	if st.Version != 1 {
		t.Fatalf("expected version 1, got %d", st.Version)
	}

	st, err = s.Settings.SettingsUpdate(ctx, SettingsUpdateReq{
		WorkspaceID: ws.ID, Version: 1, Settings: json.RawMessage(`{"daily_quota":80,"theme":"light"}`),
	})
	if err != nil {
		t.Fatalf("update settings v2: %v", err)
	}
	if st.Version != 2 {
		t.Fatalf("expected version 2, got %d", st.Version)
	}
	var m map[string]any
	if err := json.Unmarshal(st.Settings, &m); err != nil || m["daily_quota"].(float64) != 80 {
		t.Fatalf("unexpected settings payload: %s", st.Settings)
	}

	// 版本冲突
	if _, err := s.Settings.SettingsUpdate(ctx, SettingsUpdateReq{
		WorkspaceID: ws.ID, Version: 1, Settings: json.RawMessage(`{}`),
	}); err == nil {
		t.Fatal("expected CONFLICT on stale settings version")
	}
}

func TestWorkspaceScoping(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()

	// 不存在的 workspace 应被拒绝
	if _, err := s.Workspace.WorkspaceGet(ctx, WorkspaceGetReq{WorkspaceID: "no-such-workspace"}); err == nil {
		t.Fatal("expected NOT_FOUND")
	}
	if _, err := s.Settings.SettingsGet(ctx, SettingsGetReq{WorkspaceID: "no-such-workspace"}); err == nil {
		t.Fatal("expected NOT_FOUND")
	}
	if _, err := s.Workspace.UserGetProfile(ctx, UserGetProfileReq{WorkspaceID: "no-such-workspace", UserID: "x"}); err == nil {
		t.Fatal("expected NOT_FOUND")
	}
}
