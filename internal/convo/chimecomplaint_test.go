package convo

import (
	"testing"

	"github.com/tzhnahida/convo/internal/clock"
)

// ─────────────────────────────────────────────────────────────
// 「被群友说了就收敛」—— 插话功能**唯一的安全阀**
//
// chime.go 自己写着: 「**被群友说了就稳定在 1%**…**不自动恢复**…
// 要放开只能 owner 手动 ClearChimeComplaint。**这是这个功能唯一的安全阀**」。
//
// ⚠️ 而 `complained_ts` **从来没有人写过** —— 读侧 (`ChimeComplained` ←
// `DecideChime.Complained`) 全接好了, 写侧零调用。
//
// 后果不是"少了个功能", 而是: **插话这条路已经在生产上跑**
// (`chimewire.go` 经 `value.go:445`), 而被嫌弃之后**它照样插** ——
// 社交损伤不可逆, 正是排障记录 §1.1 那个形态。
// ─────────────────────────────────────────────────────────────

const chimeTestScene = "group:1034712035"

// chimeCfg 显式白名单。
//
// ⚠️ **不能用 `DefaultConfig().Group`**: 它的白名单是空的, 而 `SceneAllowed` 是
// fail-closed (`WhitelistAllows` 要求 Enabled 且群号在白名单里) ——
// 于是裁决在**白名单那一关**就早退了, 根本走不到插话收敛那一段,
// 现象是"我明明接了线却什么都没发生"。
// (热力那类用例能过, 是因为它入账在**白名单之前**。位置不同, 命运不同。)
func chimeCfg() GroupConfig {
	return GroupConfig{Enabled: true, Whitelist: []string{"1034712035"}}
}

// chimeSay 让一条群消息走完整条裁决。
func chimeSay(t *testing.T, e *Engine, uin, text string, atSelf bool) {
	t.Helper()
	var at []string
	if atSelf {
		at = []string{"3487037837"}
	}
	e.GateGroupMessage(SceneMsg{
		SceneKey: chimeTestScene, UIN: uin, Text: text, AtUINs: at, TS: clock.Now(),
	}, SelfIdentity{UIN: "3487037837"}, chimeCfg())
}

// TestChimeComplaint_TowardsSelfLands
//
// 正面: 被 @ 着说「别刷屏」→ 必须落库, 而且**下一次**它是收敛的。
func TestChimeComplaint_TowardsSelfLands(t *testing.T) {
	e := gateEngine(t)
	if err := EnsureChimeSchema(e.Store.DB); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.Store.ChimeComplained(e.ownerKey(), chimeTestScene); got {
		t.Fatal("一开始就说被抱怨过了")
	}
	chimeSay(t, e, "111", "别刷屏了", true)
	if got, _ := e.Store.ChimeComplained(e.ownerKey(), chimeTestScene); !got {
		t.Fatal("**被 @ 着说「别刷屏」却没有落库** —— 那条安全阀是空的")
	}
	// ⚠️ **落库不是内存**: 重启之后还得收敛。
	// (chime.go 原话: 「重启会丢掉内存, 而"重启之后又变得话多"正是群友最会注意到的反复」。)
	e2 := &Engine{Store: e.Store, Cfg: DefaultConfig()}
	if got, _ := e2.Store.ChimeComplained(e2.ownerKey(), chimeTestScene); !got {
		t.Fatal("换个 Engine 实例就读不到了 —— 那说明写的是内存不是库")
	}
}

// TestChimeComplaint_NotTowardsSelfDoesNotLand (⚠️ 这条比正面重要)
//
// ⚠️ chime.go 原话: 「只看词的话, 群友 A 对群友 B 说「你话真多」会把它的插话率打到 1%
// —— 而它什么都没做错, **且没有人会知道为什么它突然不说话了**」。
//
// 这就是这条安全阀**最危险的失效方向**: 不是"该收敛没收敛",
// 而是"**没人说它的时候它自我禁言**"。
func TestChimeComplaint_NotTowardsSelfDoesNotLand(t *testing.T) {
	e := gateEngine(t)
	if err := EnsureChimeSchema(e.Store.DB); err != nil {
		t.Fatal(err)
	}
	// ① 群友之间互喷: 没有 @ 它。
	chimeSay(t, e, "111", "你话真多", false)
	if got, _ := e.Store.ChimeComplained(e.ownerKey(), chimeTestScene); got {
		t.Fatal("**群友之间的「你话真多」被算到它头上了** —— 它会在没人说它的时候自我禁言")
	}
	// ② 在跟它说话, 但没抱怨。
	chimeSay(t, e, "222", "你在吗", true)
	if got, _ := e.Store.ChimeComplained(e.ownerKey(), chimeTestScene); got {
		t.Fatal("只是问了一句「你在吗」也被当成抱怨")
	}

	// ③ 反面锚点: 同一句话 @ 着它说, **必须**落库 ——
	// 证明上面两条不是"整个机制没接"。
	chimeSay(t, e, "333", "你话真多", true)
	if got, _ := e.Store.ChimeComplained(e.ownerKey(), chimeTestScene); !got {
		t.Fatal("@ 着它说「你话真多」却没落库 —— 洞修过头了")
	}
}

// TestChimeComplaint_OnlyGroupScenes
//
// ⚠️ 私聊没有"插话"这回事 —— 收敛只对群场景有意义。
func TestChimeComplaint_OnlyGroupScenes(t *testing.T) {
	e := gateEngine(t)
	if err := EnsureChimeSchema(e.Store.DB); err != nil {
		t.Fatal(err)
	}
	// 私聊那条路不走 GateGroupMessage, 所以这里直接钉存储层:
	// 一个非群 key 上**不该**有抱怨记录。
	if got, _ := e.Store.ChimeComplained(e.ownerKey(), DefaultSceneKey); got {
		t.Fatal("私聊场景上出现了插话抱怨")
	}
	// 而群场景写完之后, 私聊那条仍然干净 (互不串)。
	chimeSay(t, e, "111", "别插嘴", true)
	if got, _ := e.Store.ChimeComplained(e.ownerKey(), DefaultSceneKey); got {
		t.Fatal("群里的抱怨串到私聊场景上去了")
	}
}

// TestChimeComplaint_DecideChimeSeesIt
//
// ⚠️ **接线尺子**: 光落库不算数 —— 判插话的那一侧必须真的读到它。
func TestChimeComplaint_DecideChimeSeesIt(t *testing.T) {
	e := gateEngine(t)
	if err := EnsureChimeSchema(e.Store.DB); err != nil {
		t.Fatal(err)
	}
	// ⚠️ **掷 5 而不是 0** —— 收敛是"10% 降到 **1%**", 不是"降到 0"。
	// 掷 0 在两边都命中, 那样这条用例什么都测不出来(我第一版就是 0, 白红了一次)。
	// 掷 5: 10% 时命中, 1% 时不命中 —— 恰好卡在中间。
	in := ChimeInput{Roll: 5, WakingElapsed: 1.0}
	if d := DecideChime(in); !d.Speak {
		t.Fatal("没被抱怨过、掷中 5 却不插话 —— 夹具不对")
	}
	chimeSay(t, e, "111", "别刷屏", true)
	complained, err := e.Store.ChimeComplained(e.ownerKey(), chimeTestScene)
	if err != nil {
		t.Fatal(err)
	}
	in.Complained = complained
	if d := DecideChime(in); d.Speak {
		t.Fatal("**被抱怨过之后掷中 5 仍然插话** —— 收敛没传到判插话那一侧")
	}
}
