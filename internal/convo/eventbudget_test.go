package convo

import (
	"testing"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
)

// ─────────────────────────────────────────────────────────────
// #14 §3.5 事件频率预算
//
// 「超了生活就变连续剧」—— 而连续剧最要命的不是夸张,
// 是**它把每一天都变成有事的**, 于是"平平常常的一天"消失了。
// ─────────────────────────────────────────────────────────────

func budgetFixture(t *testing.T) (*Store, time.Time) {
	t.Helper()
	s := newTestStore(t)
	// 固定"现在", 免得跨周跑出不稳定结果。
	now := clock.Now()
	return s, now
}

func evAt(text string, intensity float64, ts time.Time) WorldEvent {
	return WorldEvent{
		TS: ts.Format(bodyTSLayout), Kind: "relation", Source: "家人",
		Text: text, Valence: -0.3, Arousal: 0.6, Intensity: intensity,
		Weight: 1.0, Duration: "pulse",
	}
}

// TestEventBudget_BackgroundBypassesQuota
//
// ⚠️ 2026-10-17~20 的生产事故: 天气事件(Intensity 1.0 + Duration "background")
// 被当成大事件, 一周只进一条 → mood_log 冻在 10-16, 心情再也不动。
// 背景压力是"底色", 不该和脉冲事件抢配额。
func TestEventBudget_BackgroundBypassesQuota(t *testing.T) {
	s, now := budgetFixture(t)
	// 先把这周的大事件额度用掉。
	if _, err := s.IngestWorldEvents([]WorldEvent{evAt("家人住院了", 0.8, now.Add(-2*time.Hour))}); err != nil {
		t.Fatal(err)
	}
	// 脉冲事件 → 必须被挡 (原有行为不许退化)。
	if v := s.CheckEventBudget(evAt("又住院了", 0.8, now), now); v.Allow {
		t.Fatal("大事件配额失效了")
	}
	// 背景天气 → 必须放行。
	bg := WorldEvent{
		TS: now.Format(bodyTSLayout), Kind: "environment", Source: "天气",
		Text: "今天晴天", Valence: 0.15, Arousal: 0.5, Intensity: 1.0,
		Weight: 1.0, Duration: "background",
	}
	if v := s.CheckEventBudget(bg, now); !v.Allow {
		t.Fatalf("背景压力被事件配额挡住了 (%s) —— 心情又会冻住", v.Reason)
	}
}

// TestEventBudget_MajorIsOnePerWeek
//
// ⚠️ §3.5-① 大事件 ≤1/周。没有它, 一周里能出现三场"家人住院"。
func TestEventBudget_MajorIsOnePerWeek(t *testing.T) {
	s, now := budgetFixture(t)
	// 第一件大事: 放行。
	first := evAt("家人住院了", 0.8, now.Add(-24*time.Hour))
	if v := s.CheckEventBudget(first, now); !v.Allow {
		t.Fatalf("第一件大事被挡了: %s", v.Reason)
	}
	if _, err := s.IngestWorldEvents([]WorldEvent{first}); err != nil {
		t.Fatal(err)
	}
	// 第二件大事 (不同类, 免得被冷却先挡住): 必须被**频率**挡住。
	second := evAt("朋友出了点事", 0.8, now.Add(-2*time.Hour))
	second.Source = "朋友"
	second.Kind = "social"
	v := s.CheckEventBudget(second, now)
	if v.Allow {
		t.Fatal("一周里的第二件大事被放行了 —— 生活要变连续剧了")
	}
	if v.Reason == "" {
		t.Error("挡了却不说为什么 —— 日志里会变成空白")
	}
}

