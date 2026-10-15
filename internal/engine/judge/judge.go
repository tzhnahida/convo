package judge

import (
	"regexp"
	"strings"
)

type Source string

const (
	SourceExact         Source = "exact"
	SourceRegex         Source = "regex"
	SourceAI            Source = "ai"
	SourceRegexFallback Source = "regex_fallback"
)

type Strictness string

const (
	Strict  Strictness = "strict"
	Normal  Strictness = "normal"
	Lenient Strictness = "lenient"
)

type Result struct {
	Correct  bool    `json:"correct"`
	Score    float64 `json:"score"`
	Feedback string  `json:"feedback"`
	Source   Source  `json:"source"`
	// ErrorCategory classifies a WRONG verdict for diagnostics:
	// "similarity" (user's answer close to expected — typical typos/
	// confusion pairs), "empty" (no answer / don't know), "" (correct).
	// Deterministic heuristic today; the AI tier can override with a
	// richer category in a future prompt change.
	ErrorCategory string `json:"error_category"`
}

// Exact compares the user answer exactly against the expected answer.
func Exact(userAnswer, expected string) *Result {
	ua := normalize(userAnswer)
	ea := normalize(expected)
	if ua == ea {
		return &Result{Correct: true, Score: 1.0, Feedback: "Correct", Source: SourceExact}
	}
	return nil // not an exact match, try next tier
}

// Regex scores the user answer against the expected using pattern matching.
// Returns a score 0.0–1.0 indicating confidence.
func Regex(userAnswer, expected string, strictness Strictness) *Result {
	ua := normalize(userAnswer)
	ea := normalize(expected)

	// Exact substring match gets high confidence
	if strings.Contains(ua, ea) || strings.Contains(ea, ua) {
		score := 0.9
		if strictness == Strict {
			score = 0.7
		}
		return &Result{
			Correct:  true,
			Score:    score,
			Feedback: "Close match",
			Source:   SourceRegex,
		}
	}

	// For lenient mode, try fuzzy matching
	if strictness == Lenient {
		if fuzzyMatch(ua, ea) {
			return &Result{
				Correct:  true,
				Score:    0.7,
				Feedback: "Approximate match",
				Source:   SourceRegex,
			}
		}
	}

	// No match — return low-confidence result
	if strictness == Lenient {
		return &Result{
			Correct:  false,
			Score:    0.3,
			Feedback: "No match found",
			Source:   SourceRegex,
		}
	}
	return &Result{
		Correct:  false,
		Score:    0.0,
		Feedback: "Incorrect",
		Source:   SourceRegex,
	}
}

// Judge runs the 3-tier chain: exact → regex → (ai callback if provided).
// expected is a list of acceptable answers (e.g. [word, reading] for fill questions).
// The primary answer (index 0) is used for the AI tier; exact and regex try all candidates.
func Judge(userAnswer string, expected []string, strictness Strictness, aiJudge func(user, expected string) (*Result, error)) *Result {
	// Empty/blank answer = "I don't know" — fail immediately, skip AI
	if strings.TrimSpace(userAnswer) == "" {
		return &Result{Correct: false, Score: 0.0, Feedback: "No answer provided", Source: SourceRegex, ErrorCategory: "empty"}
	}

	if len(expected) == 0 {
		return &Result{Correct: false, Score: 0.0, Feedback: "No expected answer", Source: SourceRegex, ErrorCategory: "other"}
	}

	// Tier 1: exact — try each expected answer
	for _, exp := range expected {
		if r := Exact(userAnswer, exp); r != nil {
			return r
		}
	}

	// Tier 2: regex — try each expected, keep the best
	var best *Result
	for _, exp := range expected {
		r := Regex(userAnswer, exp, strictness)
		if best == nil || r.Score > best.Score {
			best = r
		}
	}
	if best == nil {
		best = &Result{Correct: false, Score: 0.0, Feedback: "Incorrect", Source: SourceRegex}
	}

	// If regex is high-confidence, return immediately.
	// NOTE: with the default (Normal) strictness Regex scores substring
	// matches 0.9, so this branch never fires for them — by design they
	// still go to the AI tier for verification (see judge_test.go
	// TestJudgeRegexScore09CallsAI). The comment above the original code
	// described 0.9 as returning here; it does not.
	if best.Score > 0.9 {
		return best
	}

	// Tier 3: AI (if available) — use primary expected answer
	primaryExpected := expected[0]
	if aiJudge != nil {
		aiR, err := aiJudge(userAnswer, primaryExpected)
		if err == nil && aiR != nil {
			aiR.Source = SourceAI
			if aiR.ErrorCategory == "" {
				aiR.ErrorCategory = classifyError(userAnswer, primaryExpected)
			}
			return aiR
		}
		// AI failed — fall back to regex
		best.Source = SourceRegexFallback
	}
	if best.ErrorCategory == "" {
		best.ErrorCategory = classifyError(userAnswer, primaryExpected)
	}
	return best
}

// classifyError is a deterministic heuristic for diagnostics:
// near-miss answers (high string similarity to the expected answer) are
// "similarity" (typographic/confusable) — 82% of L2 errors are close
// misses; everything else wrong is "other". Empty answers are "empty".
func classifyError(user, expected string) string {
	ua := normalize(user)
	if ua == "" {
		return "empty"
	}
	ea := normalize(expected)
	dist := levenshtein([]rune(ua), []rune(ea))
	maxLen := len([]rune(ea))
	if len([]rune(ua)) > maxLen {
		maxLen = len([]rune(ua))
	}
	if maxLen == 0 {
		return "other"
	}
	if 1.0-float64(dist)/float64(maxLen) >= 0.6 {
		return "similarity"
	}
	return "other"
}

func normalize(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ToLower(s)
	s = collapseSpaces(s)
	return s
}

func collapseSpaces(s string) string {
	re := regexp.MustCompile(`\s+`)
	return re.ReplaceAllString(s, " ")
}

func fuzzyMatch(a, b string) bool {
	// Simple Levenshtein-based check
	dist := levenshtein([]rune(a), []rune(b))
	maxLen := len([]rune(a))
	if len([]rune(b)) > maxLen {
		maxLen = len([]rune(b))
	}
	if maxLen == 0 {
		return true
	}
	// Accept if >70% similar
	return 1.0-float64(dist)/float64(maxLen) > 0.7
}

func levenshtein(a, b []rune) int {
	m, n := len(a), len(b)
	if m == 0 {
		return n
	}
	if n == 0 {
		return m
	}
	prev := make([]int, n+1)
	cur := make([]int, n+1)
	for j := 0; j <= n; j++ {
		prev[j] = j
	}
	for i := 1; i <= m; i++ {
		cur[0] = i
		for j := 1; j <= n; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min3(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[n]
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}
