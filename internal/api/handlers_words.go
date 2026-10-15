package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tzhnahida/VocabLock/internal/api/mock"
	"github.com/tzhnahida/VocabLock/internal/db"
	"github.com/tzhnahida/VocabLock/internal/log"
)

func (s *Server) handleWordsList(w http.ResponseWriter, r *http.Request) {
	if s.Mock {
		mock.HandleWordsList(w, r, s.Config.App.ActiveLang)
		return
	}
	if s.DB == nil {
		writeError(w, 500, "database not available")
		return
	}

	lang := r.URL.Query().Get("language")
	if lang == "" {
		lang = s.Config.App.ActiveLang
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	log.Debug("words", "list: lang=%s limit=%d offset=%d", lang, limit, offset)

	words, total, err := s.DB.ListWords(lang, limit, offset)
	if err != nil {
		writeError(w, 500, "list words: "+err.Error())
		return
	}

	if words == nil {
		words = []*db.Word{}
	}

	// Pre-warm TTS in background for new words (next_review_at IS NULL).
	// This feeds the frontend's getStudyWords() — by the time the user
	// reaches the learning screen, audio is cached.
	if s.TTSProvider != nil {
		for _, w := range words {
			if w.NextReviewAt == nil {
				s.preWarmTTS(w)
			}
		}
	}

	writeJSON(w, map[string]interface{}{
		"words": words,
		"total": total,
	})
}

func (s *Server) handleWordsGet(w http.ResponseWriter, r *http.Request) {
	if s.Mock {
		mock.HandleWordsGet(w, r)
		return
	}
	if s.DB == nil {
		writeError(w, 500, "database not available")
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, 400, "invalid word id")
		return
	}

	word, err := s.DB.GetWord(id)
	if err != nil {
		writeError(w, 500, "get word: "+err.Error())
		return
	}
	if word == nil {
		writeError(w, 404, "word not found")
		return
	}

	writeJSON(w, word)
}

func (s *Server) handleWordsCreate(w http.ResponseWriter, r *http.Request) {
	if s.Mock {
		mock.HandleWordsCreate(w, r)
		return
	}
	if s.DB == nil {
		writeError(w, 500, "database not available")
		return
	}

	var req struct {
		Word     string `json:"word"`
		Reading  string `json:"reading"`
		Meaning  string `json:"meaning"`
		POS      string `json:"pos"`
		Language string `json:"language"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}

	if req.Word == "" {
		writeError(w, 400, "word is required")
		return
	}
	if req.Language == "" {
		req.Language = s.Config.App.ActiveLang
	}

	log.Debug("words", "create: word=%s lang=%s", req.Word, req.Language)

	wrd := &db.Word{
		Word:     req.Word,
		Reading:  req.Reading,
		Meaning:  req.Meaning,
		POS:      req.POS,
		Language: req.Language,
		Source:   "manual",
	}

	id, err := s.DB.InsertWord(wrd)
	if err != nil {
		writeError(w, 500, "insert word: "+err.Error())
		return
	}
	if id == 0 {
		writeError(w, 409, "word already exists")
		return
	}

	// Re-query to get DB defaults (easiness_factor, created_at, etc.)
	inserted, _ := s.DB.GetWord(id)
	if inserted != nil {
		writeJSON(w, inserted)
	} else {
		wrd.ID = id
		writeJSON(w, wrd)
	}
}

func (s *Server) handleWordsUpdate(w http.ResponseWriter, r *http.Request) {
	if s.Mock {
		mock.HandleWordsUpdate(w, r)
		return
	}
	if s.DB == nil {
		writeError(w, 500, "database not available")
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, 400, "invalid word id")
		return
	}

	log.Debug("words", "update: id=%d", id)

	word, err := s.DB.GetWord(id)
	if err != nil {
		writeError(w, 500, "get word: "+err.Error())
		return
	}
	if word == nil {
		writeError(w, 404, "word not found")
		return
	}

	var req struct {
		Word    *string `json:"word"`
		Reading *string `json:"reading"`
		Meaning *string `json:"meaning"`
		POS     *string `json:"pos"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}

	if req.Word != nil {
		word.Word = *req.Word
	}
	if req.Reading != nil {
		word.Reading = *req.Reading
	}
	if req.Meaning != nil {
		word.Meaning = *req.Meaning
	}
	if req.POS != nil {
		word.POS = *req.POS
	}

	if err := s.DB.UpdateWord(word); err != nil {
		writeError(w, 500, "update word: "+err.Error())
		return
	}

	writeJSON(w, word)
}

