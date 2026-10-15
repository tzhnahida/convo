package api

import "github.com/tzhnahida/VocabLock/internal/engine"

// questionMinutes returns the estimated time in minutes for a question type.
// Used for time-based daily target tracking.
func questionMinutes(qType string) int {
	switch engine.QuestionType(qType) {
	case engine.TypeChoice, engine.TypePronounce, engine.TypeMeaning, engine.TypeReadWord:
		return 1
	case engine.TypeFill, engine.TypeReadSentence, engine.TypeListening, engine.TypeGrammar:
		return 2
	case engine.TypeReadingComp:
		return 3
	default:
		return 1
	}
}
