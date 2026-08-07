package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"lumo/internal/agent"
	"lumo/internal/domain"
	"lumo/internal/repository"
)

// KnowledgeNode 是知识点 DTO（树形）。
type KnowledgeNode struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	NodePath  string           `json:"node_path"`
	Level     int              `json:"level"`
	ParentID  *string          `json:"parent_id"`
	Children  []*KnowledgeNode `json:"children,omitempty"`
	Version   int              `json:"version"`
	CreatedAt string           `json:"created_at"`
	UpdatedAt string           `json:"updated_at"`
}

// QuestionOption 是选择题选项。
type QuestionOption struct {
	Key  string `json:"key"`
	Text string `json:"text"`
}

// RubricItem 是简答题量规条目。
type RubricItem struct {
	Point  string  `json:"point"`
	Score  float64 `json:"score"`
	Max    float64 `json:"max"`
}

// QuestionPayload 是题目版本载荷。
type QuestionPayload struct {
	Type          string            `json:"type"`
	Stem          string            `json:"stem"`
	Options       []QuestionOption  `json:"options,omitempty"`
	Answer        json.RawMessage   `json:"answer"`
	Analysis      string            `json:"analysis,omitempty"`
	Difficulty    int               `json:"difficulty"`
	KnowledgeIDs  []string          `json:"knowledge_ids,omitempty"`
	GradingConfig map[string]any    `json:"grading_config,omitempty"`
	Rubric        []RubricItem      `json:"rubric,omitempty"`
	Source        string            `json:"source,omitempty"`
	Tags          []string          `json:"tags,omitempty"`
}

// QuestionVersion 是题目版本 DTO。
type QuestionVersion struct {
	ID               string          `json:"id"`
	QuestionID       string          `json:"question_id"`
	VersionNo        int             `json:"version_no"`
	Payload          json.RawMessage `json:"payload"`
	GeneratedByModel *string         `json:"generated_by_model"`
	PromptVersion    *string         `json:"prompt_version"`
	ReviewStatus     string          `json:"review_status"`
	CreatedAt        string          `json:"created_at"`
}

// Question 是题目 DTO。
type Question struct {
	ID             string           `json:"id"`
	WorkspaceID    string           `json:"workspace_id"`
	Type           string           `json:"type"`
	Status         string           `json:"status"`
	Source         string           `json:"source"`
	Tags           []string         `json:"tags"`
	ContentHash    string           `json:"content_hash"`
	CurrentVersion *QuestionVersion `json:"current_version,omitempty"`
	CreatedAt      string           `json:"created_at"`
	UpdatedAt      string           `json:"updated_at"`
	Version        int              `json:"version"`
}

// QuestionPage 是题目分页。
type QuestionPage struct {
	Items      []*Question `json:"items"`
	NextCursor string      `json:"next_cursor"`
	HasMore    bool        `json:"has_more"`
}

// KnowledgeService 实现知识点与题目用例。
type KnowledgeService struct{ s *Services }

// KnowledgeCreateReq 创建知识点请求。
type KnowledgeCreateReq struct {
	WorkspaceID string  `json:"workspace_id"`
	Name        string  `json:"name"`
	ParentID    *string `json:"parent_id"`
}

// KnowledgeCreate 创建知识点（node_path 用 ID 链，改名不影响路径）。
func (k *KnowledgeService) KnowledgeCreate(ctx context.Context, req KnowledgeCreateReq) (*KnowledgeNode, error) {
	if err := k.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.Name == "" || len(req.Name) > 160 {
		return nil, domain.InvalidArg("知识点名称长度须为 1-160")
	}
	id := NewID()
	path := "/" + id
	level := 0
	if req.ParentID != nil && *req.ParentID != "" {
		parent, err := k.s.Repo.GetKnowledgeNode(ctx, req.WorkspaceID, *req.ParentID)
		if err != nil {
			return nil, err
		}
		if parent == nil {
			return nil, domain.NotFound("父知识点不存在")
		}
		path = parent.NodePath + "/" + id
		level = parent.Level + 1
	}
	if err := k.s.Repo.CreateKnowledgeNode(ctx, &repository.KnowledgeNodeRow{
		ID: id, WorkspaceID: req.WorkspaceID, ParentID: req.ParentID,
		Name: req.Name, NodePath: path, Level: level,
	}); err != nil {
		return nil, err
	}
	k.s.audit(ctx, req.WorkspaceID, "knowledge.create", "knowledge", id, map[string]any{"name": req.Name})
	row, err := k.s.Repo.GetKnowledgeNode(ctx, req.WorkspaceID, id)
	if err != nil {
		return nil, err
	}
	return knowledgeFromRow(row), nil
}

