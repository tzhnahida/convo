package db

import (
	"database/sql"
	"fmt"

	"github.com/tzhnahida/VocabLock/internal/clock"
)

type wordScanner struct {
	ID             int64
	Word           string
	Reading        string
	Meaning        string
	POS            string
	Language       string
	Source         string
	Confidence     string
	Level          string
	Detail         string
	Direction      string
	Status         string
	Tags           string
	EasinessFactor float64
	Interval       int
	Repetitions    int
	NextReviewAt   sql.NullString
	LastReviewAt   sql.NullString
	TotalAttempts  int
	TotalCorrect   int
	CreatedAt      string
	UpdatedAt      string
}

func (s *wordScanner) toWord() *Word {
	return &Word{
		ID:             s.ID,
		Word:           s.Word,
		Reading:        s.Reading,
		Meaning:        s.Meaning,
		POS:            s.POS,
		Language:       s.Language,
		Source:         s.Source,
		Confidence:     s.Confidence,
		Level:          s.Level,
		Detail:         s.Detail,
		Direction:      s.Direction,
		Status:         s.Status,
		Tags:           s.Tags,
		EasinessFactor: s.EasinessFactor,
		Interval:       s.Interval,
		Repetitions:    s.Repetitions,
		NextReviewAt:   NullTime(s.NextReviewAt),
		LastReviewAt:   NullTime(s.LastReviewAt),
		TotalAttempts:  s.TotalAttempts,
		TotalCorrect:   s.TotalCorrect,
		CreatedAt:      TimeStr(s.CreatedAt),
		UpdatedAt:      TimeStr(s.UpdatedAt),
	}
}

const wordColumns = `id, word, reading, meaning, pos, language, source, confidence, level, detail, direction, status, tags, easiness_factor, interval, repetitions, next_review_at, last_review_at, total_attempts, total_correct, created_at, updated_at`

func scanWordRow(row interface{ Scan(...interface{}) error }) (*Word, error) {
	var s wordScanner
	err := row.Scan(
		&s.ID, &s.Word, &s.Reading, &s.Meaning, &s.POS, &s.Language, &s.Source, &s.Confidence,
		&s.Level, &s.Detail, &s.Direction, &s.Status, &s.Tags,
		&s.EasinessFactor, &s.Interval, &s.Repetitions,
		&s.NextReviewAt, &s.LastReviewAt,
		&s.TotalAttempts, &s.TotalCorrect, &s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return s.toWord(), nil
}

func (d *Database) InsertWord(w *Word) (int64, error) {
	if w.Status == "" {
		w.Status = "active"
	}
	r, err := d.DB.Exec(`
		INSERT OR IGNORE INTO words (word, reading, meaning, pos, language, source, confidence, level, detail, direction, status, tags,
		                   easiness_factor, interval, repetitions)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 2.5, 0, 0)
	`, w.Word, w.Reading, w.Meaning, w.POS, w.Language, w.Source, w.Confidence, w.Level, w.Detail, w.Direction, w.Status, w.Tags)
	if err != nil {
		return 0, fmt.Errorf("insert word: %w", err)
	}
	id, err := r.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("last insert id: %w", err)
	}
	if id == 0 {
		return 0, nil // duplicate, ignored
	}
	return id, nil
}

func (d *Database) WordExists(language, word string) (bool, error) {
	var count int
	err := d.DB.QueryRow(`SELECT COUNT(*) FROM words WHERE language = ? AND word = ?`, language, word).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("word exists: %w", err)
	}
	return count > 0, nil
}

// WordActive checks if an active (learnable) word exists. Pending/archived words
// are NOT counted — the student hasn't learned them yet.
func (d *Database) WordActive(language, word string) (bool, error) {
	var count int
	err := d.DB.QueryRow(`SELECT COUNT(*) FROM words WHERE language = ? AND word = ? AND status = 'active'`, language, word).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("word active: %w", err)
	}
	return count > 0, nil
}

func (d *Database) GetWord(id int64) (*Word, error) {
	row := d.DB.QueryRow(`SELECT `+wordColumns+` FROM words WHERE id = ?`, id)
	w, err := scanWordRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get word: %w", err)
	}
	return w, nil
}

func (d *Database) ListWords(language string, limit, offset int) ([]*Word, int, error) {
	var count int
	countQuery := `SELECT COUNT(*) FROM words`
	args := []interface{}{}
	if language != "" {
		countQuery += ` WHERE language = ? AND status != 'rejected'`
		args = append(args, language)
	}
	if err := d.DB.QueryRow(countQuery, args...).Scan(&count); err != nil {
		return nil, 0, fmt.Errorf("count words: %w", err)
	}

	query := `SELECT ` + wordColumns + ` FROM words`
	if language != "" {
		query += ` WHERE language = ? AND status != 'rejected'`
	}
	query += ` ORDER BY created_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := d.DB.Query(query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list words: %w", err)
	}
	defer rows.Close()

	var words []*Word
	for rows.Next() {
		w, err := scanWordRow(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan word: %w", err)
		}
		words = append(words, w)
	}
	return words, count, nil
}

func (d *Database) UpdateWord(w *Word) error {
	nextRev := sql.NullString{}
	if w.NextReviewAt != nil {
		nextRev.String = w.NextReviewAt.Format("2006-01-02 15:04:05")
		nextRev.Valid = true
	}
	lastRev := sql.NullString{}
	if w.LastReviewAt != nil {
		lastRev.String = w.LastReviewAt.Format("2006-01-02 15:04:05")
		lastRev.Valid = true
	}

	_, err := d.DB.Exec(`
		UPDATE words SET word=?, reading=?, meaning=?, pos=?, level=?, detail=?,
		       status=?, source=?, confidence=?, tags=?,
		       easiness_factor=?, interval=?, repetitions=?,
		       next_review_at=?, last_review_at=?,
		       total_attempts=?, total_correct=?,
		       updated_at=datetime('now','localtime')
		WHERE id=?
	`, w.Word, w.Reading, w.Meaning, w.POS, w.Level, w.Detail,
		w.Status, w.Source, w.Confidence, w.Tags,
		w.EasinessFactor, w.Interval, w.Repetitions,
		nextRev, lastRev,
		w.TotalAttempts, w.TotalCorrect, w.ID)
	if err != nil {
		return fmt.Errorf("update word: %w", err)
	}
	return nil
}

func (d *Database) DeleteWord(id int64) error {
	// Cascade: remove related review_log rows first
	if _, err := d.DB.Exec(`DELETE FROM review_log WHERE (subject_type='word' AND subject_id=?) OR word_id=?`, id, id); err != nil {
		return fmt.Errorf("delete review_log for word: %w", err)
	}
	_, err := d.DB.Exec(`DELETE FROM words WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete word: %w", err)
	}
	return nil
}

