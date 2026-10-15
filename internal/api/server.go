package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tzhnahida/VocabLock/internal/log"

	"github.com/tzhnahida/VocabLock/internal/ai"
	"github.com/tzhnahida/VocabLock/internal/clock"
	"github.com/tzhnahida/VocabLock/internal/config"
	"github.com/tzhnahida/VocabLock/internal/db"
	"github.com/tzhnahida/VocabLock/internal/dict"
	"github.com/tzhnahida/VocabLock/internal/engine"
	"github.com/tzhnahida/VocabLock/internal/pronounce"
	"github.com/tzhnahida/VocabLock/internal/system"
	"github.com/tzhnahida/VocabLock/internal/tts"
)

type cachedQuestion struct {
	q         *engine.Question
	createdAt time.Time
}

var slowTypes = map[string]bool{
	"listening":    true,
	"reading_comp": true,
	"grammar":      true,
}

type Server struct {
	http      *http.Server
	Port      int
	Token     string
	Mock      bool
	Config    *config.Config
	configMu  sync.RWMutex // guards Config field writes during concurrent bootstrap reads
	ConfigDir string       // path to configs/ directory
	DB        *db.Database

	// Optional — nil means not configured, handlers fall back gracefully
	aiValue           atomic.Value // *ai.Client, safe for concurrent access
	TTSProvider       *tts.Provider
	DictProvider      *dict.Provider
	PronounceProvider *pronounce.Provider

	// Question cache for AI-generated questions (maps question_id → cachedQuestion)
	questionCache   map[string]*cachedQuestion
	questionCacheMu sync.Mutex

	// Prevent double-serving: words served in the last 5 seconds are skipped
	servedWordIDs   map[int64]time.Time
	servedWordIDsMu sync.Mutex

	// Blocks quiz until initial bootstrap is complete
	bootstrapDone   bool
	bootstrapDoneMu sync.RWMutex

	// Prevents concurrent AI word expansion
	expanding   bool
	expandingMu sync.Mutex

	// Deduplicates concurrent TTS generation requests.
	// Key: cacheKey (MD5 of provider:voice:text), Value: chan struct{} closed when done.
	ttsInFlight   map[string]chan struct{}
	ttsInFlightMu sync.Mutex

	// Session budget tracks time/type usage for the current day
	sessionBudget sessionBudget

	// System-level features (lock screen, tray, passive quiz)
	System system.SystemProvider

	// Escape tracking (daily limits)
	escapeMu           sync.Mutex
	escapeDate         string
	escapePasswordUsed int
	escapeClickUsed    int

	// Restart support — set by main after Flutter is spawned
	FlutterPID int
	// RestartFunc re-wires providers in place (config reload + TTS/Dict/
	// Pronounce re-init). It does NOT kill/restart the Flutter process —
	// the comment on the old code claimed otherwise.
	RestartFunc func()

	// Session time accounting (stats only — NOT an unlock condition; the
	// daily target is count-based, see DailyTargetMet). Elapsed time
	// between question interactions accumulates into daily_stats.
	timeMu         sync.Mutex
	lastActivityAt time.Time
}

// sessionGapCapSec bounds a single inter-question gap when accumulating
// study time — anything longer is treated as idle, not study.
const sessionGapCapSec = 15 * 60

// markActivity records the moment a question-interaction happens. Called
// when a session goes READY so the study/learning phase counts as time.
// Stats only; never gates unlock.
func (s *Server) markActivity() {
	s.timeMu.Lock()
	defer s.timeMu.Unlock()
	s.lastActivityAt = time.Now()
}

// consumeActivityElapsed returns the seconds since the last activity,
// clamped to sessionGapCapSec, and resets the clock. Returns 0 if the
// clock was never started (e.g. submit without session/start).
func (s *Server) consumeActivityElapsed() int {
	s.timeMu.Lock()
	defer s.timeMu.Unlock()
	if s.lastActivityAt.IsZero() {
		s.lastActivityAt = time.Now()
		return 0
	}
	elapsed := int(time.Since(s.lastActivityAt).Seconds())
	s.lastActivityAt = time.Now()
	if elapsed > sessionGapCapSec {
		elapsed = sessionGapCapSec
	}
	if elapsed < 0 {
		elapsed = 0
	}
	return elapsed
}

