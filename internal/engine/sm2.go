package engine

import (
	"math"
	"time"
)

const (
	minEasinessFactor = 1.3
	maxQuality        = 5
)

// SM2Result is the output of an SM-2 review calculation.
type SM2Result struct {
	EasinessFactor float64
	Interval       int // days until next review
	Repetitions    int
	NextReviewAt   time.Time
}

// SM2 computes the next review schedule using the SM-2 algorithm.
//
// quality: 0-5 where 0=complete blackout, 5=perfect recall.
// maxInterval: maximum interval in days (0 = no limit).
// Returns nil if quality is invalid.
func SM2(ef float64, interval, reps int, quality int, now time.Time, maxInterval int) *SM2Result {
	if quality < 0 || quality > maxQuality {
		return nil
	}

	r := &SM2Result{
		EasinessFactor: ef,
		Repetitions:    reps,
	}

	if quality >= 3 {
		// Correct response
		switch reps {
		case 0:
			r.Interval = 1
		case 1:
			r.Interval = 6
		default:
			r.Interval = int(math.Round(float64(interval) * ef))
		}
		r.Repetitions = reps + 1
	} else {
		// Incorrect response — reset
		r.Interval = 1
		r.Repetitions = 0
	}

	// Clamp to max interval
	if maxInterval > 0 && r.Interval > maxInterval {
		r.Interval = maxInterval
	}

	// Update easiness factor
	r.EasinessFactor = ef + (0.1 - float64(maxQuality-quality)*(0.08+float64(maxQuality-quality)*0.02))
	if r.EasinessFactor < minEasinessFactor {
		r.EasinessFactor = minEasinessFactor
	}

	r.NextReviewAt = now.AddDate(0, 0, r.Interval)
	return r
}

// QualityFromScore maps a 0.0–1.0 score to SM-2 quality 0–5.
func QualityFromScore(score float64) int {
	switch {
	case score >= 1.0:
		return 5
	case score >= 0.8:
		return 4
	case score >= 0.6:
		return 3
	case score >= 0.4:
		return 2
	case score >= 0.2:
		return 1
	default:
		return 0
	}
}

// IsLongTerm returns true when a word has reached SM-2 tier 3 (long-term memory).
// Tier 3 triggers sense expansion: new dissimilar meanings can be unlocked.
func IsLongTerm(reps int, interval int) bool {
	return reps >= 4 && interval > 14
}

// QualityFromPassiveRecall maps a passive recall self-assessment to SM-2 quality.
// Used for passive quiz popup submissions where there's no scored answer.
// recall: "easy" → 4, "hard" → 3, "forgot" → 1, empty/other → 3 (neutral acknowledgement)
func QualityFromPassiveRecall(recall string) int {
	switch recall {
	case "easy":
		return 4
	case "hard":
		return 3
	case "forgot":
		return 1
	default:
		return 3
	}
}
