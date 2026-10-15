package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/tzhnahida/VocabLock/internal/clock"
	"github.com/tzhnahida/VocabLock/internal/db"
)

// ─── ① admission semantics: empty vault (no AI) must return "empty", not BLOCK ───

func TestSessionStartEmptyVault(t *testing.T) {
	s := newTestServer(t) // no words seeded, no AI

	rec, _ := newTestRequest(s, "POST", "/api/v1/quiz/session/start", "")
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if out["preparing"] == true {
		t.Fatalf("empty vault without AI must NOT return preparing (was a permanent dead-lock)")
	}
	if out["empty"] != true {
		t.Fatalf("empty vault without AI must return empty=true, got %v", out)
	}
}

// ─── ① admission semantics: pool < target but > 0 must be READY ───

func TestSessionStartPartialPoolReady(t *testing.T) {
	s := newTestServer(t)
	for i := 0; i < 5; i++ {
		_, err := s.DB.InsertWord(&db.Word{
			Word: fmt.Sprintf("w%d", i), Reading: fmt.Sprintf("w%d", i),
			Meaning: fmt.Sprintf("meaning of w%d", i), POS: "noun",
			Language: "en", Source: "seed", Status: "active",
		})
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	rec, _ := newTestRequest(s, "POST", "/api/v1/quiz/session/start", "")
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var out map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["preparing"] == true {
		t.Fatalf("pool=5 (< target 20) must be READY, not preparing")
	}
	if out["new_words"].(float64) != 5 {
		t.Fatalf("new_words = %v, want 5", out["new_words"])
	}
	if out["daily_remaining"].(float64) != 20 {
		t.Fatalf("daily_remaining = %v, want 20 (target unreachable but allowed)", out["daily_remaining"])
	}
}

// ─── ② soft cap + deliberate continue ───

func TestNextSoftCapAndContinue(t *testing.T) {
	s := newTestServer(t)
	s.Config.App.MinAccuracy = 0.8 // newTestServer leaves it 0 (acc gate always passes)
	for i := 0; i < 40; i++ {
		if _, err := s.DB.InsertWord(&db.Word{
			Word: fmt.Sprintf("w%d", i), Reading: fmt.Sprintf("w%d", i),
			Meaning: fmt.Sprintf("meaning of w%d", i), POS: "noun",
			Language: "en", Source: "seed", Status: "active",
		}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	// helper: answer one question; correct only when the answer model says so
	answerOne := func(correct bool) map[string]interface{} {
		t.Helper()
		rec, _ := newTestRequest(s, "GET", "/api/v1/quiz/next", "")
		var q map[string]interface{}
		if err := json.Unmarshal(rec.Body.Bytes(), &q); err != nil {
			t.Fatalf("next json: %v", err)
		}
		if q["completed"] == true || q["id"] == nil {
			t.Fatalf("expected a question, got %v", q)
		}
		qt, _ := q["type"].(string)
		var ans string
		if qt == "choice" {
			ans, _ = q["subject_meaning"].(string)
			if !correct {
				for _, o := range q["options"].([]interface{}) {
					if os, ok := o.(string); ok && os != ans {
						ans = os
						break
					}
				}
			}
		} else { // fill
			ans, _ = q["subject_word"].(string)
			if !correct {
				ans = "XXX" + strings.TrimPrefix(ans, "w")
			}
		}
		body, _ := json.Marshal(map[string]interface{}{
			"question_id":   q["id"],
			"subject_type":  q["subject_type"],
			"subject_id":    q["subject_id"],
			"question_type": qt,
			"answer":        ans,
		})
		rec, _ = newTestRequest(s, "POST", "/api/v1/quiz/submit", string(body))
		var sub map[string]interface{}
		_ = json.Unmarshal(rec.Body.Bytes(), &sub)
		if sub["correct"] != correct {
			t.Fatalf("verdict mismatch: wanted %v, got %v (q=%v ans=%q sub=%v)", correct, sub["correct"], q, ans, sub)
		}
		return sub
	}

	// 10 wrong + 20 right → 30 answered, accuracy 66% → target never met.
	// Soft cap = max(20+10, 1.5×20) = 30.
	for i := 0; i < 10; i++ {
		answerOne(false)
	}
	for i := 0; i < 20; i++ {
		answerOne(true)
	}

	rec, _ := newTestRequest(s, "GET", "/api/v1/quiz/next", "")
	var out map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["session_exhausted"] != true {
		t.Fatalf("after 30 answered & unmet, next must return session_exhausted, got %v", out)
	}

	// Deliberate continue → questions flow again.
	rec, _ = newTestRequest(s, "POST", "/api/v1/quiz/session/continue", "")
	if rec.Code != 200 {
		t.Fatalf("continue status = %d", rec.Code)
	}
	rec, _ = newTestRequest(s, "GET", "/api/v1/quiz/next", "")
	var out2 map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out2)
	if out2["id"] == nil || out2["session_exhausted"] == true {
		t.Fatalf("after continue, next must keep serving, got %v", out2)
	}
}

// ─── ③ zero-AI adaptive target ───

func TestCompleteAdjustsTargetAfter3UnmetDays(t *testing.T) {
	s := newTestServer(t)
	// 10 words so every day contributes unique subjects.
	for i := 0; i < 10; i++ {
		s.DB.InsertWord(&db.Word{
			Word: fmt.Sprintf("w%d", i), Reading: fmt.Sprintf("w%d", i),
			Meaning: fmt.Sprintf("meaning of w%d", i), POS: "noun",
			Language: "en", Source: "seed", Status: "active",
		})
	}
	// Three consecutive days, each with 10 unique subjects but 5 correct
	// (50% < 80% gate, and unique 10 < target 20): 3 unmet days.
	for dayOffset := 0; dayOffset < 3; dayOffset++ {
		day := clock.Now().AddDate(0, 0, -dayOffset).Format("2006-01-02 15:04:05")
		for i := 0; i < 10; i++ {
			correct := 1
			if i >= 5 {
				correct = 0
			}
			_, err := s.DB.DB.Exec(`
				INSERT INTO review_log (word_id, language, subject_type, subject_id, question_type, user_answer, expected_answer, correct, score, source, created_at)
				VALUES (?, 'en', 'word', ?, 'choice', 'a', 'b', ?, 1.0, 'exact', ?)
			`, i+1, i+1, correct, day)
			if err != nil {
				t.Fatalf("insert log: %v", err)
			}
		}
	}

	rec, _ := newTestRequest(s, "POST", "/api/v1/quiz/complete", "")
	if rec.Code != 200 {
		t.Fatalf("complete status = %d", rec.Code)
	}
	var out map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["target_adjusted"] != true {
		t.Fatalf("3 consecutive unmet days must adjust target, got %v", out)
	}
	if s.Config.App.DailyTarget != 16 {
		t.Fatalf("daily target = %d, want 16 (20×0.8)", s.Config.App.DailyTarget)
	}
}

// ─── ⑥ batch accept-all ───

func TestPendingAcceptAll(t *testing.T) {
	s := newTestServer(t)
	if _, err := s.DB.InsertWord(&db.Word{
		Word: "pa", Reading: "pa", Meaning: "mA", POS: "noun",
		Language: "en", Source: "ai_expansion", Status: "pending_review",
	}); err != nil {
		t.Fatalf("insert pa: %v", err)
	}
	if _, err := s.DB.InsertWord(&db.Word{
		Word: "pb", Reading: "", Meaning: "", POS: "",
		Language: "en", Source: "ai_expansion", Status: "pending_review",
	}); err != nil {
		t.Fatalf("insert pb: %v", err)
	}

	rec, _ := newTestRequest(s, "POST", "/api/v1/words/pending-review/accept-all", "")
	if rec.Code != 200 {
		t.Fatalf("accept-all status = %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["activated"].(float64) != 1 || out["skipped"].(float64) != 1 {
		t.Fatalf("want 1 activated / 1 skipped, got %v", out)
	}
	pa, _ := s.DB.GetWord(1)
	if pa == nil || pa.Status != "active" || pa.Source != "ai_expansion_user" {
		t.Fatalf("pa should be active/user-approved, got %+v", pa)
	}
	pb, _ := s.DB.GetWord(2)
	if pb == nil || pb.Status != "pending_review" {
		t.Fatalf("pb (no meaning) must stay pending, got %+v", pb)
	}
}
