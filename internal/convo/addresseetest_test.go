package convo

import (
	"strings"
	"testing"

	"github.com/tzhnahida/VocabLock/internal/clock"
)

// ─────────────────────────────────────────────────────────────
// §6.1「故障与判不出来**必须分得开**」
//
// 规格原话:
//
//   > 这两条不冲突, 是两件事。实现者会用「判不出来 → 沉默」
//   > 顺手把读库失败也吞掉 —— 那就是**纪律 #3 的反面**。
//
// ⚠️ 而 `GateGroupMessage` 原来调的是**纯函数** `IsAddressedToMe` ——
// 它不读库, 所以**看不见**这个区别。三个 `*_speak` 理由
// (`no_scene_key` / `scene_load_failed` / `scene_missing`)
// 在生产路径上**恒为 0 次**: 那条性质在此之前根本就不存在。
//
// ⚠️ 接线的时候 `case strings.HasSuffix(why, "_speak"):` 那个分支**早就写好了** ——
// 也就是说这个 switch **一直在等这个调用方**。
// ─────────────────────────────────────────────────────────────

// TestAddressee_FaultSpeaksInsteadOfGoingSilent (本条最重要)
//
// ⚠️ **故障必须倒向"说话"**, 不许被"判不出来"顺手吞掉。
//
// 做法是**真的把库关掉** —— 不是打桩。那样才测得到"读库失败"这条路,
// 而不是"我以为读库会失败"那条路。
func TestAddressee_FaultSpeaksInsteadOfGoingSilent(t *testing.T) {
	e := gateEngine(t)
	if err := EnsureSceneSchema(e.Store); err != nil {
		t.Fatal(err)
	}
	// 先让它正常跑一次(场景登记好), 再关库 ——
	// 这样测的是"读场景失败", 不是"场景未登记"。
	sceneWireSay(e, "111", "先来一条")
	if err := e.Store.DB.Close(); err != nil {
		t.Fatal(err)
	}
	res := e.GateGroupMessage(SceneMsg{
		SceneKey: sceneWireKey, UIN: "222", Text: "随便说点什么", TS: clock.Now(),
	}, SelfIdentity{UIN: "3487037837"}, sceneWireCfg())
	if !strings.Contains(res.AddressedWhy, "scene_load_failed") {
		t.Fatalf("读库失败没有被认出来: why=%q —— 那它就被「判不出来」吞了", res.AddressedWhy)
	}
	if res.Stance != StanceSpeak {
		t.Fatalf("**读库失败倒向了沉默**: %s (%s) —— 那正是 §6.1 警告的那件事",
			res.Stance, res.Reason)
	}
	// ⚠️ 而它**不走价值判断** —— 故障时不该让"值不值得回"再来拦一道。
	if strings.Contains(res.Reason, "判不出") {
		t.Fatalf("故障被写成了判据结论: %q", res.Reason)
	}
}

// TestAddressee_UndecidableStillGoesSilent (反面, 与上一条成对)
//
// ⚠️ 接上读库版**不许**把"判不出来"也一起变成说话 ——
// 那会让它在群里对每一条不指名道姓的话都插一句。
func TestAddressee_UndecidableStillGoesSilent(t *testing.T) {
	e := gateEngine(t)
	if err := EnsureSceneSchema(e.Store); err != nil {
		t.Fatal(err)
	}
	res := e.GateGroupMessage(SceneMsg{
		SceneKey: sceneWireKey, UIN: "111", Text: "今天天气不错", TS: clock.Now(),
	}, SelfIdentity{UIN: "3487037837"}, sceneWireCfg())
	if res.AddressedWhy != ReasonUndecidable {
		t.Fatalf("没 @ 没名字的话应当是 undecidable, 实得 %q", res.AddressedWhy)
	}
	if res.Stance != StanceSilent {
		t.Fatalf("**判不出来却说话了**: %s (%s)", res.Stance, res.Reason)
	}
}

// TestAddressee_MissingSceneSpeaks (第三个 *_speak 理由)
//
// ⚠️ 未登记的场景与"读失败"同列 —— 都往说话倒 (scene.go 的注释)。
// 而它**只在直接调 `ResolveAddressee` 时才会出现**:
// 走 `GateGroupMessage` 的话, `EnsureScene` 已经在前面把行登记好了 ——
// **那个顺序就是为这件事排的**。
func TestAddressee_MissingSceneSpeaks(t *testing.T) {
	e := gateEngine(t)
	if err := EnsureSceneSchema(e.Store); err != nil {
		t.Fatal(err)
	}
	addressed, why := e.Store.ResolveAddressee(
		SceneMsg{SceneKey: "group:4040404", UIN: "111", Text: "喂"},
		SelfIdentity{UIN: "3487037837"}, sceneWireCfg())
	if !addressed || why != ReasonSceneMissing {
		t.Fatalf("未登记的场景应当往说话倒, 实得 addressed=%v why=%q", addressed, why)
	}

	// ⚠️ 而**走完整裁决**时它不该出现 —— 因为场景已经被登记了。
	// 这一条钉的是"顺序是承重的"那件事: 少了 EnsureScene,
	// 每条群消息都会拿到 scene_missing_speak 而短路成发言。
	res := e.GateGroupMessage(SceneMsg{
		SceneKey: sceneWireKey, UIN: "111", Text: "喂", TS: clock.Now(),
	}, SelfIdentity{UIN: "3487037837"}, sceneWireCfg())
	if strings.Contains(res.AddressedWhy, "scene_missing") {
		t.Fatal("**走完整裁决时场景居然是未登记的** —— EnsureScene 没在前面跑")
	}
}
