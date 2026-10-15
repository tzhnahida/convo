package convo

import (
	"strings"
	"testing"

	"github.com/tzhnahida/VocabLock/internal/clock"
)

// ——— 模块 11: 梗学习 ———

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(t.TempDir(), "test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestUpsertSlang_userActive(t *testing.T) {
	s := newTestStore(t)
	id, err := s.UpsertSlang(SlangDelta{
		Phrase: "麻了", Meaning: "累到无语", Example: "今天加班麻了",
		SourceQuote: "麻了麻了", Confidence: 0.9,
	}, "user")
	if err != nil {
		t.Fatal(err)
	}
	if id <= 0 {
		t.Fatal("expected id")
	}
	sl, err := s.SlangByPhrase("麻了")
	if err != nil {
		t.Fatal(err)
	}
	if sl.State != SlangActive {
		t.Fatalf("user verbatim should be active, got %s", sl.State)
	}
	if sl.Source != "user" || sl.Confidence != 0.9 {
		t.Fatalf("source=%s conf=%v", sl.Source, sl.Confidence)
	}
}

func TestUpsertSlang_conflictMerge(t *testing.T) {
	s := newTestStore(t)
	_, _ = s.UpsertSlang(SlangDelta{Phrase: "躺平", Meaning: "不卷了", Confidence: 0.6}, "web")
	// 再提取到 (conf 更高) → 取高; web 词条保持 candidate
	_, err := s.UpsertSlang(SlangDelta{Phrase: "躺平", Meaning: "不卷了", Confidence: 0.95}, "web")
	if err != nil {
		t.Fatal(err)
	}
	sl, _ := s.SlangByPhrase("躺平")
	if sl.Confidence != 0.95 {
		t.Fatalf("conf should merge to max, got %v", sl.Confidence)
	}
	if sl.State != SlangCandidate {
		t.Fatalf("web-origin should stay candidate, got %s", sl.State)
	}
	// 用户又说了 → 重新激活
	_, _ = s.UpsertSlang(SlangDelta{Phrase: "躺平", Meaning: "不卷了", Confidence: 0.9}, "user")
	sl, _ = s.SlangByPhrase("躺平")
	if sl.State != SlangActive {
		t.Fatalf("user re-say should reactivate, got %s", sl.State)
	}
}

func TestTopSlang_candidatePenaltyAndDecay(t *testing.T) {
	s := newTestStore(t)
	_, _ = s.UpsertSlang(SlangDelta{Phrase: "aaa", Meaning: "x", Confidence: 0.9}, "user") // active 0.9
	_, _ = s.UpsertSlang(SlangDelta{Phrase: "bbb", Meaning: "y", Confidence: 0.9}, "web")  // candidate 0.9*0.6=0.54
	sl, _ := s.SlangByPhrase("aaa")
	s.TouchSlang(sl.ID) // 挨着的 last_used 让 fresh 记忆
	top, err := s.TopSlang(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 2 {
		t.Fatalf("len=%d", len(top))
	}
	if top[0].Phrase != "aaa" {
		t.Fatalf("active should outrank candidate, got %s first", top[0].Phrase)
	}
}

func TestFindSlangHit_longestFirst(t *testing.T) {
	s := newTestStore(t)
	_, _ = s.UpsertSlang(SlangDelta{Phrase: "麻了", Meaning: "x", Confidence: 0.9}, "user")
	_, _ = s.UpsertSlang(SlangDelta{Phrase: "真的麻了", Meaning: "x", Confidence: 0.9}, "user")
	hits, err := s.FindSlangHit("我真的麻了兄弟们")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Phrase != "真的麻了" {
		t.Fatalf("want longest match first, got %+v", hits)
	}
	hits, _ = s.FindSlangHit("今天天气不错")
	if len(hits) != 0 {
		t.Fatalf("no hit expected, got %+v", hits)
	}
}

func TestTouchSlangText(t *testing.T) {
	s := newTestStore(t)
	_, _ = s.UpsertSlang(SlangDelta{Phrase: "破防了", Meaning: "x", Confidence: 0.9}, "user")
	sl, _ := s.SlangByPhrase("破防了")
	s.TouchSlangText("我直接破防了")
	sl2, _ := s.SlangByPhrase("破防了")
	if sl2.UseCount != sl.UseCount+1 || sl2.LastUsedTS == "" {
		t.Fatalf("touch failed: %+v", sl2)
	}
}

func TestRetireOldSlang_lifecycle(t *testing.T) {
	s := newTestStore(t)
	_, _ = s.UpsertSlang(SlangDelta{Phrase: "旧梗A", Meaning: "x", Confidence: 0.9}, "user")
	_, _ = s.UpsertSlang(SlangDelta{Phrase: "试用B", Meaning: "x", Confidence: 0.6}, "web")
	now := clock.Now()
	// 模拟 100 天前创建 (active) 与 30 天前创建 (candidate)
	_, _ = s.DB.Exec(`UPDATE convo_slang SET created_ts=? WHERE phrase='旧梗A'`,
		now.AddDate(0, 0, -100).Format("2006-01-02 15:04:05"))
	_, _ = s.DB.Exec(`UPDATE convo_slang SET created_ts=? WHERE phrase='试用B'`,
		now.AddDate(0, 0, -30).Format("2006-01-02 15:04:05"))
	n, err := s.RetireOldSlang(90, 21)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("retired=%d want 2", n)
	}
	a, _ := s.SlangByPhrase("旧梗A")
	if a.State != SlangRetired {
		t.Fatalf("old active should retire, got %s", a.State)
	}
	b, _ := s.SlangByPhrase("试用B")
	if b.State != SlangRetired {
		t.Fatalf("long trial should retire, got %s", b.State)
	}
}

