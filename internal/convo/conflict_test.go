package convo

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ——— §6.9 冲突介入: 测试 ———
//
// 写法约定 (与规格 §7 对齐): 每个契约至少一条**反面用例** ——
// 用例注释里写明"把实现改坏成 X → 这条会红", 让红/绿本身带上诊断信息。
//
// 辅助函数一律带 cflTest 前缀 (叶子模块合并时防同名)。

const cflTestScene = "group:10086"

// cflTestT0 是所有夹具的时间原点。**不读时钟** —— 判据只吃消息序列, 所以测试可复现。
var cflTestT0 = time.Date(2026, 3, 1, 20, 0, 0, 0, time.Local)

func cflTestMsg(uin string, at time.Time, text string, ats ...string) ConflictMsg {
	return ConflictMsg{Uin: uin, Text: text, Ats: ats, TS: at}
}

// cflTestPrior 造**前一个同长窗口** (t0-20m, t0-10m] 里的 6 条平话:
// 间隔 60s、发言人不交替、一个负面词都没有。
//
// 它是 ③ 的分母 (前窗密度 0) 与 ④ 的分母 (基线中位间隔 60s), 同时让窗口条数够 MinMsgs。
// 发言顺序刻意写成不交替的 (u1 u2 u2 u1 u3 u3) —— 最长严格交替段只有 1 轮, ① 不会误命中。
func cflTestPrior(t0 time.Time) []ConflictMsg {
	speakers := []string{"u1", "u2", "u2", "u1", "u3", "u3"}
	start := t0.Add(-19 * time.Minute)
	out := make([]ConflictMsg, 0, len(speakers))
	for i, sp := range speakers {
		out = append(out, cflTestMsg(sp, start.Add(time.Duration(i)*time.Minute), "哈哈哈今天天气不错"))
	}
	return out
}

// cflTestConflictWindow 造当前窗口 (t0-10m, t0] 里的 6 条消息: 严格交替、间隔 10s、
// 每条都带负面词、互相 @ —— **①②③④ 全中**, 是"确实在吵"的正样本。
func cflTestConflictWindow(t0 time.Time) []ConflictMsg {
	texts := []string{"你有病吧", "你才恶心", "神经病", "闭嘴", "真无语", "滚"}
	out := make([]ConflictMsg, 0, len(texts))
	for i, tx := range texts {
		ts := t0.Add(-time.Duration((len(texts)-1-i)*10) * time.Second)
		if i%2 == 0 {
			out = append(out, cflTestMsg("u2", ts, tx, "u1"))
		} else {
			out = append(out, cflTestMsg("u1", ts, tx, "u2"))
		}
	}
	return out
}

// cflTestChillWindow 造"同长度、同节奏、同负面密度, 但全无交锋"的窗口:
// 用来把 ②③④ 一个个摘出去 (发言人成对出现 → 不交替; 没有 @; 间隔与基线一致)。
func cflTestChillWindow(t0 time.Time, negative bool) []ConflictMsg {
	speakers := []string{"u1", "u2", "u2", "u1", "u3", "u3"}
	text := "哈哈哈今天天气不错"
	if negative {
		text = "真无语"
	}
	start := t0.Add(-5 * time.Minute)
	out := make([]ConflictMsg, 0, len(speakers))
	for i, sp := range speakers {
		out = append(out, cflTestMsg(sp, start.Add(time.Duration(i)*time.Minute), text))
	}
	return out
}

// cflTestSelfWindow 造"**它自己就是冲突的一方**"的窗口 (B2 / R25):
// 空串 = convo 自己, 与 10001 严格交替、互相 @、句句带负面词。
func cflTestSelfWindow(t0 time.Time) []ConflictMsg {
	texts := []string{"你真的很烦人", "你有病吧", "神经病", "闭嘴", "真无语", "滚"}
	out := make([]ConflictMsg, 0, len(texts))
	for i, tx := range texts {
		ts := t0.Add(-time.Duration((len(texts)-1-i)*10) * time.Second)
		if i%2 == 0 {
			out = append(out, cflTestMsg(ConflictSelfUin, ts, tx, "10001"))
		} else {
			out = append(out, cflTestMsg("10001", ts, tx, ConflictSelfUin))
		}
	}
	return out
}

// cflTestEscalateWindow = 冲突窗口 + 一条人身攻击 (硬线 4 / R26)。
func cflTestEscalateWindow(t0 time.Time) []ConflictMsg {
	w := cflTestConflictWindow(t0)
	w[3].Text = "你怎么不去死" // escalate 词表命中; 它不是负面词, 所以密度仍由其余 5 条撑着 (5/6)
	return w
}

// cflTestHistory 把前窗与当前窗拼成一份升序历史, 并算好基线 (现算, 不落库)。
func cflTestHistory(t0 time.Time, cur []ConflictMsg) ([]ConflictMsg, ConflictBaseline, ConflictPolicy) {
	cfg := DefaultConflictPolicy()
	msgs := append(cflTestPrior(t0), cur...)
	return msgs, ConflictBaselineFrom(msgs, cfg), cfg
}

// ——— ① 连续对呛 ———

