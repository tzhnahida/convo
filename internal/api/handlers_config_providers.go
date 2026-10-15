package api

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tzhnahida/VocabLock/internal/config"
	"github.com/tzhnahida/VocabLock/internal/log"
	"gopkg.in/yaml.v3"
)

// ——— AI config ———

func (s *Server) handleConfigAI(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"base_url":   s.Config.App.AI.BaseURL,
		"model":      s.Config.App.AI.Model,
		"configured": s.Config.App.AI.APIKey != "",
	})
}

func (s *Server) handleConfigAIUpdate(w http.ResponseWriter, r *http.Request) {
	s.configMu.Lock()
	defer s.configMu.Unlock()

	var req struct {
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
		Model   string `json:"model"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "invalid body")
		return
	}
	if req.BaseURL != "" {
		s.Config.App.AI.BaseURL = req.BaseURL
	}
	if req.APIKey != "" {
		s.Config.App.AI.APIKey = req.APIKey
	}
	if req.Model != "" {
		s.Config.App.AI.Model = req.Model
	}
	s.Config.SaveAppConfig(s.ConfigDir)
	s.WireAI()
	writeJSON(w, map[string]interface{}{"updated": true})
}

// ——— TTS providers ———

func (s *Server) handleConfigTTSList(w http.ResponseWriter, r *http.Request) {
	providers := make([]map[string]interface{}, 0)
	for name, p := range s.Config.TTS.TTS.Providers {
		providers = append(providers, map[string]interface{}{
			"name":    name,
			"binary":  p.Binary,
			"args":    p.Args,
			"voices":  p.Voices,
			"healthy": s.checkProviderHealth("tts", name),
		})
	}
	writeJSON(w, map[string]interface{}{
		"providers":      providers,
		"cache_limit":    s.Config.TTS.TTS.CacheLimit,
		"fallback_order": s.Config.TTS.TTS.FallbackOrder,
	})
}

func (s *Server) handleConfigTTSGet(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, ok := s.Config.TTS.TTS.Providers[name]
	if !ok {
		writeError(w, 404, "provider not found")
		return
	}
	writeJSON(w, map[string]interface{}{
		"name":    name,
		"binary":  p.Binary,
		"args":    p.Args,
		"voices":  p.Voices,
		"healthy": s.checkProviderHealth("tts", name),
	})
}

func (s *Server) handleConfigTTSSave(w http.ResponseWriter, r *http.Request) {
	s.configMu.Lock()
	defer s.configMu.Unlock()

	name := r.PathValue("name")
	_, exists := s.Config.TTS.TTS.Providers[name]
	isNew := name != "" && !exists

	var req struct {
		Name   string            `json:"name"`
		Binary string            `json:"binary"`
		Args   []string          `json:"args"`
		Voices map[string]string `json:"voices"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "invalid body")
		return
	}
	if req.Name != "" {
		name = req.Name
	}
	if name == "" {
		writeError(w, 400, "name required")
		return
	}

	if isNew {
		s.Config.TTS.TTS.Providers[name] = config.TTSProvider{}
	}
	p := s.Config.TTS.TTS.Providers[name]
	if req.Binary != "" {
		if err := s.validateProviderBinary(req.Binary); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		p.Binary = req.Binary
	}
	if req.Args != nil {
		p.Args = req.Args
	}
	if req.Voices != nil {
		p.Voices = req.Voices
	}
	s.Config.TTS.TTS.Providers[name] = p
	s.saveTTSConfig()
	writeJSON(w, map[string]interface{}{"updated": true, "name": name})
}

func (s *Server) handleConfigTTSDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.Config.TTS.TTS.Providers[name]; !ok {
		writeError(w, 404, "provider not found")
		return
	}
	delete(s.Config.TTS.TTS.Providers, name)
	s.saveTTSConfig()
	writeJSON(w, map[string]interface{}{"deleted": true, "name": name})
}

// ——— Dict providers ———

func (s *Server) handleConfigDictList(w http.ResponseWriter, r *http.Request) {
	providers := make([]map[string]interface{}, 0)
	for name, p := range s.Config.Dict.Dict.Providers {
		providers = append(providers, map[string]interface{}{
			"name":        name,
			"lang":        p.Lang,
			"binary":      p.Binary,
			"args":        p.Args,
			"exam_levels": p.ExamLevels,
			"healthy":     s.checkProviderHealth("dict", name),
		})
	}
	writeJSON(w, map[string]interface{}{
		"providers": providers,
	})
}

