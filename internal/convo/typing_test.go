package convo

import (
	"strings"
	"testing"
	"time"
)

// ⚠️ **本批的核心主张**: 延迟与字数挂钩。
func TestTypingDelay_GrowsWithLength(t *testing.T) {
	short := TypingDelayFor("嗯", 1, 0)
	long := TypingDelayFor(strings.Repeat("字", 100), 1, 0)
	if long <= short {
		t.Fatalf("长文应当等更久: short=%v long=%v", short, long)
	}
	// 「嗯」≈ 2.18s —— 秒回不像人。
	if short < 2*time.Second {
		t.Fatalf("基础反应不许是 0, got %v", short)
	}
}

// ⚠️ **上限是必须的**: 没有它, 500 字要等 90 秒 —— 那不是真人节奏, 是卡死。
func TestTypingDelay_HasHardCap(t *testing.T) {
	got := TypingDelayFor(strings.Repeat("字", 5000), 1, 0)
	if got > 26*time.Second {
		t.Fatalf("超长文本没有卡上限: %v", got)
	}
	// 上限**在 factor 之后**卡 —— 否则疲劳日能把 25 秒推成 30 秒。
	tired := TypingDelayFor(strings.Repeat("字", 5000), 2.5, 0)
	if tired > 26*time.Second {
		t.Fatalf("上限应当在 factor 之后再卡: %v", tired)
	}
}

func TestTypingDelay_EmptyIsZero(t *testing.T) {
	if TypingDelayFor("   ", 1, 0) != 0 {
		t.Fatal("没有要打的东西不该等")
	}
}

// ⚠️ factor < 1 只可能来自调用方算错 —— 不许让它把延迟压到 0。
func TestTypingDelay_FactorBelowOneIsClamped(t *testing.T) {
	got := TypingDelayFor("你好", 0.01, 0)
	want := TypingDelayFor("你好", 1, 0)
	if got != want {
		t.Fatalf("factor<1 应当按 1 处理: got=%v want=%v", got, want)
	}
}

// ⚠️ 抖动的**方向**要对: 真人不会每次都精确一样, 但均值不该被拉偏。
func TestTypingDelay_JitterDoesNotBias(t *testing.T) {
	base := TypingDelayFor("这是一句测试用的话", 1, 0)
	var sum time.Duration
	n := 40
	for i := 0; i < n; i++ {
		sum += TypingDelayFor("这是一句测试用的话", 1, Dice(31)-15)
	}
	avg := sum / time.Duration(n)
	if avg < base*8/10 || avg > base*12/10 {
		t.Fatalf("抖动把均值拉偏了: base=%v avg=%v", base, avg)
	}
}

// ⚠️ 疲劳/心情必须**能**让它变慢 —— 否则那条 factor 链白接了。
func TestTypingDelay_FactorSlowsIt(t *testing.T) {
	fast := TypingDelayFor("这是一句测试用的话", 1, 0)
	slow := TypingDelayFor("这是一句测试用的话", 1.25, 0)
	if slow <= fast {
		t.Fatalf("factor 没有生效: %v vs %v", fast, slow)
	}
}
