package database

// 0005 迁移回归测试（TDD：先于 0005_student.sql 编写，未实现时必须失败）。
// 覆盖：全新库 47 张扩展表 + review_events 列扩展 + settings/import_batches 保持旧结构
//       + timer_sessions 扩展列 + 唯一约束 + 升级路径（旧库数据不丢失、新列默认值正确）。

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// studentTables 是 0005 按完整设计文档 6.2.1 创建/扩展的表清单（47 张，含 3 张既有表）。
var studentTables = []string{
	"notes", "note_annotations", "flashcards", "flashcard_reviews",
	"exam_papers", "exam_paper_sections", "exams", "checkins",
	"achievement_defs", "user_achievements", "reports", "report_cache",
	"timer_sessions", "reminders", "notifications", "favorites",
	"read_later", "document_bookmarks", "document_summaries", "calendar_events",
	"goal_milestones", "health_settings", "speaking_results", "knowledge_relations",
	"mastery_snapshots", "shares", "content_requests", "family_bindings",
	"parent_settings", "classes", "class_members", "assignments",
	"assignment_submissions", "grading_appeals", "review_queue_items",
	"content_reports", "feature_flags", "provider_policies", "plugins",
	"webhook_subscriptions", "webhook_deliveries", "usage_events",
	"agent_tool_calls", "streak_snapshots", "content_scan_results",
	"attachments", "devices", "review_events", "settings", "import_batches",
}

// forbiddenTables 是 6.2.1 清单之外的禁止表名（不得创建）。
var forbiddenTables = []string{
	"focus_sessions", "health_metrics", "class_students", "grading_reviews",
	"webhooks", "achievements", "community_requests",
	"knowledge_graph_notes", "knowledge_graph_relations", "knowledge_graph_edges",
}

// tableColumns 返回表的列名集合（PRAGMA table_info）。
func tableColumns(t *testing.T, db *sql.DB, table string) map[string]bool {
	t.Helper()
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan pragma row for %s: %v", table, err)
		}
		cols[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate pragma rows for %s: %v", table, err)
	}
	return cols
}

