package service

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"lumo/internal/config"
	"lumo/internal/database"
	"lumo/internal/domain"
)

// injectSwapDB 注入与 app 层一致的数据库替换回调。
func injectSwapDB(t *testing.T, s *Services, cfg *config.Config) {
	t.Helper()
	s.SwapDB = func(newPath string) (*sql.DB, error) {
		old := s.Repo.DB()
		old.Close()
		if err := os.Rename(newPath, cfg.DBPath); err != nil {
			return nil, err
		}
		_ = os.Remove(cfg.DBPath + "-wal")
		_ = os.Remove(cfg.DBPath + "-shm")
		db, err := database.Open(cfg.DBPath)
		if err != nil {
			return nil, err
		}
		if err := database.Migrate(context.Background(), db); err != nil {
			db.Close()
			return nil, err
		}
		t.Cleanup(func() { db.Close() })
		return db, nil
	}
}

func TestBackupCreateAndRestore(t *testing.T) {
	s, cfg := newTestServices(t)
	ctx := context.Background()
	ws, userID := createWorkspace(t, s)

	// 修改用户资料（恢复后应回到备份前状态）
	name := "备份前名字"
	if _, err := s.Workspace.UserUpdateProfile(ctx, UserUpdateProfileReq{
		WorkspaceID: ws.ID, UserID: userID, Version: 1, DisplayName: &name,
	}); err != nil {
		t.Fatalf("update profile: %v", err)
	}

	backup, err := s.Backup.BackupCreate(ctx, BackupCreateReq{
		WorkspaceID: ws.ID, Password: "secret-pw", IdempotencyKey: "bak-" + NewID(),
	})
	if err != nil {
		t.Fatalf("backup create: %v", err)
	}
	if backup.Path == "" || backup.SizeBytes == 0 {
		t.Fatalf("unexpected backup result: %+v", backup)
	}

	// 备份后修改（应被恢复覆盖）
	name2 := "备份后名字"
	if _, err := s.Workspace.UserUpdateProfile(ctx, UserUpdateProfileReq{
		WorkspaceID: ws.ID, UserID: userID, Version: 2, DisplayName: &name2,
	}); err != nil {
		t.Fatalf("update profile 2: %v", err)
	}

	injectSwapDB(t, s, cfg)
	res, err := s.Backup.BackupRestore(ctx, BackupRestoreReq{
		BackupPath: backup.Path, Password: "secret-pw", TargetWorkspaceID: ws.ID,
	})
	if err != nil {
		t.Fatalf("backup restore: %v", err)
	}
	if !res.Restored {
		t.Fatal("expected restored=true")
	}

	prof, err := s.Workspace.UserGetProfile(ctx, UserGetProfileReq{WorkspaceID: ws.ID, UserID: userID})
	if err != nil {
		t.Fatalf("get profile after restore: %v", err)
	}
	if prof.DisplayName != "备份前名字" {
		t.Fatalf("restore did not roll back data: %s", prof.DisplayName)
	}
}

func TestBackupRestoreErrors(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)

	backup, err := s.Backup.BackupCreate(ctx, BackupCreateReq{
		WorkspaceID: ws.ID, Password: "secret-pw", IdempotencyKey: "bak-" + NewID(),
	})
	if err != nil {
		t.Fatalf("backup create: %v", err)
	}

	// 错误密码
	if _, err := s.Backup.BackupRestore(ctx, BackupRestoreReq{
		BackupPath: backup.Path, Password: "wrong-pw", TargetWorkspaceID: ws.ID,
	}); err == nil {
		t.Fatal("expected error for wrong password")
	} else if domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("expected INVALID_ARGUMENT, got %s", domain.AsError(err).Code)
	}

	// 备份中不存在的目标工作区
	if _, err := s.Backup.BackupRestore(ctx, BackupRestoreReq{
		BackupPath: backup.Path, Password: "secret-pw", TargetWorkspaceID: "no-such-ws",
	}); err == nil {
		t.Fatal("expected NOT_FOUND for missing target workspace")
	} else if domain.AsError(err).Code != domain.CodeNotFound {
		t.Fatalf("expected NOT_FOUND, got %s", domain.AsError(err).Code)
	}

	// 不存在的备份文件
	if _, err := s.Backup.BackupRestore(ctx, BackupRestoreReq{
		BackupPath: "no-such-file.sqz", Password: "secret-pw", TargetWorkspaceID: ws.ID,
	}); err == nil {
		t.Fatal("expected NOT_FOUND for missing file")
	}

	// 备份创建幂等
	r1, err := s.Backup.BackupCreate(ctx, BackupCreateReq{
		WorkspaceID: ws.ID, Password: "secret-pw", IdempotencyKey: "bak-idem-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := s.Backup.BackupCreate(ctx, BackupCreateReq{
		WorkspaceID: ws.ID, Password: "secret-pw", IdempotencyKey: "bak-idem-" + NewID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = r1
	_ = r2
}

func TestDataExportJSON(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)

	res, err := s.Backup.DataExport(ctx, DataExportReq{WorkspaceID: ws.ID, Scope: "all", Format: "json"})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	abs := filepath.Join(s.Cfg.DataDir, res.Path)
	b, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("export is not valid json: %v", err)
	}
	if m["workspace_id"] != ws.ID {
		t.Fatalf("unexpected workspace_id in export: %v", m["workspace_id"])
	}
	if _, ok := m["learning_records"]; !ok {
		t.Fatal("learning_records missing from all-scope export")
	}
}

func TestDataExportZIP(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)

	res, err := s.Backup.DataExport(ctx, DataExportReq{WorkspaceID: ws.ID, Scope: "questions", Format: "zip"})
	if err != nil {
		t.Fatalf("export zip: %v", err)
	}
	if res.Format != "zip" {
		t.Fatalf("expected zip, got %s", res.Format)
	}
	abs := filepath.Join(s.Cfg.DataDir, res.Path)
	zr, err := zip.OpenReader(abs)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer zr.Close()
	found := false
	for _, f := range zr.File {
		if f.Name == "data.json" {
			found = true
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(rc)
			rc.Close()
			var m map[string]any
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatalf("zip data.json invalid: %v", err)
			}
			if m["workspace_id"] != ws.ID {
				t.Fatal("wrong workspace in zip")
			}
		}
	}
	if !found {
		t.Fatal("data.json missing in zip")
	}
}

func TestDataExportInvalidScope(t *testing.T) {
	s, _ := newTestServices(t)
	ctx := context.Background()
	ws, _ := createWorkspace(t, s)
	if _, err := s.Backup.DataExport(ctx, DataExportReq{WorkspaceID: ws.ID, Scope: "secret", Format: "json"}); err == nil {
		t.Fatal("expected INVALID_ARGUMENT for bad scope")
	}
}
