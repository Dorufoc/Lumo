package repository

import (
	"context"
	"fmt"
	"strings"

	"lumo/internal/domain"
)

// FTS5 全文检索基础设施（Todo 9/16 的搜索服务复用本层）。
// FTS5 虚拟表与同步触发器在 0005_student.sql 中与源表同事务建立；
// documents_fts 无触发器，由 DocumentService.indexDocument 写入。

const (
	FTSTableNotes      = "notes_fts"
	FTSTableFlashcards = "flashcards_fts"
	FTSTableQuestions  = "questions_fts"
	FTSTableDocuments  = "documents_fts"
)

// ftsIDColumn 各 FTS 表的业务主键列（UNINDEXED 列，用于命中去重/回查）。
var ftsIDColumn = map[string]string{
	FTSTableNotes:      "note_id",
	FTSTableFlashcards: "flashcard_id",
	FTSTableQuestions:  "question_version_id",
	FTSTableDocuments:  "chunk_id",
}

// FTSHit 一次全文检索命中。
type FTSHit struct {
	RowID      int64  // FTS5 内部行号（同源多版本场景可用于排序/去重）
	BusinessID string // 业务主键（note_id / flashcard_id / question_version_id / chunk_id）
}

// isCJK 判断 rune 是否属于 CJK 统一表意文字区（与迁移 *_cjk 列的变换一致，U+4E00~U+9FFF）。
func isCJK(r rune) bool {
	return r >= 0x4E00 && r <= 0x9FFF
}

// SpaceCJK 将输入中的 CJK 字符逐字空格分隔（ASCII/其他字符保持原样）。
// FTS5 unicode61 分词器把整段连续 CJK 视为一个 token，逐字空格后每个汉字成为
// 独立 token，2 字查询（如 "量子"）即可命中含 "量子力学" 的正文。
func SpaceCJK(s string) string {
	var b strings.Builder
	for _, r := range s {
		if isCJK(r) {
			b.WriteByte(' ')
			b.WriteRune(r)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ErrFTSUnavailable 全文检索索引缺失/损坏时的明确错误（调用方应降级而非 panic）。
var ErrFTSUnavailable = domain.NewError(domain.CodeInternal, "全文检索索引不可用")

// SearchFTS 在指定 FTS 索引上执行 MATCH：自动按工作区过滤并按相关度排序。
// 索引表缺失/损坏或 MATCH 语法错误时返回明确领域错误，绝不 panic。
func (r *Repo) SearchFTS(ctx context.Context, table, workspaceID, query string, limit int) ([]FTSHit, error) {
	idCol, ok := ftsIDColumn[table]
	if !ok {
		return nil, domain.InvalidArg("不支持的全文检索索引")
	}
	if strings.TrimSpace(query) == "" {
		return nil, domain.InvalidArg("搜索词不能为空")
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	match := SpaceCJK(query)
	q := fmt.Sprintf(
		`SELECT rowid, %s FROM "%s" WHERE "%s" MATCH ? AND workspace_id = ? ORDER BY rank LIMIT ?`,
		idCol, table, table,
	)
	rows, err := r.db.QueryContext(ctx, q, match, workspaceID, limit)
	if err != nil {
		return nil, ftsUnavailable(err)
	}
	defer rows.Close()
	out := make([]FTSHit, 0, limit)
	for rows.Next() {
		var h FTSHit
		if err := rows.Scan(&h.RowID, &h.BusinessID); err != nil {
			return nil, ftsUnavailable(err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, ftsUnavailable(err)
	}
	return out, nil
}

// ftsUnavailable 将 FTS 底层错误归一为领域错误（消息固定，便于调用方识别降级场景）。
func ftsUnavailable(err error) error {
	return domain.WrapError(domain.CodeInternal, "全文检索索引不可用", err)
}
