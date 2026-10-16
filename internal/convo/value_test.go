package convo

import "testing"

// ——— §6.3 三态裁决 ———
//
// 每条契约配一条反面: "把实现改坏成 X, 这条会红"。

// TestValueHits_AddressedCountedOnce **V2 —— 本组最容易写错的一条**。
//
// 判据② (被@) 与硬门槛「被 @ 必答」**是同一个信号**。
// 两处都计分的后果: 四条门槛被一条信号顶掉两条, "四条以上"形同虚设。
//
// ⚠️ **只断言 Stance 抓不住这条** —— 两种写法都会给出 Speak。
// 必须断言 Count。
func TestValueHits_AddressedCountedOnce(t *testing.T) {
	in := ValueInput{Scene: "group:1", Addressed: true,
		HasTaste: true, HasConflict: true, HasGap: true} // 其余四条全不命中
	h := HitValue(in)
	if h.Count != 1 {
		t.Fatalf("被 @ 只该计 1 次 — 得到 %d (两处都算的话, 四条门槛会被一条信号顶掉两条)", h.Count)
	}
	if !h.Addressed {
		t.Fatal("判据② 本身应当成立")
	}
}

// TestDecideValue_PrivateAlwaysSpeaks **V4** —— §6.3 是**群形态**的判据表。
// 设计稿 L900 把"约九成判否"明写在群聊形态下。
// 反面: 去掉私聊短路 → 私聊 + 全不命中 → silent → 红 (私聊行为回退到"九成不回")。
func TestDecideValue_PrivateAlwaysSpeaks(t *testing.T) {
	in := ValueInput{Scene: "private:owner"}
	d := DecideValue(in, HitValue(in), ValueConfig{})
	if d.Stance != StanceSpeak {
		t.Fatalf("私聊被价值判断拦了 (%s) —— 九成判否限定在群聊形态", d.Stance)
	}
	// 反面: 零值 scene **按群处理** (漏传不该变成给私聊开绿灯)。
	zero := ValueInput{}
	if got := DecideValue(zero, HitValue(zero), ValueConfig{}).Stance; got != StanceSilent {
		t.Fatalf("scene 零值必须按群处理 (更严的那一侧), 得到 %s", got)
	}
}

// TestValueHits_ColdBoundaries **V6 + V7** —— 判据④ 的两个写死的边界。
func TestValueHits_ColdBoundaries(t *testing.T) {
	// ① 新场景 (还没有过上一条消息) **不是冷场**。
	//    写反了会把新群的第一条消息判成"该救场"。
	//
	// 🔴 **这条断言第一版写错了, 是变异测试抓出来的**:
	//    我原本写 `ValueInput{HasGap: false}`, 于是 GapValid 也是零值 false ——
	//    把实现改成"不看 HasGap"它**照样通过**, 因为它被 GapValid 挡住了。
	//    测试恰好测了**另一个字段**, 而那个字段掩盖了它要测的契约。
	//    → 必须显式把 GapValid 置 true, 让它成为唯一的变量。
	fresh := ValueInput{Scene: "group:1", HasGap: false, GapValid: true}
	if HitValue(fresh).Cold {
		t.Fatal("新场景被判成冷场 —— 反过来会把新群第一条消息判成该救场")
	}
	// ② 有上一条消息但**时长还不够** → 也不是冷场 (与①是两个独立的边界)。
	short := ValueInput{Scene: "group:1", HasGap: true, GapValid: false}
	if HitValue(short).Cold {
		t.Fatal("时长不够却判了冷场")
	}
	// ③ 超过上限 = **这个群死了**, 不是冷场。对它出手是"主动找陌生人说话"的反面。
	dead := ValueInput{Scene: "group:1", HasGap: true, GapValid: true, ColdCeiling: true}
	if HitValue(dead).Cold {
		t.Fatal("超过上限仍判冷场 —— 三天没人的群不是冷场, 是群死了")
	}
	// ④ 正常冷场必须判得出来 (过滤不能宽到判据永不成立)。
	normal := ValueInput{Scene: "group:1", HasGap: true, GapValid: true}
	if !HitValue(normal).Cold {
		t.Fatal("正常冷场没判出来")
	}
}

// TestValueHits_RoleFitOnlyAtCriticalPoint 判据⑥ **只在临界点抬一档**。
// 反面: 让它无条件加进五条计数 → 门槛从 4 悄悄降到 3.x (已有契约被新判据改写)。
func TestValueHits_RoleFitOnlyAtCriticalPoint(t *testing.T) {
	// hits == 2 + RoleFit → **不抬**。
	two := ValueInput{Scene: "group:1", Addressed: true, SlangHits: 1, RoleFit: true}
	if got := HitValue(two).Count; got != 2 {
		t.Fatalf("RoleFit 在非临界点也抬了: %d", got)
	}
	// hits == 3 + RoleFit → 抬到 4。
	three := ValueInput{Scene: "group:1", Addressed: true, SlangHits: 1,
		HasGap: true, GapValid: true, RoleFit: true}
	if got := HitValue(three).Count; got != 4 {
		t.Fatalf("临界点上没抬: %d", got)
	}
}

