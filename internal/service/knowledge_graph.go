// knowledge_graph.go 知识图谱服务：图谱装配、掌握度快照重算与解释。
// 掌握度公式（K1）：加权正确率 accuracy = Σ(样本值×权重)/Σ(权重)；
// damping = min(1, 样本数/20)；mastery = round4(accuracy × damping)，样本数=0 不落快照。
// 证据样本：判分（correct=1.0/wrong=0.0，权重=question_knowledge.weight 缺省 1）+
// 复习事件（good=1.0/hard=0.5/again=0.0，权重 1，按 review_cards.user_id 过滤）。
package service

import (
	"context"
	"database/sql"
	"math"

	"lumo/internal/domain"
	"lumo/internal/repository"
)

// MasteryFormulaVersion 掌握度公式版本标识。
const MasteryFormulaVersion = "v1"

// MasteryFormulaDescription 掌握度公式说明（K1 前端展示）。
const MasteryFormulaDescription = "掌握度 = 加权正确率 × min(1, 证据数/20)。加权正确率 = Σ(样本值×权重) / Σ(权重)，证据来自练习判分与复习反馈，样本少于 20 时按比例打折。"

// maxGraphNodes 图谱节点上限（>2000 截断，K5）。
const maxGraphNodes = 2000

// masteryDampingDenom 掌握度阻尼分母（20 次满格）。
const masteryDampingDenom = 20.0

// KnowledgeGraphService 知识图谱服务。
type KnowledgeGraphService struct {
	s *Services
}

// KnowledgeGraphGetReq 获取知识图谱。
type KnowledgeGraphGetReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id,omitempty"` // 传入则附带掌握度（触发重算）
}

// KnowledgeGraphNode 图谱节点。
type KnowledgeGraphNode struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Level      int      `json:"level"`
	ParentID   *string  `json:"parent_id,omitempty"`
	Mastery    *float64 `json:"mastery,omitempty"` // 掌握度 0-1；无证据不返回
	SampleSize int      `json:"sample_size,omitempty"`
}

// KnowledgeGraphEdge 图谱边：parent 派生自 parent_id；prerequisite/related 来自 knowledge_relations。
type KnowledgeGraphEdge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Type   string `json:"type"`   // parent | prerequisite | related
	Source string `json:"source"` // manual | ai
}

// KnowledgeGraph 图谱响应。
type KnowledgeGraph struct {
	Nodes     []*KnowledgeGraphNode `json:"nodes"`
	Edges     []*KnowledgeGraphEdge `json:"edges"`
	Truncated bool                  `json:"truncated"` // 节点数超过 2000 被截断
}

// KnowledgeGraphGet 装配工作区知识图谱（K2）。
// user_id 非空时先重算该用户掌握度快照，再附加到节点上。
func (k *KnowledgeGraphService) KnowledgeGraphGet(ctx context.Context, req KnowledgeGraphGetReq) (*KnowledgeGraph, error) {
	if err := k.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	nodes, err := k.s.Repo.ListKnowledgeNodes(ctx, req.WorkspaceID)
	if err != nil {
		return nil, err
	}
	relations, err := k.s.Repo.ListKnowledgeRelationsInWorkspace(ctx, req.WorkspaceID)
	if err != nil {
		return nil, err
	}

	truncated := false
	if len(nodes) > maxGraphNodes {
		nodes = nodes[:maxGraphNodes]
		truncated = true
	}
	kept := make(map[string]bool, len(nodes))
	out := make([]*KnowledgeGraphNode, 0, len(nodes))
	for _, n := range nodes {
		kept[n.ID] = true
		out = append(out, &KnowledgeGraphNode{ID: n.ID, Name: n.Name, Level: n.Level, ParentID: n.ParentID})
	}

	// 边去重 key: from|to|type（同类型重复关系只保留一条）
	type edgeKey struct{ from, to, typ string }
	seen := make(map[edgeKey]bool)
	var edges []*KnowledgeGraphEdge
	addEdge := func(from, to, typ, source string) {
		if !kept[from] || !kept[to] {
			return // 截断后不在保留集内的边丢弃
		}
		key := edgeKey{from, to, typ}
		if seen[key] {
			return
		}
		seen[key] = true
		edges = append(edges, &KnowledgeGraphEdge{From: from, To: to, Type: typ, Source: source})
	}
	for _, n := range nodes {
		if n.ParentID != nil {
			addEdge(*n.ParentID, n.ID, "parent", "manual")
		}
	}
	for _, r := range relations {
		addEdge(r.FromKnowledgeID, r.ToKnowledgeID, r.RelType, r.Source)
	}

	if req.UserID != "" {
		if err := k.recomputeMastery(ctx, req.UserID); err != nil {
			return nil, err
		}
		for _, n := range out {
			snap, err := k.s.Repo.GetLatestMasterySnapshot(ctx, req.UserID, n.ID)
			if err != nil {
				return nil, err
			}
			if snap != nil {
				score := snap.MasteryScore
				n.Mastery = &score
				n.SampleSize = snap.SampleSize
			}
		}
	}
	return &KnowledgeGraph{Nodes: out, Edges: edges, Truncated: truncated}, nil
}