func TestConflict_AlternationDetected(t *testing.T) {
	msgs, base, cfg := cflTestHistory(cflTestT0, cflTestConflictWindow(cflTestT0))
	sig := ConflictSignalsOf(msgs, base, cfg)
	if sig.AltRounds != 5 {
		t.Fatalf("严格交替 6 条 = 5 轮, got %d", sig.AltRounds)
	}
	if !sig.Alternating {
		t.Fatalf("① 应命中: %+v", sig)
	}
	if sig.AltPair != "u1<u2" {
		t.Fatalf("pair_key 应是排序后的 u1<u2, got %q", sig.AltPair)
	}
	if hit, why := DetectConflict(msgs, base, cfg); !hit {
		t.Fatalf("四信号全中应判冲突: %s", why)
	}
	// 反面: 把"轮数"改成"消息条数"(段长) → AltRounds 变 6 → 红。
	// 反面: 把 min_rounds 的比较写成 > 而不是 >= (5 > 5 假) → 红。
}

func TestConflict_ThirdPersonBreaksAlternation(t *testing.T) {
	cur := cflTestConflictWindow(cflTestT0)
	// 中间插一句第三人 —— "其间无第三人" 的直接反例。
	cur[2].Uin = "u9"
	cur[2].Ats = nil
	msgs, base, cfg := cflTestHistory(cflTestT0, cur)
	sig := ConflictSignalsOf(msgs, base, cfg)
	if sig.Alternating {
		t.Fatalf("插进第三人后不该再有 5 轮对呛, got rounds=%d", sig.AltRounds)
	}
	// 反面: 把①的延伸条件从 w[k].Uin==w[k-2].Uin 放宽成"参与者只有两个人"
	//      (允许第三人插话) → Alternating 变 true → 红。
}

// ——— ② 互相 @ ———

func TestConflict_MutualAtNeedsBothDirections(t *testing.T) {
	// 只单向 @: 不算"互相 @"。
	one := cflTestConflictWindow(cflTestT0)
	for i := range one {
		one[i].Ats = []string{"u1"} // 只有 u2→u1 一个方向
	}
	msgs, base, cfg := cflTestHistory(cflTestT0, one)
	if sig := ConflictSignalsOf(msgs, base, cfg); sig.MutualAt {
		t.Fatalf("单向 @ 不该算互相 @: %+v", sig)
	}
	// 双向 @: 算。
	both := cflTestConflictWindow(cflTestT0)
	msgs2, base2, cfg2 := cflTestHistory(cflTestT0, both)
	sig2 := ConflictSignalsOf(msgs2, base2, cfg2)
	if !sig2.MutualAt || sig2.AtPair != "u1<u2" {
		t.Fatalf("双向 @ 应命中且 pair 为 u1<u2: %+v", sig2)
	}
	// 反面: 把②的实现从"双向都有"改成"出现过 @ 就算" → 第一段变 true → 红。
}

// ——— ③ 负面密度上升 ———

func TestConflict_DensityRiseNeedsPreviousWindow(t *testing.T) {
	cfg := DefaultConflictPolicy()
	// 没有前窗 (整个历史就是当前窗口): "上升"无从谈起 → 不命中。
	only := cflTestConflictWindow(cflTestT0)
	base := ConflictBaselineFrom(only, cfg)
	if base.PrevMsgs != 0 {
		t.Fatalf("这里本来就不该有前窗, got %d", base.PrevMsgs)
	}
	if sig := ConflictSignalsOf(only, base, cfg); sig.DensityRise {
		t.Fatalf("没有前窗时不许判'上升': %+v", sig)
	}
	// 有前窗且前窗干净 → 命中。
	msgs, base2, _ := cflTestHistory(cflTestT0, cflTestConflictWindow(cflTestT0))
	sig := ConflictSignalsOf(msgs, base2, cfg)
	if !sig.DensityRise || sig.NegDensity <= sig.PrevNegDensity {
		t.Fatalf("前窗密度 0 → 应判上升: %+v", sig)
	}
	// 反面: 把"没有前窗"当成"前窗密度 0"(即 >= 0 恒真) → 第一段变 true → 红。
}

func TestConflict_DensityIsRelative(t *testing.T) {
	// R22: 一个**本来就全程吐槽**的群 —— 两边密度都是 1.0, "上升"不成立。
	// 绝对阈值会把这种群判成冲突, 这正是规格点名要避免的。
	cfg := DefaultConflictPolicy()
	// 前窗 (t0-20m, t0-10m] 与当前窗 (t0-10m, t0] 各 6 条, 同节奏同密度 —— 这个是"一直在吐槽"的群。
	msgs := append(cflTestChillWindow(cflTestT0.Add(-15*time.Minute), true),
		cflTestChillWindow(cflTestT0.Add(-5*time.Minute), true)...)
	base := ConflictBaselineFrom(msgs, cfg)
	sig := ConflictSignalsOf(msgs, base, cfg)
	if sig.NegDensity < cfg.NegativeDensity {
		t.Fatalf("夹具前提: 密度 %.2f 应已达到绝对阈值 %.2f", sig.NegDensity, cfg.NegativeDensity)
	}
	if sig.PrevNegDensity < cfg.NegativeDensity {
		t.Fatalf("夹具前提: 前窗密度 %.2f 也应很高", sig.PrevNegDensity)
	}
	if sig.DensityRise {
		t.Fatalf("两边一样高 = 没有上升, 不许因为绝对值高就命中: %+v", sig)
	}
	if hit, why := DetectConflict(msgs, base, cfg); hit {
		t.Fatalf("全程吐槽的群不许判冲突: %s", why)
	}
	// 反面: 把③的"高于前窗"去掉, 只留 >= negative_density → DensityRise 变 true → 红。
}

// ——— ④ 回复间隔骤短 ———

