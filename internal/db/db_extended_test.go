package db

import (
	"fmt"
	"testing"
	"time"
)

func TestWordStatusDefaults(t *testing.T) {
	d := openTestDB(t)
	id, err := d.InsertWord(&Word{Word: "test", Language: "en"})
	if err != nil {
		t.Fatalf("InsertWord: %v", err)
	}
	w, err := d.GetWord(id)
	if err != nil {
		t.Fatalf("GetWord: %v", err)
	}
	if w.Status != "active" {
		t.Errorf("default status = %q, want active", w.Status)
	}
}

func TestSetWordStatus(t *testing.T) {
	d := openTestDB(t)
	id, _ := d.InsertWord(&Word{Word: "test", Language: "en"})

	if err := d.SetWordStatus(id, "pending_review"); err != nil {
		t.Fatalf("SetWordStatus: %v", err)
	}
	w, _ := d.GetWord(id)
	if w.Status != "pending_review" {
		t.Errorf("status = %q, want pending_review", w.Status)
	}

	if err := d.SetWordStatus(id, "rejected"); err != nil {
		t.Fatalf("SetWordStatus rejected: %v", err)
	}
	w, _ = d.GetWord(id)
	if w.Status != "rejected" {
		t.Errorf("status = %q, want rejected", w.Status)
	}
}

func TestSetWordTags(t *testing.T) {
	d := openTestDB(t)
	id, _ := d.InsertWord(&Word{Word: "test", Language: "en"})

	if err := d.SetWordTags(id, `["noun","jlpt-n5"]`); err != nil {
		t.Fatalf("SetWordTags: %v", err)
	}
	w, _ := d.GetWord(id)
	if w.Tags != `["noun","jlpt-n5"]` {
		t.Errorf("tags = %q", w.Tags)
	}
}

func TestListPendingReview(t *testing.T) {
	d := openTestDB(t)
	d.InsertWord(&Word{Word: "ok1", Language: "en", Status: "active"})
	d.InsertWord(&Word{Word: "pending1", Language: "en", Status: "pending_review"})
	d.InsertWord(&Word{Word: "pending2", Language: "en", Status: "pending_review"})
	d.InsertWord(&Word{Word: "rejected1", Language: "en", Status: "rejected"})
	d.InsertWord(&Word{Word: "pending_fr", Language: "fr", Status: "pending_review"})

	words, err := d.ListPendingReview("en")
	if err != nil {
		t.Fatalf("ListPendingReview: %v", err)
	}
	if len(words) != 2 {
		t.Errorf("len = %d, want 2", len(words))
	}
}

func TestGetReviewWordsExcludesNonActive(t *testing.T) {
	d := openTestDB(t)
	d.InsertWord(&Word{Word: "active1", Language: "en", Status: "active"})
	d.InsertWord(&Word{Word: "pending", Language: "en", Status: "pending_review"})
	d.InsertWord(&Word{Word: "rejected", Language: "en", Status: "rejected"})

	words, err := d.GetReviewWords("en", 10)
	if err != nil {
		t.Fatalf("GetReviewWords: %v", err)
	}
	if len(words) != 1 {
		t.Errorf("len = %d, want 1 (only active)", len(words))
	}
	if len(words) > 0 && words[0].Word != "active1" {
		t.Errorf("first word = %q, want active1", words[0].Word)
	}
}

func TestInsertTrainingPlan(t *testing.T) {
	d := openTestDB(t)

	id, err := d.InsertTrainingPlan("en", "engineering", 0.9, `{"test":true}`)
	if err != nil {
		t.Fatalf("InsertTrainingPlan: %v", err)
	}
	if id == 0 {
		t.Error("expected non-zero id")
	}

	plan, err := d.GetActivePlan("en")
	if err != nil {
		t.Fatalf("GetActivePlan: %v", err)
	}
	if plan.Direction != "engineering" {
		t.Errorf("direction = %q, want engineering", plan.Direction)
	}
	if plan.DirectionConfidence != 0.9 {
		t.Errorf("confidence = %f, want 0.9", plan.DirectionConfidence)
	}
	if plan.IsActive != true {
		t.Error("plan should be active")
	}
}

