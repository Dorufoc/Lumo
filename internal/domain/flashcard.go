package domain

import (
	"math"
	"time"
)

// 闪卡评级（4.9 F4：评级只允许 again/hard/good，前端仅传 rating）。
const (
	RatingAgain = "again"
	RatingHard  = "hard"
	RatingGood  = "good"
)

// 闪卡来源（6.2.1 flashcards.source 枚举）。
const (
	FlashcardSourceKnowledge = "knowledge"
	FlashcardSourceNote      = "note"
	FlashcardSourceDocument  = "document"
	FlashcardSourceManual    = "manual"
)

// 闪卡状态（6.2.1 flashcards.state 枚举）。
const (
	FlashcardStateLearning = "learning"
	FlashcardStateReview   = "review"
	FlashcardStateMastered = "mastered"
	FlashcardStateArchived = "archived"
)

// 闪卡题型（4.9 F2：问答/选择/填空/代码）。
const (
	FlashcardTypeBasic  = "basic"
	FlashcardTypeChoice = "choice"
	FlashcardTypeCloze  = "cloze"
	FlashcardTypeCode   = "code"
)

// FlashcardMasteredInterval / FlashcardMasteredStreak 达标阈值（4.9 F4）：
// interval_days ≥ 21 且连续 3 次 good 即 mastered。
const (
	FlashcardMasteredInterval = 21
	FlashcardMasteredStreak   = 3
)

// Flashcard 是闪卡实体（4.9 数据模型，字段与 0005_student.sql flashcards 表一致）。
type Flashcard struct {
	ID           string
	WorkspaceID  string
	UserID       string
	Source       string
	SourceRef    string
	Front        string
	Back         string
	CardType     string
	State        string
	Repetition   int
	IntervalDays int
	EaseFactor   float64
	DueAt        string
	CreatedAt    string
	UpdatedAt    string
	DeletedAt    *string
	Version      int
}

// ValidateFlashcardRating 校验评级枚举。
func ValidateFlashcardRating(rating string) bool {
	return rating == RatingAgain || rating == RatingHard || rating == RatingGood
}

// ValidateFlashcardSource 校验来源枚举。
func ValidateFlashcardSource(source string) bool {
	return source == FlashcardSourceKnowledge || source == FlashcardSourceNote ||
		source == FlashcardSourceDocument || source == FlashcardSourceManual
}

// ValidateFlashcardState 校验状态枚举。
func ValidateFlashcardState(state string) bool {
	return state == FlashcardStateLearning || state == FlashcardStateReview ||
		state == FlashcardStateMastered || state == FlashcardStateArchived
}

// ValidateFlashcardCardType 校验题型枚举。
func ValidateFlashcardCardType(cardType string) bool {
	return cardType == FlashcardTypeBasic || cardType == FlashcardTypeChoice ||
		cardType == FlashcardTypeCloze || cardType == FlashcardTypeCode
}

// FlashcardSM2 是简化 SM-2 调度计算结果（间隔只由服务端计算）。
type FlashcardSM2 struct {
	Repetition   int
	IntervalDays int
	EaseFactor   float64
	DueAt        string
}

// ApplyFlashcardSM2 计算评级后的新调度，与复习模块（review.go sm2Update）共用同一套
// 简化 SM-2 体系（4.5/4.9：共用计算体系）：
//   - again：repetition 归 0，间隔回 1 天，ease -0.2（下限 1.3）；
//   - hard：repetition +1，间隔 1/3/阶梯，ease -0.15（下限 1.3）；
//   - good：repetition +1，间隔 1/3/阶梯（interval*ease），ease +0.1（上限 3.5）。
func ApplyFlashcardSM2(repetition, intervalDays int, easeFactor float64, rating string) FlashcardSM2 {
	rep := repetition
	interval := intervalDays
	ease := easeFactor
	now := time.Now().UTC()

	switch rating {
	case RatingAgain:
		rep = 0
		interval = 1
		ease = math.Max(1.3, ease-0.2)
	case RatingHard:
		rep++
		switch rep {
		case 1:
			interval = 1
		case 2:
			interval = 3
		default:
			interval = int(float64(interval)*ease*0.9) + 1
		}
		ease = math.Max(1.3, ease-0.15)
	case RatingGood:
		rep++
		switch rep {
		case 1:
			interval = 1
		case 2:
			interval = 3
		default:
			interval = int(float64(interval)*ease) + 1
		}
		ease = math.Min(3.5, ease+0.1)
	}
	return FlashcardSM2{
		Repetition:   rep,
		IntervalDays: interval,
		EaseFactor:   ease,
		DueAt:        now.AddDate(0, 0, interval).Format(time.RFC3339),
	}
}

// FlashcardNextState 依据本次评级与调度结果推导新状态（4.9 F4）：
// 本次 good 且 interval ≥ 21 且此前已有 2 次 good（连续 3 次）→ mastered；
// 否则 repetition > 0 → review；否则 → learning。
func FlashcardNextState(rating string, sm2 FlashcardSM2, prevRatings []string) string {
	if rating == RatingGood && sm2.IntervalDays >= FlashcardMasteredInterval &&
		len(prevRatings) >= FlashcardMasteredStreak-1 {
		streak := true
		for _, r := range prevRatings[len(prevRatings)-(FlashcardMasteredStreak-1):] {
			if r != RatingGood {
				streak = false
				break
			}
		}
		if streak {
			return FlashcardStateMastered
		}
	}
	if sm2.Repetition > 0 {
		return FlashcardStateReview
	}
	return FlashcardStateLearning
}
