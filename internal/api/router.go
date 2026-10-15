package api

import (
	"net/http"
)

func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/health", s.handleHealth)

	// Serve generated audio files
	mux.Handle("GET /data/audio/", http.StripPrefix("/data/audio/", http.FileServer(http.Dir("data/audio"))))

	prefix := "/api/v1"

	// Quiz
	mux.HandleFunc("POST "+prefix+"/quiz/session/start", s.handleQuizSessionStart)
	mux.HandleFunc("POST "+prefix+"/quiz/session/continue", s.handleQuizSessionContinue)
	mux.HandleFunc("GET "+prefix+"/quiz/progress", s.handleQuizProgress)
	mux.HandleFunc("GET "+prefix+"/quiz/next", s.handleQuizNext)
	mux.HandleFunc("POST "+prefix+"/quiz/submit", s.handleQuizSubmit)
	mux.HandleFunc("GET "+prefix+"/quiz/today-stats", s.handleQuizTodayStats)
	mux.HandleFunc("POST "+prefix+"/quiz/complete", s.handleQuizComplete)
	mux.HandleFunc("POST "+prefix+"/quiz/dispute", s.handleQuizDispute)

	// Session
	mux.HandleFunc("POST "+prefix+"/quiz/summarize", s.handleQuizSummarize)
	mux.HandleFunc("GET "+prefix+"/quiz/plan", s.handleQuizPlan)
	mux.HandleFunc("PUT "+prefix+"/quiz/direction", s.handleQuizDirection)

	// Assignments (after-class)
	mux.HandleFunc("GET "+prefix+"/quiz/assignment", s.handleAssignmentNext)
	mux.HandleFunc("POST "+prefix+"/quiz/assignment/submit", s.handleAssignmentSubmit)
	mux.HandleFunc("GET "+prefix+"/quiz/assignment/cadence", s.handleAssignmentCadence)

	// Words
	mux.HandleFunc("GET "+prefix+"/words", s.handleWordsList)
	// Today's new words for study-before-test phase
	mux.HandleFunc("GET "+prefix+"/words/new", s.handleWordsNew)
	mux.HandleFunc("GET "+prefix+"/words/{id}", s.handleWordsGet)
	mux.HandleFunc("POST "+prefix+"/words", s.handleWordsCreate)
	mux.HandleFunc("PUT "+prefix+"/words/{id}", s.handleWordsUpdate)
	mux.HandleFunc("DELETE "+prefix+"/words/{id}", s.handleWordsDelete)
	mux.HandleFunc("POST "+prefix+"/words/{id}/reset", s.handleWordsReset)
	mux.HandleFunc("GET "+prefix+"/words/export", s.handleWordsExport)
	mux.HandleFunc("POST "+prefix+"/words/import", s.handleWordsImport)
	mux.HandleFunc("POST "+prefix+"/words/expand", s.handleWordsExpand)
	mux.HandleFunc("GET "+prefix+"/words/pending-review", s.handleWordsPendingReview)
	mux.HandleFunc("POST "+prefix+"/words/pending-review/{id}/accept", s.handleWordsPendingAccept)
	mux.HandleFunc("POST "+prefix+"/words/pending-review/accept-all", s.handleWordsPendingAcceptAll)
	mux.HandleFunc("POST "+prefix+"/words/pending-review/{id}/reject", s.handleWordsPendingReject)
	mux.HandleFunc("PUT "+prefix+"/words/{id}/tags", s.handleWordsTags)
	mux.HandleFunc("GET "+prefix+"/grammar-points", s.handleGrammarPointsList)
	mux.HandleFunc("POST "+prefix+"/grammar-points", s.handleGrammarPointCreate)
	mux.HandleFunc("PUT "+prefix+"/grammar-points/{id}", s.handleGrammarPointUpdate)
	mux.HandleFunc("DELETE "+prefix+"/grammar-points/{id}", s.handleGrammarPointDelete)

	// AI
	mux.HandleFunc("POST "+prefix+"/ai/chat", s.handleAIChat)
	mux.HandleFunc("GET "+prefix+"/ai/profile", s.handleAIProfile)
	mux.HandleFunc("PUT "+prefix+"/ai/profile", s.handleAIProfileUpdate)

	// TTS
	mux.HandleFunc("GET "+prefix+"/tts/word/{id}", s.handleTTSWord)
	mux.HandleFunc("POST "+prefix+"/tts/text", s.handleTTSText)

	// Pronounce
	mux.HandleFunc("POST "+prefix+"/pronounce/assess", s.handlePronounceAssess)

	// Stats
	mux.HandleFunc("GET "+prefix+"/stats/heatmap", s.handleStatsHeatmap)
	mux.HandleFunc("GET "+prefix+"/stats/diagnostics", s.handleStatsDiagnostics)
	mux.HandleFunc("GET "+prefix+"/stats/streak", s.handleStatsStreak)
	mux.HandleFunc("GET "+prefix+"/stats/daily-report", s.handleStatsDailyReport)

	// System
	mux.HandleFunc("GET "+prefix+"/system/status", s.handleSystemStatus)
	mux.HandleFunc("POST "+prefix+"/system/lock", s.handleSystemLock)
	mux.HandleFunc("POST "+prefix+"/system/unlock", s.handleSystemUnlock)
	mux.HandleFunc("GET "+prefix+"/system/passive-q", s.handleSystemPassiveQ)
	mux.HandleFunc("POST "+prefix+"/system/passive-submit", s.handleSystemPassiveSubmit)
	mux.HandleFunc("POST "+prefix+"/system/quit", s.handleSystemQuit)
	mux.HandleFunc("POST "+prefix+"/system/escape", s.handleSystemEscape)
	mux.HandleFunc("POST "+prefix+"/system/restart", s.handleSystemRestart)
	mux.HandleFunc("POST "+prefix+"/debug/log", s.handleDebugLog)

	// Config
	mux.HandleFunc("GET "+prefix+"/config", s.handleConfigGet)
	mux.HandleFunc("GET "+prefix+"/config/{key}", s.handleConfigGetKey)
	mux.HandleFunc("PUT "+prefix+"/config", s.handleConfigUpdate)
	mux.HandleFunc("POST "+prefix+"/config/password", s.handleConfigPassword)

	// Config — AI
	mux.HandleFunc("GET "+prefix+"/config/ai", s.handleConfigAI)
	mux.HandleFunc("PUT "+prefix+"/config/ai", s.handleConfigAIUpdate)

	// Config — TTS providers
	mux.HandleFunc("GET "+prefix+"/config/tts", s.handleConfigTTSList)
	mux.HandleFunc("POST "+prefix+"/config/tts", s.handleConfigTTSSave)
	mux.HandleFunc("GET "+prefix+"/config/tts/{name}", s.handleConfigTTSGet)
	mux.HandleFunc("PUT "+prefix+"/config/tts/{name}", s.handleConfigTTSSave)
	mux.HandleFunc("DELETE "+prefix+"/config/tts/{name}", s.handleConfigTTSDelete)

	// Config — Dict providers
	mux.HandleFunc("GET "+prefix+"/config/dict", s.handleConfigDictList)
	mux.HandleFunc("POST "+prefix+"/config/dict", s.handleConfigDictSave)
	mux.HandleFunc("GET "+prefix+"/config/dict/{name}", s.handleConfigDictGet)
	mux.HandleFunc("PUT "+prefix+"/config/dict/{name}", s.handleConfigDictSave)
	mux.HandleFunc("DELETE "+prefix+"/config/dict/{name}", s.handleConfigDictDelete)

	// Config — Language
	mux.HandleFunc("GET "+prefix+"/config/language/{code}", s.handleConfigLanguageGet)
	mux.HandleFunc("PUT "+prefix+"/config/language/{code}", s.handleConfigLanguageUpdate)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}
