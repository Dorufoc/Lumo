// request_service.go 求题闭环（完整设计文档 4.20 P2 求题请求 / Todo 36）。
// 状态机严格对齐 content_requests.status[open|fulfilled|closed]：
//
//	open      = 唯一中间态（提交/生成/审核均在此推进，经 QuizGen 与 review_queue_items）
//	fulfilled = 审核通过且题目经草稿管线入库（status='draft'、source=ai_assisted）
//	closed    = 审核拒绝或用户取消（终态）
//
// 终态不可迁移；非法迁移 → INVALID_STATE（domain.ContentRequestCanTransition 校验 + 原子守卫）。
//
// 生成草稿存储决策（本模块自选，TDD 说明）：review_queue_items 表无载荷列，
// 生成的题目草稿（已校验的 QuestionPayload 列表）持久化为 <DataDir>/requests/<request_id>.json，
// 审核通过时读文件经 questions 草稿管线入库；审核拒绝/取消时题目不入库。
// 文件仅作草稿缓存，不入题题库；与数据库同根（DataDir），随备份目录一并管理。
package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"lumo/internal/agent"
	"lumo/internal/domain"
	"lumo/internal/repository"
)

// requestReviewRefType 求题请求在 review_queue_items 中的 ref_type。
const requestReviewRefType = "content_request"

// RequestService 求题请求用例（ContentRequest*）。
type RequestService struct {
	s *Services
}

// ContentRequest 求题请求 DTO。
type ContentRequest struct {
	ID            string   `json:"id"`
	WorkspaceID   string   `json:"workspace_id"`
	UserID        string   `json:"user_id"`
	KnowledgeIDs  []string `json:"knowledge_ids"`
	Description   string   `json:"description"`
	Status        string   `json:"status"`
	ReviewStatus  string   `json:"review_status,omitempty"` // pending/approved/rejected；未生成时为空
	ReviewReason  string   `json:"review_reason,omitempty"`
	QuestionCount int      `json:"question_count"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
}

// ContentRequestCreateReq 提交求题请求。
type ContentRequestCreateReq struct {
	WorkspaceID    string   `json:"workspace_id"`
	UserID         string   `json:"user_id"`
	KnowledgeIDs   []string `json:"knowledge_ids"`
	Description    string   `json:"description"`
	IdempotencyKey string   `json:"idempotency_key"`
}

// ContentRequestGenerateReq 生成题目草稿请求。
type ContentRequestGenerateReq struct {
	WorkspaceID    string `json:"workspace_id"`
	UserID         string `json:"user_id"`
	RequestID      string `json:"request_id"`
	Count          int    `json:"count"` // 题量 1-10
	IdempotencyKey string `json:"idempotency_key"`
}

// ContentRequestReviewReq 审核决策请求（approved/rejected）。
type ContentRequestReviewReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	RequestID   string `json:"request_id"`
	Decision    string `json:"decision"`
	Reason      string `json:"reason"`
}

// ContentRequestCancelReq 取消请求请求（open → closed）。
type ContentRequestCancelReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	RequestID   string `json:"request_id"`
}

// ContentRequestListReq 我的请求列表请求。
type ContentRequestListReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"` // 空 = 全部
}

