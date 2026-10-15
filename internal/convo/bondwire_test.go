package convo

import (
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────
// §5.1 关系量的接线
//
// ⚠️ 关系量涨不动**不是"涨得慢"**: 它让一条**硬闸门**永远关着 ——
//   closeness 恒 0 → 永远 stranger → 热力 cap 20 < 每条主动消息的代价 25
//   → floor(20/25) = 0 → **主动配额恒为 0**。
// 而那个闸门关着的表现, 只是**它比从前安静**。
// ─────────────────────────────────────────────────────────────

// TestBondSignalsFrom_ReadsTheFourPositiveKinds
//
// ⚠️ 这四个字段在接线之前**全库没有一个生产者** ——
// BondDelta 里那四个加分分支是死代码。
func TestBondSignalsFrom_ReadsTheFourPositiveKinds(t *testing.T) {
	noon := time.Date(2026, 10, 15, 14, 0, 0, 0, time.Local)
	night := time.Date(2026, 10, 15, 2, 0, 0, 0, time.Local)
	cases := []struct {
		text string
		at   time.Time
		want func(BondSignal) bool
		name string
	}{
		{"谢谢你啊", noon, func(s BondSignal) bool { return s.Thanked }, "道谢"},
		{"你觉得我该去吗", noon, func(s BondSignal) bool { return s.AskedAdvice }, "请教"},
		{"这事我只跟你说", noon, func(s BondSignal) bool { return s.SharedSecret }, "说了私事"},
		{"还没睡啊", night, func(s BondSignal) bool { return s.LateNightTalk }, "深夜还在聊"},
	}
	for _, c := range cases {
		if sig := BondSignalsFrom(c.text, c.at); !c.want(sig) {
			t.Errorf("%s 没被读出来: %+v", c.name, sig)
		}
	}
	// ⚠️ 反面: **普通闲聊不许推高关系量** —— 猜错的代价比涨得慢糟得多。
	plain := BondSignalsFrom("今天天气不错", noon)
	if plain.Thanked || plain.AskedAdvice || plain.SharedSecret || plain.LateNightTalk {
		t.Fatalf("普通闲聊被读成了关系信号: %+v", plain)
	}
	// 空文本也不许。
	if sig := BondSignalsFrom("   ", noon); sig.Thanked || sig.SharedSecret {
		t.Fatalf("空文本读出了信号: %+v", sig)
	}
	// ⚠️ "谢谢" 在**深夜**同时命中两条 —— 那是两条独立的路, 不冲突。
	both := BondSignalsFrom("谢谢", night)
	if !both.Thanked || !both.LateNightTalk {
		t.Fatalf("深夜道谢应当同时命中两条: %+v", both)
	}
}

// TestSceneBond_ClosenessCanActuallyRise
//
// ⚠️ **这条是死锁的回归测试。**
// 走的是生产路径 (NoteSceneBond), 而不是直接调 ApplyBond ——
// 直接调的话证明不了"真的有人在生产路径上喂它正向信号"。
func TestSceneBond_ClosenessCanActuallyRise(t *testing.T) {
	e, s := heatEngine(t)
	scene := DefaultSceneKey
	owner := e.ownerKey()

	br, _ := LoadBond(s.DB, owner, scene)
	if br.Bond.Closeness != 0 {
		t.Fatalf("夹具前提: 一开始亲密度应当是 0, 实得 %.3f", br.Bond.Closeness)
	}
	// ⚠️ 用**说了私事**的措辞, 不是道谢。
	//
	// `BondDelta` 里: 道谢 / 请教加的是 **regard(敬意)**, 亲密度一点不动;
	// 推亲密度的只有 `SharedSecret`(+0.04) 与 `LateNightTalk`(+0.03)。
	// 第一版夹具写的是"谢谢你, 你觉得我该怎么办" —— 那条路只涨敬意,
	// 于是用例红了, 而红得**对**: 它逼我把"亲密度靠什么涨"这件事查清楚。
	at := time.Date(2026, 10, 15, 2, 0, 0, 0, time.Local) // 凌晨 → 同时命中深夜
	for i := 0; i < 30; i++ {
		e.NoteSceneBond(scene, "这事我只跟你说, 你别告诉别人", at.Add(time.Duration(i)*time.Minute))
	}
	br2, _ := LoadBond(s.DB, owner, scene)
	if br2.Bond.Closeness <= 0 {
		t.Fatalf("道谢了 40 次亲密度还是 %.3f —— 正向信号没有接线", br2.Bond.Closeness)
	}
	if br2.Bond.Familiarity <= 0 {
		t.Fatalf("熟悉度没涨: %.3f", br2.Bond.Familiarity)
	}
	// ⚠️ 而且它必须**真的能把阶段推上去** —— 那才是这条链的意义。
	if BondStageOf(br2.Bond) == BondStageStranger {
		t.Fatalf("亲密度 %.3f 仍然判 stranger —— 热力 cap 还是 20", br2.Bond.Closeness)
	}
}

// TestStrangerCapCannotAffordAProactiveMessage
//
// ⚠️ **这条把整件事的病根钉住**: 光接热力通道是**不够的**。
// stranger 的 cap 是 20, 而每条主动消息的代价是 25 → floor(20/25) = 0。
// 那不是 bug, 是设计本意("不向陌生人掏心窝子")——
// 但它意味着"关系量必须真的能涨上去", 否则那条硬闸门永远关着。
func TestStrangerCapCannotAffordAProactiveMessage(t *testing.T) {
	cfg := DefaultHeatConfig()
	if got := HeatQuota(cfg.CapOf(HeatStageStranger), cfg.CostPerMsg, 2); got != 0 {
		t.Fatalf("stranger 的 cap 居然买得起一条主动消息 (配额 %d) —— 前提变了, 这几条尺子要重看", got)
	}
	// familiar 买得起 —— 于是"把关系推上去"确实能解决问题。
	if got := HeatQuota(cfg.CapOf(HeatStageFamiliar), cfg.CostPerMsg, 2); got <= 0 {
		t.Fatalf("familiar 的 cap 也买不起 (配额 %d) —— 那推关系量就白推了", got)
	}
}

// TestSceneBond_RebuffStillLowers (反面锚点)
//
// ⚠️ 接正向信号**不许**把负向那条路弄丢: 被冷落本来就该扣亲密度。
func TestSceneBond_RebuffStillLowers(t *testing.T) {
	cur := Bond{Closeness: 0.5}
	sig := BondSignalsFrom("谢谢你", time.Date(2026, 10, 15, 14, 0, 0, 0, time.Local))
	sig.Rebuffed = true
	got := ApplyBond(cur, sig)
	if got.Closeness >= cur.Closeness {
		t.Fatalf("被冷落之后亲密度没有下降: %.3f → %.3f", cur.Closeness, got.Closeness)
	}
}
