package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/tzhnahida/VocabLock/internal/log"
	"gopkg.in/yaml.v3"
)

// ——— App config ———

type AppConfig struct {
	ActiveLang    string `yaml:"active_lang"`
	InterfaceLang string `yaml:"interface_lang"`
	MockMode      bool   `yaml:"mock_mode"`
	Server        struct {
		Host string `yaml:"host"`
	} `yaml:"server"`
	DailyTarget         int     `yaml:"daily_target"`
	DailyTimeTarget     int     `yaml:"daily_time_target"` // minutes per day; unlock when time spent >= this
	MinAccuracy         float64 `yaml:"min_accuracy"`
	PassiveEnabled      bool    `yaml:"passive_enabled"`
	PassiveMinInterval  int     `yaml:"passive_min_interval"`
	PassiveMaxInterval  int     `yaml:"passive_max_interval"`
	EscapePassword      string  `yaml:"escape_password"`
	EscapePasswordLimit int     `yaml:"escape_password_limit"`
	EscapeClickLimit    int     `yaml:"escape_click_limit"`
	AutoStart           bool    `yaml:"auto_start"`
	AutoExpandWords     bool    `yaml:"auto_expand_words"`
	ExpandBatchSize     int     `yaml:"expand_batch_size"`
	Theme               string  `yaml:"theme"`
	// Assignment cadence
	Assignment AssignmentConfig `yaml:"assignment"`
	// Time budget
	SessionTimeBudget  int      `yaml:"session_time_budget"`   // minutes
	MaxSlowTypeRatio   float64  `yaml:"max_slow_type_ratio"`   // 0.0–1.0
	Sm2MaxIntervalDays int      `yaml:"sm2_max_interval_days"` // default 180
	AI                 AIConfig `yaml:"ai"`
}

// ——— AI config ———

type AIConfig struct {
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
	Model   string `yaml:"model"`
}

// ——— TTS config ———

type TTSConfig struct {
	TTS struct {
		CacheLimit    int                    `yaml:"cache_limit"`
		FallbackOrder []string               `yaml:"fallback_order"`
		Providers     map[string]TTSProvider `yaml:"providers"`
	} `yaml:"tts"`
}

type TTSProvider struct {
	Binary string            `yaml:"binary"`
	Args   []string          `yaml:"args"`
	Format string            `yaml:"format"`
	Voices map[string]string `yaml:"voices"`
}

// ——— Dict config ———

type DictConfig struct {
	Dict struct {
		Providers map[string]DictProvider `yaml:"providers"`
	} `yaml:"dict"`
}

type DictProvider struct {
	Lang        string   `yaml:"lang"`
	Binary      string   `yaml:"binary"`
	Args        []string `yaml:"args"`
	Format      string   `yaml:"format"`
	ExamLevels  bool     `yaml:"exam_levels"` // dict returns level field (JLPT/HSK/etc.)
	HealthCheck struct {
		Skip   bool     `yaml:"skip"`
		Custom []string `yaml:"custom"`
	} `yaml:"health_check"`
}

// ——— Pronounce config ———

type PronounceConfig struct {
	Pronounce struct {
		Providers map[string]PronounceProvider `yaml:"providers"`
	} `yaml:"pronounce"`
}

type PronounceProvider struct {
	Lang   string   `yaml:"lang"`
	Binary string   `yaml:"binary"`
	Args   []string `yaml:"args"`
}

// ——— Assignment config ———

type AssignmentConfig struct {
	CadenceDays   int  `yaml:"cadence_days"`
	CadenceCount  int  `yaml:"cadence_count"`
	MaxPerSession int  `yaml:"max_per_session"`
	AIRecommend   bool `yaml:"ai_recommend"`
}

// ——— Language config ———

