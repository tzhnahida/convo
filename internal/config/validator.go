package config

import "fmt"

var validQuestionTypes = map[string]bool{
	"choice": true, "fill": true, "spelling": true, "pronounce": true,
	"meaning": true, "listening": true, "reading_comp": true, "grammar": true,
	"read_word": true, "read_sentence": true,
}

func (c *Config) Validate() []error {
	var errs []error

	// 1. Active language must exist (skip if no languages configured — first run)
	if c.App.ActiveLang != "" && len(c.Languages) > 0 {
		if _, ok := c.Languages[c.App.ActiveLang]; !ok {
			errs = append(errs, fmt.Errorf("active_lang %q not defined in languages/", c.App.ActiveLang))
		}
	}

	// 2. Validate each language
	for code, lc := range c.Languages {
		// tts.provider must exist in tts.yaml
		if lc.TTS.Provider != "" {
			if _, ok := c.TTS.TTS.Providers[lc.TTS.Provider]; !ok {
				errs = append(errs, fmt.Errorf("lang %s: tts.provider %q not found in tts.yaml", code, lc.TTS.Provider))
			} else {
				// tts.voice must exist for that provider
				prov := c.TTS.TTS.Providers[lc.TTS.Provider]
				if _, ok := prov.Voices[code]; !ok {
					errs = append(errs, fmt.Errorf("lang %s: no voice defined for provider %q", code, lc.TTS.Provider))
				}
			}
		}

		// dict.provider must exist in dict.yaml (or be empty)
		if lc.Dict.Provider != "" {
			if _, ok := c.Dict.Dict.Providers[lc.Dict.Provider]; !ok {
				errs = append(errs, fmt.Errorf("lang %s: dict.provider %q not found in dict.yaml", code, lc.Dict.Provider))
			}
		}

		// question_types must be valid
		for _, qt := range lc.QuestionTypes {
			if !validQuestionTypes[qt] {
				errs = append(errs, fmt.Errorf("lang %s: unknown question_type %q", code, qt))
			}
		}
	}

	return errs
}