// aiExplLang returns the language the AI should use when speaking to the
// student (explanations, chat, judging feedback, assignments, summaries).
//
// Priority: user interface language (InterfaceLang) > per-language
// explanation-language config > "en".
// FIX: the UI language was never propagated into the AI prompt layer —
// users with an English UI studying Japanese got AI replies in the
// language config's explanation language (e.g. Chinese) or the hardcoded
// zh-CN default in chat.
func (s *Server) aiExplLang(lang string) string {
	if ui := strings.TrimSpace(s.Config.App.InterfaceLang); ui != "" {
		return ui
	}
	if lc, ok := s.Config.Languages[lang]; ok && lc.PromptContext.ExplanationLang != "" {
		return lc.PromptContext.ExplanationLang
	}
	return "en"
}

// DailyTargetMet reports whether today's learning target is satisfied.
//
// Semantics (design intent, 2026-09-02): the daily target is COUNT-based.
// "Time" is only a UX conversion (e.g. 30 min ≈ 15 questions) — there is
// NO wall-clock unlock. An earlier wiring made accumulated study time a
// second unlock path (idle 20 min → unlock); that was removed — it was
// not in the original design and let "studying" be faked by idling.
//
// Accuracy gate: once the student has answered anything, the target
// additionally requires accuracy >= MinAccuracy (default 0.8) — this is
// what the onboarding copy promises ("Accuracy >= 80% required to unlock")
// and what the old code never enforced.
//
// Exported for use by cmd/server (main.go startup lock check).
func (s *Server) DailyTargetMet(lang string) bool {
	if s.DB == nil {
		return false
	}
	reviewed, err := s.DB.GetUniqueSubjectsReviewed(lang)
	if err != nil {
		return false
	}
	if reviewed < s.Config.App.DailyTarget {
		return false
	}
	// No answers yet → nothing to gate on.
	if reviewed == 0 {
		return true
	}
	_, correct, err := s.DB.GetTodayStats(lang)
	if err != nil {
		return false
	}
	accuracy := float64(correct) / float64(reviewed)
	return accuracy >= s.Config.App.MinAccuracy
}

// langHasGrammarType reports whether the language's question types include
// grammar (AI grammar questions & auto grammar expansion are only meaningful
// when the student opted into them — sim §九 ⑦).
func langHasGrammarType(lang string, cfg *config.Config) bool {
	lc, ok := cfg.Languages[lang]
	if !ok {
		return false
	}
	for _, qt := range lc.QuestionTypes {
		if qt == "grammar" {
			return true
		}
	}
	return false
}

func (s *Server) tryStartExpanding() bool {
	s.expandingMu.Lock()
	defer s.expandingMu.Unlock()
	if s.expanding {
		return false
	}
	s.expanding = true
	return true
}

// AI returns the current AI client (nil if not configured).
// Safe for concurrent access from any goroutine.
func (s *Server) AI() *ai.Client {
	v := s.aiValue.Load()
	if v == nil {
		return nil
	}
	return v.(*ai.Client)
}

// WireAI initializes (or re-initializes) the AI client from current config.
// Safe to call at startup and at runtime after config changes.
func (s *Server) WireAI() {
	if s.Config.App.AI.APIKey != "" && !s.Mock {
		client := ai.NewClient(ai.Config{
			BaseURL: s.Config.App.AI.BaseURL,
			APIKey:  s.Config.App.AI.APIKey,
			Model:   s.Config.App.AI.Model,
			Timeout: 120 * time.Second,
		})
		s.aiValue.Store(client)
		log.Info("server", "ai: configured")
	} else {
		s.aiValue.Store((*ai.Client)(nil))
		log.Debug("server", "ai: disabled")
	}
}