// ContentRequestCreate 提交求题请求（status=open；幂等 + 审计）。
func (r *RequestService) ContentRequestCreate(ctx context.Context, req ContentRequestCreateReq) (*ContentRequest, error) {
	if err := r.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	desc := strings.TrimSpace(req.Description)
	if desc == "" {
		return nil, domain.InvalidArg("description 必填")
	}
	if len([]rune(desc)) > 2000 {
		return nil, domain.InvalidArg("description 过长（上限 2000 字符）")
	}
	seen := map[string]bool{}
	for _, kid := range req.KnowledgeIDs {
		if kid == "" || seen[kid] {
			continue
		}
		seen[kid] = true
		n, err := r.s.Repo.GetKnowledgeNode(ctx, req.WorkspaceID, kid)
		if err != nil {
			return nil, err
		}
		if n == nil {
			return nil, domain.InvalidArg("知识点 %s 不存在", kid)
		}
	}
	var ids []string
	for kid := range seen {
		ids = append(ids, kid)
	}
	return withIdempotency(r.s, ctx, req.WorkspaceID, req.IdempotencyKey, "ContentRequestCreate",
		func() (*ContentRequest, error) {
			row := &repository.ContentRequestRow{
				ID: NewID(), WorkspaceID: req.WorkspaceID, UserID: req.UserID,
				KnowledgeIDs: string(mustJSON(ids)), Description: desc, Status: domain.ContentRequestOpen,
			}
			if err := r.s.Repo.CreateContentRequest(ctx, row); err != nil {
				return nil, err
			}
			r.s.audit(ctx, req.WorkspaceID, "content.request.create", "content_request", row.ID,
				map[string]any{"knowledge_ids": ids})
			created, err := r.s.Repo.GetContentRequest(ctx, req.WorkspaceID, row.ID)
			if err != nil {
				return nil, err
			}
			return r.toDTO(ctx, req.WorkspaceID, created)
		})
}

// ContentRequestGenerate 生成题目草稿（open 停留；幂等 + 审计；未配置 Provider 降级模板）。
func (r *RequestService) ContentRequestGenerate(ctx context.Context, req ContentRequestGenerateReq) (*ContentRequest, error) {
	if err := r.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if req.RequestID == "" {
		return nil, domain.InvalidArg("request_id 必填")
	}
	if req.Count <= 0 || req.Count > quizGenMaxCount {
		return nil, domain.InvalidArg("题量（count）须为 1-%d", quizGenMaxCount)
	}
	return withIdempotency(r.s, ctx, req.WorkspaceID, req.IdempotencyKey, "ContentRequestGenerate",
		func() (*ContentRequest, error) { return r.doGenerate(ctx, req) })
}

