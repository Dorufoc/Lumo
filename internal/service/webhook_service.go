package service

// Webhook 服务（API 设计文档 7.13 / 完整设计文档 4.24 / Todo 31）。
// 出站投递：POST 原始 JSON body，X-Lumo-Signature: sha256=<HMAC-SHA256(body, secret)>，
// Authorization: Bearer <secret>；secret 仅存 secrets.json（键名存 secret_ref，值不落库）。
// 重试：in-process 每 30s 扫描（RunScheduler/RunOnce，同 Todo 14），退避表钉死：
// attempt 1→30s、2→2m、3→10m、4→30m，attempt ≥ 5 → status=dead（死信，不再投递）。
// 失败统一 WEBHOOK_FAILED；测试钩子 WebhookTestSend 强制单次投递、不进重试队列。

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"lumo/internal/agent"
	"lumo/internal/domain"
	"lumo/internal/repository"
)

// WebhookService 实现 Webhook 订阅/测试/删除与投递调度。
// Now 可注入时钟：测试中推进时间触发退避判定；RunOnce/RunScheduler 供调度器使用。
type WebhookService struct {
	s      *Services
	Now    func() time.Time
	client *http.Client
}

// WebhookSubscription 是订阅 DTO（webhook_subscriptions 表行）。
// SecretRef 仅存 secrets.json 内的键名，密钥值从不返回。
type WebhookSubscription struct {
	ID          string   `json:"id"`
	WorkspaceID string   `json:"workspace_id"`
	URL         string   `json:"url"`
	EventTypes  []string `json:"event_types"`
	SecretRef   *string  `json:"secret_ref"`
	Enabled     bool     `json:"enabled"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

// WebhookSubscribeReq 订阅请求。
type WebhookSubscribeReq struct {
	WorkspaceID    string   `json:"workspace_id"`
	URL            string   `json:"url"`
	EventTypes     []string `json:"event_types"`
	SecretRef      *string  `json:"secret_ref"`
	IdempotencyKey string   `json:"idempotency_key"`
}

// WebhookSubscribe 创建 Webhook 订阅（url/event_types[]/secret_ref）。
// 校验：http(s) URL（trim 后 1-2048）、事件白名单非空、secret_ref 指向 secrets.json 已有键。
func (w *WebhookService) WebhookSubscribe(ctx context.Context, req WebhookSubscribeReq) (*WebhookSubscription, error) {
	if err := w.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(req.URL)
	if trimmed == "" || len(trimmed) > 2048 {
		return nil, domain.InvalidArg("url 长度须为 1-2048")
	}
	u, err := url.Parse(trimmed)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, domain.InvalidArg("url 必须为合法的 http/https 地址")
	}
	if len(req.EventTypes) == 0 {
		return nil, domain.InvalidArg("event_types 至少选择一项")
	}
	for _, ev := range req.EventTypes {
		if !agent.IsRegisteredUserEvent(ev) {
			return nil, domain.InvalidArg("未注册的事件类型: %s", ev)
		}
	}
	if req.SecretRef != nil {
		if _, ok := w.webhookSecretValue(*req.SecretRef); !ok {
			return nil, domain.InvalidArg("secret_ref 指向的密钥不存在，请先在设置中配置")
		}
	}

	sub, err := withIdempotency(w.s, ctx, req.WorkspaceID, req.IdempotencyKey, "WebhookSubscribe", func() (*WebhookSubscription, error) {
		id := NewID()
		now := w.Now().UTC().Format(time.RFC3339)
		dto := &WebhookSubscription{
			ID: id, WorkspaceID: req.WorkspaceID, URL: trimmed,
			EventTypes: req.EventTypes, SecretRef: req.SecretRef,
			Enabled: true, CreatedAt: now, UpdatedAt: now,
		}
		if err := w.s.Repo.CreateWebhookSubscription(ctx, &repository.WebhookSubscriptionRow{
			ID: id, WorkspaceID: req.WorkspaceID, URL: trimmed,
			SecretRef: req.SecretRef, EventTypesJSON: repository.MarshalJSON(req.EventTypes),
			Enabled: true,
		}); err != nil {
			return nil, err
		}
		w.s.audit(ctx, req.WorkspaceID, "webhook.subscribe", "webhook_subscription", id,
			map[string]any{"url": trimmed, "event_types": req.EventTypes})
		return dto, nil
	})
	if err != nil {
		return nil, err
	}
	return sub, nil
}

// WebhookTestSendReq 测试发送请求。
type WebhookTestSendReq struct {
	WorkspaceID    string `json:"workspace_id"`
	SubscriptionID string `json:"subscription_id"`
}

// WebhookTestSendResp 测试发送结果（确定性测试钩子，不创建投递记录、不进重试队列）。
type WebhookTestSendResp struct {
	OK         bool   `json:"ok"`
	StatusCode int    `json:"status_code"`
	Error      string `json:"error"`
}

// WebhookTestSend 测试钩子：强制对订阅 URL 发送一次测试事件。
// 不落库投递记录、不进入重试队列；目标不可达/非 2xx 仅返回失败详情。
func (w *WebhookService) WebhookTestSend(ctx context.Context, req WebhookTestSendReq) (*WebhookTestSendResp, error) {
	sub, err := w.subscriptionInWorkspace(ctx, req.WorkspaceID, req.SubscriptionID)
	if err != nil {
		return nil, err
	}
	body, err := w.buildTestBody()
	if err != nil {
		return nil, domain.WrapError(domain.CodeWebhookFailed, "构造测试载荷失败", err)
	}
	statusCode, err := w.send(ctx, sub, body)
	if err != nil {
		return &WebhookTestSendResp{OK: false, Error: err.Error()}, nil
	}
	if statusCode >= 200 && statusCode < 300 {
		return &WebhookTestSendResp{OK: true, StatusCode: statusCode}, nil
	}
	return &WebhookTestSendResp{OK: false, StatusCode: statusCode,
		Error: fmt.Sprintf("目标返回 HTTP %d", statusCode)}, nil
}

// WebhookDeleteReq 删除订阅请求。
type WebhookDeleteReq struct {
	WorkspaceID    string `json:"workspace_id"`
	SubscriptionID string `json:"subscription_id"`
}

// WebhookDelete 删除订阅及其投递记录。
// 存在进行中投递（pending/pending_retry）时返回 CONFLICT（需等收敛）。
func (w *WebhookService) WebhookDelete(ctx context.Context, req WebhookDeleteReq) (*DeleteResult, error) {
	sub, err := w.subscriptionInWorkspace(ctx, req.WorkspaceID, req.SubscriptionID)
	if err != nil {
		return nil, err
	}
	deleted, err := w.s.Repo.DeleteWebhookSubscription(ctx, sub.ID)
	if err != nil {
		return nil, err
	}
	if !deleted {
		return nil, domain.NotFound("订阅不存在或已被删除")
	}
	w.s.audit(ctx, req.WorkspaceID, "webhook.delete", "webhook_subscription", sub.ID, nil)
	return &DeleteResult{Deleted: true, DeletedAt: w.Now().UTC().Format(time.RFC3339)}, nil
}

// WebhookListReq 列出订阅请求。
type WebhookListReq struct {
	WorkspaceID string `json:"workspace_id"`
}

// WebhookList 按工作区列出订阅（新→旧）。
func (w *WebhookService) WebhookList(ctx context.Context, req WebhookListReq) ([]*WebhookSubscription, error) {
	if err := w.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	rows, err := w.s.Repo.ListWebhookSubscriptionsByWorkspace(ctx, req.WorkspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]*WebhookSubscription, 0, len(rows))
	for _, r := range rows {
		out = append(out, webhookSubFromRow(r))
	}
	return out, nil
}

// Dispatch 分发一个领域事件：对订阅了该事件类型且启用的订阅创建投递并立即发送。
// 事件白名单与 UserEventBus 一致（7 个用户级事件）。非 2xx 进入重试队列。
func (w *WebhookService) Dispatch(ctx context.Context, workspaceID, eventType string, payload any) error {
	if err := w.s.assertWorkspace(ctx, workspaceID); err != nil {
		return err
	}
	if !agent.IsRegisteredUserEvent(eventType) {
		return nil // 未注册事件不投递（调用方保障，防御性跳过）
	}
	subs, err := w.s.Repo.ListWebhookSubscriptionsByWorkspace(ctx, workspaceID)
	if err != nil {
		return err
	}
	for _, sub := range subs {
		if !sub.Enabled || !subscribesToEvent(sub.EventTypesJSON, eventType) {
			continue
		}
		if err := w.dispatchOne(ctx, sub, eventType, payload); err != nil {
			return err
		}
	}
	return nil
}

// RunOnce 执行一轮重试扫描：到期待重试的投递（next_retry_at <= now）重发原始载荷，
// 成功→sent；失败→按退避表排下次重试或（attempt ≥ 5）死信。调度幂等：死信不再投递。
func (w *WebhookService) RunOnce(ctx context.Context) error {
	now := w.Now().UTC().Format(time.RFC3339)
	due, err := w.s.Repo.ListPendingRetryDeliveries(ctx, now)
	if err != nil {
		return err
	}
	for _, d := range due {
		if d.PayloadJSON == nil {
			continue // 缺原始载荷（旧行），无法重发，跳过
		}
		sub, err := w.s.Repo.GetWebhookSubscriptionByID(ctx, d.SubscriptionID)
		if err != nil || sub == nil || !sub.Enabled {
			continue
		}
		attempt := d.Attempt + 1
		statusCode, sendErr := w.send(ctx, sub, []byte(*d.PayloadJSON))
		if sendErr == nil && statusCode >= 200 && statusCode < 300 {
			if err := w.s.Repo.UpdateWebhookDelivery(ctx, &repository.WebhookDeliveryRow{
				ID: d.ID, Status: "sent", Attempt: attempt,
			}); err != nil {
				return err
			}
			continue
		}
		lastErr := fmt.Sprintf("HTTP %d: %v", statusCode, sendErr)
		if sendErr != nil && statusCode == 0 {
			lastErr = sendErr.Error()
		}
		if attempt >= 5 {
			// 死信：不再安排重试。
			if err := w.s.Repo.UpdateWebhookDelivery(ctx, &repository.WebhookDeliveryRow{
				ID: d.ID, Status: "dead", Attempt: attempt, LastError: &lastErr,
			}); err != nil {
				return err
			}
			continue
		}
		next := w.Now().Add(webhookBackoffFor(attempt)).UTC().Format(time.RFC3339)
		if err := w.s.Repo.UpdateWebhookDelivery(ctx, &repository.WebhookDeliveryRow{
			ID: d.ID, Status: "pending_retry", Attempt: attempt,
			NextRetryAt: &next, LastError: &lastErr,
		}); err != nil {
			return err
		}
	}
	return nil
}

// RunScheduler Webhook 重试调度循环：每 30 秒执行一次 RunOnce，直到 ctx 取消。
// 由 cmd/app/main.go 以进程级 context 启动（in-process goroutine，同 Todo 14）。
func (w *WebhookService) RunScheduler(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	slog.Info("Webhook 重试调度器已启动", "interval", "30s")
	for {
		select {
		case <-ctx.Done():
			slog.Info("Webhook 重试调度器已停止")
			return
		case <-ticker.C:
			if err := w.RunOnce(ctx); err != nil {
				slog.Error("Webhook 重试扫描失败", "error", err)
			}
		}
	}
}

// dispatchOne 为单个订阅创建投递记录并立即发送（attempt=1 语义）。
func (w *WebhookService) dispatchOne(ctx context.Context, sub *repository.WebhookSubscriptionRow, eventType string, payload any) error {
	body, err := json.Marshal(map[string]any{
		"event_type":  eventType,
		"payload":     payload,
		"occurred_at": w.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return domain.WrapError(domain.CodeWebhookFailed, "序列化投递载荷失败", err)
	}
	now := w.Now().UTC().Format(time.RFC3339)
	d := &repository.WebhookDeliveryRow{
		ID: NewID(), SubscriptionID: sub.ID, EventID: NewID(),
		Status: "pending", Attempt: 0,
	}
	payloadJSON := string(body)
	d.PayloadJSON = &payloadJSON
	_ = now // created_at 由数据库默认值生成；now 仅用于对齐语义
	if err := w.s.Repo.CreateWebhookDelivery(ctx, d); err != nil {
		return err
	}

	statusCode, sendErr := w.send(ctx, sub, body)
	if sendErr == nil && statusCode >= 200 && statusCode < 300 {
		return w.s.Repo.UpdateWebhookDelivery(ctx, &repository.WebhookDeliveryRow{
			ID: d.ID, Status: "sent", Attempt: 1,
		})
	}
	lastErr := fmt.Sprintf("HTTP %d: %v", statusCode, sendErr)
	if sendErr != nil && statusCode == 0 {
		lastErr = sendErr.Error()
	}
	next := w.Now().Add(webhookBackoffFor(1)).UTC().Format(time.RFC3339)
	return w.s.Repo.UpdateWebhookDelivery(ctx, &repository.WebhookDeliveryRow{
		ID: d.ID, Status: "pending_retry", Attempt: 1,
		NextRetryAt: &next, LastError: &lastErr,
	})
}

// send 执行一次出站 POST：签名头 + Bearer 密钥头，10s 超时。
// 返回 HTTP 状态码与错误；目标不可达时 statusCode=0。
func (w *WebhookService) send(ctx context.Context, sub *repository.WebhookSubscriptionRow, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.URL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if sub.SecretRef != nil {
		if secret, ok := w.webhookSecretValue(*sub.SecretRef); ok {
			mac := hmac.New(sha256.New, []byte(secret))
			mac.Write(body)
			req.Header.Set("X-Lumo-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
			req.Header.Set("Authorization", "Bearer "+secret)
		}
	}
	client := w.client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// buildTestBody 构造测试事件载荷（webhook:test，无业务副作用）。
func (w *WebhookService) buildTestBody() ([]byte, error) {
	return json.Marshal(map[string]any{
		"event_type":  "webhook:test",
		"payload":     map[string]any{"test": true, "sent_at": w.Now().UTC().Format(time.RFC3339)},
		"occurred_at": w.Now().UTC().Format(time.RFC3339),
	})
}

// webhookSecretValue 从 secrets.json 读取键值；键不存在返回 ok=false。
func (w *WebhookService) webhookSecretValue(ref string) (string, bool) {
	m, err := readSecretsFile(w.s.Cfg.SecretsPath)
	if err != nil {
		return "", false
	}
	v, ok := m[ref]
	if !ok || v == "" {
		return "", false
	}
	return v, true
}

// subscriptionInWorkspace 校验工作区后读取订阅；跨工作区访问按不存在处理（防存在性泄露）。
func (w *WebhookService) subscriptionInWorkspace(ctx context.Context, wsID, subID string) (*repository.WebhookSubscriptionRow, error) {
	if err := w.s.assertWorkspace(ctx, wsID); err != nil {
		return nil, err
	}
	sub, err := w.s.Repo.GetWebhookSubscriptionByID(ctx, subID)
	if err != nil {
		return nil, err
	}
	if sub == nil || sub.WorkspaceID != wsID {
		return nil, domain.NotFound("订阅不存在或已被删除")
	}
	return sub, nil
}

// subscribesToEvent 判断订阅的 event_types JSON 是否包含目标事件。
func subscribesToEvent(eventTypesJSON, eventType string) bool {
	var events []string
	if err := json.Unmarshal([]byte(eventTypesJSON), &events); err != nil {
		return false
	}
	for _, e := range events {
		if e == eventType {
			return true
		}
	}
	return false
}

func webhookSubFromRow(r *repository.WebhookSubscriptionRow) *WebhookSubscription {
	var events []string
	_ = json.Unmarshal([]byte(r.EventTypesJSON), &events)
	return &WebhookSubscription{
		ID: r.ID, WorkspaceID: r.WorkspaceID, URL: r.URL,
		EventTypes: events, SecretRef: r.SecretRef, Enabled: r.Enabled,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// webhookBackoffFor 返回第 attempt 次失败后的退避时长（Todo 31 钉死表）。
// attempt 1→30s、2→2m、3→10m、4→30m；≥5 无退避（直接死信）。
func webhookBackoffFor(attempt int) time.Duration {
	switch attempt {
	case 1:
		return 30 * time.Second
	case 2:
		return 2 * time.Minute
	case 3:
		return 10 * time.Minute
	case 4:
		return 30 * time.Minute
	default:
		return 0
	}
}
