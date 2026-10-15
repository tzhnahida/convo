package convo

import (
	"testing"
	"time"
)

// ——— §6.3 判据③: 冲突 ———

// TestCollectValueHits_ConflictNeedsFullSignals **V8**。
//
// 反面: 判据③ 改用裸负面词表 → **一个全程吐槽的群**被判成冲突 → 红。
// 设计稿的括注是「吵架劝和」—— 语义是"确实在吵", 不是"出现了负面词"。
// 裸词表会让它在别人吐槽"这个 bug 真烦"的时候插嘴, 那不是劝架, 是听不懂人话。
func TestCollectValueHits_ConflictNeedsFullSignals(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	now := time.Now()
	// 同一**个人**连发 8 条抱怨 —— 负面词密度不低, 但**没有对呛、没有互 @、间隔不骤短**。
	for i := 0; i < 8; i++ {
		if _, err := e.Store.InsertMessageFull("user", "qq", "text", "这个 bug 真烦 烦死了",
			MsgMeta{Origin: OriginUser, SceneKey: "group:1", SenderUIN: "10001"}); err != nil {
			t.Fatal(err)
		}
	}
	_ = now
	in, _ := e.collectValueHits(SceneMsg{SceneKey: "group:1", UIN: "10001", Text: "这个 bug 真烦"}, false)
	if in.Conflict {
		t.Fatal("一个全程吐槽的群被判成了冲突 —— 判据③ 用了裸负面词表?")
	}
	// ⚠️ **判过了就必须进分母** —— 与"读不到消息窗口"是两件事。
	// 写成 HasConflict=false 的话, 判据缺席的折算会把门槛压低,
	// 而那意味着"这个判据我们没算" —— 一句谎话。
	if !in.HasConflict {
		t.Fatal("判过了却没进分母 (HasConflict=false) —— 那会被当成「没有这个判据」而折算门槛")
	}
}

// TestCollectValueHits_NoWindowIsAbsent 反面: 读不到消息窗口 → **缺席** (HasConflict=false)。
// 缺席 = 不进分母。这与"判过了没冲突"必须分得开。
func TestCollectValueHits_NoWindowIsAbsent(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	// 场景里一条消息都没有 → 窗口不足 → 判据缺席。
	in, _ := e.collectValueHits(SceneMsg{SceneKey: "group:empty", UIN: "10001", Text: "喂"}, false)
	if in.HasConflict {
		t.Fatal("没有消息窗口却把判据算成了「已判」 —— 它应当是缺席(不进分母)")
	}
}

// TestCollectValueHits_ConflictIsSceneScoped **判据③ 必须只看本场景**。
//
// 反面: 用全局 RecentMessages → 拿别的群的争吵来判断这个群 → 红。
// 这条与 §6.10.3 的跨场景隔离是同一条红线, 只是发生在决策层而不是注入层。
func TestCollectValueHits_ConflictIsSceneScoped(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	// 在 A 群灌一堆对呛 (a/b 严格交替 + 负面词 + 秒回)。
	base := time.Now()
	for i := 0; i < 12; i++ {
		who := "10001"
		if i%2 == 1 {
			who = "20002"
		}
		if _, err := e.Store.InsertMessageFull("user", "qq", "text", "你有病吧 滚",
			MsgMeta{Origin: OriginUser, SceneKey: "group:A", SenderUIN: who}); err != nil {
			t.Fatal(err)
		}
	}
	_ = base
	// 在**干净的 B 群**里判 —— 不许受 A 群影响。
	in, _ := e.collectValueHits(SceneMsg{SceneKey: "group:B", UIN: "30003", Text: "今天天气不错"}, false)
	if in.Conflict {
		t.Fatal("B 群被判成了冲突 —— 判据③ 读了全局消息 (跨场景污染)")
	}
	// ⚠️ **这条才是真正抓得住 M3 的断言。**
	// 我第一版只断言 `in.Conflict == false` —— 但 A 群那 12 条**本身就不构成冲突**
	// (只命中 4 个信号里的 1 个: 对呛; 负面密度没"上升"、没有互 @、间隔没骤短),
	// 所以"读了全局消息"这个改法**照样通过**。
	// 冲突判定有很多门槛, 构造一个"换个场景就会误判"的窗口非常难 ——
	// 而"窗口里到底有几条消息"是直接的、不依赖门槛的。
	if in.ConflictWindow != 0 {
		t.Fatalf("B 群的窗口里有 %d 条消息 —— 判据③ 读了别的场景的消息", in.ConflictWindow)
	}
}
