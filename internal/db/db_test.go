package db

import (
	"database/sql"
	"os"
	"testing"
)

func openTestDB(t *testing.T) *Database {
	t.Helper()
	dir, err := os.MkdirTemp("", "vocablock_test_*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	d, err := Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestInsertAndGetWord(t *testing.T) {
	d := openTestDB(t)

	id, err := d.InsertWord(&Word{
		Word: "hello", Reading: "həˈloʊ", Meaning: "你好",
		POS: "interjection", Language: "en", Source: "manual",
	})
	if err != nil {
		t.Fatalf("InsertWord: %v", err)
	}
	if id != 1 {
		t.Errorf("first id = %d, want 1", id)
	}

	w, err := d.GetWord(id)
	if err != nil {
		t.Fatalf("GetWord: %v", err)
	}
	if w == nil {
		t.Fatal("expected word, got nil")
	}
	if w.Word != "hello" {
		t.Errorf("word = %q, want hello", w.Word)
	}
	if w.EasinessFactor != 2.5 {
		t.Errorf("EF = %f, want 2.5", w.EasinessFactor)
	}
	if w.Interval != 0 || w.Repetitions != 0 {
		t.Errorf("new word should have interval=0, reps=0")
	}
}

func TestGetWordNotFound(t *testing.T) {
	d := openTestDB(t)

	w, err := d.GetWord(999)
	if err != nil {
		t.Fatalf("GetWord: %v", err)
	}
	if w != nil {
		t.Error("expected nil for nonexistent word")
	}
}

func TestListWords(t *testing.T) {
	d := openTestDB(t)

	d.InsertWord(&Word{Word: "hello", Language: "en"})
	d.InsertWord(&Word{Word: "world", Language: "en"})
	d.InsertWord(&Word{Word: "test", Language: "fr"})

	words, total, err := d.ListWords("", 10, 0)
	if err != nil {
		t.Fatalf("ListWords: %v", err)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}
	if len(words) != 3 {
		t.Errorf("len(words) = %d, want 3", len(words))
	}

	// Filter by language
	fr, total, err := d.ListWords("fr", 10, 0)
	if err != nil {
		t.Fatalf("ListWords fr: %v", err)
	}
	if total != 1 {
		t.Errorf("fr total = %d, want 1", total)
	}
	if len(fr) != 1 {
		t.Errorf("fr len = %d, want 1", len(fr))
	}
}

func TestListWordsPagination(t *testing.T) {
	d := openTestDB(t)

	for i := 0; i < 5; i++ {
		d.InsertWord(&Word{Word: "w" + string(rune('0'+i)), Language: "en"})
	}

	words, total, err := d.ListWords("", 2, 0)
	if err != nil {
		t.Fatalf("ListWords page 1: %v", err)
	}
	if total != 5 {
		t.Errorf("total = %d, want 5", total)
	}
	if len(words) != 2 {
		t.Errorf("page 1 len = %d, want 2", len(words))
	}

	words2, _, err := d.ListWords("", 2, 2)
	if err != nil {
		t.Fatalf("ListWords page 2: %v", err)
	}
	if len(words2) != 2 {
		t.Errorf("page 2 len = %d, want 2", len(words2))
	}
}

func TestUpdateWord(t *testing.T) {
	d := openTestDB(t)

	id, _ := d.InsertWord(&Word{Word: "hello", Language: "en"})
	w, _ := d.GetWord(id)

	w.Word = "bonjour"
	w.Meaning = "hello (French)"
	w.TotalAttempts = 10
	w.TotalCorrect = 8

	if err := d.UpdateWord(w); err != nil {
		t.Fatalf("UpdateWord: %v", err)
	}

	updated, _ := d.GetWord(id)
	if updated.Word != "bonjour" {
		t.Errorf("word = %q, want bonjour", updated.Word)
	}
	if updated.TotalAttempts != 10 {
		t.Errorf("attempts = %d, want 10", updated.TotalAttempts)
	}
}

func TestDeleteWord(t *testing.T) {
	d := openTestDB(t)

	id, _ := d.InsertWord(&Word{Word: "temp", Language: "en"})

	if err := d.DeleteWord(id); err != nil {
		t.Fatalf("DeleteWord: %v", err)
	}

	w, _ := d.GetWord(id)
	if w != nil {
		t.Error("expected nil after delete")
	}
}

func TestResetWord(t *testing.T) {
	d := openTestDB(t)

	id, _ := d.InsertWord(&Word{Word: "test", Language: "en"})

	// First update to simulate review history
	w, _ := d.GetWord(id)
	w.EasinessFactor = 3.0
	w.Interval = 10
	w.Repetitions = 5
	w.TotalAttempts = 20
	w.TotalCorrect = 15
	d.UpdateWord(w)

	// Reset
	if err := d.ResetWord(id); err != nil {
		t.Fatalf("ResetWord: %v", err)
	}

	reset, _ := d.GetWord(id)
	if reset.EasinessFactor != 2.5 {
		t.Errorf("EF = %f, want 2.5", reset.EasinessFactor)
	}
	if reset.Interval != 0 || reset.Repetitions != 0 {
		t.Error("interval/reps should be 0 after reset")
	}
	if reset.TotalAttempts != 0 || reset.TotalCorrect != 0 {
		t.Error("attempts/correct should be 0 after reset")
	}
}

func TestGetReviewWords(t *testing.T) {
	d := openTestDB(t)

	d.InsertWord(&Word{Word: "new1", Language: "en"})
	d.InsertWord(&Word{Word: "new2", Language: "en"})
	d.InsertWord(&Word{Word: "old", Language: "fr"})

	words, err := d.GetReviewWords("en", 10)
	if err != nil {
		t.Fatalf("GetReviewWords: %v", err)
	}
	if len(words) != 2 {
		t.Errorf("len = %d, want 2", len(words))
	}
	// New words (next_review_at = NULL) should come first
	if words[0].Word != "new1" {
		t.Errorf("first word = %q, want new1", words[0].Word)
	}
}

func TestInsertReviewLog(t *testing.T) {
	d := openTestDB(t)

	id, _ := d.InsertWord(&Word{Word: "hello", Language: "en"})
	log := &ReviewLog{
		WordID: id, SubjectType: "word", SubjectID: id,
		QuestionType: "choice",
		UserAnswer:   "hello", ExpectedAnswer: "你好",
		Correct: true, Score: 1.0, Source: "exact",
	}

	logID, err := d.InsertReviewLog(log)
	if err != nil {
		t.Fatalf("InsertReviewLog: %v", err)
	}
	if logID != 1 {
		t.Errorf("logID = %d, want 1", logID)
	}

	logs, err := d.GetReviewLogs("word", id, 10)
	if err != nil {
		t.Fatalf("GetReviewLogs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	if !logs[0].Correct {
		t.Error("expected correct=true")
	}
}

func TestGetReviewLogsEmpty(t *testing.T) {
	d := openTestDB(t)

	logs, err := d.GetReviewLogs("word", 999, 10)
	if err != nil {
		t.Fatalf("GetReviewLogs: %v", err)
	}
	if len(logs) != 0 {
		t.Errorf("expected empty, got %d logs", len(logs))
	}
}

func TestGetTodayStats(t *testing.T) {
	d := openTestDB(t)

	id, _ := d.InsertWord(&Word{Word: "hello", Language: "en"})
	d.InsertReviewLog(&ReviewLog{WordID: id, SubjectType: "word", SubjectID: id, QuestionType: "choice", Correct: true, Score: 1.0})
	d.InsertReviewLog(&ReviewLog{WordID: id, SubjectType: "word", SubjectID: id, QuestionType: "fill", Correct: false, Score: 0.3})

	reviewed, correct, err := d.GetTodayStats("en")
	if err != nil {
		t.Fatalf("GetTodayStats: %v", err)
	}
	if reviewed != 2 {
		t.Errorf("reviewed = %d, want 2", reviewed)
	}
	if correct != 1 {
		t.Errorf("correct = %d, want 1", correct)
	}
}

func TestGetQuizProgress(t *testing.T) {
	d := openTestDB(t)

	id, _ := d.InsertWord(&Word{Word: "hello", Language: "en"})
	d.InsertReviewLog(&ReviewLog{WordID: id, SubjectType: "word", SubjectID: id, QuestionType: "choice", Correct: true, Score: 1.0})

	reviewed, remaining, err := d.GetQuizProgress("en", 20)
	if err != nil {
		t.Fatalf("GetQuizProgress: %v", err)
	}
	if reviewed != 1 {
		t.Errorf("reviewed = %d, want 1", reviewed)
	}
	if remaining != 19 {
		t.Errorf("remaining = %d, want 19", remaining)
	}
}

func TestGetQuizProgressNoNegativeRemaining(t *testing.T) {
	d := openTestDB(t)

	id, _ := d.InsertWord(&Word{Word: "hello", Language: "en"})
	// Insert 5 review logs for the same word — all count as 1 unique subject.
	for i := 0; i < 5; i++ {
		d.InsertReviewLog(&ReviewLog{WordID: id, SubjectType: "word", SubjectID: id, QuestionType: "choice", Correct: true, Score: 1.0})
	}

	// target=3, unique reviewed=1 → remaining=2 (not negative)
	_, remaining, err := d.GetQuizProgress("en", 3)
	if err != nil {
		t.Fatalf("GetQuizProgress: %v", err)
	}
	if remaining != 2 {
		t.Errorf("remaining should be 2 (target 3 - 1 unique reviewed), got %d", remaining)
	}
}

func TestBulkInsertWords(t *testing.T) {
	d := openTestDB(t)

	words := []*Word{
		{Word: "a", Language: "en"},
		{Word: "b", Language: "en"},
		{Word: "c", Language: "en"},
	}
	if err := d.BulkInsertWords(words); err != nil {
		t.Fatalf("BulkInsertWords: %v", err)
	}

	all, total, _ := d.ListWords("", 10, 0)
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}
	if len(all) != 3 {
		t.Errorf("len = %d, want 3", len(all))
	}
}

func TestBulkInsertEmpty(t *testing.T) {
	d := openTestDB(t)
	if err := d.BulkInsertWords(nil); err != nil {
		t.Fatalf("BulkInsertWords nil: %v", err)
	}
}

func TestGetWordsByIDs(t *testing.T) {
	d := openTestDB(t)

	id1, _ := d.InsertWord(&Word{Word: "a", Language: "en"})
	id2, _ := d.InsertWord(&Word{Word: "b", Language: "en"})
	d.InsertWord(&Word{Word: "c", Language: "en"})

	words, err := d.GetWordsByIDs([]int64{id1, id2})
	if err != nil {
		t.Fatalf("GetWordsByIDs: %v", err)
	}
	if len(words) != 2 {
		t.Errorf("len = %d, want 2", len(words))
	}
}

func TestGetWordsByIDsEmpty(t *testing.T) {
	d := openTestDB(t)
	words, err := d.GetWordsByIDs([]int64{})
	if err != nil {
		t.Fatalf("GetWordsByIDs: %v", err)
	}
	if words != nil {
		t.Error("expected nil for empty IDs")
	}
}

func TestUpsertDailyStats(t *testing.T) {
	d := openTestDB(t)

	if err := d.UpsertDailyStats("2026-01-01", "en", 5, 4, 120); err != nil {
		t.Fatalf("UpsertDailyStats: %v", err)
	}

	// Upsert same date — should accumulate
	if err := d.UpsertDailyStats("2026-01-01", "en", 3, 2, 60); err != nil {
		t.Fatalf("UpsertDailyStats again: %v", err)
	}

	heatmap, err := d.GetHeatmap(365, "en")
	if err != nil {
		t.Fatalf("GetHeatmap: %v", err)
	}
	if heatmap["2026-01-01"] != 8 {
		t.Errorf("accumulated count = %d, want 8", heatmap["2026-01-01"])
	}
}

func TestGetStreak(t *testing.T) {
	d := openTestDB(t)

	// Insert consecutive days
	d.UpsertDailyStats("2026-01-03", "en", 1, 1, 10)
	d.UpsertDailyStats("2026-01-02", "en", 1, 1, 10)
	d.UpsertDailyStats("2026-01-01", "en", 1, 1, 10)

	streak, err := d.GetStreak("en")
	if err != nil {
		t.Fatalf("GetStreak: %v", err)
	}
	// The streak counts from most recent (2026-01-03) backwards
	if streak < 1 {
		t.Errorf("streak = %d, want at least 1", streak)
	}
}

func TestGetStreakEmpty(t *testing.T) {
	d := openTestDB(t)

	streak, err := d.GetStreak("en")
	if err != nil {
		t.Fatalf("GetStreak: %v", err)
	}
	if streak != 0 {
		t.Errorf("streak = %d, want 0", streak)
	}
}

func TestNullTime(t *testing.T) {
	s := NullTime(sql.NullString{String: "2026-01-15 10:30:00", Valid: true})
	if s == nil {
		t.Fatal("expected non-nil")
	}
	if s.Year() != 2026 {
		t.Errorf("year = %d, want 2026", s.Year())
	}

	if n := NullTime(sql.NullString{Valid: false}); n != nil {
		t.Error("invalid NullString should return nil")
	}
	if n := NullTime(sql.NullString{String: ""}); n != nil {
		t.Error("empty string should return nil")
	}
}

func TestTimeStr(t *testing.T) {
	ts := TimeStr("2026-06-15 08:30:00")
	if ts.Year() != 2026 {
		t.Errorf("year = %d, want 2026", ts.Year())
	}
	if ts.Month() != 6 {
		t.Errorf("month = %d, want 6", ts.Month())
	}
}

func TestIsConsecutive(t *testing.T) {
	if !isConsecutive("2026-01-01", "2026-01-02") {
		t.Error("adjacent days should be consecutive")
	}
	if isConsecutive("2026-01-01", "2026-01-03") {
		t.Error("non-adjacent days should not be consecutive")
	}
	if isConsecutive("invalid", "2026-01-02") {
		t.Error("invalid date should not be consecutive")
	}
}

func TestInsertWordWithConfidence(t *testing.T) {
	d := openTestDB(t)

	id, err := d.InsertWord(&Word{
		Word: "test", Language: "en", Source: "ai_expansion", Confidence: "high",
	})
	if err != nil {
		t.Fatalf("InsertWord: %v", err)
	}

	w, err := d.GetWord(id)
	if err != nil {
		t.Fatalf("GetWord: %v", err)
	}
	if w == nil {
		t.Fatal("word not found")
	}
	if w.Confidence != "high" {
		t.Errorf("confidence = %q, want high", w.Confidence)
	}
	if w.Source != "ai_expansion" {
		t.Errorf("source = %q, want ai_expansion", w.Source)
	}
}

func TestBulkInsertWordsWithConfidence(t *testing.T) {
	d := openTestDB(t)

	words := []*Word{
		{Word: "a", Language: "en", Source: "ai_expansion", Confidence: "high"},
		{Word: "b", Language: "en", Source: "ai_expansion", Confidence: "low"},
	}
	if err := d.BulkInsertWords(words); err != nil {
		t.Fatalf("BulkInsertWords: %v", err)
	}

	all, _, err := d.ListWords("en", 10, 0)
	if err != nil {
		t.Fatalf("ListWords: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 words, got %d", len(all))
	}
	for _, w := range all {
		if w.Source != "ai_expansion" {
			t.Errorf("word %q: source = %q, want ai_expansion", w.Word, w.Source)
		}
		if w.Confidence == "" {
			t.Errorf("word %q: confidence is empty", w.Word)
		}
	}
}

func TestInsertWordDefaultConfidence(t *testing.T) {
	d := openTestDB(t)

	id, err := d.InsertWord(&Word{Word: "manual", Language: "en"})
	if err != nil {
		t.Fatalf("InsertWord: %v", err)
	}
	w, _ := d.GetWord(id)
	if w.Confidence != "" {
		t.Errorf("confidence = %q, want empty (default)", w.Confidence)
	}
	if w.Source != "" {
		t.Errorf("source = %q, want empty", w.Source)
	}
}
