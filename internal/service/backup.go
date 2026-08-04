package service

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"lumo/internal/crypto"
	"lumo/internal/domain"
	"lumo/internal/repository"
)

// BackupResult 是备份结果。
type BackupResult struct {
	Path      string `json:"path"` // 相对路径（相对备份目录）
	FileName  string `json:"file_name"`
	SizeBytes int64  `json:"size_bytes"`
	CreatedAt string `json:"created_at"`
}

// RestoreResult 是恢复结果。
type RestoreResult struct {
	Restored   bool   `json:"restored"`
	WorkspaceID string `json:"workspace_id"`
	RestoredAt string `json:"restored_at"`
}

// ExportResult 是导出结果。
type ExportResult struct {
	Path      string `json:"path"` // 相对路径（相对数据目录）
	FileName  string `json:"file_name"`
	Format    string `json:"format"`
	SizeBytes int64  `json:"size_bytes"`
}

// safeTimestamp 返回可用于文件名的 UTC 时间戳（Windows 不允许冒号）。
func safeTimestamp() string {
	return strings.ReplaceAll(Now(), ":", "-")
}

// BackupService 实现备份、恢复与数据导出。
type BackupService struct{ s *Services }

// BackupCreateReq 创建备份请求。
type BackupCreateReq struct {
	WorkspaceID    string `json:"workspace_id"`
	Password       string `json:"password"`
	IdempotencyKey string `json:"idempotency_key"`
}

// BackupCreate 创建一致性快照并加密保存到备份目录。
func (b *BackupService) BackupCreate(ctx context.Context, req BackupCreateReq) (*BackupResult, error) {
	if err := b.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if len(req.Password) < 4 {
		return nil, domain.InvalidArg("密码至少 4 个字符")
	}
	if req.IdempotencyKey == "" {
		return nil, domain.InvalidArg("idempotency_key 必填")
	}
	return withIdempotency(b.s, ctx, req.WorkspaceID, req.IdempotencyKey, "BackupCreate", func() (*BackupResult, error) {
		ts := Now()
		rand, _ := crypto.RandomUint64()
		snapshot := filepath.Join(b.s.Cfg.BackupsDir, fmt.Sprintf("snapshot-%s-%x.db", safeTimestamp(), rand))
		outName := fmt.Sprintf("backup-%s-%s.sqz", safeTimestamp(), shortID(req.WorkspaceID))
		outPath := filepath.Join(b.s.Cfg.BackupsDir, outName)
		defer os.Remove(snapshot)

		// 一致性快照（VACUUM INTO 不依赖 WAL 状态）。
		if _, err := b.s.Repo.DB().ExecContext(ctx, `VACUUM INTO ?`, snapshot); err != nil {
			return nil, domain.WrapError(domain.CodeDatabaseUnavailable, "创建数据库快照失败", err)
		}
		if err := crypto.EncryptFile(snapshot, outPath, req.Password); err != nil {
			return nil, domain.WrapError(domain.CodeInternal, "加密备份失败", err)
		}
		st, _ := os.Stat(outPath)
		b.s.audit(ctx, req.WorkspaceID, "backup.create", "backup", outName, map[string]any{"size": st.Size()})
		return &BackupResult{Path: outName, FileName: outName, SizeBytes: st.Size(), CreatedAt: ts}, nil
	})
}

// BackupRestoreReq 恢复备份请求。
type BackupRestoreReq struct {
	BackupPath        string `json:"backup_path"`
	Password          string `json:"password"`
	TargetWorkspaceID string `json:"target_workspace_id"`
}

