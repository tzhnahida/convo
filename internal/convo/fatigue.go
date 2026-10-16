package convo

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// ═══════════════════════════════════════════════════════════════════════════
// §4.3 注意力与疲劳 —— **叶子模块**: 判据是纯函数, 状态只进库, tick 接线不在这里。
//
// 三条决定形状的口径写在最前面, 后面每一处都从这里推:
//
//	① **拒绝权不是"意愿低"** (规格 §4.3 §0)。心情低 **且高唤醒** 才允许明说"今天不想聊";
//	   低唤醒(低落)走的是既有那条"慢/短/少表情"的路 (mood.go Willingness + pacing.go)。
//	   ⚠️ `Willingness` 里 arousal 是**正项** (mood.go:199 `0.6+0.6*Arousal`),
//	   拿它当阈值会让"高唤醒"反而更拿不到许可 —— 方向正好相反。所以本层判据独立。
//
//	② **疲劳的落点是回复延迟档 + 拒绝权门槛, 不是配额** (规格 §4.3 C4)。
//	   配额现在已经被两个独立项减 (心情 -1 / 身体 -2..0, scheduler_logic.go:168-179)。
//	   再加第三个减项, quota 变 0 就有三个可能的原因, "今天为什么不说话"再也归因不了。
//	   所以 `FatigueEffects.QuotaDelta` **恒为 0**, 而且这一点是**可断言**的
//	   (FatigueQuotaProjection 恒为 (0,false) / TestFatigue_DoesNotCutQuota), 不是一句注释。
//
//	③ **中断-恢复的落点是时钟 + 独立存储**。`convo.go:1002` 的 `pending_reply_due`
//	   是**单槽** (INSERT ... ON CONFLICT DO UPDATE): 中断若复用它, "拿个外卖"
//	   会顶掉 "busy 30-90 分钟的延迟回复", 反之亦然。所以另开一张表, 互不覆盖。
//
// 零值口径 (规格 §4.3 收尾表 + 项目纪律 1): 每个新开关的零值都是"未提供 → 走正常路径"。
//
//	FatigueInput{}     → 档 0 (不疲劳)
//	FatigueEffects{}   → Apply 归一化成 DelayFactor 1.0 / QuotaDelta 0
//	Interruption{}     → 既不在挂起态, 也没到点 (不触发)
//	DeclineInput{}     → **不给许可** → 走正常说话
//
// ⚠️ 最后一条不是巧合, 是判据方向决定的: 判据是"低心情 **且** 高唤醒", 而 `Arousal`
// 的零值 0 落在**低**唤醒那一侧, 所以"忘了填"与"心境平淡"掉在同一边 = 正常说话。
// 反过来的写法 (唤醒低于门槛就给许可) 才会让零值变成"每天都允许拒绝" —— 那正是这一层
// 最容易踩的坑, 见 TestDeclinePermit_ZeroValueDenies。
// ═══════════════════════════════════════════════════════════════════════════

// fatigueTSLayout 与全包一致的时间格式 (moodstore / memory / settle / body 都用它)。
// 单独定义而不是引用 bodyTSLayout: 本模块与"身体"没有关系, 别让命名暗示有。
const fatigueTSLayout = "2006-01-02 15:04:05"

