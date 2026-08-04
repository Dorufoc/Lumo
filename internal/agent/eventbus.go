// Package agent 提供受约束的 AI 能力编排：会话、事件流、Tutor/Grader/Diagnoser。
package agent

import "sync"

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
