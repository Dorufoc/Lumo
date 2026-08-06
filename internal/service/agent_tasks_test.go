package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"lumo/internal/agent"
	"lumo/internal/domain"
	"lumo/internal/provider"
	"lumo/internal/sandbox"
)

// ---- 辅助 ----

// agentAuditCount 统计某动作的审计事件数。
func agentAuditCount(t *testing.T, s *Services, action string) int {
	t.Helper()
	var n int
	if err := s.Repo.DB().QueryRowContext(ctx(),
		`SELECT COUNT(*) FROM audit_events WHERE action = ?`, action).Scan(&n); err != nil {
		t.Fatalf("count audit %s: %v", action, err)
	}
	return n
}

// agentAuditPayloadContains 检查最近一条审计事件 payload 是否包含子串。
func agentAuditPayloadContains(t *testing.T, s *Services, action, substr string) bool {
	t.Helper()
	var payload string
	if err := s.Repo.DB().QueryRowContext(ctx(),
		`SELECT payload_json FROM audit_events WHERE action = ? ORDER BY created_at DESC, id DESC LIMIT 1`, action).Scan(&payload); err != nil {
		t.Fatalf("read audit %s: %v", action, err)
	}
	return strings.Contains(payload, substr)
}

// agentRowCount 统计某表行数。
func agentRowCount(t *testing.T, s *Services, query string) int {
	t.Helper()
	var n int
	if err := s.Repo.DB().QueryRowContext(ctx(), query).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return n
}

// createKnowledgeNode 创建工作区知识点并返回 ID。
func createKnowledgeNode(t *testing.T, s *Services, wsID, name string) string {
	t.Helper()
	n, err := s.Knowledge.KnowledgeCreate(ctx(), KnowledgeCreateReq{WorkspaceID: wsID, Name: name})
	if err != nil {
		t.Fatalf("create knowledge: %v", err)
	}
	return n.ID
}

