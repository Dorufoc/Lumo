package domain

import (
	"sort"
	"time"
)

// 健康与专注辅助（4.17 / 0005_student.sql health_settings）。
const (
	// NightModeAuto/Light/Dark/Custom 是夜间模式枚举。
	NightModeAuto   = "auto"
	NightModeLight  = "light"
	NightModeDark   = "dark"
	NightModeCustom = "custom"
)

// ValidNightMode 校验夜间模式枚举。
func ValidNightMode(m string) bool {
	return m == NightModeAuto || m == NightModeLight || m == NightModeDark || m == NightModeCustom
}

// 久坐提醒规则（4.17 H1：默认每 45 分钟提醒起身活动 1–2 分钟）。
const (
	// SedentaryThresholdMinutes 久坐连续窗口触发阈值（分钟）。
	SedentaryThresholdMinutes = 45
	// SedentaryGapResetMinutes 断档重置阈值：相邻 session 间隔 > 该值（分钟）
	// 视为已起身休息，连续窗口重置；≤ 该值视为同一连续久坐窗口（4.17 H1「默认每
	// 45 分钟」+ Todo 18 决策：间隔 >10 分钟重置窗口）。
	SedentaryGapResetMinutes = 10
)

// SessionSpan 是一段已结束专注会话的时间跨度（timer_sessions 中
// started_at/ended_at 均非空的行）。
type SessionSpan struct {
	Start time.Time
	End   time.Time
}

// SedentaryWindow 是一个连续久坐窗口的聚合结果。
type SedentaryWindow struct {
	// Minutes 是窗口内累计专注分钟数（各段时长之和，向下取整）。
	Minutes int
	// Rested 表示该久坐事件后用户是否完成休息：窗口累计分钟首次达到
	// SedentaryThresholdMinutes 之后没有追加新会话即为 true（用户在提醒点附近
	// 停下休息）；若累计跨过阈值后仍继续新的会话，说明未休息 → false。
	Rested bool
}

// SedentaryWindows 将已结束会话按时间排序并切分为连续久坐窗口：
//   - 按 started_at 升序排列；
//   - 相邻会话间隔 ≤ SedentaryGapResetMinutes 视为同一窗口（累计各段时长）；
//   - 间隔 > SedentaryGapResetMinutes 视为已起身休息，开启新窗口。
//
// 返回空切片当无输入。Rested 依据「累计分钟跨过阈值后是否追加新会话」判定
// （健康统计 4.17 H6 休息完成率的原始数据）。
func SedentaryWindows(spans []SessionSpan) []SedentaryWindow {
	if len(spans) == 0 {
		return nil
	}
	sorted := make([]SessionSpan, len(spans))
	copy(sorted, spans)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start.Before(sorted[j].Start) })

	var windows []SedentaryWindow
	accMinutes := 0
	crossed := false    // 累计是否已跨过阈值
	addedAfterCross := false // 跨过阈值后是否仍追加了会话
	flush := func() {
		windows = append(windows, SedentaryWindow{Minutes: accMinutes, Rested: !addedAfterCross})
	}
	for i, sp := range sorted {
		if i > 0 && sp.Start.Sub(sorted[i-1].End) > SedentaryGapResetMinutes*time.Minute {
			flush()
			accMinutes = 0
			crossed = false
			addedAfterCross = false
		}
		dur := int(sp.End.Sub(sp.Start).Minutes())
		if crossed {
			addedAfterCross = true
		}
		accMinutes += dur
		if !crossed && accMinutes >= SedentaryThresholdMinutes {
			crossed = true
		}
	}
	flush()
	return windows
}

// ContinuousSedentaryMinutes 返回所有连续久坐窗口中最长的累计分钟数
// （0 当无输入）。调用方以 SedentaryThresholdMinutes 判定是否触发提醒。
func ContinuousSedentaryMinutes(spans []SessionSpan) int {
	windows := SedentaryWindows(spans)
	max := 0
	for _, w := range windows {
		if w.Minutes > max {
			max = w.Minutes
		}
	}
	return max
}
