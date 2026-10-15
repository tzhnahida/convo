package api

import (
	"net/http"

	"github.com/tzhnahida/VocabLock/internal/api/mock"
	"github.com/tzhnahida/VocabLock/internal/config"
	"github.com/tzhnahida/VocabLock/internal/log"
)

func (s *Server) handleConfigGet(w http.ResponseWriter, r *http.Request) {
	s.configMu.RLock()
	defer s.configMu.RUnlock()

	langs := make([]string, 0, len(s.Config.Languages))
	for code := range s.Config.Languages {
		langs = append(langs, code)
	}

	langDetails := make(map[string]interface{})
	for code, lc := range s.Config.Languages {
		ttsVoice := lc.TTS.Voice
		ttsProvider := lc.TTS.Provider
		dictProvider := lc.Dict.Provider
		langDetails[code] = map[string]interface{}{
			"display_name":  lc.Lang.DisplayName,
			"native_name":   lc.Lang.NativeName,
			"tts_provider":  ttsProvider,
			"tts_voice":     ttsVoice,
			"dict_provider": dictProvider,
		}
	}

	writeJSON(w, map[string]interface{}{
		"active_lang":           s.Config.App.ActiveLang,
		"interface_lang":        s.Config.App.InterfaceLang,
		"mock_mode":             s.Config.App.MockMode,
		"daily_target":          s.Config.App.DailyTarget,
		"min_accuracy":          s.Config.App.MinAccuracy,
		"passive_enabled":       s.Config.App.PassiveEnabled,
		"passive_min_interval":  s.Config.App.PassiveMinInterval,
		"passive_max_interval":  s.Config.App.PassiveMaxInterval,
		"escape_password_limit": s.Config.App.EscapePasswordLimit,
		"escape_click_limit":    s.Config.App.EscapeClickLimit,
		"auto_start":            s.Config.App.AutoStart,
		"auto_expand_words":     s.Config.App.AutoExpandWords,
		"expand_batch_size":     s.Config.App.ExpandBatchSize,
		"sm2_max_interval_days": s.Config.App.Sm2MaxIntervalDays,
		"session_time_budget":   s.Config.App.SessionTimeBudget,
		"max_slow_type_ratio":   s.Config.App.MaxSlowTypeRatio,
		"theme":                 s.Config.App.Theme,
		"model":                 s.Config.App.AI.Model,
		"base_url":              s.Config.App.AI.BaseURL,
		"languages":             langs,
		"language_details":      langDetails,
		"assignment": map[string]interface{}{
			"cadence_days":    s.Config.App.Assignment.CadenceDays,
			"cadence_count":   s.Config.App.Assignment.CadenceCount,
			"max_per_session": s.Config.App.Assignment.MaxPerSession,
			"ai_recommend":    s.Config.App.Assignment.AIRecommend,
		},
	})
}

func (s *Server) handleConfigGetKey(w http.ResponseWriter, r *http.Request) {
	s.configMu.RLock()
	defer s.configMu.RUnlock()

	key := r.PathValue("key")
	var value interface{}

	switch key {
	case "active_lang":
		value = s.Config.App.ActiveLang
	case "interface_lang":
		value = s.Config.App.InterfaceLang
		log.Debug("config", "GET config/interface_lang = %q", s.Config.App.InterfaceLang)
	case "daily_target":
		value = s.Config.App.DailyTarget
	case "languages":
		langs := make([]string, 0, len(s.Config.Languages))
		for code := range s.Config.Languages {
			langs = append(langs, code)
		}
		value = langs
	case "assignment":
		value = map[string]interface{}{
			"cadence_days":    s.Config.App.Assignment.CadenceDays,
			"cadence_count":   s.Config.App.Assignment.CadenceCount,
			"max_per_session": s.Config.App.Assignment.MaxPerSession,
			"ai_recommend":    s.Config.App.Assignment.AIRecommend,
		}
	default:
		writeError(w, 404, "unknown config key: "+key)
		return
	}

	writeJSON(w, map[string]interface{}{
		"key":   key,
		"value": value,
	})
}

