package convo

import (
	"fmt"
	"testing"
)

// 2026-10-24: 库里有 100+ 条梗时, 命中检测只扫 top-50 ⇒ 一半的梗永远匹配不到。
func TestFindSlangHit_ScansBeyondTopFifty(t *testing.T) {
	s := newTestStore(t)
	// 先塞 60 条高置信的陪跑词, 把目标挤到 50 名之外。
	for i := 0; i < 60; i++ {
		if _, err := s.UpsertSlang(SlangDelta{Phrase: fmt.Sprintf("陪跑梗%02d", i), Meaning: "陪跑", Confidence: 1}, "user"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.UpsertSlang(SlangDelta{Phrase: "显眼包", Meaning: "爱出风头的人", Confidence: 0.1}, "user"); err != nil {
		t.Fatal(err)
	}
	hits, err := s.FindSlangHit("你真是个显眼包")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Phrase != "显眼包" {
		t.Fatalf("排在第 50 名之外的梗没被接住: %+v", hits)
	}
}

// 注入轮换要稳定: 同一天同短语同分, 隔天才变。
func TestDayJitter(t *testing.T) {
	a := dayJitter("破防", "2026-10-24")
	b := dayJitter("破防", "2026-10-24")
	if a != b {
		t.Fatalf("同一天抖动不稳定: %d vs %d", a, b)
	}
	if a < 0 || a >= 1000 {
		t.Fatalf("抖动越界: %d", a)
	}
	c := dayJitter("破防", "2026-10-25")
	if c == a {
		t.Fatalf("换天没有轮换 (两份都是 %d)", a)
	}
}

// TopSlang 在库很大时也应当给出 12 条 (轮换窗口)。
func TestTopSlang_LimitRespectedWithBigPool(t *testing.T) {
	s := newTestStore(t)
	if _, err := SeedCommonMemes(s); err != nil {
		t.Fatal(err)
	}
	got, err := s.TopSlang(12)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 12 {
		t.Fatalf("应当给 12 条, 得到 %d", len(got))
	}
}
