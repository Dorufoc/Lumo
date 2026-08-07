package domain

import (
	"strings"
	"unicode/utf8"
)

// 申诉状态：0005_student.sql grading_appeals.status CHECK 约束，4.22 C7。
const (
	AppealStatusPending  = "pending"  // 待处理（学生已申诉，教师未处理）
	AppealStatusAccepted = "accepted" // 已接受（复议通过，待改分）
	AppealStatusRejected = "rejected" // 已驳回（终态，原判不变）
	AppealStatusResolved = "resolved" // 已改分（终态，复议改分完成）
)

// ValidAppealStatus 校验申诉状态枚举。
func ValidAppealStatus(s string) bool {
	return s == AppealStatusPending || s == AppealStatusAccepted ||
		s == AppealStatusRejected || s == AppealStatusResolved
}

// 申诉处理决策：AppealResolve.decision 枚举。
const (
	AppealDecisionAccepted = "accepted" // 接受申诉（可带 new_score 复议改分）
	AppealDecisionRejected = "rejected" // 驳回申诉（原判不变）
)

// ValidAppealDecision 校验处理决策枚举。
func ValidAppealDecision(s string) bool {
	return s == AppealDecisionAccepted || s == AppealDecisionRejected
}

// AppealCanTransition 状态机迁移表：pending→accepted/rejected；accepted→resolved。
// rejected/resolved 为终态，任何出边均非法（返回 INVALID_STATE）。
func AppealCanTransition(from, to string) bool {
	switch from {
	case AppealStatusPending:
		return to == AppealStatusAccepted || to == AppealStatusRejected
	case AppealStatusAccepted:
		return to == AppealStatusResolved
	default:
		return false
	}
}

// ValidAppealReason 校验申诉理由：去除空白后长度 1-2000。
func ValidAppealReason(s string) bool {
	n := utf8.RuneCountInString(strings.TrimSpace(s))
	return n >= 1 && n <= 2000
}