func TestConflict_IntervalDropNeedsBaselineAndSample(t *testing.T) {
	msgs, base, cfg := cflTestHistory(cflTestT0, cflTestConflictWindow(cflTestT0))
	sig := ConflictSignalsOf(msgs, base, cfg)
	if !sig.IntervalShort || sig.MedianGapSec != 10 || sig.BaselineGapSec != 60 {
		t.Fatalf("10s <= 60s*0.5 应命中: %+v", sig)
	}
	// 冷清群: 窗口里只有两条秒回 —— 样本不足, ④ 不许命中 (规格 ④ 明确要防的正是这个)。
	quiet := []ConflictMsg{
		cflTestMsg("u1", cflTestT0.Add(-2*time.Minute), "在吗"),
		cflTestMsg("u2", cflTestT0.Add(-2*time.Minute+5*time.Second), "在"),
	}
	qmsgs := append(cflTestPrior(cflTestT0), quiet...)
	qsig := ConflictSignalsOf(qmsgs, ConflictBaselineFrom(qmsgs, cfg), cfg)
	if qsig.WindowMsgs != 2 {
		t.Fatalf("夹具前提: 冷清窗口应只有 2 条, got %d", qsig.WindowMsgs)
	}
	if !qsig.HasGap || qsig.BaselineGapSec != 60 {
		t.Fatalf("夹具前提: 5s 相对基线 60s 本该算骤短, 只差样本数这一道闸: %+v", qsig)
	}
	if qsig.IntervalShort {
		t.Fatalf("窗口 %d 条 < min_msgs, ④ 不许命中: %+v", qsig.WindowMsgs, qsig)
	}
	// 反面: 把 min_msgs 门槛从 ④ 里删掉 → 第二段变 true → 红。
	// 反面: 基线取"窗口自己的中位数"(而不是前 K 个窗口) → 第一段的 baseline 变成 10 → 红。
}

// ——— C1 / C2: 判据本身给出的"不做" ———

func TestConflict_SingleSignalNotEnough(t *testing.T) {
	// R21: 只有负面词多 —— 无对呛、无互 @、间隔正常 → 不许判冲突。
	cfg := DefaultConflictPolicy()
	msgs := append(cflTestPrior(cflTestT0), cflTestChillWindow(cflTestT0, true)...)
	base := ConflictBaselineFrom(msgs, cfg)
	sig := ConflictSignalsOf(msgs, base, cfg)
	if !sig.DensityRise {
		t.Fatalf("夹具前提: ③ 应命中 (前窗 0 → 本窗 1.0): %+v", sig)
	}
	if sig.HitCount() != 1 {
		t.Fatalf("夹具前提: 只该有 ③ 一个信号, got %d", sig.HitCount())
	}
	if hit, why := DetectConflict(msgs, base, cfg); hit {
		t.Fatalf("一个信号不够判冲突 (C1): %s", why)
	}
	// 反面: signals_required 从 3 改成 1 → hit 变 true → 红。
}

func TestConflict_WindowTooFewMsgs(t *testing.T) {
	// C2: 窗口条数 < min_msgs → 样本不足, 不判。
	cfg := DefaultConflictPolicy()
	cfg.SignalsRequired = 2                     // 把夹具从"信号数不够"里摘出去, 单独考 C2
	cur := cflTestConflictWindow(cflTestT0)[:2] // 只留两条: 互 @(②) + 负面密度上升(③) = 2 个信号
	msgs := append(cflTestPrior(cflTestT0), cur...)
	base := ConflictBaselineFrom(msgs, cfg)
	sig := ConflictSignalsOf(msgs, base, cfg)
	if sig.WindowMsgs != 2 || sig.HitCount() != 2 {
		t.Fatalf("夹具前提: 2 条消息 2 个信号 (②③), got msgs=%d hits=%d", sig.WindowMsgs, sig.HitCount())
	}
	if hit, why := DetectConflict(msgs, base, cfg); hit {
		t.Fatalf("样本不足时不许判冲突 (C2): %s", why)
	}
	// 反面: 删掉 C2 (min_msgs 检查) → 2 个信号达到门槛 → hit 变 true → 红。
}

func TestConflict_SignalsRequiredClamped(t *testing.T) {
	// >4 的信号要求 = 永不判冲突 = 静默失灵。宁可夹到 4 (更严), 不许夹到"永远不响"。
	if c := (ConflictPolicy{SignalsRequired: 9}).Normalized(); c.SignalsRequired != 4 {
		t.Fatalf("signals_required 应夹到 4, got %d", c.SignalsRequired)
	}
	if c := (ConflictPolicy{SignalsRequired: -1}).Normalized(); c.SignalsRequired != 3 {
		t.Fatalf("零值应回落默认 3, got %d", c.SignalsRequired)
	}
	// 反面: 把夹取删掉 (9 原样保留) → hit 恒 false → 红。
}