type LanguageConfig struct {
	Lang struct {
		Code        string `yaml:"code"`
		DisplayName string `yaml:"display_name"`
		NativeName  string `yaml:"native_name"`
	} `yaml:"lang"`
	TTS struct {
		Provider string `yaml:"provider"`
		Voice    string `yaml:"voice"`
	} `yaml:"tts"`
	Dict struct {
		Provider string `yaml:"provider"`
	} `yaml:"dict"`
	Pronounce struct {
		Provider string `yaml:"provider"`
	} `yaml:"pronounce"`
	QuestionTypes []string `yaml:"question_types"`
	PromptContext struct {
		TeacherRole       string            `yaml:"teacher_role"`
		ExplanationLang   string            `yaml:"explanation_lang"`
		LevelDescriptions map[string]string `yaml:"level_descriptions"`
	} `yaml:"prompt_context"`
	WordFieldsBase    []WordFieldDef `yaml:"word_fields_base"`
	WordFieldsDLC     []WordFieldDLC `yaml:"word_fields_dlc"`
	GrammarFieldsBase []WordFieldDef `yaml:"grammar_fields_base"`
	GrammarFieldsDLC  []WordFieldDLC `yaml:"grammar_fields_dlc"`
}

type WordFieldDef struct {
	Key     string   `yaml:"key" json:"key"`
	Label   string   `yaml:"label" json:"label"`
	Type    string   `yaml:"type" json:"type"` // text, select, number
	Options []string `yaml:"options,omitempty" json:"options,omitempty"`
}

type WordFieldDLC struct {
	Category string         `yaml:"category" json:"category"`
	Label    string         `yaml:"label" json:"label"`
	Fields   []WordFieldDef `yaml:"fields" json:"fields"`
}

// ——— Aggregate holder ———

type Config struct {
	App       AppConfig
	TTS       TTSConfig
	Dict      DictConfig
	Pronounce PronounceConfig
	Languages map[string]*LanguageConfig // key = language code
}

func Load(configDir string) (*Config, error) {
	cfg := &Config{
		App: AppConfig{
			ActiveLang:          "",
			InterfaceLang:       "",
			DailyTarget:         20,
			MinAccuracy:         0.8,
			PassiveEnabled:      true,
			PassiveMinInterval:  25,
			PassiveMaxInterval:  60,
			EscapePasswordLimit: 1,
			EscapeClickLimit:    2,
			AutoExpandWords:     true,
			ExpandBatchSize:     10,
			Sm2MaxIntervalDays:  180,
			Theme:               "vs-dark",
			AI: AIConfig{
				BaseURL: "https://api.deepseek.com",
				Model:   "deepseek-chat",
			},
		},
		Languages: make(map[string]*LanguageConfig),
	}
	cfg.App.Server.Host = "127.0.0.1"

	// app.yaml (optional — defaults above are sufficient for auto-test mode)
	_ = loadYAML(filepath.Join(configDir, "app.yaml"), &cfg.App)

	// Env var overrides (keeps secrets out of committed yaml)
	if v := os.Getenv("VOCABLOCK_AI_KEY"); v != "" {
		cfg.App.AI.APIKey = v
	}
	if v := os.Getenv("VOCABLOCK_AI_BASE_URL"); v != "" {
		cfg.App.AI.BaseURL = v
	}
	if v := os.Getenv("VOCABLOCK_AI_MODEL"); v != "" {
		cfg.App.AI.Model = v
	}

	// tts.yaml (optional — community-provided TTS binary)
	_ = loadYAML(filepath.Join(configDir, "tts.yaml"), &cfg.TTS)

	// dict.yaml (optional — community-provided dictionary binary)
	_ = loadYAML(filepath.Join(configDir, "dict.yaml"), &cfg.Dict)

	// pronounce.yaml (optional — nil provider means not configured)
	_ = loadYAML(filepath.Join(configDir, "pronounce.yaml"), &cfg.Pronounce)

	// languages/*.yaml
	langDir := filepath.Join(configDir, "languages")
	entries, err := os.ReadDir(langDir)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read languages dir: %w", err)
		}
	} else {
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
				continue
			}
			var lc LanguageConfig
			if err := loadYAML(filepath.Join(langDir, e.Name()), &lc); err != nil {
				return nil, err
			}
			cfg.Languages[lc.Lang.Code] = &lc
		}
	}

	// Recover missing active language from _languages template (skip on first run)
	if cfg.App.ActiveLang != "" && len(cfg.Languages) > 0 {
		if _, ok := cfg.Languages[cfg.App.ActiveLang]; !ok {
			if lc := TryLoadTemplate(configDir, cfg.App.ActiveLang); lc != nil {
				cfg.Languages[cfg.App.ActiveLang] = lc
				log.Warn("config", "recovered active language %q from template", cfg.App.ActiveLang)
			}
		}
	}

	// Validation
	if errs := cfg.Validate(); len(errs) > 0 {
		for _, e := range errs {
			log.Warn("config", "config error: %v", e)
		}
		return nil, fmt.Errorf("%d config validation error(s)", len(errs))
	}

	return cfg, nil
}