// BackupRestore 校验并整体恢复数据库。
// 流程：解密 → 完整性校验 → 目标工作区存在性校验 → 保护性备份 → 原子替换。
func (b *BackupService) BackupRestore(ctx context.Context, req BackupRestoreReq) (*RestoreResult, error) {
	if b.s.RestoreMu != nil {
		b.s.RestoreMu.Lock()
		defer b.s.RestoreMu.Unlock()
	}
	if req.BackupPath == "" {
		return nil, domain.InvalidArg("backup_path 必填")
	}
	if req.Password == "" {
		return nil, domain.InvalidArg("password 必填")
	}
	if req.TargetWorkspaceID == "" {
		return nil, domain.InvalidArg("target_workspace_id 必填")
	}
	srcPath, err := resolveLocalPath(req.BackupPath, b.s.Cfg.BackupsDir)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(srcPath); err != nil {
		return nil, domain.NotFound("备份文件不存在")
	}

	rand, _ := crypto.RandomUint64()
	staging := filepath.Join(b.s.Cfg.BackupsDir, fmt.Sprintf("staging-%x.db", rand))
	defer os.Remove(staging)

	if err := crypto.DecryptFile(srcPath, staging, req.Password); err != nil {
		return nil, domain.InvalidArg("%v", err)
	}

	// 打开并校验暂存库。
	tmpDB, err := sql.Open("sqlite", staging)
	if err != nil {
		return nil, domain.WrapError(domain.CodeImportFailed, "备份文件不是有效数据库", err)
	}
	var integrity string
	if err := tmpDB.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		tmpDB.Close()
		return nil, domain.InvalidArg("备份文件完整性校验失败")
	}
	var wsCount int
	if err := tmpDB.QueryRowContext(ctx,
		`SELECT count(*) FROM workspaces WHERE id = ? AND deleted_at IS NULL`, req.TargetWorkspaceID).Scan(&wsCount); err != nil || wsCount == 0 {
		tmpDB.Close()
		return nil, domain.NotFound("备份中不存在目标工作区")
	}
	tmpDB.Close()

	// 保护性备份当前数据库。
	preName := fmt.Sprintf("pre-restore-%s.db", safeTimestamp())
	if _, err := b.s.Repo.DB().ExecContext(ctx, `VACUUM INTO ?`, filepath.Join(b.s.Cfg.BackupsDir, preName)); err != nil {
		return nil, domain.WrapError(domain.CodeDatabaseUnavailable, "保护性备份失败", err)
	}

	// 原子替换（由 app 层注入的 SwapDB 负责连接生命周期）。
	newDB, err := b.s.SwapDB(staging)
	if err != nil {
		return nil, domain.WrapError(domain.CodeDatabaseUnavailable, "数据库替换失败，已保留原数据", err)
	}
	b.s.Repo = repository.New(newDB)

	b.s.audit(ctx, req.TargetWorkspaceID, "backup.restore", "backup", filepath.Base(srcPath), nil)
	return &RestoreResult{Restored: true, WorkspaceID: req.TargetWorkspaceID, RestoredAt: Now()}, nil
}

// DataExportReq 导出数据请求。
type DataExportReq struct {
	WorkspaceID string `json:"workspace_id"`
	Scope       string `json:"scope"`  // all | questions | learning_records | documents
	Format      string `json:"format"` // json | zip
}

// DataExport 导出工作区数据为 JSON（或 ZIP 打包）。
func (b *BackupService) DataExport(ctx context.Context, req DataExportReq) (*ExportResult, error) {
	if err := b.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	scope := req.Scope
	if scope == "" {
		scope = "all"
	}
	switch scope {
	case "all", "questions", "learning_records", "documents":
	default:
		return nil, domain.InvalidArg("scope 仅允许 all/questions/learning_records/documents")
	}
	format := req.Format
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "zip" {
		return nil, domain.InvalidArg("format 仅允许 json/zip")
	}

	data := map[string]any{
		"schema_version": "2.0.0",
		"workspace_id":   req.WorkspaceID,
		"exported_at":    Now(),
		"scope":          scope,
	}
	if scope == "all" || scope == "questions" {
		questions, err := b.exportQuestions(ctx, req.WorkspaceID)
		if err != nil {
			return nil, err
		}
		data["questions"] = questions
		know, err := b.exportKnowledge(ctx, req.WorkspaceID)
		if err != nil {
			return nil, err
		}
		data["knowledge_nodes"] = know
	}
	if scope == "all" || scope == "learning_records" {
		records, err := b.exportLearningRecords(ctx, req.WorkspaceID)
		if err != nil {
			return nil, err
		}
		data["learning_records"] = records
	}
	if scope == "all" || scope == "documents" {
		docs, err := b.exportDocuments(ctx, req.WorkspaceID)
		if err != nil {
			return nil, err
		}
		data["documents"] = docs
	}

	payload, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return nil, domain.WrapError(domain.CodeInternal, "导出序列化失败", err)
	}

	exportsDir := filepath.Join(b.s.Cfg.DataDir, "exports")
	if err := os.MkdirAll(exportsDir, 0o700); err != nil {
		return nil, err
	}
	rand, _ := crypto.RandomUint64()
	base := fmt.Sprintf("export-%s-%x", safeTimestamp(), rand)
	var fileName string
	var size int64
	if format == "zip" {
		fileName = base + ".zip"
		f, err := os.Create(filepath.Join(exportsDir, fileName))
		if err != nil {
			return nil, err
		}
		zw := zip.NewWriter(f)
		w, err := zw.Create("data.json")
		if err != nil {
			f.Close()
			return nil, err
		}
		if _, err := w.Write(payload); err != nil {
			f.Close()
			return nil, err
		}
		if err := zw.Close(); err != nil {
			f.Close()
			return nil, err
		}
		st, _ := f.Stat()
		size = st.Size()
		f.Close()
	} else {
		fileName = base + ".json"
		if err := os.WriteFile(filepath.Join(exportsDir, fileName), payload, 0o600); err != nil {
			return nil, err
		}
		size = int64(len(payload))
	}
	b.s.audit(ctx, req.WorkspaceID, "data.export", "workspace", req.WorkspaceID,
		map[string]any{"scope": scope, "format": format, "file": fileName})
	return &ExportResult{
		Path: filepath.Join("exports", fileName), FileName: fileName,
		Format: format, SizeBytes: size,
	}, nil
}