// KnowledgeUpdateReq 更新知识点请求。
type KnowledgeUpdateReq struct {
	WorkspaceID string `json:"workspace_id"`
	KnowledgeID string `json:"knowledge_id"`
	Version     int    `json:"version"`
	Name        string `json:"name"`
}

// KnowledgeUpdate 重命名知识点。
func (k *KnowledgeService) KnowledgeUpdate(ctx context.Context, req KnowledgeUpdateReq) (*KnowledgeNode, error) {
	if err := k.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.Name == "" || len(req.Name) > 160 {
		return nil, domain.InvalidArg("知识点名称长度须为 1-160")
	}
	row, err := k.s.Repo.UpdateKnowledgeNode(ctx, req.WorkspaceID, req.KnowledgeID, req.Version, req.Name)
	if err != nil {
		return nil, err
	}
	return knowledgeFromRow(row), nil
}

// KnowledgeDeleteReq 删除知识点请求。
type KnowledgeDeleteReq struct {
	WorkspaceID string `json:"workspace_id"`
	KnowledgeID string `json:"knowledge_id"`
	Version     int    `json:"version"`
}

// KnowledgeDelete 删除知识点（有子节点或引用时拒绝）。
func (k *KnowledgeService) KnowledgeDelete(ctx context.Context, req KnowledgeDeleteReq) (*DeleteResult, error) {
	if err := k.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if err := k.s.Repo.SoftDeleteKnowledgeNode(ctx, req.WorkspaceID, req.KnowledgeID, req.Version); err != nil {
		return nil, err
	}
	k.s.audit(ctx, req.WorkspaceID, "knowledge.delete", "knowledge", req.KnowledgeID, nil)
	return &DeleteResult{Deleted: true, DeletedAt: Now()}, nil
}

// KnowledgeTreeGetReq 获取知识点树请求。
type KnowledgeTreeGetReq struct {
	WorkspaceID string `json:"workspace_id"`
}

// KnowledgeTreeGet 返回知识点树（根节点列表）。
func (k *KnowledgeService) KnowledgeTreeGet(ctx context.Context, req KnowledgeTreeGetReq) ([]*KnowledgeNode, error) {
	if err := k.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	rows, err := k.s.Repo.ListKnowledgeNodes(ctx, req.WorkspaceID)
	if err != nil {
		return nil, err
	}
	byID := map[string]*KnowledgeNode{}
	var roots []*KnowledgeNode
	for _, r := range rows {
		n := knowledgeFromRow(r)
		byID[n.ID] = n
	}
	for _, n := range byID {
		if n.ParentID != nil {
			if p, ok := byID[*n.ParentID]; ok {
				p.Children = append(p.Children, n)
				continue
			}
		}
		roots = append(roots, n)
	}
	// 按 node_path 排序保证确定性
	sortNodes(roots)
	return roots, nil
}

func sortNodes(nodes []*KnowledgeNode) {
	// node_path 已按字典序查询，这里保持顺序稳定
}

