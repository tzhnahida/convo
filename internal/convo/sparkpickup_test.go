package convo

import (
	"strings"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────
// §2.6.5 被接住之后 —— "这才是迸发的真正机制"
//
// ⚠️ `TouchSpark` 原来**零调用**: 灵光被说出来之后, 有没有人接, 系统不知道。
// 而设计稿把"被接住"排在"产生"之上: 被接住 → 这个方向被强化 → 它会在这个方向上想更多。
// ─────────────────────────────────────────────────────────────

func pickupEngine(t *testing.T) *Engine {
	t.Helper()
	e := gateEngine(t)
	if err := EnsureSparkSchema(e.Store.DB); err != nil {
		t.Fatal(err)
	}
	return e
}

// saidSpark 落一条**已经说出去**的灵光, 并返回它的 id。
func saidSpark(t *testing.T, e *Engine, content, talkKey string, saidAgo time.Duration) int64 {
	t.Helper()
	now := time.Now()
	id, err := e.Store.SaveSpark(Spark{
		Kind: SparkIdea, Content: content, TalkKey: talkKey,
		Source:    SparkSource{Kind: "dream", RefID: "m", HasRef: true},
		HeatDelta: 0.5, BornTS: now.Format(bodyTSLayout),
		SaidTS: now.Add(-saidAgo).Format(bodyTSLayout),
	})
	if err != nil || id == 0 {
		t.Fatalf("落库失败: id=%d err=%v", id, err)
	}
	return id
}

// TestNoteSparkPickup_BumpsWhenTheTalkKeyShowsUp
//
// ⚠️ 判据只有一条: **用户这句话里出现了那条灵光的 talk_key**。
// 刻意不做语义推断 —— 猜错的代价是"在一个其实没被接住的方向上越想越多"。
func TestNoteSparkPickup_BumpsWhenTheTalkKeyShowsUp(t *testing.T) {
	e := pickupEngine(t)
	id := saidSpark(t, e, "会唱歌的电阻", "合成器", time.Minute)
	before, _ := e.Store.SparkByID(id)

	// 没提到那个方向的 → 一动不动(也**不扣** —— 没人接就是自然衰减)。
	e.noteSparkPickup("今天天气不错", time.Now())
	mid, _ := e.Store.SparkByID(id)
	if mid.HeatDelta != before.HeatDelta {
		t.Fatalf("没提到那个方向却动了热力: %.2f → %.2f", before.HeatDelta, mid.HeatDelta)
	}
	// 提到了 → 加 SparkPickupDelta。
	e.noteSparkPickup("你那个合成器的想法挺有意思", time.Now())
	after, _ := e.Store.SparkByID(id)
	if after.HeatDelta <= before.HeatDelta {
		t.Fatalf("接住了却没加: %.2f → %.2f", before.HeatDelta, after.HeatDelta)
	}
	if !strings.Contains(after.LastTouchTS, "-") {
		t.Fatalf("last_touch_ts 没写上: %q", after.LastTouchTS)
	}
}

// TestNoteSparkPickup_OnlyOncePerSaying
//
// ⚠️ 用户连说三句带这个关键词的话, **不该**被算成接住了三次。
func TestNoteSparkPickup_OnlyOncePerSaying(t *testing.T) {
	e := pickupEngine(t)
	id := saidSpark(t, e, "会唱歌的电阻", "合成器", time.Minute)
	for i := 0; i < 3; i++ {
		e.noteSparkPickup("合成器那个我一直在想", time.Now())
	}
	sp, _ := e.Store.SparkByID(id)
	if sp.HeatDelta > 0.5+SparkPickupDelta+0.001 {
		t.Fatalf("连说三句被算成了三次接住: %.2f", sp.HeatDelta)
	}
}

// TestNoteSparkPickup_OnlyForSparksThatWereSaid
//
// ⚠️ "被接住"只能发生在**说出去之后** —— 还没说过的灵光, 用户不可能接。
func TestNoteSparkPickup_OnlyForSparksThatWereSaid(t *testing.T) {
	e := pickupEngine(t)
	id, err := e.Store.SaveSpark(Spark{
		Kind: SparkIdea, Content: "没说过的", TalkKey: "合成器",
		Source: SparkSource{Kind: "dream", RefID: "m", HasRef: true}, HeatDelta: 0.5,
	})
	if err != nil || id == 0 {
		t.Fatal(err)
	}
	e.noteSparkPickup("合成器那个想法不错", time.Now())
	sp, _ := e.Store.SparkByID(id)
	if sp.HeatDelta != 0.5 {
		t.Fatalf("还没说出去的灵光被算成接住了: %.2f", sp.HeatDelta)
	}
}

// TestNoteSparkPickup_OutsideTheWindowDoesNotCount
//
// ⚠️ 隔了一天再提起来, 那多半是它自己又想起来, 不是"被接住"。
func TestNoteSparkPickup_OutsideTheWindowDoesNotCount(t *testing.T) {
	e := pickupEngine(t)
	id := saidSpark(t, e, "会唱歌的电阻", "合成器", SparkPickupWindow+time.Hour)
	e.noteSparkPickup("合成器那个想法不错", time.Now())
	sp, _ := e.Store.SparkByID(id)
	if sp.HeatDelta != 0.5 {
		t.Fatalf("超出窗口的还说被接住了: %.2f", sp.HeatDelta)
	}
}

// TestReinforcedTopics_ExcludesTheUnreinforced
//
// ⚠️ 刚生出来的(0.5)与冷掉的都不算"被强化过" —— 判据是**高过新生基线**。
func TestReinforcedTopics_ExcludesTheUnreinforced(t *testing.T) {
	e := pickupEngine(t)
	// 一条没被接住的(基线 0.5)
	if _, err := e.Store.SaveSpark(Spark{Kind: SparkIdea, Content: "新的", TalkKey: "植物",
		Source: SparkSource{Kind: "dream", RefID: "m", HasRef: true}, HeatDelta: 0.5}); err != nil {
		t.Fatal(err)
	}
	if got := e.Store.ReinforcedTopics(3); len(got) != 0 {
		t.Fatalf("没被接住的被算成了强化方向: %v", got)
	}
	// 一条被接住的
	id := saidSpark(t, e, "会唱歌的电阻", "合成器", time.Minute)
	e.noteSparkPickup("合成器那个我在想", time.Now())
	if _, ok := e.Store.SparkByID(id); !ok {
		t.Fatal("夹具坏了")
	}
	got := e.Store.ReinforcedTopics(3)
	if len(got) != 1 || got[0] != "合成器" {
		t.Fatalf("强化方向不对: %v", got)
	}

	// ⚠️ **而同一条方向只出现一次**(它可能有好几条灵光都用了这个 key)。
	if _, err := e.Store.SaveSpark(Spark{Kind: SparkIdea, Content: "又一个", TalkKey: "合成器",
		Source: SparkSource{Kind: "dream", RefID: "m", HasRef: true}, HeatDelta: 1.2}); err != nil {
		t.Fatal(err)
	}
	if got := e.Store.ReinforcedTopics(3); len(got) != 1 {
		t.Fatalf("同一个方向出现了两次: %v", got)
	}
}

// TestReinforcedLine_ReachesTheGenerationPrompt
//
// ⚠️ **这是"想更多"的落点尺子。**
// 没有它, TouchSpark 加的那点热力只是一个**没人看的数字** ——
// 而设计稿要的是它会在这个方向上想更多。
func TestReinforcedLine_ReachesTheGenerationPrompt(t *testing.T) {
	p := &PersonaProfile{Name: "小夜"}
	d := SparkBurstDecision{Source: SparkSource{Kind: "dream", HasRef: true}}
	// 没有强化方向 → 提示词里一个字都不加。
	plain := sparkSystemPrompt(p, d, nil)
	if strings.Contains(plain, "总往这些方向上想") {
		t.Fatal("没有强化方向却写了那一行")
	}
	// 有 → 那一行必须出现, 而且带着方向词。
	withDir := sparkSystemPrompt(p, d, []string{"合成器", "星空"})
	if !strings.Contains(withDir, "合成器") || !strings.Contains(withDir, "星空") {
		t.Fatalf("强化方向没进提示词:\n%s", withDir)
	}
	if !strings.Contains(withDir, "别重复已经说过的") {
		t.Fatal("那一行少了「别重复」的护栏 —— 它会开始复读")
	}
	// ⚠️ 空输入 → 空行 (零值 = 未提供, 不是"没有方向"这句话)。
	if ReinforcedLine(nil) != "" {
		t.Fatal("空输入应当返回空串")
	}
}
