package convo

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tzhnahida/VocabLock/internal/clock"
	"github.com/tzhnahida/VocabLock/internal/log"
)

// ——— §6.9 群内职责与冲突介入 (叶子模块) ———
//
// 这一层只回答两个问题:
//  1. 窗口里这些消息算不算"冲突"? —— 四个**无 LLM 可算**的信号 (纪律 #6: 判据必须能算)
//  2. 算的话, 它该不该出声、以什么方式出声? —— §5.2 的 A/B/C 三类"不该介入"
//
// 三条纪律在本文件里的落点:
//
//	#4 派生量不落库: 四个信号、基线、冷却**全部现算**。唯一入库的是"介入过"这个事实
//	   (convo_conflict_action) —— 因为"劝不动就撤"需要一个跨 tick 的记忆, 而它是**事实**
//	   (某时刻以某方式介入过某一对), 不是派生量。冷却窗口本身仍然是现算的 (acted_ts + cooldown_min)。
//
//	#3 失败往"正常说话"倒: 读库失败 = **系统故障** → 照常走正常对话 (ConflictNormal);
//	   只有**语义判不出**才沉默 (ConflictSilent)。这两件事在 ResolveConflict / GateConflict 里
//	   被显式分成两条通道 —— 用"判不出来 → 沉默"顺手吞掉读库失败, 正是规格 R28 点名的写法。
//
//	#1 零值 = 未提供: 配置里所有数值 ≤0 走默认; 运行时的空 stage / nil affinity 一律解释成
//	   "未提供" → **不施加该类抑制** (而不是"等于最保守档", 那会让零值恰好等于"永远不劝")。
//
// A3 裁定 (不定向): 本文件**没有任何**关于群规模的分支 —— 没有 groupSize / isBigGroup /
// "典型群" 这类输入。规模只能通过"窗口内有多少条消息、谁说的、间隔多少"进入同一张档位表,
// 所以 N=2 (只有 owner + 它) 与 N=500 走的是**同一个函数、同一组默认值**。

// ConflictSelfUin 是"这条是 convo 自己"的记号 —— 承 convo_messages.sender_uin 的空串约定
// (它自己在库里就是空串)。调用方用 ConflictMsgFrom 做归一化, 判定内部只认这一个记号。
const ConflictSelfUin = ""

// conflictTSLayout 与 clock.TodayTS / 既有列一致, 直接可比大小 (SQLite 的 MAX 走字典序)。
const conflictTSLayout = "2006-01-02 15:04:05"

// ErrConflictCooldown 同一对在冷却窗口内已经介入过 —— §6.9:991「劝不动就撤」。
// 它是**拒绝写**, 不是故障: 调用方看到它应当作"这次不介入", 不要重试。
var ErrConflictCooldown = errors.New("convo/conflict: 同一对在冷却窗口内已介入过")

// ——— 配置 ———

// ConflictPolicy 对应配置 group.conflict.* (§2.2)。
//
// 词表**必须配置化**: 只有 nil / 空切片才回落到内置小词表 —— 空切片是"未提供",
// 不是"一个词都不算" (后者会让冲突判定静默失灵, 且从配置上看不出来)。
//
// ⚠️ 未核实 (§8 U6): 设计稿完全没有给词条, 也没给 familiarity / history 这两个门槛。
// 下方内置词表与 FamiliarAffinityMin 都是我拟的, 需要语料支撑后再调。
type ConflictPolicy struct {
	WindowMin       int      `yaml:"window_min"`       // <=0 → 10 分钟
	MinMsgs         int      `yaml:"min_msgs"`         // <=0 → 6 (样本不足 → 不判)
	MinRounds       int      `yaml:"min_rounds"`       // <=0 → 5 (① 严格交替的最小轮数)
	NegativeDensity float64  `yaml:"negative_density"` // <=0 → 0.40 (③ 的密度阈值)
	IntervalRatio   float64  `yaml:"interval_ratio"`   // <=0 → 0.50 (④ 相对基线的比例)
	SignalsRequired int      `yaml:"signals_required"` // <=0 → 3; >4 → 4 (四信号封顶)
	CooldownMin     int      `yaml:"cooldown_min"`     // <=0 → 120 (A5)
	NegativeWords   []string `yaml:"negative_words"`   // 空 → 内置
	EscalateWords   []string `yaml:"escalate_words"`   // 空 → 内置 (人身攻击/歧视/安全)
	// MediationWords 是 §5.3 硬线 5「不把'我在劝你们'说出来」的判据词表 (校验链用)。
	// 规格给了硬线没给词条 —— 与上面两张表同一条说明: 我拟的, 未核实。
	MediationWords []string `yaml:"mediation_words"`
	// FamiliarAffinityMin 是 A3「跟双方都不熟 → 不劝架」的门槛 (场景级熟悉度)。未核实。
	FamiliarAffinityMin float64 `yaml:"familiar_affinity_min"`
	// HistoryRateRise 是 C3 (§5.2 列为开放问题): 该群本来就互怼 → 把信号门槛**抬一档**。
	// <=0 = **关闭** (零值 = 未提供 → 走正常路径), 不是"0 就是高冲突率"。
	HistoryRateRise float64 `yaml:"history_rate_rise"`
	// ModeByStage 是劝和方式的**档位表** (纪律 #7: 档位表, 不是连续公式)。
	// 表缺失 → 内置表; 表内某项为空 → 取该键的内置值 (承 §2.4 的零值精化)。
	ModeByStage map[string]string `yaml:"mode_by_stage"`
}

// 场景阶段 (§5.6 的热力 cap 用同一组名字, 不要另起一套)。
const (
	ConflictStageStranger = "stranger" // 陌生 → A1: 有冲突也不掺和
	ConflictStageFamiliar = "familiar"
	ConflictStageIntimate = "intimate"
)

// 劝和方式 (§6.9:990 原话的三种) + 硬线 4 的上报。
const (
	ConflictModeDivert = "divert" // 转移话题 (发个无关的梗或图) —— 最轻
	ConflictModeAbsorb = "absorb" // 把温度吸过来 (自嘲)
	ConflictModeDM     = "dm"     // 私下问一句 —— 最深
	ConflictModeReport = "report" // 不劝和, 上报 owner (§6.9 硬线 4) —— **不是劝和三式之一**
)

