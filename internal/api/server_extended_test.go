package api

import (
	"encoding/json"
	"testing"

	"github.com/tzhnahida/VocabLock/internal/db"
)

func mustUnmarshal(t *testing.T, data string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

// ——— Pending review ———

func TestWordsPendingReview(t *testing.T) {
	s := newTestServer(t)

	// Insert a pending_review word
	s.DB.InsertWord(&db.Word{Word: "pending1", Language: "en", Status: "pending_review"})

	rec, _ := newTestRequest(s, "GET", "/api/v1/words/pending-review", "")
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	m := mustUnmarshal(t, rec.Body.String())
	words, ok := m["words"].([]interface{})
	if !ok {
		t.Fatal("words field missing or not array")
	}
	if len(words) != 1 {
		t.Errorf("len(words) = %d, want 1", len(words))
	}
}

func TestWordsPendingAccept(t *testing.T) {
	s := newTestServer(t)
	id, _ := s.DB.InsertWord(&db.Word{Word: "test", Language: "en", Status: "pending_review"})

	body := `{"reading":"テスト","meaning":"test","pos":"noun"}`
	rec, _ := newTestRequest(s, "POST", "/api/v1/words/pending-review/"+itoa64(id)+"/accept", body)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	w, _ := s.DB.GetWord(id)
	if w.Status != "active" {
		t.Errorf("status = %q, want active", w.Status)
	}
	if w.Reading != "テスト" {
		t.Errorf("reading = %q, want テスト", w.Reading)
	}
	if w.Meaning != "test" {
		t.Errorf("meaning = %q, want test", w.Meaning)
	}
}

func TestWordsPendingReject(t *testing.T) {
	s := newTestServer(t)
	id, _ := s.DB.InsertWord(&db.Word{Word: "test", Language: "en", Status: "pending_review"})

	rec, _ := newTestRequest(s, "POST", "/api/v1/words/pending-review/"+itoa64(id)+"/reject", "")
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	w, _ := s.DB.GetWord(id)
	if w.Status != "rejected" {
		t.Errorf("status = %q, want rejected", w.Status)
	}
}

// ——— Word tags ———

func TestWordsTags(t *testing.T) {
	s := newTestServer(t)
	id, _ := s.DB.InsertWord(&db.Word{Word: "test", Language: "en"})

	rec, _ := newTestRequest(s, "PUT", "/api/v1/words/"+itoa64(id)+"/tags", `{"tags":["jlpt-n5","verb"]}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	w, _ := s.DB.GetWord(id)
	if w.Tags != `["jlpt-n5","verb"]` {
		t.Errorf("tags = %q", w.Tags)
	}
}

// ——— Assignment (no AI) ———

func TestAssignmentNext_NoAI(t *testing.T) {
	s := newTestServer(t)
	rec, _ := newTestRequest(s, "GET", "/api/v1/quiz/assignment", "")
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	m := mustUnmarshal(t, rec.Body.String())
	msg, _ := m["message"].(string)
	if msg == "" {
		t.Error("expected non-empty message when no AI")
	}
}

func TestAssignmentSubmit_NoAI(t *testing.T) {
	s := newTestServer(t)
	rec, _ := newTestRequest(s, "POST", "/api/v1/quiz/assignment/submit", `{"assignment_id":"1","answer":"test"}`)
	if rec.Code == 500 {
		// Expected: AI not available
		return
	}
	// If AI is somehow available, body should be valid
	if rec.Code != 200 {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestAssignmentCadence_NoAI(t *testing.T) {
	s := newTestServer(t)
	rec, _ := newTestRequest(s, "GET", "/api/v1/quiz/assignment/cadence", "")
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	m := mustUnmarshal(t, rec.Body.String())
	if _, ok := m["cadence_days"]; !ok {
		t.Error("cadence_days missing from response")
	}
}

// ——— Plan ———

func TestQuizPlan_Empty(t *testing.T) {
	s := newTestServer(t)
	rec, _ := newTestRequest(s, "GET", "/api/v1/quiz/plan", "")
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	m := mustUnmarshal(t, rec.Body.String())
	if _, ok := m["direction"]; !ok {
		t.Error("direction missing from response")
	}
}

func TestQuizDirection(t *testing.T) {
	s := newTestServer(t)
	rec, _ := newTestRequest(s, "PUT", "/api/v1/quiz/direction", `{"direction":"engineering"}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	// Should now have an active plan
	rec2, _ := newTestRequest(s, "GET", "/api/v1/quiz/plan", "")
	m := mustUnmarshal(t, rec2.Body.String())
	if m["direction"] != "engineering" {
		t.Errorf("direction = %q, want engineering", m["direction"])
	}
}

// ——— Summary (no AI) ———

func TestQuizSummarize_NoData(t *testing.T) {
	s := newTestServer(t)
	rec, _ := newTestRequest(s, "POST", "/api/v1/quiz/summarize", "")
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	// Without AI, should return a message about AI being required
}

// ——— Bad request cases ———

func TestPendingAcceptBadID(t *testing.T) {
	s := newTestServer(t)
	rec, _ := newTestRequest(s, "POST", "/api/v1/words/pending-review/abc/accept", "")
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestDirectionMissing(t *testing.T) {
	s := newTestServer(t)
	rec, _ := newTestRequest(s, "PUT", "/api/v1/quiz/direction", `{}`)
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}