func TestConflict_ZeroPolicyEqualsDefaults(t *testing.T) {
	// 纪律 #1: 零值 = 未提供 → 走默认档, 不许"零值恰好等于某个极端档"。
	z := ConflictPolicy{}.Normalized()
	d := DefaultConflictPolicy()
	if z.WindowMin != d.WindowMin || z.MinMsgs != d.MinMsgs || z.MinRounds != d.MinRounds ||
		z.NegativeDensity != d.NegativeDensity || z.IntervalRatio != d.IntervalRatio ||
		z.SignalsRequired != d.SignalsRequired || z.CooldownMin != d.CooldownMin ||
		z.FamiliarAffinityMin != d.FamiliarAffinityMin {
		t.Fatalf("空配置应收成默认档: zero=%+v default=%+v", z, d)
	}
	for _, v := range []int{z.WindowMin, z.MinMsgs, z.MinRounds, z.SignalsRequired, z.CooldownMin} {
		if v <= 0 {
			t.Fatalf("默认档里不许有 <=0 的数: %+v", z)
		}
	}
	for _, v := range []float64{z.NegativeDensity, z.IntervalRatio, z.FamiliarAffinityMin} {
		if v <= 0 {
			t.Fatalf("默认档里不许有 <=0 的数: %+v", z)
		}
	}
	if len(z.NegativeWords) == 0 || len(z.EscalateWords) == 0 || len(z.MediationWords) == 0 {
		t.Fatalf("空配置必须回落内置词表: %+v", z)
	}
	if len(z.ModeByStage) == 0 {
		t.Fatalf("空配置必须回落内置档位表: %+v", z)
	}
	// 反面: 把某个 <=0 分支写成"取 0"(例如 MinMsgs) → 上面的 >0 断言红。
}

// ——— A 类: 没资格 ———

func TestConflict_StrangerDoesNotIntervene(t *testing.T) {
	// R23: 平台角色是 admin, 但**场景阶段 = 陌生** → 不许介入。
	msgs, base, cfg := cflTestHistory(cflTestT0, cflTestConflictWindow(cflTestT0))
	sig := ConflictSignalsOf(msgs, base, cfg)
	d := DecideConflict(sig, ConflictContext{Stage: ConflictStageStranger, SelfRole: "admin"}, cfg)
	if d.Verdict != ConflictNormal {
		t.Fatalf("陌生阶段不许介入 (A1), got %s (%s)", d.Verdict, d.Why)
	}
	if !strings.Contains(d.Why, "陌生") {
		t.Fatalf("理由要说清是 A1: %s", d.Why)
	}
	// 档位表: 阶段越深, 方式越"贴"; 未提供 → 最轻的一档 (divert), 不许落到 dm。
	cases := []struct {
		stage string
		want  string
	}{
		{ConflictStageFamiliar, ConflictModeAbsorb},
		{ConflictStageIntimate, ConflictModeDM},
		{"", ConflictModeDivert},
		{"不认识的阶段", ConflictModeDivert},
	}
	for _, c := range cases {
		dd := DecideConflict(sig, ConflictContext{Stage: c.stage}, cfg)
		if dd.Verdict != ConflictIntervene || dd.Mode != c.want {
			t.Fatalf("阶段 %q 应介入且方式 %s, got %s/%s", c.stage, c.want, dd.Verdict, dd.Mode)
		}
	}
	// 反面: 删掉 A1 → 第一段变 intervene → 红。
	// 反面: 把 A1 改成"看平台角色"(admin 就劝) → 同样红 (夹具里角色就是 admin)。
	// 反面: 把未知阶段兜底改成 dm → 第三/四段红。
}

func TestConflict_SelfIsNew_NoIntervene(t *testing.T) {
	// A2: 它自己刚进群 (is_new 窗口内) → 不掺和。
	msgs, base, cfg := cflTestHistory(cflTestT0, cflTestConflictWindow(cflTestT0))
	sig := ConflictSignalsOf(msgs, base, cfg)
	if d := DecideConflict(sig, ConflictContext{Stage: ConflictStageIntimate, SelfIsNew: true}, cfg); d.Verdict != ConflictNormal {
		t.Fatalf("刚进群不许劝架 (A2), got %s (%s)", d.Verdict, d.Why)
	}
	if d := DecideConflict(sig, ConflictContext{Stage: ConflictStageIntimate}, cfg); d.Verdict != ConflictIntervene {
		t.Fatalf("不在 is_new 窗口内应可介入, got %s (%s)", d.Verdict, d.Why)
	}
	// 反面: 删掉 A2 → 第一段变 intervene → 红。
}

func TestConflict_UnfamiliarPair_NoIntervene(t *testing.T) {
	// A3: 跟冲突双方都不熟 → 不劝架。
	msgs, base, cfg := cflTestHistory(cflTestT0, cflTestConflictWindow(cflTestT0))
	sig := ConflictSignalsOf(msgs, base, cfg)
	strangerAff := map[string]float64{"u1": 0.05, "u2": 0.05}
	if d := DecideConflict(sig, ConflictContext{Stage: ConflictStageIntimate, Affinity: strangerAff}, cfg); d.Verdict != ConflictNormal {
		t.Fatalf("双方都不熟不许劝架 (A3), got %s (%s)", d.Verdict, d.Why)
	}
	// 有一边熟 → 可以介入。
	mixed := map[string]float64{"u1": 0.9, "u2": 0.05}
	if d := DecideConflict(sig, ConflictContext{Stage: ConflictStageIntimate, Affinity: mixed}, cfg); d.Verdict != ConflictIntervene {
		t.Fatalf("有一边熟就该能介入, got %s (%s)", d.Verdict, d.Why)
	}
	// 未提供 (nil) → A3 不生效 (零值 = 未提供, 不是"都不熟")。
	if d := DecideConflict(sig, ConflictContext{Stage: ConflictStageIntimate}, cfg); d.Verdict != ConflictIntervene {
		t.Fatalf("nil affinity = 未提供, 不许当成不熟, got %s (%s)", d.Verdict, d.Why)
	}
	// 反面: 删掉 A3 → 第一段变 intervene → 红。
	// 反面: 把 nil 也当成"都不熟" → 第三段变 normal → 红。
}