// TestDecideValue_LandsOnLadderNotDice **V10** —— 落点是档位, 不是概率。
//
// 本项目的实测教训 (两次): 把状态乘到抖动概率上完全看不出效果。
// 纯函数 + 无骰子 → 同输入 100 次结果必须**逐字节相同**。
func TestDecideValue_LandsOnLadderNotDice(t *testing.T) {
	cfg := ValueConfig{}
	in := ValueInput{Scene: "group:1", Addressed: true, SlangHits: 1}
	h := HitValue(in)
	first := DecideValue(in, h, cfg)
	for i := 0; i < 100; i++ {
		if got := DecideValue(in, h, cfg); got != first {
			t.Fatalf("裁决有随机成分: %+v vs %+v", got, first)
		}
	}
	// 三态的边界必须**由 hits 决定**, 与任何概率无关。
	in1 := ValueInput{Scene: "group:1", Addressed: true} // 1 条 → 表情
	if got := DecideValue(in1, HitValue(in1), cfg).Stance; got != StanceReact {
		t.Fatalf("1 条命中应当是表情反应, 得到 %s", got)
	}
	in0 := ValueInput{Scene: "group:1"} // 0 条 → 沉默
	if got := DecideValue(in0, HitValue(in0), cfg).Stance; got != StanceSilent {
		t.Fatalf("0 条命中应当沉默, 得到 %s", got)
	}
}

// TestValue_MissingCriteriaShrinkThreshold **V16 + V17** —— 缺席判据折算分母, 但要有下限。
//
// 不折算的后果: 因为一个**还没建的功能**(品味轨迹), "四条以上"变成事实上的永不发言 ——
// 那是一个安静的默认值 bug。
// 没下限的后果: 只剩一条判据时退化成"命中就发言"。
func TestValue_MissingCriteriaShrinkThreshold(t *testing.T) {
	cfg := ValueConfig{}
	// 三条判据缺席 (Taste / Conflict / Gap) → 只剩 2 条可用 → 门槛降到 2。
	sparse := ValueInput{Scene: "group:1"}
	d := DecideValue(sparse, HitValue(sparse), cfg)
	if d.Need != 2 {
		t.Fatalf("缺席判据没折算门槛: Need=%d, 期望 2", d.Need)
	}
	// 折算后的门槛必须**真的生效**: 命中 2 条就该发言。
	s := ValueInput{Scene: "group:1", Addressed: true, SlangHits: 1}
	if got := DecideValue(s, HitValue(s), cfg).Stance; got != StanceSpeak {
		t.Fatalf("折算后的门槛没生效: 命中 2 条得到 %s", got)
	}
	// 下限: Need 绝不许降到 1。
	if d.Need < 2 {
		t.Fatalf("Need 跌破下限 2 (%d) —— 那会退化成「命中就发言」", d.Need)
	}
	// 判据齐全时门槛必须回到 4 (折算不能变成"永远宽松")。
	full := ValueInput{Scene: "group:1", HasTaste: true, HasConflict: true, HasGap: true}
	if got := DecideValue(full, HitValue(full), cfg).Need; got != 4 {
		t.Fatalf("判据齐全时门槛应当是 4, 得到 %d", got)
	}
}

// TestValueStanceOf_IsTheOnlyMapping **§6.8 的接口: 唯一档位映射口**。
// 反面: 在别处再写一张阈值表 → 两张表迟早在边界上打架,
// 而那时"为什么这次不说话"就归因不了了。
func TestValueStanceOf_IsTheOnlyMapping(t *testing.T) {
	cases := []struct {
		w    int
		want SpeakStance
	}{{0, StanceSilent}, {1, StanceReact}, {2, StanceSpeak}, {3, StanceSpeak}, {-1, StanceSilent}}
	for _, c := range cases {
		if got := ValueStanceOf(c.w); got != c.want {
			t.Fatalf("ValueStanceOf(%d) = %s, 期望 %s", c.w, got, c.want)
		}
	}
}

// ⚠️ 2026-10-16: `TestStickerOnlyFrame_Shape` **已删**, 连同它测的 `StickerOnlyFrame`。
//
// §6.1 第二态**一直是通的** —— 但它的发射器不是帧, 而是 `Engine.ReactInGroup`
// (main.go 的 `case convo.StanceReact:` 分支 → `GroupSender.SendGroupImage`)。
// 那个帧构造器没有任何消费者: 留着它, 下一个人会去接帧那条路, 然后困惑于"为什么发不出去"。
// 真正要钉的不变量在 `ReactInGroup` 里 (文件真的在才发 / 一张能用的都没有就**什么都不做**,
// 不许退化成发文本), 而那一条由 value 侧的用例覆盖。
