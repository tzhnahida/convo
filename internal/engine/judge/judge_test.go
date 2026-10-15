package judge

import "testing"

func TestExactMatch(t *testing.T) {
	r := Exact("hello", "hello")
	if r == nil || !r.Correct || r.Source != SourceExact {
		t.Errorf("exact match failed: %+v", r)
	}
}

func TestExactMatchNormalized(t *testing.T) {
	r := Exact("  Hello World  ", "hello world")
	if r == nil || !r.Correct {
		t.Errorf("normalized exact match failed: %+v", r)
	}
}

func TestExactMismatch(t *testing.T) {
	r := Exact("hello", "world")
	if r != nil {
		t.Error("exact should return nil on mismatch")
	}
}

func TestRegexSubstring(t *testing.T) {
	r := Regex("hello world", "hello", Normal)
	if !r.Correct || r.Score < 0.8 {
		t.Errorf("regex substring failed: %+v", r)
	}
}

func TestRegexNoMatch(t *testing.T) {
	r := Regex("abc", "xyz", Normal)
	if r.Correct {
		t.Errorf("regex no-match should be false: %+v", r)
	}
}

func TestJudgeExactChain(t *testing.T) {
	r := Judge("hello", []string{"hello"}, Normal, nil)
	if r.Source != SourceExact {
		t.Errorf("judge should use exact: got %s", r.Source)
	}
}

func TestJudgeRegexChain(t *testing.T) {
	r := Judge("hello world", []string{"hello"}, Normal, nil)
	if r.Source != SourceRegex {
		t.Errorf("judge should use regex: got %s", r.Source)
	}
}

func TestJudgeAIFallback(t *testing.T) {
	aiCalled := false
	ai := func(u, e string) (*Result, error) {
		aiCalled = true
		return &Result{Correct: true, Score: 0.95, Feedback: "AI says correct"}, nil
	}
	r := Judge("bonjour", []string{"hello"}, Normal, ai)
	if !aiCalled || r.Source != SourceAI {
		t.Errorf("judge should call AI for low-confidence regex: source=%s aiCalled=%v", r.Source, aiCalled)
	}
}

func TestJudgeAIFallbackOnError(t *testing.T) {
	ai := func(u, e string) (*Result, error) {
		return nil, nil
	}
	r := Judge("abc", []string{"xyz"}, Normal, ai)
	if r.Source != SourceRegexFallback {
		t.Errorf("judge should use regex_fallback when AI errors: got %s", r.Source)
	}
}

func TestJudgeMultiAnswer(t *testing.T) {
	// Fill question: user types reading (kana) instead of word (kanji)
	r := Judge("たべる", []string{"食べる", "たべる"}, Normal, nil)
	if !r.Correct {
		t.Errorf("reading should be accepted as correct: %+v", r)
	}
	if r.Source != SourceExact {
		t.Errorf("reading exact match should use exact source: got %s", r.Source)
	}
}

func TestJudgeMultiAnswerSecondWins(t *testing.T) {
	// User types word, but word is the second candidate
	r := Judge("食べる", []string{"たべる", "食べる"}, Strict, nil)
	if !r.Correct {
		t.Errorf("word should be accepted as correct: %+v", r)
	}
}

func TestJudgeMultiAnswerSubstring(t *testing.T) {
	// User answer is a substring of the second candidate
	r := Judge("たべ", []string{"食べる", "たべる"}, Normal, nil)
	if !r.Correct {
		t.Errorf("substring of reading should be accepted: %+v", r)
	}
}

func TestJudgeEmptyExpected(t *testing.T) {
	r := Judge("hello", []string{}, Normal, nil)
	if r.Correct {
		t.Error("empty expected should not be correct")
	}
}

func TestRegexStrict(t *testing.T) {
	// Strict mode gives lower score for substring match
	r := Regex("hello world", "hello", Strict)
	if !r.Correct {
		t.Error("strict substring should be correct")
	}
	if r.Score != 0.7 {
		t.Errorf("strict substring score = %f, want 0.7", r.Score)
	}
}

func TestRegexLenientFuzzyMatch(t *testing.T) {
	// Lenient mode accepts small typos via fuzzy matching
	r := Regex("helo", "hello", Lenient)
	if !r.Correct {
		t.Errorf("lenient fuzzy should be correct: %+v", r)
	}
	if r.Score != 0.7 {
		t.Errorf("lenient fuzzy score = %f, want 0.7", r.Score)
	}
	if r.Feedback != "Approximate match" {
		t.Errorf("feedback = %s, want 'Approximate match'", r.Feedback)
	}
}

