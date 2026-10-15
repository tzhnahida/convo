//go:build ignore

package main

import (
	"fmt"
	"github.com/tzhnahida/VocabLock/internal/db"
	"unicode/utf8"
)

func main() {
	d, _ := db.Open("data")
	if d == nil {
		fmt.Println("open failed")
		return
	}
	defer d.Close()

	badCount := 0

	// Words
	rows, _ := d.DB.Query("SELECT id, word, reading, meaning, language, source FROM words ORDER BY id")
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var id int64
			var w, r, m, l, s string
			rows.Scan(&id, &w, &r, &m, &l, &s)
			bad := false
			for _, str := range []string{r, m} {
				if !utf8.ValidString(str) {
					bad = true
				}
				for _, c := range str {
					if c == '�' {
						bad = true
					}
				}
			}
			if bad {
				fmt.Printf("BAD word ID=%d word=%q lang=%q source=%q\n", id, w, l, s)
				d.DB.Exec("DELETE FROM words WHERE id = ?", id)
				d.DB.Exec("DELETE FROM review_log WHERE word_id = ?", id)
				badCount++
			} else {
				fmt.Printf("OK  word ID=%d word=%q reading=%q meaning=%q\n", id, w, r, m)
			}
		}
	}

	// Assignments
	rows2, _ := d.DB.Query("SELECT id, prompt, context, answer, feedback, language FROM assignments ORDER BY id")
	if rows2 != nil {
		defer rows2.Close()
		for rows2.Next() {
			var id int64
			var p, c, a, f, l string
			rows2.Scan(&id, &p, &c, &a, &f, &l)
			bad := false
			for _, str := range []string{p, c, a, f} {
				if !utf8.ValidString(str) {
					bad = true
				}
				for _, ch := range str {
					if ch == '�' {
						bad = true
					}
				}
			}
			if bad {
				fmt.Printf("BAD assignment ID=%d lang=%q\n", id, l)
				d.DB.Exec("DELETE FROM assignments WHERE id = ?", id)
				badCount++
			} else {
				fmt.Printf("OK  assignment ID=%d type OK lang=%q\n", id, l)
			}
		}
	}

	// Grammar points
	rows3, _ := d.DB.Query("SELECT id, title, explanation, example, language FROM grammar_points ORDER BY id")
	if rows3 != nil {
		defer rows3.Close()
		for rows3.Next() {
			var id int64
			var t, e, ex, l string
			rows3.Scan(&id, &t, &e, &ex, &l)
			bad := false
			for _, str := range []string{t, e, ex} {
				if !utf8.ValidString(str) {
					bad = true
				}
				for _, ch := range str {
					if ch == '�' {
						bad = true
					}
				}
			}
			if bad {
				fmt.Printf("BAD grammar ID=%d lang=%q\n", id, l)
				d.DB.Exec("DELETE FROM grammar_points WHERE id = ?", id)
				badCount++
			} else {
				fmt.Printf("OK  grammar ID=%d title=%q lang=%q\n", id, t, l)
			}
		}
	}

	// Review logs
	rows4, _ := d.DB.Query("SELECT id, user_answer, expected_answer FROM review_log ORDER BY id")
	if rows4 != nil {
		defer rows4.Close()
		for rows4.Next() {
			var id int64
			var u, e string
			rows4.Scan(&id, &u, &e)
			bad := false
			for _, str := range []string{u, e} {
				if !utf8.ValidString(str) {
					bad = true
				}
				for _, ch := range str {
					if ch == '�' {
						bad = true
					}
				}
			}
			if bad {
				fmt.Printf("BAD review_log ID=%d\n", id)
				d.DB.Exec("DELETE FROM review_log WHERE id = ?", id)
				badCount++
			}
		}
	}

	fmt.Printf("\nCleaned %d bad records.\n", badCount)
}
