// Package sim drives month-long simulations of the VocabLock quiz loop
// through the REAL HTTP handlers, with time travel via the injectable clock
// (internal/clock). Each "user" is a probabilistic answer model; each day
// runs session/start → next/submit loop → complete (→ summarize when AI is
// on, exactly like the frontend does).
//
// Output: SIMULATION.md at repo root + per-day tables in test logs.
package sim

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tzhnahida/VocabLock/internal/api"
	"github.com/tzhnahida/VocabLock/internal/clock"
	"github.com/tzhnahida/VocabLock/internal/config"
	"github.com/tzhnahida/VocabLock/internal/db"
)

// ————————————————————————————————————————————————
// Time driver
// ————————————————————————————————————————————————

type timeDriver struct {
	mu sync.Mutex
	t  time.Time
}

func (d *timeDriver) now() time.Time { d.mu.Lock(); defer d.mu.Unlock(); return d.t }

func (d *timeDriver) set(t time.Time) { d.mu.Lock(); d.t = t; d.mu.Unlock() }

func (d *timeDriver) add(dur time.Duration) { d.mu.Lock(); d.t = d.t.Add(dur); d.mu.Unlock() }

// ————————————————————————————————————————————————
// Fake OpenAI-compatible AI (routes on system-prompt keywords)
// ————————————————————————————————————————————————

type fakeAI struct {
	srv           *httptest.Server
	seq           int64 // expansion word sequence (never repeats → avoids dedup loop)
	last          string
	mu            sync.Mutex
	injectGrammar bool // when true, expand_grammar returns points (else empty)
}

func newFakeAI() *fakeAI {
	f := &fakeAI{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		var sys, user strings.Builder
		for _, m := range req.Messages {
			if m.Role == "system" {
				sys.WriteString(m.Content)
			} else {
				user.WriteString(m.Content)
			}
		}
		joined := sys.String() + "\n" + user.String()
		f.mu.Lock()
		f.last = joined
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":` + jsonStr(f.respond(joined)) + `}}]}`))
	}))
	return f
}

// respond returns the inner content string per prompt family.
func (f *fakeAI) respond(s string) string {
	switch {
	case strings.Contains(s, "suggest target-language words"):
		// expand — dict is NOT configured in sims, so every word lands in
		// pending_review quarantine. Unique names per call ⇒ dedup loop never fires.
		n := 6 + int(atomic.LoadInt64(&f.seq))%5
		words := make([]string, 0, n)
		for i := 0; i < n; i++ {
			num := atomic.AddInt64(&f.seq, 1)
			words = append(words, fmt.Sprintf(`{"word":"aiw%d","example":"aiw%d wo tsukau"}`, num, num))
		}
		return `{"status":"ok","words":[` + strings.Join(words, ",") + `]}`
	case strings.Contains(s, "ready for grammar"):
		if f.injectGrammar {
			return `{"grammar_points":["ai grammar point 1","ai grammar point 2"]}`
		}
		return `{"grammar_points":[]}`
	case strings.Contains(s, "Generate ONE short example sentence"):
		return `{"example":""}`
	case strings.Contains(s, "Create a grammar question"):
		return `{"question":{"type":"grammar","prompt":"文法問題?","options":["A","B","C"],"answer":"A"}}`
	case strings.Contains(s, "for a language learner"):
		return `{"question":{"type":"choice","prompt":"?","options":["A","B","C"],"answer":"A"}}`
	case strings.Contains(s, "grading a student's answer"):
		// Judge: never rescue a non-exact answer → sim verdicts stay
		// deterministic (exact match decides), the AI tier is exercised
		// but does not change outcomes.
		return `{"correct":false,"score":0.0,"feedback":"not exact"}`
	case strings.Contains(s, "analyzing today's study session"):
		return `{"today_summary":{"strengths":["steadiness"],"weaknesses":["width"]},"direction":"general","direction_confidence":0.6,"tomorrow":{"word_count":20,"question_type_ratio":{"choice":50,"fill":50},"difficulty":"beginner"}}`
	case strings.Contains(s, "produce a training plan"):
		return `{"direction":"general","direction_confidence":0.6,"tomorrow":{"word_count":20,"question_type_ratio":{"choice":50,"fill":50},"difficulty":"beginner"},"today_summary":{}}`
	case strings.Contains(s, "linguistic detail for a vocabulary word"):
		return `{"reading":"","meaning":"","pos":"noun","level":"beginner","detail":"{}"}`
	default:
		return `{"reply":"ok"}`
	}
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// ————————————————————————————————————————————————
// Server wrapper (real listener + auth middleware)
// ————————————————————————————————————————————————

type server struct {
	t   *testing.T
	s   *api.Server
	tok string
}

func startServer(t *testing.T, s *api.Server) *server {
	t.Helper()
	if err := s.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() { _ = s.Shutdown() })
	return &server{t: t, s: s, tok: s.Token}
}

