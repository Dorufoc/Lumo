package domain

import (
	"strings"
	"unicode/utf8"
)

// 班级状态（0005_student.sql classes.status，4.22）。
const (
	ClassStatusActive   = "active"   // 正常使用
	ClassStatusArchived = "archived" // 已归档（教师不可再邀请/加人/改信息）
)

// ValidClassStatus 校验班级状态枚举。
func ValidClassStatus(s string) bool {
	return s == ClassStatusActive || s == ClassStatusArchived
}

// 班级成员状态（0005_student.sql class_members.status CHECK 约束，4.22）。
const (
	ClassMemberStatusActive  = "active"  // 在班
	ClassMemberStatusRemoved = "removed" // 已移出
)

// ValidClassMemberStatus 校验班级成员状态枚举。
func ValidClassMemberStatus(s string) bool {
	return s == ClassMemberStatusActive || s == ClassMemberStatusRemoved
}

// ValidClassName 校验班级名称：去空白后长度 1-120（与 classes.name CHECK 一致）。
func ValidClassName(s string) bool {
	n := utf8.RuneCountInString(strings.TrimSpace(s))
	return n >= 1 && n <= 120
}

// ValidClassSubject 校验科目：可为空，非空时长度 ≤ 80（超出按 INVALID_ARGUMENT 拦截）。
func ValidClassSubject(s string) bool {
	return len(s) <= 80
}

// ValidClassSemester 校验学期：可为空，非空时长度 ≤ 40。
func ValidClassSemester(s string) bool {
	return len(s) <= 40
}
