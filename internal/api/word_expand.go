package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/tzhnahida/VocabLock/internal/ai"
	"github.com/tzhnahida/VocabLock/internal/clock"
	"github.com/tzhnahida/VocabLock/internal/db"
	"github.com/tzhnahida/VocabLock/internal/dict"
	"github.com/tzhnahida/VocabLock/internal/log"
)

const maxExpandRounds = 3

type expandWordEntry struct {
	Word    string `json:"word"`
	Example string `json:"example"`
}

type expandResponse struct {
	Status string            `json:"status"`
	Reason string            `json:"reason"`
	Words  []json.RawMessage `json:"words"`
}

// parseExpandWords normalizes the AI response's "words" field.
// Supports both legacy format (string array) and new format (object array with example).
func parseExpandWords(raw []json.RawMessage) []expandWordEntry {
	var entries []expandWordEntry
	for _, r := range raw {
		// Try new format first: {"word": "...", "example": "..."}
		var e expandWordEntry
		if json.Unmarshal(r, &e) == nil && e.Word != "" {
			entries = append(entries, e)
			continue
		}
		// Fallback: plain string
		var s string
		if json.Unmarshal(r, &s) == nil && s != "" {
			entries = append(entries, expandWordEntry{Word: s})
		}
	}
	return entries
}