// conflictDefaultModeByStage 是内置档位表: 关系越深, 能用的方式越"贴"。
// stranger 那一档其实到不了 (A1 先拦), 仍然给最轻的一档 —— 万一有人绕过 A1, 也不许落到 dm。
var conflictDefaultModeByStage = map[string]string{
	ConflictStageStranger: ConflictModeDivert,
	ConflictStageFamiliar: ConflictModeAbsorb,
	ConflictStageIntimate: ConflictModeDM,
}

// conflictDefaultNegativeWords ⚠️ 未核实 (§8 U6: 设计稿没有词表)。
//
// 选词标准: 中文群聊里**指向人**的负面表达, 尽量不把普通吐槽算进来。
// 语料支撑之前这只能是起点 —— 这也是为什么它必须可配置。
var conflictDefaultNegativeWords = []string{
	"滚", "闭嘴", "有病", "神经病", "傻", "脑残", "智障", "垃圾", "恶心", "烦人",
	"无语", "服了", "离谱", "过分", "讨厌", "别来了", "拉黑", "阴阳怪气",
}

// conflictDefaultEscalateWords ⚠️ 未核实 (同上)。人身攻击 / 歧视 / 安全三条硬线。
var conflictDefaultEscalateWords = []string{
	"去死", "死全家", "弄死", "打死你", "废物", "贱", "骚", "杂种", "娘炮",
	"开盒", "人肉", "威胁", "自杀", "自残", "砍死",
}

// conflictDefaultMediationWords ⚠️ 未核实 (同上)。硬线 5 的判据:
// **它不能让人看出"我在劝你们"** —— 一旦用嘴说出来, 劝和本身就变成新的社交事故。
var conflictDefaultMediationWords = []string{
	"我劝", "劝你们", "别吵", "别打了", "都冷静", "冷静一下", "消消气",
	"各退一步", "和和气气", "大家都是朋友", "别为这种事",
}

// DefaultConflictPolicy 是内置默认档 (§2.2 的数值 + 我拟的三张词表)。
func DefaultConflictPolicy() ConflictPolicy {
	return ConflictPolicy{
		WindowMin:           10,
		MinMsgs:             6,
		MinRounds:           5,
		NegativeDensity:     0.40,
		IntervalRatio:       0.50,
		SignalsRequired:     3,
		CooldownMin:         120,
		FamiliarAffinityMin: 0.35, // 未核实
		HistoryRateRise:     0,    // C3 默认关闭 (开放问题)
		NegativeWords:       append([]string(nil), conflictDefaultNegativeWords...),
		EscalateWords:       append([]string(nil), conflictDefaultEscalateWords...),
		MediationWords:      append([]string(nil), conflictDefaultMediationWords...),
		ModeByStage:         cloneConflictModes(conflictDefaultModeByStage),
	}
}