func TestInsertTrainingPlan_DeactivatesOld(t *testing.T) {
	d := openTestDB(t)
	d.InsertTrainingPlan("en", "general", 0.5, `{"old":true}`)
	d.InsertTrainingPlan("en", "business", 0.8, `{"new":true}`)

	plan, err := d.GetActivePlan("en")
	if err != nil {
		t.Fatalf("GetActivePlan: %v", err)
	}
	if plan.Direction != "business" {
		t.Errorf("direction = %q, want business", plan.Direction)
	}
}

func TestInsertAndListAssignments(t *testing.T) {
	d := openTestDB(t)

	id, err := d.InsertAssignment("en", "essay", "Write about your day", "use past tense")
	if err != nil {
		t.Fatalf("InsertAssignment: %v", err)
	}
	if id == 0 {
		t.Error("expected non-zero id")
	}

	pending, err := d.ListPendingAssignments("en")
	if err != nil {
		t.Fatalf("ListPendingAssignments: %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("len = %d, want 1", len(pending))
	}
	if pending[0].Status != "pending" {
		t.Errorf("status = %q, want pending", pending[0].Status)
	}
}

func TestCompleteAssignment(t *testing.T) {
	d := openTestDB(t)
	aid, _ := d.InsertAssignment("en", "sentence", "Make a sentence", "use: eat")

	if err := d.CompleteAssignment(fmt.Sprintf("%d", aid), "I eat breakfast", 0.9, "Good!"); err != nil {
		t.Fatalf("CompleteAssignment: %v", err)
	}

	pending, _ := d.ListPendingAssignments("en")
	if len(pending) != 0 {
		t.Errorf("len = %d, want 0 after complete", len(pending))
	}
}

func TestAssignmentCadence(t *testing.T) {
	d := openTestDB(t)

	last := d.LastAssignmentDate("en")
	if !last.IsZero() {
		t.Error("last should be zero before any marking")
	}

	d.MarkAssignmentGiven("en")
	last = d.LastAssignmentDate("en")
	if last.IsZero() {
		t.Error("last should not be zero after marking")
	}
}

func TestGetTodayTimeSpent(t *testing.T) {
	d := openTestDB(t)
	date := time.Now().Format("2006-01-02")
	lang := "ja"

	// No record yet → 0
	spent, err := d.GetTodayTimeSpent(lang)
	if err != nil {
		t.Fatalf("empty lookup error: %v", err)
	}
	if spent != 0 {
		t.Fatalf("expected 0 minutes for empty day, got %d", spent)
	}

	// Accumulate 150s → 2 minutes (integer truncation)
	if err := d.UpsertDailyStats(date, lang, 1, 1, 150); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	spent, err = d.GetTodayTimeSpent(lang)
	if err != nil {
		t.Fatalf("lookup error: %v", err)
	}
	if spent != 2 {
		t.Fatalf("expected 2 minutes after 150s, got %d", spent)
	}

	// Accumulate again: +300s → 7 minutes total
	if err := d.UpsertDailyStats(date, lang, 1, 0, 300); err != nil {
		t.Fatalf("upsert#2: %v", err)
	}
	spent, err = d.GetTodayTimeSpent(lang)
	if err != nil {
		t.Fatalf("lookup#2 error: %v", err)
	}
	if spent != 7 {
		t.Fatalf("expected 7 minutes after +300s, got %d", spent)
	}

	// Another language is isolated
	spent, err = d.GetTodayTimeSpent("en")
	if err != nil {
		t.Fatalf("en lookup error: %v", err)
	}
	if spent != 0 {
		t.Fatalf("expected 0 minutes for en, got %d", spent)
	}
}
