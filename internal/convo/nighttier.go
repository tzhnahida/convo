package convo

import (
	"strings"
	"sync"
	"time"
)

// ——— §4.4.3 记忆四档 (夜间批的第 2 步) ———
//
// 它是"什么留下来"的唯一裁决点。四档 (设计稿 641-651):
//
//	forget  当天就丢的闲聊细节 —— **唯一的动作是「不写」**, 不落库
//	short   3-14 天, 不加固就掉
//	long    关系事实 / 边界 / 梗 / 纪念日 —— 永久 (但边界类**降权而非禁止**)
//	habit   **行为模式**, 不是事实 ("用户总在深夜出现" → 推迟睡觉 → 写回日程表)
//
// ⚠️ 第四档是关键: 它不是"记住了某事", 而是"**我的作息因此变了**"。

const (
	TierForget = "forget"
	TierShort  = "short"
	TierLong   = "long"
	TierHabit  = "habit"
)

// tierRow 判据表的一行 (按优先级从上到下查第一行命中)。
type tierRow struct {
	Tier   string
	Reason string
	Match  func(m MemoryDelta) bool
}

// MemoryTierTable 四档判据。**纯函数表** —— 顺序即优先级。
//
// ⚠️ 顺序的讲究: habit **最先**判 —— 一条"作息偏晚"既像偏好又像习惯,
// 而它们的**落点完全不同** (一个进记忆, 一个写回日程表)。
// 判模糊了会导致"它记住了你熬夜"但"作息没变" —— 那正是第四档要防的事。
var MemoryTierTable = []tierRow{
	{Tier: TierHabit, Reason: "行为模式 (写回日程, 不是记事实)",
		Match: func(m MemoryDelta) bool { return strings.EqualFold(strings.TrimSpace(m.Kind), "habit") }},
	{Tier: TierLong, Reason: "关系事实 / 边界 / 纪念日 (永久)",
		Match: func(m MemoryDelta) bool {
			k := strings.ToLower(strings.TrimSpace(m.Kind))
			switch k {
			case "relation", "boundary", "anniversary", "family", "pet", "meme":
				return true
			}
			l := m.Label + m.V
			return strings.Contains(l, "生日") || strings.Contains(l, "纪念日") ||
				strings.Contains(l, "不能提") || strings.Contains(l, "别问")
		}},
	{Tier: TierForget, Reason: "低置信的闲聊细节 (当天就丢)",
		Match: func(m MemoryDelta) bool {
			if m.Confidence >= 0.6 {
				return false
			}
			k := strings.ToLower(strings.TrimSpace(m.Kind))
			return k == "" || k == "chat" || k == "other"
		}},
	// 兜底: 其余全是短期。**它是兜底而不是判据** —— 没命中任何一条时,
	// "留 3-14 天看看"比"永久留下"和"当场丢掉"都安全。
	{Tier: TierShort, Reason: "默认短期 (3-14 天, 不加固就掉)", Match: func(MemoryDelta) bool { return true }},
}

// DeriveTier 四档判据。**纯函数** (纪律: 决策器不接 LLM)。
func DeriveTier(m MemoryDelta) string {
	for _, r := range MemoryTierTable {
		if r.Match(m) {
			return r.Tier
		}
	}
	return TierShort
}

// TierReason 归因 (进日志与轨迹: "这条为什么没留下")。
func TierReason(m MemoryDelta) string {
	for _, r := range MemoryTierTable {
		if r.Match(m) {
			return r.Reason
		}
	}
	return ""
}

// ⚠️ 这里原来有一个 `TierMemory`(四档落库) —— **已删**。
//
// 它被 `memory.go` 里那条路径**取代**了, 而那条路**更强**:
//
// | | `TierMemory`(已删) | `memory.go` 那条 |
// |---|---|---|
// | forget / habit 不落库 | ✓ | ✓ |
// | **置信度分流** | ✗(一律落 active) | ✓(≥0.7 落 active, 否则 pending) |
// | 痕迹 | 只返回条数 | `noteTierDropped`(进了读数) |
//
// 所以它不是"没接上", 而是**接上它会是一次回归** ——
// 一条低置信度的记忆会被它直接落成 active, 绕过整个待求证机制。
//
// ⚠️ 而它注释里那两条规则**已经在 `memory.go` 那一边实现了**(逐字):
// 「forget 档不落库」/「habit 档不写记忆表, 它要去改日程」。
// 删的是**重复的那一份**, 不是那两条规则。

// BoundaryDecayFloor 边界类记忆的**降权地板** (§7.4 L1580 / §4.4.3 L828)。
//
// 设计稿原话: 「久不用的记忆降权, 但**边界类记忆降权而非永久禁止**
// (否则越骂越畏缩)」。
//
// ⚠️ **单一真源**: `LongMemoryDecay` 与**注入打分**(`TopMemoriesFor`)都用它 ——
// 两处各写一个 0.5 早晚会分叉, 而分叉的表现是"边界有时候管用有时候不管"。
const BoundaryDecayFloor = 0.5