func cloneConflictModes(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// Normalized 把零值/非法值收成有效档。
//
// **零值 = 未提供**: 每个 <=0 的数值都换成默认, 而不是"取 0"。
// 这一条不是洁癖 —— signals_required 取 0 会让**任何**窗口都判冲突,
// min_msgs 取 0 会让"两条秒回的冷清群"判冲突 (规格 ④ 明确要防的正是这个)。
func (p ConflictPolicy) Normalized() ConflictPolicy {
	d := DefaultConflictPolicy()
	if p.WindowMin > 0 {
		d.WindowMin = p.WindowMin
	}
	if p.MinMsgs > 0 {
		d.MinMsgs = p.MinMsgs
	}
	if p.MinRounds > 0 {
		d.MinRounds = p.MinRounds
	}
	if p.NegativeDensity > 0 {
		d.NegativeDensity = conflictClamp01(p.NegativeDensity)
	}
	if p.IntervalRatio > 0 {
		d.IntervalRatio = conflictClamp01(p.IntervalRatio)
	}
	if p.SignalsRequired > 0 {
		d.SignalsRequired = p.SignalsRequired
	}
	if d.SignalsRequired > 4 {
		d.SignalsRequired = 4 // 只有四个信号, 要求 5 个 = 永不判冲突 (那是静默失灵, 不是"更严")
	}
	if p.CooldownMin > 0 {
		d.CooldownMin = p.CooldownMin
	}
	if p.FamiliarAffinityMin > 0 {
		d.FamiliarAffinityMin = p.FamiliarAffinityMin
	}
	if p.HistoryRateRise > 0 {
		d.HistoryRateRise = p.HistoryRateRise
	}
	if len(p.NegativeWords) > 0 {
		d.NegativeWords = append([]string(nil), p.NegativeWords...)
	}
	if len(p.EscalateWords) > 0 {
		d.EscalateWords = append([]string(nil), p.EscalateWords...)
	}
	if len(p.MediationWords) > 0 {
		d.MediationWords = append([]string(nil), p.MediationWords...)
	}
	if len(p.ModeByStage) > 0 {
		m := cloneConflictModes(d.ModeByStage)
		for k, v := range p.ModeByStage {
			if v != "" {
				m[k] = v
			}
		}
		d.ModeByStage = m
	}
	return d
}

func conflictClamp01(v float64) float64 {
	if v > 1 {
		return 1
	}
	if v < 0 {
		return 0
	}
	return v
}

// requiredSignals 本轮实际要求的信号数。
//
// C3 (未核实, §5.2 列为开放问题): 该场景历史冲突率高于门槛 → **抬**一档。
// 方向上只许更严, 不许更松 —— "这个群本来就互怼" 是**别把常态当冲突**的理由,
// 任何把它实现成"降低门槛 / 干脆不劝"的写法, 都会把一个热闹群变成两个都不管。
func (p ConflictPolicy) requiredSignals(ctx ConflictContext) int {
	c := p.Normalized()
	n := c.SignalsRequired
	if c.HistoryRateRise > 0 && ctx.PriorConflictRate >= c.HistoryRateRise {
		n++
		if n > 4 {
			n = 4
		}
	}
	return n
}

// modeForStage 查档位表。未提供 / 未知档 → **最轻的一档** (divert)。
// 零值不许落到 dm: "私下问一句"是关系最深才用的, 拿它当兜底等于对陌生人掏心窝子。
func (p ConflictPolicy) modeForStage(stage string) string {
	c := p.Normalized()
	if m, ok := c.ModeByStage[stage]; ok && m != "" {
		return m
	}
	return ConflictModeDivert
}

// ——— 输入 ———

// ConflictMsg 是判据的一条输入消息 (**必须按时间升序传入** —— 四个信号里有三个靠顺序)。
//
// Uin 用 ConflictSelfUin (空串) 表示 convo 自己; Ats 里同样用空串表示"@了它"。
// 调用方不要手工拼这个约定, 用 ConflictMsgFrom 归一化。
type ConflictMsg struct {
	Uin  string
	Text string
	Ats  []string
	TS   time.Time
}

// ConflictMsgFrom 把平台侧一条消息归一化成判据输入 (selfUin = 平台上的自己, 可为空)。
//
// 归一化必须发生在**进判定之前**: 判定内部只认 ConflictSelfUin 一个记号,
// 否则"它自己是不是冲突的一方" (B2) 会随平台账号形态漂移。
func ConflictMsgFrom(uin, selfUin, text string, ats []string, ts time.Time) ConflictMsg {
	norm := func(u string) string {
		if selfUin != "" && u == selfUin {
			return ConflictSelfUin
		}
		return u
	}
	m := ConflictMsg{Uin: norm(uin), Text: text, TS: ts}
	for _, a := range ats {
		m.Ats = append(m.Ats, norm(a))
	}
	return m
}

// ConflictBaseline 是该场景的基线读数 —— **现算, 不落库** (§5.1: 基线从哪来)。
type ConflictBaseline struct {
	PrevMsgs    int     // 前一个同长窗口的消息数 (0 = 没有前窗 → "上升"无从谈起)
	PrevNegMsgs int     // 前窗里含负面词的消息条数
	PrevDensity float64 // 前一个同长窗口的负面密度
	MedGapSec   float64 // 最近 K 个同长窗口的**中位间隔的中位数**
	HasGap      bool    // 是否真的算出了基线间隔 (样本足够)
	Windows     int     // 参与基线计算的窗口数 (K, 现算)
}

// ConflictBaselineWindows 是基线回看的窗口数上限 (K)。
// ⚠️ 未核实: 规格只说"最近 K 个同长窗口", 没给 K。取 6 = 一小时的回看。
const ConflictBaselineWindows = 6

// ConflictContext 是**场景侧**的已知事实 (不是消息内容)。
//
// 每个"未提供"都写成零值, 且语义是"不施加该类抑制" —— 除了 Undecidable,
// 它的零值 false 表示"判得出", 那正是我们要的正常路径。
type ConflictContext struct {
	SceneKey string
	// Stage 是**场景阶段** (convo_scene.stage)。空 = 未提供 → A1 不生效。
	Stage string
	// SelfRole 是平台角色 owner|admin|member。
	// ⚠️ **它不参与 A1** —— §5.2 A1 的原话是"无论它的平台角色是 admin 还是 member"。
	// 这个字段留着只为轨迹/日志, 判定一行都不读它 (读了就会写出"admin 就能劝架")。
	SelfRole string
	// SelfIsNew: 它在场景的 is_new 窗口内 (刚进群) → A2。
	SelfIsNew bool
	// Affinity: 它在**本场景**对每个人的熟悉度。
	// nil = 未提供 → A3 不生效; 非 nil 但缺某个 uin = 查过了, 不熟 (0)。
	// (承 §2.4 的精化: 零值语义按"整个键是否存在"判定, 不按单个条目的数值判定。)
	Affinity map[string]float64
	// MentionedSelf: 它正被 @ / 被问 → A4: 线程归属优先于冲突介入 (§6.10.2 ①)。
	MentionedSelf bool
	// SelfInPair: 调用方**已经知道**它自己就是冲突的一方 (B2 的显式通道)。
	// 判定自己也会从窗口现算一个 (见 ConflictSignals.SelfInvolved), 两个取或。
	SelfInPair bool
	// Cooldown: 同一 pair 还在 cooldown_min 内 (由 ConflictCooldownActive 现算后填)。
	Cooldown bool
	// PriorConflictRate: 该场景历史冲突率 ∈ [0,1] (§5.2 C3, 未核实 / 默认关闭)。
	PriorConflictRate float64
	// Undecidable: **语义判不出** (连"谁在跟谁吵"都认不出) → 沉默 (§6.10.2 ④)。
	//
	// ⚠️ 它 != 读库失败。读库失败走 ResolveConflict / GateConflict 的 err 通道,
	// 那条路必须往**说话**倒 (纪律 #3)。把两者并成一个 bool 就是 R28。
	Undecidable bool
}

// ——— 输出 ———

// ConflictVerdict 是本模块对外的唯一结论。
type ConflictVerdict string

const (
	// ConflictNormal: 不介入 —— 但**照常走正常说话路径**。它不等于沉默。
	ConflictNormal ConflictVerdict = "normal"
	// ConflictIntervene: 介入劝和, Mode ∈ {divert, absorb, dm}。
	ConflictIntervene ConflictVerdict = "intervene"
	// ConflictEscalate: 命中硬线 4 (人身攻击/歧视/安全) → **不劝和**, 上报 owner。
	ConflictEscalate ConflictVerdict = "escalate"
	// ConflictSelfSide: 它自己就是冲突的一方 —— **不是"介入", 是"冲突处理"**,
	// 必须走 §5.5 (违抗/拒绝/道歉找补), 严禁走 §6.9 的劝和话术。
	ConflictSelfSide ConflictVerdict = "self_side"
	// ConflictSilent: 语义判不出 → 沉默。**只有这一条路会沉默。**
	ConflictSilent ConflictVerdict = "silent"
)

// ConflictDecision 是决策结果。Why 是给人看的理由 (进日志/轨迹)。
type ConflictDecision struct {
	Verdict   ConflictVerdict
	Mode      string // Verdict=intervene / escalate 时有意义
	Pair      string // 冲突的那一对 (pair_key: 两个 uin 排序拼接)
	Escalated bool   // 命中硬线 4 (即使 Verdict=self_side 也可能为真)
	Need      int    // 本轮实际要求的信号数 (C3 抬门槛后)
	Hits      int    // 实际命中几个
	Why       string
}

// Speaking 报告这次决策是否落在"正常说话"这一侧 (不沉默)。
// 调用方用它回答 R28 那个问题: 系统故障**绝不能**变成不说话。
func (d ConflictDecision) Speaking() bool { return d.Verdict != ConflictSilent }

// ——— 四个信号 (全部现算) ———

// ConflictSignals 是四个信号的现算结果 (纪律 #4: 不落库)。
// 它同时是读数: "为什么这次判了冲突"必须能只从这几个数里读出来。
type ConflictSignals struct {
	WindowMsgs int
	WindowFrom time.Time
	WindowTo   time.Time

	AltRounds   int    // ① 最长严格交替段的**轮数** (= 段长-1, 即交替次数)
	AltA, AltB  string // ① 那一对
	AltPair     string // = ConflictPairKey(AltA, AltB)
	Alternating bool   // ① 命中 (AltRounds >= min_rounds)

	MutualAt bool   // ② a@b 且 b@a
	AtPair   string // ② 那一对

	NegMsgs        int     // ③ 窗口内含负面词的**消息条数** (不是词频)
	NegDensity     float64 // = NegMsgs / WindowMsgs
	PrevNegDensity float64 // 前一个同长窗口的同值
	DensityRise    bool    // ③ 命中 (>= 阈值 **且** 高于前窗)

	MedianGapSec   float64 // ④ 窗口内相邻消息间隔的中位数
	BaselineGapSec float64 // 该场景最近 K 个同长窗口的中位数
	HasGap         bool
	IntervalShort  bool // ④ 命中

	Escalate     bool // B1: 窗口里命中 escalation 词表
	SelfInvolved bool // B2: 它自己卷进去了 (对呛那一对里有它 / 被点名挨骂 / 它在火里说过话)
}

// HitCount 命中几个信号 (四个布尔量; 阈值已经烘进布尔量里了)。
func (s ConflictSignals) HitCount() int {
	n := 0
	for _, b := range []bool{s.Alternating, s.MutualAt, s.DensityRise, s.IntervalShort} {
		if b {
			n++
		}
	}
	return n
}

// PairKey 这次冲突落在哪一对上 (优先 ① 的对呛对; 没有就用 ② 的互 @ 对)。
func (s ConflictSignals) PairKey() string {
	if s.AltPair != "" {
		return s.AltPair
	}
	return s.AtPair
}

// ConflictPairKey 是 pair_key: 两个 uin **排序后**拼接 —— 与谁先开口无关,
// 所以 (A,B) 与 (B,A) 是同一把冷却钥匙 (A5 的"劝不动就撤"按人对记, 不按发言顺序记)。
func ConflictPairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "<" + b
}

