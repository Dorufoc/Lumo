package domain

// 收藏引用类型（4.15：ref_type[question|document|agent_message|note]）。
const (
	FavoriteRefTypeQuestion = "question"
	FavoriteRefTypeDocument = "document"
	FavoriteRefTypeAgentMsg = "agent_message"
	FavoriteRefTypeNote     = "note"
)

// ValidateFavoriteRefType 校验收藏引用类型枚举。
func ValidateFavoriteRefType(t string) bool {
	return t == FavoriteRefTypeQuestion || t == FavoriteRefTypeDocument ||
		t == FavoriteRefTypeAgentMsg || t == FavoriteRefTypeNote
}

// 稍后读状态（4.15：status[queued|read|skipped]；requeue 为动作而非落库状态）。
const (
	ReadLaterStatusQueued  = "queued"
	ReadLaterStatusRead    = "read"
	ReadLaterStatusSkipped = "skipped"
)

// 稍后读动作（4.15 / API 7.8：action[read|skip|requeue]）。
const (
	ReadLaterActionRead    = "read"
	ReadLaterActionSkip    = "skip"
	ReadLaterActionRequeue = "requeue"
)

// ValidateReadLaterAction 校验稍后读动作枚举。
func ValidateReadLaterAction(a string) bool {
	return a == ReadLaterActionRead || a == ReadLaterActionSkip || a == ReadLaterActionRequeue
}

// ReadLaterNextStatus 计算动作后的状态；requeue 落库为 queued（DDL CHECK 不含 requeue）。
// 非法流转返回 INVALID_STATE。
func ReadLaterNextStatus(current, action string) (string, error) {
	switch action {
	case ReadLaterActionRead:
		if current != ReadLaterStatusQueued {
			return "", InvalidState("稍后读状态 %s 不允许执行 read", current)
		}
		return ReadLaterStatusRead, nil
	case ReadLaterActionSkip:
		if current != ReadLaterStatusQueued {
			return "", InvalidState("稍后读状态 %s 不允许执行 skip", current)
		}
		return ReadLaterStatusSkipped, nil
	case ReadLaterActionRequeue:
		if current != ReadLaterStatusRead && current != ReadLaterStatusSkipped {
			return "", InvalidState("稍后读状态 %s 不允许执行 requeue", current)
		}
		return ReadLaterStatusQueued, nil
	default:
		return "", InvalidArg("稍后读动作非法：%s", action)
	}
}

// 文档摘要状态（4.15：status[pending|ready|failed]）。
const (
	SummaryStatusPending = "pending"
	SummaryStatusReady   = "ready"
	SummaryStatusFailed  = "failed"
)

// SummaryPayload 是文档摘要载荷（4.15 / document_summaries.summary_json）。
// points 要点、structure 文档结构、terms 关键术语；LLM 未配置时降级为确定性模板。
type SummaryPayload struct {
	Points    []string `json:"points"`
	Structure []string `json:"structure"`
	Terms     []string `json:"terms"`
	Note      string   `json:"note,omitempty"`
}