// WireTTS initializes (or re-initializes) the TTS provider from the active language config.
// Safe to call at startup and after active_lang changes.
func (s *Server) WireTTS() {
	s.TTSProvider = nil
	lang := s.Config.App.ActiveLang
	lc, ok := s.Config.Languages[lang]
	if !ok || lc.TTS.Provider == "" || s.Mock {
		return
	}
	provCfg, ok := s.Config.TTS.TTS.Providers[lc.TTS.Provider]
	if !ok {
		return
	}
	provider, err := tts.NewProvider(lc.TTS.Provider, provCfg, "data/audio")
	if err != nil {
		log.Warn("tts", "wire: %v", err)
		return
	}
	s.TTSProvider = provider
	log.Info("server", "tts: %s (%s)", lc.TTS.Provider, lc.TTS.Voice)
}

// WireDict initializes (or re-initializes) the dictionary provider from the active language config.
// Safe to call at startup and after active_lang changes.
func (s *Server) WireDict() {
	s.DictProvider = nil
	lang := s.Config.App.ActiveLang
	lc, ok := s.Config.Languages[lang]
	if !ok || lc.Dict.Provider == "" || s.Mock {
		return
	}
	provCfg, ok := s.Config.Dict.Dict.Providers[lc.Dict.Provider]
	if !ok {
		return
	}
	s.DictProvider = dict.NewProvider(lc.Dict.Provider, provCfg)
	log.Info("server", "dict: %s", lc.Dict.Provider)
}

// WirePronounce initializes (or re-initializes) the pronounce provider from the active language config.
// Safe to call at startup and after active_lang changes.
func (s *Server) WirePronounce() {
	s.PronounceProvider = nil
	lang := s.Config.App.ActiveLang
	lc, ok := s.Config.Languages[lang]
	if !ok || lc.Pronounce.Provider == "" || s.Mock {
		return
	}
	provCfg, ok := s.Config.Pronounce.Pronounce.Providers[lc.Pronounce.Provider]
	if !ok {
		return
	}
	s.PronounceProvider = pronounce.NewProvider(lc.Pronounce.Provider, provCfg)
	log.Info("server", "pronounce: %s", lc.Pronounce.Provider)
}

func (s *Server) stopExpanding() {
	s.expandingMu.Lock()
	s.expanding = false
	s.expandingMu.Unlock()
}

func (s *Server) isExpanding() bool {
	s.expandingMu.Lock()
	defer s.expandingMu.Unlock()
	return s.expanding
}

func NewServer(mock bool, cfg *config.Config, database *db.Database) *Server {
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		panic(fmt.Sprintf("crypto/rand.Read failed: %v", err))
	}

	s := &Server{
		Token:         hex.EncodeToString(token),
		Mock:          mock,
		Config:        cfg,
		DB:            database,
		questionCache: make(map[string]*cachedQuestion),
		servedWordIDs: make(map[int64]time.Time),
		ttsInFlight:   make(map[string]chan struct{}),
	}
	s.startCacheCleanup()
	return s
}

func (s *Server) Start() error {
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("bind: %w", err)
	}

	s.Port = listener.Addr().(*net.TCPAddr).Port
	s.http = &http.Server{
		Handler:      withMiddleware(mux, s.Token),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 1200 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		if err := s.http.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Error("server", "http serve: %v", err)
		}
	}()

	return nil
}

func (s *Server) Shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.http.Shutdown(ctx)
}

// sessionBudget tracks time and type usage during a single day session.
type sessionBudget struct {
	mu             sync.Mutex
	date           string
	timeUsed       int
	slowCount      int
	totalCount     int
	typeWeights    map[string]int // cached from training plan's question_type_ratio
	grammarFocus   []string       // cached from training plan's grammar_focus
	sessionDone    bool           // true once quiz/complete was called today
	sessionStarted bool           // true once first READY was returned today
	forceContinue  bool           // student opted in past the soft cap (see hitSoftCap)
}

func (b *sessionBudget) reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.date = ""
	b.timeUsed = 0
	b.slowCount = 0
	b.totalCount = 0
	b.sessionDone = false
	b.sessionStarted = false
	b.forceContinue = false
}

