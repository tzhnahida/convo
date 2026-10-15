package db

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tzhnahida/VocabLock/internal/clock"
)

// Diagnostics aggregates learning-quality signals for the diagnostics
// endpoint: error-category distribution, weak phonemes, AI-vs-deterministic
// disagreement, per-type error rates and vocabulary coverage.
//
// All windows are "last `days` days". The phoneme/AI sections parse the
// structured rows that have been stored since the 2026-09-02 schema update;
// older rows keep their string-encoded feedback and are not backfilled.
type Diagnostics struct {
	Days           int                 `json:"days"`
	ByCategory     map[string]int      `json:"by_category"`     // error_category counts (wrong answers)
	WeakPhonemes   map[string]int      `json:"weak_phonemes"`   // phonemes scored < 0.45
	PhonemeSamples int                 `json:"phoneme_samples"` // read-aloud rows with structured phonemes
	AIDisagreement *AIDisagreement     `json:"ai_disagreement"`
	TypeRates      map[string]TypeRate `json:"type_rates"`
	Coverage       Coverage            `json:"coverage"`
}

type AIDisagreement struct {
	AIVerdicts       int      `json:"ai_verdicts"`
	WrongButExactHit int      `json:"wrong_but_exact_hit"` // AI said wrong while user answer == expected (AI false negative)
	Samples          []string `json:"samples"`             // up to 5 user_answer|expected pairs
}

type TypeRate struct {
	Total   int `json:"total"`
	Correct int `json:"correct"`
}

type Coverage struct {
	TotalWords    int `json:"total_words"`
	PendingReview int `json:"pending_review"`
	LongTermWords int `json:"long_term_words"` // reps >= 4 && interval > 14 days
}

// Diagnostics aggregates learning-quality signals for one language.
func (d *Database) Diagnostics(language string, days int) (*Diagnostics, error) {
	if days <= 0 || days > 365 {
		days = 30
	}
	since := clock.Now().AddDate(0, 0, -days).Format("2006-01-02 15:04:05")
	res := &Diagnostics{Days: days, ByCategory: map[string]int{}, WeakPhonemes: map[string]int{}, TypeRates: map[string]TypeRate{}}

	// ——— Error categories (wrong answers, last `days`) ———
	rows, err := d.DB.Query(`SELECT COALESCE(error_category,''), COUNT(*) FROM review_log
		WHERE language = ? AND correct = 0 AND created_at >= ? GROUP BY error_category`, language, since)
	if err != nil {
		return nil, fmt.Errorf("diagnostics categories: %w", err)
	}
	for rows.Next() {
		var cat string
		var n int
		if err := rows.Scan(&cat, &n); err == nil {
			res.ByCategory[cat] = n
		}
	}
	rows.Close()

	// ——— Weak phonemes + sample count (read-aloud, structured rows) ———
	prows, err := d.DB.Query(`SELECT phonemes_json FROM review_log
		WHERE language = ? AND phonemes_json != '' AND created_at >= ? LIMIT 500`, language, since)
	if err == nil {
		for prows.Next() {
			var raw string
			if err := prows.Scan(&raw); err != nil || raw == "" {
				continue
			}
			var scores []struct {
				Phoneme string  `json:"phoneme"`
				Score   float64 `json:"score"`
			}
			if json.Unmarshal([]byte(raw), &scores) != nil {
				continue
			}
			res.PhonemeSamples++
			for _, ps := range scores {
				if ps.Score < 0.45 {
					res.WeakPhonemes[ps.Phoneme]++
				}
			}
		}
		prows.Close()
	}

	// ——— AI disagreement: AI said WRONG while the user answer matches
	// the expected answer exactly (normalized). These are AI false
	// negatives — the cheapest trust signal. ———
	var aiTotal, aiWrongButExact int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM review_log WHERE language = ? AND source = 'ai' AND created_at >= ?`,
		language, since).Scan(&aiTotal); err != nil {
		aiTotal = 0
	}
	drows, err := d.DB.Query(`SELECT user_answer, expected_answer FROM review_log
		WHERE language = ? AND source = 'ai' AND correct = 0 AND created_at >= ? LIMIT 2000`, language, since)
	samples := []string{}
	if err == nil {
		for drows.Next() {
			var ua, ea string
			if err := drows.Scan(&ua, &ea); err != nil {
				continue
			}
			if normEq(ua, ea) {
				aiWrongButExact++
				if len(samples) < 5 {
					samples = append(samples, ua+"|"+ea)
				}
			}
		}
		drows.Close()
	}
	if aiTotal > 0 {
		res.AIDisagreement = &AIDisagreement{
			AIVerdicts:       aiTotal,
			WrongButExactHit: aiWrongButExact,
			Samples:          samples,
		}
	}

	// ——— Per-type rates ———
	trows, err := d.DB.Query(`SELECT question_type, COUNT(*), COALESCE(SUM(correct),0) FROM review_log
		WHERE language = ? AND created_at >= ? GROUP BY question_type`, language, since)
	if err == nil {
		for trows.Next() {
			var qt string
			var total, correct int
			if err := trows.Scan(&qt, &total, &correct); err == nil {
				res.TypeRates[qt] = TypeRate{Total: total, Correct: correct}
			}
		}
		trows.Close()
	}

	// ——— Coverage ———
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM words WHERE language = ? AND status != 'pending'`, language).Scan(&res.Coverage.TotalWords); err != nil {
		res.Coverage.TotalWords = 0
	}
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM words WHERE language = ? AND status = 'pending'`, language).Scan(&res.Coverage.PendingReview); err != nil {
		res.Coverage.PendingReview = 0
	}
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM words WHERE language = ? AND reps >= 4 AND interval > 14`, language).Scan(&res.Coverage.LongTermWords); err != nil {
		res.Coverage.LongTermWords = 0
	}

	return res, nil
}

// normEq mirrors judge.normalize (lower/trim/collapse spaces) for the
// disagreement heuristic without importing the engine package.
func normEq(a, b string) bool {
	n := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		return strings.Join(strings.Fields(s), " ")
	}
	return n(a) == n(b)
}
