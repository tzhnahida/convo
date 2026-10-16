package convo

import (
	"database/sql"
	"fmt"

	"github.com/tzhnahida/convo/internal/clock"
)

// ——— §4.4.5 / §4.4.6 睡眠质量结算 (夜间批的第 4 步) ———
//
// 它是"睡眠 → 次日行为"的唯一出口。设计稿 §4.4.7 给了一句关键提示:
//
//	"睡眠不足 = **一种身体状态**"
//
// 所以我们**不新增热力字段、不新增段**: 让睡眠不足产出一个 BodyState
// (BodyInsomnia 已存在), 于是 HeatMods.BodyUnwell **自动生效** —— 一处机制、两个落点、零改动。
//
// ⚠️ 但睡眠质量**本身要单独存** (convo_night 一行一夜): convo_body_state 是**单行表**,
// "感冒 + 没睡好"会互相覆盖。

// Night 一夜的记录。**只存事实** —— tier / effects 全部现算 (派生量不落库)。
type Night struct {
	Night        string // 睡点所归属的那一天 (NightKeyOf)
	SleepStartTS string // 实际睡点 (含被聊天拖延后的)
	WakeTS       string
	LatencyMin   int // 入睡耗时 (躺下睡不着)
	Interrupts   int // 被吵醒次数
	InterruptMax int // 最深一次: 0 无 | 1 轻微醒转 | 2 彻底醒了
	Nightmare    bool
	Insomnia     bool // 睡点后仍活跃
}

// SleepQualityInput 结算的全部输入。**全是当天已发生的事实**。
//
// ⚠️⚠️ **零值陷阱 (零值 = 未提供 ≠ 极值)**:
// Interrupts 的零值既可能是"没被打断", 也可能是"没读数" ——
// 这是本模块最容易做错的一处。所以必须有 HasReadings。
// 与 scheduler_logic 的 HasImpulse / HasBody / HasHeat 同款。
type SleepQualityInput struct {
	HasReadings bool // false = 整条未提供 → **中性档**, 不是"睡得很差"

	LatencyMin   int  // 入睡耗时 (分钟)
	Interrupts   int  // 被吵醒次数
	InterruptMax int  // 最深一次
	Nightmare    bool // 噩梦
	Insomnia     bool // 失眠
}

// sleepQualityRow 档位表的一行 —— 与 BodyEffectTable 同构。
type sleepQualityRow struct {
	MinScore     int // 综合分下限 (降序查第一行命中)
	DelayFactor  float64
	LengthFactor float64
	QuotaDelta   int
	Reason       string
}

// SleepQualityLadder 档位表。**三条方向性约束与 BodyEffectTable 同款**:
//
//	DelayFactor >= 1 · LengthFactor <= 1 · QuotaDelta <= 0
//
// 破了任何一条, "睡眠质量"这一层就从"有代价的状态"退化成随机扰动。
var SleepQualityLadder = []sleepQualityRow{
	{MinScore: 90, DelayFactor: 1.0, LengthFactor: 1.0, QuotaDelta: 0, Reason: "睡得不错"},
	{MinScore: 70, DelayFactor: 1.05, LengthFactor: 0.95, QuotaDelta: 0, Reason: "还行"},
	{MinScore: 40, DelayFactor: 1.25, LengthFactor: 0.8, QuotaDelta: -1, Reason: "没睡好"},
	{MinScore: 0, DelayFactor: 1.5, LengthFactor: 0.6, QuotaDelta: -1, Reason: "几乎没睡"},
}

// SleepEffects 折好的次日行为参数。**零值 = 没读数 = 一切照常**。
type SleepEffects struct {
	Active       bool
	Tier         int // 0 很差 .. 3 很好
	Score        int
	DelayFactor  float64
	LengthFactor float64
	QuotaDelta   int
	Reason       string
}

// SleepScore 把一夜的事实折成分数 (0-100)。**纯函数**。
//
// 扣分幅度是**档位化**的, 不做连续公式 —— 连续漂移会让"今天状态如何"变成一个
// 说不清的量, 而它要同时决定延迟、长度、配额三件事。
func SleepScore(in SleepQualityInput) int {
	if !in.HasReadings {
		return 100 // 未提供 → 满分 → 走"一切照常"那一档
	}
	s := 100
	// 入睡耗时: 超过 30 分钟开始扣, 每 15 分钟一档。
	if in.LatencyMin > 30 {
		s -= 10 + (in.LatencyMin-30)/15*10
	}
	// 被吵醒: 次数 × 10; 最深一次额外扣 (彻底醒了比轻微醒转重得多)。
	s -= in.Interrupts * 10
	switch in.InterruptMax {
	case 1:
		s -= 5
	case 2:
		s -= 20
	}
	if in.Nightmare {
		s -= 15
	}
	if in.Insomnia {
		s -= 25
	}
	if s < 0 {
		s = 0
	}
	return s
}

