package convo

import "strings"

// ——— §6.8 偏向性加入: 它更愿意接谁的话 ———
//
// 设计稿 L966: 在**价值判断(§6.3)之外**, 说话决策再加三项权重。
// 所以它**不是**第二个闸门, 而是**加在命中数上的一个偏移** ——
//
//	hits      = §6.3 的价值判断 (0..5)
//	biasDelta = §6.8 三项权重 (∈ {-1, 0, +1})
//	保底      = §6.8 护栏 1 (missed >= N → 直接抬到发言门槛)
//	Willingness = 档位表(hits + biasDelta, 保底)
//	Stance      = ValueStanceOf(Willingness)     ← **唯一的档位映射口**
//
// ⚠️ 为什么改计数而不是改阈值:
//  1. **阈值有两个** (发言 / 表情)。升降阈值要同时动两个, 还得保证它们不交叉 ——
//     否则会出现"表情比发言还难"的荒谬档。改计数只需要一条。
//  2. **幅度上限天然可表达**: 三项**合计**至多 ±1, 与
//     "心情的幅度上限 = 至多减 1" 是同一个数量级。
//  3. **可测**: hits=4 且 bias=-1 → 表情 (不是沉默); hits=0 且 bias=+1 → 表情。
//     这两条直接证明"权重落在档位上, 不是概率上"。

// SituationKind §6.8 的"情境"权重。
//
// ⚠️ **零值 SituationNone 是合法值** (确实没有该管的事), 所以这里**刻意不加 Has***。
// 与 MoodWillingness / HasImpulse 那套"零值 = 未提供"**语义相反** ——
// 加了 Has* 会让"没事发生"变成"忘了填"。
type SituationKind string

const (
	SituationNone     SituationKind = ""         // 零值 = 没事发生 (合法)
	SituationConflict SituationKind = "conflict" // §6.9 冲突
	SituationHelp     SituationKind = "help"     // 求助
	SituationSilence  SituationKind = "silence"  // 冷场
)

// Speaker 发言主体 (§6.10.1 的降级版; 单场景期 Scene 恒为私聊)。
type Speaker struct {
	Who      int64  // uin
	Scene    string // "private:<uin>" | "group:<gid>"
	Nickname string
	IsOwner  bool
	Stage    string // 关系阶段; 零值 = 未知 → 按最陌生
}

// BiasInput 三项权重 + 保底的输入。
//
// ⚠️ 每一项都有独立的 Has* —— 缺席 = 这一项还没建/没数据 → **不进计算**;
// 不参与 = 值确实是 0。混起来的后果是"因为一个还没建的功能, 偏向永远按最差算"。
type BiasInput struct {
	Speaker Speaker

	// ① 关系权重 (§5.1 亲密度 / 敬意) —— ⚠️ 当前**不存在**, 客户端填 Has*=false
	Closeness    float64
	HasCloseness bool
	Regard       float64
	HasRegard    bool

	// ② 品味权重 (§2.5 品味轨迹) —— 同上
	TasteHit float64
	HasTaste bool

	// ③ 情境权重 —— 见 SituationKind 的注释 (零值合法, 无 Has*)
	Situation SituationKind

	// §5.6 owner 手设的关系倍率 (默认 1.0, 上限 5×)
	Multiplier    float64
	HasMultiplier bool

	// §6.8 护栏 1 (保底回应率): 距上一次被接住隔了几条用户消息。
	MissedSinceLastReply int
	HasMissed            bool
}

// BiasDecision §6.8 的产出。
type BiasDecision struct {
	Delta int  // ∈ {-1, 0, +1} —— **三项合计**, 不是每项一个
	Floor bool // 是否由保底回应率救回 (护栏 1)
	// Reason 归因: 哪一项把档位推到了这里 (护栏 3「偏向可见」的数据)。
	Reason string
	// EffMult 实际生效的倍率 —— ⚠️ **管理页必须显示这个值, 不是配置值**。
	// 群场景恒为 1.0; 显示配置值的话 owner 会以为群里有特权 (或反之)。
	EffMult float64
}

// BiasConfig 档位表常量。
type BiasConfig struct {
	// FloorMissed 保底阈值: 连续 N 条用户消息没被接住 → 强制抬到发言门槛。
	//
	// ⚠️ **N 必须 >= 10**。设计稿 L900 说价值判断"约九成判否";
	// N 太小会让保底**比正常接话还频繁** —— 那时"偏向"就是假的,
	// 因为它对谁都在接。
	FloorMissed int
	// ClosenessHigh / RegardHigh: 关系够近/够敬重 → +1
	ClosenessHigh float64
	RegardHigh    float64
}

// DefaultBiasConfig 内置档位 (未核实, 需仿真标定)。
func DefaultBiasConfig() BiasConfig {
	return BiasConfig{FloorMissed: 12, ClosenessHigh: 0.6, RegardHigh: 0.6}
}

