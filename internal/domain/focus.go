package domain

import (
	"time"
)

// 专注模式（0005_student.sql timer_sessions.mode 枚举）。
const (
	TimerModePomodoro = "pomodoro"
	TimerModeFree     = "free"
)

// 专注会话终态（0005_student.sql timer_sessions.status 枚举）。
// 活动中的会话 ended_at 为空、status 为 DDL 默认占位（'completed'），结束/归档时覆写为终态。
const (
	TimerStatusCompleted   = "completed"
	TimerStatusInterrupted = "interrupted"
	TimerStatusAbandoned   = "abandoned"
)

// 番茄钟计划时长边界（分钟，4.13 T1 配置 5–120；1 分钟下限允许短专注）。
const (
	TimerPomodoroMinMinutes = 1
	TimerPomodoroMaxMinutes = 120
)

// TimerSession 是专注会话领域模型（0005 timer_sessions 行）。
// 既是仓储的行类型，也作为 RPC 响应 DTO（json tag 与传输契约一致）。
type TimerSession struct {
	ID              string  `json:"id"`
	WorkspaceID     string  `json:"workspace_id"`
	UserID          string  `json:"user_id"`
	Mode            string  `json:"mode"`
	PlannedMinutes  int     `json:"planned_minutes"`
	ActualSeconds   int     `json:"actual_seconds"`
	TaskID          *string `json:"task_id"`
	Status          string  `json:"status"`
	InterruptReason *string `json:"interrupt_reason"`
	StartedAt       *string `json:"started_at"`
	EndedAt         *string `json:"ended_at"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
}

// ValidTimerMode 校验专注模式枚举。
func ValidTimerMode(mode string) bool {
	return mode == TimerModePomodoro || mode == TimerModeFree
}

// ValidTimerStatus 校验终态枚举。
func ValidTimerStatus(status string) bool {
	return status == TimerStatusCompleted || status == TimerStatusInterrupted || status == TimerStatusAbandoned
}

// ValidateTimerPlannedMinutes 校验计划时长（分钟）：
// pomodoro 须在 1..120；free 允许 0（不限时开放式专注）；负数一律拒绝。
func ValidateTimerPlannedMinutes(mode string, minutes int) error {
	if minutes < 0 {
		return InvalidArg("planned_minutes 不能为负")
	}
	if mode == TimerModePomodoro && (minutes < TimerPomodoroMinMinutes || minutes > TimerPomodoroMaxMinutes) {
		return InvalidArg("pomodoro 的 planned_minutes 须在 %d..%d 之间", TimerPomodoroMinMinutes, TimerPomodoroMaxMinutes)
	}
	return nil
}

// TimerStatusFor 决定会话终态（4.13 / Todo 13 决策，语义顺序固定）：
//  1. 已达成计划时长（planned_minutes>0 且 actual_seconds ≥ planned*60）→ completed；
//  2. 否则有中断原因 → interrupted；
//  3. 否则 → abandoned。
//
// 注意：free 不限时（planned_minutes=0）的会话永不进入 completed——按设计
// 「结束前退出需选择原因」，无原因自然结束按 abandoned 归档（计为一次会话）。
func TimerStatusFor(plannedMinutes, actualSeconds int, interruptReason string) string {
	if plannedMinutes > 0 && actualSeconds >= plannedMinutes*60 {
		return TimerStatusCompleted
	}
	if interruptReason != "" {
		return TimerStatusInterrupted
	}
	return TimerStatusAbandoned
}

// ValidDate 校验日期字符串（YYYY-MM-DD）。
func ValidDate(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}
