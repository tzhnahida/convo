package convo

import (
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/tzhnahida/VocabLock/internal/clock"
	"github.com/tzhnahida/VocabLock/internal/log"
)

// ═══════════════════════════════════════════════════════════════════════════
// §6.7 热力 —— 叶子模块 (本文件不含任何接线; 段序 / TickInput / migrate 调用
// 由主控统一做)。
//
// 热力回答一个问题: **现在能有多想主动开口**。它的唯一落点是
// **主动发起条数配额** (§4.3), 不是概率。
//
// 四条不许破的纪律 (写在这里, 免得后来人重犯):
//
//  1. **落点 = 配额 (硬约束), 不是概率**。scheduler_logic.go:159-163 是实测教训:
//     "把状态乘到抖动概率上完全看不出效果 —— 一个开放槽里几 tick 就掷过去了"。
//     所以本文件**没有任何函数接收或返回概率**: 出去的全是整数条数或热力值。
//
//  2. **收益递减的键是「场景 × 会话序号」** (§4.2 的承重结构), 不是「人 × 会话」,
//     也不是「消息总数」。若键含人, N=500 的群会有 500 份独立预算 → 破"打扰红线";
//     若用消息总数, N=500 会瞬间掉到地板值。按会话序号计, **N=2 与 N=500 走
//     同一张档位表、同一个地板、同一个饱和速度** (A3 不定向的落点)。
//     本文件里发言人**进不了梯度** —— HeatInteraction.Speaker 只写进审计流水。
//
//  3. **派生量不落库** (§4.5 / 纪律 #4): 落库的只有「上次结算的值 + 结算时刻 +
//     会话计数器」; 当前热力 / cap / 满热能发几条, 全部**读的时候现算**。
//
//  4. **失败往说话倒** (§4.4 / 纪律 #2): 读库读不出来 → 走常数配额, 绝不 return 0
//     (那会让她因为一次读库失败整天不说话)。见 HeatQuotaFrom / Store.HeatQuotaAt。
// ═══════════════════════════════════════════════════════════════════════════

// ——— 档位表 (纪律 #8: 幅度小、用表不用连续公式) ———

// 场景阶段 (§5.6: 热力上限取**场景级**)。
const (
	HeatStageStranger = "stranger"
	HeatStageFamiliar = "familiar"
	HeatStageIntimate = "intimate"
)

// 内置默认值 —— 与 §2.4 的默认列逐项对齐。改这里 = 改默认值; 配置可以覆盖。
const (
	HeatDefaultCostPerMsg   = 25.0 // 每次主动发起的花费
	HeatDefaultCapStranger  = 20.0
	HeatDefaultCapFamiliar  = 60.0
	HeatDefaultCapIntimate  = 100.0
	HeatDefaultHalfLifeH    = 36.0 // ⚠️ 规格里的**提案值**, §7.2 U4 未核实
	HeatDefaultSessionGap   = 30   // 分钟; 超过它 = 新会话
	HeatDefaultGainOpen     = 5.0  // 本会话第 1 条互动的 Δ
	HeatDefaultGainFloor    = 0.5  // 本会话第 10 条及以后的 Δ
	HeatDefaultGainMoodLow  = 0.5
	HeatDefaultGainBodyIll  = 0.5
	HeatDefaultGainEnvGood  = 1.1
	HeatDefaultOwnerMultMax = 5.0 // §5.6 硬上限 5×
)

// 审计流水的原因词 (§3 的 reason 枚举)。
const (
	HeatReasonGain     = "gain_interaction" // 场景内一次互动 → 加温
	HeatReasonCost     = "cost_initiate"    // 一次主动发起 → 扣 cost
	HeatReasonDecay    = "decay_settle"     // 只把衰减落一次 (无 delta)
	HeatReasonCapClamp = "cap_clamp"        // 本次加温被上限截断
)

// heatLadderSteps 档位表的长度: 第 1..9 条各自的形状, 第 10 条及以后 = gain_floor。
const heatLadderSteps = 9

