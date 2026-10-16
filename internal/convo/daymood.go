package convo

// ─────────────────────────────────────────────────────────────
// 无来由的低意愿日
//
// 「真人今天就是不想说话, 而且不解释。」
//
// ⚠️ **为什么必须没有归因**:
// 这个系统里每一处"不想说话"都查得到原因 —— 失眠归因到「白天吵了一架」,
// 累归因到「你今天找了它 12 次」, 心情有 MoodLog 逐条记因。
// 而真人不是这样的: 今天就是不想说, 而且不会解释。
//
// **服务必须可解释, 人不需要。** 一个全部状态都可归因的陪伴体,
// 不管它多像人, 行为模式仍然是服务 —— 这是"客服味"最深的来源。
//
// ⚠️ 所以它:
//   · **不写 MoodLog**, 不进任何 reason 字段, 不进决策轨迹的归因;
//   · **日级**, 不是每条消息掷骰子 (随机抖动看不出效果, 这个项目实测过);
//   · **不解释** —— 提示词里明说"不要解释为什么, 别道歉"。
// ─────────────────────────────────────────────────────────────

// DayMood 今天是什么日子。
type DayMood int

const (
	DayMoodFlat   DayMood = iota // 平常
	DayMoodLow                   // 低意愿日
	DayMoodBright                // 高意愿日
)

const (
	// DayMoodLowBelow / BrightBelow: 概率区间。
	// 低约 15% (一周一天), 高约 10% —— **高意愿日也是必需的**:
	// 只有低谷没有高峰的不是人, 是病人。
	DayMoodLowBelow    = 15
	DayMoodBrightBelow = 25
)

// DayMoodOf 今天是什么日子。
// ⚠️ **确定性**: 同一天永远是同一个值 —— 否则刷新一次人格就变了。
func DayMoodOf(day string) DayMood {
	v := DayRoll("daymood:"+day, 100)
	switch {
	case v < DayMoodLowBelow:
		return DayMoodLow
	case v < DayMoodBrightBelow:
		return DayMoodBright
	}
	return DayMoodFlat
}

// DayMoodDelayFactor 延迟倍率 —— 与身体/疲劳/睡眠**同一条乘法链**。
//
// ⚠️ 落在延迟上是**既有落点**, 不新增机制 (§4.2/§4.3 都是这么落的)。
func DayMoodDelayFactor(m DayMood) float64 {
	switch m {
	case DayMoodLow:
		return 1.6
	case DayMoodBright:
		return 0.85
	}
	return 1.0
}

// DayMoodBlocksAction 这个动作今天要不要被低意愿日拦下。
//
// ⚠️ 抽成纯函数**就是为了能被测**: 埋在 proactive 里的话,
// 「今天不开场」这条唯一的可观察行为就只能靠肉眼——而它一天只发生 15% 的日子。
// ⚠️ 只拦主动开场。中断-恢复是"接着说", 拦它会把"回到刚才那句话"变成又一次消失。
func DayMoodBlocksAction(actionKind string, m DayMood) bool {
	return m == DayMoodLow && actionKind == ActActiveOpen
}

// DayMoodSilencesAtReply 低意愿日连 **@** 都可能不回。
//
// ⚠️ 这是「漏」在这个系统里的**唯一形态**, 而且它刻意的:
// 真人被 @ 也会漏。但要分清 —— 它**不是"没看见"**, 是"看见了, 今天不想说"。
// ⚠️ 危机词仍然短路必答, 那条在调用方, 见 GateGroupMessage。
func DayMoodSilencesAtReply(m DayMood) bool { return m == DayMoodLow }

// DayMoodBypass 这几类消息**不走**低意愿日的减速。
//
// ⚠️ 这是它与"冷暴力"的分界线: 求助、紧急、危机必须秒回。
// 少了这一条, "真人今天不想说话"会退化成"它在我最需要的时候不理我" ——
// 那不是真实, 是坏掉。
func DayMoodBypass(text string, teaching, urgent bool) bool {
	return teaching || urgent || CrisisHit(text)
}

// DayMoodLine 注入文本。**只说行为, 不说原因。**
func DayMoodLine(m DayMood) (string, bool) {
	switch m {
	case DayMoodLow:
		return "【今天】你今天不太想说话。回得短一点, 别主动起话题。" +
			// ⚠️ 措辞里**不许出现**「心情不好」这类词 —— 禁令句会被自己的禁令词表命中
			// (SparkLineIsClean 踩过同一个坑, 这是第二次)。禁令只说"别解释", 不点名。
			"**不要解释为什么, 别道歉, 别给自己找个说法** —— 就只是话少。", true
	case DayMoodBright:
		return "【今天】你今天话比平时多, 想找人聊两句。", true
	}
	return "", false
}