// EffectiveMultiplier 关系倍率的**唯一读取口** (§5.6 护栏 5 / §6.10.7)。
//
// ⚠️ 两条硬规则都在这里, **不许在别处再乘一次**:
//
//  1. **只作用于私聊** (2026-10-13 用户拍板): 群场景**恒返回 1.0** ——
//     群里的热力上限只由该群场景的阶段决定, 与群里有没有 owner 无关。
//     零值 Scene **按群处理** (恒 1.0), 不按私聊 —— 把它默认成私聊
//     就是默认给 owner 特权, 而那正是这条护栏要防的事。
//  2. 零值 = 未设 → **1.0, 不是 0 倍**。写反了, owner 会被静默降级为"永不理"。
func EffectiveMultiplier(in BiasInput) float64 {
	if SceneKindOf(in.Speaker.Scene) != SceneKindPrivate {
		return 1.0 // 群 / 未知 → 无倍率
	}
	if !in.HasMultiplier || in.Multiplier <= 0 {
		return 1.0
	}
	if in.Multiplier > 5.0 {
		return 5.0 // §5.6: 上限 5× —— 防止调成"舔狗模式"
	}
	return in.Multiplier
}

// DecideBias §6.8 的三项权重 + 保底。**纯函数**: 不读库、不掷骰、不接 LLM。
//
// 幅度: **三项合计至多 ±1** —— 与"心情的幅度上限 = 至多减 1"同一个数量级。
// 每一项各自 ±1 的话, 三项叠起来就是 ±3, 那已经能盖过整张五条判据表。
func DecideBias(in BiasInput, cfg BiasConfig) BiasDecision {
	if cfg.FloorMissed <= 0 {
		cfg.FloorMissed = 12
	}
	d := BiasDecision{EffMult: EffectiveMultiplier(in)}

	// ——— 护栏 1: 保底回应率 ———
	//
	// ⚠️ 它**先算, 且能盖过一切负向** —— 设计稿 L976:
	// "对不那么喜欢的人**不许永远沉默**; 偏向不等于忽视"。
	if in.HasMissed && in.MissedSinceLastReply >= cfg.FloorMissed {
		d.Floor = true
		d.Reason = "保底回应率: 连续 " + itoa(in.MissedSinceLastReply) + " 条没被接住"
		// 保底只保证"接", 不保证"更愿意接" —— 所以不给正 delta。
		return d
	}

	// ——— 三项权重 ———
	var up, down int
	var why []string

	// ① 关系: 喜欢的人 / 敬重的人说话 → 更愿意接。
	if in.HasCloseness && in.Closeness >= cfg.ClosenessHigh {
		up++
		why = append(why, "亲近")
	}
	if in.HasRegard && in.Regard >= cfg.RegardHigh {
		up++
		why = append(why, "敬重")
	}

	// ② 品味: 它感兴趣的方向 → 主动凑。
	// ⚠️ 与 §6.3 判据① **共用同一个品味数据源**, 但**落点各自独立** ——
	// 判据① 决定"这条消息值不值得接", 权重② 决定"我更愿意接谁的话"。不许互相扣减。
	if in.HasTaste && in.TasteHit > 0 {
		up++
		why = append(why, "品味命中")
	}

	// ③ 情境: 该管的情境 → 出手。
	switch in.Situation {
	case SituationConflict:
		up++
		why = append(why, "在吵架")
	case SituationHelp:
		up++
		why = append(why, "求助")
	}

	// 负向只到"不参与": 没有一项命中, 且关系数据明确为"疏远"时才 -1。
	//
	// ⚠️ **绝大多数情况 delta=0** —— 偏向是"更愿意接谁", 不是"更不愿意接谁"。
	// 设计稿 L977 的护栏 2 把负向**上界**钉死在"不参与", 而不是"冷淡"。
	if up == 0 && in.HasCloseness && in.Closeness < cfg.ClosenessHigh/2 {
		down++
		why = append(why, "关系疏远")
	}

	switch {
	case up > 0:
		d.Delta = 1 // **至多 +1**, 不管命中几项
	case down > 0:
		d.Delta = -1
	}
	if len(why) > 0 {
		d.Reason = strings.Join(why, " · ")
	}
	return d
}

// ValueWithBias **唯一的档位合成点**: 把 §6.3 的命中数与 §6.8 的偏移合成三态。
//
// ⚠️ **不许在别处再写一张阈值表** —— §6.8 与 §6.3 各有一张的话,
// 它们迟早在边界上打架, 而那时"为什么这次不说话"就归因不了了。
//
// 档位 (Willingness 是唯一的档位量):
//
//	0 → 沉默 · 1 → 表情 · 2 → 发言 · 3 → 更愿意接
func ValueWithBias(hits ValueHits, bias BiasDecision, need int, cfg ValueConfig) ValueDecision {
	score := hits.Count + bias.Delta
	if score < 0 {
		score = 0
	}
	// 保底: 直接抬到发言门槛 (绕过计分, 但不是绕过门槛本身)。
	if bias.Floor && score < need {
		score = need
	}
	reactMin := reactMinOf(cfg)
	var w int
	switch {
	case score <= 0:
		w = 0
	case score < reactMin:
		w = 0
	case score < need:
		w = 1
	case score >= need+2:
		w = 3
	default:
		w = 2
	}
	return ValueDecision{
		Stance: ValueStanceOf(w),
		Hits:   hits.Count,
		Need:   need,
		Reason: "命中 " + itoa(hits.Count) + " (门槛 " + itoa(need) + ")" +
			biasReasonSuffix(bias),
	}
}

func biasReasonSuffix(b BiasDecision) string {
	switch {
	case b.Floor:
		return " · 保底救回"
	case b.Delta != 0:
		return " · 偏向 " + itoaSigned(b.Delta) + " (" + b.Reason + ")"
	case b.Reason != "":
		return " · " + b.Reason
	}
	return ""
}

func itoaSigned(n int) string {
	if n > 0 {
		return "+" + itoa(n)
	}
	return itoa(n)
}