func (s *Server) handleWordsDelete(w http.ResponseWriter, r *http.Request) {
	if s.Mock {
		mock.HandleWordsDelete(w, r)
		return
	}
	if s.DB == nil {
		writeError(w, 500, "database not available")
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, 400, "invalid word id")
		return
	}

	log.Debug("words", "delete: id=%d", id)

	if err := s.DB.DeleteWord(id); err != nil {
		writeError(w, 500, "delete word: "+err.Error())
		return
	}

	writeJSON(w, map[string]bool{"deleted": true})
}

func (s *Server) handleWordsReset(w http.ResponseWriter, r *http.Request) {
	if s.Mock {
		mock.HandleWordsReset(w, r)
		return
	}
	if s.DB == nil {
		writeError(w, 500, "database not available")
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, 400, "invalid word id")
		return
	}

	if err := s.DB.ResetWord(id); err != nil {
		writeError(w, 500, "reset word: "+err.Error())
		return
	}

	writeJSON(w, map[string]interface{}{
		"reset":   true,
		"word_id": id,
	})
}

func (s *Server) handleWordsExport(w http.ResponseWriter, r *http.Request) {
	if s.Mock {
		mock.HandleWordsExport(w, r)
		return
	}
	if s.DB == nil {
		writeError(w, 500, "database not available")
		return
	}

	lang := r.URL.Query().Get("language")
	if lang == "" {
		lang = s.Config.App.ActiveLang
	}

	words, _, err := s.DB.ListWords(lang, 10000, 0)
	if err != nil {
		writeError(w, 500, "export words: "+err.Error())
		return
	}

	type exportWord struct {
		Word    string `json:"word"`
		Reading string `json:"reading"`
		Meaning string `json:"meaning"`
		POS     string `json:"pos"`
	}
	result := make([]exportWord, 0, len(words))
	for _, wd := range words {
		result = append(result, exportWord{
			Word: wd.Word, Reading: wd.Reading, Meaning: wd.Meaning, POS: wd.POS,
		})
	}

	w.Header().Set("Content-Disposition", "attachment; filename=vocablock-export.json")
	writeJSON(w, result)
}

func (s *Server) handleWordsImport(w http.ResponseWriter, r *http.Request) {
	if s.Mock {
		mock.HandleWordsImport(w, r)
		return
	}
	if s.DB == nil {
		writeError(w, 500, "database not available")
		return
	}

	var words []struct {
		Word     string `json:"word"`
		Reading  string `json:"reading"`
		Meaning  string `json:"meaning"`
		POS      string `json:"pos"`
		Language string `json:"language"`
	}
	if err := readJSON(r, &words); err != nil {
		writeError(w, 400, "invalid import data: "+err.Error())
		return
	}

	lang := s.Config.App.ActiveLang
	log.Debug("words", "import: count=%d", len(words))
	imported, skipped := 0, 0
	for _, iw := range words {
		if iw.Word == "" {
			skipped++
			continue
		}
		if iw.Language == "" {
			iw.Language = lang
		}
		s.DB.InsertWord(&db.Word{
			Word: iw.Word, Reading: iw.Reading, Meaning: iw.Meaning,
			POS: iw.POS, Language: iw.Language, Source: "import",
		})
		imported++
	}

	writeJSON(w, map[string]interface{}{
		"imported": imported,
		"skipped":  skipped,
	})
}