// seedPublishedQuestion 创建草稿并流转到 published。
func seedPublishedQuestion(t *testing.T, s *Services, wsID string, payload json.RawMessage) *Question {
	t.Helper()
	q, err := s.Knowledge.QuestionCreateDraft(ctx(), QuestionCreateDraftReq{
		WorkspaceID: wsID, Payload: payload, IdempotencyKey: "seed-" + NewID(),
	})
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	r, err := s.Knowledge.QuestionTransition(ctx(), QuestionTransitionReq{
		WorkspaceID: wsID, QuestionID: q.ID, Version: q.Version, Action: "review",
	})
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	p, err := s.Knowledge.QuestionTransition(ctx(), QuestionTransitionReq{
		WorkspaceID: wsID, QuestionID: r.ID, Version: r.Version, Action: "publish",
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	return p
}

// ---- ① Summarizer 正常生成 → 落库 status=ready + model/prompt_version ----

func TestAgentSummarizeLLMPath(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	doc := favImportDoc(t, s, ws, "mechanics.md", "# 力学\n\n牛顿第二定律。")

	s.Agent.LLMFactory = func() (provider.LLMProvider, error) {
		return &stubLLM{content: `{"title":"力学","key_points":[{"content":"牛顿第二定律","chunk_id":"c1"}],` +
			`"structure":["力学"],"glossary":[{"term":"加速度","definition":"速度变化率"}],"citation_refs":["mechanics.md"]}`}, nil
	}
	res, err := s.AgentTasks.AgentSummarize(ctx(), AgentSummarizeReq{
		WorkspaceID: ws.ID, UserID: userID, DocumentID: doc.ID, IdempotencyKey: "as-" + NewID(),
	})
	if err != nil {
		t.Fatalf("summarize llm: %v", err)
	}
	if res.Degraded {
		t.Fatal("expected not degraded on llm path")
	}
	if res.Summary.Status != domain.SummaryStatusReady {
		t.Fatalf("expected ready, got %s", res.Summary.Status)
	}
	if res.Summary.Model != "stub" {
		t.Fatalf("expected model stub, got %q", res.Summary.Model)
	}
	if res.Summary.PromptVersion == nil || *res.Summary.PromptVersion != "1" {
		t.Fatalf("expected prompt_version 1, got %v", res.Summary.PromptVersion)
	}
	var out agent.SummarizeOutput
	if err := json.Unmarshal(res.Summary.SummaryJSON, &out); err != nil {
		t.Fatalf("unmarshal summary: %v", err)
	}
	if len(out.KeyPoints) != 1 || out.KeyPoints[0].ChunkID != "c1" || out.KeyPoints[0].Content != "牛顿第二定律" {
		t.Fatalf("unexpected key_points: %+v", out.KeyPoints)
	}
	if len(out.Glossary) != 1 || out.Glossary[0].Term != "加速度" {
		t.Fatalf("unexpected glossary: %+v", out.Glossary)
	}
	if res.Summary.CreatedAt == "" || res.Summary.UpdatedAt == "" {
		t.Fatalf("expected persisted timestamps, got CreatedAt=%q UpdatedAt=%q", res.Summary.CreatedAt, res.Summary.UpdatedAt)
	}
	if n := agentAuditCount(t, s, "agent.summarize"); n < 1 {
		t.Fatalf("expected audit agent.summarize, got %d", n)
	}
	if n := agentRowCount(t, s, `SELECT COUNT(*) FROM document_summaries WHERE document_id = '`+doc.ID+`'`); n != 1 {
		t.Fatalf("expected 1 summary row, got %d", n)
	}
}

// ---- ② Provider 未配置 → 降级：status=failed 可重试 + 审计 ----

func TestAgentSummarizeDegraded(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	doc := favImportDoc(t, s, ws, "notes.md", "# 高等数学\n\n导数定义。")

	// newTestServices 中 s.Agent.LLMFactory 为 nil → 未配置
	res, err := s.AgentTasks.AgentSummarize(ctx(), AgentSummarizeReq{
		WorkspaceID: ws.ID, UserID: userID, DocumentID: doc.ID, IdempotencyKey: "as-" + NewID(),
	})
	if err != nil {
		t.Fatalf("summarize degraded: %v", err)
	}
	if !res.Degraded {
		t.Fatal("expected degraded flag")
	}
	if res.Summary.Status != domain.SummaryStatusFailed {
		t.Fatalf("expected failed (retryable), got %s", res.Summary.Status)
	}
	if res.Summary.PromptVersion == nil {
		t.Fatal("expected prompt_version recorded on degraded path")
	}
	if !agentAuditPayloadContains(t, s, "agent.summarize", "provider") {
		t.Fatal("expected degraded audit reason recorded")
	}
}

// ---- ③ LLM 非法 JSON / 超时 → 明确错误 + 审计失败原因 ----

func TestAgentSummarizeLLMFailureAudit(t *testing.T) {
	t.Run("invalid json", func(t *testing.T) {
		s, _ := newTestServices(t)
		ws, userID := createWorkspace(t, s)
		doc := favImportDoc(t, s, ws, "bad.md", "# 失败\n\n内容。")
		s.Agent.LLMFactory = func() (provider.LLMProvider, error) {
			return &stubLLM{content: `{"broken":`}, nil
		}
		_, err := s.AgentTasks.AgentSummarize(ctx(), AgentSummarizeReq{
			WorkspaceID: ws.ID, UserID: userID, DocumentID: doc.ID, IdempotencyKey: "as-" + NewID(),
		})
		if err == nil {
			t.Fatal("expected error on invalid JSON")
		}
		if de, ok := err.(*domain.Error); !ok || de.Code != domain.CodeOutputInvalid {
			t.Fatalf("expected OUTPUT_INVALID, got %v", err)
		}
		if !agentAuditPayloadContains(t, s, "agent.summarize", `"reason"`) {
			t.Fatal("expected audit failure reason on invalid JSON")
		}
	})
	t.Run("provider timeout", func(t *testing.T) {
		s, _ := newTestServices(t)
		ws, userID := createWorkspace(t, s)
		doc := favImportDoc(t, s, ws, "timeout.md", "# 超时\n\n内容。")
		s.Agent.LLMFactory = func() (provider.LLMProvider, error) {
			return &stubLLM{err: context.DeadlineExceeded}, nil
		}
		_, err := s.AgentTasks.AgentSummarize(ctx(), AgentSummarizeReq{
			WorkspaceID: ws.ID, UserID: userID, DocumentID: doc.ID, IdempotencyKey: "as-" + NewID(),
		})
		if err == nil {
			t.Fatal("expected error on provider timeout")
		}
		if de, ok := err.(*domain.Error); !ok || de.Code != domain.CodeProviderTimeout {
			t.Fatalf("expected PROVIDER_TIMEOUT, got %v", err)
		}
		if !agentAuditPayloadContains(t, s, "agent.summarize", `"reason"`) {
			t.Fatal("expected audit failure reason on timeout")
		}
	})
}

// ---- ④ QuizGen 正常生成 → 题目草稿落库（draft/source=ai + pending/generated_by_model/prompt_version + knowledge） ----

func TestAgentQuizGenGenerate(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	kid := createKnowledgeNode(t, s, ws.ID, "牛顿力学")

	body := `{"questions":[` +
		`{"type":"single_choice","stem":"牛顿第二定律公式是？","options":[{"key":"A","text":"F=ma"},{"key":"B","text":"F=mv"}],"answer":"A","analysis":"牛顿第二定律","difficulty":2,"knowledge_ids":["KID"]},` +
		`{"type":"single_choice","stem":"速度的国际单位？","options":[{"key":"A","text":"m/s"},{"key":"B","text":"km/h"}],"answer":"A","analysis":"速度单位","difficulty":1,"knowledge_ids":["KID"]}` +
		`]}`
	s.Agent.LLMFactory = func() (provider.LLMProvider, error) {
		return &stubLLM{content: strings.ReplaceAll(body, "KID", kid)}, nil
	}
	res, err := s.AgentTasks.AgentQuizGen(ctx(), AgentQuizGenReq{
		WorkspaceID: ws.ID, UserID: userID, KnowledgeIDs: []string{kid},
		Types: []string{"single_choice"}, Count: 2, IdempotencyKey: "qg-" + NewID(),
	})
	if err != nil {
		t.Fatalf("quizgen generate: %v", err)
	}
	if res.Mode != "generated" {
		t.Fatalf("expected mode generated, got %s", res.Mode)
	}
	if len(res.Questions) != 2 {
		t.Fatalf("expected 2 questions, got %d", len(res.Questions))
	}
	for i, q := range res.Questions {
		if q.Status != "draft" || q.Source != "ai" {
			t.Fatalf("q%d expected draft/ai, got %s/%s", i, q.Status, q.Source)
		}
		if q.CurrentVersion == nil {
			t.Fatalf("q%d missing current version", i)
		}
		if q.CurrentVersion.ReviewStatus != "pending" {
			t.Fatalf("q%d expected review_status pending, got %s", i, q.CurrentVersion.ReviewStatus)
		}
		if q.CurrentVersion.GeneratedByModel == nil || *q.CurrentVersion.GeneratedByModel != "stub" {
			t.Fatalf("q%d expected generated_by_model stub, got %v", i, q.CurrentVersion.GeneratedByModel)
		}
		if q.CurrentVersion.PromptVersion == nil || *q.CurrentVersion.PromptVersion != "1" {
			t.Fatalf("q%d expected prompt_version, got %v", i, q.CurrentVersion.PromptVersion)
		}
	}
	if n := agentRowCount(t, s, `SELECT COUNT(*) FROM questions WHERE status='draft' AND source='ai'`); n != 2 {
		t.Fatalf("expected 2 draft ai questions, got %d", n)
	}
	if n := agentRowCount(t, s, `SELECT COUNT(*) FROM question_versions WHERE review_status='pending'`); n != 2 {
		t.Fatalf("expected 2 pending versions, got %d", n)
	}
	if n := agentRowCount(t, s, `SELECT COUNT(*) FROM question_knowledge`); n != 2 {
		t.Fatalf("expected 2 question_knowledge rows, got %d", n)
	}
	if n := agentAuditCount(t, s, "agent.quizgen"); n < 1 {
		t.Fatalf("expected audit agent.quizgen, got %d", n)
	}
}

// ---- ⑤ QuizGen 题量 / 题型 / 输入校验 ----

func TestAgentQuizGenValidation(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	kid := createKnowledgeNode(t, s, ws.ID, "知识点")
	base := AgentQuizGenReq{WorkspaceID: ws.ID, UserID: userID, KnowledgeIDs: []string{kid}, Count: 3}

	cases := []struct {
		name string
		mut  func(*AgentQuizGenReq)
	}{
		{"count zero", func(r *AgentQuizGenReq) { r.Count = 0 }},
		{"count negative", func(r *AgentQuizGenReq) { r.Count = -1 }},
		{"count too large", func(r *AgentQuizGenReq) { r.Count = 11 }},
		{"bad type", func(r *AgentQuizGenReq) { r.Types = []string{"essay"} }},
		{"no inputs", func(r *AgentQuizGenReq) {
			r.KnowledgeIDs = nil
			r.DocumentIDs = nil
		}},
		{"missing user", func(r *AgentQuizGenReq) { r.UserID = "" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := base
			c.mut(&req)
			_, err := s.AgentTasks.AgentQuizGen(ctx(), req)
			if err == nil {
				t.Fatal("expected INVALID_ARGUMENT, got nil")
			}
			if de, ok := err.(*domain.Error); !ok || de.Code != domain.CodeInvalidArgument {
				t.Fatalf("expected INVALID_ARGUMENT, got %v", err)
			}
		})
	}
}

// ---- ⑥ QuizGen 去重（content_hash 冲突 → 跳过，不 panic）+ 幂等重放 ----

func TestAgentQuizGenDedupAndIdempotent(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	kid := createKnowledgeNode(t, s, ws.ID, "去重")

	// 预置与 LLM 输出 Q1 完全同内容的题目（type+stem+options+answer 相同 → content_hash 相同）
	seedPublishedQuestion(t, s, ws.ID, mustJSON(map[string]any{
		"type": "single_choice", "stem": "重复题目",
		"options": []map[string]any{{"key": "A", "text": "选项A"}, {"key": "B", "text": "选项B"}, {"key": "C", "text": "选项C"}},
		"answer":  "A", "difficulty": 3, "knowledge_ids": []string{kid},
	}))

	body := `{"questions":[` +
		`{"type":"single_choice","stem":"重复题目","options":[{"key":"A","text":"选项A"},{"key":"B","text":"选项B"},{"key":"C","text":"选项C"}],"answer":"A","analysis":"同内容","difficulty":3,"knowledge_ids":["KID"]},` +
		`{"type":"single_choice","stem":"新题目","options":[{"key":"A","text":"A"},{"key":"B","text":"B"}],"answer":"B","analysis":"新内容","difficulty":2,"knowledge_ids":["KID"]}` +
		`]}`
	s.Agent.LLMFactory = func() (provider.LLMProvider, error) {
		return &stubLLM{content: strings.ReplaceAll(body, "KID", kid)}, nil
	}
	key := "qg-" + NewID()
	req := AgentQuizGenReq{WorkspaceID: ws.ID, UserID: userID, KnowledgeIDs: []string{kid},
		Types: []string{"single_choice"}, Count: 2, IdempotencyKey: key}

	first, err := s.AgentTasks.AgentQuizGen(ctx(), req)
	if err != nil {
		t.Fatalf("quizgen dedup: %v", err)
	}
	if first.SkippedCount != 1 {
		t.Fatalf("expected 1 skipped, got %d", first.SkippedCount)
	}
	if len(first.Questions) != 1 || first.Questions[0].CurrentVersion == nil {
		t.Fatalf("expected 1 new question, got %d", len(first.Questions))
	}
	var pl struct {
		Stem string `json:"stem"`
	}
	if err := json.Unmarshal(first.Questions[0].CurrentVersion.Payload, &pl); err != nil || pl.Stem != "新题目" {
		t.Fatalf("expected new question stem 新题目, got %+v err=%v", pl, err)
	}

	// 幂等重放：同 key 返回同批草稿
	replay, err := s.AgentTasks.AgentQuizGen(ctx(), req)
	if err != nil {
		t.Fatalf("quizgen replay: %v", err)
	}
	if replay.SkippedCount != first.SkippedCount || len(replay.Questions) != len(first.Questions) {
		t.Fatalf("idempotency violated: %+v vs %+v", replay, first)
	}
	if replay.Questions[0].ID != first.Questions[0].ID {
		t.Fatalf("idempotency violated: different question id")
	}
}

// ---- ⑦ QuizGen Provider 未配置 → 降级为基于已有题目的筛选推荐，不生成新题 ----

func TestAgentQuizGenDegraded(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	kid := createKnowledgeNode(t, s, ws.ID, "推荐")
	seedPublishedQuestion(t, s, ws.ID, mustJSON(map[string]any{
		"type": "single_choice", "stem": "推荐题目",
		"options": []map[string]any{{"key": "A", "text": "A"}, {"key": "B", "text": "B"}},
		"answer":  "A", "difficulty": 3, "knowledge_ids": []string{kid},
	}))

	res, err := s.AgentTasks.AgentQuizGen(ctx(), AgentQuizGenReq{
		WorkspaceID: ws.ID, UserID: userID, KnowledgeIDs: []string{kid},
		Types: []string{"single_choice"}, Count: 1, IdempotencyKey: "qg-" + NewID(),
	})
	if err != nil {
		t.Fatalf("quizgen degraded: %v", err)
	}
	if res.Mode != "recommended" {
		t.Fatalf("expected mode recommended, got %s", res.Mode)
	}
	if len(res.Questions) != 1 {
		t.Fatalf("expected 1 recommendation, got %d", len(res.Questions))
	}
	if res.Questions[0].Status != "published" || res.Questions[0].Source != "manual" {
		t.Fatalf("expected published manual recommendation, got %s/%s", res.Questions[0].Status, res.Questions[0].Source)
	}
	// 不生成新题
	if n := agentRowCount(t, s, `SELECT COUNT(*) FROM questions`); n != 1 {
		t.Fatalf("expected no new questions, got %d", n)
	}
	if n := agentAuditCount(t, s, "agent.quizgen"); n < 1 {
		t.Fatalf("expected audit agent.quizgen, got %d", n)
	}
}

// ---- Prompt 构造（设计文档 10.9 / 10.10 契约） ----

func TestAgentPromptConstruction(t *testing.T) {
	system, prompt := agent.BuildSummarizePrompt(agent.SummarizeInput{
		Title: "力学", Text: "[c1]正文", Preferences: "重点看公式",
	})
	for _, want := range []string{"key_points", "chunk_id", "800", "glossary", "citation_refs"} {
		if !strings.Contains(system, want) {
			t.Fatalf("summarize system prompt missing %q", want)
		}
	}
	if !strings.Contains(prompt, "力学") || !strings.Contains(prompt, "[c1]正文") || !strings.Contains(prompt, "重点看公式") {
		t.Fatalf("summarize prompt missing input: %q", prompt)
	}

	sys2, prompt2 := agent.BuildQuizGenPrompt(agent.QuizGenInput{
		Types: []string{"single_choice"}, Count: 3, Knowledge: "牛顿力学", Material: "[c1]资料",
	})
	if !strings.Contains(sys2, "questions") || !strings.Contains(sys2, "answer") {
		t.Fatalf("quizgen system prompt missing schema: %q", sys2)
	}
	if !strings.Contains(prompt2, "3") || !strings.Contains(prompt2, "single_choice") ||
		!strings.Contains(prompt2, "牛顿力学") || !strings.Contains(prompt2, "[c1]资料") {
		t.Fatalf("quizgen prompt missing input: %q", prompt2)
	}
}

// ---- ⑧ Debugger prompt 构造（设计文档 10.12） ----

func TestDebugPromptConstruction(t *testing.T) {
	system, prompt := agent.BuildDebugPrompt(agent.DebugInput{
		Language: "python", Code: "print(1/0)", ErrorOutput: "ZeroDivisionError", TestCases: "case1",
	})
	for _, want := range []string{"error_location", "cause", "fix_suggestions", "steps"} {
		if !strings.Contains(system, want) {
			t.Fatalf("debug system prompt missing %q", want)
		}
	}
	if !strings.Contains(prompt, "python") || !strings.Contains(prompt, "print(1/0)") ||
		!strings.Contains(prompt, "ZeroDivisionError") || !strings.Contains(prompt, "case1") {
		t.Fatalf("debug prompt missing input: %q", prompt)
	}
}

// ---- ⑨ EssayGrader prompt 构造（设计文档 10.12） ----

func TestEssayGraderPromptConstruction(t *testing.T) {
	system, prompt := agent.BuildEssayGraderPrompt(agent.EssayGraderInput{
		Stem: "题目", Rubric: "量规", Essay: "作文", MaxScore: 10,
	})
	for _, want := range []string{"dimensions", "content", "structure", "language", "standard", "overall_score", "suggestions"} {
		if !strings.Contains(system, want) {
			t.Fatalf("essay system prompt missing %q", want)
		}
	}
	if !strings.Contains(prompt, "题目") || !strings.Contains(prompt, "量规") ||
		!strings.Contains(prompt, "作文") || !strings.Contains(prompt, "10.0") {
		t.Fatalf("essay prompt missing input: %q", prompt)
	}
}

// ---- ⑩ Debugger LLM 路径：沙箱采集 stderr → 结构化解析 → 审计 ----

func TestAgentDebugLLMPath(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	s.Agent.LLMFactory = func() (provider.LLMProvider, error) {
		return &stubLLM{content: `{"summary":"定位","error_location":"第1行","cause":"除零","steps":[{"title":"步骤","content":"内容"}],"fix_suggestions":[{"description":"改","example":"if b!=0"}]}`}, nil
	}
	s.AgentTasks.sbx = &stubSandbox{res: &sandbox.Result{ExitCode: 1, Stderr: "ZeroDivisionError\n"}}
	res, err := s.AgentTasks.AgentDebug(ctx(), AgentDebugReq{
		WorkspaceID: ws.ID, UserID: userID, Language: "python", Code: "print(1/0)",
		ErrorOutput: "", TestCases: "1/0", IdempotencyKey: "ad-" + NewID(),
	})
	if err != nil {
		t.Fatalf("debug llm: %v", err)
	}
	if res.Degraded {
		t.Fatal("expected not degraded on llm path")
	}
	if !res.SandboxUsed {
		t.Fatal("expected sandbox used")
	}
	if res.Result == nil || res.Result.ErrorLocation != "第1行" || len(res.Result.FixSuggestions) != 1 {
		t.Fatalf("unexpected debug result: %+v", res.Result)
	}
	if n := agentAuditCount(t, s, "agent.debug"); n < 1 {
		t.Fatalf("expected audit agent.debug, got %d", n)
	}
}

// ---- ⑪ Debugger 沙箱失败 → SANDBOX_LIMIT 明确错误 + 审计失败原因 ----

func TestAgentDebugSandboxFailure(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	s.AgentTasks.sbx = &stubSandbox{err: &sandbox.LimitError{Kind: sandbox.KindTimeout, Message: "timed out"}}
	_, err := s.AgentTasks.AgentDebug(ctx(), AgentDebugReq{
		WorkspaceID: ws.ID, UserID: userID, Language: "python", Code: "while True: pass",
		IdempotencyKey: "ad-" + NewID(),
	})
	if err == nil {
		t.Fatal("expected SANDBOX_LIMIT error")
	}
	if de, ok := err.(*domain.Error); !ok || de.Code != domain.CodeSandboxLimit {
		t.Fatalf("expected SANDBOX_LIMIT, got %v", err)
	}
	if !agentAuditPayloadContains(t, s, "agent.debug", `"reason"`) {
		t.Fatal("expected audit failure reason on sandbox error")
	}
}

// ---- ⑫ Debugger Provider 未配置 → 确定性降级模板 + degraded 标志 + 审计 ----

func TestAgentDebugDegraded(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	s.AgentTasks.sbx = &stubSandbox{res: &sandbox.Result{ExitCode: 1, Stderr: "boom\n"}}
	res, err := s.AgentTasks.AgentDebug(ctx(), AgentDebugReq{
		WorkspaceID: ws.ID, UserID: userID, Language: "python", Code: "x",
		IdempotencyKey: "ad-" + NewID(),
	})
	if err != nil {
		t.Fatalf("debug degraded: %v", err)
	}
	if !res.Degraded {
		t.Fatal("expected degraded flag")
	}
	if res.Result == nil {
		t.Fatal("expected template result on degraded")
	}
	if !agentAuditPayloadContains(t, s, "agent.debug", "provider") {
		t.Fatal("expected degraded audit reason")
	}
}

// ---- ⑬ Debugger 校验矩阵 + 幂等重放 ----

func TestAgentDebugValidationAndIdempotent(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	base := AgentDebugReq{WorkspaceID: ws.ID, UserID: userID, Language: "python", Code: "x"}
	cases := []struct {
		name string
		mut  func(*AgentDebugReq)
	}{
		{"missing user", func(r *AgentDebugReq) { r.UserID = "" }},
		{"missing lang", func(r *AgentDebugReq) { r.Language = "" }},
		{"missing code", func(r *AgentDebugReq) { r.Code = "" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := base
			c.mut(&req)
			_, err := s.AgentTasks.AgentDebug(ctx(), req)
			if err == nil {
				t.Fatal("expected INVALID_ARGUMENT, got nil")
			}
			if de, ok := err.(*domain.Error); !ok || de.Code != domain.CodeInvalidArgument {
				t.Fatalf("expected INVALID_ARGUMENT, got %v", err)
			}
		})
	}
	// 幂等重放：同 key 返回同结果
	s.Agent.LLMFactory = func() (provider.LLMProvider, error) {
		return &stubLLM{content: `{"summary":"s","error_location":"l","cause":"c","steps":[],"fix_suggestions":[]}`}, nil
	}
	s.AgentTasks.sbx = &stubSandbox{res: &sandbox.Result{ExitCode: 0}}
	key := "ad-" + NewID()
	req := AgentDebugReq{WorkspaceID: ws.ID, UserID: userID, Language: "python", Code: "x", IdempotencyKey: key}
	first, err := s.AgentTasks.AgentDebug(ctx(), req)
	if err != nil {
		t.Fatalf("debug first: %v", err)
	}
	replay, err := s.AgentTasks.AgentDebug(ctx(), req)
	if err != nil {
		t.Fatalf("debug replay: %v", err)
	}
	if first.Result == nil || replay.Result == nil || replay.Result.ErrorLocation != first.Result.ErrorLocation {
		t.Fatalf("idempotency violated: %+v vs %+v", replay.Result, first.Result)
	}
}

// ---- ⑭ EssayGrader LLM 路径：维度评分校验 ≤ max_score + 审计 ----

func TestAgentEssayGradeLLMPath(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	s.Agent.LLMFactory = func() (provider.LLMProvider, error) {
		return &stubLLM{content: `{"dimensions":{"content":4,"structure":3,"language":2,"standard":1},"overall_score":3.5,"comment":"总评","suggestions":["多分段"]}`}, nil
	}
	res, err := s.AgentTasks.AgentEssayGrade(ctx(), AgentEssayGradeReq{
		WorkspaceID: ws.ID, UserID: userID, Stem: "题", Rubric: "量规", Essay: "作", MaxScore: 5,
		IdempotencyKey: "eg-" + NewID(),
	})
	if err != nil {
		t.Fatalf("essaygrade llm: %v", err)
	}
	if res.Degraded {
		t.Fatal("expected not degraded")
	}
	if res.Result == nil || res.Result.OverallScore != 3.5 || res.Result.Dimensions.Content != 4 {
		t.Fatalf("unexpected result: %+v", res.Result)
	}
	if n := agentAuditCount(t, s, "agent.essaygrade"); n < 1 {
		t.Fatalf("expected audit agent.essaygrade, got %d", n)
	}
}

// ---- ⑮ EssayGrader 越界得分 → OUTPUT_INVALID + 审计失败原因 ----

func TestAgentEssayGradeOutOfRange(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	s.Agent.LLMFactory = func() (provider.LLMProvider, error) {
		return &stubLLM{content: `{"dimensions":{"content":4,"structure":3,"language":2,"standard":1},"overall_score":999,"comment":"c","suggestions":[]}`}, nil
	}
	_, err := s.AgentTasks.AgentEssayGrade(ctx(), AgentEssayGradeReq{
		WorkspaceID: ws.ID, UserID: userID, Stem: "题", Essay: "作", MaxScore: 5, IdempotencyKey: "eg-" + NewID(),
	})
	if err == nil {
		t.Fatal("expected error on out-of-range score")
	}
	if de, ok := err.(*domain.Error); !ok || de.Code != domain.CodeOutputInvalid {
		t.Fatalf("expected OUTPUT_INVALID, got %v", err)
	}
	if !agentAuditPayloadContains(t, s, "agent.essaygrade", `"reason"`) {
		t.Fatal("expected audit failure reason")
	}
}

// ---- ⑯ EssayGrader Provider 未配置 → 确定性降级 + degraded 标志 + 审计 ----

func TestAgentEssayGradeDegraded(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	res, err := s.AgentTasks.AgentEssayGrade(ctx(), AgentEssayGradeReq{
		WorkspaceID: ws.ID, UserID: userID, Stem: "题", Rubric: "量规", Essay: "作", MaxScore: 10,
		IdempotencyKey: "eg-" + NewID(),
	})
	if err != nil {
		t.Fatalf("essaygrade degraded: %v", err)
	}
	if !res.Degraded {
		t.Fatal("expected degraded flag")
	}
	if res.Result == nil {
		t.Fatal("expected template result")
	}
	if !agentAuditPayloadContains(t, s, "agent.essaygrade", "provider") {
		t.Fatal("expected degraded audit reason")
	}
}

// ---- ⑰ EssayGrader 校验矩阵 + 幂等重放 ----

func TestAgentEssayGradeValidationAndIdempotent(t *testing.T) {
	s, _ := newTestServices(t)
	ws, userID := createWorkspace(t, s)
	base := AgentEssayGradeReq{WorkspaceID: ws.ID, UserID: userID, Stem: "题", Essay: "作", MaxScore: 10}
	cases := []struct {
		name string
		mut  func(*AgentEssayGradeReq)
	}{
		{"missing user", func(r *AgentEssayGradeReq) { r.UserID = "" }},
		{"missing essay", func(r *AgentEssayGradeReq) { r.Essay = "" }},
		{"bad max score", func(r *AgentEssayGradeReq) { r.MaxScore = 0 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := base
			c.mut(&req)
			_, err := s.AgentTasks.AgentEssayGrade(ctx(), req)
			if err == nil {
				t.Fatal("expected INVALID_ARGUMENT, got nil")
			}
			if de, ok := err.(*domain.Error); !ok || de.Code != domain.CodeInvalidArgument {
				t.Fatalf("expected INVALID_ARGUMENT, got %v", err)
			}
		})
	}
	s.Agent.LLMFactory = func() (provider.LLMProvider, error) {
		return &stubLLM{content: `{"dimensions":{"content":1,"structure":1,"language":1,"standard":1},"overall_score":1,"comment":"c","suggestions":["s"]}`}, nil
	}
	key := "eg-" + NewID()
	req := AgentEssayGradeReq{WorkspaceID: ws.ID, UserID: userID, Stem: "题", Essay: "作", MaxScore: 10, IdempotencyKey: key}
	first, err := s.AgentTasks.AgentEssayGrade(ctx(), req)
	if err != nil {
		t.Fatalf("essaygrade first: %v", err)
	}
	replay, err := s.AgentTasks.AgentEssayGrade(ctx(), req)
	if err != nil {
		t.Fatalf("essaygrade replay: %v", err)
	}
	if first.Result == nil || replay.Result == nil || replay.Result.OverallScore != first.Result.OverallScore {
		t.Fatalf("idempotency violated: %+v vs %+v", replay.Result, first.Result)
	}
}

// ---- 辅助：沙箱桩（确定性执行结果/错误） ----

type stubSandbox struct {
	res *sandbox.Result
	err error
}

func (s *stubSandbox) Run(ctx context.Context, spec sandbox.Spec) (*sandbox.Result, error) {
	return s.res, s.err
}
