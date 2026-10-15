package db

import (
	"fmt"

	"github.com/tzhnahida/VocabLock/internal/clock"
)

func (d *Database) InsertReviewLog(log *ReviewLog) (int64, error) {
	correct := 0
	if log.Correct {
		correct = 1
	}
	// created_at is stamped from the injectable clock (NOT the SQLite
	// default) so "today" queries (GetTodayStats, GetUniqueSubjectsReviewed,
	// training plan) and the simulation harness agree on the calendar day.
	r, err := d.DB.Exec(`
		INSERT INTO review_log (word_id, language, subject_type, subject_id, question_type, user_answer, expected_answer, correct, score, source, phonemes_json, error_category, dispute, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, log.WordID, log.Language, log.SubjectType, log.SubjectID, log.QuestionType, log.UserAnswer, log.ExpectedAnswer, correct, log.Score, log.Source, log.PhonemesJSON, log.ErrorCategory, log.Dispute, clock.Now().Format("2006-01-02 15:04:05"))
	if err != nil {
		return 0, fmt.Errorf("insert review log: %w", err)
	}
	return r.LastInsertId()
}

func (d *Database) GetReviewLogs(subjectType string, subjectID int64, limit int) ([]*ReviewLog, error) {
	rows, err := d.DB.Query(`
		SELECT id, word_id, COALESCE(language,''), COALESCE(subject_type,'word'), COALESCE(subject_id,0), question_type, user_answer, expected_answer, correct, score, source, COALESCE(phonemes_json,''), COALESCE(error_category,''), COALESCE(dispute,0), created_at
		FROM review_log
		WHERE subject_type = ? AND subject_id = ?
		ORDER BY created_at DESC
		LIMIT ?
	`, subjectType, subjectID, limit)
	if err != nil {
		return nil, fmt.Errorf("get review logs: %w", err)
	}
	defer rows.Close()

	var logs []*ReviewLog
	for rows.Next() {
		l := &ReviewLog{}
		var correct int
		var createdAt string
		if err := rows.Scan(&l.ID, &l.WordID, &l.Language, &l.SubjectType, &l.SubjectID, &l.QuestionType, &l.UserAnswer, &l.ExpectedAnswer,
			&correct, &l.Score, &l.Source, &l.PhonemesJSON, &l.ErrorCategory, &l.Dispute, &createdAt); err != nil {
			return nil, fmt.Errorf("scan review log: %w", err)
		}
		l.Correct = correct == 1
		l.CreatedAt = TimeStr(createdAt)
		logs = append(logs, l)
	}
	return logs, nil
}

// GetTodayTimeSpent returns today's accumulated time in minutes.
func (d *Database) GetTodayTimeSpent(language string) (int, error) {
	var timeSec int
	err := d.DB.QueryRow(`
		SELECT COALESCE(time_spent_sec, 0) FROM daily_stats
		WHERE date = ? AND language = ?
	`, clock.Today(), language).Scan(&timeSec)
	if err != nil {
		return 0, nil
	}
	return timeSec / 60, nil
}

func (d *Database) GetTodayStats(language string) (reviewed, correct int, err error) {
	err = d.DB.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(correct), 0)
		FROM review_log r
		WHERE date(r.created_at) = ?
		AND (
			(r.subject_type = 'word' AND r.subject_id IN (SELECT id FROM words WHERE language = ?))
			OR (r.subject_type = 'grammar' AND r.subject_id IN (SELECT id FROM grammar_points WHERE language = ?))
		)
	`, clock.Today(), language, language).Scan(&reviewed, &correct)
	return
}

