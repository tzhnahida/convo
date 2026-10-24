package convo

import "testing"

// 2026-10-24 用户反馈: 「梗他知道的来源太少了, 好几次常见梗都不知道」。
// 内置种子要真的进库、且是 active (不是 candidate 试用态)。
func TestSeedCommonMemes(t *testing.T) {
	s := newTestStore(t)
	n, err := SeedCommonMemes(s)
	if err != nil {
		t.Fatal(err)
	}
	if n < 50 {
		t.Fatalf("种子太少: %d 条", n)
	}
	for _, phrase := range []string{"破防", "栓Q", "显眼包", "搭子", "电子榨菜"} {
		sl, err := s.SlangByPhrase(phrase)
		if err != nil || sl == nil {
			t.Fatalf("常见梗 %q 没进库: %v", phrase, err)
		}
		if sl.State != SlangActive {
			t.Fatalf("%q 状态是 %q, 应当是 active (不用试用)", phrase, sl.State)
		}
		if sl.Meaning == "" {
			t.Fatalf("%q 没有释义", phrase)
		}
	}
	// 幂等: 第二次一条都不加。
	if again, err := SeedCommonMemes(s); err != nil || again != 0 {
		t.Fatalf("种子不幂等: 第二次加了 %d 条 (err=%v)", again, err)
	}
}

// 种子**不许覆盖**用户自己说过/教过的释义。
func TestSeedCommonMemes_DoesNotOverwriteUserMeaning(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.UpsertSlang(SlangDelta{Phrase: "破防", Meaning: "用户自己的理解", Confidence: 1}, "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := SeedCommonMemes(s); err != nil {
		t.Fatal(err)
	}
	sl, err := s.SlangByPhrase("破防")
	if err != nil || sl == nil {
		t.Fatal("查不到破防")
	}
	if sl.Meaning != "用户自己的理解" {
		t.Fatalf("种子覆盖了用户释义: %q", sl.Meaning)
	}
}