func (s *Server) handleConfigDictGet(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, ok := s.Config.Dict.Dict.Providers[name]
	if !ok {
		writeError(w, 404, "provider not found")
		return
	}
	writeJSON(w, map[string]interface{}{
		"name":        name,
		"lang":        p.Lang,
		"binary":      p.Binary,
		"args":        p.Args,
		"exam_levels": p.ExamLevels,
		"healthy":     s.checkProviderHealth("dict", name),
	})
}

func (s *Server) handleConfigDictSave(w http.ResponseWriter, r *http.Request) {
	s.configMu.Lock()
	defer s.configMu.Unlock()

	name := r.PathValue("name")
	_, exists := s.Config.Dict.Dict.Providers[name]
	isNew := name != "" && !exists

	var req struct {
		Name       string   `json:"name"`
		Lang       string   `json:"lang"`
		Binary     string   `json:"binary"`
		Args       []string `json:"args"`
		ExamLevels *bool    `json:"exam_levels"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "invalid body")
		return
	}
	if req.Name != "" {
		name = req.Name
	}
	if name == "" {
		writeError(w, 400, "name required")
		return
	}

	if isNew {
		s.Config.Dict.Dict.Providers[name] = config.DictProvider{}
	}
	p := s.Config.Dict.Dict.Providers[name]
	if req.Lang != "" {
		p.Lang = req.Lang
	}
	if req.Binary != "" {
		if err := s.validateProviderBinary(req.Binary); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		p.Binary = req.Binary
	}
	if req.Args != nil {
		p.Args = req.Args
	}
	if req.ExamLevels != nil {
		p.ExamLevels = *req.ExamLevels
	}
	s.Config.Dict.Dict.Providers[name] = p
	s.saveDictConfig()
	writeJSON(w, map[string]interface{}{"updated": true, "name": name})
}

func (s *Server) handleConfigDictDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.Config.Dict.Dict.Providers[name]; !ok {
		writeError(w, 404, "provider not found")
		return
	}
	delete(s.Config.Dict.Dict.Providers, name)
	s.saveDictConfig()
	writeJSON(w, map[string]interface{}{"deleted": true, "name": name})
}

// ——— Language config ———

func (s *Server) handleConfigLanguageGet(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	lc, ok := s.Config.Languages[code]
	if !ok {
		// Try template file first (has real tts/dict providers), then builtin.
		lc2 := config.ReadTemplate(s.ConfigDir, code)
		if lc2 == nil {
			lc2 = config.BuiltinLanguage(code)
		}
		if lc2 != nil {
			writeJSON(w, map[string]interface{}{
				"code":             code,
				"display_name":     lc2.Lang.DisplayName,
				"native_name":      lc2.Lang.NativeName,
				"tts_provider":     lc2.TTS.Provider,
				"tts_voice":        lc2.TTS.Voice,
				"dict_provider":    lc2.Dict.Provider,
				"question_types":   lc2.QuestionTypes,
				"prompt_context":   lc2.PromptContext,
				"word_fields_base": lc2.WordFieldsBase,
				"word_fields_dlc":  lc2.WordFieldsDLC,
			})
			return
		}
		writeError(w, 404, "language not found")
		return
	}
	writeJSON(w, map[string]interface{}{
		"code":             code,
		"display_name":     lc.Lang.DisplayName,
		"native_name":      lc.Lang.NativeName,
		"tts_provider":     lc.TTS.Provider,
		"tts_voice":        lc.TTS.Voice,
		"dict_provider":    lc.Dict.Provider,
		"question_types":   lc.QuestionTypes,
		"prompt_context":   lc.PromptContext,
		"word_fields_base": lc.WordFieldsBase,
		"word_fields_dlc":  lc.WordFieldsDLC,
	})
}

func (s *Server) handleConfigLanguageUpdate(w http.ResponseWriter, r *http.Request) {
	s.configMu.Lock()
	defer s.configMu.Unlock()

	code := r.PathValue("code")
	lc, ok := s.Config.Languages[code]
	if !ok {
		writeError(w, 404, "language not found")
		return
	}
	var req struct {
		TTSProvider   string   `json:"tts_provider"`
		TTSVoice      string   `json:"tts_voice"`
		DictProvider  string   `json:"dict_provider"`
		QuestionTypes []string `json:"question_types"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "invalid body")
		return
	}
	if req.TTSProvider != "" {
		lc.TTS.Provider = req.TTSProvider
	}
	if req.TTSVoice != "" {
		lc.TTS.Voice = req.TTSVoice
	}
	if req.DictProvider != "" {
		lc.Dict.Provider = req.DictProvider
	}
	if req.QuestionTypes != nil {
		lc.QuestionTypes = req.QuestionTypes
	}
	s.saveLanguageConfig(code, lc)
	writeJSON(w, map[string]interface{}{"updated": true, "code": code})
}

