package config

import "testing"

func TestValidateValidConfig(t *testing.T) {
	c := &Config{
		App: AppConfig{ActiveLang: "en"},
		Languages: map[string]*LanguageConfig{
			"en": {
				QuestionTypes: []string{"choice", "fill"},
			},
		},
	}
	if errs := c.Validate(); len(errs) != 0 {
		t.Errorf("expected no errors, got %d: %v", len(errs), errs)
	}
}

func TestValidateMissingLanguage(t *testing.T) {
	c := &Config{
		App:       AppConfig{ActiveLang: "ja"},
		Languages: map[string]*LanguageConfig{"en": {}},
	}
	errs := c.Validate()
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d", len(errs))
	}
	if errs[0].Error() != `active_lang "ja" not defined in languages/` {
		t.Errorf("wrong error: %v", errs[0])
	}
}

func TestValidateEmptyLanguagesFirstRun(t *testing.T) {
	c := &Config{
		App:       AppConfig{ActiveLang: "zh"},
		Languages: map[string]*LanguageConfig{},
	}
	errs := c.Validate()
	if len(errs) != 0 {
		t.Fatalf("expected 0 errors for first run (empty languages), got %d: %v", len(errs), errs)
	}
}

func TestValidateTTsProviderNotInConfig(t *testing.T) {
	c := &Config{
		App: AppConfig{ActiveLang: ""},
		Languages: map[string]*LanguageConfig{
			"en": {
				TTS: struct {
					Provider string `yaml:"provider"`
					Voice    string `yaml:"voice"`
				}{Provider: "azure", Voice: ""},
			},
		},
	}
	errs := c.Validate()
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d", len(errs))
	}
}

func TestValidateTTSVoiceMissing(t *testing.T) {
	c := &Config{
		App: AppConfig{ActiveLang: ""},
		TTS: TTSConfig{
			TTS: struct {
				CacheLimit    int                    `yaml:"cache_limit"`
				FallbackOrder []string               `yaml:"fallback_order"`
				Providers     map[string]TTSProvider `yaml:"providers"`
			}{
				Providers: map[string]TTSProvider{
					"azure": {
						Voices: map[string]string{}, // no voices
					},
				},
			},
		},
		Languages: map[string]*LanguageConfig{
			"en": {
				TTS: struct {
					Provider string `yaml:"provider"`
					Voice    string `yaml:"voice"`
				}{Provider: "azure", Voice: ""},
			},
		},
	}
	errs := c.Validate()
	// Should fail: provider exists but no voice for language
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d: %v", len(errs), errs)
	}
}

func TestValidateDictProviderNotInConfig(t *testing.T) {
	c := &Config{
		App: AppConfig{ActiveLang: ""},
		Languages: map[string]*LanguageConfig{
			"en": {
				Dict: struct {
					Provider string `yaml:"provider"`
				}{Provider: "nonexistent"},
			},
		},
	}
	errs := c.Validate()
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d", len(errs))
	}
}

func TestValidateInvalidQuestionType(t *testing.T) {
	c := &Config{
		App: AppConfig{ActiveLang: ""},
		Languages: map[string]*LanguageConfig{
			"en": {
				QuestionTypes: []string{"choice", "invalid_type"},
			},
		},
	}
	errs := c.Validate()
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d: %v", len(errs), errs)
	}
}

func TestValidateEmptyConfig(t *testing.T) {
	c := &Config{}
	if errs := c.Validate(); len(errs) != 0 {
		t.Errorf("empty config should have no errors, got %d", len(errs))
	}
}

func TestValidateValidTTsConfig(t *testing.T) {
	c := &Config{
		App: AppConfig{ActiveLang: ""},
		TTS: TTSConfig{
			TTS: struct {
				CacheLimit    int                    `yaml:"cache_limit"`
				FallbackOrder []string               `yaml:"fallback_order"`
				Providers     map[string]TTSProvider `yaml:"providers"`
			}{
				Providers: map[string]TTSProvider{
					"edge-tts": {
						Voices: map[string]string{
							"ja": "ja-JP-NanamiNeural",
						},
					},
				},
			},
		},
		Languages: map[string]*LanguageConfig{
			"ja": {
				TTS: struct {
					Provider string `yaml:"provider"`
					Voice    string `yaml:"voice"`
				}{Provider: "edge-tts", Voice: ""},
				QuestionTypes: []string{"choice"},
			},
		},
	}
	errs := c.Validate()
	if len(errs) != 0 {
		t.Errorf("expected no errors, got %d: %v", len(errs), errs)
	}
}