func (d *Database) ResetWord(id int64) error {
	_, err := d.DB.Exec(`
		UPDATE words SET easiness_factor=2.5, interval=0, repetitions=0,
		       next_review_at=NULL, last_review_at=NULL,
		       total_attempts=0, total_correct=0,
		       updated_at=datetime('now','localtime')
		WHERE id=?
	`, id)
	if err != nil {
		return fmt.Errorf("reset word: %w", err)
	}
	return nil
}

func (d *Database) GetReviewWords(language string, limit int) ([]*Word, error) {
	rows, err := d.DB.Query(`
		SELECT `+wordColumns+`
		FROM words
		WHERE language = ? AND status = 'active'
		  AND (next_review_at IS NULL OR next_review_at <= ?)
		ORDER BY next_review_at ASC NULLS FIRST
		LIMIT ?
	`, language, clock.TodayTS(), limit)
	if err != nil {
		return nil, fmt.Errorf("get review words: %w", err)
	}
	defer rows.Close()

	var words []*Word
	for rows.Next() {
		w, err := scanWordRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan review word: %w", err)
		}
		words = append(words, w)
	}
	return words, nil
}

func (d *Database) GetWordsByIDs(ids []int64) ([]*Word, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	query := `SELECT ` + wordColumns + ` FROM words WHERE id IN (?`
	for i := 1; i < len(ids); i++ {
		query += ", ?"
	}
	query += ")"

	args := make([]interface{}, len(ids))
	for i, id := range ids {
		args[i] = id
	}

	rows, err := d.DB.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("get words by ids: %w", err)
	}
	defer rows.Close()

	var words []*Word
	for rows.Next() {
		w, err := scanWordRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan word: %w", err)
		}
		words = append(words, w)
	}
	return words, nil
}

func (d *Database) BulkInsertWords(words []*Word) error {
	tx, err := d.DB.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO words (word, reading, meaning, pos, language, source, confidence, level, detail, direction, status, tags,
		                   easiness_factor, interval, repetitions)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 2.5, 0, 0)
	`)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	for _, w := range words {
		if w.Status == "" {
			w.Status = "active"
		}
		if _, err := stmt.Exec(w.Word, w.Reading, w.Meaning, w.POS, w.Language, w.Source, w.Confidence, w.Level, w.Detail, w.Direction, w.Status, w.Tags); err != nil {
			return fmt.Errorf("insert: %w", err)
		}
	}
	return tx.Commit()
}

// ——— Pending review ———

func (d *Database) ListPendingReview(language string) ([]*Word, error) {
	rows, err := d.DB.Query(`
		SELECT `+wordColumns+` FROM words
		WHERE language = ? AND status = 'pending_review'
		ORDER BY created_at DESC
	`, language)
	if err != nil {
		return nil, fmt.Errorf("list pending review: %w", err)
	}
	defer rows.Close()

	var words []*Word
	for rows.Next() {
		w, err := scanWordRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		words = append(words, w)
	}
	return words, nil
}

func (d *Database) SetWordStatus(id int64, status string) error {
	_, err := d.DB.Exec(`UPDATE words SET status = ?, updated_at = datetime('now','localtime') WHERE id = ?`, status, id)
	if err != nil {
		return fmt.Errorf("set word status: %w", err)
	}
	return nil
}

func (d *Database) SetWordTags(id int64, tags string) error {
	_, err := d.DB.Exec(`UPDATE words SET tags = ?, updated_at = datetime('now','localtime') WHERE id = ?`, tags, id)
	if err != nil {
		return fmt.Errorf("set word tags: %w", err)
	}
	return nil
}

// WordPOSCounts returns total word count and a map of POS -> count for
// non-rejected words in the given language.
func (d *Database) WordPOSCounts(language string) (total int, posMap map[string]int, err error) {
	rows, err := d.DB.Query(`SELECT pos FROM words WHERE language = ? AND status != 'rejected' AND pos != ''`, language)
	if err != nil {
		return 0, nil, fmt.Errorf("word pos counts: %w", err)
	}
	defer rows.Close()
	posMap = make(map[string]int)
	for rows.Next() {
		var pos string
		if err := rows.Scan(&pos); err != nil {
			return 0, nil, fmt.Errorf("scan pos: %w", err)
		}
		posMap[pos]++
		total++
	}
	return total, posMap, nil
}