// indexColumns 返回某索引的列序（PRAGMA index_info）。
func indexColumns(t *testing.T, db *sql.DB, index string) []string {
	t.Helper()
	rows, err := db.Query(fmt.Sprintf("PRAGMA index_info(%s)", index))
	if err != nil {
		t.Fatalf("PRAGMA index_info(%s): %v", index, err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var seqno, cid int
		var name string
		if err := rows.Scan(&seqno, &cid, &name); err != nil {
			t.Fatalf("scan index_info(%s): %v", index, err)
		}
		cols = append(cols, name)
	}
	return cols
}

// hasUniqueIndex 报告表上是否存在列序完全一致的唯一索引。
func hasUniqueIndex(t *testing.T, db *sql.DB, table string, cols []string) bool {
	t.Helper()
	rows, err := db.Query(fmt.Sprintf("PRAGMA index_list(%s)", table))
	if err != nil {
		t.Fatalf("PRAGMA index_list(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var seq int
		var name string
		var unique int
		var origin any
		var partial any
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			t.Fatalf("scan index_list(%s): %v", table, err)
		}
		if unique != 1 {
			continue
		}
		if reflect.DeepEqual(indexColumns(t, db, name), cols) {
			return true
		}
	}
	return false
}

// TestMigrateV0005Fresh 全新库迁移：47 张表齐备、禁止表不存在、扩展列/唯一约束正确。
func TestMigrateV0005Fresh(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate fresh: %v", err)
	}

	// 6.2.1 全部 47 张表必须存在。
	for _, table := range studentTables {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil {
			t.Fatalf("check table %s: %v", table, err)
		}
		if n != 1 {
			t.Errorf("6.2.1 table %s missing after fresh migrate", table)
		}
	}

	// 禁止表名必须不存在。
	for _, table := range forbiddenTables {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil {
			t.Fatalf("check forbidden table %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("forbidden table %s must not exist", table)
		}
	}

	// review_events：0001 旧列保留 + 6.2.1 新列追加（0005 ALTER）。
	reCols := tableColumns(t, db, "review_events")
	for _, col := range []string{"id", "review_card_id", "rating", "previous_json", "current_json", "created_at"} {
		if !reCols[col] {
			t.Errorf("review_events lost existing column %s", col)
		}
	}
	for _, col := range []string{"user_id", "interval_days", "due_at", "reviewed_at"} {
		if !reCols[col] {
			t.Errorf("review_events missing extended column %s", col)
		}
	}

	// settings 保持 0002 结构，不得新增 user_id/key/value_json。
	wantSettings := map[string]bool{"workspace_id": true, "settings_json": true, "created_at": true, "updated_at": true, "version": true}
	gotSettings := tableColumns(t, db, "settings")
	for col := range gotSettings {
		if !wantSettings[col] {
			t.Errorf("settings gained unexpected column %s (must keep 0002 shape)", col)
		}
	}
	for col := range wantSettings {
		if !gotSettings[col] {
			t.Errorf("settings lost column %s", col)
		}
	}

	// import_batches 保持 0001 结构，不得按 6.2.1 重建。
	ibCols := tableColumns(t, db, "import_batches")
	for _, col := range []string{"id", "workspace_id", "idempotency_key", "file_name", "file_hash", "format", "status", "total_count", "valid_count", "error_count", "created_at", "updated_at"} {
		if !ibCols[col] {
			t.Errorf("import_batches lost column %s", col)
		}
	}
	for _, col := range []string{"user_id", "source_type", "stats_json", "error_summary_json", "committed_at"} {
		if ibCols[col] {
			t.Errorf("import_batches gained 6.2.1-only column %s (must keep 0001 shape)", col)
		}
	}

	// timer_sessions：6.2.1 列 + 决策 a 扩展 started_at/ended_at。
	tsCols := tableColumns(t, db, "timer_sessions")
	for _, col := range []string{"id", "workspace_id", "user_id", "mode", "planned_minutes", "actual_seconds", "task_id", "status", "interrupt_reason"} {
		if !tsCols[col] {
			t.Errorf("timer_sessions lost column %s", col)
		}
	}
	for _, col := range []string{"started_at", "ended_at"} {
		if !tsCols[col] {
			t.Errorf("timer_sessions missing extension column %s", col)
		}
	}

	// 6.2.1 内联唯一约束 + 6.3.1 中作用于 0005 新表的唯一约束。
	for _, uc := range []struct{ table string; cols []string }{
		{"favorites", []string{"user_id", "ref_type", "ref_id"}},
		{"checkins", []string{"user_id", "date"}},
		{"attachments", []string{"sha256"}},
	} {
		if !hasUniqueIndex(t, db, uc.table, uc.cols) {
			t.Errorf("%s(%s) unique constraint missing", uc.table, strings.Join(uc.cols, ","))
		}
	}

	// 6.3.1 作用于 0005 新表的索引抽查（review_events 新索引不能与 0001 同名冲突）。
	for _, idx := range []string{
		"idx_flashcards_user_due_state", "idx_flashcards_user_source",
		"idx_flashcard_reviews_card_reviewed", "idx_notes_user_kind", "idx_notes_user_updated",
		"idx_note_annotations_document", "idx_exam_papers_user_status", "idx_exams_user_status",
		"idx_exams_paper", "idx_user_achievements_user", "idx_reports_user_period",
		"idx_timer_sessions_user_started", "idx_reminders_user_kind_next",
		"idx_notifications_user_read", "idx_favorites_user_group", "idx_read_later_user_status",
		"idx_classes_owner", "idx_class_members_class", "idx_assignments_class_status",
		"idx_assignment_submissions_assignment", "idx_grading_appeals_status",
		"idx_review_queue_items_status", "idx_usage_events_user_type_occurred",
		"idx_attachments_workspace_deleted", "idx_devices_workspace_status",
		"idx_review_events_user_due", "idx_review_events_card_reviewed",
	} {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, idx).Scan(&n); err != nil {
			t.Fatalf("check index %s: %v", idx, err)
		}
		if n != 1 {
			t.Errorf("6.3.1 index %s missing", idx)
		}
	}

	// 既有索引 idx_review_events_card_created 必须保留（与新增索引不冲突）。
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='idx_review_events_card_created'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("existing index idx_review_events_card_created must be kept")
	}
}