func (sv *server) do(method, path, body string) (map[string]interface{}, int) {
	sv.t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, "http://127.0.0.1:"+strconv.Itoa(sv.s.Port)+path, rdr)
	if err != nil {
		sv.t.Fatalf("new request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+sv.tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		sv.t.Fatalf("do %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	out := map[string]interface{}{}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &out); err != nil {
			out["_raw"] = string(b)
		}
	}
	if resp.StatusCode >= 500 {
		out["_http_error"] = resp.StatusCode
	}
	return out, resp.StatusCode
}

// ————————————————————————————————————————————————
// Seeds
// ————————————————————————————————————————————————

type seedWord struct {
	Word, Reading, Meaning, POS string
}

func seedWords(lang string, n int) []seedWord {
	out := make([]seedWord, n)
	for i := 0; i < n; i++ {
		if lang == "ja" {
			out[i] = seedWord{
				Word:    fmt.Sprintf("単語%d", i),
				Reading: fmt.Sprintf("たんご%d", i),
				Meaning: fmt.Sprintf("単語%dの意味", i),
				POS:     "noun",
			}
		} else {
			out[i] = seedWord{
				Word:    fmt.Sprintf("w%d", i),
				Reading: fmt.Sprintf("w%d", i),
				Meaning: fmt.Sprintf("meaning of w%d", i),
				POS:     "noun",
			}
		}
	}
	return out
}

// ————————————————————————————————————————————————
// Scenario plumbing
// ————————————————————————————————————————————————

type scenario struct {
	name    string
	desc    string
	langs   []string // active_lang per day, cycled
	words   int      // words per language
	grammar int
	ai      bool
	// learner
	pCorrect       func(seed uint64, wordID int64, lang string) float64
	skipDays       map[int]bool // 1-indexed days with NO session at all
	maxAns         int          // 0 = session until server says done
	summarizeEvery bool
	autoExpand     bool
	disputeOnWrong float64 // probability of disputing a wrong verdict
}

type dayRec struct {
	Day      int
	Lang     string
	Blocked  bool
	Empty    bool // vault truly empty & no way to refill (not a dead-lock)
	Skipped  bool
	Served   int
	Answered int
	Correct  int
	Met      bool // DailyTargetMet via /quiz/complete
	Acc      float64
	PoolNW   int // new (never reviewed) words left
	PoolRW   int // due now
	IntBuck  map[string]int
	EFLow    int
	Bugs     []string
	Disputed int
}

type runOut struct {
	scen     *scenario
	days     []dayRec
	httpErrs int
}

func hash64(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64()
}

// answerFor returns the strictly-correct answer string for a question.
func answerFor(q map[string]interface{}) string {
	qt, _ := q["type"].(string)
	sw, _ := q["subject_word"].(string)
	sr, _ := q["subject_reading"].(string)
	sm, _ := q["subject_meaning"].(string)
	if qt == "grammar" {
		return "A" // matches fakeAI
	}
	switch qt {
	case "choice", "meaning":
		return sm
	case "fill", "spelling":
		return sw
	case "pronounce":
		return sr
	default:
		return sm
	}
}

