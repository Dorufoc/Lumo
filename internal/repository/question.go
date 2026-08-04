package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"lumo/internal/domain"
)

// QuestionRow 是 questions 表行。
type QuestionRow struct {
	ID               string
	WorkspaceID      string
	Type             string
	Status           string
	Source           string
	Tags             json.RawMessage
	ContentHash      string
	CurrentVersionID *string
	CreatedAt        string
	UpdatedAt        string
	DeletedAt        *string
	Version          int
}

// QuestionVersionRow 是 question_versions 表行。
type QuestionVersionRow struct {
	ID          string
	QuestionID  string
	VersionNo   int
	Payload     json.RawMessage
	GeneratedBy *string
	PromptVer   *string
	Review      string
	CreatedAt   string
}

const questionCols = `id, workspace_id, type, status, source, tags_json, content_hash, current_version_id, created_at, updated_at, deleted_at, version`

func scanQuestion(row interface{ Scan(...any) error }) (*QuestionRow, error) {
	var q QuestionRow
	var tags string
	if err := row.Scan(&q.ID, &q.WorkspaceID, &q.Type, &q.Status, &q.Source, &tags, &q.ContentHash,
		&q.CurrentVersionID, &q.CreatedAt, &q.UpdatedAt, &q.DeletedAt, &q.Version); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	q.Tags = json.RawMessage(tags)
	return &q, nil
}

// CreateQuestion 创建题目。
func (r *Repo) CreateQuestion(ctx context.Context, q *QuestionRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO questions (id, workspace_id, type, status, source, tags_json, content_hash, version)
		VALUES (?, ?, ?, 'draft', ?, ?, ?, 1)`,
		q.ID, q.WorkspaceID, q.Type, q.Source, string(q.Tags), q.ContentHash)
	return normalizeErr(err)
}

// GetQuestion 获取未删除题目。
func (r *Repo) GetQuestion(ctx context.Context, wsID, id string) (*QuestionRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+questionCols+` FROM questions
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`, id, wsID)
	return scanQuestion(row)
}

// GetQuestionByContentHash 按规范化内容哈希查重。
func (r *Repo) GetQuestionByContentHash(ctx context.Context, wsID, hash string) (*QuestionRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+questionCols+` FROM questions
		WHERE workspace_id = ? AND content_hash = ? AND deleted_at IS NULL`, wsID, hash)
	return scanQuestion(row)
}

// QuestionFilter 是列表过滤条件。
type QuestionFilter struct {
	Type        string
	Status      string
	KnowledgeID string
	Keyword     string
	Cursor      string
	Limit       int
}

// ListQuestions 分页列出题目。
func (r *Repo) ListQuestions(ctx context.Context, wsID string, f QuestionFilter) ([]*QuestionRow, string, bool, error) {
	limit := f.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var conds []string
	var args []any
	conds = append(conds, "q.workspace_id = ?")
	args = append(args, wsID)
	conds = append(conds, "q.deleted_at IS NULL")
	if f.Type != "" {
		conds = append(conds, "q.type = ?")
		args = append(args, f.Type)
	}
	if f.Status != "" {
		conds = append(conds, "q.status = ?")
		args = append(args, f.Status)
	}
	if f.Keyword != "" {
		conds = append(conds, "(q.id IN (SELECT question_id FROM question_versions WHERE payload_json LIKE ? ESCAPE '\\'))")
		args = append(args, "%"+escapeLike(f.Keyword)+"%")
	}
	join := ""
	if f.KnowledgeID != "" {
		join = ` JOIN question_knowledge qk ON qk.question_version_id = q.current_version_id`
		conds = append(conds, "qk.knowledge_id = ?")
		args = append(args, f.KnowledgeID)
	}
	if f.Cursor != "" {
		conds = append(conds, "q.id < ?")
		args = append(args, f.Cursor)
	}
	query := `SELECT ` + questionCols + ` FROM questions q` + join +
		` WHERE ` + strings.Join(conds, " AND ") +
		` ORDER BY q.id DESC LIMIT ?`
	args = append(args, limit+1)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", false, normalizeErr(err)
	}
	defer rows.Close()
	var out []*QuestionRow
	for rows.Next() {
		q, err := scanQuestion(rows)
		if err != nil {
			return nil, "", false, err
		}
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, "", false, normalizeErr(err)
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	next := ""
	if hasMore && len(out) > 0 {
		next = out[len(out)-1].ID
	}
	return out, next, hasMore, nil
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// CreateQuestionVersion 创建不可变版本；触发器更新题目 current_version_id。
func (r *Repo) CreateQuestionVersion(ctx context.Context, v *QuestionVersionRow) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO question_versions (id, question_id, version_no, payload_json, generated_by_model, prompt_version, review_status)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		v.ID, v.QuestionID, v.VersionNo, string(v.Payload), v.GeneratedBy, v.PromptVer, v.Review)
	return normalizeErr(err)
}

// GetQuestionVersion 获取版本。
func (r *Repo) GetQuestionVersion(ctx context.Context, id string) (*QuestionVersionRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, question_id, version_no, payload_json, generated_by_model, prompt_version, review_status, created_at
		FROM question_versions WHERE id = ?`, id)
	var v QuestionVersionRow
	var payload string
	if err := row.Scan(&v.ID, &v.QuestionID, &v.VersionNo, &payload, &v.GeneratedBy, &v.PromptVer, &v.Review, &v.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	v.Payload = json.RawMessage(payload)
	return &v, nil
}

// NextVersionNo 返回题目下一个版本号。
func (r *Repo) NextVersionNo(ctx context.Context, questionID string) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version_no), 0) + 1 FROM question_versions WHERE question_id = ?`, questionID).Scan(&n); err != nil {
		return 0, normalizeErr(err)
	}
	return n, nil
}

// UpdateQuestionStatus 乐观锁更新题目状态。
func (r *Repo) UpdateQuestionStatus(ctx context.Context, wsID, id string, version int, status string) (*QuestionRow, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE questions SET status = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), version = version + 1
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL AND version = ?`,
		status, id, wsID, version)
	if err != nil {
		return nil, normalizeErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		cur, err := r.GetQuestion(ctx, wsID, id)
		if err != nil {
			return nil, err
		}
		if cur == nil {
			return nil, NotFoundErr("题目", id)
		}
		return nil, domain.Conflict("题目已被修改，请刷新后重试")
	}
	return r.GetQuestion(ctx, wsID, id)
}