// MasterySnapshotListReq 分页列出掌握度快照。
type MasterySnapshotListReq struct {
	UserID      string `json:"user_id"`
	KnowledgeID string `json:"knowledge_id,omitempty"`
	Cursor      string `json:"cursor,omitempty"`
	Limit       int    `json:"limit,omitempty"` // 默认 50，上限 100
}

// MasterySnapshotListItem 快照条目。
type MasterySnapshotListItem struct {
	ID            string  `json:"id"`
	UserID        string  `json:"user_id"`
	KnowledgeID   string  `json:"knowledge_id"`
	KnowledgeName string  `json:"knowledge_name"`
	MasteryScore  float64 `json:"mastery_score"`
	SampleSize    int     `json:"sample_size"`
	ComputedAt    string  `json:"computed_at"`
}

// MasterySnapshotPage 快照分页响应。
type MasterySnapshotPage struct {
	Items      []*MasterySnapshotListItem `json:"items"`
	NextCursor string                     `json:"next_cursor"`
	HasMore    bool                       `json:"has_more"`
}

// MasterySnapshotList 分页列出某用户的掌握度快照（K4）。
func (k *KnowledgeGraphService) MasterySnapshotList(ctx context.Context, req MasterySnapshotListReq) (*MasterySnapshotPage, error) {
	if req.UserID == "" || !domain.ValidID(req.UserID) {
		return nil, domain.InvalidArg("user_id 无效")
	}
	limit := req.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	items, next, hasMore, err := k.s.Repo.ListMasterySnapshots(ctx, req.UserID, req.KnowledgeID, req.Cursor, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*MasterySnapshotListItem, 0, len(items))
	for _, it := range items {
		out = append(out, &MasterySnapshotListItem{
			ID:            it.ID,
			UserID:        it.UserID,
			KnowledgeID:   it.KnowledgeID,
			KnowledgeName: it.KnowledgeName,
			MasteryScore:  it.MasteryScore,
			SampleSize:    it.SampleSize,
			ComputedAt:    it.ComputedAt,
		})
	}
	return &MasterySnapshotPage{Items: out, NextCursor: next, HasMore: hasMore}, nil
}

// MasteryExplainReq 掌握度解释。
type MasteryExplainReq struct {
	UserID      string `json:"user_id"`
	KnowledgeID string `json:"knowledge_id"`
}

// MasteryEvidence 一条证据样本。
type MasteryEvidence struct {
	KnowledgeID string  `json:"knowledge_id"`
	Type        string  `json:"type"` // grading | review
	Value       float64 `json:"value"`
	Weight      float64 `json:"weight"`
	OccurredAt  string  `json:"occurred_at"`
}

// MasteryExplanation 掌握度解释（K3：公式 + 最近 10 次作答明细）。
type MasteryExplanation struct {
	KnowledgeID        string             `json:"knowledge_id"`
	KnowledgeName      string             `json:"knowledge_name"`
	MasteryScore       float64            `json:"mastery_score"`
	SampleSize         int                `json:"sample_size"`
	FormulaVersion     string             `json:"formula_version"`
	FormulaDescription string             `json:"formula_description"`
	Evidence           []*MasteryEvidence `json:"evidence"`
}

