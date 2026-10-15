package db

import (
	"database/sql"
	"time"
)

// NullTime scans a possibly-null SQLite datetime string into *time.Time.
func NullTime(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t, err := time.Parse("2006-01-02 15:04:05", s.String)
	if err != nil {
		// Try RFC3339
		t, err = time.Parse(time.RFC3339, s.String)
		if err != nil {
			return nil
		}
	}
	return &t
}

// TimeStr parses a non-null SQLite datetime string.
func TimeStr(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		t, _ = time.Parse(time.RFC3339, s)
	}
	return t
}

type Word struct {
	ID             int64      `json:"id"`
	Word           string     `json:"word"`
	Reading        string     `json:"reading"`
	Meaning        string     `json:"meaning"`
	POS            string     `json:"pos"`
	Language       string     `json:"language"`
	Source         string     `json:"source"`
	Confidence     string     `json:"confidence"`
	Level          string     `json:"level"`
	Detail         string     `json:"detail"`    // JSON for extension fields
	Direction      string     `json:"direction"` // learning domain
	Status         string     `json:"status"`    // active/pending_review/rejected
	Tags           string     `json:"tags"`      // JSON array of tags
	EasinessFactor float64    `json:"easiness_factor"`
	Interval       int        `json:"interval"`
	Repetitions    int        `json:"repetitions"`
	NextReviewAt   *time.Time `json:"next_review_at"`
	LastReviewAt   *time.Time `json:"last_review_at"`
	TotalAttempts  int        `json:"total_attempts"`
	TotalCorrect   int        `json:"total_correct"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type ReviewLog struct {
	ID             int64     `json:"id"`
	WordID         int64     `json:"word_id"`      // deprecated, kept for old data
	Language       string    `json:"language"`     // logging language (non-empty for post-2026-09 rows)
	SubjectType    string    `json:"subject_type"` // "word" | "grammar"
	SubjectID      int64     `json:"subject_id"`   // words.id or grammar_points.id
	QuestionType   string    `json:"question_type"`
	UserAnswer     string    `json:"user_answer"`
	ExpectedAnswer string    `json:"expected_answer"`
	Correct        bool      `json:"correct"`
	Score          float64   `json:"score"`
	Source         string    `json:"source"`
	PhonemesJSON   string    `json:"phonemes_json"`  // structured phoneme scores (read-aloud)
	ErrorCategory  string    `json:"error_category"` // 形似/音似/义混/... (judge enrichment)
	Dispute        int       `json:"dispute"`        // student disputed the AI verdict
	CreatedAt      time.Time `json:"created_at"`
}

type GrammarPoint struct {
	ID             int64      `json:"id"`
	Title          string     `json:"title"`
	Explanation    string     `json:"explanation"`
	Example        string     `json:"example"`
	Detail         string     `json:"detail"` // JSON for extension fields
	Language       string     `json:"language"`
	Source         string     `json:"source"`
	Status         string     `json:"status"`
	EasinessFactor float64    `json:"easiness_factor"`
	Interval       int        `json:"interval"`
	Repetitions    int        `json:"repetitions"`
	NextReviewAt   *time.Time `json:"next_review_at"`
	LastReviewAt   *time.Time `json:"last_review_at"`
	TotalAttempts  int        `json:"total_attempts"`
	TotalCorrect   int        `json:"total_correct"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type DailyStats struct {
	Date          string `json:"date"`
	WordsReviewed int    `json:"words_reviewed"`
	WordsCorrect  int    `json:"words_correct"`
	TimeSpentSec  int    `json:"time_spent_sec"`
}