// ConflictSignalsOf 现算四个信号。
//
// msgs 传该场景的历史 (升序), base 传基线 —— 两者都由调用方给; 本函数不读库、不读时钟,
// 所以同样的输入必得同样的结论 (仿真可复现, 测试可断言)。
func ConflictSignalsOf(msgs []ConflictMsg, base ConflictBaseline, cfg ConflictPolicy) ConflictSignals {
	c := cfg.Normalized()
	all := conflictSorted(msgs)
	var sig ConflictSignals
	if len(all) == 0 {
		return sig
	}
	end := all[len(all)-1].TS
	d := time.Duration(c.WindowMin) * time.Minute
	win := conflictWindow(all, end.Add(-d), end)
	sig.WindowFrom, sig.WindowTo = end.Add(-d), end
	sig.WindowMsgs = len(win)

	sig.AltRounds, sig.AltA, sig.AltB = conflictAlternation(win)
	if sig.AltRounds > 0 {
		sig.AltPair = ConflictPairKey(sig.AltA, sig.AltB)
	}
	sig.Alternating = sig.AltRounds >= c.MinRounds

	sig.MutualAt, sig.AtPair = conflictMutualAt(win)

	sig.NegMsgs = conflictNegMsgs(win, c.NegativeWords)
	if sig.WindowMsgs > 0 {
		sig.NegDensity = float64(sig.NegMsgs) / float64(sig.WindowMsgs)
	}
	sig.PrevNegDensity = base.PrevDensity
	// ③ 是**相对量**: 绝对阈值会把一个本来就在吐槽的群判成冲突 (§5.1 原话)。
	// 没有前窗 (PrevMsgs=0) → "上升"无从谈起 → 不命中 (这是"证据不足", 不是"沉默")。
	sig.DensityRise = sig.WindowMsgs > 0 &&
		sig.NegDensity >= c.NegativeDensity &&
		base.PrevMsgs > 0 &&
		sig.NegDensity > base.PrevDensity

	sig.MedianGapSec, sig.HasGap = conflictMedianGap(win)
	sig.BaselineGapSec = base.MedGapSec
	// ④ 也要 min_msgs: 冷清群里两条秒回不是冲突, 是没人说话。
	sig.IntervalShort = sig.HasGap && base.HasGap && base.MedGapSec > 0 &&
		sig.WindowMsgs >= c.MinMsgs &&
		sig.MedianGapSec <= base.MedGapSec*c.IntervalRatio

	sig.Escalate = conflictNegMsgs(win, c.EscalateWords) > 0
	sig.SelfInvolved = conflictSelfInvolved(win, sig, c.NegativeWords)
	return sig
}

// conflictSelfInvolved 现算"它自己是不是这场火里的人" (B2)。
//
// 三条路取或 —— 宁可多认一分, 也不能漏:
//   - 对呛那一对里有它 (最硬的一条);
//   - 有人 @ 它并且那条是负面的 (被点名挨骂);
//   - 窗口里它说过话**且**负面密度在上升 (它就站在火里)。
//
// 漏判的代价是"它给自己打圆场" —— 规格点名严禁的那种话术;
// 多判的代价只是它这次不劝架 (走 §5.5 / 正常说话)。代价不对称, 所以偏保守。
func conflictSelfInvolved(win []ConflictMsg, sig ConflictSignals, negWords []string) bool {
	if sig.AltRounds > 0 && (sig.AltA == ConflictSelfUin || sig.AltB == ConflictSelfUin) {
		return true
	}
	for _, m := range win {
		if m.Uin == ConflictSelfUin {
			continue
		}
		if conflictAt(m, ConflictSelfUin) && conflictHasWord(m.Text, negWords) != "" {
			return true
		}
	}
	if sig.DensityRise {
		for _, m := range win {
			if m.Uin == ConflictSelfUin {
				return true
			}
		}
	}
	return false
}

// ConflictBaselineFrom 现算基线 (不落库, §5.1)。
//
// 前一个同长窗口给 ③ 的"上升"当分母; 最近 K 个同长窗口的中位间隔给 ④ 当分母。
// K = 有多少算多少 (上限 ConflictBaselineWindows), **不落库也不配置** ——
// 它是"这段时间这个场景有多快"的现算读数。
func ConflictBaselineFrom(msgs []ConflictMsg, cfg ConflictPolicy) ConflictBaseline {
	c := cfg.Normalized()
	all := conflictSorted(msgs)
	var base ConflictBaseline
	if len(all) == 0 {
		return base
	}
	end := all[len(all)-1].TS
	d := time.Duration(c.WindowMin) * time.Minute

	prev := conflictWindow(all, end.Add(-2*d), end.Add(-d))
	base.PrevMsgs = len(prev)
	base.PrevNegMsgs = conflictNegMsgs(prev, c.NegativeWords)
	if base.PrevMsgs > 0 {
		base.PrevDensity = float64(base.PrevNegMsgs) / float64(base.PrevMsgs)
	}

	var medians []float64
	for k := 1; k <= ConflictBaselineWindows; k++ {
		w := conflictWindow(all, end.Add(-time.Duration(k+1)*d), end.Add(-time.Duration(k)*d))
		if len(w) < 2 {
			continue
		}
		if med, ok := conflictMedianGap(w); ok {
			medians = append(medians, med)
			base.Windows++
		}
	}
	if len(medians) > 0 {
		base.MedGapSec = conflictMedian(medians)
		base.HasGap = true
	}
	return base
}