// heatLadderShape 本会话第 n 条互动的**相对形状**: n=1 → 1.00, n≥10 → 0。
//
// 两头由 gain_open / gain_floor 两个配置给出, 中间这 9 档只定形状 ——
// 所以把 gain_open 配成 20、"第 10 条 = gain_floor(0.5)" 这条规格仍然成立
// (若直接拿 gain_open 乘系数, 换个配置第 10 条就不再是 gain_floor 了)。
// **严格递减** 由 TestHeat_GainLadderIsATable 钉死。
var heatLadderShape = [heatLadderSteps]float64{1.00, 0.78, 0.61, 0.48, 0.37, 0.28, 0.21, 0.15, 0.10}

// heatBuiltinGainByState §4.1 日程槽的槽位权重 (§6.7:939「focus ≈ 0 · slack 中 · free 高」)。
func heatBuiltinGainByState() map[string]float64 {
	return map[string]float64{StateFocus: 0, StateBusy: 1, StateSlack: 2, StateFree: 3}
}

// ——— 配置 ———

// HeatConfig 热力的全部旋钮 (§2.4)。
//
// ⚠️ **零值纪律**: 零值 HeatConfig{} 不是"全部归零", 而是"**全部未提供 → 用内置默认**"。
// 每个用到配置的函数都先过 Normalized(), 所以不存在"忘了配 → 热力恒 0 → 她不说话"这条路
// (纪律 #1 + #2)。整表语义也照 §2.4 的精化办: **按"整个键是否存在"判定, 不按单个条目的数值**
// —— 被提供的 GainByState 表里写 focus: 0 是合法取值, 不是"未提供"。
//
// 唯一**不在这里**的开关是 heat.enabled: 它等价于 §2.5 的 HasHeat=false,
// 由主控在裁决点上用 HeatQuotaInput.Provided 表达 (两个开关会互相打架)。
//
// ⚠️ **yaml tag 是 2026-10-15 补的。** 在那之前它一个 tag 都没有,
// 而上面的注释写着「主控 overlay convo.yaml 时以它为底」—— 也就是说
// 那句话描述的是**一件没做的事**: 这一节只能吃内置默认, 一行都配不了。
type HeatConfig struct {
	// ⚠️ **代价的唯一来源是 `scheduler.heat_cost_per_msg`**(见 Engine.heatCfg)。
	// 这里留 tag 只是让 yaml 能解析; 值会被调度器那一份**覆盖** ——
	// 两个旋钮管同一件事, 迟早分叉, 而分叉的表现是"配额按 25 算、扣费按 50 扣"。
	CostPerMsg     float64            `yaml:"cost_per_msg"`     // 见上; ≤0 → 25
	CapStranger    float64            `yaml:"cap_stranger"`     // ≤0 → 20
	CapFamiliar    float64            `yaml:"cap_familiar"`     // ≤0 → 60
	CapIntimate    float64            `yaml:"cap_intimate"`     // ≤0 → 100
	HalfLifeH      float64            `yaml:"half_life_h"`      // ≤0 → 36
	SessionGapMin  int                `yaml:"session_gap_min"`  // ≤0 → 30
	GainOpen       float64            `yaml:"gain_open"`        // ≤0 → 5
	GainFloor      float64            `yaml:"gain_floor"`       // ≤0 → 0.5
	GainByState    map[string]float64 `yaml:"gain_by_state"`    // nil → 内置表; 非 nil → 逐键取值, **缺键**回落内置表
	GainMoodLow    float64            `yaml:"gain_mood_low"`    // ≤0 → 0.5
	GainBodyUnwell float64            `yaml:"gain_body_unwell"` // ≤0 → 0.5
	GainEnvGood    float64            `yaml:"gain_env_good"`    // ≤0 → 1.1
	OwnerMultMax   float64            `yaml:"owner_mult_max"`   // ≤0 → 5
}

// DefaultHeatConfig 全部走内置默认的配置 (主控 overlay convo.yaml 时以它为底)。
func DefaultHeatConfig() HeatConfig { return HeatConfig{}.Normalized() }

