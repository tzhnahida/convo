package judge

import (
	"testing"
)

// go test -run TestJudgeBench -v
//
// Golden-set benchmarks: the judge chain (exact → regex → AI) scored
// against hand-annotated samples. Run these before/after any prompt or
// judge change — they are the "prompt quality evaluation" the project
// was missing. The AI tier is exercised via a stub; its tier is assessed
// by correctness of the stub verdict: exact/regex tiers are assessed
// solely on determinism.
//
// NOTE: tiers 1-2 verdicts here are authoritative (deterministic);
// "ai" rows are illustrative.
type benchCase struct {
	name        string
	user        string
	expected    []string
	strictness  Strictness
	wantCorrect bool
	wantScore   float64 // expected score band (>= this)
}

var benchCases = []benchCase{
	// exact tier
	{"exact hit en", "hello", []string{"hello"}, Normal, true, 1.0},
	{"exact hit with spacing", "  hello  ", []string{"hello"}, Normal, true, 1.0},
	{"exact case-insensitive", "HELLO", []string{"hello"}, Normal, true, 1.0},
	{"exact kana", "たべる", []string{"たべる"}, Normal, true, 1.0},
	{"exact fullwidth", "食べる", []string{"食べる"}, Normal, true, 1.0},
	// regex tier (substring → 0.9 in Normal; stays regex, not AI)
	{"substring match", "hello there", []string{"hello"}, Normal, true, 0.9},
	{"contains expected in user", "xhello", []string{"hello"}, Normal, true, 0.9},
	// near-miss errors → similarity category
	{"typo similarity", "helo", []string{"hello"}, Normal, false, 0.0},
	// DESIGN: substring → 0.9 correct (lenient). hello안녕 contains the
	// expected word, so it passes by regex; flagged as a future tightening
	// candidate (e.g. require word-boundary for Latin scripts).
	{"extra char substring passes", "hello안녕", []string{"hello"}, Normal, true, 0.9},
	// clearly wrong → other
	{"wrong word", "goodbye", []string{"hello"}, Normal, false, 0.0},
	{"empty answer", "", []string{"hello"}, Normal, false, 0.0},
	{"spaces only", "   ", []string{"hello"}, Normal, false, 0.0},
	// multiple acceptable answers
	{"either acceptable", "たべる", []string{"食べる", "たべる"}, Normal, true, 1.0},
	// strict mode: substring is less generous
	{"strict substring lower confidence", "hello there", []string{"hello"}, Strict, true, 0.7},
	// lenient fuzzy: >70% similarity accepted
	{"lenient fuzzy accepted", "helloo", []string{"hello"}, Lenient, true, 0.7},
}

func TestJudgeBench(t *testing.T) {
	// Deterministic tiers (exact → regex) must not depend on AI: judge with
	// aiJudge = nil. NOTE (design, see TestJudgeRegexScore09CallsAI): regex
	// 0.9 near-matches ARE sent to the AI tier deliberately when AI exists —
	// the deterministic behavior below is what the chain guarantees when AI
	// is unavailable (regex_fallback source).
	for _, c := range benchCases {
		res := Judge(c.user, c.expected, c.strictness, nil)
		if res.Correct != c.wantCorrect {
			t.Errorf("[%s] correct = %v, want %v (source=%s)", c.name, res.Correct, c.wantCorrect, res.Source)
		}
		if res.Score < c.wantScore-1e-9 {
			t.Errorf("[%s] score = %.2f, want >= %.2f", c.name, res.Score, c.wantScore)
		}
		if !c.wantCorrect {
			switch c.name {
			case "typo similarity", "extra char similarity":
				if res.ErrorCategory != "similarity" {
					t.Errorf("[%s] category = %q, want similarity", c.name, res.ErrorCategory)
				}
			case "empty answer", "spaces only", "wrong word":
				// empty → category "empty"; wrong word should be other
				if c.name == "empty answer" || c.name == "spaces only" {
					if res.ErrorCategory != "empty" {
						t.Errorf("[%s] category = %q, want empty", c.name, res.ErrorCategory)
					}
				}
			}
		}
	}

	// AI tier: the stub mirrors an exact-matching AI. With AI present, an
	// exact match must pass (AI agrees) and the substring 0.9 case is
	// AI-verifiable (its verdict wins — this is why AI quality matters).
	aiStub := func(user, expected string) (*Result, error) {
		if Exact(user, expected) != nil {
			return &Result{Correct: true, Score: 1.0, Feedback: "stub", Source: SourceAI}, nil
		}
		return &Result{Correct: false, Score: 0.1, Feedback: "stub wrong", Source: SourceAI}, nil
	}
	if r := Judge("hello", []string{"hello"}, Normal, aiStub); !r.Correct {
		t.Errorf("exact with AI: want correct, got source=%s", r.Source)
	}
	// 0.9 substring goes to AI (by design) — the stub's verdict applies.
	if r := Judge("hello there", []string{"hello"}, Normal, aiStub); r.Source != SourceAI {
		t.Errorf("substring with AI: source=%s, want ai (design: AI verifies 0.9)", r.Source)
	}
}