// doGenerate 执行草稿生成：校验状态/权限 → QuizGen（或降级）→ 校验载荷 → 写草稿文件 → 创建 pending 审核项。
func (r *RequestService) doGenerate(ctx context.Context, req ContentRequestGenerateReq) (*ContentRequest, error) {
	row, err := r.s.Repo.GetContentRequest(ctx, req.WorkspaceID, req.RequestID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, domain.NotFound("求题请求不存在")
	}
	if row.UserID != req.UserID {
		return nil, domain.Forbidden("仅请求创建者可生成题目草稿")
	}
	if row.Status != domain.ContentRequestOpen {
		return nil, domain.InvalidState("仅 open 状态的求题请求可生成题目草稿")
	}
	if it, err := r.s.Repo.GetPendingReviewItemByRef(ctx, requestReviewRefType, row.ID); err != nil {
		return nil, err
	} else if it != nil {
		return nil, domain.Conflict("该请求已有待审核的题目草稿，请先完成审核")
	}

	reqKIDs := row.UnmarshalKnowledgeIDs()
	knowledgeNames := ""
	if len(reqKIDs) > 0 {
		var names []string
		for _, kid := range reqKIDs {
			n, err := r.s.Repo.GetKnowledgeNode(ctx, req.WorkspaceID, kid)
			if err != nil {
				return nil, err
			}
			if n != nil {
				names = append(names, n.Name)
			}
		}
		knowledgeNames = strings.Join(names, "、")
	}

	var drafts []agent.QuizDraft
	degraded := false
	model := ""
	llm, llmErr := r.s.Agent.LLMFactoryFunc()
	if llmErr != nil {
		// 降级：确定性模板草稿（不阻塞求题闭环）
		degraded = true
		drafts = r.templateDrafts(knowledgeNames, row.Description, req.Count)
	} else {
		system, prompt := agent.BuildQuizGenPrompt(agent.QuizGenInput{
			Types:     []string{"single_choice", "multiple_choice"},
			Count:     req.Count,
			Knowledge: knowledgeNames,
			Material:  "求题方向：" + row.Description,
		})
		var out agent.QuizGenOutput
		m, err := agent.ChatJSON(ctx, llm, system, prompt, &out)
		if err != nil {
			r.s.audit(ctx, req.WorkspaceID, "content.request.generate", "content_request", row.ID,
				map[string]any{"status": "failed", "reason": err.Error()})
			return nil, providerError(err)
		}
		model = m
		if len(out.Questions) == 0 {
			err := domain.WrapError(domain.CodeOutputInvalid, "AI 未生成任何题目草稿", nil)
			r.s.audit(ctx, req.WorkspaceID, "content.request.generate", "content_request", row.ID,
				map[string]any{"status": "failed", "reason": err.Error()})
			return nil, err
		}
		drafts = out.Questions
		if len(drafts) > req.Count {
			drafts = drafts[:req.Count]
		}
	}

	// 载荷化并校验（与题库草稿管线同一契约）；无效草稿 → OUTPUT_INVALID
	var payloads []*QuestionPayload
	for i, d := range drafts {
		payload := &QuestionPayload{
			Type:       d.Type,
			Stem:       d.Stem,
			Options:    draftOptionsToService(d.Options),
			Answer:     d.Answer,
			Analysis:   d.Analysis,
			Difficulty: d.Difficulty,
		}
		parsed, err := parseQuestionPayload(mustJSON(payload))
		if err != nil {
			r.s.audit(ctx, req.WorkspaceID, "content.request.generate", "content_request", row.ID,
				map[string]any{"status": "failed", "reason": err.Error(), "index": i})
			return nil, domain.WrapError(domain.CodeOutputInvalid, "AI 生成的题目草稿非法: %v", err)
		}
		parsed.Source = "ai_assisted"
		parsed.KnowledgeIDs = r.mergeRequestKnowledgeIDs(ctx, req.WorkspaceID, reqKIDs, d.KnowledgeIDs)
		if err := r.s.Knowledge.validatePayload(ctx, req.WorkspaceID, parsed); err != nil {
			r.s.audit(ctx, req.WorkspaceID, "content.request.generate", "content_request", row.ID,
				map[string]any{"status": "failed", "reason": err.Error(), "index": i})
			return nil, err
		}
		payloads = append(payloads, parsed)
	}

	// 草稿文件落盘（仅缓存，不入题库）
	if err := r.writeDrafts(row.ID, payloads, model); err != nil {
		r.s.audit(ctx, req.WorkspaceID, "content.request.generate", "content_request", row.ID,
			map[string]any{"status": "failed", "reason": "write drafts: " + err.Error()})
		return nil, domain.WrapError(domain.CodeInternal, "草稿保存失败", err)
	}

	// 创建 pending 审核项
	if _, err := r.s.Repo.CreateReviewQueueItem(ctx, r.s.Repo.DB(), requestReviewRefType, row.ID); err != nil {
		return nil, err
	}
	r.s.audit(ctx, req.WorkspaceID, "content.request.generate", "content_request", row.ID,
		map[string]any{"status": "ready", "degraded": degraded, "count": len(payloads), "model": model})
	cur, err := r.s.Repo.GetContentRequest(ctx, req.WorkspaceID, row.ID)
	if err != nil {
		return nil, err
	}
	return r.toDTO(ctx, req.WorkspaceID, cur)
}

// ContentRequestReview 审核决策：approved → fulfilled + 题目入库；rejected → closed（题目不入库）。
func (r *RequestService) ContentRequestReview(ctx context.Context, req ContentRequestReviewReq) (*ContentRequest, error) {
	if err := r.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if req.RequestID == "" {
		return nil, domain.InvalidArg("request_id 必填")
	}
	if req.Decision != "approved" && req.Decision != "rejected" {
		return nil, domain.InvalidArg("decision 仅允许 approved/rejected")
	}
	return r.doReview(ctx, req)
}

