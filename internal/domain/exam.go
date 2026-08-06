package domain

import "encoding/json"

// ExamPaperSectionConfig 是试卷 config_json 中的大题配置。
type ExamPaperSectionConfig struct {
	Title             string   `json:"title"`
	OrderNo           int      `json:"order_no"`
	QuestionVersionIDs []string `json:"question_version_ids"`
	Score             int      `json:"score"`
}

// ExamPaperConfig 是试卷 config_json（duration_min 只在 config_json 中，exams 表无该列）。
type ExamPaperConfig struct {
	DurationMin int                    `json:"duration_min"`
	Sections    []ExamPaperSectionConfig `json:"sections"`
}

// ExamPaperStatus 试卷状态（0005_student.sql CHECK）。
const (
	ExamPaperStatusDraft     = "draft"
	ExamPaperStatusPublished = "published"
	ExamPaperStatusArchived  = "archived"
)

// ExamStatus 考试状态（设计文档 4.10：created/answering/graded）。
const (
	ExamStatusCreated   = "created"
	ExamStatusAnswering = "answering"
	ExamStatusGraded    = "graded"
)

// ParseExamPaperConfig 解析并校验 config_json。
func ParseExamPaperConfig(raw json.RawMessage) (*ExamPaperConfig, error) {
	if len(raw) == 0 || !json.Valid(raw) {
		return nil, InvalidArg("config_json 必须是合法 JSON")
	}
	var cfg ExamPaperConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, InvalidArg("config_json 解析失败: %v", err)
	}
	return &cfg, nil
}

// Validate 校验试卷配置结构（顺序号唯一、题目版本非空、分值非负）。
func (c *ExamPaperConfig) Validate() error {
	if c.DurationMin < 0 {
		return InvalidArg("duration_min 不能为负")
	}
	if len(c.Sections) == 0 {
		return InvalidArg("试卷至少需要一个大题")
	}
	seen := map[int]bool{}
	for i, s := range c.Sections {
		if s.Title == "" {
			return InvalidArg("第 %d 个大题缺少标题", i+1)
		}
		if len(s.Title) > 200 {
			return InvalidArg("大题标题长度不能超过 200")
		}
		if s.OrderNo <= 0 {
			return InvalidArg("第 %d 个大题 order_no 必须大于 0", i+1)
		}
		if seen[s.OrderNo] {
			return InvalidArg("大题 order_no %d 重复", s.OrderNo)
		}
		seen[s.OrderNo] = true
		if len(s.QuestionVersionIDs) == 0 {
			return InvalidArg("大题 %q 未包含任何题目", s.Title)
		}
		if s.Score < 0 {
			return InvalidArg("大题 %q 分值不能为负", s.Title)
		}
	}
	return nil
}

// Duration 返回考试时长（分钟）；未配置时返回 0。
func (c *ExamPaperConfig) Duration() int {
	if c == nil {
		return 0
	}
	return c.DurationMin
}

// SectionTitles 返回各题标题（组卷展示用）。
func (c *ExamPaperConfig) SectionTitles() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, len(c.Sections))
	for _, s := range c.Sections {
		out = append(out, s.Title)
	}
	return out
}
