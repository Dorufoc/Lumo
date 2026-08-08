// knowledge_graph_test.go 知识图谱服务测试（TDD）。
// 测试先于实现：MasterySnapshotList/MasteryExplain 的读出路径依赖 0005 ALTER 追加的
// review_events.user_id 未填充，故复习证据一律按 review_cards.user_id 过滤（见仓储 JOIN）。
package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"lumo/internal/domain"
	"lumo/internal/repository"
)

// setupKG 建服务 + 工作区 + 默认用户，返回 (s, workspaceID, userID)。
func setupKG(t *testing.T) (*Services, string, string) {
	t.Helper()
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	return s, ws.ID, userID
}

// taggedSCPayload 构造带知识点标签的单选 payload（question_knowledge 经 QuestionCreateDraft 写入）。
func taggedSCPayload(stem, answer string, knowledgeIDs []string) json.RawMessage {
	return mustJSON(map[string]any{
		"type": "single_choice",
		"stem": stem,
		"options": []map[string]any{
			{"key": "A", "text": "选项A"},
			{"key": "B", "text": "选项B"},
			{"key": "C", "text": "选项C"},
		},
		"answer":        answer,
		"knowledge_ids": knowledgeIDs,
	})
}

// runPractice 跑完一个单题练习会话（发布题已就绪）：start → save → submit，全部走真实服务路径。
func runPractice(t *testing.T, s *Services, wsID, userID, questionID, answer string) {
	t.Helper()
	session, err := s.Practice.PracticeStart(ctx(), PracticeStartReq{
		WorkspaceID: wsID, UserID: userID, Mode: "practice",
		QuestionIDs: []string{questionID}, IdempotencyKey: "ps-" + NewID(),
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	qvid := session.Questions[0].QuestionVersionID
	if _, err := s.Practice.PracticeSaveAnswer(ctx(), PracticeSaveAnswerReq{
		WorkspaceID: wsID, SessionID: session.ID, QuestionVersionID: qvid,
		Answer: json.RawMessage(answer), ClientSequence: 1,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := s.Practice.PracticeSubmit(ctx(), PracticeSubmitReq{
		WorkspaceID: wsID, SessionID: session.ID, Version: session.Version,
		IdempotencyKey: "psub-" + NewID(),
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}
}

// seedGrading 造一条判分证据：发布一道贴知识点的题并完成一次练习。
// correct=true 答对（value=1.0），否则答错（value=0.0）。
func seedGrading(t *testing.T, s *Services, wsID, userID, knowledgeID string, correct bool) {
	t.Helper()
	q := publishedQuestion(t, s, wsID, taggedSCPayload("判分-"+NewID(), "B", []string{knowledgeID}))
	ans := `"A"`
	if correct {
		ans = `"B"`
	}
	runPractice(t, s, wsID, userID, q.ID, ans)
}

// seedReviewEvent 直插复习证据链（不经 PracticeSubmit，避免产生判分样本）：
// 会话 → 提交 → 错题 → 复习卡 → 复习事件。经 0005 ALTER 的 review_events.user_id 未填充，
// 服务侧按 review_cards.user_id 过滤，因此 userID 必须落在 review_cards 行上。
func seedReviewEvent(t *testing.T, s *Services, wsID, userID, knowledgeID, rating string) {
	t.Helper()
	q := publishedQuestion(t, s, wsID, taggedSCPayload("复习-"+NewID(), "B", []string{knowledgeID}))
	session := &repository.PracticeSessionRow{
		ID: NewID(), WorkspaceID: wsID, UserID: userID, Mode: "practice",
		QuestionSnapshot: json.RawMessage(`[]`), StartedAt: strPtr(Now()),
	}
	if err := s.Repo.CreateSession(ctx(), session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	sub := &repository.SubmissionRow{
		ID: NewID(), SessionID: session.ID, QuestionVersionID: q.CurrentVersion.ID,
		Answer: json.RawMessage(`"A"`), ClientSequence: 1,
	}
	if _, err := s.Repo.UpsertDraft(ctx(), sub); err != nil {
		t.Fatalf("upsert draft: %v", err)
	}
	wa := &repository.WrongAnswerRow{
		ID: NewID(), WorkspaceID: wsID, UserID: userID, SubmissionID: sub.ID,
		QuestionVersionID: q.CurrentVersion.ID, Answer: json.RawMessage(`"A"`),
	}
	if err := s.Repo.CreateWrongAnswer(ctx(), wa); err != nil {
		t.Fatalf("create wrong answer: %v", err)
	}
	card := &repository.ReviewCardRow{
		ID: NewID(), WorkspaceID: wsID, UserID: userID, WrongAnswerID: wa.ID,
		Repetition: 1, IntervalDays: 1, EaseFactor: 2.5, DueAt: Now(),
	}
	if err := s.Repo.CreateReviewCard(ctx(), card); err != nil {
		t.Fatalf("create review card: %v", err)
	}
	if err := s.Repo.CreateReviewEvent(ctx(), &repository.ReviewEventRow{
		ID: NewID(), ReviewCardID: card.ID, Rating: rating,
		Previous: json.RawMessage(`{}`), Current: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("create review event: %v", err)
	}
}

// seedSnapshot 直插一条掌握度快照（分页测试用）。
func seedSnapshot(t *testing.T, s *Services, userID, knowledgeID string, score float64, sample int, computedAt string) {
	t.Helper()
	if _, err := s.Repo.DB().ExecContext(ctx(), `
		INSERT INTO mastery_snapshots (id, user_id, knowledge_id, mastery_score, sample_size, computed_at)
		VALUES (?, ?, ?, ?, ?, ?)`, NewID(), userID, knowledgeID, score, sample, computedAt); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
}

// seedRelation 直插一条知识关系（无写接口，图谱读取侧直接入库）。
func seedRelation(t *testing.T, s *Services, fromID, toID, relType, source string) {
	t.Helper()
	if _, err := s.Repo.DB().ExecContext(ctx(), `
		INSERT INTO knowledge_relations (id, from_knowledge_id, to_knowledge_id, rel_type, source, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, NewID(), fromID, toID, relType, source, Now()); err != nil {
		t.Fatalf("seed relation: %v", err)
	}
}

// setParent 直接把知识节点的 parent_id 指向父节点（派生 parent 边来源）。
func setParent(t *testing.T, s *Services, wsID, childID, parentID string) {
	t.Helper()
	if _, err := s.Repo.DB().ExecContext(ctx(), `
		UPDATE knowledge_nodes SET parent_id = ? WHERE id = ? AND workspace_id = ?`, parentID, childID, wsID); err != nil {
		t.Fatalf("set parent: %v", err)
	}
}

// bulkSeedNodes 批量直插知识节点（Top-N 截断测试），返回 id 切片（node_path 与序号对齐）。
func bulkSeedNodes(t *testing.T, s *Services, wsID string, n int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("kn-%05d", i)
		if _, err := s.Repo.DB().ExecContext(ctx(), `
			INSERT INTO knowledge_nodes (id, workspace_id, name, node_path, level, version)
			VALUES (?, ?, ?, ?, 0, 1)`, id, wsID, fmt.Sprintf("节点%05d", i), fmt.Sprintf("/%05d", i)); err != nil {
			t.Fatalf("seed node %d: %v", i, err)
		}
		ids = append(ids, id)
	}
	return ids
}

// assertSnapshot 断言 (user, knowledge) 最新快照的掌握度与样本数。
func assertSnapshot(t *testing.T, s *Services, userID, knowledgeID string, mastery float64, sample int) {
	t.Helper()
	snap, err := s.Repo.GetLatestMasterySnapshot(ctx(), userID, knowledgeID)
	if err != nil {
		t.Fatalf("snapshot %s: %v", knowledgeID, err)
	}
	if snap == nil {
		t.Fatalf("expected snapshot for %s", knowledgeID)
	}
	if snap.SampleSize != sample {
		t.Fatalf("%s sample_size = %d, want %d", knowledgeID, snap.SampleSize, sample)
	}
	if snap.MasteryScore != mastery {
		t.Fatalf("%s mastery = %v, want %v", knowledgeID, snap.MasteryScore, mastery)
	}
}

// TestMasteryComputeFromGradingsOnly 只靠判分证据：1 对 1 错 → accuracy 0.5，
// damping = min(1, 2/20) = 0.1 → mastery 0.05。
func TestMasteryComputeFromGradingsOnly(t *testing.T) {
	s, wsID, userID := setupKG(t)
	k1 := createKnowledgeNode(t, s, wsID, "K1")
	seedGrading(t, s, wsID, userID, k1, true)
	seedGrading(t, s, wsID, userID, k1, false)

	if _, err := s.KnowledgeGraph.KnowledgeGraphGet(ctx(), KnowledgeGraphGetReq{WorkspaceID: wsID, UserID: userID}); err != nil {
		t.Fatalf("graph: %v", err)
	}
	assertSnapshot(t, s, userID, k1, 0.05, 2)
}

// TestMasteryComputeWeighting 权重与 damping：20 连对 → 1.0；40 连对 → 仍 1.0；10 连对 → 0.5。
func TestMasteryComputeWeighting(t *testing.T) {
	s, wsID, userID := setupKG(t)
	k2 := createKnowledgeNode(t, s, wsID, "K2")
	k3 := createKnowledgeNode(t, s, wsID, "K3")
	k4 := createKnowledgeNode(t, s, wsID, "K4")
	q2 := publishedQuestion(t, s, wsID, taggedSCPayload("加权题2", "B", []string{k2}))
	q3 := publishedQuestion(t, s, wsID, taggedSCPayload("加权题3", "B", []string{k3}))
	q4 := publishedQuestion(t, s, wsID, taggedSCPayload("加权题4", "B", []string{k4}))
	for i := 0; i < 20; i++ {
		runPractice(t, s, wsID, userID, q2.ID, `"B"`)
	}
	for i := 0; i < 40; i++ {
		runPractice(t, s, wsID, userID, q3.ID, `"B"`)
	}
	for i := 0; i < 10; i++ {
		runPractice(t, s, wsID, userID, q4.ID, `"B"`)
	}

	if _, err := s.KnowledgeGraph.KnowledgeGraphGet(ctx(), KnowledgeGraphGetReq{WorkspaceID: wsID, UserID: userID}); err != nil {
		t.Fatalf("graph: %v", err)
	}
	assertSnapshot(t, s, userID, k2, 1.0, 20)
	assertSnapshot(t, s, userID, k3, 1.0, 40)
	assertSnapshot(t, s, userID, k4, 0.5, 10)
}

// TestMasteryComputeReviewEvents 复习证据：good(1.0) + again(0.0) → accuracy 0.5、
// sample 2、mastery 0.05；且判分证据为空时必须只吃复习证据。
func TestMasteryComputeReviewEvents(t *testing.T) {
	s, wsID, userID := setupKG(t)
	k4 := createKnowledgeNode(t, s, wsID, "K4")
	seedReviewEvent(t, s, wsID, userID, k4, "good")
	seedReviewEvent(t, s, wsID, userID, k4, "again")

	if _, err := s.KnowledgeGraph.KnowledgeGraphGet(ctx(), KnowledgeGraphGetReq{WorkspaceID: wsID, UserID: userID}); err != nil {
		t.Fatalf("graph: %v", err)
	}
	assertSnapshot(t, s, userID, k4, 0.05, 2)
}

// TestMasteryComputeMixedSources 混合证据：正确判分(1.0) + good(1.0) + again(0.0)
// → 加权 2/3、sample 3、mastery = 2/3 × 0.15 = 0.1。
func TestMasteryComputeMixedSources(t *testing.T) {
	s, wsID, userID := setupKG(t)
	k5 := createKnowledgeNode(t, s, wsID, "K5")
	seedGrading(t, s, wsID, userID, k5, true)
	seedReviewEvent(t, s, wsID, userID, k5, "good")
	seedReviewEvent(t, s, wsID, userID, k5, "again")

	if _, err := s.KnowledgeGraph.KnowledgeGraphGet(ctx(), KnowledgeGraphGetReq{WorkspaceID: wsID, UserID: userID}); err != nil {
		t.Fatalf("graph: %v", err)
	}
	assertSnapshot(t, s, userID, k5, 0.1, 3)
}

// TestMasteryNoSamplesNoSnapshot 无证据不落快照：节点照常返回且无 mastery 字段。
func TestMasteryNoSamplesNoSnapshot(t *testing.T) {
	s, wsID, userID := setupKG(t)
	k6 := createKnowledgeNode(t, s, wsID, "K6")

	g, err := s.KnowledgeGraph.KnowledgeGraphGet(ctx(), KnowledgeGraphGetReq{WorkspaceID: wsID, UserID: userID})
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	snap, err := s.Repo.GetLatestMasterySnapshot(ctx(), userID, k6)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap != nil {
		t.Fatal("expected no snapshot without samples")
	}
	found := false
	for _, n := range g.Nodes {
		if n.ID == k6 {
			found = true
			if n.Mastery != nil {
				t.Fatalf("unexpected mastery: %v", *n.Mastery)
			}
			if n.SampleSize != 0 {
				t.Fatalf("unexpected sample_size: %d", n.SampleSize)
			}
		}
	}
	if !found {
		t.Fatal("k6 not in graph")
	}
}

// TestKnowledgeGraphGetAssembly 图谱装配：parent_id 派生边 + DB 关系边各一条、
// 重复关系去重、工作区隔离。
func TestKnowledgeGraphGetAssembly(t *testing.T) {
	s, wsID, userID := setupKG(t)
	root := createKnowledgeNode(t, s, wsID, "根")
	child := createKnowledgeNode(t, s, wsID, "子")
	setParent(t, s, wsID, child, root)
	seedRelation(t, s, root, child, "prerequisite", "manual")
	seedRelation(t, s, root, child, "prerequisite", "manual") // 重复插入 → 去重
	ws2, _ := createWorkspace(t, s)
	otherA := createKnowledgeNode(t, s, ws2.ID, "别的A")
	otherB := createKnowledgeNode(t, s, ws2.ID, "别的B")
	seedRelation(t, s, otherA, otherB, "related", "manual")

	g, err := s.KnowledgeGraph.KnowledgeGraphGet(ctx(), KnowledgeGraphGetReq{WorkspaceID: wsID, UserID: userID})
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	if len(g.Nodes) != 2 {
		t.Fatalf("nodes = %d", len(g.Nodes))
	}
	if g.Truncated {
		t.Fatal("unexpected truncation")
	}
	parentEdges, prereqEdges := 0, 0
	for _, e := range g.Edges {
		if e.From != root || e.To != child {
			t.Fatalf("unexpected edge: %+v", e)
		}
		switch e.Type {
		case "parent":
			parentEdges++
		case "prerequisite":
			prereqEdges++
		default:
			t.Fatalf("unexpected edge type: %s", e.Type)
		}
	}
	if parentEdges != 1 || prereqEdges != 1 {
		t.Fatalf("edges: parent=%d prerequisite=%d, want 1/1", parentEdges, prereqEdges)
	}
}

// TestKnowledgeGraphGetTopNTruncation 超过 2000 节点：截断到 2000，被截断节点的边丢弃。
func TestKnowledgeGraphGetTopNTruncation(t *testing.T) {
	s, wsID, _ := setupKG(t)
	ids := bulkSeedNodes(t, s, wsID, 2001)
	seedRelation(t, s, ids[0], ids[2000], "related", "manual")

	g, err := s.KnowledgeGraph.KnowledgeGraphGet(ctx(), KnowledgeGraphGetReq{WorkspaceID: wsID})
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	if !g.Truncated {
		t.Fatal("expected truncation")
	}
	if len(g.Nodes) != 2000 {
		t.Fatalf("nodes = %d", len(g.Nodes))
	}
	for _, e := range g.Edges {
		if e.To == ids[2000] || e.From == ids[2000] {
			t.Fatal("edge to truncated node leaked")
		}
	}
}

// TestKnowledgeGraphGetSingleIsolatedNode 单节点无关系：1 节点 0 边不报错；
// 不带 user_id 时节点无 mastery。
func TestKnowledgeGraphGetSingleIsolatedNode(t *testing.T) {
	s, wsID, _ := setupKG(t)
	createKnowledgeNode(t, s, wsID, "孤立")

	g, err := s.KnowledgeGraph.KnowledgeGraphGet(ctx(), KnowledgeGraphGetReq{WorkspaceID: wsID})
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	if len(g.Nodes) != 1 {
		t.Fatalf("nodes = %d", len(g.Nodes))
	}
	if len(g.Edges) != 0 {
		t.Fatalf("edges = %d", len(g.Edges))
	}
	if g.Edges == nil {
		t.Fatal("edges should be non-nil empty slice, not null")
	}
	raw, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"edges":[]`)) {
		t.Fatalf("edges should marshal to [] not null: %s", raw)
	}
	if g.Nodes[0].Mastery != nil {
		t.Fatalf("mastery without user: %v", *g.Nodes[0].Mastery)
	}
}

// TestMasterySnapshotListPagination 分页：computed_at DESC 排序、cursor 续页、
// 知识过滤。7 条（KB×2 最新 + KA×5），页大小 5 → 第二页 2 条且无更多。
func TestMasterySnapshotListPagination(t *testing.T) {
	s, wsID, userID := setupKG(t)
	ka := createKnowledgeNode(t, s, wsID, "KA")
	kb := createKnowledgeNode(t, s, wsID, "KB")
	for i := 1; i <= 5; i++ {
		seedSnapshot(t, s, userID, ka, float64(i)/10, i, fmt.Sprintf("2026-08-0%dT10:00:00Z", i))
	}
	for i := 6; i <= 7; i++ {
		seedSnapshot(t, s, userID, kb, float64(i)/10, i, fmt.Sprintf("2026-08-0%dT10:00:00Z", i))
	}

	page, err := s.KnowledgeGraph.MasterySnapshotList(ctx(), MasterySnapshotListReq{UserID: userID, Limit: 5})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Items) != 5 || !page.HasMore {
		t.Fatalf("page1: items=%d hasMore=%v", len(page.Items), page.HasMore)
	}
	if page.Items[0].KnowledgeID != kb || page.Items[1].KnowledgeID != kb {
		t.Fatal("snapshots not sorted by computed_at DESC")
	}
	if page.NextCursor == "" {
		t.Fatal("expected next_cursor")
	}
	page2, err := s.KnowledgeGraph.MasterySnapshotList(ctx(), MasterySnapshotListReq{UserID: userID, Cursor: page.NextCursor, Limit: 5})
	if err != nil {
		t.Fatalf("list2: %v", err)
	}
	if len(page2.Items) != 2 || page2.HasMore || page2.NextCursor != "" {
		t.Fatalf("page2: items=%d hasMore=%v next=%q", len(page2.Items), page2.HasMore, page2.NextCursor)
	}
	only, err := s.KnowledgeGraph.MasterySnapshotList(ctx(), MasterySnapshotListReq{UserID: userID, KnowledgeID: kb})
	if err != nil {
		t.Fatalf("list kb: %v", err)
	}
	if len(only.Items) != 2 {
		t.Fatalf("kb items = %d", len(only.Items))
	}
	for _, it := range only.Items {
		if it.KnowledgeID != kb {
			t.Fatal("wrong knowledge in filtered list")
		}
	}
}

// TestMasteryExplainEvidence 解释：formula_version=v1、最近 10 条证据倒序、knowledge 匹配。
func TestMasteryExplainEvidence(t *testing.T) {
	s, wsID, userID := setupKG(t)
	k := createKnowledgeNode(t, s, wsID, "K")
	q := publishedQuestion(t, s, wsID, taggedSCPayload("解释题", "B", []string{k}))
	for i := 0; i < 12; i++ {
		runPractice(t, s, wsID, userID, q.ID, `"B"`)
	}

	ex, err := s.KnowledgeGraph.MasteryExplain(ctx(), MasteryExplainReq{UserID: userID, KnowledgeID: k})
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if ex.KnowledgeID != k {
		t.Fatalf("knowledge_id = %s", ex.KnowledgeID)
	}
	if ex.FormulaVersion != "v1" {
		t.Fatalf("formula_version = %s", ex.FormulaVersion)
	}
	if ex.FormulaDescription == "" {
		t.Fatal("empty formula description")
	}
	if ex.SampleSize != 12 {
		t.Fatalf("sample_size = %d", ex.SampleSize)
	}
	if len(ex.Evidence) != 10 {
		t.Fatalf("evidence = %d", len(ex.Evidence))
	}
	for i := 0; i < len(ex.Evidence); i++ {
		if ex.Evidence[i].KnowledgeID != k {
			t.Fatalf("evidence %d wrong knowledge", i)
		}
		if i > 0 && ex.Evidence[i-1].OccurredAt < ex.Evidence[i].OccurredAt {
			t.Fatal("evidence not sorted desc")
		}
	}
}

// TestMasteryExplainValidation 参数校验：空 user_id → INVALID_ARGUMENT；未知知识点 → NOT_FOUND。
func TestMasteryExplainValidation(t *testing.T) {
	s, wsID, userID := setupKG(t)
	if _, err := s.KnowledgeGraph.MasteryExplain(ctx(), MasteryExplainReq{UserID: "", KnowledgeID: "x"}); err == nil || domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("empty user: %v", err)
	}
	createKnowledgeNode(t, s, wsID, "K")
	if _, err := s.KnowledgeGraph.MasteryExplain(ctx(), MasteryExplainReq{UserID: userID, KnowledgeID: "nope"}); err == nil || domain.AsError(err).Code != domain.CodeNotFound {
		t.Fatalf("unknown knowledge: %v", err)
	}
}

// TestMasterySnapshotListValidation 参数校验：空 user_id → INVALID_ARGUMENT。
func TestMasterySnapshotListValidation(t *testing.T) {
	s, _, _ := setupKG(t)
	if _, err := s.KnowledgeGraph.MasterySnapshotList(ctx(), MasterySnapshotListReq{UserID: ""}); err == nil || domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("empty user: %v", err)
	}
}