// dbErr 将 SQL 错误映射为领域错误（service 层不依赖 repository 内部实现）。
func dbErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "UNIQUE constraint failed"):
		return domain.Conflict("资源已存在（唯一约束冲突）")
	case strings.Contains(msg, "FOREIGN KEY constraint failed"):
		return domain.Conflict("关联资源不存在或状态不允许")
	case strings.Contains(msg, "CHECK constraint failed"):
		return domain.InvalidArg("数据不满足约束")
	case strings.Contains(msg, "database is locked"):
		return domain.WrapError(domain.CodeDatabaseUnavailable, "数据库繁忙，请稍后重试", err)
	default:
		return err
	}
}

// exportQuestions 导出题目及全部版本。
func (b *BackupService) exportQuestions(ctx context.Context, wsID string) ([]map[string]any, error) {
	rows, err := b.s.Repo.DB().QueryContext(ctx, `
		SELECT id, type, status, source, tags_json, content_hash, current_version_id, created_at, updated_at, version
		FROM questions WHERE workspace_id = ? AND deleted_at IS NULL ORDER BY created_at`, wsID)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, qtype, status, source, tags, hash string
		var cur *string
		var created, updated string
		var version int
		if err := rows.Scan(&id, &qtype, &status, &source, &tags, &hash, &cur, &created, &updated, &version); err != nil {
			return nil, dbErr(err)
		}
		versions, err := b.exportQuestionVersions(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"id": id, "type": qtype, "status": status, "source": source,
			"tags_json": tags, "content_hash": hash, "current_version_id": cur,
			"created_at": created, "updated_at": updated, "version": version,
			"versions": versions,
		})
	}
	return out, rows.Err()
}

func (b *BackupService) exportQuestionVersions(ctx context.Context, qid string) ([]map[string]any, error) {
	rows, err := b.s.Repo.DB().QueryContext(ctx, `
		SELECT id, version_no, payload_json, generated_by_model, prompt_version, review_status, created_at
		FROM question_versions WHERE question_id = ? ORDER BY version_no`, qid)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, payload, review string
		var vno int
		var model, pv *string
		var created string
		if err := rows.Scan(&id, &vno, &payload, &model, &pv, &review, &created); err != nil {
			return nil, dbErr(err)
		}
		out = append(out, map[string]any{
			"id": id, "version_no": vno, "payload_json": payload,
			"generated_by_model": model, "prompt_version": pv, "review_status": review, "created_at": created,
		})
	}
	return out, rows.Err()
}

func (b *BackupService) exportKnowledge(ctx context.Context, wsID string) ([]map[string]any, error) {
	return b.exportTable(ctx, wsID, "knowledge_nodes",
		`SELECT id, parent_id, name, node_path, level, created_at, updated_at, version
		 FROM knowledge_nodes WHERE workspace_id = ? AND deleted_at IS NULL ORDER BY node_path`)
}