// rolloverLocked resets the day-accumulated counters when the calendar day
// changed. Callers must hold b.mu. (hitSoftCap previously lacked this check —
// yesterday's totalCount kept the soft cap permanently triggered, see sim §九.)
func (b *sessionBudget) rolloverLocked() {
	today := clock.Today()
	if b.date != today {
		b.date = today
		b.timeUsed = 0
		b.slowCount = 0
		b.totalCount = 0
		b.forceContinue = false
	}
}

// hitSoftCap reports whether today's session exceeded the soft cap (1.5×
// daily target, min +10) without meeting the target. The day does not end
// silently — the student chooses: continue training (POST /quiz/session/
// continue) or stop (the completion screen shows the remaining gap).
// Without this, a sub-80%-accuracy student was served an unbounded number
// of questions every day (simulation S2: 100+/day).
func (b *sessionBudget) hitSoftCap(dailyTarget int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rolloverLocked()
	cap := dailyTarget + dailyTarget/2
	if cap < dailyTarget+10 {
		cap = dailyTarget + 10
	}
	return b.totalCount >= cap
}

func (b *sessionBudget) setForceContinue(v bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.forceContinue = v
}

func (b *sessionBudget) isForceContinue() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.forceContinue
}

func (b *sessionBudget) markSessionDone() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sessionDone = true
	b.date = clock.Today()
}

func (b *sessionBudget) isSessionDone() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	today := clock.Today()
	if b.date != today {
		b.sessionDone = false
	}
	return b.sessionDone
}

func (b *sessionBudget) markSessionStarted() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sessionStarted = true
	b.date = clock.Today()
}

func (b *sessionBudget) isSessionStarted() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	today := clock.Today()
	if b.date != today {
		b.sessionStarted = false
	}
	return b.sessionStarted
}

func (b *sessionBudget) setTypeWeights(weights map[string]int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.typeWeights = weights
}

func (b *sessionBudget) getTypeWeights() map[string]int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.typeWeights
}

func (b *sessionBudget) setGrammarFocus(focus []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.grammarFocus = focus
}

func (b *sessionBudget) getGrammarFocus() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.grammarFocus
}

func (b *sessionBudget) allowType(typeName string, timeBudgetMinutes int, maxSlowRatio float64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.rolloverLocked()

	weight := engine.TimeWeights[typeName]
	if weight == 0 {
		weight = 1
	}

	// 0 = unlimited time budget
	if timeBudgetMinutes > 0 {
		maxUnits := timeBudgetMinutes * 6
		if b.timeUsed+weight > maxUnits {
			return false
		}
	}

	// 0 = no slow-type ratio limit
	if maxSlowRatio > 0 && engine.IsSlowType(typeName) {
		maxSlow := int(float64(b.totalCount+1) * maxSlowRatio)
		if b.slowCount >= maxSlow {
			return false
		}
	}

	return true
}

func (b *sessionBudget) recordType(typeName string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.rolloverLocked()

	weight := engine.TimeWeights[typeName]
	if weight == 0 {
		weight = 1
	}
	b.timeUsed += weight
	b.totalCount++
	if engine.IsSlowType(typeName) {
		b.slowCount++
	}
}