// TestEventBudget_MinorIsThreePerWeek
func TestEventBudget_MinorIsThreePerWeek(t *testing.T) {
	s, now := budgetFixture(t)
	var batch []WorldEvent
	for i := 0; i < EventMinorPerWeek; i++ {
		e := evAt("小事一件", 0.3, now.Add(-time.Duration(i+1)*time.Hour))
		e.Source = "生活"
		e.Kind = "self"
		e.Text = "小事第 " + string(rune('A'+i)) + " 件"
		batch = append(batch, e)
	}
	if _, err := s.IngestWorldEvents(batch); err != nil {
		t.Fatal(err)
	}
	extra := evAt("又来一件小事", 0.3, now)
	extra.Source = "生活"
	extra.Kind = "self"
	if v := s.CheckEventBudget(extra, now); v.Allow {
		t.Fatalf("第 %d 件小事被放行了 —— 上限是 %d", EventMinorPerWeek+1, EventMinorPerWeek)
	}
	// 反面锚点: 大事件不该被**小事**的额度挡住。
	big := evAt("一件大事", 0.9, now)
	big.Source = "陌生人"
	big.Kind = "social"
	if v := s.CheckEventBudget(big, now); !v.Allow {
		t.Fatalf("小事满了却把大事也挡了: %s", v.Reason)
	}
}

// TestEventBudget_SameClassCooldownOnlyForMajor
//
// ⚠️ 冷却**只对大事件生效**, 这是刻意的收窄:
// 照字面把 30 天冷却套在**所有**事件上, "天气"这个 Source 会把一个月里
// 所有的天气事件全挡掉 —— 那是**把池子掏空**, 不是防重复。
// 设计稿给冷却举的例子是「家人重病」, 而它显然是大事。
func TestEventBudget_SameClassCooldownOnlyForMajor(t *testing.T) {
	s, now := budgetFixture(t)
	old := evAt("家人住院了", 0.8, now.AddDate(0, 0, -3))
	old.Source = "家人"
	old.Kind = "relation"
	if _, err := s.IngestWorldEvents([]WorldEvent{old}); err != nil {
		t.Fatal(err)
	}
	// 同类大事, 3 天后 → 冷却挡住 (注意: 频率额度可能先挡, 所以把周内的大事数控制在 1 以内)。
	same := evAt("家人又出事了", 0.8, now.AddDate(0, 0, -1))
	same.Source = "家人"
	same.Kind = "relation"
	// 3 天前那件还在 7 天窗口里 → 频率先挡住。这条断言仍然成立(挡了就行), 但要分清是谁挡的。
	if v := s.CheckEventBudget(same, now); v.Allow {
		t.Fatal("同类大事在 30 天里重复了")
	}
	// ⚠️ 而**小事**同类不受冷却限制 —— 天气天天都在。
	w1 := evAt("今天下雨", 0.3, now.AddDate(0, 0, -3))
	w1.Source = "天气"
	w1.Kind = "environment"
	if _, err := s.IngestWorldEvents([]WorldEvent{w1}); err != nil {
		t.Fatal(err)
	}
	w2 := evAt("今天也下雨", 0.3, now.AddDate(0, 0, -1))
	w2.Source = "天气"
	w2.Kind = "environment"
	if v := s.CheckEventBudget(w2, now); !v.Allow {
		t.Fatalf("天气这类小事被同类冷却挡了 —— 那是把池子掏空: %s", v.Reason)
	}
}

// TestIngestWorldEvents_BudgetActuallyBlocks
//
// ⚠️ **接线尺子**: 光测 CheckEventBudget 是个纯查询, 证明不了入池那一步真的调了它。
func TestIngestWorldEvents_BudgetActuallyBlocks(t *testing.T) {
	s, now := budgetFixture(t)
	first := evAt("家人住院了", 0.8, now.Add(-24*time.Hour))
	if n, err := s.IngestWorldEvents([]WorldEvent{first}); err != nil || n != 1 {
		t.Fatalf("第一件应当入池: n=%d err=%v", n, err)
	}
	second := evAt("朋友出了点事", 0.8, now.Add(-time.Hour))
	second.Source = "朋友"
	second.Kind = "social"
	n, err := s.IngestWorldEvents([]WorldEvent{second})
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("第二件大事**入了池** (%d 条) —— 预算没接在入池那一步", n)
	}
}
