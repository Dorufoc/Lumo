package domain

import (
	"encoding/json"
	"regexp"
	"time"
)

// 日历事件类型（0005_student.sql calendar_events.kind 枚举，4.16）。
const (
	CalendarKindTask     = "task"
	CalendarKindReview   = "review"
	CalendarKindExam     = "exam"
	CalendarKindCheckin  = "checkin"
	CalendarKindFocus    = "focus"
	CalendarKindPersonal = "personal"
)

// ValidCalendarKind 校验日历事件类型枚举。
func ValidCalendarKind(k string) bool {
	switch k {
	case CalendarKindTask, CalendarKindReview, CalendarKindExam,
		CalendarKindCheckin, CalendarKindFocus, CalendarKindPersonal:
		return true
	}
	return false
}

// monthRe 校验月份格式 YYYY-MM。
var monthRe = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

// ValidMonth 校验月份字符串（YYYY-MM，日历月参数格式）。
func ValidMonth(s string) bool {
	return monthRe.MatchString(s)
}

// startTimeRe 校验开始时间格式 HH:MM（24 小时制）。
var startTimeRe = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

// ValidStartTime 校验开始时间字符串（HH:MM）。
func ValidStartTime(s string) bool {
	return startTimeRe.MatchString(s)
}

// 里程碑状态（0005_student.sql goal_milestones.status 无 CHECK 约束，服务端定义枚举）。
const (
	MilestoneStatusPending  = "pending"  // 未判定
	MilestoneStatusAchieved = "achieved" // 已达成（题量达标且正确率达标）
	MilestoneStatusNotMet   = "not_met"  // 已判定未达成
)

// ValidMilestoneStatus 校验里程碑状态枚举。
func ValidMilestoneStatus(s string) bool {
	return s == MilestoneStatusPending || s == MilestoneStatusAchieved || s == MilestoneStatusNotMet
}

// 里程碑验收条件类型（criteria_json.type，4.16 C3：题量/正确率）。
const (
	MilestoneCriteriaPractice = "practice" // 题量达标（且可选手正确率下限）
	MilestoneCriteriaTasks    = "tasks"    // 完成计划任务数达标
)

// MilestoneCriteria 是里程碑验收条件（criteria_json 结构化视图）。
// practice：完成练习提交数 ≥ count，且（可选）正确率 ≥ min_accuracy；
// tasks：完成计划任务数 ≥ count。
type MilestoneCriteria struct {
	Type        string   `json:"type"`
	Count       int      `json:"count"`
	MinAccuracy *float64 `json:"min_accuracy,omitempty"`
}

// ParseMilestoneCriteria 解析并校验验收条件；非法 JSON/类型/阈值 → INVALID_ARGUMENT。
func ParseMilestoneCriteria(raw json.RawMessage) (*MilestoneCriteria, error) {
	if len(raw) == 0 || !json.Valid(raw) {
		return nil, InvalidArg("criteria_json 必须是合法 JSON")
	}
	var c MilestoneCriteria
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, InvalidArg("criteria_json 解析失败: %v", err)
	}
	if c.Type != MilestoneCriteriaPractice && c.Type != MilestoneCriteriaTasks {
		return nil, InvalidArg("criteria type 仅允许 practice|tasks")
	}
	if c.Count <= 0 {
		return nil, InvalidArg("criteria count 必须大于 0")
	}
	if c.MinAccuracy != nil && (*c.MinAccuracy < 0 || *c.MinAccuracy > 1) {
		return nil, InvalidArg("criteria min_accuracy 须在 0..1 之间")
	}
	if c.Type == MilestoneCriteriaTasks && c.MinAccuracy != nil {
		return nil, InvalidArg("tasks 类型不支持 min_accuracy")
	}
	return &c, nil
}

// ValidMilestoneDueAt 校验里程碑到期日：接受 YYYY-MM-DD 或 RFC3339 时间戳。
func ValidMilestoneDueAt(s string) bool {
	if ValidDate(s) {
		return true
	}
	_, err := time.Parse(time.RFC3339, s)
	return err == nil
}
