package db

import (
	"fmt"
	"time"
)

// Assignment tracks an after-class assignment.
type Assignment struct {
	ID        int64     `json:"id"`
	Language  string    `json:"language"`
	Type      string    `json:"type"`
	Prompt    string    `json:"prompt"`
	Context   string    `json:"context"`
	Answer    string    `json:"answer"`
	Score     float64   `json:"score"`
	Feedback  string    `json:"feedback"`
	Status    string    `json:"status"` // pending / completed
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (d *Database) InsertAssignment(lang, aType, prompt, context string) (int64, error) {
	r, err := d.DB.Exec(`
		INSERT INTO assignments (language, type, prompt, context, status)
		VALUES (?, ?, ?, ?, 'pending')
	`, lang, aType, prompt, context)
	if err != nil {
		return 0, fmt.Errorf("insert assignment: %w", err)
	}
	return r.LastInsertId()
}

func (d *Database) ListPendingAssignments(language string) ([]*Assignment, error) {
	rows, err := d.DB.Query(`
		SELECT id, language, type, prompt, context, status, created_at
		FROM assignments
		WHERE language = ? AND status = 'pending'
		ORDER BY created_at ASC
	`, language)
	if err != nil {
		return nil, fmt.Errorf("list pending assignments: %w", err)
	}
	defer rows.Close()

	var list []*Assignment
	for rows.Next() {
		a := &Assignment{}
		var createdAt string
		if err := rows.Scan(&a.ID, &a.Language, &a.Type, &a.Prompt, &a.Context,
			&a.Status, &createdAt); err != nil {
			return nil, fmt.Errorf("scan assignment: %w", err)
		}
		a.CreatedAt = TimeStr(createdAt)
		list = append(list, a)
	}
	return list, nil
}

func (d *Database) CompleteAssignment(id, answer string, score float64, feedback string) error {
	_, err := d.DB.Exec(`
		UPDATE assignments
		SET answer = ?, score = ?, feedback = ?, status = 'completed', updated_at = datetime('now','localtime')
		WHERE id = ?
	`, answer, score, feedback, id)
	if err != nil {
		return fmt.Errorf("complete assignment: %w", err)
	}
	return nil
}

func (d *Database) MarkAssignmentGiven(language string) {
	d.DB.Exec(`
		INSERT INTO assignment_cadence (language, last_given_at) VALUES (?, datetime('now','localtime'))
		ON CONFLICT(language) DO UPDATE SET last_given_at = datetime('now','localtime')
	`, language)
}

func (d *Database) LastAssignmentDate(language string) time.Time {
	var dateStr string
	err := d.DB.QueryRow(`
		SELECT last_given_at FROM assignment_cadence WHERE language = ?
	`, language).Scan(&dateStr)
	if err != nil {
		return time.Time{}
	}
	return TimeStr(dateStr)
}