func (s *Server) handleWordsExpand(w http.ResponseWriter, r *http.Request) {
	if s.Mock {
		mock.HandleWordsExpand(w, r)
		return
	}
	if s.DB == nil {
		writeError(w, 500, "database not available")
		return
	}
	if s.AI() == nil {
		writeJSON(w, map[string]interface{}{
			"verified":       []interface{}{},
			"pending_review": []interface{}{},
			"message":        "AI word expansion requires API key configuration",
		})
		return
	}

	lang := s.Config.App.ActiveLang

	// Parse optional request body
	var req struct {
		Count   int      `json:"count"`
		Context []string `json:"context"`
	}
	req.Count = 5
	if err := readJSON(r, &req); err != nil {
		req.Count = 5
		req.Context = nil
	}
	if req.Count <= 0 || req.Count > 20 {
		req.Count = 5
	}

	log.Debug("words", "expand: count=%d", req.Count)

	// Don't pile up expansions — if one is already running, reject
	if s.isExpanding() {
		writeJSON(w, map[string]interface{}{
			"verified":       0,
			"pending_review": 0,
			"message":        "expansion already in progress",
		})
		return
	}

	extraCtx := ""
	if len(req.Context) > 0 {
		extraCtx = strings.Join(req.Context, ", ")
	}

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	if !s.tryStartExpanding() {
		writeJSON(w, map[string]interface{}{
			"verified":       0,
			"pending_review": 0,
			"message":        "expansion already in progress",
		})
		return
	}
	defer s.stopExpanding()
	// Cap requested count to actual gap so multiple expansion paths
	// don't collectively exceed the daily target.
	if gapCount, _ := s.planExpandParams(lang); gapCount > 0 && gapCount < req.Count {
		log.Debug("words", "expand: capping count=%d to gap=%d", req.Count, gapCount)
		req.Count = gapCount
	}

	verified, pending, err := s.expandWords(ctx, lang, req.Count, extraCtx)
	if err != nil {
		writeJSON(w, map[string]interface{}{
			"verified":       []interface{}{},
			"pending_review": []interface{}{},
			"message":        err.Error(),
		})
		return
	}

	// Expose decision context so the UI can explain "why these words".
	planDir := ""
	if p, err := s.DB.GetActivePlan(lang); err == nil {
		planDir = p.Direction
	}
	_, existingTotal := s.collectExistingWords(lang)

	writeJSON(w, map[string]interface{}{
		"verified":       verified,
		"pending_review": pending,
		"direction":      planDir,
		"context_words":  existingTotal,
		"message":        fmt.Sprintf("Added %d verified + %d pending review", verified, pending),
	})
}

func extractJSON(s string) string {
	// Try to find JSON between triple backticks
	if i := strings.Index(s, "```json"); i != -1 {
		j := strings.Index(s[i+7:], "```")
		if j != -1 {
			return strings.TrimSpace(s[i+7 : i+7+j])
		}
	}
	if i := strings.Index(s, "```"); i != -1 {
		j := strings.Index(s[i+3:], "```")
		if j != -1 {
			return strings.TrimSpace(s[i+3 : i+3+j])
		}
	}
	// Try to find JSON between { and }
	if i := strings.Index(s, "{"); i != -1 {
		j := strings.LastIndex(s, "}")
		if j > i {
			return s[i : j+1]
		}
	}
	return s
}

// handleWordsPendingReview lists words with pending_review status.
func (s *Server) handleWordsPendingReview(w http.ResponseWriter, r *http.Request) {
	if s.DB == nil {
		writeError(w, 500, "database not available")
		return
	}
	lang := r.URL.Query().Get("language")
	if lang == "" {
		lang = s.Config.App.ActiveLang
	}
	words, err := s.DB.ListPendingReview(lang)
	if err != nil {
		writeError(w, 500, "list pending: "+err.Error())
		return
	}
	if words == nil {
		words = []*db.Word{}
	}
	writeJSON(w, map[string]interface{}{"words": words})
}

