package db

import (
	"fmt"
	"time"

	"github.com/tzhnahida/VocabLock/internal/clock"
)

func (d *Database) UpsertDailyStats(date, language string, reviewed, correct, timeSec int) error {
	_, err := d.DB.Exec(`
		INSERT INTO daily_stats (date, language, words_reviewed, words_correct, time_spent_sec)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(date, language) DO UPDATE SET
			words_reviewed = words_reviewed + ?,
			words_correct = words_correct + ?,
			time_spent_sec = time_spent_sec + ?
	`, date, language, reviewed, correct, timeSec, reviewed, correct, timeSec)
	if err != nil {
		return fmt.Errorf("upsert daily stats: %w", err)
	}
	return nil
}

func (d *Database) GetStreak(language string) (int, error) {
	rows, err := d.DB.Query(`
		SELECT date FROM daily_stats
		WHERE words_reviewed > 0 AND language = ?
		ORDER BY date DESC
	`, language)
	if err != nil {
		return 0, fmt.Errorf("get streak: %w", err)
	}
	defer rows.Close()

	var streak int
	var prevDate string
	for rows.Next() {
		var date string
		if err := rows.Scan(&date); err != nil {
			return 0, err
		}
		if prevDate == "" {
			streak = 1
		} else {
			// Check if consecutive
			if isConsecutive(date, prevDate) {
				streak++
			} else {
				break
			}
		}
		prevDate = date
	}
	return streak, nil
}

func (d *Database) GetHeatmap(days int, language string) (map[string]int, error) {
	rows, err := d.DB.Query(`
		SELECT date, words_reviewed FROM daily_stats
		WHERE date >= ? AND language = ?
		ORDER BY date ASC
	`, clock.Now().AddDate(0, 0, -days).Format("2006-01-02"), language)
	if err != nil {
		return nil, fmt.Errorf("get heatmap: %w", err)
	}
	defer rows.Close()

	heatmap := make(map[string]int)
	for rows.Next() {
		var date string
		var count int
		if err := rows.Scan(&date, &count); err != nil {
			return nil, err
		}
		heatmap[date] = count
	}
	return heatmap, nil
}

func isConsecutive(earlier, later string) bool {
	t1, err := time.Parse("2006-01-02", earlier)
	if err != nil {
		return false
	}
	t2, err := time.Parse("2006-01-02", later)
	if err != nil {
		return false
	}
	diff := t2.Sub(t1)
	return diff == 24*time.Hour
}