// isBoundaryKind 是不是边界类记忆。
//
// ⚠️ Kind 是自由文本(不是枚举), 大小写与空格都可能不一样 —— 一律 TrimSpace + EqualFold。
func isBoundaryKind(kind string) bool {
	return strings.EqualFold(strings.TrimSpace(kind), "boundary")
}

// LongMemoryDecay 长期档的权重衰减。
//
// ⚠️ **边界类降权而非禁止** (设计稿 §7.4:1126):
// 边界是"别拿 X 开玩笑", 时间久了它会**淡**一些 —— 因为人也这样,
// 而且永久禁止会让它越骂越畏缩, 那比复读更不像人。
// 但它**永远不会掉到 0**: 降到 0.3 就不动了。
func LongMemoryDecay(m Memory, now time.Time) float64 {
	w := 1.0
	if t, ok := parseMemTS(m.UpdatedTS); ok {
		days := now.Sub(t).Hours() / 24
		switch {
		case days > 365:
			w = 0.3
		case days > 90:
			w = 0.5
		case days > 30:
			w = 0.7
		}
	}
	// 边界类额外保底: 它是"不能碰"的事, 降权只降**提及频率**, 不降**禁忌强度**。
	if isBoundaryKind(m.Kind) && w < BoundaryDecayFloor {
		w = BoundaryDecayFloor
	}
	return w
}

// parseMemTS 读记忆时间戳 (与全包同一套格式)。
func parseMemTS(s string) (time.Time, bool) {
	t, err := time.ParseInLocation(bodyTSLayout, strings.TrimSpace(s), time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// ——— 第四档: 行为模式 → 写回日程表 ———

// HabitSignal 一条从行为里抽出来的模式。
type HabitSignal struct {
	Key        string // "late_night_presence"
	Evidence   int    // 观察到的次数
	WindowDays int    // 观察窗口
	SuggestMin int    // 建议的睡点位移 (分钟), 正 = 推迟
	Reason     string
}

// HabitFromMemories 从记忆里抽行为模式。**零 LLM** (统计)。
//
// ⚠️ 它**只产出建议**, 不直接改日程 —— 改作息是 §4.5.3 的事,
// 那条路有自己的护栏 (单次 ≤30 分钟 / 累计 ≤2 小时每周 / 睡眠窗 ≥6 小时)。
// 在这里直接改 = 绕过全部护栏。
func HabitFromMemories(ms []Memory, now time.Time) []HabitSignal {
	lateNight := 0
	for _, m := range ms {
		if !strings.EqualFold(strings.TrimSpace(m.Kind), "habit") {
			continue
		}
		v := m.V + m.Label
		// ⚠️ **这是一张未核实的关键词表** (设计稿没给判据, 本仓库也没有语料)。
		// 它是**廉价判据**: 宁可漏判 (不抽模式) 也不误判 (把一次熬夜当作息改了)。
		// 将来若要接真的作息统计 (从 convo_messages 的活跃时刻现算), 只换这一处。
		if strings.Contains(v, "晚") || strings.Contains(v, "熬夜") ||
			strings.Contains(v, "深夜") || strings.Contains(v, "夜猫") ||
			strings.Contains(v, "凌晨") || strings.Contains(v, "一两点") {
			lateNight++
		}
	}
	var out []HabitSignal
	if lateNight >= HabitMinEvidence {
		out = append(out, HabitSignal{
			Key: "late_night_presence", Evidence: lateNight, WindowDays: 30,
			SuggestMin: 30, // 推迟半小时 —— 幅度小, 与 §4.5.3 的单次上限一致
			Reason:     "它在深夜出现得比日程表写的更频繁",
		})
	}
	return out
}

// HabitMinEvidence 抽一条行为模式所需的最少观察次数。
//
// ⚠️ 太小会让"有一次熬夜"变成"作息改了" —— 那是把噪声当模式。
const HabitMinEvidence = 3

// ——— 四档的可观测读数 ———
//
// ⚠️ 没有它, "今天丢了几条"是**看不见的** —— 而"遗忘"恰恰是最需要被观察的一步:
// 丢多了它记不住事, 丢少了它变成笔记本, 两种都只有靠数字才能发现。

var (
	tierMu    sync.Mutex
	tierStats = map[string]int{}
)

// noteTierDropped 记一次"因为分档而没有写"。
func noteTierDropped(tier string) {
	tierMu.Lock()
	tierStats[tier]++
	tierMu.Unlock()
}

// NightTierStatsSnapshot 读四档的丢弃计数 (副本)。
func NightTierStatsSnapshot() map[string]int {
	tierMu.Lock()
	defer tierMu.Unlock()
	cp := make(map[string]int, len(tierStats))
	for k, v := range tierStats {
		cp[k] = v
	}
	return cp
}

// ResetNightTierStats 清零 (仿真每次运行开始时)。
func ResetNightTierStats() {
	tierMu.Lock()
	tierStats = map[string]int{}
	tierMu.Unlock()
}