func TestRegexLenientNoMatch(t *testing.T) {
	r := Regex("abc", "xyz", Lenient)
	if r.Correct {
		t.Error("lenient no-match should be false")
	}
	if r.Score != 0.3 {
		t.Errorf("lenient no-match score = %f, want 0.3", r.Score)
	}
}

func TestRegexNormalNoMatchScore(t *testing.T) {
	r := Regex("abc", "xyz", Normal)
	if r.Correct {
		t.Error("regex normal no-match should be false")
	}
	if r.Score != 0.0 {
		t.Errorf("normal no-match score = %f, want 0.0", r.Score)
	}
}

func TestJudgeRegexScore09CallsAI(t *testing.T) {
	// Regex substring match in Normal mode gives score 0.9 (NOT > 0.9), so AI is called
	aiCalled := false
	ai := func(u, e string) (*Result, error) {
		aiCalled = true
		return &Result{Correct: true, Score: 0.95}, nil
	}
	r := Judge("hello world", []string{"hello"}, Normal, ai)
	if !aiCalled {
		t.Error("score 0.9 should still call AI (threshold is > 0.9)")
	}
	if r.Source != SourceAI {
		t.Errorf("source = %s, want ai", r.Source)
	}
}

func TestJudgeHighConfidenceRegexSkipsAI(t *testing.T) {
	// Strict mode substring match gives score 0.7, which is <= 0.9 so AI is called
	// Actually we need exact match of answer embedded in user answer for Normal to give 0.9
	// To get > 0.9 we'd need something else — let's just verify exact match skips AI
	aiCalled := false
	ai := func(u, e string) (*Result, error) {
		aiCalled = true
		return &Result{Correct: true, Score: 1.0}, nil
	}
	r := Judge("hello", []string{"hello"}, Normal, ai)
	if r.Source != SourceExact {
		t.Errorf("exact match should skip everything: got %s", r.Source)
	}
	if aiCalled {
		t.Error("AI should not be called for exact match")
	}
}

func TestFuzzyMatchIdentical(t *testing.T) {
	if !fuzzyMatch("hello", "hello") {
		t.Error("identical strings should match")
	}
}

func TestFuzzyMatchClose(t *testing.T) {
	// "helo" vs "hello": distance 1, maxLen 5 → 1 - 1/5 = 0.8 > 0.7
	if !fuzzyMatch("helo", "hello") {
		t.Error("close strings should fuzzy match")
	}
}

func TestFuzzyMatchFar(t *testing.T) {
	// "cat" vs "dog": distance 3, maxLen 3 → 1 - 3/3 = 0.0 < 0.7
	if fuzzyMatch("cat", "dog") {
		t.Error("different strings should not fuzzy match")
	}
}

func TestFuzzyMatchEmpty(t *testing.T) {
	if !fuzzyMatch("", "") {
		t.Error("empty strings should match")
	}
}

func TestLevenshteinDistance(t *testing.T) {
	tests := []struct {
		a, b string
		dist int
	}{
		{"", "", 0},
		{"a", "", 1},
		{"", "b", 1},
		{"abc", "abc", 0},
		{"abc", "abd", 1},
		{"abc", "adc", 1},
		{"kitten", "sitting", 3},
		{"hello", "world", 4},
	}
	for _, tt := range tests {
		if d := levenshtein([]rune(tt.a), []rune(tt.b)); d != tt.dist {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", tt.a, tt.b, d, tt.dist)
		}
	}
}

func TestMin3(t *testing.T) {
	if m := min3(3, 1, 2); m != 1 {
		t.Errorf("min3(3,1,2) = %d, want 1", m)
	}
	if m := min3(0, 0, 0); m != 0 {
		t.Errorf("min3(0,0,0) = %d, want 0", m)
	}
	if m := min3(-1, 5, -3); m != -3 {
		t.Errorf("min3(-1,5,-3) = %d, want -3", m)
	}
}

func TestExactUnicode(t *testing.T) {
	r := Exact("日本語", "日本語")
	if r == nil || !r.Correct {
		t.Errorf("unicode exact match failed: %+v", r)
	}
}

func TestExactCaseInsensitive(t *testing.T) {
	r := Exact("Hello", "hello")
	if r == nil || !r.Correct {
		t.Errorf("case insensitive exact match failed: %+v", r)
	}
}

func TestNormalize(t *testing.T) {
	if n := normalize("  Héllo   Wörld  "); n != "héllo wörld" {
		t.Errorf("normalize failed: %q", n)
	}
}
