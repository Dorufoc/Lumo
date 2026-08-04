package service

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"lumo/internal/domain"
)

// GradeResult 是规则判分结果。
type GradeResult struct {
	Score    float64
	MaxScore float64
	Matched  []string // 命中项说明（如 "答案 A 正确"）
	Reason   string
}

// gradingMaxScore 返回题目满分（grading_config.max_score 或默认）。
func gradingMaxScore(p *QuestionPayload, fallback float64) float64 {
	if p.GradingConfig != nil {
		if v, ok := p.GradingConfig["max_score"].(float64); ok && v > 0 {
			return v
		}
	}
	return fallback
}

// GradeObjective 客观题规则判分（单选/多选/填空）。
// 返回判分结果；无法判分时返回错误。
func GradeObjective(p *QuestionPayload, answer json.RawMessage, maxScore float64) (GradeResult, error) {
	switch p.Type {
	case "single_choice", "multiple_choice":
		return gradeChoice(p, answer, maxScore)
	case "fill_blank":
		return gradeFillBlank(p, answer, maxScore)
	default:
		return GradeResult{}, domain.InvalidState("题型 %s 不支持规则判分", p.Type)
	}
}

// gradeChoice 判分选择题。
func gradeChoice(p *QuestionPayload, answer json.RawMessage, maxScore float64) (GradeResult, error) {
	if p.Type == "single_choice" {
		var user string
		if err := json.Unmarshal(answer, &user); err != nil {
			return GradeResult{}, domain.InvalidArg("单选题答案格式非法")
		}
		var std string
		if err := json.Unmarshal(p.Answer, &std); err != nil {
			return GradeResult{}, domain.InvalidArg("标准答案格式非法")
		}
		if strings.EqualFold(strings.TrimSpace(user), strings.TrimSpace(std)) {
			return GradeResult{Score: maxScore, MaxScore: maxScore,
				Matched: []string{"答案 " + std + " 正确"}, Reason: "答案正确"}, nil
		}
		return GradeResult{Score: 0, MaxScore: maxScore,
			Matched: []string{"答案 " + user + " 错误，正确为 " + std}, Reason: "答案错误"}, nil
	}

	// multiple_choice
	var user []string
	if err := json.Unmarshal(answer, &user); err != nil {
		return GradeResult{}, domain.InvalidArg("多选题答案格式非法（应为数组）")
	}
	var std []string
	if err := json.Unmarshal(p.Answer, &std); err != nil {
		return GradeResult{}, domain.InvalidArg("标准答案格式非法")
	}
	if len(std) == 0 {
		return GradeResult{Score: 0, MaxScore: maxScore, Matched: []string{"标准答案为空"}, Reason: "标准答案为空"}, nil
	}
	mode := "exact"
	if p.GradingConfig != nil {
		if m, ok := p.GradingConfig["mode"].(string); ok && m == "partial" {
			mode = "partial"
		}
	}
	stdSet := map[string]bool{}
	for _, s := range std {
		stdSet[s] = true
	}
	userSet := map[string]bool{}
	for _, u := range user {
		userSet[u] = true
	}
	if mode == "partial" {
		correct := 0
		wrong := 0
		for u := range userSet {
			if stdSet[u] {
				correct++
			} else {
				wrong++
			}
		}
		score := (float64(correct) - float64(wrong)) / float64(len(std))
		if score < 0 {
			score = 0
		}
		return GradeResult{Score: score * maxScore, MaxScore: maxScore,
			Matched: []string{"部分得分模式：正确 " + strconv.Itoa(correct) + " 项，错误 " + strconv.Itoa(wrong) + " 项"},
			Reason:  "部分得分"}, nil
	}
	// exact
	if len(userSet) != len(stdSet) {
		return GradeResult{Score: 0, MaxScore: maxScore,
			Matched: []string{"选项数量不一致"}, Reason: "答案错误"}, nil
	}
	for u := range userSet {
		if !stdSet[u] {
			return GradeResult{Score: 0, MaxScore: maxScore,
				Matched: []string{"含错误选项 " + u}, Reason: "答案错误"}, nil
		}
	}
	return GradeResult{Score: maxScore, MaxScore: maxScore,
		Matched: []string{"全部选项正确"}, Reason: "答案正确"}, nil
}

// gradeFillBlank 判分填空题（支持多空与数值容差）。
func gradeFillBlank(p *QuestionPayload, answer json.RawMessage, maxScore float64) (GradeResult, error) {
	// 标准答案：字符串或字符串数组
	var std []string
	var stdSingle string
	if err := json.Unmarshal(p.Answer, &stdSingle); err == nil {
		std = []string{stdSingle}
	} else if err := json.Unmarshal(p.Answer, &std); err != nil {
		return GradeResult{}, domain.InvalidArg("标准答案格式非法")
	}
	// 用户答案：字符串或字符串数组
	var user []string
	var userSingle string
	if err := json.Unmarshal(answer, &userSingle); err == nil {
		user = []string{userSingle}
	} else if err := json.Unmarshal(answer, &user); err != nil {
		return GradeResult{}, domain.InvalidArg("填空题答案格式非法")
	}
	caseSensitive := false
	numeric := false
	tolerance := 0.01
	if p.GradingConfig != nil {
		if v, ok := p.GradingConfig["case_sensitive"].(bool); ok {
			caseSensitive = v
		}
		if v, ok := p.GradingConfig["numeric"].(bool); ok {
			numeric = v
		}
		if v, ok := p.GradingConfig["tolerance"].(float64); ok && v >= 0 {
			tolerance = v
		}
	}
	matched := 0
	details := make([]string, 0, len(std))
	for i, s := range std {
		if i >= len(user) {
			details = append(details, "第 "+strconv.Itoa(i+1)+" 空未作答")
			continue
		}
		u := user[i]
		ok := compareAnswer(s, u, caseSensitive, numeric, tolerance)
		if ok {
			matched++
			details = append(details, "第 "+strconv.Itoa(i+1)+" 空正确")
		} else {
			details = append(details, "第 "+strconv.Itoa(i+1)+" 空错误")
		}
	}
	score := 0.0
	if len(std) > 0 {
		score = float64(matched) / float64(len(std)) * maxScore
	}
	return GradeResult{Score: score, MaxScore: maxScore, Matched: details,
		Reason: "答对 " + strconv.Itoa(matched) + "/" + strconv.Itoa(len(std)) + " 空"}, nil
}

// compareAnswer 比较单个答案（数值容差/大小写/去空白）。
func compareAnswer(std, user string, caseSensitive, numeric bool, tolerance float64) bool {
	if numeric {
		sv, err1 := strconv.ParseFloat(strings.TrimSpace(std), 64)
		uv, err2 := strconv.ParseFloat(strings.TrimSpace(user), 64)
		if err1 == nil && err2 == nil {
			return math.Abs(sv-uv) <= tolerance
		}
	}
	if !caseSensitive {
		std = strings.ToLower(strings.TrimSpace(std))
		user = strings.ToLower(strings.TrimSpace(user))
	} else {
		std = strings.TrimSpace(std)
		user = strings.TrimSpace(user)
	}
	return std == user
}