// DetectConflict 是规格要求的判定入口: 四信号命中 >= signals_required → 冲突。
//
// 它是**纯判据**: 只说"算不算冲突", 不说"该不该介入" (那是 DecideConflict)。
// C1/C2 在这里落地 —— 命中不足或样本不足都返回 false, 理由写清楚。
func DetectConflict(msgs []ConflictMsg, base ConflictBaseline, cfg ConflictPolicy) (bool, string) {
	c := cfg.Normalized()
	sig := ConflictSignalsOf(msgs, base, c)
	if sig.WindowMsgs < c.MinMsgs {
		// C2: 样本不足。冷清群里什么都判不出来, 不判就是对的。
		return false, fmt.Sprintf("样本不足: 窗口 %d 条 < %d 条 (C2)", sig.WindowMsgs, c.MinMsgs)
	}
	need := c.SignalsRequired
	hit := sig.HitCount() >= need
	return hit, fmt.Sprintf("窗口 %d 条 · 命中 %d/%d 个信号: %s", sig.WindowMsgs, sig.HitCount(), need, sig.detail())
}

// detail 把命中的信号翻成人话 (未命中的不列, 否则日志里全是噪声)。
func (s ConflictSignals) detail() string {
	var parts []string
	if s.Alternating {
		parts = append(parts, fmt.Sprintf("连续对呛 %d 轮(%s)", s.AltRounds, s.AltPair))
	}
	if s.MutualAt {
		parts = append(parts, "互相 @"+s.AtPair)
	}
	if s.DensityRise {
		parts = append(parts, fmt.Sprintf("负面密度上升 %.2f>%.2f", s.NegDensity, s.PrevNegDensity))
	} else if s.NegDensity > 0 {
		parts = append(parts, fmt.Sprintf("负面密度 %.2f(前窗 %.2f, 未上升)", s.NegDensity, s.PrevNegDensity))
	}
	if s.IntervalShort {
		parts = append(parts, fmt.Sprintf("回复间隔骤短 %.0fs<=%.0fs", s.MedianGapSec, s.BaselineGapSec))
	}
	if s.Escalate {
		parts = append(parts, "命中硬线词表")
	}
	// ⚠️ **窗口边界也要说** —— 这两个字段原来是"只写不读"的,
	// 而这个结构体的自述是「它同时是读数: 为什么这次判了冲突必须能只从这几个数里读出来」。
	//
	// 少了它, 日志里只看到"命中 3/3", 看不出**判的是哪一段** ——
	// 而"窗口取错了"恰恰是这一节最容易出的错(它按时间切, 不按条数切)。
	if !s.WindowFrom.IsZero() && !s.WindowTo.IsZero() {
		parts = append(parts, fmt.Sprintf("窗口 %s~%s 共 %d 条",
			s.WindowFrom.Format("15:04"), s.WindowTo.Format("15:04"), s.WindowMsgs))
	}
	if len(parts) == 0 {
		parts = append(parts, "无信号命中")
	}
	return strings.Join(parts, "; ")
}

// ——— 决策: 该不该介入, 以什么方式 ———

// DecideConflict 把"信号"+"场景事实"翻成一个结论。
//
// 分支顺序是**承重的**, 不是随手排的:
//  1. 语义判不出 → 沉默 (唯一的沉默出口);
//  2. 不构成冲突 → 正常说话 (C1/C2);
//  3. 硬线 / 当事人在**资格判据之前** —— 因为"没资格劝架"管的是**劝架**这件事:
//     它自己被骂了不该因为"跟对方不熟"就闭嘴 (那是 §5.5 的事, 不是劝架);
//     人身攻击/歧视/安全线同理: 不掺和 ≠ 知情不报。
//     (这一步是我的判断, 见交付里的"不确定"。)
//  4. A 类 (没资格) → 正常说话;
//  5. 否则 → 按档位表介入。
func DecideConflict(sig ConflictSignals, ctx ConflictContext, cfg ConflictPolicy) ConflictDecision {
	c := cfg.Normalized()
	need := c.requiredSignals(ctx)
	d := ConflictDecision{Pair: sig.PairKey(), Need: need, Hits: sig.HitCount()}

	if ctx.Undecidable {
		d.Verdict = ConflictSilent
		d.Why = "语义判不出 (谁说的/对谁说的都认不出) → 沉默 (§6.10.2 ④)"
		return d
	}
	if sig.HitCount() == 0 {
		d.Verdict = ConflictNormal
		d.Why = "不构成冲突 (C1): " + sig.detail()
		return d
	}
	if sig.HitCount() < need {
		d.Verdict = ConflictNormal
		d.Why = fmt.Sprintf("不构成冲突 (C1): 命中 %d < %d; %s", sig.HitCount(), need, sig.detail())
		return d
	}

	selfPair := ctx.SelfInPair || sig.SelfInvolved
	d.Escalated = sig.Escalate

	// B2: 它自己就是一方 —— 这不是"介入", 是"冲突处理"。
	if selfPair {
		d.Verdict = ConflictSelfSide
		if d.Escalated {
			d.Why = "它自己是冲突的一方 + 命中硬线词表 → 走 §5.5 冲突处理, **不劝和**, 并上报 owner"
		} else {
			d.Why = "它自己是冲突的一方 → 走 §5.5 (违抗/拒绝/道歉找补), 严禁劝和话术 (B2)"
		}
		return d
	}

	// B1: 硬线 4 —— 人身攻击 / 歧视 / 安全。
	if d.Escalated {
		d.Verdict = ConflictEscalate
		d.Mode = ConflictModeReport
		d.Why = "命中硬线词表 (人身攻击/歧视/安全) → 不劝和, 上报 owner (§6.9 硬线 4)"
		return d
	}

	// A 类: 有冲突, 但不该出声。
	if ctx.Stage == ConflictStageStranger {
		// A1: 只认**场景阶段**, 不看平台角色 —— admin 也不许在陌生群里劝架。
		d.Verdict = ConflictNormal
		d.Why = "场景阶段 = 陌生 → 不掺和 (A1; 平台角色 " + conflictRoleLabel(ctx.SelfRole) + " 不参与该判据)"
		return d
	}
	if ctx.SelfIsNew {
		d.Verdict = ConflictNormal
		d.Why = "它自己还在 is_new 窗口内 (刚进群) → 不掺和 (A2)"
		return d
	}
	if unk, about := conflictUnfamiliar(sig, ctx, c); unk {
		d.Verdict = ConflictNormal
		d.Why = "跟冲突双方都不熟 → 不劝架 (A3): " + about
		return d
	}
	if ctx.MentionedSelf {
		d.Verdict = ConflictNormal
		d.Why = "它正被 @ / 被问 → 线程归属优先于冲突介入 (A4, §6.10.2 ①)"
		return d
	}
	if ctx.Cooldown {
		d.Verdict = ConflictNormal
		d.Why = fmt.Sprintf("同一对 (%s) 在 %d 分钟冷却内已介入过 → 劝不动就撤 (A5)", sig.PairKey(), c.CooldownMin)
		return d
	}

	d.Verdict = ConflictIntervene
	d.Mode = c.modeForStage(ctx.Stage)
	d.Why = fmt.Sprintf("介入: 命中 %d/%d 个信号 (%s) · 方式 %s (阶段 %s)",
		sig.HitCount(), need, sig.detail(), d.Mode, conflictStageLabel(ctx.Stage))
	return d
}

