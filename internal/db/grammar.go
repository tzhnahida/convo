package db

import (
	"database/sql"
	"fmt"

	"github.com/tzhnahida/VocabLock/internal/clock"
)

type grammarScanner struct {
	ID             int64
	Title          string
	Explanation    string
	Example        string
	Detail         string
	Language       string
	Source         string
	Status         string
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

func (s *grammarScanner) toGrammar() *GrammarPoint {
	return &GrammarPoint{
		ID:             s.ID,
		Title:          s.Title,
		Explanation:    s.Explanation,
		Example:        s.Example,
		Detail:         s.Detail,
		Language:       s.Language,
		Source:         s.Source,
		Status:         s.Status,
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

const grammarColumns = `id, title, explanation, example, detail, language, source, status, easiness_factor, interval, repetitions, next_review_at, last_review_at, total_attempts, total_correct, created_at, updated_at`

func scanGrammarRow(row interface{ Scan(...interface{}) error }) (*GrammarPoint, error) {
	var s grammarScanner
	err := row.Scan(
		&s.ID, &s.Title, &s.Explanation, &s.Example, &s.Detail, &s.Language, &s.Source, &s.Status,
		&s.EasinessFactor, &s.Interval, &s.Repetitions,
		&s.NextReviewAt, &s.LastReviewAt,
		&s.TotalAttempts, &s.TotalCorrect, &s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return s.toGrammar(), nil
}

func (d *Database) InsertGrammarPoint(g *GrammarPoint) (int64, error) {
	if g.Status == "" {
		g.Status = "active"
	}
	r, err := d.DB.Exec(`
		INSERT INTO grammar_points (title, explanation, example, detail, language, source, status)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, g.Title, g.Explanation, g.Example, g.Detail, g.Language, g.Source, g.Status)
	if err != nil {
		return 0, fmt.Errorf("insert grammar point: %w", err)
	}
	return r.LastInsertId()
}

func (d *Database) GetGrammarPoint(id int64) (*GrammarPoint, error) {
	row := d.DB.QueryRow(`SELECT `+grammarColumns+` FROM grammar_points WHERE id = ?`, id)
	g, err := scanGrammarRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get grammar point: %w", err)
	}
	return g, nil
}

func (d *Database) ListGrammarPoints(language string, limit, offset int) ([]*GrammarPoint, int, error) {
	var count int
	countQuery := `SELECT COUNT(*) FROM grammar_points`
	args := []interface{}{}
	if language != "" {
		countQuery += ` WHERE language = ? AND status != 'rejected'`
		args = append(args, language)
	}
	if err := d.DB.QueryRow(countQuery, args...).Scan(&count); err != nil {
		return nil, 0, fmt.Errorf("count grammar points: %w", err)
	}

	query := `SELECT ` + grammarColumns + ` FROM grammar_points`
	if language != "" {
		query += ` WHERE language = ? AND status != 'rejected'`
	}
	query += ` ORDER BY created_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := d.DB.Query(query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list grammar points: %w", err)
	}
	defer rows.Close()

	var points []*GrammarPoint
	for rows.Next() {
		g, err := scanGrammarRow(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan grammar point: %w", err)
		}
		points = append(points, g)
	}
	return points, count, nil
}

func (d *Database) UpdateGrammarPoint(g *GrammarPoint) error {
	nextRev := sql.NullString{}
	if g.NextReviewAt != nil {
		nextRev.String = g.NextReviewAt.Format("2006-01-02 15:04:05")
		nextRev.Valid = true
	}
	lastRev := sql.NullString{}
	if g.LastReviewAt != nil {
		lastRev.String = g.LastReviewAt.Format("2006-01-02 15:04:05")
		lastRev.Valid = true
	}

	_, err := d.DB.Exec(`
		UPDATE grammar_points SET title=?, explanation=?, example=?, detail=?,
		       easiness_factor=?, interval=?, repetitions=?,
		       next_review_at=?, last_review_at=?,
		       total_attempts=?, total_correct=?,
		       updated_at=datetime('now','localtime')
		WHERE id=?
	`, g.Title, g.Explanation, g.Example, g.Detail,
		g.EasinessFactor, g.Interval, g.Repetitions,
		nextRev, lastRev,
		g.TotalAttempts, g.TotalCorrect, g.ID)
	if err != nil {
		return fmt.Errorf("update grammar point: %w", err)
	}
	return nil
}

func (d *Database) GrammarPointExists(language, title string) (bool, error) {
	var count int
	err := d.DB.QueryRow(`SELECT COUNT(*) FROM grammar_points WHERE language = ? AND title = ?`, language, title).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("grammar point exists: %w", err)
	}
	return count > 0, nil
}

func (d *Database) DeleteGrammarPoint(id int64) error {
	// Cascade: remove related review_log rows first
	if _, err := d.DB.Exec(`DELETE FROM review_log WHERE subject_type='grammar' AND subject_id=?`, id); err != nil {
		return fmt.Errorf("delete review_log for grammar point: %w", err)
	}
	_, err := d.DB.Exec(`DELETE FROM grammar_points WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete grammar point: %w", err)
	}
	return nil
}

func (d *Database) GetReviewGrammarPoints(language string, limit int) ([]*GrammarPoint, error) {
	rows, err := d.DB.Query(`
		SELECT `+grammarColumns+`
		FROM grammar_points
		WHERE language = ? AND status = 'active'
		  AND (next_review_at IS NULL OR next_review_at <= ?)
		ORDER BY next_review_at ASC NULLS FIRST
		LIMIT ?
	`, language, clock.TodayTS(), limit)
	if err != nil {
		return nil, fmt.Errorf("get review grammar points: %w", err)
	}
	defer rows.Close()

	var points []*GrammarPoint
	for rows.Next() {
		g, err := scanGrammarRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan review grammar point: %w", err)
		}
		points = append(points, g)
	}
	return points, nil
}

func (d *Database) GetGrammarPointsByIDs(ids []int64) ([]*GrammarPoint, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	query := `SELECT ` + grammarColumns + ` FROM grammar_points WHERE id IN (?`
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
		return nil, fmt.Errorf("get grammar points by ids: %w", err)
	}
	defer rows.Close()

	var points []*GrammarPoint
	for rows.Next() {
		g, err := scanGrammarRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan grammar point: %w", err)
		}
		points = append(points, g)
	}
	return points, nil
}