// doReview 执行审核（不在幂等包裹内：审核不允许重放）。
func (r *RequestService) doReview(ctx context.Context, req ContentRequestReviewReq) (*ContentRequest, error) {
	row, err := r.s.Repo.GetContentRequest(ctx, req.WorkspaceID, req.RequestID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, domain.NotFound("求题请求不存在")
	}
	if row.UserID != req.UserID {
		return nil, domain.Forbidden("仅请求创建者可审核")
	}
	if row.Status != domain.ContentRequestOpen {
		return nil, domain.InvalidState("仅 open 状态的求题请求可审核")
	}
	item, err := r.s.Repo.GetPendingReviewItemByRef(ctx, requestReviewRefType, row.ID)
	if err != nil {
		return nil, err
	}
	if item == nil {
		return nil, domain.InvalidState("该请求尚未生成题目草稿，无法审核")
	}

	if req.Decision == "approved" {
		if err := r.approve(ctx, req.WorkspaceID, row, item, req.Reason); err != nil {
			return nil, err
		}
	} else {
		if err := r.reject(ctx, req.WorkspaceID, row, item, req.Reason); err != nil {
			return nil, err
		}
	}
	cur, err := r.s.Repo.GetContentRequest(ctx, req.WorkspaceID, row.ID)
	if err != nil {
		return nil, err
	}
	return r.toDTO(ctx, req.WorkspaceID, cur)
}

