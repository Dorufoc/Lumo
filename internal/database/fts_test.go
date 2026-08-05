package database

// Todo 2 全文检索（FTS5）迁移测试。
// TDD：在 0005_student.sql 追加 FTS5 表/触发器之前先运行 → 断言失败（RED），追加后转绿。

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// ftsTables 6.1.1 全文检索的 4 张 FTS5 虚拟表（追加于 0005_student.sql）。
var ftsTables = []string{"notes_fts", "flashcards_fts", "questions_fts", "documents_fts"}

// ftsVirtualTables 返回迁移后存在的 FTS5 虚拟表集合。
func ftsVirtualTables(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND sql LIKE 'CREATE VIRTUAL TABLE%'`)
	if err != nil {
		t.Fatalf("list virtual tables: %v", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan virtual table: %v", err)
		}
		out[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// ftsTriggerCount 迁移建立的 FTS 同步触发器总数。
const ftsTriggerCount = 7

func TestFTS5TablesCreatedByMigration(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	got := ftsVirtualTables(t, db)
	for _, tbl := range ftsTables {
		if !got[tbl] {
			t.Errorf("迁移后缺少 FTS 虚拟表 %s", tbl)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name IN (
		'trg_notes_fts_insert','trg_notes_fts_update','trg_notes_fts_delete',
		'trg_flashcards_fts_insert','trg_flashcards_fts_update','trg_flashcards_fts_delete',
		'trg_questions_fts_insert')`).Scan(&n); err != nil {
		t.Fatalf("count fts triggers: %v", err)
	}
	if n != ftsTriggerCount {
		t.Errorf("期望 %d 个 FTS 同步触发器，得到 %d", ftsTriggerCount, n)
	}
}

// insertFixture 建立工作区 + 默认用户，返回 (workspaceID, userID)。
func insertFixture(t *testing.T, db *sql.DB) (string, string) {
	t.Helper()
	wsID := fmt.Sprintf("ws-fts-%s", t.Name())
	userID := fmt.Sprintf("u-fts-%s", t.Name())
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO workspaces (id, name, owner_type) VALUES (?, 'FTS 测试', 'local')`, wsID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users (id, workspace_id, display_name, role) VALUES (?, ?, '测试用户', 'student')`, userID, wsID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return wsID, userID
}

func TestNotesFTSSyncOnInsertUpdateDelete(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	wsID, userID := insertFixture(t, db)
	noteID := "n-fts-1"
	if _, err := db.ExecContext(ctx, `INSERT INTO notes (id, workspace_id, user_id, title, body_md)
		VALUES (?, ?, ?, '量子力学复习', '薛定谔方程描述量子态的时间演化。')`, noteID, wsID, userID); err != nil {
		t.Fatalf("insert note: %v", err)
	}

	// 连续 CJK 整串不会被 unicode61 拆开，查询端需逐字空格（repository.SearchFTS 承担）：
	// 此处验证 *_cjk 列已按逐字空格写入。
	var titleCJK, bodyCJK string
	if err := db.QueryRow(`SELECT title_cjk, body_cjk FROM notes_fts WHERE note_id = ?`, noteID).
		Scan(&titleCJK, &bodyCJK); err != nil {
		t.Fatalf("query notes_fts: %v", err)
	}
	if !strings.Contains(bodyCJK, "薛 定 谔") {
		t.Errorf("body_cjk 应逐字空格：%q", bodyCJK)
	}
	if strings.Contains(bodyCJK, "薛定谔") {
		t.Errorf("body_cjk 不应包含连续 CJK 串：%q", bodyCJK)
	}
	if !strings.Contains(titleCJK, "量 子 力 学") {
		t.Errorf("title_cjk 应逐字空格：%q", titleCJK)
	}

	// 更新正文 → 旧词消失、新词命中
	if _, err := db.ExecContext(ctx, `UPDATE notes SET body_md = '牛顿运动定律是经典力学的基础。' WHERE id = ?`, noteID); err != nil {
		t.Fatalf("update note: %v", err)
	}
	if n := ftsMatchCount(t, db, "notes_fts", "薛 定 谔"); n != 0 {
		t.Errorf("更新后旧词 '薛定谔' 应不再命中，得到 %d", n)
	}
	if n := ftsMatchCount(t, db, "notes_fts", "牛 顿"); n != 1 {
		t.Errorf("更新后新词 '牛顿' 应命中 1 条，得到 %d", n)
	}

	// 软删除 → 从索引移除
	if _, err := db.ExecContext(ctx, `UPDATE notes SET deleted_at = '2026-01-01T00:00:00Z' WHERE id = ?`, noteID); err != nil {
		t.Fatalf("soft delete note: %v", err)
	}
	if n := ftsMatchCount(t, db, "notes_fts", "牛 顿"); n != 0 {
		t.Errorf("软删除后不应再命中，得到 %d", n)
	}

	// 物理删除 → 索引行清理
	if _, err := db.ExecContext(ctx, `DELETE FROM notes WHERE id = ?`, noteID); err != nil {
		t.Fatalf("hard delete note: %v", err)
	}
	var left int
	if err := db.QueryRow(`SELECT count(*) FROM notes_fts WHERE note_id = ?`, noteID).Scan(&left); err != nil {
		t.Fatalf("count leftover: %v", err)
	}
	if left != 0 {
		t.Errorf("物理删除后 notes_fts 残留 %d 行", left)
	}
}