// expandWords generates new words via AI, with dedup feedback loop and dict verification.
// Returns (verified, pending, error).
func (s *Server) expandWords(ctx context.Context, lang string, count int, extraContext string) (verified, pending int, err error) {
	log.Debug("expand", "start: lang=%s count=%d extra=%s", lang, count, extraContext)
	if s.AI() == nil {
		return 0, 0, fmt.Errorf("AI not configured")
	}
	if count <= 0 {
		count = 5
	}
	if count > 50 {
		count = 50
	}

	// Collect existing words for dedup context
	existingWords, totalCount := s.collectExistingWords(lang)

	// Build language context once
	teacherRole, level, explLang, targetLang := s.expandLangContext(lang)

	// Build extraContext from recent words if not provided
	if extraContext == "" {
		extraContext = s.buildRecentContext(lang)
	}

	// Expansion audit: record decision inputs so "why these words" is
	// traceable (plan direction, dedup pool size, recent-word context).
	planDir := ""
	if p, err := s.DB.GetActivePlan(lang); err == nil {
		planDir = p.Direction
	}
	log.Debug("expand", "audit: lang=%s count=%d dir=%q dedup=%d words recent=%d chars temp=0.5",
		lang, count, planDir, totalCount, len(extraContext))

	// Retry pending-review words before expanding new ones
	if s.DB != nil {
		pendingWords, _ := s.DB.ListPendingReview(lang)
		if len(pendingWords) > 10 {
			pendingWords = pendingWords[:10]
		}
		for _, pw := range pendingWords {
			// Try dictionary lookup again
			dictEntry := s.lookupDict(ctx, pw.Word)
			if dictEntry != nil {
				aiDetail := s.generateDetail(ctx, pw.Word, dictEntry.Reading, dictEntry.Meaning, dictEntry.POS, dictEntry.Level, lang)
				detail := mergeJSON(dictEntry.Detail, aiDetail)
				pw.Reading = dictEntry.Reading
				if pw.Reading == "" {
					pw.Reading = extractFieldFromDetail(aiDetail, "reading")
				}
				pw.Meaning = dictEntry.Meaning
				if pw.Meaning == "" {
					pw.Meaning = extractFieldFromDetail(aiDetail, "meaning")
				}
				pw.POS = dictEntry.POS
				if pw.POS == "" {
					pw.POS = extractFieldFromDetail(aiDetail, "pos")
				}
				pw.Level = dictEntry.Level
				if pw.Level == "" {
					pw.Level = extractFieldFromDetail(aiDetail, "level")
				}
				pw.Detail = detail
				pw.Status = "active"
				pw.Source = "ai_expansion_dict"
				pw.Confidence = "high"
				s.DB.UpdateWord(pw)
				verified++
				s.preWarmTTS(pw)
				continue
			}
			// Dict still miss — stay pending for user review
		}
	}

	duplicates := ""
	for round := 1; round <= maxExpandRounds; round++ {
		pt, err := ai.LoadPrompt("expand")
		if err != nil {
			return 0, 0, fmt.Errorf("load prompt: %w", err)
		}

		messages, err := pt.BuildMessages(&ai.PromptVars{
			TeacherRole:     teacherRole,
			Language:        targetLang,
			Level:           level,
			ExplanationLang: explLang,
			ExistingWords:   existingWords,
			WordCount:       totalCount,
			ExpandCount:     count,
			ExtraContext:    extraContext,
			DuplicateWords:  duplicates,
		})
		if err != nil {
			return 0, 0, fmt.Errorf("build prompt: %w", err)
		}

		timeoutCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		response, aiErr := s.AI().Chat(timeoutCtx, messages, pt.Temperature)
		cancel()
		if aiErr != nil {
			return 0, 0, fmt.Errorf("AI chat: %w", aiErr)
		}

		cleaned := extractJSON(response)
		var result expandResponse
		if err := json.Unmarshal([]byte(cleaned), &result); err != nil {
			return 0, 0, fmt.Errorf("parse AI response: %w", err)
		}

		// AI says vocabulary is complete
		if result.Status == "complete" {
			if s.DB != nil {
				s.DB.CompletePlan(lang)
			}
			return 0, 0, nil
		}

		// Parse words (supports both legacy string array and new object array)
		entries := parseExpandWords(result.Words)

		// Check each word for duplicates
		var dupList []string
		for _, e := range entries {
			if e.Word == "" {
				continue
			}
			exists, _ := s.DB.WordExists(lang, e.Word)
			if exists {
				dupList = append(dupList, e.Word)
			}
		}

		// All words are duplicates — feedback to AI and retry
		if len(dupList) > 0 && len(dupList) == len(entries) {
			duplicates = strings.Join(dupList, ", ")
			continue
		}

		// Process non-duplicate words
		dupSet := make(map[string]bool, len(dupList))
		for _, d := range dupList {
			dupSet[d] = true
		}

		for _, e := range entries {
			if e.Word == "" || dupSet[e.Word] {
				continue
			}

			// Dictionary provides reading, meaning, POS — not AI
			dictEntry := s.lookupDict(ctx, e.Word)
			if dictEntry != nil {
				// Word confirmed by dictionary. Dict provides what it has;
				// AI fills any gaps in reading/meaning/POS/level + DLC fields.
				aiDetail := s.generateDetail(ctx, e.Word, dictEntry.Reading, dictEntry.Meaning, dictEntry.POS, dictEntry.Level, lang)

				reading := dictEntry.Reading
				if reading == "" {
					reading = extractFieldFromDetail(aiDetail, "reading")
				}
				meaning := dictEntry.Meaning
				if meaning == "" {
					meaning = extractFieldFromDetail(aiDetail, "meaning")
				}
				pos := dictEntry.POS
				if pos == "" {
					pos = extractFieldFromDetail(aiDetail, "pos")
				}
				lvl := dictEntry.Level
				if lvl == "" {
					lvl = extractFieldFromDetail(aiDetail, "level")
				}

				// Dict detail base + AI gap-fill (dict keys take priority)
				detail := mergeJSON(dictEntry.Detail, aiDetail)

				// Inject AI-provided example sentence into detail
				if e.Example != "" {
					detail = injectExample(detail, e.Example)
				}

				newWord := &db.Word{
					Word:       e.Word,
					Reading:    reading,
					Meaning:    meaning,
					POS:        pos,
					Level:      lvl,
					Detail:     detail,
					Language:   lang,
					Source:     "ai_expansion_dict",
					Confidence: "high",
				}
				_, insertErr := s.DB.InsertWord(newWord)
				if insertErr == nil {
					verified++
					s.preWarmTTS(newWord)
				}
			} else {
				// Dictionary miss — might be AI-fabricated. Quarantine for user review.
				detail := "{}"
				if e.Example != "" {
					detail = `{"example":"` + jsonEscape(e.Example) + `"}`
				}
				s.DB.InsertWord(&db.Word{
					Word: e.Word, Language: lang, Source: "ai_expansion",
					Status: "pending_review", Detail: detail,
				})
				pending++
			}
		}
		log.Debug("expand", "done: %d verified + %d pending", verified, pending)
		return verified, pending, nil
	}

	// All 3 rounds produced only duplicates — mark plan complete
	if s.DB != nil {
		s.DB.CompletePlan(lang)
	}
	return 0, 0, nil
}

