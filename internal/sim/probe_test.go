package sim

import (
	"fmt"
	"testing"
	"time"

	"github.com/tzhnahida/VocabLock/internal/clock"
	"github.com/tzhnahida/VocabLock/internal/db"
	"github.com/tzhnahida/VocabLock/internal/engine"
)

// TestSubmitPersistsSM2 is a minimal probe: one word, one correct submit, then
// read the DB row directly. Isolates whether the submit path's SM-2 write
// actually lands in the words table.
func TestSubmitPersistsSM2(t *testing.T) {
	sc := &scenario{
		name: "PROBE", desc: "probe", langs: []string{"ja"}, words: 5,
		pCorrect: func(uint64, int64, string) float64 { return 1.0 },
	}

	mkDB := func(t *testing.T) *db.Database {
		dir := t.TempDir()
		database, err := db.Open(dir)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() { database.Close() })
		return database
	}

	database := mkDB(t)
	for _, w := range seedWords("ja", 5) {
		database.InsertWord(&db.Word{Word: w.Word, Reading: w.Reading, Meaning: w.Meaning,
			POS: w.POS, Language: "ja", Source: "seed", Status: "active"})
	}

	drv := &timeDriver{}
	old := clock.Now
	clock.Now = drv.now
	t.Cleanup(func() { clock.Now = old })
	drv.set(time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local))

	w, _ := database.GetWord(1)
	fmt.Printf("BEFORE id=1: ef=%.2f iv=%d reps=%d next=%v last=%v\n",
		w.EasinessFactor, w.Interval, w.Repetitions, w.NextReviewAt, w.LastReviewAt)

	// Simulate the exact submit-path SM-2 update.
	sm2 := engine.SM2(w.EasinessFactor, w.Interval, w.Repetitions, 5, clock.Now(), 180)
	w.EasinessFactor = sm2.EasinessFactor
	w.Interval = sm2.Interval
	w.Repetitions = sm2.Repetitions
	w.NextReviewAt = &sm2.NextReviewAt
	w.LastReviewAt = nil
	lr := clock.Now()
	w.LastReviewAt = &lr
	if err := database.UpdateWord(w); err != nil {
		t.Fatalf("update: %v", err)
	}

	w2, _ := database.GetWord(1)
	fmt.Printf("AFTER  id=1: ef=%.2f iv=%d reps=%d next=%v last=%v\n",
		w2.EasinessFactor, w2.Interval, w2.Repetitions, w2.NextReviewAt, w2.LastReviewAt)

	if w2.Repetitions != 1 || w2.Interval != 1 || w2.NextReviewAt == nil ||
		w2.NextReviewAt.Format("2006-01-02") != "2026-06-02" {
		t.Fatalf("SM-2 not persisted correctly: %+v", w2)
	}

	_ = sc
}