// conflictUnfamiliar 是 A3。
//
// 零值语义按"整个键是否存在"判定 (承 §2.4): Affinity == nil → **未提供** → 不施加抑制;
// 非 nil 而缺某个 uin → 查过了, 那个人不熟 (0 分)。
func conflictUnfamiliar(sig ConflictSignals, ctx ConflictContext, c ConflictPolicy) (bool, string) {
	if ctx.Affinity == nil || sig.AltRounds == 0 {
		return false, "" // 未提供熟悉度 / 没有可谈的"双方" → A3 不适用
	}
	// 它自己不算"要熟的对方": 被骂的时候不需要先跟骂它的人熟。
	others := make([]string, 0, 2)
	if sig.AltA != ConflictSelfUin {
		others = append(others, sig.AltA)
	}
	if sig.AltB != ConflictSelfUin {
		others = append(others, sig.AltB)
	}
	if len(others) == 0 {
		return false, ""
	}
	var parts []string
	for _, u := range others {
		f := ctx.Affinity[u]
		parts = append(parts, fmt.Sprintf("%s=%.2f", u, f))
		if f >= c.FamiliarAffinityMin {
			return false, "" // 有一边是熟的 → 不是"都不熟"
		}
	}
	return true, strings.Join(parts, " / ") + fmt.Sprintf(" < %.2f", c.FamiliarAffinityMin)
}

func conflictRoleLabel(role string) string {
	if role == "" {
		return "未提供"
	}
	return role
}

func conflictStageLabel(stage string) string {
	if stage == "" {
		return "未提供"
	}
	return stage
}

// ResolveConflict 是**纯函数侧的唯一入口**: 把"读场景"这一步的结果也接进来。
//
//	ctxErr != nil (读库失败) → ConflictNormal: 系统故障**往说话倒** (纪律 #3), 绝不沉默;
//	ctx.Undecidable          → ConflictSilent: 只有**语义判不出**才沉默。
//
// 顺序: 故障优先。因为"判不出"这个结论本身要靠读来的上下文支撑 —— 读都读不到,
// 就不存在"判出来了但是判不出"这回事。把两者并成一条, 就是 R28 写错的样子。
func ResolveConflict(msgs []ConflictMsg, base ConflictBaseline, ctx ConflictContext, ctxErr error, cfg ConflictPolicy) ConflictDecision {
	if ctxErr != nil {
		return ConflictDecision{
			Verdict: ConflictNormal,
			Pair:    ConflictSignalsOf(msgs, base, cfg).PairKey(),
			Why:     "场景状态读取失败: " + ctxErr.Error() + " → 系统故障走正常说话 (纪律 #3), 不沉默也不介入",
		}
	}
	return DecideConflict(ConflictSignalsOf(msgs, base, cfg), ctx, cfg)
}

// ——— 落库: 只落"介入过"这一事实 ———

// conflictSchema 是本模块自己的表 (纪律 #3: 不改 store.go; 建表走幂等的 EnsureConflictSchema,
// 由主控在迁移链里调一次)。
//
// 表里**只有事实**: 什么时候、对哪一对、用了什么方式、是不是硬线上报。
// 对呛轮数 / 互 @ / 负面密度 / 间隔 —— 一个都不落: 它们是消息序列的纯函数 (纪律 #4),
// 存了就必须回答"谁在什么时候重算它", 而答案是"每次都要重算", 那就没有存的理由。
const conflictSchema = `
CREATE TABLE IF NOT EXISTS convo_conflict_action (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    scene_key TEXT NOT NULL,
    pair_key  TEXT NOT NULL,
    acted_ts  TEXT NOT NULL,
    mode      TEXT NOT NULL,
    escalated INTEGER NOT NULL DEFAULT 0,
    outcome   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS ix_conflict_pair ON convo_conflict_action(scene_key, pair_key, acted_ts);
`

// EnsureConflictSchema 幂等建表 (重复调用不报错, 老库缺表也能补)。
func EnsureConflictSchema(s *Store) error {
	if s == nil || s.DB == nil {
		return fmt.Errorf("conflict: store 未初始化")
	}
	_, err := s.DB.Exec(conflictSchema)
	return err
}

// ConflictAction 是"介入过"的一条事实。
type ConflictAction struct {
	ID        int64
	SceneKey  string
	PairKey   string
	ActedTS   string
	Mode      string
	Escalated bool
	Outcome   string
}