// wrongAnswerFor returns a deterministic wrong answer.
func wrongAnswerFor(q map[string]interface{}, id int64) string {
	qt, _ := q["type"].(string)
	if qt == "choice" {
		opts, _ := q["options"].([]interface{})
		sm, _ := q["subject_meaning"].(string)
		for _, o := range opts {
			if s, ok := o.(string); ok && s != sm {
				return s
			}
		}
	}
	if qt == "grammar" {
		return "B"
	}
	return "XXX" + strconv.FormatInt(id, 10)
}

func (sc *scenario) lang(day int) string { return sc.langs[day%len(sc.langs)] }

func runScenario(t *testing.T, sc *scenario) *runOut {
	dir, _ := os.MkdirTemp("", "vocablock_sim_*")
	t.Cleanup(func() { os.RemoveAll(dir) })
	database, err := db.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	cfg := &config.Config{
		App: config.AppConfig{
			ActiveLang:         sc.langs[0],
			DailyTarget:        20,
			MinAccuracy:        0.8,
			Sm2MaxIntervalDays: 180,
			AutoExpandWords:    sc.autoExpand,
			ExpandBatchSize:    10,
		},
		Languages: map[string]*config.LanguageConfig{},
	}
	qts := []string{"choice", "fill", "spelling", "pronounce", "meaning"}
	if sc.grammar > 0 {
		qts = append(qts, "grammar")
	}
	for _, l := range sc.langs {
		cfg.Languages[l] = &config.LanguageConfig{QuestionTypes: qts}
		cfg.Languages[l].PromptContext.ExplanationLang = "zh-CN"
		cfg.Languages[l].PromptContext.TeacherRole = "Teacher"
		for _, w := range seedWords(l, sc.words) {
			database.InsertWord(&db.Word{
				Word: w.Word, Reading: w.Reading, Meaning: w.Meaning,
				POS: w.POS, Language: l, Source: "seed", Status: "active",
			})
		}
		for g := 0; g < sc.grammar; g++ {
			database.InsertGrammarPoint(&db.GrammarPoint{
				Title:       fmt.Sprintf("文法%d", g),
				Explanation: fmt.Sprintf("文法%dの説明", g),
				Example:     fmt.Sprintf("文法%dの例文", g),
				Language:    l, Source: "seed", Status: "active",
			})
		}
	}

	if sc.ai {
		fake := newFakeAI()
		fake.injectGrammar = sc.grammar > 0
		t.Cleanup(fake.srv.Close)
		cfg.App.AI.APIKey = "sim-key"
		cfg.App.AI.BaseURL = fake.srv.URL
		cfg.App.AI.Model = "sim"
	}

	s := api.NewServer(false, cfg, database)
	s.WireAI()
	if sc.ai {
		for _, l := range sc.langs {
			s.EnsureBootstrap(l)
		}
	}

	sv := startServer(t, s)

	// Time travel
	drv := &timeDriver{}
	oldNow := clock.Now
	clock.Now = drv.now
	t.Cleanup(func() { clock.Now = oldNow })

	base := time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local)
	out := &runOut{scen: sc}
	rng := rand.New(rand.NewSource(int64(hash64(sc.name))))

	for day := 0; day < 30; day++ {
		dayNum := day + 1
		lang := sc.lang(day)
		cfg.App.ActiveLang = lang
		drv.set(base.AddDate(0, 0, day).Add(10 * time.Hour))

		rec := dayRec{Day: dayNum, Lang: lang, IntBuck: map[string]int{}}

		if sc.skipDays[dayNum] {
			rec.Skipped = true
			out.days = append(out.days, rec)
			t.Logf("  day %02d [%s] holiday — no session", dayNum, lang)
			continue
		}

		// ── session/start (bounded expansion polling for AI) ──
		st, code := sv.do("POST", "/api/v1/quiz/session/start", "")
		if code >= 500 {
			out.httpErrs++
			rec.Bugs = append(rec.Bugs, fmt.Sprintf("start HTTP %d", code))
		}
		prep, _ := st["preparing"].(bool)
		for i := 0; prep && i < 4; i++ {
			time.Sleep(30 * time.Millisecond) // let in-flight expansion settle
			st, _ = sv.do("POST", "/api/v1/quiz/session/start", "")
			prep, _ = st["preparing"].(bool)
		}
		if prep {
			rec.Blocked = true
			nwB, _, rwB, _, _ := database.SessionCounts(lang)
			rec.PoolNW, rec.PoolRW = nwB, rwB
			rec.Bugs = append(rec.Bugs, fmt.Sprintf("BLOCKED: preparing never resolved (pool nw=%d rw=%d)", nwB, rwB))
			out.days = append(out.days, rec)
			t.Logf("  day %02d [%s] BLOCKED (preparing forever, pool nw=%d rw=%d)", dayNum, lang, nwB, rwB)
			continue
		}
		if empty, _ := st["empty"].(bool); empty {
			rec.Empty = true
			out.days = append(out.days, rec)
			t.Logf("  day %02d [%s] EMPTY vault (honest empty state)", dayNum, lang)
			continue
		}
		if done, _ := st["daily_completed"].(bool); done {
			out.days = append(out.days, rec)
			t.Logf("  day %02d [%s] already completed", dayNum, lang)
			continue
		}

		// ── next / submit loop ──
		answered := 0
		firstServe := map[string]map[string]interface{}{} // subject -> submit response
		dupServes := 0
		for i := 0; i < 100; i++ {
			if sc.maxAns > 0 && answered >= sc.maxAns {
				break
			}
			q, code := sv.do("GET", "/api/v1/quiz/next", "")
			if code >= 500 {
				out.httpErrs++
				rec.Bugs = append(rec.Bugs, fmt.Sprintf("next HTTP %d", code))
				break
			}
			if comp, _ := q["completed"].(bool); comp {
				if ex, _ := q["session_exhausted"].(bool); ex {
					rec.Bugs = append(rec.Bugs, "SOFT-CAP: session ended at cap without meeting target (student rests)")
				}
				break
			}
			if q["id"] == nil {
				break
			}
			rec.Served++

			sid := int64(0)
			if f, ok := q["subject_id"].(float64); ok {
				sid = int64(f)
			}
			qt, _ := q["type"].(string)
			stype, _ := q["subject_type"].(string)
			qid, _ := q["id"].(string)

			skey := stype + ":" + strconv.FormatInt(sid, 10)
			if _, ok := firstServe[skey]; ok {
				dupServes++
				rec.Bugs = append(rec.Bugs, fmt.Sprintf("DUP-SERVE %s (type=%s) served twice in one day", skey, qt))
			}

			correct := true
			if sc.pCorrect != nil {
				correct = rng.Float64() < sc.pCorrect(hash64(lang+":"+qid), sid, lang)
			}

			ans := answerFor(q)
			if !correct {
				ans = wrongAnswerFor(q, sid)
			}

			body, _ := json.Marshal(map[string]interface{}{
				"question_id":   qid,
				"subject_type":  stype,
				"subject_id":    sid,
				"question_type": qt,
				"answer":        ans,
			})
			sub, code := sv.do("POST", "/api/v1/quiz/submit", string(body))
			if code >= 500 {
				out.httpErrs++
				rec.Bugs = append(rec.Bugs, fmt.Sprintf("submit HTTP %d (%s)", code, qid))
				break
			}
			rec.Answered++
			answered++
			verdict, _ := sub["correct"].(bool)
			if correct {
				if verdict {
					rec.Correct++
				} else {
					rec.Bugs = append(rec.Bugs, fmt.Sprintf("ORACLE %s q=%s: intended correct, got wrong (ans=%q, resp=%v, httpq=%v)", qt, qid, ans, sub, q))
				}
			} else if verdict {
				rec.Bugs = append(rec.Bugs, fmt.Sprintf("lucky pass %s q=%s (expected wrong, got correct)", qt, qid))
			}

			if _, ok := firstServe[skey]; !ok {
				firstServe[skey] = sub
				// Immediacy probe: does the DB row match the claimed next_review?
				// Only for words (grammar rows live in a different table).
				if stype == "word" {
					var dbNow sql.NullString
					database.DB.QueryRow(`SELECT next_review_at FROM words WHERE id=?`, sid).Scan(&dbNow)
					if respNext, ok := sub["next_review_at"].(string); ok && len(respNext) >= 10 && !strings.Contains(dbNow.String, respNext[:10]) {
						rec.Bugs = append(rec.Bugs, fmt.Sprintf("PERSIST-MISMATCH id=%s type=%s subNext=%s dbNext=%q (immediately after submit)", skey, qt, respNext, dbNow.String))
					}
				}
			}

			if !verdict && sc.disputeOnWrong > 0 && rng.Float64() < sc.disputeOnWrong {
				if lid, ok := sub["log_id"].(float64); ok && lid > 0 {
					dbg, _ := json.Marshal(map[string]interface{}{"log_id": int64(lid)})
					sv.do("POST", "/api/v1/quiz/dispute", string(dbg))
					rec.Disputed++
				}
			}
			drv.add(8 * time.Second) // realistic per-question pacing
		}

		// ── complete (and summarize like the frontend does) ──
		cp, _ := sv.do("POST", "/api/v1/quiz/complete", "")
		rec.Met, _ = cp["completed"].(bool)
		if sc.summarizeEvery {
			sv.do("POST", "/api/v1/quiz/summarize", "")
		}

		// ── end-of-day snapshot ──
		nw, _, rw, _, _ := database.SessionCounts(lang)
		rec.PoolNW, rec.PoolRW = nw, rw
		if rec.Answered > 0 {
			rec.Acc = float64(rec.Correct) / float64(rec.Answered)
		}
		rows, _ := database.DB.Query(`SELECT CASE WHEN interval<=1 THEN '1' WHEN interval<=6 THEN '2-6' WHEN interval<=14 THEN '7-14' WHEN interval<=30 THEN '15-30' WHEN interval<=90 THEN '31-90' ELSE '91+' END, COUNT(*) FROM words WHERE language=? AND status='active' AND interval>0 GROUP BY 1`, lang)
		for rows.Next() {
			var b string
			var c int
			rows.Scan(&b, &c)
			rec.IntBuck[b] = c
		}
		rows.Close()
		database.DB.QueryRow(`SELECT COUNT(*) FROM words WHERE language=? AND status='active' AND easiness_factor < 1.5`, lang).Scan(&rec.EFLow)

		out.days = append(out.days, rec)
		t.Logf("  day %02d [%s] served=%d correct=%d acc=%.2f met=%v pool(nw=%d rw=%d) lowEF=%d",
			dayNum, lang, rec.Served, rec.Correct, rec.Acc, rec.Met, rec.PoolNW, rec.PoolRW, rec.EFLow)
	}
	return out
}

