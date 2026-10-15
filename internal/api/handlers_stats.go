package api

import (
	"net/http"
	"strconv"

	"github.com/tzhnahida/VocabLock/internal/api/mock"
	"github.com/tzhnahida/VocabLock/internal/log"
)

func (s *Server) handleStatsHeatmap(w http.ResponseWriter, r *http.Request) {
	if s.Mock {
		mock.HandleStatsHeatmap(w, r)
		return
	}
	if s.DB == nil {
		writeError(w, 500, "database not available")
		return
	}

	lang := s.Config.App.ActiveLang
	log.Debug("stats", "heatmap: lang=%s", lang)

	days := 90
	if d := r.URL.Query().Get("days"); d != "" {
		if parsed, err := strconv.Atoi(d); err == nil && parsed > 0 && parsed <= 365 {
			days = parsed
		}
	}

	heatmap, err := s.DB.GetHeatmap(days, lang)
	if err != nil {
		writeError(w, 500, "get heatmap: "+err.Error())
		return
	}

	writeJSON(w, heatmap)
}

func (s *Server) handleStatsStreak(w http.ResponseWriter, r *http.Request) {
	if s.Mock {
		mock.HandleStatsStreak(w, r)
		return
	}
	if s.DB == nil {
		writeError(w, 500, "database not available")
		return
	}

	streak, err := s.DB.GetStreak(s.Config.App.ActiveLang)
	if err != nil {
		writeError(w, 500, "get streak: "+err.Error())
		return
	}

	writeJSON(w, map[string]interface{}{
		"streak": streak,
	})
}

func (s *Server) handleStatsDailyReport(w http.ResponseWriter, r *http.Request) {
	if s.Mock {
		mock.HandleStatsDailyReport(w, r)
		return
	}
	if s.DB == nil {
		writeError(w, 500, "database not available")
		return
	}

	lang := s.Config.App.ActiveLang
	reviewed, correct, err := s.DB.GetTodayStats(lang)
	if err != nil {
		writeError(w, 500, "get today stats: "+err.Error())
		return
	}

	accuracy := 0.0
	if reviewed > 0 {
		accuracy = float64(correct) / float64(reviewed)
	}

	writeJSON(w, map[string]interface{}{
		"date":           "today",
		"words_reviewed": reviewed,
		"words_correct":  correct,
		"accuracy":       accuracy,
	})
}

// handleStatsDiagnostics returns learning-quality signals for the AI/quality
// audit loop: error-category distribution, weak phonemes, AI-vs-deterministic
// disagreement, per-type error rates and vocabulary coverage.
func (s *Server) handleStatsDiagnostics(w http.ResponseWriter, r *http.Request) {
	if s.Mock {
		writeJSON(w, map[string]interface{}{"message": "mock mode: no diagnostics"})
		return
	}
	if s.DB == nil {
		writeError(w, 500, "database not available")
		return
	}
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 365 {
			days = n
		}
	}
	lang := s.Config.App.ActiveLang
	diag, err := s.DB.Diagnostics(lang, days)
	if err != nil {
		writeError(w, 500, "diagnostics: "+err.Error())
		return
	}
	writeJSON(w, diag)
}
