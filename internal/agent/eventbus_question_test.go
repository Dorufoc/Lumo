package agent

// Todo 37：主题市场 + Webhook 扩展——新事件注册（question:published / question:changed）。
// 断言 IsRegisteredUserEvent 对新事件返回 true（WebhookSubscribe 白名单自动扩展）。

import "testing"

func TestQuestionEventsRegistered(t *testing.T) {
	for _, ev := range []string{EventQuestionPublished, EventQuestionChanged} {
		if !IsRegisteredUserEvent(ev) {
			t.Fatalf("%s 应注册为合法用户事件", ev)
		}
	}
}

func TestQuestionEventsConstants(t *testing.T) {
	if EventQuestionPublished != "question:published" {
		t.Fatalf("EventQuestionPublished 常量值错误: %q", EventQuestionPublished)
	}
	if EventQuestionChanged != "question:changed" {
		t.Fatalf("EventQuestionChanged 常量值错误: %q", EventQuestionChanged)
	}
}
