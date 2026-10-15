package convo

import "testing"

// ——— §6.8 偏向性加入 ———

// TestDecideBias_ClampedToPlusMinusOne **V18** —— 三项**合计**至多 ±1。
//
// 反面: 每项各自 ±1 (合计 ±3) → 已经能盖过整张五条判据表 → 红。
// 与"心情的幅度上限 = 至多减 1"是同一个数量级。
func TestDecideBias_ClampedToPlusMinusOne(t *testing.T) {
	full := BiasInput{
		HasCloseness: true, Closeness: 1.0,
		HasRegard: true, Regard: 1.0,
		HasTaste: true, TasteHit: 1.0,
		Situation: SituationConflict,
	}
	if d := DecideBias(full, DefaultBiasConfig()); d.Delta != 1 {
		t.Fatalf("四项全满也该是 +1 (合计上限), 得到 %d", d.Delta)
	}
	// 只命中一项 → 也是 +1 (幅度上限, 不是累加)。
	one := BiasInput{HasTaste: true, TasteHit: 1.0}
	if d := DecideBias(one, DefaultBiasConfig()); d.Delta != 1 {
		t.Fatalf("单项命中应当是 +1, 得到 %d", d.Delta)
	}
	// 什么都没给 → 0 (零值 = 未提供, 不是"疏远")。
	if d := DecideBias(BiasInput{}, DefaultBiasConfig()); d.Delta != 0 {
		t.Fatalf("零值输入必须是 0 (未提供 ≠ 疏远), 得到 %d", d.Delta)
	}
}

// TestEffectiveMultiplier_GroupAlwaysOne **护栏 5 / B1 / B2 / B3** —— 四条一起钉。
//
// 反面(任一条): 群里也乘倍率 / 零值 scene 按私聊 / 倍率零值当 0× / 不封顶 → 红。
func TestEffectiveMultiplier_GroupAlwaysOne(t *testing.T) {
	mk := func(scene string, mult float64) BiasInput {
		return BiasInput{Speaker: Speaker{Scene: scene}, HasMultiplier: true, Multiplier: mult}
	}
	// ① **群里恒 1.0** —— 不许在群里表现为特殊待遇 (那等于公示主人)。
	if got := EffectiveMultiplier(mk("group:1", 5)); got != 1.0 {
		t.Fatalf("群里生效了倍率 %v —— 那等于在群里公示关系", got)
	}
	// ② **零值 scene 按群处理**, 不按私聊 —— 默认成私聊就是默认给 owner 特权。
	if got := EffectiveMultiplier(mk("", 5)); got != 1.0 {
		t.Fatalf("零值 scene 被当成了私聊 (%v)", got)
	}
	// ③ 私聊生效 + 上限 5×。
	if got := EffectiveMultiplier(mk("private:1", 3)); got != 3.0 {
		t.Fatalf("私聊应当生效, 得到 %v", got)
	}
	if got := EffectiveMultiplier(mk("private:1", 99)); got != 5.0 {
		t.Fatalf("倍率没封顶, 得到 %v — 防止调成舔狗模式", got)
	}
	// ④ **零值倍率 = 1.0, 不是 0 倍**。写反了 owner 会被静默降级为"永不理"。
	none := BiasInput{Speaker: Speaker{Scene: "private:1"}}
	if got := EffectiveMultiplier(none); got != 1.0 {
		t.Fatalf("未设倍率必须是 1.0 (不是 0 倍), 得到 %v", got)
	}
	// ⑤ **矛盾输入**: 调用方说"有倍率", 值却是 0 或负数。
	//
	// ⚠️ **这一条是变异测试逼出来的。** 我第一版只写了 ④ (HasMultiplier=false),
	// 它走的是**另一个分支** —— 把 `Multiplier <= 0 → 1.0` 改成 `→ 0` 之后
	// 测试**照样通过**, 因为那个条件根本没被执行到。
	// 也就是说: "0 倍"这个最危险的写法当时是**没有测试覆盖**的。
	for _, m := range []float64{0, -1, -3.5} {
		bad := BiasInput{Speaker: Speaker{Scene: "private:1"}, HasMultiplier: true, Multiplier: m}
		if got := EffectiveMultiplier(bad); got != 1.0 {
			t.Fatalf("Multiplier=%v 时应当按「未提供」处理 (1.0), 得到 %v"+
				" —— 0 倍会把 owner 静默降级为「永不理」", m, got)
		}
	}
}

