// Package agent 提供受约束的 AI 能力编排：会话、事件流、Tutor/Grader/Diagnoser。
package agent

import (
	"context"
	"strings"
	"sync"

	"lumo/internal/domain"
	"lumo/internal/repository"
)

// Event 是应用事件（agent:delta 等），载荷含 request_id/session_id/sequence_no。
type Event struct {
	Name    string
	Payload map[string]any
}

// Bus 是进程内事件总线（按 session 订阅）。
type Bus struct {
	mu   sync.Mutex
	subs map[string]map[chan Event]struct{}
}

// NewBus 创建总线。
func NewBus() *Bus {
	return &Bus{subs: map[string]map[chan Event]struct{}{}}
}

// Publish 向会话订阅者广播事件（非阻塞，丢弃无订阅者事件）。
func (b *Bus) Publish(sessionID string, ev Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs[sessionID] {
		select {
		case ch <- ev:
		default:
		}
	}
}

// Subscribe 订阅会话事件，返回通道与取消函数。
func (b *Bus) Subscribe(sessionID string) (<-chan Event, func()) {
	ch := make(chan Event, 64)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subs[sessionID] == nil {
		b.subs[sessionID] = map[chan Event]struct{}{}
	}
	b.subs[sessionID][ch] = struct{}{}
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.subs[sessionID], ch)
		if len(b.subs[sessionID]) == 0 {
			delete(b.subs, sessionID)
		}
	}
}

// ---------- 用户级领域事件（Todo 4：UserEventBus + 7 个扩展事件） ----------

// 用户级领域事件名称（API设计文档.md 7.14 扩展事件表 + 第 3 节 grading:updated）。
// 载荷均含 request_id/session_id/sequence_no/occurred_at（第 3 节事件规范）。
const (
	EventReportReady       = "report:ready"        // 报告生成完成：report_id, period, status
	EventExamAutoSubmitted = "exam:auto_submitted" // 考试倒计时到期自动提交：exam_id, status
	EventFlashcardDue      = "flashcard:due"       // 到期闪卡数变化：due_count
	EventReminderTriggered = "reminder:triggered"  // 提醒触发：kind, ref_type, ref_id
	EventGradingAppeal     = "grading:appeal"      // 申诉状态变化：appeal_id, grading_id, status
	EventSyncExtended      = "sync:extended"       // 扩展对象同步冲突：entity_type, conflict_count
	EventGradingUpdated    = "grading:updated"     // 异步评分完成/失败：grading_id, status, score
	EventQuestionPublished = "question:published"  // 新题发布（Todo 37）：question_id, version_id, status
	EventQuestionChanged   = "question:changed"    // 题目内容变更（Todo 37）：question_id, version_id
)

// userEventRegistry 记录可发布的用户级事件名称。
var userEventRegistry = map[string]struct{}{
	EventReportReady:       {},
	EventExamAutoSubmitted: {},
	EventFlashcardDue:      {},
	EventReminderTriggered: {},
	EventGradingAppeal:     {},
	EventSyncExtended:      {},
	EventGradingUpdated:    {},
	EventQuestionPublished: {},
	EventQuestionChanged:   {},
}

// IsRegisteredUserEvent 判断事件名是否为已注册的用户级领域事件。
func IsRegisteredUserEvent(name string) bool {
	_, ok := userEventRegistry[name]
	return ok
}

// UserEventBus 是用户级领域事件总线：按 user_id 订阅，
// 每次 Publish 同时持久化一条 notifications 记录（4.14/6.2.1）。
// 与 session 级 Bus 并存：Agent 流式事件走 Bus，领域事件走 UserEventBus。
type UserEventBus struct {
	repo *repository.Repo
	mu   sync.Mutex
	subs map[string]map[chan Event]struct{}
	seq  map[string]int64 // user_id → 已发布事件序号（sequence_no 严格递增）
}

// NewUserEventBus 创建用户级事件总线。
func NewUserEventBus(repo *repository.Repo) *UserEventBus {
	return &UserEventBus{
		repo: repo,
		subs: map[string]map[chan Event]struct{}{},
		seq:  map[string]int64{},
	}
}

// Publish 发布用户级领域事件：校验事件类型 → 持久化通知 → 广播给订阅者。
// 未注册的事件类型返回错误，且不落库、不广播（不 panic）。
// 载荷补齐 occurred_at（缺省）与 sequence_no（按 user 严格递增）。
func (b *UserEventBus) Publish(userID string, ev Event) error {
	if !IsRegisteredUserEvent(ev.Name) {
		return domain.InvalidArg("未注册的用户事件类型: %s", ev.Name)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	next := b.seq[userID] + 1
	ev.Payload["sequence_no"] = next
	if err := b.persist(userID, ev); err != nil {
		return err
	}
	b.seq[userID] = next
	for ch := range b.subs[userID] {
		select {
		case ch <- ev:
		default:
		}
	}
	return nil
}

// persist 将领域事件落库为 notifications 行（调用方需持有锁）。
func (b *UserEventBus) persist(userID string, ev Event) error {
	payload := ev.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	if _, ok := payload["occurred_at"]; !ok {
		payload["occurred_at"] = nowUTC()
	}
	var refType, refID *string
	if v, ok := payload["ref_type"].(string); ok && v != "" {
		refType = &v
	}
	if v, ok := payload["ref_id"].(string); ok && v != "" {
		refID = &v
	}
	n := &repository.NotificationRow{
		ID:           newID(),
		UserID:       userID,
		Kind:         ev.Name,
		TitleKey:     notificationTitleKey(ev.Name),
		BodyArgsJSON: repository.MarshalJSON(payload),
		RefType:      refType,
		RefID:        refID,
	}
	return b.repo.CreateNotification(context.Background(), n)
}

// notificationTitleKey 由事件名派生 i18n 标题键（event:name → notification.event_name.title）。
func notificationTitleKey(kind string) string {
	return "notification." + strings.ReplaceAll(kind, ":", "_") + ".title"
}

// SubscribeUser 订阅用户事件，返回通道与取消函数。
func (b *UserEventBus) SubscribeUser(userID string) (<-chan Event, func()) {
	ch := make(chan Event, 64)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subs[userID] == nil {
		b.subs[userID] = map[chan Event]struct{}{}
	}
	b.subs[userID][ch] = struct{}{}
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.subs[userID], ch)
		if len(b.subs[userID]) == 0 {
			delete(b.subs, userID)
		}
	}
}
