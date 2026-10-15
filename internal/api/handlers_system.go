package api

import (
	"net/http"
	"os"

	"github.com/tzhnahida/VocabLock/internal/api/mock"
	"github.com/tzhnahida/VocabLock/internal/clock"
	"github.com/tzhnahida/VocabLock/internal/db"
	"github.com/tzhnahida/VocabLock/internal/engine"
	"github.com/tzhnahida/VocabLock/internal/log"
)

func (s *Server) handleSystemStatus(w http.ResponseWriter, r *http.Request) {
	log.Debug("system", "status")
	writeJSON(w, map[string]interface{}{
		"status":      "running",
		"locked":      false,
		"active_lang": s.Config.App.ActiveLang,
	})
}

func (s *Server) handleSystemLock(w http.ResponseWriter, r *http.Request) {
	log.Debug("system", "lock")
	if s.Mock {
		mock.HandleSystemLock(w, r)
		return
	}
	if s.System != nil {
		s.System.FullscreenFlutterWindow()
		hwnd, _ := s.System.FindFlutterWindow()
		if err := s.System.EnterLockMode(hwnd); err != nil {
			writeJSON(w, map[string]interface{}{
				"locked": false,
				"error":  err.Error(),
			})
			return
		}
	}
	writeJSON(w, map[string]interface{}{
		"locked": true,
	})
}

func (s *Server) handleSystemUnlock(w http.ResponseWriter, r *http.Request) {
	log.Debug("system", "unlock")
	if s.Mock {
		mock.HandleSystemUnlock(w, r)
		return
	}
	if s.System != nil {
		s.System.ExitLockMode()
		s.System.RestoreFlutterWindow()
	}
	writeJSON(w, map[string]interface{}{
		"locked": false,
	})
}

