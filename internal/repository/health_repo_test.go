package repository

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"lumo/internal/database"
	"lumo/internal/domain"
)

// seedHealthWorkspace 直接写工作区与用户行（health_settings 外键依赖），返回 (wsID, userID)。
func seedHealthWorkspace(t *testing.T, db *sql.DB) (string, string) {
	t.Helper()
	wsID := "ws-health-1"
	userID := "u-health-1"
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO workspaces (id, name, owner_type) VALUES (?, ?, 'local')`, wsID, "健康测试"); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO users (id, workspace_id, display_name) VALUES (?, ?, '学生')`, userID, wsID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return wsID, userID
}

func TestHealthSettingsUpsertCreateAndReadBack(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(filepath.Join(t.TempDir(), "health.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	r := New(db)
	wsID, userID := seedHealthWorkspace(t, db)

	// 缺失 → nil
	got, err := r.GetHealthSettings(ctx, wsID, userID)
	if err != nil {
		t.Fatalf("get missing: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for missing settings, got %+v", got)
	}

	// 创建 → 回读，created_at/updated_at 非空
	row := &HealthSettingsRow{
		WorkspaceID: wsID, UserID: userID,
		SedentaryEnabled: 1, EyeEnabled: 1, NightMode: domain.NightModeAuto,
		BlueLightFilter: 0, StatsEnabled: 1, UpdatedAt: "2026-08-06T10:00:00Z",
	}
	if err := r.UpsertHealthSettings(ctx, row); err != nil {
		t.Fatalf("upsert create: %v", err)
	}
	got, err = r.GetHealthSettings(ctx, wsID, userID)
	if err != nil {
		t.Fatalf("get after create: %v", err)
	}
	if got == nil {
		t.Fatal("expected settings after create")
	}
	if got.CreatedAt == "" || got.UpdatedAt != "2026-08-06T10:00:00Z" {
		t.Fatalf("expected non-empty created_at + caller updated_at, got %+v", got)
	}
	if got.SedentaryEnabled != 1 || got.NightMode != domain.NightModeAuto || got.StatsEnabled != 1 {
		t.Fatalf("unexpected settings after create: %+v", got)
	}

	// 更新 → 仍一行，值变化，created_at 保留
	row.UpdatedAt = "2026-08-06T12:00:00Z"
	row.SedentaryEnabled = 0
	row.NightMode = domain.NightModeDark
	row.StatsEnabled = 0
	if err := r.UpsertHealthSettings(ctx, row); err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	got, err = r.GetHealthSettings(ctx, wsID, userID)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if got.SedentaryEnabled != 0 || got.NightMode != domain.NightModeDark || got.StatsEnabled != 0 {
		t.Fatalf("unexpected settings after update: %+v", got)
	}
	if got.UpdatedAt != "2026-08-06T12:00:00Z" {
		t.Fatalf("expected updated_at from caller, got %s", got.UpdatedAt)
	}
	if got.CreatedAt == "" {
		t.Fatal("created_at must be preserved on update")
	}

	// 行数仍为 1
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM health_settings WHERE workspace_id = ? AND user_id = ?`, wsID, userID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 row, got %d", n)
	}
}

func TestHealthSettingsUpsertRejectsBadNightMode(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(filepath.Join(t.TempDir(), "health-bad.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	r := New(db)
	wsID, userID := seedHealthWorkspace(t, db)

	row := &HealthSettingsRow{
		WorkspaceID: wsID, UserID: userID,
		SedentaryEnabled: 1, EyeEnabled: 1, NightMode: "sepia",
		BlueLightFilter: 0, StatsEnabled: 1, UpdatedAt: "2026-08-06T10:00:00Z",
	}
	err = r.UpsertHealthSettings(ctx, row)
	if err == nil {
		t.Fatal("expected error for invalid night_mode")
	}
	if domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("expected INVALID_ARGUMENT (CHECK constraint), got %s", domain.AsError(err).Code)
	}
}