// Normalized 逐项兜底。**返回值是副本**, 不改调用方那份 (map 会被复制)。
func (c HeatConfig) Normalized() HeatConfig {
	out := c
	if out.CostPerMsg <= 0 {
		out.CostPerMsg = HeatDefaultCostPerMsg
	}
	if out.CapStranger <= 0 {
		out.CapStranger = HeatDefaultCapStranger
	}
	if out.CapFamiliar <= 0 {
		out.CapFamiliar = HeatDefaultCapFamiliar
	}
	if out.CapIntimate <= 0 {
		out.CapIntimate = HeatDefaultCapIntimate
	}
	if out.HalfLifeH <= 0 {
		out.HalfLifeH = HeatDefaultHalfLifeH
	}
	if out.SessionGapMin <= 0 {
		out.SessionGapMin = HeatDefaultSessionGap
	}
	if out.GainOpen <= 0 {
		out.GainOpen = HeatDefaultGainOpen
	}
	if out.GainFloor <= 0 {
		out.GainFloor = HeatDefaultGainFloor
	}
	if out.GainMoodLow <= 0 {
		out.GainMoodLow = HeatDefaultGainMoodLow
	}
	if out.GainBodyUnwell <= 0 {
		out.GainBodyUnwell = HeatDefaultGainBodyIll
	}
	if out.GainEnvGood <= 0 {
		out.GainEnvGood = HeatDefaultGainEnvGood
	}
	if out.OwnerMultMax <= 0 {
		out.OwnerMultMax = HeatDefaultOwnerMultMax
	}
	// 退化配置 (open < floor) → 压成平档: 宁可不递减, 也不许出现"越聊越凉"。
	if out.GainOpen < out.GainFloor {
		out.GainOpen = out.GainFloor
	}
	builtin := heatBuiltinGainByState()
	if out.GainByState == nil {
		out.GainByState = builtin
	} else {
		merged := make(map[string]float64, len(builtin)+len(out.GainByState))
		for k, v := range builtin {
			merged[k] = v
		}
		for k, v := range out.GainByState {
			if v < 0 {
				v = 0 // 热力只由**时间衰减**来降; 入账不设"负互动"这条惩罚通道
			}
			merged[k] = v
		}
		out.GainByState = merged
	}
	return out
}

// CapOf 场景阶段 → 热力上限。**未知 / 空白阶段落到 stranger**(关系阶梯的下界,
// 与 convo_scene.stage 的 DEFAULT 'stranger' 同一条约定)。
//
// ⚠️ 这里**刻意不接 owner 倍率** (§7.2 U1 的提案): 群场景的 cap 只由群阶段决定。
// 否则"有 owner 的群它更话多"会变成可观测的差别, 直接破 §6.10.7。
func (c HeatConfig) CapOf(stage string) float64 {
	n := c.Normalized()
	switch strings.ToLower(strings.TrimSpace(stage)) {
	case HeatStageFamiliar:
		return n.CapFamiliar
	case HeatStageIntimate:
		return n.CapIntimate
	default:
		return n.CapStranger
	}
}

// HeatCap 内置档位表的只读入口 (§5.6: cap 取场景级, 每次裁决现算, 不落库)。
func HeatCap(stage string) float64 { return HeatConfig{}.CapOf(stage) }

// HeatClamp 把热力夹到 [0, cap]。
//
// ⚠️ **cap ≤ 0 = 未提供上限 → 不下压**, 不是"上限为零" (纪律 #1)。
// 真实路径上 cap 永远来自 CapOf, 那里 ≤0 已经被兜成默认值。
func HeatClamp(v, cap float64) float64 {
	if v < 0 {
		return 0
	}
	if cap <= 0 {
		return v
	}
	if v > cap {
		return cap
	}
	return v
}

// ——— 事实 ———

// HeatState 热力的**全部持久事实** (§4.5)。
//
// 存的是「上次结算那一刻的热力值 + 那一刻」, **不是当前热力** ——
// 当前值 = 存量 × 衰减(now − settled), 每次读的时候现算 (纪律 #4)。
//
// SessionSeq / LastMsgTS 也是事实, 不是派生量: 它们回答的是
// 「本会话里已经计入了几条互动」「场景内最后一条消息是什么时候」,
// **只在入账时 +1, 跨越 session_gap_min 时归 1** —— 这就是"谁在什么时候重算它"的答案。
type HeatState struct {
	SceneKey    string
	Value       float64 // 上次结算时刻的热力 (= 那一次结算后的值)
	SettledTS   string  // 结算时刻 (注入时钟盖章)
	CapAtSettle float64 // 结算时的上限 (审计; 现值 cap 每次按 stage 现算)
	SessionSeq  int     // 本会话内已计入的互动条数
	LastMsgTS   string  // 场景内上一条消息的时刻 (定会话边界; 含它自己主动发的那条)
}