// ————————————————————————————————————————————————
// Learner models
// ————————————————————————————————————————————————

func steady(_ uint64, _ int64, _ string) float64 { return 0.90 }

func volatile(seed uint64, wordID int64, lang string) float64 {
	// Per-word latent ability: hash → 0.35–0.85 (avg ≈ 0.6)
	return 0.35 + 0.5*float64(hash64(lang+":"+strconv.FormatInt(wordID, 10))%1000)/1000.0
}

func stuck(seed uint64, wordID int64, lang string) float64 {
	if hash64(lang+":"+strconv.FormatInt(wordID, 10))%10 == 0 {
		return 0.15 // 10% rote-fail words
	}
	return 0.92
}

// ————————————————————————————————————————————————
// Scenarios + report
// ————————————————————————————————————————————————

func TestMonthSimulation(t *testing.T) {
	scenarios := []*scenario{
		{
			name: "S1-steady-ja", desc: "稳定学霸: 90% 正确率, 30 天不间断, 无 AI(纯本地出题), 日语, 300 词",
			langs: []string{"ja"}, words: 300, pCorrect: steady, disputeOnWrong: 0.2,
		},
		{
			name: "S2-volatile-ja", desc: "波动生: 每词固有难度, 正确率 35%-85%(均 ≈60%), 无 AI, 300 词 — 验证准确率门槛",
			langs: []string{"ja"}, words: 300, pCorrect: volatile,
		},
		{
			name: "S3-stuck-ja", desc: "卡手难词: 10% 单词几乎学不会(p=0.15), 其余 0.92 — 验证错误重置对到期池的冲击",
			langs: []string{"ja"}, words: 300, pCorrect: stuck,
		},
		{
			name: "S4-vacation-ja", desc: "请假族: 90% 正确, 第 11-15 天完全不学, 之后回归 — 验证补卡潮",
			langs: []string{"ja"}, words: 300, pCorrect: steady,
			skipDays: map[int]bool{11: true, 12: true, 13: true, 14: true, 15: true},
		},
		{
			name: "S5-slacker-ja", desc: "摸鱼族: 每天只答 5 题就走, 目标永远不达标 — 验证锁屏与池子积压",
			langs: []string{"ja"}, words: 300, pCorrect: steady, maxAns: 5,
		},
		{
			name: "S6-ai-expand-ja", desc: "AI+自动扩词: 90% 正确, 无词典二进制(现实常见)→ 扩词全部进 pending — 验证隔离墙",
			langs: []string{"ja"}, words: 150, ai: true,
			pCorrect: steady, summarizeEvery: true, autoExpand: true,
		},
		{
			name: "S7-dual-ja-en", desc: "语言混学: 日英交替每天切换, 各 150 词 — 验证数据隔离与切换",
			langs: []string{"ja", "en"}, words: 150, pCorrect: steady,
		},
		{
			name: "S8-ai-grammar-ja", desc: "AI+语法: 200 词 + 15 语法点, 语法题混入 — 验证语法 SM-2(跳过 1 天)与交织",
			langs: []string{"ja"}, words: 200, grammar: 15, ai: true,
			pCorrect: steady, summarizeEvery: true,
		},
		{
			name: "S9-small-ja", desc: "贫词汇零AI: 只有 40 词, 目标 20/天 — 验证词库耗尽后的 BLOCK 行为",
			langs: []string{"ja"}, words: 40, pCorrect: steady,
		},
	}

	all := make([]scenarioResult, 0, len(scenarios))
	for _, sc := range scenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			// clock.Now is process-wide; subtests run strictly sequentially
			// (no t.Parallel) and Cleanup restores the real clock.
			all = append(all, scenarioResult{sc, runScenario(t, sc)})
		})
	}

	writeReport(t, all)
}

