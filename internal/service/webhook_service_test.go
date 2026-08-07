package service

// Webhook 服务 TDD（API 设计文档 7.13 / 完整设计文档 4.24 / Todo 31）。
// 覆盖：订阅校验与幂等、HMAC-SHA256 签名、退避调度表、重试→死信、Dispatch 事件过滤、
// 删除语义、投递状态迁移、测试钩子。

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"lumo/internal/agent"
	"lumo/internal/domain"
	"lumo/internal/repository"
)

// webhookFixedNow 固定时钟（测试推进退避判定）。
var webhookFixedNow = time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)

// capturedWebhook 记录桩服务器收到的最后一次 webhook POST。
type capturedWebhook struct {
	mu            sync.Mutex
	count         int
	body          []byte
	signature     string
	authorization string
	status        int
}

// webhookStub 启动一个 httptest 桩服务器：记录请求并返回固定状态码。
func webhookStub(t *testing.T, status int) (*httptest.Server, *capturedWebhook) {
	t.Helper()
	c := &capturedWebhook{status: status}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.count++
		c.body = body
		c.signature = r.Header.Get("X-Lumo-Signature")
		c.authorization = r.Header.Get("Authorization")
		c.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

// webhookRequests 返回桩服务器累计收到的请求数。
func (c *capturedWebhook) requests() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

// setupWebhookServices 构造服务并注入固定时钟；返回 (s, cfg, ws, 首个默认用户)。
func setupWebhookServices(t *testing.T) (*Services, string, string) {
	t.Helper()
	s, _ := newTestServices(t)
	s.Webhooks.Now = func() time.Time { return webhookFixedNow }
	ws, userID := createWorkspace(t, s)
	return s, ws.ID, userID
}

// subscribeWebhook 便捷：注册一个订阅（默认 enabled，可带 secret_ref）。
func subscribeWebhook(t *testing.T, s *Services, wsID, url string, events []string, secretRef *string, key string) *WebhookSubscription {
	t.Helper()
	sub, err := s.Webhooks.WebhookSubscribe(ctx(), WebhookSubscribeReq{
		WorkspaceID: wsID, URL: url, EventTypes: events, SecretRef: secretRef, IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("subscribe webhook: %v", err)
	}
	return sub
}

// ---- ① 订阅 happy：url/event_types 落库、返回 DTO、secret_ref 仅存键名 ----

func TestWebhookSubscribeHappy(t *testing.T) {
	s, wsID, _ := setupWebhookServices(t)
	sec := "webhook_test_secret"
	if err := writeSecretsFile(s.Cfg.SecretsPath, map[string]string{sec: "s3cr3t-value"}); err != nil {
		t.Fatal(err)
	}

	sub := subscribeWebhook(t, s, wsID, "  https://example.com/hook  ", []string{"report:ready", "grading:updated"}, strPtr(sec), "wh-sub-happy-01")

	if sub.ID == "" || sub.WorkspaceID != wsID {
		t.Fatalf("unexpected subscription DTO: %+v", sub)
	}
	if sub.URL != "https://example.com/hook" {
		t.Errorf("url 应被 trim，got %q", sub.URL)
	}
	if len(sub.EventTypes) != 2 {
		t.Fatalf("event_types 应落库 2 项，got %v", sub.EventTypes)
	}
	if sub.SecretRef == nil || *sub.SecretRef != sec {
		t.Fatalf("secret_ref 应仅存键名，got %+v", sub.SecretRef)
	}
	if !sub.Enabled || sub.CreatedAt == "" || sub.UpdatedAt == "" {
		t.Fatalf("订阅应默认启用且带时间戳: %+v", sub)
	}

	// 落库行：event_types JSON、secret_ref 只有键名（不含密钥值）。
	row, err := s.Repo.GetWebhookSubscriptionByID(ctx(), sub.ID)
	if err != nil || row == nil {
		t.Fatalf("get subscription row: %v", err)
	}
	if strings.Contains(row.EventTypesJSON, "s3cr3t") {
		t.Errorf("密钥值不得落库")
	}
	if !strings.Contains(row.EventTypesJSON, "report:ready") || !strings.Contains(row.EventTypesJSON, "grading:updated") {
		t.Errorf("event_types JSON 异常: %s", row.EventTypesJSON)
	}
}

// ---- ② 订阅校验：非法 url / 白名单外事件 / 空事件 / 缺失密钥 → INVALID_ARGUMENT ----

func TestWebhookSubscribeValidation(t *testing.T) {
	s, wsID, _ := setupWebhookServices(t)
	longURL := "https://example.com/" + strings.Repeat("a", 2100)

	cases := []struct {
		name   string
		url    string
		events []string
		secret *string
	}{
		{"非 URL", "notaurl", []string{"report:ready"}, nil},
		{"非法协议", "ftp://example.com/hook", []string{"report:ready"}, nil},
		{"空 URL", "", []string{"report:ready"}, nil},
		{"超长 URL", longURL, []string{"report:ready"}, nil},
		{"白名单外事件", "https://example.com/hook", []string{"bogus:event"}, nil},
		{"空事件列表", "https://example.com/hook", nil, nil},
		{"secret 键不存在", "https://example.com/hook", []string{"report:ready"}, strPtr("no_such_key")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := s.Webhooks.WebhookSubscribe(ctx(), WebhookSubscribeReq{
				WorkspaceID: wsID, URL: c.url, EventTypes: c.events, SecretRef: c.secret,
				IdempotencyKey: "wh-sub-invalid-01",
			})
			if err == nil || domain.AsError(err).Code != domain.CodeInvalidArgument {
				t.Fatalf("应返回 INVALID_ARGUMENT，got %v", err)
			}
		})
	}
}

// ---- ③ 订阅幂等：同 key 重放返回同一订阅，不重复落库 ----

func TestWebhookSubscribeIdempotent(t *testing.T) {
	s, wsID, _ := setupWebhookServices(t)

	sub1 := subscribeWebhook(t, s, wsID, "https://example.com/hook", []string{"flashcard:due"}, nil, "wh-sub-idem-0001")
	sub2 := subscribeWebhook(t, s, wsID, "https://example.com/hook", []string{"flashcard:due"}, nil, "wh-sub-idem-0001")

	if sub1.ID != sub2.ID || sub2.URL != sub1.URL {
		t.Fatalf("幂等重放应返回同一订阅: %+v vs %+v", sub1, sub2)
	}
	rows, err := s.Repo.ListWebhookSubscriptionsByWorkspace(ctx(), wsID)
	if err != nil {
		t.Fatalf("list subscriptions: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("同 key 重放不应新增订阅，got %d rows", len(rows))
	}
}

// ---- ④ 测试发送 happy：桩返回 200 → ok=true + status_code ----

func TestWebhookTestSendHappy(t *testing.T) {
	s, wsID, _ := setupWebhookServices(t)
	srv, _ := webhookStub(t, http.StatusOK)
	sub := subscribeWebhook(t, s, wsID, srv.URL, []string{"report:ready"}, nil, "wh-sub-test-0001")

	res, err := s.Webhooks.WebhookTestSend(ctx(), WebhookTestSendReq{WorkspaceID: wsID, SubscriptionID: sub.ID})
	if err != nil {
		t.Fatalf("test send: %v", err)
	}
	if !res.OK || res.StatusCode != http.StatusOK {
		t.Fatalf("test send 应成功，got %+v", res)
	}
	if res.Error != "" {
		t.Errorf("成功时 error 应为空，got %q", res.Error)
	}
	// 测试钩子不创建投递记录（不进重试队列）。
	dels, _ := s.Repo.ListWebhookDeliveriesBySubscription(ctx(), sub.ID)
	if len(dels) != 0 {
		t.Errorf("测试发送不应落库投递记录，got %d", len(dels))
	}
}

// ---- ⑤ 测试发送失败：500/不可达 → ok=false + error，不进重试队列 ----

func TestWebhookTestSendFailure(t *testing.T) {
	s, wsID, _ := setupWebhookServices(t)
	srv, _ := webhookStub(t, http.StatusInternalServerError)
	sub := subscribeWebhook(t, s, wsID, srv.URL, []string{"report:ready"}, nil, "wh-sub-test-0002")

	res, err := s.Webhooks.WebhookTestSend(ctx(), WebhookTestSendReq{WorkspaceID: wsID, SubscriptionID: sub.ID})
	if err != nil {
		t.Fatalf("test send 500 不应是服务错误: %v", err)
	}
	if res.OK || res.StatusCode != http.StatusInternalServerError || res.Error == "" {
		t.Fatalf("test send 应报告失败，got %+v", res)
	}
	dels, _ := s.Repo.ListWebhookDeliveriesBySubscription(ctx(), sub.ID)
	if len(dels) != 0 {
		t.Errorf("测试发送失败也不应进入重试队列，got %d", len(dels))
	}

	// 目标不可达（连接关闭的服务器地址）。
	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := closed.URL
	closed.Close()
	sub2 := subscribeWebhook(t, s, wsID, closedURL, []string{"report:ready"}, nil, "wh-sub-test-0003")
	res2, err := s.Webhooks.WebhookTestSend(ctx(), WebhookTestSendReq{WorkspaceID: wsID, SubscriptionID: sub2.ID})
	if err != nil {
		t.Fatalf("test send 不可达不应是服务错误: %v", err)
	}
	if res2.OK || res2.Error == "" {
		t.Fatalf("test send 应报告不可达，got %+v", res2)
	}
}

// ---- ⑥ 签名正确性（核心）：X-Lumo-Signature = sha256=HMAC(body, secret) ----

func TestWebhookDispatchSignature(t *testing.T) {
	s, wsID, _ := setupWebhookServices(t)
	sec := "webhook_sign_key"
	if err := writeSecretsFile(s.Cfg.SecretsPath, map[string]string{sec: "signing-secret-abc"}); err != nil {
		t.Fatal(err)
	}
	srv, captured := webhookStub(t, http.StatusOK)
	sub := subscribeWebhook(t, s, wsID, srv.URL, []string{"grading:updated"}, strPtr(sec), "wh-sub-sig-0001")

	if err := s.Webhooks.Dispatch(ctx(), wsID, "grading:updated", map[string]any{"grading_id": "g1", "status": "completed", "score": 90}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	if captured.requests() != 1 {
		t.Fatalf("桩应收到 1 次 POST，got %d", captured.requests())
	}
	// body 结构：event_type + payload + occurred_at。
	var body struct {
		EventType  string         `json:"event_type"`
		Payload    map[string]any `json:"payload"`
		OccurredAt string         `json:"occurred_at"`
	}
	if err := json.Unmarshal(captured.body, &body); err != nil {
		t.Fatalf("body 非合法 JSON: %v (%s)", err, captured.body)
	}
	if body.EventType != "grading:updated" || body.OccurredAt == "" {
		t.Fatalf("body 结构异常: %+v", body)
	}
	if body.Payload["grading_id"] != "g1" {
		t.Fatalf("payload 丢失: %+v", body.Payload)
	}

	// 签名 = sha256=<HMAC-SHA256(body 原始字节, secret)>。
	mac := hmac.New(sha256.New, []byte("signing-secret-abc"))
	mac.Write(captured.body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if captured.signature != expected {
		t.Errorf("X-Lumo-Signature 不符:\n got  %s\n want %s", captured.signature, expected)
	}
	if captured.authorization != "Bearer signing-secret-abc" {
		t.Errorf("Authorization 头不符: %q", captured.authorization)
	}

	// 投递行应为 sent。
	dels, _ := s.Repo.ListWebhookDeliveriesBySubscription(ctx(), sub.ID)
	if len(dels) != 1 || dels[0].Status != "sent" || dels[0].Attempt != 1 {
		t.Fatalf("投递行应 sent/attempt=1，got %+v", dels)
	}
}

// ---- ⑦ 退避调度表钉死：attempt 1→30s、2→2m、3→10m、4→30m ----

func TestWebhookBackoffSchedule(t *testing.T) {
	if webhookBackoffFor(1) != 30*time.Second {
		t.Errorf("backoffFor(1) 应 30s，got %v", webhookBackoffFor(1))
	}
	if webhookBackoffFor(2) != 2*time.Minute {
		t.Errorf("backoffFor(2) 应 2m，got %v", webhookBackoffFor(2))
	}
	if webhookBackoffFor(3) != 10*time.Minute {
		t.Errorf("backoffFor(3) 应 10m，got %v", webhookBackoffFor(3))
	}
	if webhookBackoffFor(4) != 30*time.Minute {
		t.Errorf("backoffFor(4) 应 30m，got %v", webhookBackoffFor(4))
	}
	if webhookBackoffFor(5) != 0 {
		t.Errorf("backoffFor(5) 不应有退避（死信），got %v", webhookBackoffFor(5))
	}

	// 投递行 next_retry_at 精确断言。
	s, wsID, _ := setupWebhookServices(t)
	srv, _ := webhookStub(t, http.StatusInternalServerError)
	sub := subscribeWebhook(t, s, wsID, srv.URL, []string{"report:ready"}, nil, "wh-sub-back-0001")
	if err := s.Webhooks.Dispatch(ctx(), wsID, "report:ready", map[string]any{"report_id": "r1"}); err != nil {
		t.Fatal(err)
	}
	wantFirst := webhookFixedNow.Add(30 * time.Second).UTC().Format(time.RFC3339)
	dels, _ := s.Repo.ListWebhookDeliveriesBySubscription(ctx(), sub.ID)
	if len(dels) != 1 || dels[0].Status != "pending_retry" || dels[0].Attempt != 1 {
		t.Fatalf("首次失败应 pending_retry/attempt=1，got %+v", dels)
	}
	if dels[0].NextRetryAt == nil || *dels[0].NextRetryAt != wantFirst {
		t.Errorf("next_retry_at 应精确为 now+30s=%s，got %+v", wantFirst, dels[0].NextRetryAt)
	}
}

// ---- ⑧ 重试→死信：桩持续 500 → RunOnce 推进 attempt → 第 5 次 → dead 不再投递 ----

func TestWebhookRetryThenDead(t *testing.T) {
	s, wsID, _ := setupWebhookServices(t)
	srv, captured := webhookStub(t, http.StatusInternalServerError)
	sub := subscribeWebhook(t, s, wsID, srv.URL, []string{"report:ready"}, nil, "wh-sub-dead-0001")

	if err := s.Webhooks.Dispatch(ctx(), wsID, "report:ready", map[string]any{"report_id": "r1"}); err != nil {
		t.Fatal(err)
	}
	if captured.requests() != 1 {
		t.Fatalf("初始投递应发生 1 次，got %d", captured.requests())
	}

	// 每轮把时钟推进到该投递的 next_retry_at，RunOnce 重投（退避 30s/2m/10m/30m）。
	for i := 0; i < 5; i++ {
		dels, _ := s.Repo.ListWebhookDeliveriesBySubscription(ctx(), sub.ID)
		if len(dels) != 1 || dels[0].NextRetryAt == nil {
			break // 已收敛（dead），无需再推进
		}
		next, err := time.Parse(time.RFC3339, *dels[0].NextRetryAt)
		if err != nil {
			t.Fatalf("parse next_retry_at: %v", err)
		}
		s.Webhooks.Now = func() time.Time { return next }
		if err := s.Webhooks.RunOnce(ctx()); err != nil {
			t.Fatalf("RunOnce #%d: %v", i+1, err)
		}
	}

	if captured.requests() != 5 {
		t.Fatalf("应累计投递 5 次（初始 1 + 重试 4），got %d", captured.requests())
	}
	dels, _ := s.Repo.ListWebhookDeliveriesBySubscription(ctx(), sub.ID)
	if len(dels) != 1 || dels[0].Status != "dead" {
		t.Fatalf("attempt 5 次后应死信，got %+v", dels)
	}
	if dels[0].Attempt != 5 {
		t.Errorf("死信投递 attempt 应为 5，got %d", dels[0].Attempt)
	}
	if dels[0].NextRetryAt != nil {
		t.Errorf("死信投递不应再有 next_retry_at，got %+v", dels[0].NextRetryAt)
	}
	if dels[0].LastError == nil || *dels[0].LastError == "" {
		t.Errorf("死信投递应记录 last_error")
	}

	// 死信后不再投递。
	if err := s.Webhooks.RunOnce(ctx()); err != nil {
		t.Fatal(err)
	}
	if captured.requests() != 5 {
		t.Errorf("死信投递不应再投递，got %d requests", captured.requests())
	}
}

// ---- ⑨ 重试扫描：到期行重投、未到期行跳过（RunOnce 直接验证） ----

func TestWebhookRetryScan(t *testing.T) {
	s, wsID, _ := setupWebhookServices(t)
	srv, captured := webhookStub(t, http.StatusInternalServerError)
	_ = subscribeWebhook(t, s, wsID, srv.URL, []string{"report:ready"}, nil, "wh-sub-scan-0001")

	if err := s.Webhooks.Dispatch(ctx(), wsID, "report:ready", map[string]any{"report_id": "r1"}); err != nil {
		t.Fatal(err)
	}

	// 未到期：next_retry_at = +30s，此时 +10s 应跳过。
	s.Webhooks.Now = func() time.Time { return webhookFixedNow.Add(10 * time.Second) }
	if err := s.Webhooks.RunOnce(ctx()); err != nil {
		t.Fatal(err)
	}
	if captured.requests() != 1 {
		t.Errorf("未到期行不应重投，got %d requests", captured.requests())
	}

	// 到期：+31s 应重投。
	s.Webhooks.Now = func() time.Time { return webhookFixedNow.Add(31 * time.Second) }
	if err := s.Webhooks.RunOnce(ctx()); err != nil {
		t.Fatal(err)
	}
	if captured.requests() != 2 {
		t.Errorf("到期行应重投，got %d requests", captured.requests())
	}
}

// ---- ⑩ Dispatch 事件过滤：未订阅事件不投递、订阅事件投递、禁用订阅不投递 ----

func TestWebhookDispatchEventFilter(t *testing.T) {
	s, wsID, _ := setupWebhookServices(t)
	srv, captured := webhookStub(t, http.StatusOK)
	sub := subscribeWebhook(t, s, wsID, srv.URL, []string{"grading:updated"}, nil, "wh-sub-filter-01")

	// 触发其他事件 → 不投递。
	if err := s.Webhooks.Dispatch(ctx(), wsID, "report:ready", map[string]any{"report_id": "r1"}); err != nil {
		t.Fatal(err)
	}
	if captured.requests() != 0 {
		t.Fatalf("未订阅事件不应投递，got %d", captured.requests())
	}
	dels, _ := s.Repo.ListWebhookDeliveriesBySubscription(ctx(), sub.ID)
	if len(dels) != 0 {
		t.Fatalf("未订阅事件不应产生投递记录，got %d", len(dels))
	}

	// 订阅的事件 → 投递。
	if err := s.Webhooks.Dispatch(ctx(), wsID, "grading:updated", map[string]any{"grading_id": "g2"}); err != nil {
		t.Fatal(err)
	}
	if captured.requests() != 1 {
		t.Fatalf("订阅事件应投递，got %d", captured.requests())
	}
	dels, _ = s.Repo.ListWebhookDeliveriesBySubscription(ctx(), sub.ID)
	if len(dels) != 1 || dels[0].Status != "sent" {
		t.Fatalf("投递记录应为 sent，got %+v", dels)
	}

	// 禁用订阅（enabled=0 直插）→ 不投递。
	disabledSub := subscribeWebhook(t, s, wsID, srv.URL, []string{"grading:updated"}, nil, "wh-sub-filter-02")
	if err := s.Repo.UpdateWebhookSubscriptionEnabled(ctx(), disabledSub.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Webhooks.Dispatch(ctx(), wsID, "grading:updated", map[string]any{"grading_id": "g3"}); err != nil {
		t.Fatal(err)
	}
	if captured.requests() != 2 {
		t.Fatalf("禁用订阅不应投递，got %d", captured.requests())
	}
}

// ---- ⑪ 删除：happy / NOT_FOUND / 存在进行中投递时 CONFLICT ----

func TestWebhookDelete(t *testing.T) {
	s, wsID, _ := setupWebhookServices(t)

	// happy：删除成功返回 DeleteResult。
	sub := subscribeWebhook(t, s, wsID, "https://example.com/hook", []string{"report:ready"}, nil, "wh-sub-del-0001")
	res, err := s.Webhooks.WebhookDelete(ctx(), WebhookDeleteReq{WorkspaceID: wsID, SubscriptionID: sub.ID})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !res.Deleted || res.DeletedAt == "" {
		t.Fatalf("delete 应返回 deleted，got %+v", res)
	}
	if row, _ := s.Repo.GetWebhookSubscriptionByID(ctx(), sub.ID); row != nil {
		t.Fatalf("订阅应已删除")
	}

	// NOT_FOUND：重复删除 / 随机 ID。
	if _, err := s.Webhooks.WebhookDelete(ctx(), WebhookDeleteReq{WorkspaceID: wsID, SubscriptionID: sub.ID}); err == nil ||
		domain.AsError(err).Code != domain.CodeNotFound {
		t.Fatalf("重复删除应 NOT_FOUND，got %v", err)
	}
	if _, err := s.Webhooks.WebhookDelete(ctx(), WebhookDeleteReq{WorkspaceID: wsID, SubscriptionID: "no-such-sub"}); err == nil ||
		domain.AsError(err).Code != domain.CodeNotFound {
		t.Fatalf("随机 ID 删除应 NOT_FOUND，got %v", err)
	}

	// 跨工作区访问 → NOT_FOUND（防存在性泄露）。
	s2, ws2ID, _ := setupWebhookServices(t)
	sub2 := subscribeWebhook(t, s2, ws2ID, "https://example.com/hook", []string{"report:ready"}, nil, "wh-sub-del-0002")
	if _, err := s.Webhooks.WebhookDelete(ctx(), WebhookDeleteReq{WorkspaceID: wsID, SubscriptionID: sub2.ID}); err == nil ||
		domain.AsError(err).Code != domain.CodeNotFound {
		t.Fatalf("跨工作区删除应 NOT_FOUND，got %v", err)
	}

	// 存在进行中投递（pending）→ CONFLICT，订阅保留。
	inFlight := subscribeWebhook(t, s, wsID, "https://example.com/hook", []string{"report:ready"}, nil, "wh-sub-del-0003")
	if err := s.Repo.CreateWebhookDelivery(ctx(), &repository.WebhookDeliveryRow{ID: NewID(), SubscriptionID: inFlight.ID, EventID: NewID(), Status: "pending", Attempt: 0}); err != nil {
		t.Fatalf("create in-flight delivery: %v", err)
	}
	if _, err := s.Webhooks.WebhookDelete(ctx(), WebhookDeleteReq{WorkspaceID: wsID, SubscriptionID: inFlight.ID}); err == nil ||
		domain.AsError(err).Code != domain.CodeConflict {
		t.Fatalf("存在进行中投递应 CONFLICT，got %v", err)
	}
	if row, _ := s.Repo.GetWebhookSubscriptionByID(ctx(), inFlight.ID); row == nil {
		t.Fatalf("CONFLICT 后订阅应保留")
	}
}

// ---- ⑫ 投递状态迁移：pending→sent / pending→pending_retry→dead 全路径 ----

func TestWebhookDeliveryStatusTransitions(t *testing.T) {
	// 2xx → sent。
	s, wsID, _ := setupWebhookServices(t)
	srvOK, _ := webhookStub(t, http.StatusOK)
	subOK := subscribeWebhook(t, s, wsID, srvOK.URL, []string{"report:ready"}, nil, "wh-sub-trans-01")
	if err := s.Webhooks.Dispatch(ctx(), wsID, "report:ready", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	dels, _ := s.Repo.ListWebhookDeliveriesBySubscription(ctx(), subOK.ID)
	if len(dels) != 1 || dels[0].Status != "sent" || dels[0].Attempt != 1 {
		t.Fatalf("2xx 应 sent/attempt=1，got %+v", dels)
	}

	// 500 → pending_retry → RunOnce 推进 → 第 5 次 → dead。
	srvErr, _ := webhookStub(t, http.StatusInternalServerError)
	subErr := subscribeWebhook(t, s, wsID, srvErr.URL, []string{"report:ready"}, nil, "wh-sub-trans-02")
	if err := s.Webhooks.Dispatch(ctx(), wsID, "report:ready", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	dels, _ = s.Repo.ListWebhookDeliveriesBySubscription(ctx(), subErr.ID)
	if len(dels) != 1 || dels[0].Status != "pending_retry" {
		t.Fatalf("500 应 pending_retry，got %+v", dels)
	}
	for i := 0; i < 4; i++ {
		dels, _ := s.Repo.ListWebhookDeliveriesBySubscription(ctx(), subErr.ID)
		if len(dels) != 1 || dels[0].NextRetryAt == nil {
			break
		}
		next, err := time.Parse(time.RFC3339, *dels[0].NextRetryAt)
		if err != nil {
			t.Fatalf("parse next_retry_at: %v", err)
		}
		s.Webhooks.Now = func() time.Time { return next }
		if err := s.Webhooks.RunOnce(ctx()); err != nil {
			t.Fatal(err)
		}
	}
	dels, _ = s.Repo.ListWebhookDeliveriesBySubscription(ctx(), subErr.ID)
	if len(dels) != 1 || dels[0].Status != "dead" || dels[0].Attempt != 5 {
		t.Fatalf("连续失败应 dead/attempt=5，got %+v", dels)
	}
}

// ---- ⑬ WebhookList 便捷端点：按工作区列出订阅 ----

func TestWebhookList(t *testing.T) {
	s, wsID, _ := setupWebhookServices(t)
	subscribeWebhook(t, s, wsID, "https://a.example/hook", []string{"report:ready"}, nil, "wh-sub-list-0001")
	subscribeWebhook(t, s, wsID, "https://b.example/hook", []string{"grading:updated"}, nil, "wh-sub-list-0002")

	subs, err := s.Webhooks.WebhookList(ctx(), WebhookListReq{WorkspaceID: wsID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(subs) != 2 {
		t.Fatalf("应列出 2 条订阅，got %d", len(subs))
	}
	if !subs[0].Enabled {
		t.Errorf("订阅应默认启用")
	}
}

// ---- ⑭ 题目事件白名单：question:published / question:changed 注册 + 订阅接受 + 投递 ----

func TestWebhookQuestionEvents(t *testing.T) {
	s, wsID, _ := setupWebhookServices(t)

	// IsRegisteredUserEvent 对新事件返回 true（WebhookSubscribe 白名单自动扩展）。
	for _, ev := range []string{"question:published", "question:changed"} {
		if !agent.IsRegisteredUserEvent(ev) {
			t.Fatalf("%s 应注册", ev)
		}
	}

	// WebhookSubscribe 接受新事件。
	srv, captured := webhookStub(t, http.StatusOK)
	sub := subscribeWebhook(t, s, wsID, srv.URL, []string{"question:published", "question:changed"}, nil, "wh-sub-q-0001")
	if len(sub.EventTypes) != 2 {
		t.Fatalf("订阅事件应含 2 个新事件，got %v", sub.EventTypes)
	}

	// Dispatch 产生投递：两条事件各投递一次。
	if err := s.Webhooks.Dispatch(ctx(), wsID, "question:published",
		map[string]any{"question_id": "q1", "version_id": "v1", "status": "published"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Webhooks.Dispatch(ctx(), wsID, "question:changed",
		map[string]any{"question_id": "q1", "version_id": "v2"}); err != nil {
		t.Fatal(err)
	}
	if captured.requests() != 2 {
		t.Fatalf("应投递 2 次，got %d", captured.requests())
	}
	dels, err := s.Repo.ListWebhookDeliveriesBySubscription(ctx(), sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(dels) != 2 || dels[0].Status != "sent" || dels[1].Status != "sent" {
		t.Fatalf("两条投递均应 sent，got %+v", dels)
	}

	// 请求体契约：event_type 与载荷透传。
	var body struct {
		EventType string         `json:"event_type"`
		Payload   map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(captured.body, &body); err != nil {
		t.Fatalf("解析投递请求体: %v", err)
	}
	if body.EventType != "question:changed" {
		t.Fatalf("最后一条应 question:changed，got %q", body.EventType)
	}
	if body.Payload["question_id"] != "q1" || body.Payload["version_id"] != "v2" {
		t.Fatalf("载荷透传异常: %+v", body.Payload)
	}
}