func TestConflict_MentionedSelf_ThreadWins(t *testing.T) {
	// A4: 它正被 @ / 被问 → 线程归属优先, 不介入冲突 (§6.10.2 ①)。
	msgs, base, cfg := cflTestHistory(cflTestT0, cflTestConflictWindow(cflTestT0))
	sig := ConflictSignalsOf(msgs, base, cfg)
	if d := DecideConflict(sig, ConflictContext{Stage: ConflictStageIntimate, MentionedSelf: true}, cfg); d.Verdict != ConflictNormal {
		t.Fatalf("被 @ 时不许去劝架 (A4), got %s (%s)", d.Verdict, d.Why)
	}
	if d := DecideConflict(sig, ConflictContext{Stage: ConflictStageIntimate}, cfg); d.Verdict != ConflictIntervene {
		t.Fatalf("没被 @ 时应可介入, got %s (%s)", d.Verdict, d.Why)
	}
	// 反面: 删掉 A4 → 第一段变 intervene → 红。
}

// ——— B 类: 有资格, 但方式不是劝和 ———

func TestConflict_SelfIsParty_NotMediation(t *testing.T) {
	// R25 (规格点名): 被 A 骂时**不是"介入"**, 严禁走劝和话术。
	msgs, base, cfg := cflTestHistory(cflTestT0, cflTestSelfWindow(cflTestT0))
	sig := ConflictSignalsOf(msgs, base, cfg)
	if !sig.SelfInvolved {
		t.Fatalf("夹具前提: 对呛那一对里有它自己: %+v", sig)
	}
	d := DecideConflict(sig, ConflictContext{Stage: ConflictStageIntimate}, cfg)
	if d.Verdict != ConflictSelfSide {
		t.Fatalf("它自己是一方 → 必须 self_side (走 §5.5), got %s (%s)", d.Verdict, d.Why)
	}
	if d.Mode == ConflictModeDivert || d.Mode == ConflictModeAbsorb || d.Mode == ConflictModeDM {
		t.Fatalf("self_side 不许带劝和三式, got mode=%q", d.Mode)
	}
	// 第二重: 就算模型自己写出打圆场的话, 判据也必须抓住它 (校验链据此 Refused)。
	for _, tx := range []string{"大家都冷静一下", "你们别吵了", "我劝你们各退一步"} {
		if hit, _ := ConflictMediationHit(tx, nil); !hit {
			t.Fatalf("打圆场文本必须被抓住: %q", tx)
		}
	}
	// 反面: 删掉 B2 (自己也是一方时照常劝和) → Verdict 变 intervene/mode=absorb → 红。
	// 反面: 把 ConflictMediationHit 换成永远 false → 第二段红。
}

func TestConflict_Escalate_NoMediation_ReportOwner(t *testing.T) {
	// R26: 硬线 4 (人身攻击/歧视/安全) → 不劝和 + 上报 owner。
	msgs, base, cfg := cflTestHistory(cflTestT0, cflTestEscalateWindow(cflTestT0))
	sig := ConflictSignalsOf(msgs, base, cfg)
	if !sig.Escalate {
		t.Fatalf("夹具前提: 应命中硬线词表")
	}
	d := DecideConflict(sig, ConflictContext{Stage: ConflictStageIntimate}, cfg)
	if d.Verdict != ConflictEscalate {
		t.Fatalf("命中硬线应 escalate, got %s (%s)", d.Verdict, d.Why)
	}
	if d.Mode == ConflictModeDivert || d.Mode == ConflictModeAbsorb {
		t.Fatalf("硬线下 mode 不许是劝和三式, got %q", d.Mode)
	}
	if !d.Escalated || d.Mode != ConflictModeReport {
		t.Fatalf("应为 report 且 Escalated=true: %+v", d)
	}
	// "必须落一条 owner 上报": 决策落库后表里要能看见 escalated=1。
	s := newTestStore(t)
	if err := EnsureConflictSchema(s); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordConflictDecision(cflTestScene, d, cflTestT0, cfg); err != nil {
		t.Fatal(err)
	}
	acts, err := s.ConflictActions(cflTestScene, d.Pair)
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 1 || !acts[0].Escalated || acts[0].Mode != ConflictModeReport {
		t.Fatalf("应落一条 escalated=1 / mode=report 的记录, got %+v", acts)
	}
	// 反面: 把硬线 4 改成照常劝和 → Verdict/Mode 两条断言红。
}

func TestConflict_NeverAnnouncesMediation(t *testing.T) {
	// R27 / 硬线 5: 帧文本命中"我劝/你们别吵/都冷静" → 判据命中 (校验链据此 Refused,
	// **不计入帧失败率** —— 那是刻意不说, 与 A6 同族; 接线由主控在校验链做)。
	bad := []string{"大家都冷静一下", "我劝你们别为这种事吵", "你们别吵了", "都消消气"}
	for _, tx := range bad {
		hit, w := ConflictMediationHit(tx, nil)
		if !hit || w == "" {
			t.Fatalf("应命中硬线 5: %q", tx)
		}
	}
	for _, tx := range []string{"", "今天天气不错", "我先去吃饭了"} {
		if hit, w := ConflictMediationHit(tx, nil); hit {
			t.Fatalf("不该误伤正常文本 %q (命中词 %q)", tx, w)
		}
	}
	// 词表可配: 换成自己的词, 内置词就不再算数。
	if hit, _ := ConflictMediationHit("大家都冷静", []string{"消停会儿"}); hit {
		t.Fatalf("词表给了就以给的为准")
	}
	if hit, _ := ConflictMediationHit("你俩消停会儿吧", []string{"消停会儿"}); !hit {
		t.Fatalf("自定义词应命中")
	}
	// 反面: 删掉硬线 5 的判据 (永远返回 false) → 第一段红。
}

