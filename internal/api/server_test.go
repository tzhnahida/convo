package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/tzhnahida/VocabLock/internal/config"
	"github.com/tzhnahida/VocabLock/internal/db"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()

	dir, _ := os.MkdirTemp("", "vocablock_test_*")
	t.Cleanup(func() { os.RemoveAll(dir) })

	database, err := db.Open(dir)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	cfg := &config.Config{
		App: config.AppConfig{
			ActiveLang:  "en",
			DailyTarget: 20,
		},
		Languages: map[string]*config.LanguageConfig{
			"en": {QuestionTypes: []string{"choice", "fill"}},
		},
	}
	cfg.App.Server.Host = "127.0.0.1"

	s := NewServer(false, cfg, database)
	return s
}

func newTestRequest(s *Server, method, path, body string) (*httptest.ResponseRecorder, *http.Request) {
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	handler := withMiddleware(mux, s.Token)

	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec, req
}

func unauthedRequest(s *Server, method, path string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	handler := withMiddleware(mux, s.Token)

	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestHealthEndpoint(t *testing.T) {
	s := newTestServer(t)

	// Health should work without auth
	rec := unauthedRequest(s, "GET", "/health")
	if rec.Code != 200 {
		t.Errorf("health status = %d, want 200", rec.Code)
	}

	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["status"] != "ok" {
		t.Errorf("health response = %v", resp)
	}
}

func TestAuthRequired(t *testing.T) {
	s := newTestServer(t)

	rec := unauthedRequest(s, "GET", "/api/v1/words")
	if rec.Code != 401 {
		t.Errorf("unauth status = %d, want 401", rec.Code)
	}

	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["error"] != "unauthorized" {
		t.Errorf("unauth response = %v", resp)
	}
}

func TestAuthValidToken(t *testing.T) {
	s := newTestServer(t)

	rec, _ := newTestRequest(s, "GET", "/api/v1/config", "")
	if rec.Code != 200 {
		t.Errorf("auth status = %d, want 200", rec.Code)
	}
}

func TestConfigGet(t *testing.T) {
	s := newTestServer(t)

	rec, _ := newTestRequest(s, "GET", "/api/v1/config", "")

	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp["active_lang"] != "en" {
		t.Errorf("active_lang = %v, want en", resp["active_lang"])
	}
	if resp["daily_target"] != float64(20) {
		t.Errorf("daily_target = %v, want 20", resp["daily_target"])
	}
}

func TestConfigGetKey(t *testing.T) {
	s := newTestServer(t)

	rec, _ := newTestRequest(s, "GET", "/api/v1/config/active_lang", "")

	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp["key"] != "active_lang" {
		t.Errorf("key = %v, want active_lang", resp["key"])
	}
	if resp["value"] != "en" {
		t.Errorf("value = %v, want en", resp["value"])
	}
}

func TestConfigGetKeyUnknown(t *testing.T) {
	s := newTestServer(t)
	rec, _ := newTestRequest(s, "GET", "/api/v1/config/unknown", "")

	if rec.Code != 404 {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestConfigGetLanguages(t *testing.T) {
	s := newTestServer(t)
	rec, _ := newTestRequest(s, "GET", "/api/v1/config/languages", "")

	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp["key"] != "languages" {
		t.Errorf("key = %v, want languages", resp["key"])
	}
	langs, ok := resp["value"].([]interface{})
	if !ok {
		t.Fatal("value is not a list")
	}
	if len(langs) != 1 || langs[0] != "en" {
		t.Errorf("languages = %v, want [en]", langs)
	}
}

func TestConfigUpdate(t *testing.T) {
	s := newTestServer(t)

	rec, _ := newTestRequest(s, "PUT", "/api/v1/config", `{"daily_target": 50}`)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp["daily_target"] != float64(50) {
		t.Errorf("daily_target = %v, want 50", resp["daily_target"])
	}

	// Verify it persisted
	rec2, _ := newTestRequest(s, "GET", "/api/v1/config", "")
	json.NewDecoder(rec2.Body).Decode(&resp)
	if resp["daily_target"] != float64(50) {
		t.Errorf("not persisted: daily_target = %v", resp["daily_target"])
	}
}

func TestConfigUpdateInvalidLang(t *testing.T) {
	s := newTestServer(t)

	rec, _ := newTestRequest(s, "PUT", "/api/v1/config", `{"active_lang": "xx"}`)
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestStatsHeatmap(t *testing.T) {
	s := newTestServer(t)

	rec, _ := newTestRequest(s, "GET", "/api/v1/stats/heatmap", "")

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var heatmap map[string]int
	json.NewDecoder(rec.Body).Decode(&heatmap)
	// Empty DB should return empty map
}

func TestStatsStreak(t *testing.T) {
	s := newTestServer(t)

	rec, _ := newTestRequest(s, "GET", "/api/v1/stats/streak", "")

	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp["streak"] != float64(0) {
		t.Errorf("streak = %v, want 0", resp["streak"])
	}
}

func TestStatsDailyReport(t *testing.T) {
	s := newTestServer(t)

	rec, _ := newTestRequest(s, "GET", "/api/v1/stats/daily-report", "")

	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp["words_reviewed"] != float64(0) {
		t.Errorf("words_reviewed = %v, want 0", resp["words_reviewed"])
	}
}

func TestWordsCRUD(t *testing.T) {
	s := newTestServer(t)

	// Create
	rec, _ := newTestRequest(s, "POST", "/api/v1/words", `{"word":"hello","reading":"həˈloʊ","meaning":"你好","pos":"interjection","language":"en"}`)
	if rec.Code != 200 {
		t.Fatalf("create status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var word map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&word)
	id := int64(word["id"].(float64))

	// Get
	rec, _ = newTestRequest(s, "GET", "/api/v1/words/"+strconv.FormatInt(id, 10), "")
	if rec.Code != 200 {
		t.Fatalf("get status = %d, want 200", rec.Code)
	}

	// Update
	rec, _ = newTestRequest(s, "PUT", "/api/v1/words/"+strconv.FormatInt(id, 10), `{"meaning":"您好"}`)
	if rec.Code != 200 {
		t.Fatalf("update status = %d, want 200", rec.Code)
	}
	json.NewDecoder(rec.Body).Decode(&word)
	if word["meaning"] != "您好" {
		t.Errorf("meaning = %v, want 您好", word["meaning"])
	}

	// List
	rec, _ = newTestRequest(s, "GET", "/api/v1/words", "")
	if rec.Code != 200 {
		t.Fatalf("list status = %d, want 200", rec.Code)
	}

	// Delete
	rec, _ = newTestRequest(s, "DELETE", "/api/v1/words/"+strconv.FormatInt(id, 10), "")
	if rec.Code != 200 {
		t.Fatalf("delete status = %d, want 200", rec.Code)
	}
}

func TestWordsExport(t *testing.T) {
	s := newTestServer(t)

	// Insert some words
	newTestRequest(s, "POST", "/api/v1/words", `{"word":"a","language":"en"}`)
	newTestRequest(s, "POST", "/api/v1/words", `{"word":"b","language":"en"}`)

	rec, _ := newTestRequest(s, "GET", "/api/v1/words/export", "")

	if rec.Code != 200 {
		t.Fatalf("export status = %d, want 200", rec.Code)
	}

	var exported []map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&exported)
	if len(exported) != 2 {
		t.Errorf("exported %d words, want 2", len(exported))
	}
}

func TestWordsImport(t *testing.T) {
	s := newTestServer(t)

	importData := `[{"word":"hello","reading":"həˈloʊ","meaning":"你好","language":"en"},{"word":"world","meaning":"世界","language":"en"}]`
	rec, _ := newTestRequest(s, "POST", "/api/v1/words/import", importData)

	if rec.Code != 200 {
		t.Fatalf("import status = %d, want 200", rec.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["imported"] != float64(2) {
		t.Errorf("imported = %v, want 2", resp["imported"])
	}

	// Verify list
	rec, _ = newTestRequest(s, "GET", "/api/v1/words", "")
	var listResp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&listResp)
	if listResp["total"] != float64(2) {
		t.Errorf("total after import = %v, want 2", listResp["total"])
	}
}

func TestQuizProgress(t *testing.T) {
	s := newTestServer(t)

	rec, _ := newTestRequest(s, "GET", "/api/v1/quiz/progress", "")

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp["remaining"] != float64(20) {
		t.Errorf("remaining = %v, want 20", resp["remaining"])
	}
}

func TestQuizTodayStats(t *testing.T) {
	s := newTestServer(t)

	rec, _ := newTestRequest(s, "GET", "/api/v1/quiz/today-stats", "")

	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp["reviewed"] != float64(0) {
		t.Errorf("reviewed = %v, want 0", resp["reviewed"])
	}
}

func TestQuizNextAndSubmit(t *testing.T) {
	s := newTestServer(t)

	// Insert a word to review
	newTestRequest(s, "POST", "/api/v1/words", `{"word":"hello","reading":"həˈloʊ","meaning":"你好","language":"en"}`)

	// Get next question
	rec, _ := newTestRequest(s, "GET", "/api/v1/quiz/next", "")

	if rec.Code != 200 {
		t.Fatalf("next status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var question map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&question)

	if question["subject_type"] != "word" {
		t.Errorf("subject_type = %v, want word", question["subject_type"])
	}

	// Submit answer — must match the question type's expected answer
	qID := question["id"].(string)
	sType := question["subject_type"].(string)
	sID := int64(question["subject_id"].(float64))
	qType := question["type"].(string)

	var answer string
	switch qType {
	case "choice":
		answer = "你好" // meaning
	case "fill", "spelling":
		answer = "hello" // word
	case "pronounce":
		answer = "həˈloʊ" // pronunciation
	default:
		answer = "你好"
	}

	submitBody := `{"question_id":"` + qID + `","subject_type":"` + sType + `","subject_id":` + strconv.FormatInt(sID, 10) + `,"question_type":"` + qType + `","answer":"` + answer + `"}`
	rec, _ = newTestRequest(s, "POST", "/api/v1/quiz/submit", submitBody)

	if rec.Code != 200 {
		t.Fatalf("submit status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var result map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&result)

	if result["correct"] != true {
		t.Errorf("correct = %v, want true: type=%s answer=%s result=%v", result["correct"], qType, answer, result)
	}
}

func TestMockModeRouting(t *testing.T) {
	cfg := &config.Config{
		App: config.AppConfig{
			ActiveLang:  "en",
			DailyTarget: 20,
		},
		Languages: map[string]*config.LanguageConfig{
			"en": {QuestionTypes: []string{"choice"}},
		},
	}

	s := NewServer(true, cfg, nil)
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	handler := withMiddleware(mux, s.Token)

	// Test mock quiz progress
	req := httptest.NewRequest("GET", "/api/v1/quiz/progress", nil)
	req.Header.Set("Authorization", "Bearer "+s.Token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("mock quiz progress status = %d, want 200", rec.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)
	// Mock returns preset data
	if _, ok := resp["reviewed_today"]; !ok {
		t.Error("mock response missing reviewed_today")
	}
}

func TestRecoveryMiddleware(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/panic", func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	})

	// Only recovery + logging, no auth — auth would return 401 before handler runs
	var handler http.Handler = mux
	handler = recoveryMiddleware(handler)
	handler = loggingMiddleware(handler)

	req := httptest.NewRequest("GET", "/panic", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != 500 {
		t.Errorf("panic handler status = %d, want 500", rec.Code)
	}
}

func TestLoggingMiddleware(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})

	var handler http.Handler = mux
	handler = loggingMiddleware(handler)

	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestWordsNotFound(t *testing.T) {
	s := newTestServer(t)

	rec, _ := newTestRequest(s, "GET", "/api/v1/words/999", "")
	if rec.Code != 404 {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestWordsInvalidID(t *testing.T) {
	s := newTestServer(t)

	rec, _ := newTestRequest(s, "GET", "/api/v1/words/abc", "")
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
