package domain

import (
	"encoding/json"
	"time"
)

// 提醒类型（0005_student.sql reminders.kind 枚举 + API 设计文档 7.7）。
const (
	ReminderKindReview = "review"
	ReminderKindGoal   = "goal"
	ReminderKindExam   = "exam"
	ReminderKindStreak = "streak"
	ReminderKindHealth = "health"
)

// ValidReminderKind 校验提醒类型枚举。
func ValidReminderKind(k string) bool {
	switch k {
	case ReminderKindReview, ReminderKindGoal, ReminderKindExam, ReminderKindStreak, ReminderKindHealth:
		return true
	}
	return false
}

// ReminderRule 是提醒规则（reminders.rule_json 的结构化视图，4.14）。
// 当前实现支持 interval 规则：{"type":"interval","minutes":N,"repeat":true|false}。
// minutes 为间隔分钟数（N>=1），next_trigger_at = 触发时刻 + minutes；
// repeat=true 表示重复提醒（触发后 next_trigger_at 前移继续）；false 表示一次性
// （触发后 enabled 翻转为 0，不再触发）。
// 更丰富的业务规则（M1 复习到期 / M2 目标与考试倒计时 / M3 连续学习 / M4 久坐护眼）
// 依赖其他模块（复习卡/计划/考试/打卡）的状态，后续可扩展为独立 type，本 Todo
// 以 interval 规则为可测试核心（见 .omo learnings）。
type ReminderRule struct {
	Type    string `json:"type"`
	Minutes int    `json:"minutes"`
	Repeat  bool   `json:"repeat"`
}

// ReminderRuleInterval 是当前唯一支持的规则类型。
const ReminderRuleInterval = "interval"

// ParseReminderRule 解析 rule_json；非法 JSON / 未知类型 / minutes<1 → INVALID_ARGUMENT。
func ParseReminderRule(raw json.RawMessage) (*ReminderRule, error) {
	var r ReminderRule
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, InvalidArg("提醒规则 JSON 非法: %v", err)
	}
	if r.Type != ReminderRuleInterval {
		return nil, InvalidArg("未知提醒规则类型: %q", r.Type)
	}
	if r.Minutes < 1 {
		return nil, InvalidArg("interval 规则 minutes 须 ≥ 1")
	}
	return &r, nil
}

// NextTriggerAt 返回规则基于 now 的下一次触发时间（UTC RFC3339）。
func (r *ReminderRule) NextTriggerAt(now time.Time) string {
	return now.Add(time.Duration(r.Minutes) * time.Minute).UTC().Format(time.RFC3339)
}