// TestDecideBias_FloorBeatsNegative **护栏 1** —— 保底能盖过一切负向。
//
// 设计稿 L976: "对不那么喜欢的人**不许永远沉默**; 偏向不等于忽视"。
// 反面: 保底被负 delta 抵掉 → 红。
func TestDecideBias_FloorBeatsNegative(t *testing.T) {
	in := BiasInput{
		HasCloseness: true, Closeness: 0.0, // 疏远 → 本来要给 -1
		HasMissed: true, MissedSinceLastReply: 20,
	}
	d := DecideBias(in, DefaultBiasConfig())
	if !d.Floor {
		t.Fatal("保底没生效 —— 它会一直忽视一个不那么喜欢的人")
	}
	if d.Delta != 0 {
		t.Fatalf("保底时不该再给负 delta (那会互相抵掉), 得到 %d", d.Delta)
	}
	// 反面: 阈值必须 >= 10 (九成判否), 否则保底比正常接话还频繁。
	if DefaultBiasConfig().FloorMissed < 10 {
		t.Fatalf("保底阈值 %d 太小 —— 与「约九成判否」矛盾, 偏向就是假的", DefaultBiasConfig().FloorMissed)
	}
}

// TestValueWithBias_LandsOnLadderNotDice **V19** —— 权重落在档位上, 不是概率上。
//
// 反面: 把 bias 乘到某个概率上 → 这两条断言会**不确定地**时红时绿 → 红。
func TestValueWithBias_LandsOnLadderNotDice(t *testing.T) {
	cfg := ValueConfig{}
	// hits=4 刚好够门槛, bias=-1 → 掉到 3 → **表情**, 不是沉默。
	d := ValueWithBias(ValueHits{Count: 4}, BiasDecision{Delta: -1}, 4, cfg)
	if d.Stance != StanceReact {
		t.Fatalf("hits=4 bias=-1 应当是表情 (掉一档), 得到 %s", d.Stance)
	}
	// hits=0 + bias=+1 → 1 → 表情 (从沉默抬一档)。
	d2 := ValueWithBias(ValueHits{Count: 0}, BiasDecision{Delta: 1}, 4, cfg)
	if d2.Stance != StanceReact {
		t.Fatalf("hits=0 bias=+1 应当是表情 (抬一档), 得到 %s", d2.Stance)
	}
	// 保底: hits=0 但保底 → **直接到发言门槛**。
	d3 := ValueWithBias(ValueHits{Count: 0}, BiasDecision{Floor: true}, 4, cfg)
	if d3.Stance != StanceSpeak {
		t.Fatalf("保底应当直接抬到发言门槛, 得到 %s", d3.Stance)
	}
	// 同输入 100 次必须完全一致 (纯函数, 无骰子)。
	for i := 0; i < 100; i++ {
		if got := ValueWithBias(ValueHits{Count: 4}, BiasDecision{Delta: -1}, 4, cfg); got != d {
			t.Fatalf("裁决有随机成分: %+v vs %+v", got, d)
		}
	}
}

// TestSituationNoneIsLegal **零值语义的反面** —— SituationNone 是**合法值**。
//
// 与 MoodWillingness 那套"零值 = 未提供"**语义相反**:
// 这里"没事发生"是一个真实的结论, 不是"忘了填"。
// 反面: 给它加 Has* → 调用方每次都要记得填 true → 红。
func TestSituationNoneIsLegal(t *testing.T) {
	in := BiasInput{Situation: SituationNone}
	if d := DecideBias(in, DefaultBiasConfig()); d.Delta != 0 {
		t.Fatalf("没事发生时不该有任何偏移, 得到 %d", d.Delta)
	}
	if d := DecideBias(in, DefaultBiasConfig()); d.Reason != "" {
		t.Fatalf("没事发生不该有归因文字, 得到 %q", d.Reason)
	}
}