// RecordConflictAction 记一次介入。
//
// 冷却是**硬约束不是提示** (纪律 #5 的精神): 这一层自己再挡一次,
// 否则"劝不动就撤"只存在于调用方的自觉里 —— 而调用方是会忘的。
// 被挡下时返回 ErrConflictCooldown (**不是**故障, 调用方不要重试)。
func (s *Store) RecordConflictAction(sceneKey, pairKey, mode string, escalated bool, at time.Time, cooldownMin int) error {
	if s == nil || s.DB == nil {
		return fmt.Errorf("conflict: store 未初始化")
	}
	if sceneKey == "" {
		return fmt.Errorf("conflict: scene_key 为空 (场景分区必须显式给出)")
	}
	if pairKey == "" {
		return fmt.Errorf("conflict: pair_key 为空")
	}
	if mode == "" {
		return fmt.Errorf("conflict: mode 为空")
	}
	if at.IsZero() {
		at = clock.Now() // 时间戳由注入时钟盖章 (§9)
	}
	active, err := s.ConflictCooldownActive(sceneKey, pairKey, at, cooldownMin)
	if err != nil {
		return fmt.Errorf("conflict: 冷却读取失败: %w", err)
	}
	if active {
		return ErrConflictCooldown
	}
	esc := 0
	if escalated {
		esc = 1
	}
	_, err = s.DB.Exec(`INSERT INTO convo_conflict_action
        (scene_key, pair_key, acted_ts, mode, escalated, outcome) VALUES(?,?,?,?,?,'')`,
		sceneKey, pairKey, at.Format(conflictTSLayout), mode, esc)
	return err
}

// RecordConflictDecision 把一个决策落成事实。
//
// 只有"介入"和"上报"落库 —— Normal / Silent 不是介入, 普通的 SelfSide 也不是
// (它自己的事走 §5.5, 不在这张表的语义里; 除非同时命中硬线词表, 那要上报 owner)。
// 落了不该落的, 冷却会误伤下一次真正该劝的场合。
func (s *Store) RecordConflictDecision(sceneKey string, d ConflictDecision, at time.Time, cfg ConflictPolicy) error {
	// §4.4.6: 冲突次数是失眠成因之一, 而它**不可重算**(决策表没有按月归档),
	// 所以要在**发生的当下**累加。
	//
	// ⚠️ 只记"真的介入了"的那几次 —— 一个被判成"不介入"的决策不是冲突,
	// 把它算进去会让阈值靠噪声达到。
	if d.Verdict == ConflictIntervene || d.Verdict == ConflictEscalate {
		if err := s.AddDayMetric(at.Format("2006-01-02"), dayMetricConflicts, 1); err != nil {
			log.Warn("convo:conflict", "冲突计数失败(失眠成因会偏低): %v", err)
		}
	}
	cooldown := cfg.Normalized().CooldownMin
	switch d.Verdict {
	case ConflictIntervene:
		mode := d.Mode
		if mode == "" {
			mode = ConflictModeDivert
		}
		return s.RecordConflictAction(sceneKey, d.Pair, mode, false, at, cooldown)
	case ConflictEscalate:
		return s.RecordConflictAction(sceneKey, d.Pair, ConflictModeReport, true, at, cooldown)
	case ConflictSelfSide:
		if !d.Escalated {
			return nil
		}
		return s.RecordConflictAction(sceneKey, d.Pair, ConflictModeReport, true, at, cooldown)
	default:
		return nil
	}
}

// ConflictCooldownActive 现算: 同一对是否还在 cooldown_min 内。
//
// **读失败必须冒泡** —— 吞掉它, R28 那类"系统故障变成沉默"就会从这里长出来。
func (s *Store) ConflictCooldownActive(sceneKey, pairKey string, now time.Time, cooldownMin int) (bool, error) {
	if s == nil || s.DB == nil {
		return false, fmt.Errorf("conflict: store 未初始化")
	}
	if now.IsZero() {
		now = clock.Now()
	}
	if cooldownMin <= 0 {
		cooldownMin = DefaultConflictPolicy().CooldownMin
	}
	var last sql.NullString
	if err := s.DB.QueryRow(
		`SELECT MAX(acted_ts) FROM convo_conflict_action WHERE scene_key=? AND pair_key=?`,
		sceneKey, pairKey).Scan(&last); err != nil {
		return false, err
	}
	if !last.Valid || last.String == "" {
		return false, nil
	}
	t, err := time.ParseInLocation(conflictTSLayout, last.String, time.Local)
	if err != nil {
		return false, fmt.Errorf("conflict: 解析 acted_ts %q: %w", last.String, err)
	}
	return now.Sub(t) < time.Duration(cooldownMin)*time.Minute, nil
}

// ConflictActions 按时间序取某一对的介入记录 (测试与观测用; 不参与判定)。
func (s *Store) ConflictActions(sceneKey, pairKey string) ([]ConflictAction, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("conflict: store 未初始化")
	}
	rows, err := s.DB.Query(`SELECT id, scene_key, pair_key, acted_ts, mode, escalated, outcome
        FROM convo_conflict_action WHERE scene_key=? AND pair_key=? ORDER BY id ASC`, sceneKey, pairKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConflictAction
	for rows.Next() {
		var a ConflictAction
		var esc int
		if err := rows.Scan(&a.ID, &a.SceneKey, &a.PairKey, &a.ActedTS, &a.Mode, &esc, &a.Outcome); err != nil {
			return nil, err
		}
		a.Escalated = esc != 0
		out = append(out, a)
	}
	return out, rows.Err()
}