// ReadTemplate reads a _languages template without side effects.
// Returns nil if the file doesn't exist. Does NOT copy to languages/.
func ReadTemplate(configDir, code string) *LanguageConfig {
	templatePath := filepath.Join(configDir, "_languages", code+".yaml")
	data, err := os.ReadFile(templatePath)
	if err != nil {
		return nil
	}
	var lc LanguageConfig
	if err := yaml.Unmarshal(data, &lc); err != nil {
		return nil
	}
	return &lc
}

func TryLoadTemplate(configDir, code string) *LanguageConfig {
	// Try file first
	templatePath := filepath.Join(configDir, "_languages", code+".yaml")
	if data, err := os.ReadFile(templatePath); err == nil {
		activeDir := filepath.Join(configDir, "languages")
		os.MkdirAll(activeDir, 0755)
		if err := os.WriteFile(filepath.Join(activeDir, code+".yaml"), data, 0644); err != nil {
			log.Debug("config", "recover language %q: %v", code, err)
			return nil
		}
		var lc LanguageConfig
		if err := yaml.Unmarshal(data, &lc); err != nil {
			return nil
		}
		return &lc
	}
	// Fallback: built-in minimal config for known languages
	if lc := BuiltinLanguage(code); lc != nil {
		return lc
	}
	return nil
}

// BuiltinLanguage returns a minimal built-in language config for zero-config mode.
// Works without any YAML template files on disk. Supported: ja, zh, en, ko.
func BuiltinLanguage(code string) *LanguageConfig {
	makeLC := func(c, dn, nn, tr, el string, qts []string) *LanguageConfig {
		lc := &LanguageConfig{QuestionTypes: qts}
		lc.Lang.Code = c
		lc.Lang.DisplayName = dn
		lc.Lang.NativeName = nn
		lc.PromptContext.TeacherRole = tr
		lc.PromptContext.ExplanationLang = el
		lc.PromptContext.LevelDescriptions = map[string]string{"beginner": c, "intermediate": c, "advanced": c}
		return lc
	}
	switch code {
	case "ja":
		return makeLC("ja", "日本語", "日语", "日本語教師", "zh-CN",
			[]string{"choice", "fill", "spelling", "pronounce", "meaning", "listening", "reading_comp", "grammar", "read_word", "read_sentence"})
	case "zh":
		return makeLC("zh", "中文", "中文", "中文教师", "zh-CN",
			[]string{"choice", "fill", "spelling", "pronounce", "meaning", "listening", "reading_comp", "grammar", "read_word", "read_sentence"})
	case "en":
		return makeLC("en", "English", "English", "English Teacher", "zh-CN",
			[]string{"choice", "fill", "spelling", "pronounce", "meaning"})
	case "ko":
		return makeLC("ko", "한국어", "韩语", "한국어 교사", "zh-CN",
			[]string{"choice", "fill", "spelling", "pronounce", "meaning", "listening", "reading_comp", "grammar", "read_word", "read_sentence"})
	}
	return nil
}

func (c *Config) SaveAppConfig(configDir string) error {
	data, err := yaml.Marshal(&c.App)
	if err != nil {
		return fmt.Errorf("marshal app config: %w", err)
	}
	path := filepath.Join(configDir, "app.yaml")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write app.yaml: %w", err)
	}
	return nil
}

func loadYAML(path string, v interface{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	if err := yaml.Unmarshal(data, v); err != nil {
		return fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	return nil
}
