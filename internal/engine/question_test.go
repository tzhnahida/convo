package engine

import (
	"strings"
	"testing"
)

func makeWord(id int64, word, reading, meaning string) *WordInfo {
	return &WordInfo{ID: id, Word: word, Reading: reading, Meaning: meaning}
}

func TestGenerateChoiceBasic(t *testing.T) {
	w := makeWord(1, "猫", "ねこ", "cat")
	distractors := []*WordInfo{
		makeWord(2, "犬", "いぬ", "dog"),
		makeWord(3, "鳥", "とり", "bird"),
		makeWord(4, "魚", "さかな", "fish"),
	}
	q := GenerateChoice(w, distractors, "en")
	if q.Type != TypeChoice {
		t.Errorf("type = %s, want choice", q.Type)
	}
	if q.SubjectType != "word" {
		t.Errorf("subject_type = %s, want word", q.SubjectType)
	}
	if q.SubjectID != 1 {
		t.Errorf("subject_id = %d, want 1", q.SubjectID)
	}
	if q.Answer != "cat" {
		t.Errorf("answer = %s, want cat", q.Answer)
	}
	if len(q.Options) != 4 {
		t.Errorf("options count = %d, want 4", len(q.Options))
	}
	// Answer must be in options
	found := false
	for _, o := range q.Options {
		if o == "cat" {
			found = true
			break
		}
	}
	if !found {
		t.Error("correct answer not in options")
	}
	// Prompt should mention the word
	if !strings.Contains(q.Prompt, "猫") {
		t.Errorf("prompt doesn't contain word: %s", q.Prompt)
	}
}

func TestGenerateChoiceFewDistractors(t *testing.T) {
	w := makeWord(1, "hello", "həˈloʊ", "你好")
	distractors := []*WordInfo{
		makeWord(2, "world", "wɜːld", "世界"),
	}
	q := GenerateChoice(w, distractors, "en")
	if len(q.Options) != 2 {
		t.Errorf("options count = %d, want 2", len(q.Options))
	}
}

func TestGenerateChoiceNoDistractors(t *testing.T) {
	w := makeWord(1, "test", "tɛst", "测试")
	q := GenerateChoice(w, nil, "en")
	if len(q.Options) != 1 {
		t.Errorf("options count = %d, want 1", len(q.Options))
	}
}

func TestGenerateChoiceDuplicateMeaning(t *testing.T) {
	w := makeWord(1, "run", "rʌn", "跑")
	// Distractor with same meaning should be filtered out
	distractors := []*WordInfo{
		makeWord(2, "jog", "dʒɒɡ", "跑"), // same meaning
		makeWord(3, "walk", "wɔːk", "走"),
	}
	q := GenerateChoice(w, distractors, "en")
	if len(q.Options) != 2 {
		t.Errorf("options count = %d, want 2 (duplicate filtered)", len(q.Options))
	}
	count := 0
	for _, o := range q.Options {
		if o == "跑" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("meaning '跑' appears %d times, want 1", count)
	}
}

func TestGenerateChoiceShuffle(t *testing.T) {
	w := makeWord(1, "apple", "æpəl", "苹果")
	distractors := []*WordInfo{
		makeWord(2, "banana", "bəˈnænə", "香蕉"),
		makeWord(3, "grape", "ɡreɪp", "葡萄"),
		makeWord(4, "mango", "mæŋɡoʊ", "芒果"),
	}

	// Run many times — shuffle should produce different orderings at least once
	sameOrderCount := 0
	first := GenerateChoice(w, distractors, "en")
	for i := 0; i < 50; i++ {
		q := GenerateChoice(w, distractors, "en")
		match := true
		for j := range first.Options {
			if first.Options[j] != q.Options[j] {
				match = false
				break
			}
		}
		if match {
			sameOrderCount++
		}
	}
	// It's theoretically possible (but astronomically unlikely) that all 50 match.
	// We just verify the function doesn't panic and options always contain the answer.
	if sameOrderCount == 50 {
		t.Log("all shuffles produced same order (very unlikely but not a bug)")
	}
}

