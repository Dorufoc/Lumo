package domain

// 报告周期（0005_student.sql reports.period 枚举 + API 设计文档 7.5）。
const (
	ReportPeriodDaily   = "daily"
	ReportPeriodWeekly  = "weekly"
	ReportPeriodMonthly = "monthly"
)

// ValidReportPeriod 校验报告周期枚举。
func ValidReportPeriod(p string) bool {
	switch p {
	case ReportPeriodDaily, ReportPeriodWeekly, ReportPeriodMonthly:
		return true
	}
	return false
}

// 报告生成状态（0005_student.sql reports.status 枚举）。
const (
	ReportStatusGenerating = "generating"
	ReportStatusReady      = "ready"
	ReportStatusFailed     = "failed"
)

// ValidReportStatus 校验报告状态枚举。
func ValidReportStatus(s string) bool {
	switch s {
	case ReportStatusGenerating, ReportStatusReady, ReportStatusFailed:
		return true
	}
	return false
}

// 洞察维度（API 设计文档 7.5 InsightGet）。
const (
	InsightDimensionKnowledge = "knowledge"
	InsightDimensionTime      = "time"
	InsightDimensionTrend     = "trend"
)

// ValidInsightDimension 校验洞察维度枚举。
func ValidInsightDimension(d string) bool {
	switch d {
	case InsightDimensionKnowledge, InsightDimensionTime, InsightDimensionTrend:
		return true
	}
	return false
}

// MinReportSample 是最小样本量：低于该样本数不展示百分比，仅标注“样本不足”（完整设计文档 4.12）。
const MinReportSample = 20

// ReportSummary 是报告核心汇总（设计 4.12 R1/R2/R3）。
type ReportSummary struct {
	PracticeCount      int     `json:"practice_count"`
	CorrectCount       int     `json:"correct_count"`
	Accuracy           float64 `json:"accuracy"`
	AccuracySamples    int     `json:"accuracy_samples"`
	ReviewDone         int     `json:"review_done"`
	ReviewDue          int     `json:"review_due"`
	FocusMinutes       int     `json:"focus_minutes"`
	FocusSessions      int     `json:"focus_sessions"`
	CheckinDays        int     `json:"checkin_days"`
	TaskDone           int     `json:"task_done"`
	TaskTotal          int     `json:"task_total"`
	SampleInsufficient bool    `json:"sample_insufficient"`
}

// WeakKnowledgeItem 是薄弱知识点统计（设计 4.12 R2 Top5）。
type WeakKnowledgeItem struct {
	KnowledgeID string `json:"knowledge_id"`
	Name        string `json:"name"`
	WrongCount  int    `json:"wrong_count"`
}

// TrendPoint 是按日练习/正确率趋势点（设计 4.12 R7）。
type TrendPoint struct {
	Date          string  `json:"date"`
	PracticeCount int     `json:"practice_count"`
	CorrectCount  int     `json:"correct_count"`
	Accuracy      float64 `json:"accuracy"`
}

// TimeDistribution 是学习时段分布（设计 4.12 R5：早/午/晚，UTC 时区口径）。
type TimeDistribution struct {
	Morning   int `json:"morning"`
	Afternoon int `json:"afternoon"`
	Evening   int `json:"evening"`
}

// ReportPayload 是报告载荷（reports.payload_json，快照不可变）。
type ReportPayload struct {
	Period           string              `json:"period"`
	PeriodStart      string              `json:"period_start"`
	PeriodEnd        string              `json:"period_end"`
	GeneratedAt      string              `json:"generated_at"`
	SchemaVersion    string              `json:"schema_version"`
	Summary          ReportSummary       `json:"summary"`
	WeakKnowledge    []WeakKnowledgeItem `json:"weak_knowledge"`
	Trend            []TrendPoint        `json:"trend"`
	TimeDistribution TimeDistribution    `json:"time_distribution"`
	InterruptReasons map[string]int      `json:"interrupt_reasons"`
	Suggestions      []string            `json:"suggestions"`
}

// KnowledgeInsight 是知识点洞察（设计 4.12 R4：正确率/练习量/最近复习时间）。
type KnowledgeInsight struct {
	KnowledgeID    string  `json:"knowledge_id"`
	Name           string  `json:"name"`
	PracticeCount  int     `json:"practice_count"`
	CorrectCount   int     `json:"correct_count"`
	Accuracy       float64 `json:"accuracy"`
	LastReviewedAt *string `json:"last_reviewed_at"`
}

// TimeInsight 是时间分析（设计 4.12 R5：时段分布/单次平均时长/专注中断原因）。
type TimeInsight struct {
	Distribution     TimeDistribution `json:"distribution"`
	AvgSessionMin    float64          `json:"avg_session_min"`
	InterruptReasons map[string]int   `json:"interrupt_reasons"`
}

// TrendInsight 是趋势洞察（设计 4.12 R7）。
type TrendInsight struct {
	Points []TrendPoint `json:"points"`
}

// Insight 是 InsightGet 响应，按 dimension 返回对应字段（API 设计文档 7.5）。
type Insight struct {
	Dimension string             `json:"dimension"`
	Knowledge []KnowledgeInsight `json:"knowledge,omitempty"`
	Time      *TimeInsight       `json:"time,omitempty"`
	Trend     *TrendInsight      `json:"trend,omitempty"`
}
