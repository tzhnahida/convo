package engine

import (
	"testing"
	"time"
)

func TestSelectQuestionType_NewWord(t *testing.T) {
	// New word, never reviewed → tier 0 (recognition + shadowing).
	// The pick is randomized (rand.Intn over candidates), so the old
	// "must be choice" assertion was flaky by design — assert the
	// sampled set belongs to tier 0 and shows variety instead.
	types := []QuestionType{TypeChoice, TypeFill, TypePronounce, TypeSpelling, TypeListening, TypeReadingComp, TypeGrammar}
	tier0 := map[QuestionType]bool{TypeChoice: true, TypePronounce: true, TypeMeaning: true, TypeReadWord: true}
	seen := make(map[QuestionType]int)
	for i := 0; i < 50; i++ {
		qt := SelectQuestionType(nil, 2.5, 0, 0, types, true, false)
		seen[qt]++
		if !tier0[qt] {
			t.Errorf("new word got %s — not a tier-0 type", qt)
		}
	}
	if len(seen) < 2 {
		t.Errorf("expected variety across tier-0 types, got only %v", seen)
	}
}

func TestSelectQuestionType_EarlyStage(t *testing.T) {
	now := time.Now()
	next := now.AddDate(0, 0, 1) // 1 day interval
	types := []QuestionType{TypeChoice, TypeFill, TypePronounce}

	// reps=0, interval=1 → tier 1: choice or pronounce (fill removed from tier 1)
	seen := make(map[QuestionType]int)
	for i := 0; i < 50; i++ {
		qt := SelectQuestionType(&next, 2.5, 1, 0, types, false, false)
		seen[qt]++
	}

	if seen[TypeChoice]+seen[TypePronounce] == 0 {
		t.Error("early stage should get choice or pronounce")
	}
	if seen[TypeFill] > 0 {
		t.Errorf("early stage should not get fill, got %d times", seen[TypeFill])
	}
}

func TestSelectQuestionType_MidStage(t *testing.T) {
	now := time.Now()
	next := now.AddDate(0, 0, 5) // 5 day interval
	types := []QuestionType{TypeChoice, TypeFill, TypePronounce, TypeSpelling}

	// reps=2, interval=5 → tier 2: fill or reading
	seen := make(map[QuestionType]int)
	for i := 0; i < 50; i++ {
		qt := SelectQuestionType(&next, 2.5, 5, 2, types, false, false)
		seen[qt]++
	}

	if seen[TypeChoice] > 0 {
		t.Errorf("mid stage should not get choice, got %d times", seen[TypeChoice])
	}
}

func TestSelectQuestionType_MatureStage(t *testing.T) {
	now := time.Now()
	next := now.AddDate(0, 0, 30) // 30 day interval
	types := []QuestionType{TypeChoice, TypeFill, TypePronounce, TypeSpelling, TypeListening}

	// reps=5, interval=30 → tier 3
	seen := make(map[QuestionType]int)
	for i := 0; i < 50; i++ {
		qt := SelectQuestionType(&next, 2.5, 30, 5, types, true, false)
		seen[qt]++
	}

	if seen[TypeChoice] > 0 || seen[TypeFill] > 0 {
		t.Errorf("mature stage should not get choice/fill, got choice=%d fill=%d", seen[TypeChoice], seen[TypeFill])
	}
}

func TestSelectQuestionType_StrugglingWord(t *testing.T) {
	now := time.Now()
	next := now.AddDate(0, 0, 5) // mid stage by interval
	types := []QuestionType{TypeChoice, TypeFill, TypePronounce}

	// EF=1.4 (< 1.5) should drop tier 2 → tier 1
	// Tier 1: choice, pronounce. Fill not in tier 1.
	seen := make(map[QuestionType]int)
	for i := 0; i < 50; i++ {
		qt := SelectQuestionType(&next, 1.4, 5, 2, types, false, false)
		seen[qt]++
	}

	if seen[TypeFill] > 0 {
		t.Errorf("struggling word should not get fill, got %d times", seen[TypeFill])
	}
}

func TestSelectQuestionType_NoAITypesWhenUnavailable(t *testing.T) {
	now := time.Now()
	next := now.AddDate(0, 0, 30)
	types := []QuestionType{TypeChoice, TypeFill, TypePronounce, TypeSpelling, TypeListening, TypeReadingComp, TypeGrammar}

	// reps=5, interval=30 → tier 3, but AI unavailable
	for i := 0; i < 50; i++ {
		qt := SelectQuestionType(&next, 2.5, 30, 5, types, false, false)
		if qt == TypeListening || qt == TypeReadingComp || qt == TypeGrammar {
			t.Errorf("AI type %s should not appear when AI unavailable", qt)
		}
	}
}

func TestSelectQuestionType_FallbackWhenNoCandidates(t *testing.T) {
	now := time.Now()
	next := now.AddDate(0, 0, 30)
	// Only AI types available, but AI unavailable → falls back to choice
	types := []QuestionType{TypeListening, TypeReadingComp, TypeGrammar}

	qt := SelectQuestionType(&next, 2.5, 30, 5, types, false, false)
	if qt != TypeChoice {
		t.Errorf("should fall back to choice when no candidates, got %s", qt)
	}
}

func TestSelectQuestionType_RespectsAvailableTypes(t *testing.T) {
	now := time.Now()
	next := now.AddDate(0, 0, 1) // tier 1

	// Pronounce IS a tier-1 type, so it should be returned directly.
	types := []QuestionType{TypePronounce}

	for i := 0; i < 20; i++ {
		qt := SelectQuestionType(&next, 2.5, 1, 0, types, false, false)
		if qt != TypePronounce {
			t.Errorf("pronounce is tier 1, should be returned, got %s", qt)
		}
	}
}

func TestComputeTier(t *testing.T) {
	tests := []struct {
		name     string
		next     *time.Time
		interval int
		reps     int
		want     int
	}{
		{"new word", nil, 0, 0, 0},
		{"first review", timePtr(time.Now().AddDate(0, 0, 1)), 1, 0, 1},
		{"second review short", timePtr(time.Now().AddDate(0, 0, 3)), 3, 1, 1},
		{"third review medium", timePtr(time.Now().AddDate(0, 0, 7)), 7, 2, 2},
		{"fourth review medium", timePtr(time.Now().AddDate(0, 0, 10)), 10, 3, 2},
		{"mature long interval", timePtr(time.Now().AddDate(0, 0, 30)), 30, 4, 3},
		{"mature very long", timePtr(time.Now().AddDate(0, 0, 60)), 60, 6, 3},
		{"high reps short interval", timePtr(time.Now().AddDate(0, 0, 5)), 5, 5, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeTier(tt.next, tt.interval, tt.reps)
			if got != tt.want {
				t.Errorf("computeTier() = %d, want %d", got, tt.want)
			}
		})
	}
}

func timePtr(t time.Time) *time.Time {
	return &t
}