func TestApplySessionDelta_memes(t *testing.T) {
	s := newTestStore(t)
	d := SessionDelta{
		Summary: "s",
		Memes: []SlangDelta{
			{Phrase: "典中典", Meaning: "经典中的经典", Example: "太典了",
				SourceQuote: "典中典", Confidence: 0.9},
			{Phrase: "疑似有点", Meaning: "x", Confidence: 0.5},
		},
	}
	if err := s.ApplySessionDelta("2026-09-07", d); err != nil {
		t.Fatal(err)
	}
	hi, _ := s.SlangByPhrase("典中典")
	if hi.State != SlangActive {
		t.Fatalf("high conf should be active, got %s", hi.State)
	}
	lo, _ := s.SlangByPhrase("疑似有点")
	if lo.State != SlangCandidate {
		t.Fatalf("low conf should be trial, got %s", lo.State)
	}
}

func TestExtractMemeNote(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"这张图是个猫, 好可爱\n【梗图】打工人的命也是命", "打工人的命也是命"},
		{"就是普通照片", ""},
		{"【梗图】意思太长了超了十五个字的标记啊哈哈哈哈", "意思太长了超了十五个字的标记啊…"},
	}
	for _, c := range cases {
		if got := extractMemeNote(c.in); got != c.want {
			t.Fatalf("extractMemeNote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ——— 注入: 段 7 黑话表 + 段 3.6 接住 ———

func TestBuildSystem_injectsSlangAndHit(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜", Description: "测试"}
	in := DialogInput{
		UserText: "我真的麻了",
		Slangs: []Slang{
			{Phrase: "麻了", Meaning: "累到无语", Example: "今天加班麻了"},
		},
		SlangHit: "麻了",
	}
	sys := d.buildSystem(in, p, "ja")
	if !strings.Contains(sys, "【你会的梗】") || !strings.Contains(sys, "「麻了」") {
		t.Fatalf("段7 missing: %s", sys)
	}
	if !strings.Contains(sys, "你会的梗「麻了」") {
		t.Fatalf("段3.6 接住 missing: %s", sys)
	}
}

func TestBuildSystem_noSlangSegment(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜", Description: "测试"}
	sys := d.buildSystem(DialogInput{UserText: "hi"}, p, "ja")
	if strings.Contains(sys, "【你会的梗】") {
		t.Fatalf("empty slang list should not inject segment: %s", sys)
	}
}