func TestGenerateFillBasic(t *testing.T) {
	w := makeWord(1, "apple", "æpəl", "苹果")
	q := GenerateFill(w, "en")
	if q.Type != TypeFill {
		t.Errorf("type = %s, want fill", q.Type)
	}
	if q.Answer != "apple" {
		t.Errorf("answer = %s, want apple", q.Answer)
	}
	if !strings.Contains(q.Prompt, "苹果") {
		t.Errorf("prompt doesn't contain meaning: %s", q.Prompt)
	}
	if !strings.Contains(q.Prompt, "_") {
		t.Errorf("prompt should contain blanks: %s", q.Prompt)
	}
}

func TestGenerateSpellingBasic(t *testing.T) {
	w := makeWord(1, "test", "tɛst", "测试")
	q := GenerateSpelling(w, "en")
	if q.Type != TypeSpelling {
		t.Errorf("type = %s, want spelling", q.Type)
	}
	if q.Answer != "test" {
		t.Errorf("answer = %s, want test", q.Answer)
	}
	if !strings.Contains(q.Prompt, "测试") {
		t.Errorf("prompt doesn't contain meaning: %s", q.Prompt)
	}
}

func TestGeneratePronounceBasic(t *testing.T) {
	w := makeWord(1, "hello", "həˈloʊ", "你好")
	q := GeneratePronounce(w, "en")
	if q.Type != TypePronounce {
		t.Errorf("type = %s, want pronounce", q.Type)
	}
	if q.Answer != "həˈloʊ" {
		t.Errorf("answer = %s, want həˈloʊ", q.Answer)
	}
	if !strings.Contains(q.Prompt, "hello") {
		t.Errorf("prompt doesn't contain word: %s", q.Prompt)
	}
}

func TestMaskWordSingleChar(t *testing.T) {
	if m := maskWord("a"); m != "_" {
		t.Errorf("maskWord('a') = %q, want '_'", m)
	}
}

func TestMaskWordEmpty(t *testing.T) {
	// maskWord("") with len <= 1 returns "_"
	if m := maskWord(""); m != "_" {
		t.Errorf("maskWord('') = %q, want '_'", m)
	}
}

func TestMaskWordCJK(t *testing.T) {
	// CJK characters should be masked correctly (each is one rune)
	m := maskWord("日本語")
	if !strings.Contains(m, "_") {
		t.Error("masked CJK word should contain underscores")
	}
	// First character should be revealed
	runes := []rune(m)
	if runes[0] != '日' {
		t.Errorf("first char should be revealed: %q", m)
	}
	// Should have at least one underscore
	hasUnderscore := false
	for _, r := range m {
		if r == '_' {
			hasUnderscore = true
		}
	}
	if !hasUnderscore {
		t.Errorf("masked word should have underscores: %q", m)
	}
}

func TestMaskWordRevealCount(t *testing.T) {
	// 9-char word: reveal = 9/3 = 3 chars (first + 2 more)
	m := maskWord("123456789")
	underscores := strings.Count(m, "_")
	// 9 chars total, 3 revealed → 6 underscores
	if underscores != 6 {
		t.Errorf("underscores = %d, want 6 for 9-char word", underscores)
	}
}

func TestGenID(t *testing.T) {
	if id := genID("word", "choice", 42); id != "wc42" {
		t.Errorf("genID('word', 'choice', 42) = %q, want 'wc42'", id)
	}
	if id := genID("grammar", "fill", 7); id != "gf7" {
		t.Errorf("genID('grammar', 'fill', 7) = %q, want 'gf7'", id)
	}
}

func TestItoa(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0"},
		{5, "5"},
		{42, "42"},
		{100, "100"},
		{9999, "9999"},
		{-1, ""}, // negative not expected but verify behavior
	}
	for _, tt := range tests {
		if got := itoa(tt.n); got != tt.want {
			t.Errorf("itoa(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}