// ——— helpers ———

func (s *Server) collectExistingWords(lang string) (existingWords string, total int) {
	words, total, err := s.DB.ListWords(lang, 100, 0)
	if err != nil {
		words, total, _ = s.DB.ListWords(lang, 5, 0)
	}
	parts := make([]string, 0, len(words))
	for _, w := range words {
		if w.Word != "" {
			parts = append(parts, w.Word)
		}
	}
	if len(parts) == 0 {
		return "(no words yet)", 0
	}
	return strings.Join(parts, ", "), total
}

func (s *Server) buildRecentContext(lang string) string {
	recentWords, _ := s.DB.GetReviewWords(lang, 5)
	parts := make([]string, 0, len(recentWords))
	for _, w := range recentWords {
		parts = append(parts, w.Word)
	}
	if len(parts) == 0 {
		return "(new learner, no words yet)"
	}
	return strings.Join(parts, ", ")
}

func (s *Server) expandLangContext(lang string) (teacherRole, level, explLang, targetLang string) {
	teacherRole = "Language Teacher"
	level = s.getStudentLevel(lang)
	explLang = "zh-CN"
	targetLang = lang
	if lc, ok := s.Config.Languages[lang]; ok {
		teacherRole = lc.PromptContext.TeacherRole
		explLang = lc.PromptContext.ExplanationLang
		targetLang = lc.Lang.DisplayName
	}
	return
}