// ——— A5: 冷却 (落库的唯一事实 + 现算的窗口) ———

func TestConflict_InterveneAtMostOncePerWindow(t *testing.T) {
	// R24: 同一对在冷却窗内只许落 1 条。冷却本身是**现算**的 (acted_ts + cooldown_min)。
	s := newTestStore(t)
	if err := EnsureConflictSchema(s); err != nil {
		t.Fatal(err)
	}
	pair := ConflictPairKey("u1", "u2")
	if pair != "u1<u2" {
		t.Fatalf("pair_key 应排序拼接, got %q", pair)
	}
	at := cflTestT0
	if err := s.RecordConflictAction(cflTestScene, pair, ConflictModeDivert, false, at, 120); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordConflictAction(cflTestScene, pair, ConflictModeAbsorb, false, at.Add(5*time.Minute), 120); !errors.Is(err, ErrConflictCooldown) {
		t.Fatalf("冷却期内第二次介入应被拒 (ErrConflictCooldown), got %v", err)
	}
	n, err := s.ConflictActionCount(cflTestScene, pair)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("同一冲突窗内只许 1 条, got %d", n)
	}
	// A5 决策侧: 冷却期内同一对不许再介入 (冷却被读出来后要真的挡住决策)。
	msgs, _, cfg := cflTestHistory(cflTestT0, cflTestConflictWindow(cflTestT0))
	if d := s.GateConflict(cflTestScene, msgs, ConflictContext{Stage: ConflictStageIntimate}, cfg, at.Add(5*time.Minute)); d.Verdict != ConflictNormal {
		t.Fatalf("冷却期内不许再次介入 (A5), got %s (%s)", d.Verdict, d.Why)
	}
	if d := s.GateConflict(cflTestScene, msgs, ConflictContext{Stage: ConflictStageIntimate}, cfg, at.Add(121*time.Minute)); d.Verdict != ConflictIntervene {
		t.Fatalf("出冷却后应可介入, got %s (%s)", d.Verdict, d.Why)
	}
	// 反面: 删掉 DecideConflict 的 A5 分支 -> 第一段变 intervene -> 红。
	// 反面: 删掉落库层冷却闸 -> 上面 count 断言红。

	// 现算的冷却读数 (冷却 = acted_ts + cooldown_min, 不落库)。
	active, err := s.ConflictCooldownActive(cflTestScene, pair, at.Add(30*time.Minute), 120)
	if err != nil || !active {
		t.Fatalf("30 分钟后仍在 120 分钟冷却内: active=%v err=%v", active, err)
	}
	active, err = s.ConflictCooldownActive(cflTestScene, pair, at.Add(121*time.Minute), 120)
	if err != nil || active {
		t.Fatalf("121 分钟后应已出冷却: active=%v err=%v", active, err)
	}
	// 冷却过期后可以再介入 (劝不动就撤, 不是永久拉黑) —— 那时已经是另一场冲突了。
	if err := s.RecordConflictAction(cflTestScene, pair, ConflictModeAbsorb, false, at.Add(121*time.Minute), 120); err != nil {
		t.Fatalf("冷却过后应可再介入: %v", err)
	}
	if n2, _ := s.ConflictActionCount(cflTestScene, pair); n2 != 2 {
		t.Fatalf("冷却过后应能落第二条, got %d", n2)
	}
	// 反面: 删掉落库那一层的冷却检查 → 第二次写成功, count 变 2 → 红。
	// 反面: 把冷却判据写成 now.Sub(t) > cooldown (方向反了) → 第一次就判"已冷却" → 红。
}

// ——— R28: 系统故障 vs 语义判不出 ———

func TestSceneLoadFailure_FallsBackToSpeaking(t *testing.T) {
	// R28 (规格标为最容易写错的一条):
	//   读场景失败 = **系统故障** → 往说话倒 (ConflictNormal), 绝不沉默;
	//   只有**语义判不出**才沉默 (ConflictSilent)。
	msgs, base, cfg := cflTestHistory(cflTestT0, cflTestConflictWindow(cflTestT0))

	// (1) 纯函数侧: 读库失败走 err 通道。
	d := ResolveConflict(msgs, base, ConflictContext{Stage: ConflictStageIntimate},
		errors.New("disk I/O error"), cfg)
	if d.Verdict != ConflictNormal || !d.Speaking() {
		t.Fatalf("读场景失败必须往说话倒, got %s (%s)", d.Verdict, d.Why)
	}
	if strings.Contains(d.Why, "沉默") && !strings.Contains(d.Why, "不沉默") {
		t.Fatalf("失败理由不许把结论说成沉默: %s", d.Why)
	}
	// (2) 语义判不出 → 沉默 (唯一出口)。
	dc := DecideConflict(ConflictSignalsOf(msgs, base, cfg), ConflictContext{Undecidable: true}, cfg)
	if dc.Verdict != ConflictSilent || dc.Speaking() {
		t.Fatalf("语义判不出才沉默, got %s", dc.Verdict)
	}
	// (3) 读库侧的读失败必须**冒泡**, 不许被吞成"没有记录"。
	s := newTestStore(t)
	if err := EnsureConflictSchema(s); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConflictCooldownActive(cflTestScene, "u1<u2", cflTestT0, 120); err == nil {
		t.Fatal("读库失败必须返回错误, 不许静默当成'没有介入过'")
	}
	d3 := s.GateConflict(cflTestScene, msgs, ConflictContext{Stage: ConflictStageIntimate}, cfg, cflTestT0)
	if d3.Verdict != ConflictNormal || !d3.Speaking() {
		t.Fatalf("冷却读取失败必须走正常说话, got %s (%s)", d3.Verdict, d3.Why)
	}
	// 反面: 把 ctxErr != nil 与 ctx.Undecidable 并成一条 (都返回 Silent) → (1)/(3) 变红。
	// 反面: 让 ConflictCooldownActive 读失败时 return false, nil → 第三段"必须返回错误"红。
}