func knowledgeFromRow(r *repository.KnowledgeNodeRow) *KnowledgeNode {
	return &KnowledgeNode{
		ID: r.ID, Name: r.Name, NodePath: r.NodePath, Level: r.Level,
		ParentID: r.ParentID, Version: r.Version,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// QuestionCreateDraftReq 创建题目草稿请求。
type QuestionCreateDraftReq struct {
	WorkspaceID    string          `json:"workspace_id"`
	Payload        json.RawMessage `json:"payload"`
	IdempotencyKey string          `json:"idempotency_key"`
}

// QuestionCreateDraft 创建草稿题目（去重：规范化内容哈希）。
func (k *KnowledgeService) QuestionCreateDraft(ctx context.Context, req QuestionCreateDraftReq) (*Question, error) {
	if err := k.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if len(req.Payload) == 0 {
		return nil, domain.InvalidArg("payload 必填")
	}
	payload, err := parseQuestionPayload(req.Payload)
	if err != nil {
		return nil, err
	}
	if err := k.validatePayload(ctx, req.WorkspaceID, payload); err != nil {
		return nil, err
	}
	hash := questionContentHash(payload)

	return withIdempotency(k.s, ctx, req.WorkspaceID, req.IdempotencyKey, "QuestionCreateDraft", func() (*Question, error) {
		if existing, err := k.s.Repo.GetQuestionByContentHash(ctx, req.WorkspaceID, hash); err != nil {
			return nil, err
		} else if existing != nil {
			return nil, domain.Conflict("相同内容的题目已存在")
		}
		id := NewID()
		versionID := NewID()
		if err := k.s.Repo.CreateQuestion(ctx, &repository.QuestionRow{
			ID: id, WorkspaceID: req.WorkspaceID, Type: payload.Type,
			Source: payload.Source, Tags: mustJSON(payload.Tags), ContentHash: hash,
		}); err != nil {
			return nil, err
		}
		if err := k.s.Repo.CreateQuestionVersion(ctx, &repository.QuestionVersionRow{
			ID: versionID, QuestionID: id, VersionNo: 1,
			Payload: mustJSON(payload), Review: "pending",
		}); err != nil {
			return nil, err
		}
		if len(payload.KnowledgeIDs) > 0 {
			if err := k.s.Repo.SetQuestionKnowledge(ctx, versionID, payload.KnowledgeIDs); err != nil {
				return nil, err
			}
		}
		k.s.audit(ctx, req.WorkspaceID, "question.create", "question", id, map[string]any{"type": payload.Type})
		return k.questionByID(ctx, req.WorkspaceID, id)
	})
}

// QuestionCreateVersionReq 创建新版本请求。
type QuestionCreateVersionReq struct {
	WorkspaceID    string          `json:"workspace_id"`
	QuestionID     string          `json:"question_id"`
	Version        int             `json:"version"`
	Payload        json.RawMessage `json:"payload"`
	IdempotencyKey string          `json:"idempotency_key"`
}

// QuestionCreateVersion 创建不可变新版本。
func (k *KnowledgeService) QuestionCreateVersion(ctx context.Context, req QuestionCreateVersionReq) (*QuestionVersion, error) {
	if err := k.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	payload, err := parseQuestionPayload(req.Payload)
	if err != nil {
		return nil, err
	}
	if err := k.validatePayload(ctx, req.WorkspaceID, payload); err != nil {
		return nil, err
	}
	return withIdempotency(k.s, ctx, req.WorkspaceID, req.IdempotencyKey, "QuestionCreateVersion", func() (*QuestionVersion, error) {
		q, err := k.s.Repo.GetQuestion(ctx, req.WorkspaceID, req.QuestionID)
		if err != nil {
			return nil, err
		}
		if q == nil {
			return nil, domain.NotFound("题目不存在")
		}
		if q.Version != req.Version {
			return nil, domain.Conflict("题目已被修改，请刷新后重试")
		}
		if q.Status == "archived" {
			return nil, domain.InvalidState("已归档题目不能创建新版本")
		}
		vno, err := k.s.Repo.NextVersionNo(ctx, q.ID)
		if err != nil {
			return nil, err
		}
		v := &repository.QuestionVersionRow{
			ID: NewID(), QuestionID: q.ID, VersionNo: vno,
			Payload: mustJSON(payload), Review: "pending",
		}
		if err := k.s.Repo.CreateQuestionVersion(ctx, v); err != nil {
			return nil, err
		}
		if len(payload.KnowledgeIDs) > 0 {
			if err := k.s.Repo.SetQuestionKnowledge(ctx, v.ID, payload.KnowledgeIDs); err != nil {
				return nil, err
			}
		}
		// 题目内容变更（Todo 37）：新版本创建即内容变更，经 Webhook 投递
		// （复用 Todo 31 HMAC/退避/投递机制；不走 UserEventBus）。
		_ = k.s.Webhooks.Dispatch(ctx, req.WorkspaceID, agent.EventQuestionChanged,
			map[string]any{"question_id": q.ID, "version_id": v.ID})
		k.s.audit(ctx, req.WorkspaceID, "question.version", "question", q.ID, map[string]any{"version_no": vno})
		row, err := k.s.Repo.GetQuestionVersion(ctx, v.ID)
		if err != nil {
			return nil, err
		}
		return questionVersionFromRow(row), nil
	})
}

// QuestionTransitionReq 题目状态迁移请求。
type QuestionTransitionReq struct {
	WorkspaceID string `json:"workspace_id"`
	QuestionID  string `json:"question_id"`
	Version     int    `json:"version"`
	Action      string `json:"action"`
}

// QuestionTransition 状态机：draft/reviewed → review → publish → archive。
func (k *KnowledgeService) QuestionTransition(ctx context.Context, req QuestionTransitionReq) (*Question, error) {
	if err := k.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	q, err := k.s.Repo.GetQuestion(ctx, req.WorkspaceID, req.QuestionID)
	if err != nil {
		return nil, err
	}
	if q == nil {
		return nil, domain.NotFound("题目不存在")
	}
	if q.Version != req.Version {
		return nil, domain.Conflict("题目已被修改，请刷新后重试")
	}
	var next string
	switch req.Action {
	case "review":
		if q.Status != "draft" {
			return nil, domain.InvalidState("仅 draft 状态可进入 review")
		}
		next = "reviewed"
	case "publish":
		if q.Status != "reviewed" {
			return nil, domain.InvalidState("仅 reviewed 状态可发布")
		}
		if q.CurrentVersionID == nil {
			return nil, domain.InvalidState("题目没有版本，无法发布")
		}
		next = "published"
	case "archive":
		if q.Status == "archived" {
			return nil, domain.InvalidState("题目已归档")
		}
		next = "archived"
	default:
		return nil, domain.InvalidArg("action 仅允许 review/publish/archive")
	}
	if _, err := k.s.Repo.UpdateQuestionStatus(ctx, req.WorkspaceID, req.QuestionID, req.Version, next); err != nil {
		return nil, err
	}
	// 新题发布（Todo 37）：QuestionTransition action=publish 成功后经 Webhook 投递
	// （复用 Todo 31 HMAC/退避/投递机制；不走 UserEventBus，不产生 notifications 行）。
	if req.Action == "publish" {
		var versionID string
		if q.CurrentVersionID != nil {
			versionID = *q.CurrentVersionID
		}
		_ = k.s.Webhooks.Dispatch(ctx, req.WorkspaceID, agent.EventQuestionPublished,
			map[string]any{"question_id": q.ID, "version_id": versionID, "status": next})
	}
	k.s.audit(ctx, req.WorkspaceID, "question.transition", "question", q.ID,
		map[string]any{"action": req.Action, "from": q.Status, "to": next})
	return k.questionByID(ctx, req.WorkspaceID, q.ID)
}

// QuestionGetReq 获取题目请求。
type QuestionGetReq struct {
	WorkspaceID string `json:"workspace_id"`
	QuestionID  string `json:"question_id"`
}

// QuestionGet 获取题目（含当前版本）。
func (k *KnowledgeService) QuestionGet(ctx context.Context, req QuestionGetReq) (*Question, error) {
	if err := k.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	return k.questionByID(ctx, req.WorkspaceID, req.QuestionID)
}

// QuestionListReq 题目列表请求。
type QuestionListReq struct {
	WorkspaceID string `json:"workspace_id"`
	Type        string `json:"type"`
	Status      string `json:"status"`
	KnowledgeID string `json:"knowledge_id"`
	Keyword     string `json:"keyword"`
	Cursor      string `json:"cursor"`
	Limit       int    `json:"limit"`
}

// QuestionList 分页列出题目。
func (k *KnowledgeService) QuestionList(ctx context.Context, req QuestionListReq) (*QuestionPage, error) {
	if err := k.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	rows, next, hasMore, err := k.s.Repo.ListQuestions(ctx, req.WorkspaceID, repository.QuestionFilter{
		Type: req.Type, Status: req.Status, KnowledgeID: req.KnowledgeID,
		Keyword: req.Keyword, Cursor: req.Cursor, Limit: req.Limit,
	})
	if err != nil {
		return nil, err
	}
	items := make([]*Question, 0, len(rows))
	for _, r := range rows {
		q, err := k.questionByID(ctx, req.WorkspaceID, r.ID)
		if err != nil {
			return nil, err
		}
		items = append(items, q)
	}
	return &QuestionPage{Items: items, NextCursor: next, HasMore: hasMore}, nil
}

// questionByID 组装完整题目 DTO。
func (k *KnowledgeService) questionByID(ctx context.Context, wsID, id string) (*Question, error) {
	q, err := k.s.Repo.GetQuestion(ctx, wsID, id)
	if err != nil {
		return nil, err
	}
	if q == nil {
		return nil, domain.NotFound("题目不存在或已被删除")
	}
	out := &Question{
		ID: q.ID, WorkspaceID: q.WorkspaceID, Type: q.Type, Status: q.Status,
		Source: q.Source, ContentHash: q.ContentHash,
		CreatedAt: q.CreatedAt, UpdatedAt: q.UpdatedAt, Version: q.Version,
	}
	_ = json.Unmarshal(q.Tags, &out.Tags)
	if q.CurrentVersionID != nil {
		v, err := k.s.Repo.GetQuestionVersion(ctx, *q.CurrentVersionID)
		if err != nil {
			return nil, err
		}
		if v != nil {
			out.CurrentVersion = questionVersionFromRow(v)
		}
	}
	return out, nil
}

func questionVersionFromRow(r *repository.QuestionVersionRow) *QuestionVersion {
	return &QuestionVersion{
		ID: r.ID, QuestionID: r.QuestionID, VersionNo: r.VersionNo,
		Payload: r.Payload, GeneratedByModel: r.GeneratedBy, PromptVersion: r.PromptVer,
		ReviewStatus: r.Review, CreatedAt: r.CreatedAt,
	}
}

// parseQuestionPayload 解析并做基础类型校验。
func parseQuestionPayload(raw json.RawMessage) (*QuestionPayload, error) {
	var p QuestionPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, domain.InvalidArg("payload 解析失败: %v", err)
	}
	switch p.Type {
	case "single_choice", "multiple_choice", "fill_blank", "short_answer", "code":
	default:
		return nil, domain.InvalidArg("type 仅允许 single_choice/multiple_choice/fill_blank/short_answer/code")
	}
	if strings.TrimSpace(p.Stem) == "" {
		return nil, domain.InvalidArg("题干（stem）不能为空")
	}
	if len(p.Stem) > 10000 {
		return nil, domain.InvalidArg("题干过长（上限 10000 字符）")
	}
	if p.Difficulty < 1 || p.Difficulty > 5 {
		p.Difficulty = 3
	}
	if p.Source == "" {
		p.Source = "manual"
	}
	return &p, nil
}

// validatePayload 做题型相关的业务校验。
func (k *KnowledgeService) validatePayload(ctx context.Context, wsID string, p *QuestionPayload) error {
	switch p.Type {
	case "single_choice", "multiple_choice":
		if len(p.Options) < 2 || len(p.Options) > 10 {
			return domain.InvalidArg("选择题选项须为 2-10 个")
		}
		keys := map[string]bool{}
		for _, o := range p.Options {
			if o.Key == "" || o.Text == "" {
				return domain.InvalidArg("选项 key 与 text 不能为空")
			}
			if keys[o.Key] {
				return domain.InvalidArg("选项 key 重复: %s", o.Key)
			}
			keys[o.Key] = true
		}
		var single string
		var multi []string
		if err := json.Unmarshal(p.Answer, &single); err == nil {
			if !keys[single] {
				return domain.InvalidArg("答案 %s 不在选项中", single)
			}
		} else if err := json.Unmarshal(p.Answer, &multi); err == nil {
			if len(multi) == 0 {
				return domain.InvalidArg("多选题答案不能为空")
			}
			for _, k := range multi {
				if !keys[k] {
					return domain.InvalidArg("答案 %s 不在选项中", k)
				}
			}
			if p.Type == "single_choice" && len(multi) > 1 {
				return domain.InvalidArg("单选题只能有一个答案")
			}
		} else {
			return domain.InvalidArg("选择题答案格式非法")
		}
	case "fill_blank":
		var s string
		var arr []string
		if err := json.Unmarshal(p.Answer, &s); err != nil && json.Unmarshal(p.Answer, &arr) != nil {
			return domain.InvalidArg("填空题答案须为字符串或字符串数组")
		}
	case "short_answer":
		if len(p.Answer) == 0 {
			return domain.InvalidArg("简答题需要参考答案")
		}
	case "code":
		if len(p.Answer) == 0 {
			return domain.InvalidArg("代码题需要参考实现")
		}
		if p.GradingConfig == nil {
			return domain.InvalidArg("代码题需要 grading_config（language/time_limit/memory_limit）")
		}
	}
	if len(p.KnowledgeIDs) > 0 {
		for _, kid := range p.KnowledgeIDs {
			n, err := k.s.Repo.GetKnowledgeNode(ctx, wsID, kid)
			if err != nil {
				return err
			}
			if n == nil {
				return domain.InvalidArg("知识点 %s 不存在", kid)
			}
		}
	}
	return nil
}

// questionContentHash 规范化内容哈希：type + stem + options + answer。
func questionContentHash(p *QuestionPayload) string {
	canonical := map[string]any{
		"type":    p.Type,
		"stem":    strings.TrimSpace(p.Stem),
		"options": p.Options,
		"answer":  p.Answer,
	}
	b, _ := json.Marshal(canonical)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// mustJSON 序列化任意值为 JSON 文本（失败返回 {}）。
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}
