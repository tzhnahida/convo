package api

import (
	"context"
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/tzhnahida/VocabLock/internal/api/mock"
	"github.com/tzhnahida/VocabLock/internal/log"
)

func (s *Server) handlePronounceAssess(w http.ResponseWriter, r *http.Request) {
	if s.Mock {
		mock.HandlePronounceAssess(w, r)
		return
	}

	if s.PronounceProvider == nil {
		writeJSON(w, map[string]interface{}{
			"status": "not_configured",
			"score":  0,
		})
		return
	}

	var req struct {
		WordID int64  `json:"word_id"`
		Audio  string `json:"audio"`  // base64 encoded WAV
		Target string `json:"target"` // explicit target text (test mode)
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}

	if req.Audio == "" {
		writeError(w, 400, "audio is required")
		return
	}

	// Determine target text: explicit target first, then DB lookup
	targetText := req.Target
	if targetText == "" && req.WordID > 0 && s.DB != nil {
		word, err := s.DB.GetWord(req.WordID)
		if err == nil && word != nil {
			targetText = word.Word
			if targetText == "" {
				targetText = word.Reading
			}
		}
	}
	if targetText == "" {
		writeError(w, 400, "could not determine target text for word")
		return
	}

	// Write audio to temp file
	audioBytes, err := base64.StdEncoding.DecodeString(req.Audio)
	if err != nil {
		writeError(w, 400, "invalid base64 audio")
		return
	}

	tmpDir := filepath.Join("data", "pronounce")
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		writeError(w, 500, "create temp dir: "+err.Error())
		return
	}

	tmpFile, err := os.CreateTemp(tmpDir, "assess_*.wav")
	if err != nil {
		writeError(w, 500, "create temp file: "+err.Error())
		return
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.Write(audioBytes); err != nil {
		tmpFile.Close()
		writeError(w, 500, "write audio: "+err.Error())
		return
	}
	tmpFile.Close()

	log.Debug("pronounce", "assess: text=%s bytes=%d", targetText, len(audioBytes))

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	result, err := s.PronounceProvider.Assess(ctx, tmpPath, targetText)
	if err != nil {
		writeJSON(w, map[string]interface{}{
			"status": "error",
			"error":  err.Error(),
			"score":  0,
		})
		return
	}

	writeJSON(w, map[string]interface{}{
		"status":   "ok",
		"score":    result.Score,
		"phonemes": result.Phonemes,
		"detail":   result.Detail,
	})
}
