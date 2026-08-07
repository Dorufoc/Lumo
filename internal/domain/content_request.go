// content_request.go 求题请求领域状态（完整设计文档 4.20 P2：求题闭环）。
// 状态机严格对齐 content_requests.status CHECK 枚举：open|fulfilled|closed。
package domain

// 求题请求状态（4.20 content_requests.status 枚举，禁止发明新值）。
const (
	ContentRequestOpen      = "open"      // 唯一中间态：已提交（生成与审核均在此推进）
	ContentRequestFulfilled = "fulfilled" // 已生成题目并入库（终态）
	ContentRequestClosed    = "closed"    // 已拒绝/关闭（终态）
)

// ValidContentRequestStatus 校验是否为 4.20 枚举值。
func ValidContentRequestStatus(s string) bool {
	return s == ContentRequestOpen || s == ContentRequestFulfilled || s == ContentRequestClosed
}

// ContentRequestCanTransition 判定状态迁移是否合法。
// open 是唯一中间态：可迁至 fulfilled（审核通过并入库）或 closed（拒绝/取消）；
// 终态不可迁移，非法迁移返回 false。
func ContentRequestCanTransition(from, to string) bool {
	if !ValidContentRequestStatus(from) || !ValidContentRequestStatus(to) {
		return false
	}
	if from != ContentRequestOpen {
		return false
	}
	return to == ContentRequestFulfilled || to == ContentRequestClosed
}