// ——— 落库的形状: 只有事实, 没有派生量 ———

func TestConflict_SchemaOnlyFacts(t *testing.T) {
	s := newTestStore(t)
	if err := EnsureConflictSchema(nil); err == nil {
		t.Fatal("store 未初始化时应报错, 不许静默成功")
	}
	if err := EnsureConflictSchema(s); err != nil {
		t.Fatal(err)
	}
	if err := EnsureConflictSchema(s); err != nil { // 幂等: 重复调用不报错
		t.Fatal(err)
	}
	var tables int
	if err := s.DB.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name LIKE 'convo_conflict%'").Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 1 {
		t.Fatalf("本模块只该有 1 张表 (只有'介入过'这一个事实), got %d", tables)
	}
	rows, err := s.DB.Query("SELECT name FROM pragma_table_info('convo_conflict_action') ORDER BY cid")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, n)
	}
	want := []string{"id", "scene_key", "pair_key", "acted_ts", "mode", "escalated", "outcome"}
	if strings.Join(cols, ",") != strings.Join(want, ",") {
		t.Fatalf("列清单必须是纯事实 (纪律 #4: 派生量不落库)\n got %v\nwant %v", cols, want)
	}
	// 反面: 往表里加 alt_rounds / neg_density 之类的派生列 → 上面红
	//       (而且它们没有"谁在什么时候重算"的答案)。
	// 反面: 把建表改成非幂等 (去掉 IF NOT EXISTS) → 第二次调用报错 → 红。
}

// ——— A3 裁定: 规模不进公式 ———

func TestConflict_ScaleInvariant_SameFormula(t *testing.T) {
	// A3「不定向」: 不许为任何特定群形态做设计。
	// 同一段"6 条严格交替、间隔 10s、句句负面"的对话, 换三种人群形态:
	//   (a) N=2 的最小群: 只有它自己 (空串) 与 owner 10001;
	//   (b) 另外两个陌生人 u1/u2 (同样 6 条);
	//   (c) 同一段对话, 但历史里另外还有 499 个不同的人各说过一句话 (大群)。
	// 三个结果必须**逐字段相同** —— 规模只能通过"窗口内有多少条消息"进入公式。
	cfg := DefaultConflictPolicy()
	small := append(cflTestPrior(cflTestT0), cflTestSelfWindow(cflTestT0)...)
	pairA := append(cflTestPrior(cflTestT0), cflTestConflictWindow(cflTestT0)...)

	big := append([]ConflictMsg(nil), pairA...)
	for i := 0; i < 499; i++ {
		big = append(big, cflTestMsg(fmt.Sprintf("u%03d", i+100), cflTestT0.Add(-6*time.Hour-time.Duration(i)*time.Second), "哈哈哈"))
	}

	sigSmall := ConflictSignalsOf(small, ConflictBaselineFrom(small, cfg), cfg)
	sigPair := ConflictSignalsOf(pairA, ConflictBaselineFrom(pairA, cfg), cfg)
	sigBig := ConflictSignalsOf(big, ConflictBaselineFrom(big, cfg), cfg)

	if sigSmall.AltRounds != 5 || sigPair.AltRounds != 5 || sigBig.AltRounds != 5 {
		t.Fatalf("三种人群形态的对呛轮数必须一致: %d/%d/%d",
			sigSmall.AltRounds, sigPair.AltRounds, sigBig.AltRounds)
	}
	if sigPair != sigBig {
		t.Fatalf("500 人场景与 3 人场景的读数必须逐字段相同\n pair=%+v\n  big=%+v", sigPair, sigBig)
	}
	if sigSmall.NegDensity != sigPair.NegDensity || sigSmall.MedianGapSec != sigPair.MedianGapSec ||
		sigSmall.Alternating != sigPair.Alternating || sigSmall.MutualAt != sigPair.MutualAt {
		t.Fatalf("N=2 的最小群与 N=3 走同一套公式: small=%+v pair=%+v", sigSmall, sigPair)
	}
	// 唯一的差别是"它自己"这个记号 (B2 的判据), 与群规模无关。
	if !sigSmall.SelfInvolved || sigPair.SelfInvolved {
		t.Fatalf("SelfInvolved 只该由'对呛对里有它自己'决定: small=%v pair=%v",
			sigSmall.SelfInvolved, sigPair.SelfInvolved)
	}
	ctx := ConflictContext{Stage: ConflictStageIntimate}
	if da, db := DecideConflict(sigPair, ctx, cfg), DecideConflict(sigBig, ctx, cfg); da != db {
		t.Fatalf("决策也必须一致: %+v vs %+v", da, db)
	}
	// 反面: 加一条按规模分档的分支 (例如"参与者 > 100 就抬门槛/换方式") →
	//       sigBig 与 sigPair 不再相同 → 红。这正是 A3 要禁的写法。
}

// ——— 词表配置化 ———