// SettleSleepQuality 第 4 步。返回档位与次日行为参数。
//
// **失败往"正常说话"倒**: 没读数 → 满分 → 中性档, 不是"睡得很差"。
// 反过来的话, 一次读库失败会让它第二天**整天迟钝**, 而没人能从表现上看出那是故障。
func SettleSleepQuality(in SleepQualityInput) (int, SleepEffects) {
	score := SleepScore(in)
	row := SleepQualityLadder[len(SleepQualityLadder)-1]
	tier := 0
	for i, r := range SleepQualityLadder {
		if score >= r.MinScore {
			row = r
			tier = len(SleepQualityLadder) - 1 - i
			break
		}
	}
	return tier, SleepEffects{
		Active:       in.HasReadings,
		Tier:         tier,
		Score:        score,
		DelayFactor:  row.DelayFactor,
		LengthFactor: row.LengthFactor,
		QuotaDelta:   row.QuotaDelta,
		Reason:       row.Reason,
	}
}

// nightSchema 一夜一行 (只存事实)。单行 SQL 常量, 与 moodstore 同规。
const nightSchema = "CREATE TABLE IF NOT EXISTS convo_night (" +
	"night TEXT PRIMARY KEY, sleep_start_ts TEXT DEFAULT '', wake_ts TEXT DEFAULT '', " +
	"latency_min INTEGER DEFAULT 0, interrupts INTEGER DEFAULT 0, interrupt_max INTEGER DEFAULT 0, " +
	"nightmare INTEGER DEFAULT 0, insomnia INTEGER DEFAULT 0, updated_ts TEXT DEFAULT '')"

// EnsureNightSchema 幂等建表。
func EnsureNightSchema(s *Store) error {
	if s == nil {
		return fmt.Errorf("nil store")
	}
	_, err := s.DB.Exec(nightSchema)
	return err
}

// SaveNight 记一夜 (同 night 覆盖 —— 一夜只有一条)。
func (s *Store) SaveNight(n Night) error {
	_, err := s.DB.Exec("INSERT INTO convo_night (night, sleep_start_ts, wake_ts, latency_min, interrupts, interrupt_max, nightmare, insomnia, updated_ts) VALUES(?,?,?,?,?,?,?,?,?) "+
		"ON CONFLICT(night) DO UPDATE SET sleep_start_ts=excluded.sleep_start_ts, wake_ts=excluded.wake_ts, latency_min=excluded.latency_min, interrupts=excluded.interrupts, interrupt_max=excluded.interrupt_max, nightmare=excluded.nightmare, insomnia=excluded.insomnia, updated_ts=excluded.updated_ts",
		n.Night, n.SleepStartTS, n.WakeTS, n.LatencyMin, n.Interrupts,
		n.InterruptMax, boolToInt(n.Nightmare), boolToInt(n.Insomnia), clock.TodayTS())
	return err
}

// LastNight 读最近一夜 (按 night 倒序)。
//
// **没有记录 → ok=false, 不是错误** —— 与 LoadBody / LoadInterruption 同规:
// "没记录"与"读失败"必须分得开, 否则漏接线会表现成"它一直睡得很好"。
func (s *Store) LastNight() (Night, bool, error) {
	var n Night
	var nm, ins int
	err := s.DB.QueryRow("SELECT night, COALESCE(sleep_start_ts,''), COALESCE(wake_ts,''), "+
		"COALESCE(latency_min,0), COALESCE(interrupts,0), COALESCE(interrupt_max,0), "+
		"COALESCE(nightmare,0), COALESCE(insomnia,0) FROM convo_night ORDER BY night DESC LIMIT 1").
		Scan(&n.Night, &n.SleepStartTS, &n.WakeTS, &n.LatencyMin, &n.Interrupts,
			&n.InterruptMax, &nm, &ins)
	if err == sql.ErrNoRows {
		return Night{}, false, nil
	}
	if err != nil {
		return Night{}, false, err
	}
	n.Nightmare, n.Insomnia = nm != 0, ins != 0
	return n, true, nil
}

// sleepEffectOf 当前生效的睡眠效应 (读不到 → 中性档 —— 失败往正常倒)。
func (e *Engine) sleepEffect() SleepEffects {
	n, ok, err := e.Store.LastNight()
	if err != nil || !ok {
		_, fx := SettleSleepQuality(SleepQualityInput{})
		return fx
	}
	// ⚠️ **只认"昨夜"** —— 三天前没睡好不该影响今天。
	// 归属日与夜间批闸门用同一个键 (NightKeyOf), 免得两处各算一套。
	today := NightKeyOf(clock.Now())
	yesterday := NightKeyOf(clock.Now().AddDate(0, 0, -1))
	if n.Night != today && n.Night != yesterday {
		_, fx := SettleSleepQuality(SleepQualityInput{})
		return fx
	}
	_, fx := SettleSleepQuality(SleepQualityInput{
		HasReadings:  true,
		LatencyMin:   n.LatencyMin,
		Interrupts:   n.Interrupts,
		InterruptMax: n.InterruptMax,
		Nightmare:    n.Nightmare,
		Insomnia:     n.Insomnia,
	})
	return fx
}