func (b *BackupService) exportLearningRecords(ctx context.Context, wsID string) (map[string]any, error) {
	tables := []struct {
		name  string
		query string
	}{
		{"learning_goals", `SELECT id, user_id, name, subject, exam_at, target_score, daily_minutes,
			available_weekdays_json, knowledge_ids_json, status, created_at, updated_at, version
			FROM learning_goals WHERE workspace_id = ? AND deleted_at IS NULL`},
		{"plan_tasks", `SELECT id, user_id, goal_id, task_type, due_at, duration_min, priority, status,
			reason_codes_json, generated_reason, created_at, updated_at, version
			FROM plan_tasks WHERE workspace_id = ? AND deleted_at IS NULL`},
		{"practice_sessions", `SELECT id, user_id, mode, status, question_snapshot_json, time_limit_sec,
			started_at, submitted_at, created_at, updated_at, version
			FROM practice_sessions WHERE workspace_id = ?`},
		{"submissions", `SELECT id, session_id, question_version_id, attempt_no, answer_json, status,
			client_sequence, submitted_at, created_at, updated_at
			FROM submissions WHERE session_id IN (SELECT id FROM practice_sessions WHERE workspace_id = ?)`},
		{"grading_results", `SELECT id, submission_id, status, score, max_score, method, confidence,
			rule_version, matched_json, reason, needs_review, created_at
			FROM grading_results WHERE submission_id IN (
			  SELECT s.id FROM submissions s JOIN practice_sessions p ON s.session_id = p.id WHERE p.workspace_id = ?)`},
		{"wrong_answers", `SELECT id, user_id, submission_id, question_version_id, answer_json, cause, status,
			latest_grading_id, created_at, updated_at, version
			FROM wrong_answers WHERE workspace_id = ? AND deleted_at IS NULL`},
		{"review_cards", `SELECT id, user_id, wrong_answer_id, repetition, interval_days, ease_factor, due_at,
			status, created_at, updated_at, version
			FROM review_cards WHERE workspace_id = ?`},
		{"review_events", `SELECT id, review_card_id, rating, previous_json, current_json, created_at
			FROM review_events WHERE review_card_id IN (
			  SELECT id FROM review_cards WHERE workspace_id = ?)`},
	}
	out := map[string]any{}
	for _, t := range tables {
		rows, err := b.s.Repo.DB().QueryContext(ctx, t.query, wsID)
		if err != nil {
			return nil, dbErr(err)
		}
		items, err := rowsToMaps(rows)
		rows.Close()
		if err != nil {
			return nil, err
		}
		out[t.name] = items
	}
	return out, nil
}

func (b *BackupService) exportDocuments(ctx context.Context, wsID string) (map[string]any, error) {
	docs, err := b.exportTable(ctx, wsID, "documents",
		`SELECT id, relative_path, file_name, mime_type, byte_size, sha256, encrypted, status,
		 failure_reason, created_at, updated_at, version
		 FROM documents WHERE workspace_id = ? AND deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	chunks, err := b.exportTable(ctx, wsID, "document_chunks",
		`SELECT c.id, c.document_id, c.section_name, c.page_no, c.paragraph_no, c.start_offset, c.end_offset,
		 c.embedding_version, c.created_at
		 FROM document_chunks c JOIN documents d ON c.document_id = d.id
		 WHERE d.workspace_id = ? AND d.deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	return map[string]any{"documents": docs, "chunks": chunks}, nil
}

// exportTable 通用表导出：查询 → []map[string]any。
func (b *BackupService) exportTable(ctx context.Context, wsID, name, query string) ([]map[string]any, error) {
	rows, err := b.s.Repo.DB().QueryContext(ctx, query, wsID)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	return rowsToMaps(rows)
}

// rowsToMaps 将查询结果转为 map 列表。
func rowsToMaps(rows *sql.Rows) ([]map[string]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, dbErr(err)
	}
	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, dbErr(err)
		}
		m := map[string]any{}
		for i, c := range cols {
			switch v := vals[i].(type) {
			case []byte:
				m[c] = string(v)
			case nil:
				m[c] = nil
			default:
				m[c] = v
			}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// resolveLocalPath 解析用户提供的相对路径（以 baseDir 为根，禁止绝对路径与逃逸）。
func resolveLocalPath(p, baseDir string) (string, error) {
	if filepath.IsAbs(p) {
		return "", domain.InvalidArg("仅接受相对路径，不允许绝对路径")
	}
	clean := filepath.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", domain.InvalidArg("路径不允许包含 ..")
	}
	return filepath.Join(baseDir, clean), nil
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