// handleSystemEscape handles emergency escape attempts (password or click).
// Password escape: daily limit from config (escape_password_limit, default 1).
// Click escape: daily limit from config (escape_click_limit, default 2).
func (s *Server) handleSystemEscape(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method   string `json:"method"` // "password" or "click"
		Password string `json:"password,omitempty"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "invalid request")
		return
	}

	todayKey := clock.Today()
	s.escapeMu.Lock()
	if s.escapeDate != todayKey {
		s.escapeDate = todayKey
		s.escapePasswordUsed = 0
		s.escapeClickUsed = 0
	}

	switch req.Method {
	case "password":
		max := s.Config.App.EscapePasswordLimit
		if max <= 0 {
			max = 1
		}
		if s.escapePasswordUsed >= max {
			s.escapeMu.Unlock()
			writeJSON(w, map[string]interface{}{
				"allowed": false,
				"reason":  "daily password escape limit reached",
				"used":    s.escapePasswordUsed,
				"max":     max,
			})
			return
		}
		// Verify password (config password, or default)
		expected := s.Config.App.EscapePassword
		if expected == "" {
			expected = "vocablock" // default password
		}
		if req.Password != expected {
			s.escapeMu.Unlock()
			writeJSON(w, map[string]interface{}{
				"allowed": false,
				"reason":  "wrong password",
			})
			return
		}
		s.escapePasswordUsed++
		s.escapeMu.Unlock()
		s.performEscape(w)

	case "click":
		max := s.Config.App.EscapeClickLimit
		if max <= 0 {
			max = 2
		}
		if s.escapeClickUsed >= max {
			s.escapeMu.Unlock()
			writeJSON(w, map[string]interface{}{
				"allowed": false,
				"reason":  "daily click escape limit reached",
				"used":    s.escapeClickUsed,
				"max":     max,
			})
			return
		}
		s.escapeClickUsed++
		s.escapeMu.Unlock()
		s.performEscape(w)

	default:
		s.escapeMu.Unlock()
		writeError(w, 400, "unknown escape method")
	}
}

func (s *Server) performEscape(w http.ResponseWriter) {
	if s.System != nil {
		s.System.ExitLockMode()
		s.System.RestoreFlutterWindow()
	}
	writeJSON(w, map[string]interface{}{
		"allowed":  true,
		"unlocked": true,
	})
}

func (s *Server) handleSystemPassiveQ(w http.ResponseWriter, r *http.Request) {
	if s.Mock {
		mock.HandleSystemPassiveQ(w, r)
		return
	}
	// Return a random word card for passive display widget
	if s.DB != nil {
		words, err := s.DB.GetReviewWords(s.Config.App.ActiveLang, 1)
		if err == nil && len(words) > 0 {
			wd := words[0]
			writeJSON(w, map[string]interface{}{
				"word_id": wd.ID,
				"word":    wd.Word,
				"reading": wd.Reading,
				"meaning": wd.Meaning,
			})
			return
		}
	}
	writeJSON(w, map[string]interface{}{
		"word_id": 0, "word": "", "reading": "", "meaning": "",
	})
}

func (s *Server) handleSystemPassiveSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Mock {
		mock.HandleSystemPassiveSubmit(w, r)
		return
	}

	var req struct {
		WordID int64  `json:"word_id"`
		Recall string `json:"recall"` // "easy", "hard", "forgot"
	}
	if err := readJSON(r, &req); err != nil {
		// Backward compat: accept empty body as basic acknowledgement
		writeJSON(w, map[string]bool{"accepted": true})
		return
	}

	if s.DB != nil && req.WordID > 0 {
		word, err := s.DB.GetWord(req.WordID)
		if err == nil && word != nil {
			// Light SM-2 boost for passive recall
			quality := engine.QualityFromPassiveRecall(req.Recall)
			now := clock.Now()
			sm2 := engine.SM2(word.EasinessFactor, word.Interval, word.Repetitions, quality, now, s.Config.App.Sm2MaxIntervalDays)
			if sm2 != nil {
				word.EasinessFactor = sm2.EasinessFactor
				word.Interval = sm2.Interval
				word.Repetitions = sm2.Repetitions
				word.NextReviewAt = &sm2.NextReviewAt
			}
			word.LastReviewAt = &now
			s.DB.UpdateWord(word)

			// Log passive review
			s.DB.InsertReviewLog(&db.ReviewLog{
				SubjectType:    "word",
				SubjectID:      req.WordID,
				WordID:         req.WordID,
				QuestionType:   "passive",
				ExpectedAnswer: word.Meaning,
				Correct:        quality >= 3,
				Score:          float64(quality) / 5.0,
				Source:         "passive",
			})
			log.Debug("system", "passive submit: word=%s recall=%s quality=%d", word.Word, req.Recall, quality)
		}
	}

	writeJSON(w, map[string]bool{"accepted": true})
}

func (s *Server) handleSystemQuit(w http.ResponseWriter, r *http.Request) {
	if s.Mock {
		mock.HandleSystemQuit(w, r)
		return
	}
	writeJSON(w, map[string]bool{"quitting": true})

	// Graceful shutdown: signal via exit
	go func() {
		s.Shutdown()
		os.Exit(0)
	}()
}

// handleDebugLog accepts log lines from the Flutter frontend so they
// appear in the unified Go server output stream.
func (s *Server) handleDebugLog(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Level    string   `json:"level"`
		Message  string   `json:"message"`
		Messages []string `json:"messages"`
	}
	if err := readJSON(r, &req); err != nil {
		_ = err
	}
	emit := func(msg string) {
		switch req.Level {
		case "warn":
			log.Warn("flutter", "%s", msg)
		case "error":
			log.Error("flutter", "%s", msg)
		default:
			log.Debug("flutter", "%s", msg)
		}
	}
	if req.Message != "" {
		emit(req.Message)
	}
	for _, msg := range req.Messages {
		if msg != "" {
			emit(msg)
		}
	}
	w.WriteHeader(204)
}

// handleSystemRestart re-wires providers and restarts Flutter.
// Called by Flutter after onboarding completes so that TTS/Dict/Pronounce
// pick up the newly configured active language.
func (s *Server) handleSystemRestart(w http.ResponseWriter, r *http.Request) {
	if s.RestartFunc == nil {
		writeError(w, 500, "restart not available")
		return
	}
	s.RestartFunc()
	writeJSON(w, map[string]interface{}{"restarted": true})
}