// handleWordsPendingAccept accepts a pending_review word.
// User must provide reading, meaning, and pos — these are required.
// AI then fills extended detail fields automatically.
func (s *Server) handleWordsPendingAccept(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, 400, "invalid word id")
		return
	}

	var req struct {
		Reading string `json:"reading"`
		Meaning string `json:"meaning"`
		POS     string `json:"pos"`
		Level   string `json:"level"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Reading) == "" {
		writeError(w, 400, "reading is required")
		return
	}
	if strings.TrimSpace(req.Meaning) == "" {
		writeError(w, 400, "meaning is required")
		return
	}
	if strings.TrimSpace(req.POS) == "" {
		writeError(w, 400, "pos is required")
		return
	}

	word, err := s.DB.GetWord(id)
	if err != nil {
		writeError(w, 500, "get word: "+err.Error())
		return
	}
	if word == nil {
		writeError(w, 404, "word not found")
		return
	}

	// Apply user-provided basic fields
	word.Reading = strings.TrimSpace(req.Reading)
	word.Meaning = strings.TrimSpace(req.Meaning)
	word.POS = strings.TrimSpace(req.POS)
	word.Level = strings.TrimSpace(req.Level)

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// Try dictionary for additional context, then AI fills extended detail fields
	dictEntry := s.lookupDict(ctx, word.Word)
	dictReading := word.Reading
	dictMeaning := word.Meaning
	dictPOS := word.POS
	dictLevel := word.Level
	dictDetailJSON := ""
	if dictEntry != nil {
		// Dict data supplements but doesn't override user input
		if dictReading == "" {
			dictReading = dictEntry.Reading
		}
		if dictMeaning == "" {
			dictMeaning = dictEntry.Meaning
		}
		if dictPOS == "" {
			dictPOS = dictEntry.POS
		}
		if dictLevel == "" {
			dictLevel = dictEntry.Level
		}
		dictDetailJSON = dictEntry.Detail
	}

	// AI generates extended detail from the combined data
	aiDetail := s.generateDetail(ctx, word.Word, dictReading, dictMeaning, dictPOS, dictLevel, word.Language)
	word.Detail = mergeJSON(dictDetailJSON, aiDetail)
	word.Source = "user_review"
	word.Confidence = "user"

	word.Status = "active"
	if err := s.DB.UpdateWord(word); err != nil {
		writeError(w, 500, "update word: "+err.Error())
		return
	}

	writeJSON(w, map[string]interface{}{
		"id":         id,
		"status":     "active",
		"reading":    word.Reading,
		"meaning":    word.Meaning,
		"pos":        word.POS,
		"level":      word.Level,
		"detail":     word.Detail,
		"confidence": word.Confidence,
	})
}

// handleWordsPendingReject rejects a pending_review word.
func (s *Server) handleWordsPendingReject(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, 400, "invalid word id")
		return
	}
	if err := s.DB.SetWordStatus(id, "rejected"); err != nil {
		writeError(w, 500, "reject word: "+err.Error())
		return
	}
	writeJSON(w, map[string]interface{}{"id": id, "status": "rejected"})
}

// handleWordsTags updates tags for a word.
func (s *Server) handleWordsTags(w http.ResponseWriter, r *http.Request) {
	if s.DB == nil {
		writeError(w, 500, "database not available")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, 400, "invalid word id")
		return
	}
	var req struct {
		Tags []string `json:"tags"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	tagsJSON, err := json.Marshal(req.Tags)
	if err != nil {
		writeError(w, 500, "marshal tags: "+err.Error())
		return
	}
	if err := s.DB.SetWordTags(id, string(tagsJSON)); err != nil {
		writeError(w, 500, "set tags: "+err.Error())
		return
	}
	writeJSON(w, map[string]interface{}{"id": id, "tags": req.Tags})
}