// ConflictActionCount 某一对落了几条 (R24: 同一冲突窗内只许 1 条)。
func (s *Store) ConflictActionCount(sceneKey, pairKey string) (int, error) {
	if s == nil || s.DB == nil {
		return 0, fmt.Errorf("conflict: store 未初始化")
	}
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM convo_conflict_action WHERE scene_key=? AND pair_key=?`,
		sceneKey, pairKey).Scan(&n)
	return n, err
}

// GateConflict 是**给调用方的唯一入口**: 现算基线 → 现算四信号 → 读冷却 → 决策。
//
// 两个失败方向在这里被分开:
//   - 冷却读库失败 = **系统故障** → ConflictNormal (正常说话), 绝不 ConflictSilent;
//   - ctx.Undecidable = **语义判不出** → ConflictSilent。
//
// 冷却只在"确实够上冲突"时才查库 —— 普通消息不该为一次判定付一次查询。
func (s *Store) GateConflict(sceneKey string, msgs []ConflictMsg, ctx ConflictContext, cfg ConflictPolicy, now time.Time) ConflictDecision {
	c := cfg.Normalized()
	if sceneKey == "" {
		// 零值 = 未提供 → 走正常路径 (不介入, 也不沉默): 没有场景分区就不该谈冲突。
		return ConflictDecision{Verdict: ConflictNormal, Why: "scene_key 未提供 → 不介入冲突"}
	}
	base := ConflictBaselineFrom(msgs, c)
	sig := ConflictSignalsOf(msgs, base, c)
	if ctx.Undecidable {
		return DecideConflict(sig, ctx, c)
	}
	if sig.WindowMsgs >= c.MinMsgs && sig.HitCount() >= c.requiredSignals(ctx) {
		if pair := sig.PairKey(); pair != "" {
			active, err := s.ConflictCooldownActive(sceneKey, pair, now, c.CooldownMin)
			if err != nil {
				return ConflictDecision{
					Verdict: ConflictNormal,
					Pair:    pair,
					Hits:    sig.HitCount(),
					Need:    c.requiredSignals(ctx),
					Why: "冷却读取失败: " + err.Error() +
						" → 系统故障走正常说话 (纪律 #3), 不沉默也不介入",
				}
			}
			ctx.Cooldown = active
		}
	}
	return DecideConflict(sig, ctx, c)
}

// ——— 硬线 5: 不把"我在劝你们"说出来 ———

// ConflictMediationHit 是 §5.3 硬线 5 的判据: 帧文本里有没有"我在劝你们"的味道。
// words 空 → 内置词表。
//
// ⚠️ 命中 != 坏: 它是**刻意不说** (与 A6 同族), 调用方应当作 Refused 处理 ——
// 静默丢弃, 不降级、不报错、**不计入帧失败率**。这一条属于校验链, 由主控接线;
// 本函数只提供判据 (校验链在 dialog/verify 那侧, 不是本叶子模块的活)。
func ConflictMediationHit(text string, words []string) (bool, string) {
	if len(words) == 0 {
		words = conflictDefaultMediationWords
	}
	w := conflictHasWord(text, words)
	return w != "", w
}

// ——— 小工具 ———

// conflictSorted 返回按时间升序的副本。
// 判据全靠顺序, 所以这里**不信任调用方**: 顺序错了得出的结论会静默地不对,
// 而"静默地不对"比"报错"贵得多。等时刻保持输入顺序 (stable)。
func conflictSorted(msgs []ConflictMsg) []ConflictMsg {
	out := append([]ConflictMsg(nil), msgs...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS.Before(out[j].TS) })
	return out
}

// conflictWindow 取 (from, to] 的消息。左开右闭是为了让相邻两个同长窗口**不重不漏**:
// 边界上那一条只能属于一个窗口, 否则 ③ 的"前一窗"会把它数两遍。
func conflictWindow(msgs []ConflictMsg, from, to time.Time) []ConflictMsg {
	var out []ConflictMsg
	for _, m := range msgs {
		if m.TS.After(from) && !m.TS.After(to) {
			out = append(out, m)
		}
	}
	return out
}

// conflictAlternation 找 ①: 一对人**严格交替**的最长段, 返回轮数 (= 段长-1) 与那一对。
//
// "其间无第三人" 是这个循环的自然结果: 段只在 w[k].Uin == w[k-2].Uin 时延伸,
// 一旦插进第三个人 (或同一个人连说两句), 段就断了。
func conflictAlternation(w []ConflictMsg) (rounds int, a, b string) {
	for i := 0; i+1 < len(w); i++ {
		if w[i].Uin == w[i+1].Uin {
			continue
		}
		j := i + 1
		for j+1 < len(w) && w[j+1].Uin == w[j-1].Uin {
			j++
		}
		r := j - i
		if r < rounds {
			continue
		}
		pk := ConflictPairKey(w[i].Uin, w[i+1].Uin)
		if r == rounds && rounds > 0 && pk >= ConflictPairKey(a, b) {
			continue // 平手时取字典序小的那一对, 让结果与扫描起点无关
		}
		rounds, a, b = r, w[i].Uin, w[i+1].Uin
	}
	return rounds, a, b
}

// conflictMutualAt 找 ②: a@b 且 b@a (各 >=1 次)。只扫 @ 段, 不比文本。
func conflictMutualAt(w []ConflictMsg) (bool, string) {
	type edge struct{ from, to string }
	seen := map[edge]bool{}
	best := ""
	for _, m := range w {
		for _, to := range m.Ats {
			if to == m.Uin {
				continue // @ 自己不算
			}
			seen[edge{m.Uin, to}] = true
		}
	}
	for e := range seen {
		if !seen[edge{e.to, e.from}] {
			continue
		}
		pk := ConflictPairKey(e.from, e.to)
		if best == "" || pk < best {
			best = pk
		}
	}
	return best != "", best
}

// conflictAt 判断这条消息有没有 @ 到 uin。
func conflictAt(m ConflictMsg, uin string) bool {
	for _, a := range m.Ats {
		if a == uin {
			return true
		}
	}
	return false
}

// conflictHasWord 返回命中的词 (空串 = 未命中)。大小写不敏感 (中文词不受影响, 英文词受益)。
func conflictHasWord(text string, words []string) string {
	if text == "" {
		return ""
	}
	low := strings.ToLower(text)
	for _, w := range words {
		if w == "" {
			continue
		}
		if strings.Contains(low, strings.ToLower(w)) {
			return w
		}
	}
	return ""
}

// conflictNegMsgs 数**含负面词的消息条数** (不是词频)。
//
// 为什么按条数: 密度要拿去和 0.40 比。若按词频, 一条骂五个词的消息就能把密度顶到 5.0,
// 阈值 0.40 立刻失去意义 (任何一条脏话都超标)。按条数则密度天然 ∈ [0,1]。
func conflictNegMsgs(w []ConflictMsg, words []string) int {
	n := 0
	for _, m := range w {
		if conflictHasWord(m.Text, words) != "" {
			n++
		}
	}
	return n
}

// conflictMedianGap 窗口内相邻消息间隔的中位数 (秒)。ok=false = 不足两条。
// 用中位数而不是均值: 一条隔了半天的消息会把均值拉飞, 而"骤短"是多数消息的形态。
func conflictMedianGap(w []ConflictMsg) (float64, bool) {
	if len(w) < 2 {
		return 0, false
	}
	gaps := make([]float64, 0, len(w)-1)
	for i := 1; i < len(w); i++ {
		gaps = append(gaps, w[i].TS.Sub(w[i-1].TS).Seconds())
	}
	return conflictMedian(gaps), true
}

func conflictMedian(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	ys := append([]float64(nil), xs...)
	sort.Float64s(ys)
	n := len(ys)
	if n%2 == 1 {
		return ys[n/2]
	}
	return (ys[n/2-1] + ys[n/2]) / 2
}