// GetUniqueSubjectsReviewed returns the count of distinct (subject_type, subject_id)
// pairs reviewed today for the given language. This counts unique words/grammar points
// rather than total attempts, so repeated attempts on the same word don't inflate
// the daily counter for session completion checks.
func (d *Database) GetUniqueSubjectsReviewed(language string) (int, error) {
	var count int
	err := d.DB.QueryRow(`
		SELECT COUNT(DISTINCT subject_type || ':' || subject_id)
		FROM review_log
		WHERE date(created_at) = ?
		AND (
			(subject_type = 'word' AND subject_id IN (SELECT id FROM words WHERE language = ?))
			OR (subject_type = 'grammar' AND subject_id IN (SELECT id FROM grammar_points WHERE language = ?))
		)
	`, clock.Today(), language, language).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count unique subjects: %w", err)
	}
	return count, nil
}

func (d *Database) GetQuizProgress(language string, dailyTarget int) (reviewedToday, remaining int, err error) {
	reviewedToday, err = d.GetUniqueSubjectsReviewed(language)
	if err != nil {
		return 0, 0, err
	}
	remaining = dailyTarget - reviewedToday
	if remaining < 0 {
		remaining = 0
	}
	return reviewedToday, remaining, nil
}

// SessionCounts returns the breakdown for quiz session start.
func (d *Database) SessionCounts(language string) (newWords, newGrammar, reviewWords, reviewGrammar int, err error) {
	err = d.DB.QueryRow(`
		SELECT COUNT(*) FROM words
		WHERE language = ? AND status = 'active' AND next_review_at IS NULL
	`, language).Scan(&newWords)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("count new words: %w", err)
	}

	err = d.DB.QueryRow(`
		SELECT COUNT(*) FROM words
		WHERE language = ? AND status = 'active' AND next_review_at IS NOT NULL AND next_review_at <= ?
	`, language, clock.TodayTS()).Scan(&reviewWords)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("count review words: %w", err)
	}

	err = d.DB.QueryRow(`
		SELECT COUNT(*) FROM grammar_points
		WHERE language = ? AND status = 'active' AND next_review_at IS NULL
	`, language).Scan(&newGrammar)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("count new grammar: %w", err)
	}

	err = d.DB.QueryRow(`
		SELECT COUNT(*) FROM grammar_points
		WHERE language = ? AND status = 'active' AND next_review_at IS NOT NULL AND next_review_at <= ?
	`, language, clock.TodayTS()).Scan(&reviewGrammar)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("count review grammar: %w", err)
	}

	return newWords, newGrammar, reviewWords, reviewGrammar, nil
}

// DailyUnique is one calendar day's unique-subject review summary.
type DailyUnique struct {
	Date    string
	Unique  int
	Correct int
}

// GetRecentUniqueDaily returns per-day unique-subject and correct counts for
// the last n days, newest first. Only days WITH review activity produce a
// row — a missing day means the student did not study that day.
func (d *Database) GetRecentUniqueDaily(language string, n int) ([]DailyUnique, error) {
	since := clock.Now().AddDate(0, 0, -(n - 1)).Format("2006-01-02")
	rows, err := d.DB.Query(`
		SELECT date(created_at), COUNT(DISTINCT subject_type || ':' || subject_id), COALESCE(SUM(correct), 0)
		FROM review_log
		WHERE language = ? AND date(created_at) >= ?
		GROUP BY date(created_at)
		ORDER BY date(created_at) DESC
	`, language, since)
	if err != nil {
		return nil, fmt.Errorf("get recent unique daily: %w", err)
	}
	defer rows.Close()
	var out []DailyUnique
	for rows.Next() {
		var du DailyUnique
		if err := rows.Scan(&du.Date, &du.Unique, &du.Correct); err != nil {
			return nil, fmt.Errorf("scan recent unique daily: %w", err)
		}
		out = append(out, du)
	}
	return out, nil
}

// MarkDispute stamps a review_log row as disputed by the student
// ("I think this was graded wrong"). Diagnostics only — no SM-2 effects.
func (d *Database) MarkDispute(logID int64) error {
	_, err := d.DB.Exec(`UPDATE review_log SET dispute = 1 WHERE id = ?`, logID)
	if err != nil {
		return fmt.Errorf("mark dispute: %w", err)
	}
	return nil
}
