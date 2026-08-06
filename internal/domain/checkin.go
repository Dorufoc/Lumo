package domain

import (
	"encoding/json"
	"time"
)

// 打卡类型（0005_student.sql checkins.kind 枚举）。
const (
	CheckinKindNormal = "normal"
	CheckinKindMakeup = "makeup"
)

// CheckinMakeupMonthlyLimit 每月补签次数上限（4.11 A2）。
const CheckinMakeupMonthlyLimit = 3

// 成就规则类型（0005 achievement_defs.trigger_rule_json.type）。
const (
	AchievementRuleStreakDays    = "streak_days"
	AchievementRuleTotalCheckins = "total_checkins"
)

// CheckinRule 是成就触发规则（trigger_rule_json 的结构化视图，4.11 A3 固定模板）。
type CheckinRule struct {
	Type      string `json:"type"`
	Threshold int    `json:"threshold"`
}

// ValidCheckinRuleType 校验规则类型枚举。
func ValidCheckinRuleType(t string) bool {
	return t == AchievementRuleStreakDays || t == AchievementRuleTotalCheckins
}

// ParseCheckinRule 解析 trigger_rule_json；非法 JSON / 类型 / 阈值 → INVALID_ARGUMENT。
func ParseCheckinRule(raw json.RawMessage) (*CheckinRule, error) {
	var r CheckinRule
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, InvalidArg("成就规则 JSON 非法: %v", err)
	}
	if !ValidCheckinRuleType(r.Type) || r.Threshold <= 0 {
		return nil, InvalidArg("成就规则非法: type=%q threshold=%d", r.Type, r.Threshold)
	}
	return &r, nil
}

// ValidCheckinDate 校验打卡日期格式（YYYY-MM-DD，本地日期，服务端不跨时区换算）。
func ValidCheckinDate(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// CheckinDate 返回给定时间的本地日期（YYYY-MM-DD）。
func CheckinDate(t time.Time) string {
	return t.Format("2006-01-02")
}

// AddDays 返回 date（YYYY-MM-DD）偏移 n 天后的日期。
func AddDays(date string, n int) (string, error) {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return "", err
	}
	return t.AddDate(0, 0, n).Format("2006-01-02"), nil
}

// ComputeStreak 从打卡事实（日期集合）计算连续天数（4.11 A2：断签后重新累计）。
// 锚点规则：今天已打卡 → 锚点=今天；今天未打卡但昨天已打卡 → 锚点=昨天（streak 仍在延续）；
// 否则 streak=0 且 anchor 为空。streak 只来自事实聚合，禁止手工篡改。
func ComputeStreak(dates []string, today string) (streak int, anchor string, err error) {
	set := make(map[string]struct{}, len(dates))
	for _, d := range dates {
		if !ValidCheckinDate(d) {
			return 0, "", InvalidArg("打卡日期非法: %s", d)
		}
		set[d] = struct{}{}
	}
	anchor = today
	if _, ok := set[today]; !ok {
		yesterday, e := AddDays(today, -1)
		if e != nil {
			return 0, "", InvalidArg("日期非法: %s", today)
		}
		if _, ok := set[yesterday]; !ok {
			return 0, "", nil
		}
		anchor = yesterday
	}
	cur := anchor
	for {
		if _, ok := set[cur]; !ok {
			break
		}
		streak++
		prev, e := AddDays(cur, -1)
		if e != nil {
			return 0, "", e
		}
		cur = prev
	}
	return streak, anchor, nil
}