// safeSystemBinaries lists interpreter binaries that may be resolved from PATH.
// These are the backbone for dict/TTS/pronounce scripts.
var safeSystemBinaries = map[string]bool{
	"python":     true,
	"python3":    true,
	"python.exe": true,
	"powershell": true,
	"pwsh":       true,
	"pwsh.exe":   true,
}

// projectRoot returns the backend directory (parent of ConfigDir).
func (s *Server) projectRoot() string {
	return filepath.Clean(filepath.Join(s.ConfigDir, ".."))
}

// validateProviderBinary checks that binary is safe to execute:
// - It matches a known-safe interpreter name (python/powershell), OR
// - It resolves to an existing file within the backend project directory
func (s *Server) validateProviderBinary(binary string) error {
	if binary == "" {
		return nil
	}
	if safeSystemBinaries[binary] {
		return nil
	}

	// Resolve the binary path.
	resolved, err := exec.LookPath(binary)
	if err != nil {
		// Not on PATH; try as relative to project root.
		if !filepath.IsAbs(binary) {
			resolved = filepath.Join(s.projectRoot(), binary)
		} else {
			resolved = binary
		}
		resolved = filepath.Clean(resolved)
	}
	// Resolve any remaining relative components + make absolute.
	absResolved, err := filepath.Abs(resolved)
	if err != nil {
		return fmt.Errorf("cannot resolve binary path %q: %w", binary, err)
	}
	if _, err := os.Stat(absResolved); err != nil {
		return fmt.Errorf("binary not found: %s", binary)
	}

	absRoot, _ := filepath.Abs(s.projectRoot())
	prefix := absRoot + string(filepath.Separator)
	if !strings.HasPrefix(absResolved, prefix) {
		return fmt.Errorf("binary %q is outside project directory — only project-local binaries or known interpreters (python/powershell) are allowed", binary)
	}
	return nil
}

// ——— save helpers ———

func (s *Server) saveTTSConfig() {
	s.saveYAML(s.ConfigDir+"/tts.yaml", &struct {
		TTS interface{} `yaml:"tts"`
	}{s.Config.TTS.TTS})
}

func (s *Server) saveDictConfig() {
	s.saveYAML(s.ConfigDir+"/dict.yaml", &struct {
		Dict interface{} `yaml:"dict"`
	}{s.Config.Dict.Dict})
}

func (s *Server) saveLanguageConfig(code string, lc *config.LanguageConfig) {
	s.saveYAML(s.ConfigDir+"/languages/"+code+".yaml", lc)
}

func (s *Server) saveYAML(fpath string, v interface{}) {
	data, err := yaml.Marshal(v)
	if err != nil {
		log.Error("config", "marshal yaml: %v", err)
		return
	}
	// Resolve relative paths against ConfigDir
	if !filepath.IsAbs(fpath) {
		fpath = filepath.Join(s.ConfigDir, fpath)
	}
	if err := os.WriteFile(fpath, data, 0644); err != nil {
		log.Error("config", "write yaml %s: %v", filepath.Base(fpath), err)
	}
}

func (s *Server) checkProviderHealth(providerType, name string) bool {
	switch providerType {
	case "tts":
		// check if TTS provider exists
		_, ok := s.Config.TTS.TTS.Providers[name]
		return ok && s.TTSProvider != nil
	case "dict":
		_, ok := s.Config.Dict.Dict.Providers[name]
		return ok && s.DictProvider != nil
	}
	return false
}