// approve 审核通过：安全扫描 → 草稿管线入库 → 审核项 approved → 状态 fulfilled。
func (r *RequestService) approve(ctx context.Context, wsID string, row *repository.ContentRequestRow, item *repository.ReviewQueueItemRow, reason string) error {
	payloads, model, err := r.readDrafts(row.ID)
	if err != nil {
		return domain.WrapError(domain.CodeInternal, "草稿读取失败", err)
	}
	if len(payloads) == 0 {
		return domain.InvalidState("该请求没有可入库的题目草稿")
	}

	// 强制安全扫描（Todo 29 scanContent）：任一草稿命中风险 → 拒绝入库，请求保持 open、审核项保持 pending
	for i, pl := range payloads {
		res := scanContent(mustJSON(pl))
		if !res.Clean {
			r.s.audit(ctx, wsID, "content.request.review", "content_request", row.ID,
				map[string]any{"status": "blocked", "index": i, "findings": res.Findings})
			return domain.InvalidArg("安全扫描未通过（%s），请编辑草稿后重新生成", strings.Join(res.Findings, ", "))
		}
	}

	// 去重（content_hash 冲突跳过，不重复插入）
	var toCreate []*QuestionPayload
	for _, pl := range payloads {
		hash := questionContentHash(pl)
		existing, err := r.s.Repo.GetQuestionByContentHash(ctx, wsID, hash)
		if err != nil {
			return err
		}
		if existing == nil {
			toCreate = append(toCreate, pl)
		}
	}

	pv := quizGenPromptVersion
	err = r.s.Repo.WithTx(ctx, func(tx *sql.Tx) error {
		for _, pl := range toCreate {
			id := NewID()
			versionID := NewID()
			if err := r.s.Repo.CreateQuestion(ctx, &repository.QuestionRow{
				ID: id, WorkspaceID: wsID, Type: pl.Type,
				Source: pl.Source, Tags: mustJSON(pl.Tags), ContentHash: questionContentHash(pl),
			}); err != nil {
				return err
			}
			if err := r.s.Repo.CreateQuestionVersion(ctx, &repository.QuestionVersionRow{
				ID: versionID, QuestionID: id, VersionNo: 1,
				Payload: mustJSON(pl), GeneratedBy: stringPtr(model), PromptVer: &pv, Review: "pending",
			}); err != nil {
				return err
			}
			if len(pl.KnowledgeIDs) > 0 {
				if err := r.s.Repo.SetQuestionKnowledge(ctx, versionID, pl.KnowledgeIDs); err != nil {
					return err
				}
			}
		}
		if _, err := r.s.Repo.DecideReviewQueueItem(ctx, tx, item.ID, "approved", reason); err != nil {
			return err
		}
		if _, err := r.s.Repo.UpdateContentRequestStatus(ctx, tx, row.ID,
			domain.ContentRequestOpen, domain.ContentRequestFulfilled); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	r.s.audit(ctx, wsID, "content.request.review", "content_request", row.ID,
		map[string]any{"decision": "approved", "created": len(toCreate)})
	return nil
}

// reject 审核拒绝：审核项 rejected + 状态 closed（题目不入库）。
func (r *RequestService) reject(ctx context.Context, wsID string, row *repository.ContentRequestRow, item *repository.ReviewQueueItemRow, reason string) error {
	err := r.s.Repo.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := r.s.Repo.DecideReviewQueueItem(ctx, tx, item.ID, "rejected", reason); err != nil {
			return err
		}
		if _, err := r.s.Repo.UpdateContentRequestStatus(ctx, tx, row.ID,
			domain.ContentRequestOpen, domain.ContentRequestClosed); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	r.s.audit(ctx, wsID, "content.request.review", "content_request", row.ID,
		map[string]any{"decision": "rejected"})
	return nil
}

// ContentRequestCancel 取消请求：open → closed；已有 pending 审核项一并拒绝（题目不入库）。
func (r *RequestService) ContentRequestCancel(ctx context.Context, req ContentRequestCancelReq) (*ContentRequest, error) {
	if err := r.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if req.RequestID == "" {
		return nil, domain.InvalidArg("request_id 必填")
	}
	row, err := r.s.Repo.GetContentRequest(ctx, req.WorkspaceID, req.RequestID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, domain.NotFound("求题请求不存在")
	}
	if row.UserID != req.UserID {
		return nil, domain.Forbidden("仅请求创建者可取消")
	}
	if row.Status != domain.ContentRequestOpen {
		return nil, domain.InvalidState("仅 open 状态的求题请求可取消")
	}

	items, err := r.s.Repo.ListReviewQueueItemsByRef(ctx, requestReviewRefType, row.ID)
	if err != nil {
		return nil, err
	}
	err = r.s.Repo.WithTx(ctx, func(tx *sql.Tx) error {
		for _, it := range items {
			if it.Status == "pending" {
				if _, err := r.s.Repo.DecideReviewQueueItem(ctx, tx, it.ID, "rejected", "用户取消请求"); err != nil {
					return err
				}
			}
		}
		if _, err := r.s.Repo.UpdateContentRequestStatus(ctx, tx, row.ID,
			domain.ContentRequestOpen, domain.ContentRequestClosed); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	r.s.audit(ctx, req.WorkspaceID, "content.request.cancel", "content_request", row.ID,
		map[string]any{"status": "closed"})
	cur, err := r.s.Repo.GetContentRequest(ctx, req.WorkspaceID, row.ID)
	if err != nil {
		return nil, err
	}
	return r.toDTO(ctx, req.WorkspaceID, cur)
}

// ContentRequestList 我的请求列表（user_id 空 = 全部；含审核状态与草稿题量）。
func (r *RequestService) ContentRequestList(ctx context.Context, req ContentRequestListReq) ([]*ContentRequest, error) {
	if err := r.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	rows, err := r.s.Repo.ListContentRequests(ctx, req.WorkspaceID, req.UserID)
	if err != nil {
		return nil, err
	}
	out := make([]*ContentRequest, 0, len(rows))
	for _, row := range rows {
		dto, err := r.toDTO(ctx, req.WorkspaceID, row)
		if err != nil {
			return nil, err
		}
		out = append(out, dto)
	}
	return out, nil
}

// toDTO 组装 DTO（含最新审核状态与草稿题量）。
func (r *RequestService) toDTO(ctx context.Context, wsID string, row *repository.ContentRequestRow) (*ContentRequest, error) {
	dto := &ContentRequest{
		ID:           row.ID,
		WorkspaceID:  row.WorkspaceID,
		UserID:       row.UserID,
		KnowledgeIDs: row.UnmarshalKnowledgeIDs(),
		Description:  row.Description,
		Status:       row.Status,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
	items, err := r.s.Repo.ListReviewQueueItemsByRef(ctx, requestReviewRefType, row.ID)
	if err != nil {
		return nil, err
	}
	if len(items) > 0 {
		dto.ReviewStatus = items[0].Status
		dto.ReviewReason = items[0].Reason
	}
	if n := r.draftCount(row.ID); n > 0 {
		dto.QuestionCount = n
	}
	return dto, nil
}

// draftFile 返回草稿文件路径。
func (r *RequestService) draftFile(reqID string) string {
	return filepath.Join(r.s.Cfg.DataDir, "requests", reqID+".json")
}

// writeDrafts 写入草稿文件（含模型标识）。
func (r *RequestService) writeDrafts(reqID string, payloads []*QuestionPayload, model string) error {
	dir := filepath.Dir(r.draftFile(reqID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(map[string]any{
		"questions": payloads,
		"model":     model,
	})
	if err != nil {
		return err
	}
	return os.WriteFile(r.draftFile(reqID), b, 0o600)
}

// readDrafts 读取草稿文件。
func (r *RequestService) readDrafts(reqID string) ([]*QuestionPayload, string, error) {
	b, err := os.ReadFile(r.draftFile(reqID))
	if err != nil {
		return nil, "", err
	}
	var file struct {
		Questions []*QuestionPayload `json:"questions"`
		Model     string             `json:"model"`
	}
	if err := json.Unmarshal(b, &file); err != nil {
		return nil, "", err
	}
	return file.Questions, file.Model, nil
}

// draftCount 返回草稿题量（文件不存在返回 0）。
func (r *RequestService) draftCount(reqID string) int {
	payloads, _, err := r.readDrafts(reqID)
	if err != nil {
		return 0
	}
	return len(payloads)
}

// templateDrafts 未配置 Provider 时的确定性降级草稿（单选，题干区分序号避免哈希冲突）。
func (r *RequestService) templateDrafts(knowledgeNames, description string, count int) []agent.QuizDraft {
	topic := knowledgeNames
	if topic == "" {
		topic = strings.TrimSpace(description)
		if topic == "" {
			topic = "通用"
		}
	}
	out := make([]agent.QuizDraft, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, agent.QuizDraft{
			Type:       "single_choice",
			Stem:       fmt.Sprintf("关于「%s」的第 %d 题：请选出正确的表述。", topic, i+1),
			Options:    []agent.DraftOption{{Key: "A", Text: "正确表述"}, {Key: "B", Text: "错误表述"}},
			Answer:     json.RawMessage(`"A"`),
			Analysis:   "本题由求题闭环降级模板生成（AI 模型未配置），请审核后入库。",
			Difficulty: 3,
		})
	}
	return out
}

// mergeRequestKnowledgeIDs 合并请求知识点与 LLM 建议知识点（仅并入工作区内已存在节点）。
func (r *RequestService) mergeRequestKnowledgeIDs(ctx context.Context, wsID string, base, llm []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	for _, id := range base {
		add(id)
	}
	for _, id := range llm {
		n, err := r.s.Repo.GetKnowledgeNode(ctx, wsID, id)
		if err != nil || n == nil {
			continue
		}
		add(id)
	}
	return out
}

// stringPtr 返回字符串指针。
func stringPtr(s string) *string { return &s }
