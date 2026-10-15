package api

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"github.com/tzhnahida/VocabLock/internal/api/mock"
	"github.com/tzhnahida/VocabLock/internal/db"
	"github.com/tzhnahida/VocabLock/internal/log"
)

func (s *Server) handleTTSWord(w http.ResponseWriter, r *http.Request) {
	if s.Mock {
		mock.HandleTTSWord(w, r)
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, 400, "invalid word id")
		return
	}

	if s.TTSProvider == nil {
		writeJSON(w, map[string]interface{}{
			"url":    fmt.Sprintf("/api/v1/tts/word/%d", id),
			"format": "mp3",
			"cached": false,
			"status": "not_configured",
		})
		return
	}

	// Load word from DB to get its reading and word
	if s.DB == nil {
		writeError(w, 500, "database not available")
		return
	}

	word, err := s.DB.GetWord(id)
	if err != nil || word == nil {
		writeError(w, 404, "word not found")
		return
	}

	// Determine voice from active language config
	voice := ""
	if lc, ok := s.Config.Languages[s.Config.App.ActiveLang]; ok {
		voice = lc.TTS.Voice
	}

	text := word.Word
	if word.Reading != "" {
		text = word.Reading
	}

	// Deduplicate concurrent requests for the same word.
	// When auto-play + page-change + manual-play all trigger TTS for the same
	// word simultaneously, only the first request does the actual generation;
	// subsequent requests wait for it and serve the cached result.
	dedupKey := fmt.Sprintf("word:%d:%s:%s", id, voice, text)
	done, isNew := s.ttsDedupOrStart(dedupKey)
	if !isNew {
		// Another request is already generating — wait for it.
		select {
		case <-done:
		case <-r.Context().Done():
			return
		}
	}
	defer func() {
		if isNew {
			s.ttsDedupDone(dedupKey)
		}
	}()

	log.Debug("tts", "word audio: id=%d isNew=%v", id, isNew)

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	path, err := s.TTSProvider.Generate(ctx, voice, text)
	if err != nil {
		writeJSON(w, map[string]interface{}{
			"url":    "",
			"format": "mp3",
			"cached": false,
			"status": "error",
			"error":  err.Error(),
		})
		return
	}

	writeJSON(w, map[string]interface{}{
		"url":    fmt.Sprintf("/data/audio/%s", filepath.Base(path)),
		"format": "mp3",
		"cached": true,
		"status": "ok",
	})
}

func (s *Server) handleTTSText(w http.ResponseWriter, r *http.Request) {
	if s.Mock {
		mock.HandleTTSText(w, r)
		return
	}

	if s.TTSProvider == nil {
		writeJSON(w, map[string]interface{}{
			"url":    "",
			"format": "mp3",
			"cached": false,
			"status": "not_configured",
		})
		return
	}

	var req struct {
		Text  string `json:"text"`
		Voice string `json:"voice"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}

	if req.Voice == "" {
		if lc, ok := s.Config.Languages[s.Config.App.ActiveLang]; ok {
			req.Voice = lc.TTS.Voice
		}
	}

	log.Debug("tts", "text audio: len=%d", len(req.Text))

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	path, err := s.TTSProvider.Generate(ctx, req.Voice, req.Text)
	if err != nil {
		writeJSON(w, map[string]interface{}{
			"url":    "",
			"format": "mp3",
			"cached": false,
			"status": "error",
			"error":  err.Error(),
		})
		return
	}

	writeJSON(w, map[string]interface{}{
		"url":    fmt.Sprintf("/data/audio/%s", filepath.Base(path)),
		"format": "mp3",
		"cached": true,
		"status": "ok",
	})
}

// preWarmTTS pre-generates TTS audio for a word's pronunciation in a background
// goroutine, so subsequent handleTTSWord calls serve instantly from cache.
func (s *Server) preWarmTTS(word *db.Word) {
	if s.TTSProvider == nil || word == nil {
		return
	}
	voice := ""
	if lc, ok := s.Config.Languages[s.Config.App.ActiveLang]; ok {
		voice = lc.TTS.Voice
	}
	text := word.Word
	if word.Reading != "" {
		text = word.Reading
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := s.TTSProvider.Generate(ctx, voice, text); err != nil {
			log.Warn("tts", "prewarm %q: %v", text, err)
		}
	}()
}

// preWarmNewWords pre-generates TTS audio for up to `count` new study words
// for the given language. Called in background from session start.
func (s *Server) preWarmNewWords(lang string, count int) {
	if s.TTSProvider == nil || s.DB == nil || count <= 0 {
		return
	}
	words, err := s.DB.GetReviewWords(lang, count)
	if err != nil {
		log.Warn("tts", "prewarm batch query: %v", err)
		return
	}
	for _, w := range words {
		// Only pre-warm truly new words (not yet reviewed)
		if w.NextReviewAt == nil {
			s.preWarmTTS(w)
		}
	}
}

// ttsDedupOrStart checks if a TTS generation for the given key is already in
// flight. If so, returns the existing done channel and isNew=false. If not,
// registers a new entry and returns isNew=true. The caller MUST call
// ttsDedupDone(key) when generation completes (only when isNew=true).
func (s *Server) ttsDedupOrStart(key string) (chan struct{}, bool) {
	s.ttsInFlightMu.Lock()
	defer s.ttsInFlightMu.Unlock()
	if ch, ok := s.ttsInFlight[key]; ok {
		return ch, false
	}
	ch := make(chan struct{})
	s.ttsInFlight[key] = ch
	return ch, true
}

// ttsDedupDone signals that a TTS generation completed and removes it from the
// in-flight map. Must only be called by the goroutine that got isNew=true.
func (s *Server) ttsDedupDone(key string) {
	s.ttsInFlightMu.Lock()
	if ch, ok := s.ttsInFlight[key]; ok {
		close(ch)
		delete(s.ttsInFlight, key)
	}
	s.ttsInFlightMu.Unlock()
}