// handleWordsNew returns today's new words (next_review_at IS NULL, status='active').
// Used by the study-before-test phase — only shows words the student hasn't seen yet.
func (s *Server) handleWordsNew(w http.ResponseWriter, r *http.Request) {
	if s.Mock {
		mock.HandleWordsList(w, r, s.Config.App.ActiveLang)
		return
	}
	if s.DB == nil {
		writeError(w, 500, "database not available")
		return
	}
	lang := r.URL.Query().Get("language")
	if lang == "" {
		lang = s.Config.App.ActiveLang
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	words, total, err := s.DB.ListNewWords(lang, limit, offset)
	if err != nil {
		writeError(w, 500, "list new words: "+err.Error())
		return
	}
	if words == nil {
		words = []*db.Word{}
	}
	writeJSON(w, map[string]interface{}{
		"words": words,
		"total": total,
	})
}

// handleWordsPendingAcceptAll batch-accepts pending_review words (⑥-a: the
// quarantine wall stays; the batch button lets the student release AI words
// in ONE action instead of approving them one by one).
//
// Per word: dict lookup fills reading/meaning/POS when available; otherwise
// AI generates the missing fields (student explicitly opted in by pressing
// the batch button — the strict dict-first policy of auto-expansion does not
// apply here). Words that still end up without a meaning are skipped — they
// cannot be quizzed and stay pending for individual completion.
func (s *Server) handleWordsPendingAcceptAll(w http.ResponseWriter, r *http.Request) {
	if s.Mock {
		writeJSON(w, map[string]interface{}{"activated": 0, "skipped": 0})
		return
	}
	if s.DB == nil {
		writeError(w, 500, "database not available")
		return
	}
	lang := s.Config.App.ActiveLang
	words, err := s.DB.ListPendingReview(lang)
	if err != nil {
		writeError(w, 500, "list pending: "+err.Error())
		return
	}

	activated, skipped := 0, 0
	var skippedWords []string
	for _, pw := range words {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		dictEntry := s.lookupDict(ctx, pw.Word)
		cancel()

		reading, meaning, pos, lvl := pw.Reading, pw.Meaning, pw.POS, pw.Level
		detail := pw.Detail
		if detail == "" {
			detail = "{}"
		}
		if dictEntry != nil {
			reading, meaning, pos, lvl = dictEntry.Reading, dictEntry.Meaning, dictEntry.POS, dictEntry.Level
			detail = dictEntry.Detail
		}
		if s.AI() != nil {
			ctx2, cancel2 := context.WithTimeout(r.Context(), 30*time.Second)
			aiDetail := s.generateDetail(ctx2, pw.Word, reading, meaning, pos, lvl, lang)
			cancel2()
			if aiDetail != "" && aiDetail != "{}" {
				detail = mergeJSON(detail, aiDetail)
			}
			if reading == "" {
				reading = extractFieldFromDetail(aiDetail, "reading")
			}
			if meaning == "" {
				meaning = extractFieldFromDetail(aiDetail, "meaning")
			}
			if pos == "" {
				pos = extractFieldFromDetail(aiDetail, "pos")
			}
			if lvl == "" {
				lvl = extractFieldFromDetail(aiDetail, "level")
			}
		}
		// Give the AI-generated example (stored in detail) a chance to be the
		// meaning source of last resort — no, a sentence is not a meaning.
		if meaning == "" {
			skipped++
			skippedWords = append(skippedWords, pw.Word)
			continue
		}

		pw.Reading, pw.Meaning, pw.POS, pw.Level = reading, meaning, pos, lvl
		pw.Detail = detail
		pw.Status = "active"
		pw.Source = "ai_expansion_user"
		pw.Confidence = "low"
		if err := s.DB.UpdateWord(pw); err != nil {
			log.Warn("words", "accept-all update %q: %v", pw.Word, err)
			skipped++
			continue
		}
		activated++
	}

	writeJSON(w, map[string]interface{}{
		"activated":     activated,
		"skipped":       skipped,
		"skipped_words": skippedWords,
	})
}
