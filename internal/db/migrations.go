package db

import (
	"fmt"
	"strings"
)

func (d *Database) migrate() error {
	// Enable WAL mode for better concurrent read performance
	if _, err := d.DB.Exec(`PRAGMA journal_mode=WAL;`); err != nil {
		return fmt.Errorf("enable WAL: %w", err)
	}

	_, err := d.DB.Exec(`
		CREATE TABLE IF NOT EXISTS words (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			word TEXT NOT NULL,
			reading TEXT NOT NULL DEFAULT '',
			meaning TEXT NOT NULL DEFAULT '',
			pos TEXT NOT NULL DEFAULT '',
			language TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT 'manual',
			easiness_factor REAL NOT NULL DEFAULT 2.5,
			interval INTEGER NOT NULL DEFAULT 0,
			repetitions INTEGER NOT NULL DEFAULT 0,
			next_review_at TEXT,
			last_review_at TEXT,
			total_attempts INTEGER NOT NULL DEFAULT 0,
			total_correct INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT (datetime('now','localtime')),
			updated_at TEXT NOT NULL DEFAULT (datetime('now','localtime'))
		);

		CREATE TABLE IF NOT EXISTS review_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			word_id INTEGER NOT NULL,
			question_type TEXT NOT NULL,
			user_answer TEXT NOT NULL DEFAULT '',
			expected_answer TEXT NOT NULL DEFAULT '',
			correct INTEGER NOT NULL DEFAULT 0,
			score REAL NOT NULL DEFAULT 0.0,
			source TEXT NOT NULL DEFAULT 'exact',
			created_at TEXT NOT NULL DEFAULT (datetime('now','localtime')),
			FOREIGN KEY (word_id) REFERENCES words(id)
		);

		CREATE TABLE IF NOT EXISTS daily_stats (
			date TEXT PRIMARY KEY,
			words_reviewed INTEGER NOT NULL DEFAULT 0,
			words_correct INTEGER NOT NULL DEFAULT 0,
			time_spent_sec INTEGER NOT NULL DEFAULT 0
		);

		CREATE TABLE IF NOT EXISTS config (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL DEFAULT ''
		);

		CREATE TABLE IF NOT EXISTS assignments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			language TEXT NOT NULL DEFAULT '',
			type TEXT NOT NULL,
			prompt TEXT NOT NULL DEFAULT '',
			context TEXT NOT NULL DEFAULT '',
			answer TEXT NOT NULL DEFAULT '',
			score REAL NOT NULL DEFAULT 0.0,
			feedback TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'pending',
			created_at TEXT NOT NULL DEFAULT (datetime('now','localtime')),
			updated_at TEXT NOT NULL DEFAULT (datetime('now','localtime'))
		);

		CREATE TABLE IF NOT EXISTS assignment_cadence (
			language TEXT PRIMARY KEY,
			last_given_at TEXT NOT NULL DEFAULT (datetime('now','localtime'))
		);

		CREATE INDEX IF NOT EXISTS idx_words_next_review ON words(next_review_at);
		CREATE INDEX IF NOT EXISTS idx_words_language ON words(language);
		CREATE INDEX IF NOT EXISTS idx_review_log_word_id ON review_log(word_id);
		CREATE INDEX IF NOT EXISTS idx_review_log_created ON review_log(created_at);
		CREATE INDEX IF NOT EXISTS idx_assignments_status ON assignments(status);
	`)
	if err != nil {
		return err
	}

	// Phase 2: add confidence column for AI-expanded words
	if _, err := d.DB.Exec(`ALTER TABLE words ADD COLUMN confidence TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add confidence column: %w", err)
		}
	}

	// Phase 3: direction, status, tags for words + training_plans
	if _, err := d.DB.Exec(`ALTER TABLE words ADD COLUMN direction TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add direction column: %w", err)
		}
	}
	if _, err := d.DB.Exec(`ALTER TABLE words ADD COLUMN status TEXT NOT NULL DEFAULT 'active'`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add status column: %w", err)
		}
	}
	if _, err := d.DB.Exec(`ALTER TABLE words ADD COLUMN tags TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add tags column: %w", err)
		}
	}

	if _, err := d.DB.Exec(`
		CREATE TABLE IF NOT EXISTS training_plans (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			language TEXT NOT NULL,
			direction TEXT NOT NULL DEFAULT 'general',
			direction_confidence REAL NOT NULL DEFAULT 0.0,
			plan_json TEXT NOT NULL DEFAULT '',
			is_active INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL DEFAULT (datetime('now','localtime'))
		)
	`); err != nil {
		return fmt.Errorf("create training_plans table: %w", err)
	}

	// Phase 4: grammar_points table (SM-2 for grammar) + review_log generalization
	if _, err := d.DB.Exec(`
		CREATE TABLE IF NOT EXISTS grammar_points (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			title           TEXT NOT NULL DEFAULT '',
			explanation     TEXT NOT NULL DEFAULT '',
			example         TEXT NOT NULL DEFAULT '',
			language        TEXT NOT NULL DEFAULT '',
			source          TEXT NOT NULL DEFAULT 'ai',
			status          TEXT NOT NULL DEFAULT 'active',
			easiness_factor REAL NOT NULL DEFAULT 2.5,
			interval        INTEGER NOT NULL DEFAULT 0,
			repetitions     INTEGER NOT NULL DEFAULT 0,
			next_review_at  TEXT,
			last_review_at  TEXT,
			total_attempts  INTEGER NOT NULL DEFAULT 0,
			total_correct   INTEGER NOT NULL DEFAULT 0,
			created_at      TEXT NOT NULL DEFAULT (datetime('now','localtime')),
			updated_at      TEXT NOT NULL DEFAULT (datetime('now','localtime'))
		)
	`); err != nil {
		return fmt.Errorf("create grammar_points table: %w", err)
	}

	_, _ = d.DB.Exec(`CREATE INDEX IF NOT EXISTS idx_grammar_points_next_review ON grammar_points(next_review_at)`)
	_, _ = d.DB.Exec(`CREATE INDEX IF NOT EXISTS idx_grammar_points_language ON grammar_points(language)`)
	_, _ = d.DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_words_word_lang ON words(word, language)`)

	if _, err := d.DB.Exec(`ALTER TABLE review_log ADD COLUMN subject_type TEXT NOT NULL DEFAULT 'word'`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add subject_type column: %w", err)
		}
	}
	if _, err := d.DB.Exec(`ALTER TABLE review_log ADD COLUMN subject_id INTEGER NOT NULL DEFAULT 0`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add subject_id column: %w", err)
		}
	}
	if _, err := d.DB.Exec(`ALTER TABLE review_log ADD COLUMN language TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add language column: %w", err)
		}
	}
	if _, err := d.DB.Exec(`ALTER TABLE review_log ADD COLUMN phonemes_json TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add phonemes_json column: %w", err)
		}
	}
	if _, err := d.DB.Exec(`ALTER TABLE review_log ADD COLUMN error_category TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add error_category column: %w", err)
		}
	}
	if _, err := d.DB.Exec(`ALTER TABLE review_log ADD COLUMN dispute INTEGER NOT NULL DEFAULT 0`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add dispute column: %w", err)
		}
	}
	if _, err := d.DB.Exec(`CREATE INDEX IF NOT EXISTS idx_review_log_subject ON review_log(subject_type, subject_id)`); err != nil {
		return fmt.Errorf("add review_log subject index: %w", err)
	}
	if _, err := d.DB.Exec(`CREATE INDEX IF NOT EXISTS idx_review_log_source ON review_log(source, created_at)`); err != nil {
		return fmt.Errorf("add review_log source index: %w", err)
	}

	// Phase 5: student_state table for AI-evaluated persistent level
	if _, err := d.DB.Exec(`
		CREATE TABLE IF NOT EXISTS student_state (
			language TEXT PRIMARY KEY,
			level TEXT NOT NULL DEFAULT 'beginner',
			updated_at TEXT NOT NULL DEFAULT (datetime('now','localtime'))
		)
	`); err != nil {
		return fmt.Errorf("create student_state table: %w", err)
	}

	// Phase 6: add level, detail columns to words (extension fields for AI detail)
	if _, err := d.DB.Exec(`ALTER TABLE words ADD COLUMN level TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add level column: %w", err)
		}
	}
	if _, err := d.DB.Exec(`ALTER TABLE words ADD COLUMN detail TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add detail column: %w", err)
		}
	}

	// Phase 7: add detail column to grammar_points (extension fields like words)
	if _, err := d.DB.Exec(`ALTER TABLE grammar_points ADD COLUMN detail TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add grammar detail column: %w", err)
		}
	}

	// Phase 8: add language column to daily_stats for per-language tracking
	if _, err := d.DB.Exec(`ALTER TABLE daily_stats ADD COLUMN language TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add daily_stats language column: %w", err)
		}
	}
	_, _ = d.DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_daily_stats_date_lang ON daily_stats(date, language)`)

	// Phase 9: strip trailing pitch-accent digits from readings.
	// Japanese dicts often return "おかね0" — the number is a pitch accent marker.
	rows, err := d.DB.Query(`SELECT id, reading FROM words WHERE reading GLOB '*[0-9]'`)
	if err == nil {
		type fix struct {
			id      int64
			reading string
		}
		var fixes []fix
		for rows.Next() {
			var f fix
			if err := rows.Scan(&f.id, &f.reading); err == nil {
				fixes = append(fixes, f)
			}
		}
		rows.Close()
		for _, f := range fixes {
			clean := strings.TrimRight(f.reading, "0123456789")
			if clean != f.reading {
				d.DB.Exec(`UPDATE words SET reading = ? WHERE id = ?`, clean, f.id)
			}
		}
	}

	return nil
}
