package db

import "fmt"

// ListNewWords returns words with next_review_at IS NULL and status='active'
// (today's new study words). Used by the study-before-test phase.
func (d *Database) ListNewWords(language string, limit, offset int) ([]*Word, int, error) {
	var count int
	countQuery := "SELECT COUNT(*) FROM words WHERE language = ? AND status = 'active' AND next_review_at IS NULL"
	if err := d.DB.QueryRow(countQuery, language).Scan(&count); err != nil {
		return nil, 0, fmt.Errorf("count new words: %w", err)
	}

	query := "SELECT " + wordColumns + " FROM words WHERE language = ? AND status = 'active' AND next_review_at IS NULL ORDER BY created_at ASC LIMIT ? OFFSET ?"
	rows, err := d.DB.Query(query, language, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list new words: %w", err)
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