func (s *Server) handleConfigUpdate(w http.ResponseWriter, r *http.Request) {
	s.configMu.Lock()
	defer s.configMu.Unlock()

	var req map[string]interface{}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}

	langChanged := false
	if v, ok := req["interface_lang"]; ok {
		if lang, ok := v.(string); ok && lang != "" {
			s.Config.App.InterfaceLang = lang
		}
	}

	if v, ok := req["active_lang"]; ok {
		if lang, ok := v.(string); ok {
			// Initialize from _languages template on first run if needed
			if _, exists := s.Config.Languages[lang]; !exists {
				if lc := s.tryLoadLanguageTemplate(lang); lc != nil {
					s.Config.Languages[lang] = lc
				} else {
					writeError(w, 400, "unknown language: "+lang)
					return
				}
			}
			if s.Config.App.ActiveLang != lang {
				langChanged = true
			}
			s.Config.App.ActiveLang = lang
		}
	}

	if v, ok := req["daily_target"]; ok {
		switch n := v.(type) {
		case float64:
			if n > 0 && n <= 200 {
				s.Config.App.DailyTarget = int(n)
			}
		}
	}

	// daily_time_target (minutes; 0 = disabled). Was previously sent by the
	// onboarding UI but silently dropped — the time-based unlock never worked.
	if v, ok := req["daily_time_target"]; ok {
		switch n := v.(type) {
		case float64:
			if n >= 0 && n <= 600 {
				s.Config.App.DailyTimeTarget = int(n)
			}
		}
	}

	if v, ok := req["theme"]; ok {
		if theme, ok := v.(string); ok && theme != "" {
			s.Config.App.Theme = theme
		}
	}

	// Assignment config — support both nested "assignment" object and flat keys
	if v, ok := req["assignment"]; ok {
		if am, ok := v.(map[string]interface{}); ok {
			updateAssignmentConfig(&s.Config.App.Assignment, am)
		}
	}
	updateAssignmentConfig(&s.Config.App.Assignment, req)

	if v, ok := req["min_accuracy"]; ok {
		if n, ok := v.(float64); ok && n >= 0 && n <= 1 {
			s.Config.App.MinAccuracy = n
		}
	}
	if v, ok := req["auto_expand_words"]; ok {
		if b, ok := v.(bool); ok {
			s.Config.App.AutoExpandWords = b
		}
	}
	if v, ok := req["expand_batch_size"]; ok {
		if n, ok := v.(float64); ok {
			iv := int(n)
			if iv > 0 && iv <= 50 {
				s.Config.App.ExpandBatchSize = iv
			}
		}
	}
	if v, ok := req["sm2_max_interval_days"]; ok {
		if n, ok := v.(float64); ok {
			iv := int(n)
			if iv >= 30 && iv <= 365 {
				s.Config.App.Sm2MaxIntervalDays = iv
			}
		}
	}
	if v, ok := req["session_time_budget"]; ok {
		if n, ok := v.(float64); ok {
			iv := int(n)
			if iv >= 0 && iv <= 120 {
				s.Config.App.SessionTimeBudget = iv
			}
		}
	}
	if v, ok := req["max_slow_type_ratio"]; ok {
		if n, ok := v.(float64); ok && n >= 0 && n <= 1 {
			s.Config.App.MaxSlowTypeRatio = n
		}
	}

	if v, ok := req["auto_start"]; ok {
		if b, ok := v.(bool); ok && b != s.Config.App.AutoStart {
			s.Config.App.AutoStart = b
			if s.System != nil {
				if err := s.System.SetAutoStart(b); err != nil {
					log.Warn("config", "set auto-start: %v", err)
				} else {
					log.Info("config", "auto-start: %v", b)
				}
			}
		}
	}

	// Persist changes to app.yaml (skip in mock mode to avoid touching real config)
	if !s.Mock {
		if err := s.Config.SaveAppConfig(s.ConfigDir); err != nil {
			log.Error("config", "save: %v", err)
		}
	}

	// Re-wire providers + reset per-language state + bootstrap for newly activated language
	if langChanged {
		s.WireTTS()
		s.WireDict()
		s.WirePronounce()
		s.sessionBudget.reset()
		s.markBootstrapNotDone()
		go s.EnsureBootstrap(s.Config.App.ActiveLang)
	}

	writeJSON(w, map[string]interface{}{
		"updated":      true,
		"active_lang":  s.Config.App.ActiveLang,
		"daily_target": s.Config.App.DailyTarget,
		"assignment": map[string]interface{}{
			"cadence_days":    s.Config.App.Assignment.CadenceDays,
			"cadence_count":   s.Config.App.Assignment.CadenceCount,
			"max_per_session": s.Config.App.Assignment.MaxPerSession,
			"ai_recommend":    s.Config.App.Assignment.AIRecommend,
		},
	})
}

func updateAssignmentConfig(cfg *config.AssignmentConfig, m map[string]interface{}) {
	if v, ok := m["cadence_days"]; ok {
		switch n := v.(type) {
		case float64:
			if n >= 1 && n <= 30 {
				cfg.CadenceDays = int(n)
			}
		}
	}
	if v, ok := m["cadence_count"]; ok {
		switch n := v.(type) {
		case float64:
			if n >= 1 && n <= 10 {
				cfg.CadenceCount = int(n)
			}
		}
	}
	if v, ok := m["max_per_session"]; ok {
		switch n := v.(type) {
		case float64:
			if n >= 1 && n <= 20 {
				cfg.MaxPerSession = int(n)
			}
		}
	}
	if v, ok := m["ai_recommend"]; ok {
		if b, ok := v.(bool); ok {
			cfg.AIRecommend = b
		}
	}
}

func (s *Server) tryLoadLanguageTemplate(code string) *config.LanguageConfig {
	return config.TryLoadTemplate(s.ConfigDir, code)
}

func (s *Server) handleConfigPassword(w http.ResponseWriter, r *http.Request) {
	if s.Mock {
		mock.HandleConfigPassword(w, r)
		return
	}
	writeNotImplemented(w)
}