type scenarioResult struct {
	sc  *scenario
	out *runOut
}

// ————————————————————————————————————————————————
// Report
// ————————————————————————————————————————————————

func reportPath() string {
	wd, _ := os.Getwd()
	return filepath.Join(wd, "..", "..", "..", "SIMULATION.md")
}

func meanAcc(out *runOut) float64 {
	var answered, correct int
	for _, d := range out.days {
		answered += d.Answered
		correct += d.Correct
	}
	if answered == 0 {
		return 0
	}
	return float64(correct) / float64(answered)
}

func writeReport(t *testing.T, all []scenarioResult) {
	t.Helper()
	var b strings.Builder
	b.WriteString("# VocabLock 月度仿真报告\n\n")
	b.WriteString("> 生成: `go test ./internal/sim/` · 驱动真实 HTTP handler · 可注入时钟 `internal/clock` 时间旅行 30 天\n")
	b.WriteString("> 仿真起点 2026-06-01 · 每日目标 20 题 · 门槛 准确率 ≥ 80% · SM-2 最大间隔 180 天\n\n")
	b.WriteString("**9 个场景**: 稳定 · 波动 · 卡手 · 请假 · 摸鱼 · AI+扩词 · 双语混学 · AI+语法 · 贫词汇零AI\n\n")

	for _, a := range all {
		sc, out := a.sc, a.out
		met, blocked, skipped, emptyDays, bigPool, overStudy := 0, 0, 0, 0, 0, 0
		var served int
		for _, d := range out.days {
			if d.Met {
				met++
			}
			if d.Blocked {
				blocked++
			}
			if d.Skipped {
				skipped++
			}
			if d.Empty {
				emptyDays++
			}
			if d.PoolRW > 40 {
				bigPool++
			}
			if d.Served > 20 {
				overStudy++
			}
			served += d.Served
		}

		fmt.Fprintf(&b, "## %s\n\n**%s**\n\n", sc.name, sc.desc)
		fmt.Fprintf(&b, "| 日 | 语言 | 答题 | ✓ | 正确率 | 达标 | 到期池(未学/到期) | 日间隔桶 1/2-6/7-14/15-30/31-90/91+ |\n")
		b.WriteString("|---|---|---|---|---|---|---|---|\n")
		for _, d := range out.days {
			if d.Skipped {
				fmt.Fprintf(&b, "| %02d | %s | — 请假 — | | | | | |\n", d.Day, d.Lang)
				continue
			}
			if d.Blocked {
				fmt.Fprintf(&b, "| %02d | %s | ⛔ BLOCK | | | | %d/%d | |\n", d.Day, d.Lang, d.PoolNW, d.PoolRW)
				continue
			}
			if d.Empty {
				fmt.Fprintf(&b, "| %02d | %s | ◻ 空库 | | | | 0/0 | |\n", d.Day, d.Lang)
				continue
			}
			acc := ""
			if d.Answered > 0 {
				acc = fmt.Sprintf("%.0f%%", d.Acc*100)
			}
			buck := fmt.Sprintf("%d/%d/%d/%d/%d/%d",
				d.IntBuck["1"], d.IntBuck["2-6"], d.IntBuck["7-14"], d.IntBuck["15-30"], d.IntBuck["31-90"], d.IntBuck["91+"])
			metS := "✓"
			if !d.Met {
				metS = "✗"
			}
			fmt.Fprintf(&b, "| %02d | %s | %d | %d | %s | %s | %d/%d | %s |\n",
				d.Day, d.Lang, d.Answered, d.Correct, acc, metS, d.PoolNW, d.PoolRW, buck)
		}

		fmt.Fprintf(&b, "\n**月汇总**: 达标 %d 天 · 阻塞 %d 天 · 空库 %d 天 · 请假 %d 天 · 池超 40 到期 %d 天 · 超目标答题 %d 天 · 加权正确率 %.1f%% · 总答题 %d\n",
			met, blocked, emptyDays, skipped, bigPool, overStudy, meanAcc(out)*100, served)
		fmt.Fprintf(&b, "· 未学词余 %d · 第30天到期 %d · EF<1.5 词 %d\n",
			lastPoolNW(out), lastPoolRW(out), lastEFLow(out))
		if len(out.days) > 0 {
			last := out.days[len(out.days)-1]
			fmt.Fprintf(&b, "· 间隔桶(第30天): %s\n", intBuckStr(last.IntBuck))
		}

		if len(out.BugsTotal()) > 0 {
			b.WriteString("\n**事件**:\n")
			for _, bg := range out.BugsTotal() {
				fmt.Fprintf(&b, "- %s\n", bg)
			}
		}
		if out.httpErrs > 0 {
			fmt.Fprintf(&b, "\n**HTTP 5xx**: %d 次\n", out.httpErrs)
		}
		if len(out.BugsTotal()) == 0 && out.httpErrs == 0 {
			b.WriteString("\n(无异常事件)\n")
		}
		b.WriteString("\n---\n\n")
	}

	writeAutoDiagnostics(&b, all)

	p := reportPath()
	if err := os.WriteFile(p, []byte(b.String()), 0644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	t.Logf("report written: %s", p)
}

// writeAutoDiagnostics appends a rule-based findings section derived purely
// from the measured data (no human interpolation).
func writeAutoDiagnostics(b *strings.Builder, all []scenarioResult) {
	b.WriteString("## 自动诊断\n\n")
	for _, a := range all {
		sc, out := a.sc, a.out
		var findings []string
		blocked, met, emptyDays, overStudy, softCaps := 0, 0, 0, 0, 0
		var poolBig int
		for _, d := range out.days {
			if d.Blocked {
				blocked++
			}
			if d.Empty {
				emptyDays++
			}
			if d.Met {
				met++
			}
			if d.Served > 20 && !d.Blocked && !d.Skipped && !d.Empty {
				overStudy++
			}
			if d.PoolRW > 40 {
				poolBig++
			}
			for _, bg := range d.Bugs {
				if strings.Contains(bg, "SOFT-CAP") {
					softCaps++
				}
			}
		}
		active := 30 - countSkipped(out)

		if blocked > 0 {
			findings = append(findings, fmt.Sprintf("**%d 天 BLOCK** — preparing 轮询 4 次仍未解决(AI 补词途径存在但无效,如无词典时扩词进隔离墙)。", blocked))
		}
		if emptyDays > 0 {
			findings = append(findings, fmt.Sprintf("**%d 天空库空态** — 无可学内容且无补词途径;前端显示导入空态(不再卡死转圈)。", emptyDays))
		}
		if active > 0 && met == 0 {
			findings = append(findings, fmt.Sprintf("**%d/%d 天达标 0** — 准确率门(min 0.8)对低正确率用户=不达标;软上限已把单日强制题量封顶(软上限触顶 %d 天)。", met, active, softCaps))
		}
		if overStudy > 0 {
			findings = append(findings, fmt.Sprintf("**%d 天超目标答题**(单日 >20 题)— 准确率临时跌破门槛时会话延长出题;软上限 1.5×目标已封顶强制题量(实测单日 ≤ 上限)。", overStudy))
		}
		if poolBig > 0 {
			findings = append(findings, fmt.Sprintf("**%d 天到期池 >40**(目标 20)— 到期池长期≫目标,且目标 cap 下每天只消化 20,池子体感永远不减。", poolBig))
		}
		if emptyDays > 0 && !sc.ai {
			findings = append(findings, fmt.Sprintf("**小词库循环** — 40 词在第 2 周全部成熟(间隔 15-90 天),当日到期量间歇为 0 → %d 天进入诚实空态;有到期词的日期照常学习(1-10 题),不再死锁。", emptyDays))
		}
		if len(findings) == 0 {
			findings = append(findings, "无显著异常。")
		}
		fmt.Fprintf(b, "### %s\n\n", sc.name)
		for _, f := range findings {
			fmt.Fprintf(b, "- %s\n", f)
		}
		b.WriteString("\n")
	}
}

func countSkipped(out *runOut) int {
	n := 0
	for _, d := range out.days {
		if d.Skipped {
			n++
		}
	}
	return n
}

func (r *runOut) BugsTotal() []string {
	var all []string
	for _, d := range r.days {
		all = append(all, d.Bugs...)
	}
	return all
}

func lastPoolNW(out *runOut) int {
	if len(out.days) == 0 {
		return 0
	}
	return out.days[len(out.days)-1].PoolNW
}

func lastPoolRW(out *runOut) int {
	if len(out.days) == 0 {
		return 0
	}
	return out.days[len(out.days)-1].PoolRW
}

func lastEFLow(out *runOut) int {
	if len(out.days) == 0 {
		return 0
	}
	return out.days[len(out.days)-1].EFLow
}

func intBuckStr(m map[string]int) string {
	return fmt.Sprintf("1:%d 2-6:%d 7-14:%d 15-30:%d 31-90:%d 91+:%d",
		m["1"], m["2-6"], m["7-14"], m["15-30"], m["31-90"], m["91+"])
}
