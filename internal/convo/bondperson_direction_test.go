package convo

import (
	"testing"
	"time"
)

// bondPersonEngine 够用的引擎: 库 + **真实 uin** + 关系相关的表。
//
// ⚠️ UserID 必须设 —— 私聊场景键是字面量 "private:owner", 人物层的键取自 e.UserID
// (拿不到真实 uin 时**不写**: 键错了会把同一个人分裂成两笔账)。
func bondPersonEngine(t *testing.T) *Engine {
	t.Helper()
	e := gateEngine(t)
	e.UserID = 2735604537
	s := e.Store
	for _, ensure := range []func() error{
		func() error { return EnsureBondSchema(s.DB) },
		func() error { return EnsureBondPersonSchema(s.DB) },
		func() error { return EnsureSceneSchema(s) },
		func() error { return EnsureTasteSchema(s.DB) },
		func() error { return EnsureFeedbackSchema(s.DB) },
		func() error { return EnsureIntentSchema(s.DB) },
		func() error { return EnsureOwnerMultSchema(s) },
	} {
		if err := ensure(); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

const (
	bpPersonUIN = "2735604537"
	bpGroupA    = "group:1034712035"
	bpGroupB    = "group:8801"
)

// TestPersonLayer_GroupMessageLandsOnThePerson (方向规则①)
//
// ⚠️ 群里的那一笔要记到**这个人**头上, 而不是只记到群头上 ——
// 否则「群里聊得火热、私聊没聊过」会让私聊恒为陌生 (配额 0 ⇒ 疏离)。
func TestPersonLayer_GroupMessageLandsOnThePerson(t *testing.T) {
	e := bondPersonEngine(t)
	now := time.Now()
	sc := SceneMsg{SceneKey: bpGroupA, UIN: bpPersonUIN, Text: "我最近在准备考试, 有点焦虑", TS: now}
	e.recordGateOutcome(sc, GroupGateResult{Stance: StanceSilent})

	person, err := LoadBondPerson(e.Store.DB, e.ownerKey(), bpPersonUIN)
	if err != nil || !person.Loaded {
		t.Fatalf("群里的话没落到人头上: %+v err=%v", person, err)
	}
	if person.Familiarity <= 0 {
		t.Fatalf("人物层熟悉度没涨: %+v", person.Bond)
	}
	// ⚠️ 而群那一行的键是**群号**, 不是人 —— 两者各记各的。
	gb, err := LoadBond(e.Store.DB, e.ownerKey(), bpGroupA)
	if err != nil || !gb.Loaded {
		t.Fatalf("群那一行也该有记录: %+v err=%v", gb, err)
	}
	// ⚠️ 私聊那一行**一个字节都不该出现** —— 他一句私聊都没发过。
	if dm, err := LoadBond(e.Store.DB, e.ownerKey(), DefaultSceneKey); err != nil || dm.Loaded {
		t.Fatalf("群消息不该写私聊那一行: %+v err=%v", dm, err)
	}
}

// TestPersonLayer_DMStageFollowsThePerson (方向规则①的**效果**)
func TestPersonLayer_DMStageFollowsThePerson(t *testing.T) {
	e := bondPersonEngine(t)
	if got := e.heatStageOf(DefaultSceneKey); got != HeatStageStranger {
		t.Fatalf("起点应当是陌生, 实得 %q", got)
	}
	// 人物层过了熟人线 → 私聊的热力档必须跟着走 (它就是"别再疏离"的落点)。
	if err := SaveBondPerson(e.Store.DB, e.ownerKey(), bpPersonUIN,
		Bond{Familiarity: BondFamiliarAt + 0.01}); err != nil {
		t.Fatal(err)
	}
	// ⚠️ 内置档位表只有三档 (陌生 20 / 熟人 60 / 亲密 100): **acquaint 与 stranger 共用最小 cap**,
	// 所以「私聊配额真的开得了」的分界是**熟人**那一档 (cap 60 减去单条代价 25 还剩 35)。
	// 钉 acquaint 会得到一个永远为真、因而永远不动的断言。
	if got := e.heatStageOf(DefaultSceneKey); got != HeatStageFamiliar {
		t.Fatalf("人物层已是熟人, 私聊档却是 %q —— 私聊没在读人物层", got)
	}
}

// TestPersonLayer_PrivateNeverEntersGroup (方向规则④)
//
// ⚠️ 私聊里说的话**不许让它在群里更熟** —— 那是跨场景泄漏, 也是这一层最容易写错的方向。
func TestPersonLayer_PrivateNeverEntersGroup(t *testing.T) {
	e := bondPersonEngine(t)
	now := time.Now()
	if err := e.Store.EnsureScene(bpGroupA); err != nil {
		t.Fatal(err)
	}
	// 私聊里说了一大堆私事 (亲密度会涨得很快)。
	for i := 0; i < 5; i++ {
		// ⚠️ 文本要真的命中判据词表 (`bondSecretWords` 里有「我跟你说个事」)——
		// 差一个字就变成"只涨熟悉度、不涨亲密度", 而那样这条用例测不到"质"的那半边。
		e.NoteSceneBond(DefaultSceneKey, "我跟你说个事, 家里最近有点麻烦", now)
	}
	// 人物层当然涨了 (群与私聊**共用**它)。
	person, _ := LoadBondPerson(e.Store.DB, e.ownerKey(), bpPersonUIN)
	if !person.Loaded || person.Closeness <= 0 {
		t.Fatalf("私聊的话该落在人物层上: %+v", person)
	}
	// ⚠️ 而**群那一行**必须一点没动 —— 不许创建, 更不许涨。
	if gb, err := LoadBond(e.Store.DB, e.ownerKey(), bpGroupA); err != nil || gb.Loaded {
		t.Fatalf("私聊内容进了群: %+v err=%v", gb, err)
	}
	// 读侧也一样: 群拿不到人物层那个量 (哪怕它很大)。
	if got := e.heatStageOf(bpGroupA); got != HeatStageStranger {
		t.Fatalf("群读到了人物层的热度 (%q) —— 私聊进群了", got)
	}
}

// TestPersonLayer_GroupsDoNotCross (方向规则②③)
//
// ⚠️ A 群的互动只影响 **A 群** 与 **该用户的私聊**; B 群**一点都不该动**。
func TestPersonLayer_GroupsDoNotCross(t *testing.T) {
	e := bondPersonEngine(t)
	now := time.Now()
	for i := 0; i < 5; i++ {
		e.recordGateOutcome(SceneMsg{SceneKey: bpGroupA, UIN: bpPersonUIN,
			Text: "我们群下周末聚一次吧", TS: now}, GroupGateResult{Stance: StanceSpeak})
	}
	// A 群自己涨了。
	ga, err := LoadBond(e.Store.DB, e.ownerKey(), bpGroupA)
	if err != nil || !ga.Loaded || ga.Familiarity <= 0 {
		t.Fatalf("A 群那一行没涨: %+v err=%v", ga, err)
	}
	// 人物层也涨了 (规则①)。
	if p, _ := LoadBondPerson(e.Store.DB, e.ownerKey(), bpPersonUIN); !p.Loaded {
		t.Fatal("A 群的互动没落到人头上")
	}
	// ⚠️ B 群**零影响**。
	if gb, err := LoadBond(e.Store.DB, e.ownerKey(), bpGroupB); err != nil || gb.Loaded {
		t.Fatalf("A 群把 B 群带热了: %+v err=%v", gb, err)
	}
	if got := e.heatStageOf(bpGroupB); got != HeatStageStranger {
		t.Fatalf("B 群读到了 A 群的热度 (%q) —— 群之间串了", got)
	}
}

// TestPersonLayer_DMRelationLineComesFromThePerson (方向规则①的**注入侧**)
//
// ⚠️ 私聊此前**根本没有关系注入**: `din.BondNote` 只有群那一处在设。
// 现在两条路都走 bondNoteForScene, 而私聊读的是人物层。
func TestPersonLayer_DMRelationLineComesFromThePerson(t *testing.T) {
	e := bondPersonEngine(t)
	// 一点记录都没有 → **不注入** (没有记录 ≠ 陌生人)。
	if got := e.bondNoteForScene(DefaultSceneKey); got != "" {
		t.Fatalf("没有任何关系记录却注入了关系: %q", got)
	}
	// 把人物层喂到"亲密" (在群里聊出来的那部分也算)。
	if err := SaveBondPerson(e.Store.DB, e.ownerKey(), bpPersonUIN,
		Bond{Closeness: BondIntimateAt + 0.01, Regard: 0.6, Familiarity: 0.2}); err != nil {
		t.Fatal(err)
	}
	if got := e.bondNoteForScene(DefaultSceneKey); got == "" {
		t.Fatal("人物层已经亲密, 私聊却没注入关系 —— 注入侧没在读人物层")
	}
	// ⚠️ 而**群**拿不到它 (规则④): 群那一行还不存在, 人物层再大也不许注入。
	if got := e.bondNoteForScene(bpGroupA); got != "" {
		t.Fatalf("私聊的关系注入了群提示词: %q", got)
	}
}

// TestPersonLayer_DoesNotFeedTheGroupBiasJudge
//
// ⚠️ biaswire 是**群侧判据** (§6.8 关系权重): 它必须只看**本群那一行**。
// 读了人物层就等于"私聊进群"(规则④), 而那种错从行为上几乎看不出来 ——
// 只表现为"它对新群里的人莫名热络"。
func TestPersonLayer_DoesNotFeedTheGroupBiasJudge(t *testing.T) {
	e := bondPersonEngine(t)
	// 把人物层喂到非常熟。
	if err := SaveBondPerson(e.Store.DB, e.ownerKey(), bpPersonUIN,
		Bond{Closeness: 0.95, Regard: 0.95, Familiarity: 0.95}); err != nil {
		t.Fatal(err)
	}
	// 群那一行**不存在** → 群侧判据必须报"没有数据源"(ok=false)。
	if _, ok := e.groupBias(SceneMsg{SceneKey: bpGroupA, UIN: bpPersonUIN, Text: "在吗"}); ok {
		t.Fatal("群侧偏向判据读到了人物层 —— 私聊进群了 (规则④)")
	}
}