// heatParseTS 与全包同一套时间格式 (bodyTSLayout)。读不出 → ok=false,
// 由各调用点决定往哪边倒 (一律往"还有热力 / 新会话"倒, 不往沉默倒)。
func heatParseTS(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation(bodyTSLayout, s, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// HeatAt 当前热力 —— **纯函数, 衰减现算** (§4.1 / R14)。
//
// 现算而不是后台任务或落库: 仿真里时间是一格格拨的, 后台结算根本追不上;
// 而且"存了就必须回答谁在什么时候重算它" —— 现算就不需要回答。
//
// ⚠️ 签名比规格多了一个 halfLifeH: §2.4 把它列成配置项 (默认 36h 是提案值),
// 而纯函数拿不到配置 —— 与其在里面偷偷读全局, 不如让调用方把半衰期交进来。
//
// 坏值一律往"还有热力"倒, 不往 0 倒:
//   - 存量 ≤ 0 → 0 (这没有歧义);
//   - halfLifeH ≤ 0 → 内置半衰期 (零值 = 未提供);
//   - 结算时刻缺失 / 读不出 → **原样返回** (没有可衰减的锚点, 不凭空归零);
//   - now 早于结算时刻 (时钟回拨 / 测试) → 原样返回, **绝不反向放大**。
func HeatAt(st HeatState, now time.Time, halfLifeH float64) float64 {
	if st.Value <= 0 {
		return 0
	}
	hl := halfLifeH
	if hl <= 0 {
		hl = HeatDefaultHalfLifeH
	}
	settled, ok := heatParseTS(st.SettledTS)
	if !ok {
		return st.Value
	}
	d := now.Sub(settled)
	if d <= 0 {
		return st.Value
	}
	return st.Value * math.Pow(2, -d.Hours()/hl)
}

// ——— 会话序号与增益 ———

// HeatSessionSeq 本条互动落在本会话的第几号 —— 纯函数 (§4.2)。
//
// 会话 = 距上一条消息超过 gapMin 的新窗口。**入参里没有发言人, 因为序号只跟
// 场景里的消息序列有关, 与谁说的无关** —— 这是 R15 的结构性保证:
// 想按人分账, 在这个签名上写不出来 (要加参数, R15 立刻红)。
//
// 三种边界都往"新会话"倒 (新会话 = 序号回 1 = 加温最大, 不是沉默方向):
// 从没记过 / 时刻读不出 / 超时。反向的边界 (时钟回拨、同一秒内连发) **不重开窗口**
// —— 否则忙群每一 tick 都会吃一次顶格 +5, 递减退化失效。
func HeatSessionSeq(lastMsgTS string, prevSeq int, now time.Time, gapMin int) (seq int, newSession bool) {
	gap := gapMin
	if gap <= 0 {
		gap = HeatDefaultSessionGap
	}
	last, ok := heatParseTS(lastMsgTS)
	if !ok || prevSeq < 0 {
		return 1, true
	}
	if now.Sub(last) > time.Duration(gap)*time.Minute {
		return 1, true
	}
	return prevSeq + 1, false
}

// HeatMods 一次互动除槽位以外的修正项。**零值 = 全部未提供 = 不减不加** (纪律 #1)。
type HeatMods struct {
	MoodLow    bool    // §4.1 心情低 → 减半
	BodyUnwell bool    // §4.2 身体不适 → 减半
	EnvGood    bool    // §3.6 环境好 (下雨 / 在家闲着) → 略高
	OwnerMult  float64 // ≤0 → 1 (零值 = 无倍率, **不是 0 倍**); 上限 OwnerMultMax
}

// HeatGain 本会话第 sessionSeq 条互动给场景加多少热 (§4.1)。
//
//	gain = Δ(n) × gain_by_state[槽] × mood_mod × body_mod × env_mod × owner_mult
//
// ⚠️ 递减的键是「场景 × 会话序号」(§4.2)。**本函数里没有发言人这个参数** ——
// 发言人对热力的唯一影响路径是 heat_log.ref_uin (审计), 不在梯度里。
// 递推出来的性质: N=2 的群与 N=500 的群走**同一张档位表、同一个地板、同一个饱和速度**。
//
// seq ≤ 0 = 未提供序号 → 按第 1 条算 (顶格; 往有话说倒)。
// 未知槽位 → 权重 0: 它不制造沉默 (热力仍按时间衰减), 只是不加温。
func HeatGain(sessionSeq int, slotState string, mods HeatMods, cfg HeatConfig) float64 {
	n := cfg.Normalized()
	seq := sessionSeq
	if seq < 1 {
		seq = 1
	}
	step := n.GainFloor
	if seq <= heatLadderSteps {
		shape := heatLadderShape[seq-1]
		step = n.GainFloor + (n.GainOpen-n.GainFloor)*shape
	}
	w := n.GainByState[slotState]
	if w < 0 {
		w = 0
	}
	g := step * w
	if mods.MoodLow {
		g *= n.GainMoodLow
	}
	if mods.BodyUnwell {
		g *= n.GainBodyUnwell
	}
	if mods.EnvGood {
		g *= n.GainEnvGood
	}
	mult := mods.OwnerMult
	if mult <= 0 {
		mult = 1
	}
	if mult > n.OwnerMultMax {
		mult = n.OwnerMultMax
	}
	g *= mult
	if g < 0 {
		g = 0
	}
	return g
}

// ——— 落点: 配额 ———

// HeatQuotaInput §4.3 落点的全部输入。
//
// **Provided 就是 §2.5 的 HasHeat** —— heat.enabled: false 也走这一条
// (主控把它置 false), 不再另设开关: 两个开关迟早会互相打架。
type HeatQuotaInput struct {
	DailyCap   int     // cfg.ActiveQuotaDay —— 打扰红线 / 常数配额
	MoodCut    int     // 心情低 → 1 (既有 §8.3 的幅度上限就是这 1); 否则 0
	BodyCut    int     // §4.2 身体档位给出的额度变化 (≤0; 0 = 没病)
	Heat       float64 // 该场景当前热力 (已含时间衰减)
	Provided   bool    // 热力是否**真的被提供过**
	CostPerMsg float64 // ≤0 → 25
}

// HeatQuotaFrom §4.3 的落点, 一次算完: **先减法, 后取 min**。
//
//	dailyCap = max(0, DailyCap − MoodCut + BodyCut)
//	Provided == false → dailyCap                  // 未提供 → 升级前路径逐字节不变
//	Provided == true  → min(dailyCap, ⌊H / cost⌋)  // 热力预算, 再与红线取小
//
// ⚠️ **min 必须在减法之后** (R20): 心情差 / 生病仍要能压到 0, 热力拉满也不许
// 把它们顶回来 —— 这就是「打扰红线不因热力高而放开」(§6.7:943) 的实现。
func HeatQuotaFrom(in HeatQuotaInput) int {
	daily := in.DailyCap - in.MoodCut + in.BodyCut
	if daily < 0 {
		daily = 0
	}
	if !in.Provided {
		return daily
	}
	return HeatQuota(in.Heat, in.CostPerMsg, daily)
}

// HeatQuota 热力预算与日上限取小 —— 返回**允许的条数** (整数硬约束, 不是概率)。
//
// ⚠️ 传进来的 dailyCap 必须是**已经减过心情 / 身体**的值; 要一次算完用 HeatQuotaFrom。
// ⚠️ cost ≤ 0 是"未提供" → 内置 25, **不是免费** (R18): 否则一行配置漏写
// 就等于把热力闸门拆了, 而现象只是"她忽然话变多", 看不出是配置坏了。
func HeatQuota(h float64, cost float64, dailyCap int) int {
	if dailyCap <= 0 || h <= 0 {
		return 0
	}
	c := cost
	if c <= 0 {
		c = HeatDefaultCostPerMsg
	}
	budget := h / c
	// 先比再转: 免得极大热力下 float→int 越界 (那会给出一个负数, 变成"不说话")
	if budget >= float64(dailyCap) {
		return dailyCap
	}
	return int(budget) // budget > 0 且 < dailyCap, 截断即取下整
}

// ═══════════════════════════════════════════════════════════════════════════
// 持久化 —— 只存事实
// ═══════════════════════════════════════════════════════════════════════════

// heatSchema 热力的两张表 (§3 提案 4)。
//
//	convo_heat     —— 每场景一行, **只存事实**: 结算值 / 结算时刻 / 会话计数器
//	convo_heat_log —— 审计流水 (§9:1371 点名要求)。§12「机制有没有生效, 要看数字」:
//	                  涨了多少、被 clamp 了几次、扣了几次, 都在这张表里
//
// ⚠️ 没有任何 DEFAULT CURRENT_TIMESTAMP: 时间戳一律由**注入时钟**盖章
// (调用方把 now 传进来), 否则时间旅行下会出现两个时钟 (clock.go 开头那条)。
const heatSchema = `CREATE TABLE IF NOT EXISTS convo_heat (
	scene_key       TEXT PRIMARY KEY,
	value           REAL NOT NULL DEFAULT 0,
	settled_ts      TEXT NOT NULL DEFAULT '',
	cap_at_settle   REAL NOT NULL DEFAULT 0,
	session_seq     INTEGER NOT NULL DEFAULT 0,
	last_msg_ts     TEXT NOT NULL DEFAULT '',
	updated_ts      TEXT NOT NULL DEFAULT ''
);` + `
CREATE TABLE IF NOT EXISTS convo_heat_log (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	ts         TEXT NOT NULL DEFAULT '',
	scene_key  TEXT NOT NULL DEFAULT '',
	reason     TEXT NOT NULL DEFAULT '',
	ref_uin    TEXT NOT NULL DEFAULT '',
	delta      REAL NOT NULL DEFAULT 0,
	v_before   REAL NOT NULL DEFAULT 0,
	v_after    REAL NOT NULL DEFAULT 0,
	seq        INTEGER NOT NULL DEFAULT 0,
	msg_ref    TEXT NOT NULL DEFAULT ''
);` + `
CREATE INDEX IF NOT EXISTS ix_heat_log ON convo_heat_log(scene_key, ts);`

// EnsureHeatSchema 幂等建表 (新库建空表, 老库补表, 重复调用无副作用)。
//
// ⚠️ **由主控在 store 的 migrate 里接一次** (本文件不接线)。没接的后果是可见的:
// LoadHeat 会直接报错, 而不是静默返回热力 0 —— 而 HeatQuotaAt 据此走常数配额
// (失败往说话倒), 同时出 Warn (TestHeat_ReadFailureFallsBackToConstantQuota 钉死)。
func EnsureHeatSchema(s *Store) error {
	if s == nil || s.DB == nil {
		return fmt.Errorf("热力 schema: store 未打开")
	}
	_, err := s.DB.Exec(heatSchema)
	return err
}

// LoadHeat 读一个场景的热力事实。
//
// ok=false = 该场景**从未结算过** (热力就是 0), 这**不是错误** ——
// 与"读库失败"必须分开 (R13): 前者照常走热力路径 (预算 0), 后者走常数配额。
// 读操作不写库 (与 LoadBody / LoadInterruption 同规)。
func (s *Store) LoadHeat(sceneKey string) (HeatState, bool, error) {
	st := HeatState{SceneKey: sceneKey}
	err := s.DB.QueryRow(`SELECT COALESCE(value,0), COALESCE(settled_ts,''), COALESCE(cap_at_settle,0),
		COALESCE(session_seq,0), COALESCE(last_msg_ts,'') FROM convo_heat WHERE scene_key=?`, sceneKey).
		Scan(&st.Value, &st.SettledTS, &st.CapAtSettle, &st.SessionSeq, &st.LastMsgTS)
	if err == sql.ErrNoRows {
		return st, false, nil
	}
	if err != nil {
		return st, false, err
	}
	return st, true, nil
}

// SaveHeat 写热力事实 (每场景一行)。
//
// 列名 7 个、占位符 7 个 —— 数得出来, 别再让它对不上 (bodystore.go:36-37 的教训:
// 那次静默失败就是这两半对不上, 而调用方还把错误吞了)。
func (s *Store) SaveHeat(st HeatState) error {
	_, err := s.DB.Exec(`INSERT INTO convo_heat
		(scene_key, value, settled_ts, cap_at_settle, session_seq, last_msg_ts, updated_ts)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(scene_key) DO UPDATE SET
			value=excluded.value, settled_ts=excluded.settled_ts,
			cap_at_settle=excluded.cap_at_settle, session_seq=excluded.session_seq,
			last_msg_ts=excluded.last_msg_ts, updated_ts=excluded.updated_ts`,
		st.SceneKey, st.Value, st.SettledTS, st.CapAtSettle, st.SessionSeq, st.LastMsgTS, clock.TodayTS())
	return err
}

// HeatSettle 一次结算的读数 (全部现算, 不落库)。
type HeatSettle struct {
	Before     float64 // 结算前的现值 (已含衰减)
	Delta      float64 // 实际变化 (= 落库那一笔; 被 clamp 时小于名义值)
	After      float64 // 结算后的值 (= 落库的 value)
	Cap        float64 // 本次用的上限
	Seq        int     // 本条互动在本会话内的序号 (非互动事件 = 0)
	NewSession bool
	Clamped    bool
}

// HeatInteraction 一次场景内互动 (场景里**任何人的任何一条**消息)。
type HeatInteraction struct {
	SceneKey  string
	Speaker   string // uin; **只进 heat_log.ref_uin** (审计), 绝不进梯度键
	Stage     string // 场景阶段 → cap
	SlotState string // 日程槽状态 → 槽位权重
	Mods      HeatMods
	Now       time.Time
	MsgRef    string
}

// heatDeltaFn 在**同一次读**里算出本次的 delta 与会话序号。
// 存在的理由: 序号与增益都依赖当前行, 分两次读会与并发写打架 (SQLite 单连接也会交错)。
type heatDeltaFn func(st HeatState) (delta float64, seq int, newSess bool, reason string)

// settleHeat 读-改-写一次惰性结算 —— **唯一的写入口** (§4.3)。
//
// 现算衰减 → 加 delta → clamp 到 cap → 写回事实 (+ 一条流水)。
// 读失败原样返回错误: 往哪边倒由调用方定 —— HeatQuotaAt 往"说话"倒,
// 入账路径则老老实实报错 (它不出声的话, 表现是"热力永远不涨", 看不出是坏了)。
func (s *Store) settleHeat(sceneKey string, now time.Time, cap float64, cfg HeatConfig,
	refUIN, msgRef string, touchLastMsg bool, fn heatDeltaFn) (HeatSettle, error) {
	n := cfg.Normalized()
	st, _, err := s.LoadHeat(sceneKey)
	if err != nil {
		return HeatSettle{}, err
	}
	before := HeatAt(st, now, n.HalfLifeH)
	delta, seq, isNew, reason := fn(st)
	raw := before + delta
	after := HeatClamp(raw, cap)
	clamped := after != raw

	st.Value = after
	st.SettledTS = now.Format(bodyTSLayout)
	st.CapAtSettle = cap
	if seq > 0 {
		st.SessionSeq = seq
	}
	if touchLastMsg {
		// 会话边界看的是"场景内上一条消息", 而她主动发的那条也算一条消息 ——
		// 否则她说完一句、对方 5 秒后接话会被判成"新会话"而重吃顶格。
		st.LastMsgTS = st.SettledTS
	}
	if err := s.SaveHeat(st); err != nil {
		return HeatSettle{}, err
	}
	if clamped && delta > 0 {
		reason = HeatReasonCapClamp
	}
	res := HeatSettle{Before: before, Delta: after - before, After: after, Cap: cap,
		Seq: seq, NewSession: isNew, Clamped: clamped}
	if err := s.appendHeatLog(sceneKey, now, reason, refUIN, msgRef, res); err != nil {
		// 流水写失败**不算结算失败** —— 事实已经落库了; 但也绝不静默 (P7 的教训)。
		log.Warn("convo:heat", "热力流水写入失败 (场景 %s, %s): %v", sceneKey, reason, err)
	}
	return res, nil
}

// SettleHeat 只把衰减落一次 (delta = 0)。给"时间到了顺手结算"和观测用。
func (s *Store) SettleHeat(sceneKey string, now time.Time, stage string, cfg HeatConfig) (HeatSettle, error) {
	n := cfg.Normalized()
	return s.settleHeat(sceneKey, now, n.CapOf(stage), n, "", "", false,
		func(HeatState) (float64, int, bool, string) { return 0, 0, false, HeatReasonDecay })
}

// RecordHeatInteraction 入账一次互动并返回本次读数 —— 接线方对**任何**场景消息调它。
//
// 会话序号在这里推进 (键 = 场景 × 会话, §4.2): 同场景内谁说的都一样,
// 500 个不同发言人各 1 条与同一个人 500 条**逐值相同** (R15)。
func (s *Store) RecordHeatInteraction(in HeatInteraction, cfg HeatConfig) (HeatSettle, error) {
	n := cfg.Normalized()
	return s.settleHeat(in.SceneKey, in.Now, n.CapOf(in.Stage), n, in.Speaker, in.MsgRef, true,
		func(st HeatState) (float64, int, bool, string) {
			seq, isNew := HeatSessionSeq(st.LastMsgTS, st.SessionSeq, in.Now, n.SessionGapMin)
			return HeatGain(seq, in.SlotState, in.Mods, n), seq, isNew, HeatReasonGain
		})
}

// SpendHeat 一次**主动发起**的扣费 (§6.7:940「每条主动消息 = 25 热力」)。
//
// ⚠️ 回话**不走这里** (§4.4): 被 @ 必答是硬门槛, 回话不花热力。
// 若回话也扣, 忙群会因为递减退化而永远攒不出 25 → 大群必然变哑巴 —— 那正是
// "为特定群形态做设计", 违反 A3。
func (s *Store) SpendHeat(sceneKey string, now time.Time, stage string, cfg HeatConfig) (HeatSettle, error) {
	n := cfg.Normalized()
	return s.settleHeat(sceneKey, now, n.CapOf(stage), n, "", "", true,
		func(HeatState) (float64, int, bool, string) {
			return -n.CostPerMsg, 0, false, HeatReasonCost
		})
}

// ——— 读路径: 裁决时的唯一入口 ———

// HeatQuotaAt 接线用的**唯一读路径**: 读事实 → 现算衰减 → 走 §4.3 的 min。
//
// 它与 LoadHeat 的差别就是**失败方向** (纪律 #2 / R13):
// 表缺失、库坏了、读不出来 → 出 Warn + **退回常数配额** (与升级前逐字节相同),
// 绝不 return 0 —— 那会让她因为一次读库失败整天不说话。
//
// 「该场景从未结算过」(ok=false) **不算失败**: 它就是热力 0 = 预算 0,
// 陌生场景本来就不主动 (§4.3 的意图), 与"读不出来"是两回事。
func (s *Store) HeatQuotaAt(sceneKey string, now time.Time, cfg HeatConfig, in HeatQuotaInput) int {
	n := cfg.Normalized()
	st, _, err := s.LoadHeat(sceneKey)
	if err != nil {
		log.Warn("convo:heat", "热力读库失败 (场景 %s): %v —— 本轮退回常数配额 %d", sceneKey, err, in.DailyCap)
		in.Provided = false
		return HeatQuotaFrom(in)
	}
	in.Heat = HeatAt(st, now, n.HalfLifeH)
	in.Provided = true
	return HeatQuotaFrom(in)
}

// ——— 审计流水 (可观测) ———

func (s *Store) appendHeatLog(sceneKey string, now time.Time, reason, refUIN, msgRef string, r HeatSettle) error {
	_, err := s.DB.Exec(`INSERT INTO convo_heat_log
		(ts, scene_key, reason, ref_uin, delta, v_before, v_after, seq, msg_ref)
		VALUES(?,?,?,?,?,?,?,?,?)`,
		now.Format(bodyTSLayout), sceneKey, reason, refUIN, r.Delta, r.Before, r.After, r.Seq, msgRef)
	return err
}

// HeatLogRow 一条热力流水。
type HeatLogRow struct {
	TS     string
	Reason string
	RefUIN string
	Delta  float64
	Before float64
	After  float64
	Seq    int
}

// RecentHeatLog 读最近的热力流水 (§12: 机制有没有生效, 要看数字)。
// sceneKey 为空 = 全部场景。limit ≤ 0 → 20。
func (s *Store) RecentHeatLog(sceneKey string, limit int) ([]HeatLogRow, error) {
	if limit <= 0 {
		limit = 20
	}
	q := `SELECT COALESCE(ts,''), COALESCE(reason,''), COALESCE(ref_uin,''), COALESCE(delta,0),
		COALESCE(v_before,0), COALESCE(v_after,0), COALESCE(seq,0) FROM convo_heat_log`
	args := []interface{}{}
	if sceneKey != "" {
		q += ` WHERE scene_key=?`
		args = append(args, sceneKey)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HeatLogRow
	for rows.Next() {
		var r HeatLogRow
		if err := rows.Scan(&r.TS, &r.Reason, &r.RefUIN, &r.Delta, &r.Before, &r.After, &r.Seq); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