// lookupDict returns the dictionary entry for a word, or nil if not found.
func (s *Server) lookupDict(ctx context.Context, word string) *dict.LookupResult {
	if s.DictProvider == nil {
		return nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := s.DictProvider.Lookup(lookupCtx, word)
	if err != nil || result == nil || !result.Found {
		return nil
	}
	return result
}

// generateDetail fills missing extension fields via AI.
// Takes dict-provided data and returns a JSON string with additional linguistic detail.
func (s *Server) generateDetail(ctx context.Context, word, reading, meaning, pos, level, lang string) string {
	if s.AI() == nil {
		return ""
	}

	pt, err := ai.LoadPrompt("detail")
	if err != nil {
		return ""
	}

	teacherRole, _, explLang, _ := s.expandLangContext(lang)

	messages, err := pt.BuildMessages(&ai.PromptVars{
		Word:            word,
		Reading:         reading,
		Meaning:         meaning,
		POS:             pos,
		Level:           level,
		Language:        lang,
		TeacherRole:     teacherRole,
		ExplanationLang: explLang,
	})
	if err != nil {
		return ""
	}

	aiCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	response, err := s.AI().Chat(aiCtx, messages, pt.Temperature)
	if err != nil {
		return ""
	}

	// Extract JSON from response (may be wrapped in markdown)
	cleaned := extractJSON(response)
	if cleaned == "" {
		return ""
	}
	return cleaned
}

// extractFieldFromDetail extracts a single string field from a detail JSON blob.
func extractFieldFromDetail(detail, key string) string {
	if detail == "" || detail == "{}" {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(detail), &m); err != nil {
		return ""
	}
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// mergeJSON merges two JSON objects. a (dict) is authoritative, b (AI) fills only missing keys.
func mergeJSON(a, b string) string {
	if a == "" || a == "{}" {
		if b == "" {
			return "{}"
		}
		return b
	}
	if b == "" || b == "{}" {
		return a
	}

	var ma, mb map[string]interface{}
	if err := json.Unmarshal([]byte(a), &ma); err != nil {
		return b
	}
	if err := json.Unmarshal([]byte(b), &mb); err != nil {
		return a
	}
	// Dict (a) is authoritative — AI (b) only fills keys dict didn't provide
	for k, v := range mb {
		if _, exists := ma[k]; !exists {
			ma[k] = v
		}
	}
	result, err := json.Marshal(ma)
	if err != nil {
		return a
	}
	return string(result)
}

// expandGrammar generates new grammar points via AI, following the same pattern as expandWords.
// unlockNextSense checks the word's detail.glosses for an unused dissimilar meaning
// and creates a new SM-2 entry for it. Called when a word reaches long-term memory.
func (s *Server) unlockNextSense(word *db.Word) {
	if word.Detail == "" || word.Detail == "{}" || s.DB == nil {
		return
	}
	var detail struct {
		Glosses []string `json:"glosses"`
	}
	if err := json.Unmarshal([]byte(word.Detail), &detail); err != nil || len(detail.Glosses) <= 1 {
		return
	}
	// Find first gloss that is dissimilar to current meaning (common chars < 30%)
	current := word.Meaning
	for _, g := range detail.Glosses {
		if g == current {
			continue
		}
		if !dissimilarMeaning(current, g) {
			continue
		}
		// Create new SM-2 entry for this sense
		newWord := &db.Word{
			Word: word.Word, Reading: word.Reading, Meaning: g,
			POS: word.POS, Level: word.Level, Detail: "{}",
			Language: word.Language, Source: "sense_expansion", Confidence: "high",
		}
		if _, err := s.DB.InsertWord(newWord); err == nil {
			log.Debug("expand", "sense unlock: %s + %q (from %q)", word.Word, g, current)
		}
		return
	}
}

// dissimilarMeaning returns true when two Chinese glosses are semantically different.
// Heuristic: shared character ratio < 30% = dissimilar enough to learn separately.
func dissimilarMeaning(a, b string) bool {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 || len(rb) == 0 {
		return false
	}
	sa, sb := make(map[rune]bool), make(map[rune]bool)
	for _, r := range ra {
		sa[r] = true
	}
	for _, r := range rb {
		sb[r] = true
	}
	common := 0
	for r := range sa {
		if sb[r] {
			common++
		}
	}
	ratio := float64(common) / float64(max(len(sa), len(sb)))
	return ratio < 0.3
}

func (s *Server) expandGrammar(ctx context.Context, lang string, count int) (inserted int, err error) {
	log.Debug("expand", "grammar start: lang=%s count=%d", lang, count)
	if s.AI() == nil || s.DB == nil {
		return 0, fmt.Errorf("AI or DB not configured")
	}
	if count <= 0 || count > 5 {
		count = 2
	}

	// Get existing grammar titles for dedup
	existing, _, _ := s.DB.ListGrammarPoints(lang, 50, 0)
	existingTitles := make([]string, 0, len(existing))
	for _, g := range existing {
		if g.Title != "" {
			existingTitles = append(existingTitles, g.Title)
		}
	}

	// Build language context
	teacherRole, level, explLang, targetLang := s.expandLangContext(lang)
	_, extraCtx := s.planExpandParams(lang)

	// Build vocabulary summary (not full list — too large as vocab grows)
	vocabSummary := s.buildVocabSummary(lang)
	// Expansion audit: record decision inputs so "why these words" is
	pt, err := ai.LoadPrompt("expand_grammar")
	if err != nil {
		return 0, fmt.Errorf("load prompt: %w", err)
	}

	// Multi-turn: AI can request word lookups (up to 3 rounds)
	queryResult := ""
	var grammarPoints []string

	for round := 0; round < 3; round++ {
		messages, err := pt.BuildMessages(&ai.PromptVars{
			TeacherRole:     teacherRole,
			Language:        targetLang,
			Level:           level,
			ExplanationLang: explLang,
			VocabSummary:    vocabSummary,
			ExtraContext:    extraCtx,
			ExistingGrammar: strings.Join(existingTitles, ", "),
			QueryResult:     queryResult,
		})
		if err != nil {
			return 0, fmt.Errorf("build prompt: %w", err)
		}

		timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		response, aiErr := s.AI().Chat(timeoutCtx, messages, pt.Temperature)
		cancel()
		if aiErr != nil {
			return 0, fmt.Errorf("AI chat: %w", aiErr)
		}

		cleaned := extractJSON(response)

		// Check if AI wants to look up words
		var checkReq struct {
			CheckWords []string `json:"check_words"`
			Points     []string `json:"grammar_points"`
		}
		if err := json.Unmarshal([]byte(cleaned), &checkReq); err != nil {
			continue
		}

		// AI is asking to check words
		if len(checkReq.CheckWords) > 0 && len(checkReq.Points) == 0 {
			var results []string
			for _, word := range checkReq.CheckWords {
				exists, _ := s.DB.WordExists(lang, word)
				if !exists {
					// Try stripping common conjugation endings (Japanese)
					if base := tryBaseForm(word); base != "" {
						exists, _ = s.DB.WordExists(lang, base)
					}
				}
				if exists {
					results = append(results, word+"=yes")
				} else {
					results = append(results, word+"=no")
				}
			}
			queryResult = strings.Join(results, ", ")
			continue
		}

		// AI made a final decision
		if len(checkReq.Points) > 0 || round >= 2 {
			grammarPoints = checkReq.Points
			break
		}

		// Empty response with no check_words — try again
		queryResult = "(try again — suggest check_words or grammar_points)"
	}

	if len(grammarPoints) == 0 {
		return 0, nil
	}

	for _, title := range grammarPoints {
		if title == "" {
			continue
		}
		exists, _ := s.DB.GrammarPointExists(lang, title)
		if exists {
			continue
		}

		// Generate full detail for the grammar point
		gp := &db.GrammarPoint{
			Title:    title,
			Language: lang,
			Source:   "ai_expansion",
			Status:   "active",
		}

		// AI fills explanation, example, and all detail fields
		dt, _ := ai.LoadPrompt("grammar_detail")
		if dt != nil {
			dm, _ := dt.BuildMessages(&ai.PromptVars{
				GrammarTitle:    title,
				Language:        targetLang,
				TeacherRole:     teacherRole,
				ExplanationLang: explLang,
				Level:           level,
				VocabSummary:    vocabSummary,
			})
			aiCtx, c2 := context.WithTimeout(ctx, 30*time.Second)
			resp, err := s.AI().Chat(aiCtx, dm, dt.Temperature)
			c2()
			if err == nil {
				detailJSON := extractJSON(resp)
				if detailJSON != "" && detailJSON != "{}" {
					gp.Detail = detailJSON
					if v := extractFieldFromDetail(detailJSON, "explanation"); v != "" {
						gp.Explanation = v
					}
					if v := extractFieldFromDetail(detailJSON, "example"); v != "" {
						gp.Example = v
					}
				}
			}
		}

		if _, err := s.DB.InsertGrammarPoint(gp); err == nil {
			inserted++
		}
	}

	return inserted, nil
}

// tryBaseForm strips common Japanese conjugation endings to find the dictionary form.
func tryBaseForm(word string) string {
	suffixes := []string{
		"なければならない", "てしまう", "ている",
		"ました", "ません", "ます", "たい",
		"て", "た", "ない",
		"く", "ぐ", "に",
	}
	runes := []rune(word)
	for _, s := range suffixes {
		sr := []rune(s)
		if len(runes) > len(sr) && string(runes[len(runes)-len(sr):]) == s {
			return string(runes[:len(runes)-len(sr)])
		}
	}
	return ""
}

// buildVocabSummary returns a categorized summary of the student's vocabulary.
// Groups words by POS with exact counts from DB and representative samples (not full list).
func (s *Server) buildVocabSummary(lang string) string {
	// Get exact word count and POS distribution from DB
	exactTotal, posCounts, err := s.DB.WordPOSCounts(lang)
	if err != nil || exactTotal == 0 {
		// Fallback: check if any words exist at all
		if _, total, _ := s.DB.ListWords(lang, 1, 0); total == 0 {
			return "(no vocabulary yet)"
		}
	}

	// Categorize exact POS counts into groups
	categorizeCount := func() (n, v, a, e, o int) {
		for pos, count := range posCounts {
			switch {
			case strings.Contains(pos, "名") || strings.Contains(pos, "noun") || strings.Contains(pos, "名词"):
				n += count
			case strings.Contains(pos, "动") || strings.Contains(pos, "verb") || strings.Contains(pos, "自動") || strings.Contains(pos, "他動"):
				v += count
			case strings.Contains(pos, "形容") || strings.Contains(pos, "adj"):
				a += count
			case strings.Contains(pos, "惯用") || strings.Contains(pos, "熟語") || strings.Contains(pos, "感"):
				e += count
			default:
				o += count
			}
		}
		return
	}
	nCount, vCount, aCount, eCount, oCount := categorizeCount()
	total := exactTotal

	// Fetch up to 999 words as a sample pool for representative examples
	words, _, _ := s.DB.ListWords(lang, 999, 0)

	nouns := make([]string, 0)
	verbs := make([]string, 0)
	adjs := make([]string, 0)
	phrases := make([]string, 0)
	other := make([]string, 0)

	for _, w := range words {
		if w.Word == "" || w.Meaning == "" {
			continue
		}
		entry := w.Word + "(" + w.Meaning + ")"
		p := w.POS
		switch {
		case strings.Contains(p, "名") || strings.Contains(p, "noun") || strings.Contains(p, "名词"):
			nouns = append(nouns, entry)
		case strings.Contains(p, "动") || strings.Contains(p, "verb") || strings.Contains(p, "自動") || strings.Contains(p, "他動"):
			verbs = append(verbs, entry)
		case strings.Contains(p, "形容") || strings.Contains(p, "adj"):
			adjs = append(adjs, entry)
		case strings.Contains(p, "惯用") || strings.Contains(p, "熟語") || strings.Contains(p, "感"):
			phrases = append(phrases, entry)
		default:
			other = append(other, entry)
		}
	}

	// Daily-seeded sampling: same day → same sample (reproducible,
	// debuggable); next day → fresh sample (preserves the anti-repetition
	// intent of the old per-call shuffle). No fixed global seed.
	daySeed := clock.Now().Unix()/86400*31 + int64(len(lang))
	sr := rand.New(rand.NewSource(daySeed))
	sample := func(list []string, max int) string {
		if len(list) == 0 {
			return ""
		}
		sr.Shuffle(len(list), func(i, j int) { list[i], list[j] = list[j], list[i] })
		if len(list) > max {
			list = list[:max]
		}
		return strings.Join(list, ", ")
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Total: %d. ", total))
	if nCount > 0 {
		b.WriteString(fmt.Sprintf("Nouns: %d (e.g. %s). ", nCount, sample(nouns, 15)))
	}
	if vCount > 0 {
		b.WriteString(fmt.Sprintf("Verbs: %d (e.g. %s). ", vCount, sample(verbs, 10)))
	}
	if aCount > 0 {
		b.WriteString(fmt.Sprintf("Adjectives: %d (e.g. %s). ", aCount, sample(adjs, 8)))
	}
	if eCount > 0 {
		b.WriteString(fmt.Sprintf("Phrases: %d (e.g. %s). ", eCount, sample(phrases, 5)))
	}
	if oCount > 0 {
		b.WriteString(fmt.Sprintf("Other: %d (e.g. %s).", oCount, sample(other, 5)))
	}
	return b.String()
}

// injectExample adds an example sentence to a detail JSON object.
func injectExample(detail, example string) string {
	if example == "" {
		return detail
	}
	var m map[string]interface{}
	if detail != "" && detail != "{}" {
		if err := json.Unmarshal([]byte(detail), &m); err != nil {
			m = make(map[string]interface{})
		}
	} else {
		m = make(map[string]interface{})
	}
	m["example"] = example
	result, err := json.Marshal(m)
	if err != nil {
		return detail
	}
	return string(result)
}

// jsonEscape escapes a string for safe JSON embedding.
func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	// json.Marshal wraps in quotes; strip them
	if len(b) >= 2 {
		return string(b[1 : len(b)-1])
	}
	return s
}