func TestFlashcardsFTSSyncOnInsertUpdate(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	wsID, userID := insertFixture(t, db)
	cardID := "fc-fts-1"
	if _, err := db.ExecContext(ctx, `INSERT INTO flashcards (id, workspace_id, user_id, front, back, due_at)
		VALUES (?, ?, ?, '量子纠缠', '两个粒子之间非定域的关联。', '2026-01-01T00:00:00Z')`, cardID, wsID, userID); err != nil {
		t.Fatalf("insert flashcard: %v", err)
	}
	if n := ftsMatchCount(t, db, "flashcards_fts", "纠 缠"); n != 1 {
		t.Errorf("插入后 '纠缠' 应命中 1 条，得到 %d", n)
	}
	var frontCJK string
	if err := db.QueryRow(`SELECT front_cjk FROM flashcards_fts WHERE flashcard_id = ?`, cardID).Scan(&frontCJK); err != nil {
		t.Fatalf("query flashcards_fts: %v", err)
	}
	if !strings.Contains(frontCJK, "量 子 纠 缠") {
		t.Errorf("front_cjk 应逐字空格：%q", frontCJK)
	}
	// 更新 back → 旧词消失
	if _, err := db.ExecContext(ctx, `UPDATE flashcards SET back = '统计相关性的一种表现。' WHERE id = ?`, cardID); err != nil {
		t.Fatalf("update flashcard: %v", err)
	}
	if n := ftsMatchCount(t, db, "flashcards_fts", "非 定 域"); n != 0 {
		t.Errorf("更新后旧词应不再命中，得到 %d", n)
	}
}

func TestQuestionsFTSSyncOnInsert(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	wsID, _ := insertFixture(t, db)
	qID := "q-fts-1"
	if _, err := db.ExecContext(ctx, `INSERT INTO questions (id, workspace_id, type, content_hash)
		VALUES (?, ?, 'single_choice', ?)`, qID, wsID, strings.Repeat("ab", 32)); err != nil {
		t.Fatalf("insert question: %v", err)
	}
	payload := `{"stem":"量子力学中薛定谔方程描述什么？","options":[{"text":"波函数演化"},{"text":"牛顿定律"}],"analysis":"薛定谔方程描述量子态的时间演化。"}`
	if _, err := db.ExecContext(ctx, `INSERT INTO question_versions (id, question_id, version_no, payload_json)
		VALUES ('qv-fts-1', ?, 1, ?)`, qID, payload); err != nil {
		t.Fatalf("insert question_version: %v", err)
	}
	if n := ftsMatchCount(t, db, "questions_fts", "薛 定 谔"); n != 1 {
		t.Errorf("插入版本后 '薛定谔' 应命中 1 条，得到 %d", n)
	}
	var body string
	if err := db.QueryRow(`SELECT body FROM questions_fts WHERE question_version_id = 'qv-fts-1'`).Scan(&body); err != nil {
		t.Fatalf("query questions_fts: %v", err)
	}
	for _, want := range []string{"波函数演化", "牛顿定律", "时间演化"} {
		if !strings.Contains(body, want) {
			t.Errorf("questions_fts.body 应包含 %q，实际 %q", want, body)
		}
	}
}

// ftsMatchCount 在指定 FTS 表上执行（查询端已空格化的）MATCH 并统计命中数。
func ftsMatchCount(t *testing.T, db *sql.DB, table, spacedQuery string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(fmt.Sprintf(`SELECT count(*) FROM "%s" WHERE "%s" MATCH ?`, table, table), spacedQuery).Scan(&n); err != nil {
		t.Fatalf("MATCH on %s: %v", table, err)
	}
	return n
}
