package convo

import (
	"strings"
	"testing"

	"github.com/tzhnahida/convo/internal/clock"
)

// TestLearnSharePipeline_EndToEnd
//
// ⚠️ 这条测的是**管线**, 不是判据 (判据有自己的用例)。这里的失败模式是:
// **判据全对, 而没有人在跑它** —— share_week 从上线起一次都没被 +1 过,
// 于是设计稿 L153 那句「学到的 90% 永远不说出来」是**结构性**的, 不是行为选择。
func TestLearnSharePipeline_EndToEnd(t *testing.T) {
	e := gateEngine(t)
	e.UserID = 3487037837
	for _, ensure := range []func() error{
		func() error { return EnsureLearnSchema(e.Store.DB) },
		func() error { return EnsureTasteSchema(e.Store.DB) },
		func() error { return EnsureHeatSchema(e.Store) },
	} {
		if err := ensure(); err != nil {
			t.Fatal(err)
		}
	}
	// 骰子钉死 —— 与 gateEngine 同一条纪律: 不钉的话这条用例会**随机**红。
	old := Dice
	Dice = func(int) int { return 0 } // 0 < ShareMinRoll → 命中
	t.Cleanup(func() { Dice = old })

	now := clock.Now()
	owner := e.Store.OwnerKey()
	// ① 一条从没带出去过的学习内容。
	if _, err := e.Store.SaveLearned(LearnedItem{
		Kind: LearnFact, Content: "海獭睡觉会手拉手", TopicKey: "动物",
	}); err != nil {
		t.Fatal(err)
	}
	// ② 他**真的爱聊**这个话题 (Seen ≥ TasteMinSeen, 接话率 3/5)。
	for i := 0; i < 5; i++ {
		if err := RecordTaste(e.Store.DB, owner, "动物", i < 3); err != nil {
			t.Fatal(err)
		}
	}
	// ③ 热力过线 (ShareMinHeat) ④ 最近有互动 (够得到)。
	if err := e.Store.SaveHeat(HeatState{SceneKey: DefaultSceneKey, Value: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.InsertMessageEx("user", "qq", "text", "在忙吗", "user", DefaultSceneKey); err != nil {
		t.Fatal(err)
	}

	topic, id, ok := e.learnShareTopic(now, StateFree)
	if !ok || id <= 0 {
		t.Fatal("四条件都满足却没挑出可分享的内容 —— 管线没接上")
	}
	if topic.Kind != "learn" {
		t.Fatalf("话题种类不对: %q —— 段 7.3 会走成普通话题的措辞", topic.Kind)
	}
	for _, want := range []string{"不要解释", "不要当事实断言"} {
		if !strings.Contains(topic.Text, want) {
			t.Fatalf("少了禁止条款 %q: %s", want, topic.Text)
		}
	}
	// ⑤ 发出去之后记账: 带出次数 + **两道**周计数 (契约 S11: 合计 ≤2/周)。
	week := WeekStartOf(clock.Today())
	e.MarkLearnSharedAfter(id, now)
	if n, _ := e.Store.CounterGet("share_week", week); n != 1 {
		t.Fatalf("share_week 应当被 +1 —— 这正是「从来没发生过」的那一步, 实得 %d", n)
	}
	if n, _ := e.Store.CounterGet("learn_share_week", week); n != 1 {
		t.Fatalf("learn_share_week 实得 %d", n)
	}
	// ⑥ 说过的不再说 (讲两遍就是念稿)。
	if _, _, ok := e.learnShareTopic(now, StateFree); ok {
		t.Fatal("同一条内容不该被分享两次")
	}
}

// TestLearnShareLine_KeepsTheThreeBans
//
// ⚠️ 三条禁止条款缺一条, 这句话就会变成播报 (与 SparkLine 同规)。
func TestLearnShareLine_KeepsTheThreeBans(t *testing.T) {
	line := LearnShareLine(LearnedItem{Content: "海獭睡觉会手拉手"})
	for _, want := range []string{"不要解释", "不要当事实断言", "我最近在研究"} {
		if !strings.Contains(line, want) {
			t.Fatalf("少了 %q: %s", want, line)
		}
	}
	if got := LearnShareLine(LearnedItem{Content: "  "}); got != "" {
		t.Fatalf("空内容应当返回空串 (调用方据此不出题): %q", got)
	}
}