// MasteryExplain 解释某知识点的掌握度（K3）。未知知识点 → NOT_FOUND；无证据返回 0 分与空明细。
func (k *KnowledgeGraphService) MasteryExplain(ctx context.Context, req MasteryExplainReq) (*MasteryExplanation, error) {
	if req.UserID == "" || !domain.ValidID(req.UserID) {
		return nil, domain.InvalidArg("user_id 无效")
	}
	if req.KnowledgeID == "" {
		return nil, domain.InvalidArg("knowledge_id 无效")
	}
	kn, err := k.s.Repo.GetKnowledgeNodeByID(ctx, req.KnowledgeID)
	if err != nil {
		return nil, err
	}
	if kn == nil {
		return nil, domain.NotFound("知识点不存在或已删除")
	}
	if err := k.recomputeMastery(ctx, req.UserID); err != nil {
		return nil, err
	}
	snap, err := k.s.Repo.GetLatestMasterySnapshot(ctx, req.UserID, req.KnowledgeID)
	if err != nil {
		return nil, err
	}
	ev, err := k.s.Repo.ListRecentMasterySamples(ctx, req.UserID, req.KnowledgeID, 10)
	if err != nil {
		return nil, err
	}
	ex := &MasteryExplanation{
		KnowledgeID:        kn.ID,
		KnowledgeName:      kn.Name,
		FormulaVersion:     MasteryFormulaVersion,
		FormulaDescription: MasteryFormulaDescription,
		Evidence:           make([]*MasteryEvidence, 0, len(ev)),
	}
	if snap != nil {
		ex.MasteryScore = snap.MasteryScore
		ex.SampleSize = snap.SampleSize
	}
	for _, e := range ev {
		ex.Evidence = append(ex.Evidence, &MasteryEvidence{
			KnowledgeID: e.KnowledgeID, Type: e.Type, Value: e.Value,
			Weight: e.Weight, OccurredAt: e.OccurredAt,
		})
	}
	return ex, nil
}

// recomputeMastery 重算用户全部掌握度快照并整体写回（单事务）。
// 样本数=0 的知识点不落快照；每条 (user, knowledge) 恰好一条最新快照。
func (k *KnowledgeGraphService) recomputeMastery(ctx context.Context, userID string) error {
	gradings, err := k.s.Repo.ListGradingMasterySamples(ctx, userID)
	if err != nil {
		return err
	}
	reviews, err := k.s.Repo.ListReviewMasterySamples(ctx, userID)
	if err != nil {
		return err
	}
	samples := append(gradings, reviews...)

	type agg struct {
		sumValue, sumWeight float64
		count               int
	}
	byKnowledge := make(map[string]*agg)
	for _, smp := range samples {
		a := byKnowledge[smp.KnowledgeID]
		if a == nil {
			a = &agg{}
			byKnowledge[smp.KnowledgeID] = a
		}
		a.sumValue += smp.Value * smp.Weight
		a.sumWeight += smp.Weight
		a.count++
	}

	now := Now()
	var snapshots []*repository.MasterySnapshotRow
	for kid, a := range byKnowledge {
		if a.count == 0 || a.sumWeight == 0 {
			continue
		}
		accuracy := a.sumValue / a.sumWeight
		damping := math.Min(1, float64(a.count)/masteryDampingDenom)
		snapshots = append(snapshots, &repository.MasterySnapshotRow{
			ID:           NewID(),
			UserID:       userID,
			KnowledgeID:  kid,
			MasteryScore: round4(accuracy * damping),
			SampleSize:   a.count,
			ComputedAt:   now,
		})
	}
	if len(snapshots) == 0 {
		return nil
	}
	return k.s.Repo.WithTx(ctx, func(tx *sql.Tx) error {
		for _, m := range snapshots {
			if err := k.s.Repo.UpsertMasterySnapshotTx(ctx, tx, m); err != nil {
				return err
			}
		}
		return nil
	})
}

// round4 保留 4 位小数。
func round4(x float64) float64 {
	return math.Round(x*10000) / 10000
}