// applyMigrationsUpTo 复刻 db.go 的迁移逻辑，只应用文件名 <= upTo 的迁移。
func applyMigrationsUpTo(t *testing.T, db *sql.DB, upTo string) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
		checksum TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	names, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	sort.Strings(names)
	for _, name := range names {
		version := strings.TrimSuffix(filepath.Base(name), ".sql")
		// 只比较数字前缀（如 "0004_practice" → "0004"），避免全名比较跳过 0004。
		num := version
		if i := strings.IndexByte(num, '_'); i >= 0 {
			num = num[:i]
		}
		if num > upTo {
			continue
		}
		b, err := migrationsFS.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		sum := sha256.Sum256(b)
		tx, err := db.Begin()
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if _, err := tx.Exec(string(b)); err != nil {
			tx.Rollback()
			t.Fatalf("apply migration %s: %v", version, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, checksum) VALUES (?, ?)`, version, hex.EncodeToString(sum[:])); err != nil {
			tx.Rollback()
			t.Fatalf("record migration %s: %v", version, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit migration %s: %v", version, err)
		}
	}
}

// seedRows 在 0001–0004 旧库中插入历史业务数据（外键链完整）。
func seedRows(t *testing.T, db *sql.DB) {
	t.Helper()
	inserts := []string{
		`INSERT INTO workspaces (id, name, owner_type) VALUES ('w1', 'ws', 'local')`,
		`INSERT INTO users (id, workspace_id, display_name, role) VALUES ('u1', 'w1', 'u', 'student')`,
		`INSERT INTO questions (id, workspace_id, type, status, content_hash) VALUES ('q1', 'w1', 'single_choice', 'published', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')`,
		`INSERT INTO question_versions (id, question_id, version_no, payload_json) VALUES ('qv1', 'q1', 1, '{"body":"x"}')`,
		`INSERT INTO practice_sessions (id, workspace_id, user_id, mode, question_snapshot_json) VALUES ('ps1', 'w1', 'u1', 'practice', '[]')`,
		`INSERT INTO submissions (id, session_id, question_version_id, status, answer_json) VALUES ('s1', 'ps1', 'qv1', 'submitted', '{}')`,
		`INSERT INTO wrong_answers (id, workspace_id, user_id, submission_id, question_version_id, answer_json) VALUES ('wa1', 'w1', 'u1', 's1', 'qv1', '{}')`,
		`INSERT INTO review_cards (id, workspace_id, user_id, wrong_answer_id, due_at) VALUES ('rc1', 'w1', 'u1', 'wa1', '2026-01-01T00:00:00Z')`,
		`INSERT INTO review_events (id, review_card_id, rating, previous_json, current_json) VALUES ('re1', 'rc1', 'good', '{"r":0}', '{"r":1}')`,
		`INSERT INTO settings (workspace_id, settings_json) VALUES ('w1', '{"theme":"dark"}')`,
		`INSERT INTO import_batches (id, workspace_id, idempotency_key, file_name, file_hash, format) VALUES ('ib1', 'w1', 'ik1', 'f.md', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', 'markdown')`,
	}
	for _, q := range inserts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
}

// TestMigrateV0005Upgrade 升级路径：旧库(0001–0004)含数据 → 应用 0005 → 数据不丢失、新列默认值正确。
func TestMigrateV0005Upgrade(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	// 先应用 0001–0004（模拟既有旧库）。
	applyMigrationsUpTo(t, db, "0004")
	seedRows(t, db)

	// 应用 0005（及之后；实际只有 0005 待执行）。
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate upgrade: %v", err)
	}

	// 0005 必须已记录（db.go 以完整文件名记录版本，如 "0005_student"）。
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version LIKE '0005%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("0005 not recorded in schema_migrations")
	}

	// review_events 历史行保留，新列默认 NULL/0。
	var rating, createdAt string
	if err := db.QueryRow(`SELECT rating, created_at FROM review_events WHERE id='re1'`).Scan(&rating, &createdAt); err != nil {
		t.Fatalf("review_events row lost after upgrade: %v", err)
	}
	if rating != "good" || createdAt == "" {
		t.Errorf("review_events data changed: rating=%q created_at=%q", rating, createdAt)
	}
	var uid sql.NullString
	var interval int
	var dueAt, reviewedAt sql.NullString
	if err := db.QueryRow(`SELECT user_id, interval_days, due_at, reviewed_at FROM review_events WHERE id='re1'`).Scan(&uid, &interval, &dueAt, &reviewedAt); err != nil {
		t.Fatalf("read extended review_events columns: %v", err)
	}
	if uid.Valid {
		t.Errorf("historical review_events.user_id should be NULL, got %q", uid.String)
	}
	if interval != 0 {
		t.Errorf("historical review_events.interval_days should default to 0, got %d", interval)
	}
	if dueAt.Valid {
		t.Errorf("historical review_events.due_at should be NULL, got %q", dueAt.String)
	}
	if reviewedAt.Valid {
		t.Errorf("historical review_events.reviewed_at should be NULL, got %q", reviewedAt.String)
	}

	// settings 行保留且内容未变（0002 结构不动）。
	var sj string
	if err := db.QueryRow(`SELECT settings_json FROM settings WHERE workspace_id='w1'`).Scan(&sj); err != nil {
		t.Fatalf("settings row lost: %v", err)
	}
	if !strings.Contains(sj, "dark") {
		t.Errorf("settings_json content changed: %s", sj)
	}

	// import_batches 行保留且状态未变（0001 结构不动）。
	var ibStatus string
	if err := db.QueryRow(`SELECT status FROM import_batches WHERE id='ib1'`).Scan(&ibStatus); err != nil {
		t.Fatalf("import_batches row lost: %v", err)
	}
	if ibStatus != "pending" {
		t.Errorf("import_batches status changed: %s", ibStatus)
	}

	// 0005 新表可写入（结构可用）。
	if _, err := db.Exec(`INSERT INTO timer_sessions (id, workspace_id, user_id, mode, planned_minutes, actual_seconds, status, started_at, ended_at)
		VALUES ('ts1', 'w1', 'u1', 'pomodoro', 25, 1500, 'completed', '2026-01-01T08:00:00Z', '2026-01-01T08:25:00Z')`); err != nil {
		t.Fatalf("insert into timer_sessions failed: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO notes (id, workspace_id, user_id, kind, title) VALUES ('n1', 'w1', 'u1', 'manual', 'note')`); err != nil {
		t.Fatalf("insert into notes failed: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO checkins (id, user_id, date, kind, minutes) VALUES ('ck1', 'u1', '2026-01-01', 'normal', 30)`); err != nil {
		t.Fatalf("insert into checkins failed: %v", err)
	}
}