// mergeTypeWeights caps slow-type weights to maxSlowRatio, redistributing excess to fast types.
func mergeTypeWeights(aiWeights map[string]int, maxSlowRatio float64) map[string]int {
	if len(aiWeights) == 0 || maxSlowRatio <= 0 {
		return aiWeights
	}
	total := 0
	slowTotal := 0
	fastTotal := 0
	for t, w := range aiWeights {
		total += w
		if slowTypes[t] {
			slowTotal += w
		} else {
			fastTotal += w
		}
	}
	if total == 0 {
		return aiWeights
	}
	slowPct := float64(slowTotal) / float64(total)
	if slowPct <= maxSlowRatio {
		return aiWeights // already within cap
	}
	// Cap slow types and redistribute excess to fast types
	maxSlow := int(float64(total) * maxSlowRatio)
	result := make(map[string]int, len(aiWeights))
	for t, w := range aiWeights {
		if slowTypes[t] {
			scaled := int(float64(w) * float64(maxSlow) / float64(slowTotal))
			if scaled < 1 && w > 0 {
				scaled = 1 // keep at least 1 for any slow type that has weight
			}
			result[t] = scaled
		} else {
			result[t] = w
		}
	}
	// Redistribute excess proportionally to fast types
	if fastTotal > 0 {
		excess := slowTotal - maxSlow
		fastWeightTotal := 0
		for t, w := range aiWeights {
			if !slowTypes[t] {
				fastWeightTotal += w
			}
		}
		if fastWeightTotal > 0 {
			for t, w := range result {
				if !slowTypes[t] {
					result[t] = w + excess*w/fastWeightTotal
				}
			}
		}
	}
	return result
}

func (s *Server) cacheQuestion(q *engine.Question) {
	s.questionCacheMu.Lock()
	defer s.questionCacheMu.Unlock()
	if len(s.questionCache) >= 2000 {
		// Evict one random entry when over limit, instead of discarding all.
		// Discarding all would lose expected answers for pending AI questions.
		for k := range s.questionCache {
			delete(s.questionCache, k)
			break
		}
	}
	s.questionCache[q.ID] = &cachedQuestion{q: q, createdAt: time.Now()}
}

func (s *Server) getAndDelCachedQuestion(id string) *engine.Question {
	s.questionCacheMu.Lock()
	defer s.questionCacheMu.Unlock()
	cq, ok := s.questionCache[id]
	if !ok {
		return nil
	}
	// Expire after 15 days
	if time.Since(cq.createdAt) > 15*24*time.Hour {
		delete(s.questionCache, id)
		return nil
	}
	delete(s.questionCache, id)
	return cq.q
}

func (s *Server) startCacheCleanup() {
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			s.questionCacheMu.Lock()
			now := time.Now()
			for id, cq := range s.questionCache {
				if now.Sub(cq.createdAt) > 15*24*time.Hour {
					delete(s.questionCache, id)
				}
			}
			s.questionCacheMu.Unlock()
		}
	}()
}

// markWordServed records a word as recently served to prevent double-serving.
// Uses the injectable clock so the "recent" window is defined in application
// time (simulation/time-travel friendly), not raw wall-clock.
func (s *Server) markWordServed(wordID int64) {
	s.servedWordIDsMu.Lock()
	s.servedWordIDs[wordID] = clock.Now()
	// Clean expired entries (older than 10 seconds)
	for id, t := range s.servedWordIDs {
		if clock.Now().Sub(t) > 10*time.Second {
			delete(s.servedWordIDs, id)
		}
	}
	s.servedWordIDsMu.Unlock()
}

// wasRecentlyServed returns true if the word was served within the last 5 seconds.
func (s *Server) wasRecentlyServed(wordID int64) bool {
	s.servedWordIDsMu.Lock()
	defer s.servedWordIDsMu.Unlock()
	t, ok := s.servedWordIDs[wordID]
	if !ok {
		return false
	}
	if clock.Now().Sub(t) > 5*time.Second {
		delete(s.servedWordIDs, wordID)
		return false
	}
	return true
}

func (s *Server) markBootstrapDone() {
	s.bootstrapDoneMu.Lock()
	s.bootstrapDone = true
	s.bootstrapDoneMu.Unlock()
}

func (s *Server) markBootstrapNotDone() {
	s.bootstrapDoneMu.Lock()
	s.bootstrapDone = false
	s.bootstrapDoneMu.Unlock()
}

func (s *Server) isBootstrapDone() bool {
	s.bootstrapDoneMu.RLock()
	defer s.bootstrapDoneMu.RUnlock()
	return s.bootstrapDone
}

// HasPronounce returns true if a pronunciation assessment provider is configured.
func (s *Server) HasPronounce() bool {
	return s.PronounceProvider != nil
}
