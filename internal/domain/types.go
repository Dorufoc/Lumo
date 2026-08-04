package domain

import "time"

// NowUTC 返回 UTC RFC 3339 毫秒时间字符串，全库统一使用。
func NowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// ParseTime 解析 RFC 3339 时间字符串。
func ParseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339, s)
}

// Page 是列表响应的通用分页结构（游标分页）。
type Page struct {
	Items    []any  `json:"items"`
	NextCursor string `json:"next_cursor"`
	HasMore  bool   `json:"has_more"`
}

// CursorPage 泛型分页，由具体服务构造 Items。
type CursorPage[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor"`
	HasMore    bool   `json:"has_more"`
}

// IdempotencyKey 校验：长度 8-128，仅允许字母、数字、-、_。
func ValidIdempotencyKey(k string) bool {
	if len(k) < 8 || len(k) > 128 {
		return false
	}
	for _, r := range k {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// ValidUUID 校验 UUID 或 ULID 形式（36 位 UUID 或 26 位 ULID），宽松校验：非空且长度合理。
func ValidID(s string) bool {
	if len(s) < 8 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}
