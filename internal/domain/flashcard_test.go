package domain

import (
	"math"
	"testing"
	"time"
)

// almostEqual 比较浮点数（ease_factor 累加存在二进制误差）。
func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestValidateFlashcardRating(t *testing.T) {
	for _, v := range []string{RatingAgain, RatingHard, RatingGood} {
		if !ValidateFlashcardRating(v) {
			t.Errorf("ValidateFlashcardRating(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "easy", "AGAIN", "again ", "perfect", "good2"} {
		if ValidateFlashcardRating(v) {
			t.Errorf("ValidateFlashcardRating(%q) = true, want false", v)
		}
	}
}

func TestValidateFlashcardSource(t *testing.T) {
	for _, v := range []string{FlashcardSourceKnowledge, FlashcardSourceNote, FlashcardSourceDocument, FlashcardSourceManual} {
		if !ValidateFlashcardSource(v) {
			t.Errorf("ValidateFlashcardSource(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "video", "question", "manual2"} {
		if ValidateFlashcardSource(v) {
			t.Errorf("ValidateFlashcardSource(%q) = true, want false", v)
		}
	}
}

func TestValidateFlashcardState(t *testing.T) {
	for _, v := range []string{FlashcardStateLearning, FlashcardStateReview, FlashcardStateMastered, FlashcardStateArchived} {
		if !ValidateFlashcardState(v) {
			t.Errorf("ValidateFlashcardState(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "pending", "done"} {
		if ValidateFlashcardState(v) {
			t.Errorf("ValidateFlashcardState(%q) = true, want false", v)
		}
	}
}

func TestValidateFlashcardCardType(t *testing.T) {
	for _, v := range []string{FlashcardTypeBasic, FlashcardTypeChoice, FlashcardTypeCloze, FlashcardTypeCode} {
		if !ValidateFlashcardCardType(v) {
			t.Errorf("ValidateFlashcardCardType(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "video", "BASIC"} {
		if ValidateFlashcardCardType(v) {
			t.Errorf("ValidateFlashcardCardType(%q) = true, want false", v)
		}
	}
}

// again 重置 repetition，间隔回到 1 天，ease 下降但不低于 1.3。
func TestApplyFlashcardSM2AgainResets(t *testing.T) {
	next := ApplyFlashcardSM2(4, 30, 2.5, RatingAgain)
	if next.Repetition != 0 {
		t.Errorf("again repetition = %d, want 0", next.Repetition)
	}
	if next.IntervalDays != 1 {
		t.Errorf("again interval_days = %d, want 1", next.IntervalDays)
	}
	if !almostEqual(next.EaseFactor, 2.3) {
		t.Errorf("again ease_factor = %.2f, want 2.3", next.EaseFactor)
	}
	if due, err := time.Parse(time.RFC3339, next.DueAt); err != nil {
		t.Errorf("again due_at not RFC3339: %q: %v", next.DueAt, err)
	} else if !due.After(time.Now().UTC()) {
		t.Errorf("again due_at %q should be in the future", next.DueAt)
	}
}

func TestApplyFlashcardSM2Hard(t *testing.T) {
	// rep 1→2：hard 保持 3 天
	next := ApplyFlashcardSM2(1, 1, 2.5, RatingHard)
	if next.Repetition != 2 {
		t.Errorf("hard repetition = %d, want 2", next.Repetition)
	}
	if next.IntervalDays != 3 {
		t.Errorf("hard interval_days = %d, want 3", next.IntervalDays)
	}
	// rep 3+：interval = int(interval*ease*0.9)+1
	next = ApplyFlashcardSM2(3, 9, 2.8, RatingHard)
	if next.Repetition != 4 {
		t.Errorf("hard repetition = %d, want 4", next.Repetition)
	}
	hardEase := 2.8
	wantInterval := int(9.0*hardEase*0.9) + 1
	if next.IntervalDays != wantInterval {
		t.Errorf("hard interval_days = %d, want %d", next.IntervalDays, wantInterval)
	}
	if !almostEqual(next.EaseFactor, 2.65) {
		t.Errorf("hard ease_factor = %.2f, want 2.65", next.EaseFactor)
	}
}

func TestApplyFlashcardSM2GoodProgression(t *testing.T) {
	next := ApplyFlashcardSM2(0, 0, 2.5, RatingGood)
	if next.Repetition != 1 || next.IntervalDays != 1 {
		t.Errorf("first good = %+v, want rep 1 / interval 1", next)
	}
	next = ApplyFlashcardSM2(next.Repetition, next.IntervalDays, next.EaseFactor, RatingGood)
	if next.Repetition != 2 || next.IntervalDays != 3 {
		t.Errorf("second good = %+v, want rep 2 / interval 3", next)
	}
	prev := next.IntervalDays
	next = ApplyFlashcardSM2(next.Repetition, next.IntervalDays, next.EaseFactor, RatingGood)
	if next.IntervalDays <= prev {
		t.Errorf("interval should grow, prev=%d next=%d", prev, next.IntervalDays)
	}
	if !almostEqual(next.EaseFactor, 2.8) {
		t.Errorf("third good ease_factor = %.2f, want 2.8", next.EaseFactor)
	}
}

func TestApplyFlashcardSM2EaseClamps(t *testing.T) {
	if e := ApplyFlashcardSM2(0, 0, 3.5, RatingGood).EaseFactor; !almostEqual(e, 3.5) {
		t.Errorf("good ease should clamp to 3.5, got %.2f", e)
	}
	if e := ApplyFlashcardSM2(0, 0, 1.3, RatingAgain).EaseFactor; !almostEqual(e, 1.3) {
		t.Errorf("again ease should clamp to 1.3, got %.2f", e)
	}
	if e := ApplyFlashcardSM2(0, 0, 1.3, RatingHard).EaseFactor; !almostEqual(e, 1.3) {
		t.Errorf("hard ease should clamp to 1.3, got %.2f", e)
	}
}

func TestFlashcardNextState(t *testing.T) {
	cases := []struct {
		name   string
		rating string
		sm2    FlashcardSM2
		prev   []string
		want   string
	}{
		{
			name: "mastered: 3 consecutive good + interval >= 21",
			rating: RatingGood,
			sm2:    FlashcardSM2{Repetition: 4, IntervalDays: 22, EaseFactor: 2.8},
			prev:   []string{RatingGood, RatingGood},
			want:   FlashcardStateMastered,
		},
		{
			name: "not mastered when interval < 21",
			rating: RatingGood,
			sm2:    FlashcardSM2{Repetition: 2, IntervalDays: 9, EaseFactor: 2.7},
			prev:   []string{RatingGood, RatingGood},
			want:   FlashcardStateReview,
		},
		{
			name: "not mastered when good streak broken",
			rating: RatingGood,
			sm2:    FlashcardSM2{Repetition: 4, IntervalDays: 22, EaseFactor: 2.8},
			prev:   []string{RatingGood, RatingHard},
			want:   FlashcardStateReview,
		},
		{
			name: "again drops back to learning",
			rating: RatingAgain,
			sm2:    FlashcardSM2{Repetition: 0, IntervalDays: 1, EaseFactor: 2.3},
			prev:   []string{RatingGood, RatingGood},
			want:   FlashcardStateLearning,
		},
		{
			name: "first good stays review",
			rating: RatingGood,
			sm2:    FlashcardSM2{Repetition: 1, IntervalDays: 1, EaseFactor: 2.6},
			prev:   nil,
			want:   FlashcardStateReview,
		},
	}
	for _, c := range cases {
		if got := FlashcardNextState(c.rating, c.sm2, c.prev); got != c.want {
			t.Errorf("%s: FlashcardNextState = %q, want %q", c.name, got, c.want)
		}
	}
}