func TestConflict_WordListsConfigurable(t *testing.T) {
	cfg := DefaultConflictPolicy()
	cfg.NegativeWords = []string{"呜呜"}
	// 自定义词命中。
	msgs := append(cflTestPrior(cflTestT0), cflTestChillWindow(cflTestT0, false)...)
	msgs[len(msgs)-1].Text = "呜呜呜"
	base := ConflictBaselineFrom(msgs, cfg)
	if sig := ConflictSignalsOf(msgs, base, cfg); sig.NegMsgs == 0 {
		t.Fatalf("自定义负面词应命中: %+v", sig)
	}
	// 内置词在自定义词表下不再算数 (配置化 = 整体替换, 不是叠加)。
	msgs2 := append(cflTestPrior(cflTestT0), cflTestChillWindow(cflTestT0, true)...) // "真无语" = 内置词
	base2 := ConflictBaselineFrom(msgs2, cfg)
	if sig := ConflictSignalsOf(msgs2, base2, cfg); sig.NegMsgs != 0 {
		t.Fatalf("给了自定义词表后内置词不该再命中: %+v", sig)
	}
	// 空词表 = 未提供 → 回落内置。
	cfg2 := DefaultConflictPolicy()
	cfg2.NegativeWords = nil
	base3 := ConflictBaselineFrom(msgs2, cfg2)
	if sig := ConflictSignalsOf(msgs2, base3, cfg2); sig.NegMsgs == 0 {
		t.Fatalf("空词表应回落内置词表: %+v", sig)
	}
	// 反面: 把词表配置化去掉 (永远用内置) → 第一段 NegMsgs 变 0 → 红。
	// 反面: 把"空 = 未提供"改成"空 = 一个词都不算" → 第三段红。
}

// ——— C3 (开放问题, 默认关闭) ———

func TestConflict_HighHistoryRaisesBar(t *testing.T) {
	// C3: 该群本来就互怼 → 把门槛**抬一档** (只许更严)。
	// 默认 HistoryRateRise = 0 = 关闭 (零值 = 未提供 → 走正常路径)。
	msgs, base, cfg := cflTestHistory(cflTestT0, cflTestConflictWindow(cflTestT0))
	sig := ConflictSignalsOf(msgs, base, cfg)
	if sig.HitCount() != 4 {
		t.Fatalf("夹具前提: 四个信号全中, got %d", sig.HitCount())
	}
	// 关掉阈值时, 高历史冲突率不影响判定 —— 连门槛都不该动 (Need 仍是 3)。
	if d := DecideConflict(sig, ConflictContext{Stage: ConflictStageIntimate, PriorConflictRate: 0.9}, cfg); d.Verdict != ConflictIntervene || d.Need != 3 {
		t.Fatalf("C3 默认关闭, 高历史率不该改变结论/门槛, got %s need=%d", d.Verdict, d.Need)
	}
	// 打开后门槛 = 4: 四个信号仍然够 (只是不再有富余)。
	cfg.HistoryRateRise = 0.5
	d := DecideConflict(sig, ConflictContext{Stage: ConflictStageIntimate, PriorConflictRate: 0.9}, cfg)
	if d.Need != 4 || d.Verdict != ConflictIntervene {
		t.Fatalf("抬一档后 4/4 仍应介入: %+v", d)
	}
	// 只有三个信号 + 高历史率 → 被门槛挡下。
	three := sig
	three.IntervalShort = false
	if d := DecideConflict(three, ConflictContext{Stage: ConflictStageIntimate, PriorConflictRate: 0.9}, cfg); d.Verdict != ConflictNormal {
		t.Fatalf("抬门槛后 3/4 不该介入, got %s (%s)", d.Verdict, d.Why)
	}
	// 反面: 把 C3 实现的"抬门槛"改成"干脆不介入" → 第二段 (4 信号) 变 normal → 红。
	// 反面: 把 C3 的零值改成"默认开启" → 第一段变 normal → 红。
}

// ——— 归一化 ———

func TestConflict_MsgFromNormalizesSelf(t *testing.T) {
	ts := cflTestT0
	m := ConflictMsgFrom("12345", "12345", "在吗", []string{"12345", "999"}, ts)
	if m.Uin != ConflictSelfUin || len(m.Ats) != 2 || m.Ats[0] != ConflictSelfUin || m.Ats[1] != "999" {
		t.Fatalf("自己必须归一化成 ConflictSelfUin: %+v", m)
	}
	if m2 := ConflictMsgFrom("999", "", "在吗", nil, ts); m2.Uin != "999" {
		t.Fatalf("未提供 selfUin = 不做映射: %+v", m2)
	}
	// 反面: 把归一化删掉 → Uin 仍是 "12345", B2 永远认不出"它自己是一方" → 红。
}

// ——— 排好序的输入 ———

func TestConflict_UnsortedInputIsSorted(t *testing.T) {
	msgs, base, cfg := cflTestHistory(cflTestT0, cflTestConflictWindow(cflTestT0))
	rev := make([]ConflictMsg, 0, len(msgs))
	for i := len(msgs) - 1; i >= 0; i-- {
		rev = append(rev, msgs[i])
	}
	a := ConflictSignalsOf(msgs, base, cfg)
	b := ConflictSignalsOf(rev, base, cfg)
	if a != b {
		t.Fatalf("顺序不该改变结论 (内部按时间升序): \n a=%+v\n b=%+v", a, b)
	}
	// 反面: 去掉 conflictSorted → 倒序输入下 ①/④ 全乱 → 红。
}