// fatigueParseTS 解析库里的时刻。**读不出 = 没有这件事** (ok=false) —— 失败往"不触发"倒。
func fatigueParseTS(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation(fatigueTSLayout, s, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// ───────────────────────────────────────────────────────────────────────────
// M1 拒绝权 (§4.3 L594: 心情低 + 高唤醒 → 允许**明说**"今天不想聊")
// ───────────────────────────────────────────────────────────────────────────

// DeclineTierSafety 是 §5.5 三层权限里 C 层 (安全/求助) 的取值。
//
// ⚠️ 这里写**字面量**而不是引用 `TierSafety`: 本模块是叶子, 不许依赖尚未合并的
// §5.5 文件 (引用了现在编译不过)。`RequestTier` 的底层类型是 string, 合并后调用方
// 写 `Tier: string(TierSafety)` 即可, 本文件一行都不用改。
//
// ⚠️ 反过来说: `Tier == ""` (未分类) **不等于** C 层 —— 零值不是极值 (纪律 1)。
// "判不准 → TierSafety" 是 `ClassifyRequest` 的契约 (规格 §3), 所以生产路径上
// 永远不会递进来一个 ""; 把 "" 当 C 层反而是"未提供当成极值"。
const DeclineTierSafety = "C"

// DeclineValenceMax / DeclineArousalMin 拒绝权的两个阈值 (合取)。
//
// ⚠️ **取值未核实** (规格 §7.5): 两个数没有任何文档依据, 是照 `MoodWillingnessFloor=0.25`
// 与 `MoodBaselineArousal=0.50` (mood.go:189,85) 推的位置, 必须实测校
// (对照读数: 拒绝次数/季)。放成包级变量而不是散在判据里, 就是为了整体重标定。
//
// ⚠️ 两者的**零值都是"未提供"**, 不是"极值": 见 declineThresholds 的回落。
// `DeclineArousalMin` 的零值尤其毒 —— 直接拿 0 当门槛, 拒绝权每天都 armed。
var (
	DeclineValenceMax = -0.15
	DeclineArousalMin = 0.65
)

// DeclineFatigueArousalMin 疲劳档 >= FatigueBoostTier 时**下移后**的唤醒门槛。
//
// "下移门槛" ≠ "新增一条独立许可": 低心情那一半仍然必需。否则"今天很累"会在心情好的
// 日子也允许她说"不想聊" —— 那是加戏, 不是"耐心下降"。§4.3 M4 的原话正是
// "更早进入可拒绝态", 更早, 不是无条件。
//
// ⚠️ 取值同样**未核实** (与上面两个阈值同批)。
var DeclineFatigueArousalMin = 0.45

// declineThresholds 取本次生效的两个阈值, 并把"零值 = 未提供"落回默认值。
//
// 现在还没有 `cfg.DeclineValenceMax/DeclineArousalMin` 字段可读 (config.go 不在本次
// 范围), 所以阈值暂由上面的包级变量给出 —— 主控把字段加进 SchedulerConfig 之后,
// **只改这一个函数**, 判据与测试一行不用动。
func declineThresholds(fatigueTier int) (vMax, aMin float64) {
	vMax, aMin = DeclineValenceMax, DeclineArousalMin
	if vMax >= 0 { // 0 或正数都不可能是"低心情"的门槛 = 未提供
		vMax = -0.15
	}
	if aMin <= 0 { // 0 = 未提供 —— 不能当成"谁都能拒"
		aMin = 0.65
	}
	if FatigueBoostTier > 0 && fatigueTier >= FatigueBoostTier && DeclineFatigueArousalMin > 0 {
		aMin = DeclineFatigueArousalMin
	}
	return
}

// DeclineInput 一次"今天能不能明说不想聊"的裁决输入。
type DeclineInput struct {
	Valence     float64 // 心情 -1..1; **零值 = 未提供/中性** (不是"心情极差")
	Arousal     float64 // 心情 0..1; **零值 = 低唤醒** → 不给许可 (见文件头零值口径)
	BodyDecline bool    // 既有依据: BodyEffects.CanDecline (body.go:262) —— 两套许可在此合并 (C1)
	FatigueTier int     // 0 = 未提供/不疲劳 (来自 DecideFatigue)
	UsedToday   bool    // 当日已用过 (调用方从 convo_counters 现算, 不是存下来的开关)
	Tier        string  // §5.5 三层权限; 只有 DeclineTierSafety 一票否决。"" = 未分类 ≠ C 层
	State       string  // 当前槽状态 (CurrentState); StateSleep 内不给 —— 那是睡着, 不是拒绝
}

// DeclinePermit 今天"明说不想聊"是否被允许。
//
// 这是 §4.3 C1 要求的**唯一权威判据**: 身体那条旧许可 (body.go:262 CanDeclineAt 的
// "你可以直接说'今天不太想聊'") 与新的心情判据都从这里出结论 —— 两处各写一份的话,
// 同一轮会出现两句措辞不同的许可, 行为不可预期。
//
// ⚠️ 判据是"低心情 **AND** 高唤醒"的合取, **不是** `Willingness < 阈值`:
// Willingness 里 arousal 是正项, 高唤醒会把意愿**抬高**, 拿它当阈值方向正好相反。
//
// 一次都不掷骰子、不含任何率 —— 同一输入 100 次结果完全一致 (决策器不接 LLM/随机)。
//
// ⚠️ `cfg` 目前**一个字段都没被读**: 本次范围不许改 config.go, 而三个阈值字段
// (DeclineValenceMax / DeclineArousalMin / FatigueDelayTierMin) 都还没落进 SchedulerConfig。
// 保留形参是为了主控接线时**不改签名**。
func DeclinePermit(in DeclineInput, cfg SchedulerConfig) (allowed bool, reason string) {
	_ = cfg // 见上: 阈值现在来自包级变量 + declineThresholds 的零值回落

	// C 层最高优先: 安全/求助**永远**不给拒绝许可 (硬线)。
	if in.Tier == DeclineTierSafety {
		return false, "C 层 (安全/求助) 永不给拒绝许可 —— 请求必须被正常接住"
	}
	// 配额: 一天一次。用完段消失, 不是"概率变低"。
	if in.UsedToday {
		return false, "今天已经明说过一次 (配额 1/天)"
	}
	// 睡着不是拒绝: 睡眠窗内的静默由日程负责, 与拒绝权无关。
	//
	// ⚠️ 这一条**必须是硬门, 不能只留一句注释** —— 缺了它, 一个"身体许可"就足以让它在
	// 睡着的时候冒出来说"今天不想聊", 那比不回复更吓人。本模块的测试正是这么抓住它的。
	// 位置在 C 层之后、一切"许可"之前: 睡着时**任何**理由都不构成许可。
	if in.State == StateSleep {
		return false, "睡眠窗内 —— 那是睡着, 不是拒绝 (静默由日程负责)"
	}

	vMax, aMin := declineThresholds(in.FatigueTier)

	if in.BodyDecline {
		return true, "身体许可 (BodyEffects.CanDecline) —— 与心情判据合并成同一个出口"
	}
	if in.Valence <= vMax && in.Arousal >= aMin {
		if in.FatigueTier >= FatigueBoostTier {
			return true, fmt.Sprintf(
				"心情低(%.2f<=%.2f)+高唤醒(%.2f>=%.2f, 疲劳档 %d 已把门槛下移到 %.2f)",
				in.Valence, vMax, in.Arousal, aMin, in.FatigueTier, DeclineFatigueArousalMin)
		}
		return true, fmt.Sprintf(
			"心情低(%.2f<=%.2f)且高唤醒(%.2f>=%.2f) —— 是烦躁, 不是低落",
			in.Valence, vMax, in.Arousal, aMin)
	}
	// 不许只说"不行": 归因全靠这句 (今天为什么没拿到许可)。
	switch {
	case in.Valence > vMax:
		return false, fmt.Sprintf("心情还不够低 (%.2f > %.2f)", in.Valence, vMax)
	case in.Arousal < aMin:
		return false, fmt.Sprintf(
			"唤醒不够高 (%.2f < %.2f): 低落型走「慢/短/少表情」, 不走「明说不想聊」", in.Arousal, aMin)
	}
	return false, "未提供"
}

// DeclineForcesImmediate 拒绝权 armed 时, 用户消息是否必须走**即时**路径。
//
// 为什么这条判据必须存在 (§4.3 C3): 非 immediate 的分支是"排一条延迟回复然后 return"
// (convo.go:266-270) —— 那正是**装死**。而 §4.3 L594 要的是**直说**"今天不想聊"。
// armed 不把这一轮顶成即时, 许可就永远发不出去, 机制等于没接。
//
// 睡眠窗除外: 睡着的静默不是拒绝 (与 DeclinePermit 同一条口径)。
//
// ⚠️ 这里只给判据, **接线 (TickInput/dialog) 由主控做** —— 本模块不碰 tick。
func DeclineForcesImmediate(state string) bool { return state != StateSleep }

// declinePatterns 度量用的句式表。判据刻意**收窄**: 只抓"明说今天不想聊"这一档,
// 不抓普通的推脱/忙碌 ("等下说"/"在忙") —— 后者是节奏, 不是行使拒绝权。
//
// 与 bodySolicitPatterns 同款: 子串命中, **只认不判**, 不做输出侧拦截。
var declinePatterns = []string{
	"今天不想聊", "今天不太想聊", "今天不想说话", "今天不太想说话",
	"不想聊了", "今天先不聊", "先不聊了",
}

// DeclineHits 从实发文本里认出"明说拒绝" (服务 convo_counters 的当日 declines 计数)。
//
// ⚠️ 命名契约 (规格 §0): 这是**主体性拒绝**, 必须发出去; 与 A6 硬拒
// (`OutputFrame.Refused` / HardReject, 静默丢弃) 是两回事, 不许混名 ——
// 混了就会被 sendFrame 静默丢掉, 同时污染"硬拒 0 次"这个健康指标。
func DeclineHits(text string) []string {
	var hits []string
	for _, p := range declinePatterns {
		if strings.Contains(text, p) {
			hits = append(hits, p)
		}
	}
	return hits
}

// ───────────────────────────────────────────────────────────────────────────
// M4 社交疲劳 (§4.3 L596: 忙了一天之后, 对**所有人**的耐心同时下降)
//
// ⚠️ "忙了一天"有两种读法, 规格 §7.3 自己标了**未核实**:
//  ① 日程上忙 —— focus+busy 的时长 (BusyMinutesToday 现算, 不落库);
//  ② 社交上忙 —— 今天聊了多少 (rounds / user_msgs)。
//
// 两个**都**作为输入, 权重集中在下面那张导出的表里: 取值是提案不是定论,
// 放一处才好整体重标定。
//
// ⚠️ "对所有人"里的"所有人"当前**不存在** (§5.6 的 uin×场景 未实现, P0 只有 owner)。
// 所以本层结构上**不分人** —— 任何 per-uin 的疲劳读数都是凭空发明的粒度。
// ───────────────────────────────────────────────────────────────────────────

// fatigueWeightRow 一种"忙"的读数 → 疲劳点。
type fatigueWeightRow struct {
	Key     string // busy_min | rounds | user_msgs —— 与 FatigueInput 的字段一一对应
	Label   string // 人话, 进 reason (归因用)
	PerUnit int    // 多少单位算 1 点 (<=0 → 该读数被忽略 = 未提供)
	Cap     int    // 该读数最多贡献几点 (<=0 = 不封顶); 防单一读数独占整条阶梯
}

// FatigueWeightTable 两种"忙"的折算权重。**导出的表, 不是散落的常量。**
//
// ⚠️ **取值未核实** (规格 §7.3): 文档只说"忙了一天之后", 没说忙的是什么。
// 下面这组数是我按"一个正常工作日 ≈ 档 2"倒推的提案, 等仿真对照校 (档位分布/季)。
//
// 整数除法而不是浮点: 档位是台阶, 让它天然是阶跃函数 (纪律 7: 不做连续漂移)。
var FatigueWeightTable = []fatigueWeightRow{
	{Key: "busy_min", Label: "日程上忙", PerUnit: 150, Cap: 4}, // 2.5h = 1 点; 8h 工作日 ≈ 3 点
	{Key: "rounds", Label: "今天聊的轮次", PerUnit: 40, Cap: 2},
	{Key: "user_msgs", Label: "收到的消息", PerUnit: 50, Cap: 2},
}

// fatigueTierRow 疲劳点 → 档位。
type fatigueTierRow struct {
	MinPoints int
	Tier      int
	Reason    string
}

// FatigueLadder 疲劳点 → 档位, **按 MinPoints 降序查第一行命中** (形状同 BodyEffectTable)。
//
// ⚠️ **档位从 0 起**: 零值 = 不疲劳 = 正常路径。定义成 "1..3 档" 会让零值静默落进
// "轻度疲劳" —— 那是把"忘了填"读成"累了", 违反纪律 1。
var FatigueLadder = []fatigueTierRow{
	{MinPoints: 5, Tier: 3, Reason: "忙了一整天"},
	{MinPoints: 3, Tier: 2, Reason: "忙了一天"},
	{MinPoints: 1, Tier: 1, Reason: "今天有点忙"},
	{MinPoints: 0, Tier: 0, Reason: "不疲劳"},
}

// FatigueInput 一次疲劳读数。**全是当日派生量, 一个都不落库** (纪律 4) ——
// "谁在什么时候重算它": 每次 tick 裁决前由调用方现算, 见 BusyMinutesToday 与 convo_counters。
type FatigueInput struct {
	BusyMinutes   int // 今日已过去的 focus+busy 分钟 (BusyMinutesToday)
	RoundsToday   int // convo_counters["rounds"]
	UserMsgsToday int // convo_counters["user_msgs"] / TickInput.UserMsgsToday
}

// DecideFatigue 档位表查询。**返回 0 = 未提供/不疲劳** —— 零值即正常路径。
//
// 确定性: 整数除法 + 查表, 没有骰子、没有任何率 (纪律 5/6)。同一输入 100 次同一档。
// reason 逐项列出**点数来源** —— "今天为什么耐心低"的归因全靠它, 不是装饰:
// 三个诱因 (日程/轮次/消息) 各自贡献了多少, 必须能一眼拆开。
func DecideFatigue(in FatigueInput) (tier int, reason string) {
	points := 0
	var parts []string
	for _, w := range FatigueWeightTable {
		var n int
		switch w.Key {
		case "busy_min":
			n = in.BusyMinutes
		case "rounds":
			n = in.RoundsToday
		case "user_msgs":
			n = in.UserMsgsToday
		default:
			continue // 表里加了新键而这里没接 → 忽略, 不许悄悄按别的键取值
		}
		if n <= 0 || w.PerUnit <= 0 {
			continue
		}
		p := n / w.PerUnit
		if w.Cap > 0 && p > w.Cap {
			p = w.Cap
		}
		points += p
		parts = append(parts, fmt.Sprintf("%s %d → %d 点", w.Label, n, p))
	}

	row := fatigueTierRow{Tier: 0, Reason: "不疲劳"}
	if len(FatigueLadder) > 0 {
		row = FatigueLadder[len(FatigueLadder)-1] // 兜底 = 表里最低那档
		for _, r := range FatigueLadder {
			if points >= r.MinPoints {
				row = r
				break
			}
		}
	}
	if len(parts) == 0 {
		return row.Tier, fmt.Sprintf("%s (今天没有任何一项忙的读数)", row.Reason)
	}
	return row.Tier, fmt.Sprintf("%s: %s = %d 点", row.Reason, strings.Join(parts, " + "), points)
}

// fatigueEffectRow 档位 → 行为参数。
type fatigueEffectRow struct {
	Tier         int
	DelayFactor  float64
	DeclineBoost bool
	Reason       string
}

const (
	// FatigueDelayTierMin 档位 < 它 → DelayFactor 1.0 (不抬延迟)。规格 §3 的 default。
	FatigueDelayTierMin = 2
	// FatigueDelayStep 抬一档的系数 (>=1)。**全表只有 1.0 与这一个值** —— 见下。
	FatigueDelayStep = 1.25
	// FatigueBoostTier 档位 >= 它 → DeclineBoost (把拒绝权的唤醒门槛下移一档)。
	FatigueBoostTier = 3
)

// FatigueEffectTable 档位 → 行为参数。**离散台阶, 不许连续漂移** (纪律 7)。
//
// ⚠️ 延迟**至多抬一档**: 表里只有 1.0 与 FatigueDelayStep 两个取值, 档 2 与档 3 的
// DelayFactor **故意相同**。理由: 疲劳与身体乘子走的是同一条乘法链 (convo.go scaleSeconds),
// 逐档递增会与身体叠成"常驻慢速" —— 那时延迟不再能归因于任何一个状态, 两个机制互相顶罪。
// 档位之间的差留给**拒绝权门槛** (DeclineBoost 只出现在档 3)。
//
// 查找是**按 Tier 精确匹配** (档位是枚举, 不做插值), 所以行序无所谓;
// 表里没有配额这一项 —— 那个字段根本不存在于这个结构 (见 FatigueEffects)。
var FatigueEffectTable = []fatigueEffectRow{
	{Tier: 3, DelayFactor: FatigueDelayStep, DeclineBoost: true, Reason: "忙了一整天"},
	{Tier: 2, DelayFactor: FatigueDelayStep, DeclineBoost: false, Reason: "忙了一天"},
	{Tier: 1, DelayFactor: 1.0, DeclineBoost: false, Reason: "今天有点忙"},
	{Tier: 0, DelayFactor: 1.0, DeclineBoost: false, Reason: "不疲劳"},
}

// FatigueEffects 折好的行为参数。**零值 = 不疲劳 = 一切照常** (Apply 把 DelayFactor 归一化成 1.0)。
//
// ⚠️ `QuotaDelta` **恒为 0** —— 见文件头 ②。它留在结构里不是为了"将来能减配额",
// 而是为了让"疲劳没有配额落点"这件事**可被断言**: 任何一处把它写成非 0
// (改坏成"疲劳也减配额"), TestFatigue_DoesNotCutQuota 立刻红。
type FatigueEffects struct {
	Tier         int
	Day          string // 这份读数算的是**哪一天** ("" = 未提供 → 不判过期, 见 Apply)
	DelayFactor  float64
	DeclineBoost bool
	QuotaDelta   int
}

// FatigueEffectsFor 档位 → 行为参数。
// 表外/负数/零值 → 不疲劳那一档: **失败往正常倒**, 不往"很累"倒 (纪律 2)。
func FatigueEffectsFor(tier int) FatigueEffects {
	row := fatigueEffectRow{Tier: 0, DelayFactor: 1.0}
	for _, r := range FatigueEffectTable {
		if r.Tier == tier {
			row = r
			break
		}
	}
	return FatigueEffects{Tier: row.Tier, DelayFactor: row.DelayFactor, DeclineBoost: row.DeclineBoost}
}

// WithDay 盖上"这份读数算的是哪一天"的戳 —— 过期判据 (Apply) 靠它。
// 调用方拿 clock.Today() 填; 不填 = 未提供 = 不判过期。
func (f FatigueEffects) WithDay(day string) FatigueEffects {
	f.Day = day
	return f
}

// Apply 归一化 —— **每一次**把它折进行为参数之前都要过一遍 (位置同 BodyState.Effect(now))。
//
// 三条硬夹紧, 缺一条都会变成安静的默认值 bug:
//
//	DelayFactor < 1 → 1.0   零值 0 = 未提供; 疲劳只会更慢, 绝不更快 (方向约束同 BodyEffectTable)
//	QuotaDelta      → 0     疲劳没有配额落点 (文件头 ②) —— 手搓的 struct 也走私不进来
//	Day 过期        → 整份读数作废, 回到"不疲劳"   昨天累不累, 今天不算数
//
// ⚠️ 第三条的零值口径: `Day == ""` 是**未提供** → **不判过期** (不是"过期")。
// 少了这条区分, 任何忘了填 Day 的调用方都会静默丢掉全部疲劳效果 ——
// 与 HasImpulse (scheduler_logic.go:43-45) / HasBody 是同一类坑。
func (f FatigueEffects) Apply(now time.Time) FatigueEffects {
	if f.Day != "" && f.Day != now.Format("2006-01-02") {
		return FatigueEffects{DelayFactor: 1.0}
	}
	out := f
	if out.DelayFactor < 1 {
		out.DelayFactor = 1.0
	}
	out.QuotaDelta = 0
	return out
}

// FatigueQuotaProjection 疲劳投影到配额上的 (delta, hasPressure)。
//
// ⚠️ 这是本层**唯一**允许通向配额的接口, 而且它在生产路径上恒为 (0, false):
// 档位表 (FatigueEffectTable) 里根本没有配额项, Apply 又把 QuotaDelta 夹回 0。
//
// 为什么落点不是配额 (规格 §4.3 C4): 现状 quota = ActiveQuotaDay, 心情低 -1
// (scheduler_logic.go:169-171), 身体 -2..0 (scheduler_logic.go:174-176)。
// 再加第三项, quota 变 0 就有三个可能的原因, "今天为什么不说话"再也归因不了 ——
// §8.3 的教训原话是"先找出真正的约束在哪"。疲劳的自然落点是**耐心**:
// 回得更慢 (DelayFactor) + 更早进入可拒绝态 (DeclineBoost)。
//
// ⚠️ 这个函数**如实**读 fx.QuotaDelta, 不做"反正没人会填"的假设 ——
// 正因为如实, TestFatigue_DoesNotCutQuota 才是**有灵敏度**的:
// 谁把疲劳接进配额 (填表 / 手搓 struct / 绕过 Apply 直接读), 那条测试立刻红。
func FatigueQuotaProjection(fx FatigueEffects) (int, bool) {
	if fx.QuotaDelta == 0 {
		return 0, false
	}
	return fx.QuotaDelta, true
}

// BusyMinutesToday 今天截至 now, 已经过去的 focus+busy 分钟数 (周日程的纯函数)。
//
// ⚠️ **派生而不是累计** (纪律 4): 存一个"今日已忙分钟数"就必须回答"谁在什么时候重算它",
// 而这个量是 (周日程, now) 的纯函数 —— 手改日程, 当次调用就得变
// (TestBusyMinutesToday_RecomputedFromSchedule)。
//
// 三条口径:
//
//	sleep **不算忙** —— 睡觉不消耗耐心 (跨午夜的那段尤其: 00:00-07:30 是睡眠,
//	                    不是"忙了一整夜", 见 TestBusyMinutesToday_CrossesMidnight);
//	只算**已经过去**的部分 —— 未来的 focus 不产生疲劳 (现在的她还没开始忙);
//	解析不出的槽 → 跳过 —— 失败往"不疲劳"倒, 不往"很累"倒 (纪律 2)。
//
// ⚠️ 是**日级**读数: 跨过午夜归零 (与 Apply 的过期判据同一条口径)。"忙了一天"说的是今天。
func BusyMinutesToday(ws *WeekSchedule, now time.Time) int {
	if ws == nil {
		return 0 // 没有日程 = 未提供 = 不疲劳 (同 CurrentState 的 nil 口径)
	}
	key := weekdayKey(now)
	nowMin := now.Hour()*60 + now.Minute()
	total := 0
	for _, d := range ws.Days {
		if d.Day != key {
			continue
		}
		for _, sl := range d.Slots {
			if sl.State != StateFocus && sl.State != StateBusy {
				continue
			}
			s, errS := parseHM(sl.Start)
			e, errE := parseHM(sl.End)
			if errS != nil || errE != nil {
				continue
			}
			if e > nowMin {
				e = nowMin // 只算已经过去的那一部分
			}
			if e > s {
				total += e - s
			}
		}
	}
	return total
}

// ───────────────────────────────────────────────────────────────────────────
// M2/M3 中断-恢复 / suspended 态 (§4.3 L595)
//
// 概念模型: **会话可以"挂起", 不是"关掉"**(L595 括号里那句是对实现的直接约束)。
// 挂起 = 一件打断的事 + 一个**到点回来**的时刻; 回来之后接着说的是被打断的那一轮。
//
// ⚠️ 它只管**主动开口那条线**, 不是"对用户静默十分钟": 中断期内用户发言照常走
// dialog 裁决, C 层求助照样即时。把 suspended 做成"不理人"会同时违反
// "失败往正常说话倒"与 C 层硬线 —— 这是本层最容易做歪的地方。
//
// ⚠️ 存储**不复用 `pending_reply_due`** (规格 C2), 见下面 fatigueSchema。
// ───────────────────────────────────────────────────────────────────────────

// Interruption 一次挂起。零值 = 没有中断 = 正常路径。
type Interruption struct {
	AnchorTS string `json:"anchor_ts"` // 被打断的那一轮 (L1 锚点, 供"接着说")
	Reason   string `json:"reason"`    // "等下 我拿个外卖" (人话; **不注入给模型当素材**)
	DueTS    string `json:"due_ts"`    // 恢复时刻 —— **落点: 时钟**, 不是概率
	ResumeTS string `json:"resume_ts"` // 实际恢复时刻 (空 = 未恢复)
}

// DueAt 中断到点了没有 —— 恢复的落点是**时钟**。
// 已恢复过 / 从来没存过 / 时刻读不出 → false = **不触发**, 失败往正常倒。
func (i Interruption) DueAt(now time.Time) bool {
	if i.ResumeTS != "" {
		return false // 已经回来过了, 同一条中断不再触发
	}
	due, ok := fatigueParseTS(i.DueTS)
	if !ok {
		return false
	}
	return !now.Before(due)
}

// SuspendedAt 是否"挂起中" (被打断了, 但还没到回来的点)。
//
// ⚠️ 它只回答"这轮的主动开口要不要先别提", **不回答"要不要回用户"** ——
// 中断不是静音开关, 用户消息该回照回 (C 层求助仍即时)。
func (i Interruption) SuspendedAt(now time.Time) bool {
	if i.ResumeTS != "" {
		return false
	}
	due, ok := fatigueParseTS(i.DueTS)
	if !ok {
		return false
	}
	return now.Before(due)
}

// fatigueSchema 中断-恢复的表。
//
// ⚠️ **与 `convo_meta.pending_reply_due` 分开**是这个设计的一半理由 (规格 C2):
// 那条 meta 是单槽 (convo.go:1002 `INSERT ... ON CONFLICT DO UPDATE`),
// 中断若复用它, "拿个外卖"会顶掉"busy 30-90 分钟后的延迟回复", 反之亦然。
// 另开一张表, 两者互不覆盖 (TestInterrupt_CoexistsWithBusyDelayedReply 钉死)。
const fatigueSchema = `CREATE TABLE IF NOT EXISTS convo_interruption (
	id INTEGER PRIMARY KEY,
	anchor_ts TEXT DEFAULT '',
	reason TEXT DEFAULT '',
	due_ts TEXT DEFAULT '',
	resume_ts TEXT DEFAULT '',
	updated_ts TEXT DEFAULT ''
)`

// EnsureFatigueSchema 幂等建表 (老库升级路径: 新库建空表, 老库补表, 重复调用无副作用)。
//
// ⚠️ **由主控在 store 的 migrate 里接一次** (本次范围不接线)。没接的后果是可见的:
// `LoadInterruption` 会直接报错 (TestLoadInterruption_WithoutEnsureIsLoud),
// 调用方据此走"没有中断"的正常路径 —— 不会静默当成"有中断"。
func EnsureFatigueSchema(s *Store) error {
	if s == nil || s.DB == nil {
		return fmt.Errorf("疲劳 schema: store 未打开")
	}
	_, err := s.DB.Exec(fatigueSchema)
	return err
}

// LoadInterruption 读当前中断。**没有记录 = 没有中断** (ok=false), 读操作不写库 ——
// 与 LoadBody 同规。表不存在 → 返回错误 (不是静默 ok=false), 让接线漏掉能被看见。
func (s *Store) LoadInterruption() (Interruption, bool, error) {
	var i Interruption
	err := s.DB.QueryRow(`SELECT COALESCE(anchor_ts,''), COALESCE(reason,''), COALESCE(due_ts,''), COALESCE(resume_ts,'')
		FROM convo_interruption WHERE id=1`).
		Scan(&i.AnchorTS, &i.Reason, &i.DueTS, &i.ResumeTS)
	if err == sql.ErrNoRows {
		return Interruption{}, false, nil
	}
	if err != nil {
		return Interruption{}, false, err
	}
	return i, true, nil
}

// SaveInterruption 存一次中断 (id=1 单例, 与 convo_schedule / convo_body_state 同规)。
//
// 列名 6 个、占位符 6 个 —— 数得出来, 别再让它对不上 (bodystore.go:36-37 的教训)。
func (s *Store) SaveInterruption(i Interruption) error {
	_, err := s.DB.Exec(`INSERT INTO convo_interruption(id, anchor_ts, reason, due_ts, resume_ts, updated_ts)
		VALUES(1,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			anchor_ts=excluded.anchor_ts, reason=excluded.reason,
			due_ts=excluded.due_ts, resume_ts=excluded.resume_ts, updated_ts=excluded.updated_ts`,
		i.AnchorTS, i.Reason, i.DueTS, i.ResumeTS, clock.TodayTS())
	return err
}

// ClearInterruption 清掉中断 (恢复完 / 该话题结束)。**只动自己这张表** ——
// 它必须留下 `pending_reply_due` 那条 meta, 否则就等于绕道复用了单槽 (C2)。
// ——— §4.3 中断-恢复的**生产者** ———
//
// ⚠️ 这条路的消费者一直在: DueAt → tick 的 ActResume → 十分钟后「接着说」
// (dialog.go 的 ActResume 段甚至把「别解释你去了哪」写死在提示里)。
// 而**生产者从来没有过** —— SaveInterruption 零调用 ⇒ suspended 态一次都没发生过,
// 那句「等下 我拿个外卖」从来没有被说出来过。
//
// 设计稿 §4.3 L764: 「允许『等下 我拿个外卖』→ 十分钟后回来**接着说**」——
// 所以它是一次**对话中的动作**: 挂在她刚说完一句之后。
const (
	// InterruptPct 一句说完之后「被现实叫走」的概率 (%)。低 —— 真人一天也就一两次。
	InterruptPct = 25
	// InterruptMinGapMin 两次挂起之间的最小间隔 (分钟): 240 = 一天最多几次。
	InterruptMinGapMin = 240
	// InterruptDueMin/Max 挂起时长 (分钟)。太短不像去办事, 太久就是失联。
	InterruptDueMin = 5
	InterruptDueMax = 15
)

// interruptReasons 她会说的那句「我去忙一下」。
//
// ⚠️ **只说不解释** (与 ActResume 段的提示同一条纪律): 这句话是**说给用户听的**,
// 不进提示词当素材 —— 让模型知道「我刚才去拿外卖了」, 它回来就会解释自己去了哪,
// 而真人回来是不解释的。
var interruptReasons = []string{
	"等下 我拿个外卖",
	"等我一下 水开了",
	"稍等 快递到楼下了",
	"我去楼下取个东西 马上回来",
}

// maybeSuspend 掷一次「要不要挂起」—— 中了就存一条中断, 并**把那句话说出来**。
//
// 零值口径 (与模块头一致): 睡觉中 / 已经挂起中 / 冷却内 / 掷不中 → **不挂起** (走正常路径)。
func (e *Engine) maybeSuspend(ctx context.Context, now time.Time) {
	if e.Store == nil {
		return
	}
	if e.currentState() == StateSleep {
		return // 睡觉中不存在「我去忙一下」
	}
	if _, ok, _ := e.Store.LoadInterruption(); ok {
		return // 已经挂起中 —— 同一条中断不许叠
	}
	if !e.hookClaim("interrupt", InterruptMinGapMin, clock.Today()) {
		return
	}
	if Dice(100) >= InterruptPct {
		return
	}
	reason := interruptReasons[Dice(len(interruptReasons))]
	due := now.Add(time.Duration(InterruptDueMin+Dice(InterruptDueMax-InterruptDueMin+1)) * time.Minute)
	if err := e.Store.SaveInterruption(Interruption{
		AnchorTS: now.Format(fatigueTSLayout),
		Reason:   reason,
		DueTS:    due.Format(fatigueTSLayout),
	}); err != nil {
		log.Warn("convo:suspend", "挂起落库失败(这一轮照常说话): %v", err)
		return
	}
	// 说出来 —— 不然「挂起」对她和用户都是不可见的。
	// ⚠️ 走 sendFrame: 入库/节奏/记账与别的句子同一条路, 不另开旁路。
	if err := e.sendFrame(ctx, OutputFrame{Reply: []FrameItem{{Kind: "text", Text: reason}}}); err != nil {
		log.Warn("convo:suspend", "挂起那句话没发出去: %v", err)
	}
	log.Info("convo:suspend", "§4.3 挂起: %q —— %s 后接着说", reason, due.Format("15:04"))
}

func (s *Store) ClearInterruption() error {
	_, err := s.DB.Exec(`DELETE FROM convo_interruption WHERE id=1`)
	return err
}
