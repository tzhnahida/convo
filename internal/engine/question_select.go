package engine

import (
	"math/rand"
	"time"
)

// tierTypes maps SM-2 learning stages to appropriate question types.
// Higher tier = more demanding question types.
// Tier progression based on cognitive load:
//
//	tier 0: recognition + shadowing (choice, meaning, read aloud)
//	tier 1: same types, words with review history
//	tier 2: cued + free recall (fill, spelling — production)
//	tier 3: sentence-level production + comprehension
var tierTypes = map[int][]QuestionType{
	0: {TypeChoice, TypePronounce, TypeMeaning, TypeReadWord},
	1: {TypeChoice, TypePronounce, TypeMeaning, TypeReadWord},
	2: {TypeFill, TypePronounce, TypeMeaning, TypeSpelling, TypeReadWord},
	3: {TypePronounce, TypeMeaning, TypeSpelling, TypeReadWord, TypeListening, TypeGrammar, TypeReadSentence, TypeReadingComp},
}

// SelectQuestionType picks the best question type for a word based on its SM-2 state.
//
// Tier calculation:
//
//	nextReviewAt == nil → tier 0 (new, never reviewed)
//	reps 0-1, interval ≤ 3 → tier 1 (early)
//	reps 2-3 → tier 2 (mid)
//	reps ≥ 4, interval > 14 → tier 3 (mature)
//	otherwise → tier 2 (default)
//
// EF < 1.5 drops one tier (struggling word needs easier questions).
//
// available is the list of question types enabled for this language (from config).
// aiAvailable indicates whether AI-dependent types (listening/reading_comp/grammar) can be used.
func SelectQuestionType(nextReviewAt *time.Time, ef float64, interval, reps int, available []QuestionType, aiAvailable, pronounceAvailable bool) QuestionType {
	candidates := selectCandidates(nextReviewAt, ef, interval, reps, available, aiAvailable, pronounceAvailable)
	return candidates[rand.Intn(len(candidates))]
}

// SelectQuestionTypeWithWeights is like SelectQuestionType but biases the random
// pick toward the given type weights (higher weight = more likely). Weights are
// the plan's question_type_ratio percentages; types not in the map get a
// default weight of 10.
func SelectQuestionTypeWithWeights(nextReviewAt *time.Time, ef float64, interval, reps int, available []QuestionType, aiAvailable, pronounceAvailable bool, weights map[string]int) QuestionType {
	candidates := selectCandidates(nextReviewAt, ef, interval, reps, available, aiAvailable, pronounceAvailable)
	if len(weights) == 0 || len(candidates) <= 1 {
		if len(candidates) == 0 {
			return TypeChoice
		}
		return candidates[rand.Intn(len(candidates))]
	}

	totalWeight := 0
	for _, qt := range candidates {
		if w, ok := weights[string(qt)]; ok && w > 0 {
			totalWeight += w
		} else {
			totalWeight += 10 // default for unweighted types
		}
	}
	if totalWeight <= 0 {
		return candidates[rand.Intn(len(candidates))]
	}

	r := rand.Intn(totalWeight)
	for _, qt := range candidates {
		w := weights[string(qt)]
		if w <= 0 {
			w = 10
		}
		r -= w
		if r < 0 {
			return qt
		}
	}
	return candidates[len(candidates)-1]
}

func selectCandidates(nextReviewAt *time.Time, ef float64, interval, reps int, available []QuestionType, aiAvailable, pronounceAvailable bool) []QuestionType {
	tier := computeTier(nextReviewAt, interval, reps)
	if ef < 1.5 && tier > 0 {
		tier--
	}

	allowed := tierTypes[tier]

	availableSet := make(map[QuestionType]bool, len(available))
	for _, qt := range available {
		availableSet[qt] = true
	}

	candidates := make([]QuestionType, 0, len(allowed))
	for _, qt := range allowed {
		if !availableSet[qt] {
			continue
		}
		if isAIType(qt) && !aiAvailable {
			continue
		}
		if NeedsPronounce(qt) && !pronounceAvailable {
			continue
		}
		candidates = append(candidates, qt)
	}

	// Fall back: if no candidates in current tier, try lower tiers
	for t := tier - 1; t >= 0 && len(candidates) == 0; t-- {
		for _, qt := range tierTypes[t] {
			if availableSet[qt] && !(isAIType(qt) && !aiAvailable) && !(NeedsPronounce(qt) && !pronounceAvailable) {
				candidates = append(candidates, qt)
			}
		}
	}

	if len(candidates) == 0 {
		return []QuestionType{TypeChoice}
	}
	return candidates
}

func computeTier(nextReviewAt *time.Time, interval, reps int) int {
	if nextReviewAt == nil {
		return 0
	}
	if reps <= 1 && interval <= 3 {
		return 1
	}
	if reps >= 4 && interval > 14 {
		return 3
	}
	return 2
}

func isAIType(qt QuestionType) bool {
	switch qt {
	case TypeListening, TypeReadingComp, TypeGrammar, TypeReadSentence:
		return true
	}
	return false
}

// NeedsPronounce returns true if the question type requires a pronunciation
// assessment provider. These types are excluded when no provider is configured.
func NeedsPronounce(qt QuestionType) bool {
	switch qt {
	case TypeReadWord, TypeReadSentence:
		return true
	}
	return false
}
