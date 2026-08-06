package domain

import (
	"strings"
	"unicode/utf8"
)

// 作业状态（0005_student.sql assignments.status CHECK 约束，4.22）。
const (
	AssignmentStatusDraft     = "draft"     // 草稿（教师未发布，学生不可见）
	AssignmentStatusPublished = "published" // 已发布（学生可作答提交）
	AssignmentStatusClosed    = "closed"    // 已截止/关闭（学生不可再提交）
)

// ValidAssignmentStatus 校验作业状态枚举。
func ValidAssignmentStatus(s string) bool {
	return s == AssignmentStatusDraft || s == AssignmentStatusPublished || s == AssignmentStatusClosed
}

// 作业判分方式（0005_student.sql assignments.grading_rule CHECK 约束，4.22）。
const (
	GradingRuleAuto    = "auto"    // 自动判分
	GradingRuleTeacher = "teacher" // 教师判分
	GradingRuleHybrid  = "hybrid"  // 自动 + 教师复核
)

// ValidGradingRule 校验判分方式枚举。
func ValidGradingRule(s string) bool {
	return s == GradingRuleAuto || s == GradingRuleTeacher || s == GradingRuleHybrid
}

// ValidAssignmentTitle 校验作业标题：去空白后长度 1-200（与 assignments.title CHECK 一致）。
func ValidAssignmentTitle(s string) bool {
	n := utf8.RuneCountInString(strings.TrimSpace(s))
	return n >= 1 && n <= 200
}

// ValidDueAt 校验截止时间：合法 RFC3339 时间戳（与 domain.ParseTime 一致）。
func ValidDueAt(s string) bool {
	_, err := ParseTime(s)
	return err == nil
}
