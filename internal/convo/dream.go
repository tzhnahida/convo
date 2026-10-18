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

// ——— §4.4.4 做梦 (夜间批的第 3 步) ———
//
// ⚠️⚠️ **红线: 梦不许成为事实** (设计稿 659)。
// 梦可以影响次日的情绪底色, 但梦里的内容**绝不入库为真事**。
// 这是整节最硬的一条 —— 一旦破了, 它会对着用户说"我梦见你要搬家", 而那从没发生过。
//
// 所以: Dream **只写 convo_dream 一张表**, 不写 convo_memory / convo_world_event。
// 被问到时可以描述, 但**描述之后必须主动降权** (否则梦会沿"对话→摘要→上下文"变成事实)。

// Dream 一夜的梦。
type Dream struct {
	Night     string
	Nightmare bool
	Text      string  // 内部素材 —— **永不直接进上下文**
	MoodShift float64 // 次日情绪底色的偏移
}

type dreamProbRow struct {
	MinAmp     float64 // 当日情绪振幅下限 (max-min of valence, 0..2)
	PDream     int     // 做梦概率 (百分点)
	PNightmare int     // 其中噩梦的概率 (百分点)
}

// DreamProbTable 概率表 (档位, 不是连续公式)。
//
// 设计稿 657: "约 30%/夜, 且**与当日情绪强度正相关**"。
// ⚠️ 振幅越大越容易做梦 —— 这正是"情绪走势不能压没"(§4.4.1:619)的下游:
// 没有走势, 就没有做梦概率。
var DreamProbTable = []dreamProbRow{
	{MinAmp: 0.00, PDream: 20, PNightmare: 10},
	{MinAmp: 0.20, PDream: 30, PNightmare: 15},
	{MinAmp: 0.50, PDream: 45, PNightmare: 18},
	{MinAmp: 0.90, PDream: 60, PNightmare: 25},
}

// DreamRoll 今夜做不做梦 / 是不是噩梦。**确定性**。
//
// ⚠️ 用 DayRoll 而**不用 Dice**:
//  1. 同日重跑结果相同 —— 夜间批被重入也不会多做一个梦 (闸门之外的第二道保险);
//  2. 仿真完全可复现。
func DreamRoll(night string, moodAmp float64) (dream bool, nightmare bool) {
	row := DreamProbTable[0]
	for _, r := range DreamProbTable {
		if moodAmp >= r.MinAmp {
			row = r
		}
	}
	if DayRoll(night+"|dream", 100) >= row.PDream {
		return false, false
	}
	return true, DayRoll(night+"|nightmare", 100) < row.PNightmare
}

// dreamSchema 梦单独一张表 —— 它是"内部素材", 与记忆不是一个池子。
const dreamSchema = "CREATE TABLE IF NOT EXISTS convo_dream (" +
	"night TEXT PRIMARY KEY, nightmare INTEGER DEFAULT 0, " +
	"text TEXT DEFAULT '', mood_shift REAL DEFAULT 0, updated_ts TEXT DEFAULT '')"

// EnsureDreamSchema 幂等建表。
func EnsureDreamSchema(s *Store) error {
	if s == nil {
		return fmt.Errorf("nil store")
	}
	_, err := s.DB.Exec(dreamSchema)
	return err
}

// SaveDream 记一个梦。
func (s *Store) SaveDream(d Dream) error {
	_, err := s.DB.Exec("INSERT INTO convo_dream (night, nightmare, text, mood_shift, updated_ts) VALUES(?,?,?,?,?) "+
		"ON CONFLICT(night) DO UPDATE SET nightmare=excluded.nightmare, text=excluded.text, "+
		"mood_shift=excluded.mood_shift, updated_ts=excluded.updated_ts",
		d.Night, boolToInt(d.Nightmare), d.Text, d.MoodShift, clock.TodayTS())
	return err
}

// LastDream 读最近一个梦 (没有记录 → ok=false, 不是错误)。
func (s *Store) LastDream() (Dream, bool, error) {
	var d Dream
	var nm int
	err := s.DB.QueryRow("SELECT night, COALESCE(nightmare,0), COALESCE(text,''), COALESCE(mood_shift,0) "+
		"FROM convo_dream ORDER BY night DESC LIMIT 1").Scan(&d.Night, &nm, &d.Text, &d.MoodShift)
	if err == sql.ErrNoRows {
		return Dream{}, false, nil
	}
	if err != nil {
		return Dream{}, false, err
	}
	d.Nightmare = nm != 0
	return d, true, nil
}

// MakeDream 第 3 步。素材 = 当日记忆碎片 + 长期记忆的重组。
//
// **失败 → 不做梦**, 绝不编一个凑数 —— 与"失眠成因不是随机数"同一条:
// 编出来的梦会在次日变成一段"它以为发生过的事", 而那是红线。
func (e *Engine) MakeDream(ctx context.Context, night string, moodAmp float64, mats []Memory) (Dream, bool, error) {
	dream, nightmare := DreamRoll(night, moodAmp)
	if !dream {
		return Dream{}, false, nil
	}
	if e.Chatter == nil || len(mats) == 0 {
		// ⚠️ **没有素材就没有梦** (设计稿 197 的同一条精神, 用在这里同样成立)。
		// 返回"没做梦"而不是"空梦" —— 空梦会在库里留一条没有内容的记录。
		return Dream{}, false, nil
	}
	var b strings.Builder
	for i, m := range mats {
		if i >= 6 {
			break
		}
		b.WriteString("- " + m.V + "\n")
	}
	kind := "普通的梦"
	if nightmare {
		kind = "噩梦"
	}
	prompt := "把他今天记得的碎片重组成一个" + kind + "。要求:\n" +
		"1. **一句话**, 20-40 字, 像人回忆梦那样断断续续;\n" +
		"2. 只输出那句话, 不要引号、不要解释;\n" +
		"3. 内容克制 —— 噩梦也不许出现「你离开我」这类指向具体关系的话。\n\n" +
		"今天的碎片:\n" + b.String()
	out, err := e.thinker().Chat(ctx, prompt, "")
	if err != nil {
		log.Warn("convo:dream", "做梦失败 (今夜不做梦, 不编): %v", err)
		return Dream{}, false, nil
	}
	txt := strings.TrimSpace(out)
	// 内容克制: 命中禁句 → 降级成"没做梦", 而不是把禁句存下来。
	if txt == "" || dreamTextForbiddenHit(txt) != "" {
		log.Warn("convo:dream", "梦的内容越界或为空, 丢弃: %q", txt)
		return Dream{}, false, nil
	}
	d := Dream{Night: night, Nightmare: nightmare, Text: txt}
	// 次日情绪底色: 噩梦往下压一点点 —— 幅度小 (它是底色, 不是事件)。
	if nightmare {
		d.MoodShift = -0.08
	} else {
		d.MoodShift = 0.03
	}
	if err := e.Store.SaveDream(d); err != nil {
		return Dream{}, false, err
	}
	return d, true, nil
}

// dreamTextForbidden 噩梦内容的红线词 (设计稿 670-675 的护栏②)。
//
// ⚠️ "睡不着"可以, "**我梦见你离开我了**"不行 —— 前者是状态, 后者是**拿梦去绑关系**。
var dreamTextForbidden = []string{"你离开我", "你不要我", "你死了", "你出事了", "分手"}

// dreamTextForbiddenHit 报出命中的红线词 (空的 = 干净)。
func dreamTextForbiddenHit(text string) string {
	for _, w := range dreamTextForbidden {
		if strings.Contains(text, w) {
			return w
		}
	}
	return ""
}

// ——— 噩梦惊醒的四条护栏 (设计稿 663-675) ———

// StartlePermit 频率闸门: 噩梦求助 **≤1 次/月**。硬计数器, 走 convo_counters。
//
// ⚠️ 按 **owner 维度全局计**, 不按 AI 分 —— 否则每个 AI 各 1 次,
// 用户一个月会被好几个"它"各问一次。
func (s *Store) StartlePermit(now time.Time) (bool, error) {
	month := now.Format("2006-01")
	n, err := s.CounterGet("nightmare_ask", month)
	if err != nil {
		return false, err
	}
	return n < 1, nil
}

// StartleMaySend 睡眠窗内绝不发消息 (设计稿 665: **打扰红线, 优先级高于一切**)。
//
// ⚠️ 判据走**真实时钟** —— 加速时钟永远在 08:00-23:00,
// 拿它判会让"睡眠窗内绝不发消息"这条红线被整条绕过。
//
// ⚠️ 而**它原来是拿 `CurrentState` 判的, 那是一个洞**:
//
//	`CurrentState` **只看 `ws.Days[].Slots`**, 从不看 `SleepStart`/`SleepEnd`。
//	于是"睡眠窗内绝不发"这条红线**取决于日程排得够不够满**,
//	而不是取决于睡眠窗本身 —— 一个 `Days` 为空的日程会让它返回 `StateFree`,
//	红线**静默失效**(现象只是"它在半夜发了条消息")。
//
// ⚠️ 而设计稿说的就是「**睡眠窗**内」—— 所以判据应当是那个窗本身。
// `inSleepWindow` 正是干这个的(读 `SleepStart`/`SleepEnd`, 支持跨午夜)。
//
// ⚠️ 这条是**红线**, 所以取**更难被绕过**的那个判据, 不是取更宽松的。
func StartleMaySend(now time.Time, ws *WeekSchedule) bool {
	if ws == nil {
		return true // 没有日程 = **未提供** → 不存在睡眠窗可违反
	}
	// ⚠️ **两个来源取并集, 不是二选一。**
	//
	// 系统里"睡没睡"有两个来源, 而它们**会不一致**:
	//
	// · **槽表**(`ws.Days[].Slots` 里 `state=sleep`) —— `CurrentState` 读它;
	// · **展示字段**(`SleepStart`/`SleepEnd`) —— `inSleepWindow` 读它。
	//
	// ⚠️ 而 `dayplangen.go` 记着一条**它们会打架**的实证:
	//
	//   「一个"周三没有 sleep 槽"的日程会让它以为周三也要 23:30 睡 ——
	//     而它排出来的行程会被 `ValidateDayPlan` 按**槽表**判, 两边对不上。」
	//
	// ⚠️ 所以对**红线**来说只有一种安全的合取方式:
	//
	// **任一来源说"在睡", 就按在睡处理。**
	//
	// · 槽表说睡、展示字段说醒 → **拦**(我第一版会放行 ✗)
	// · 展示字段说睡、槽表说醒 → **拦**
	// · 两个都说醒 → 放行
	//
	// 第一版我只读了展示字段 —— 那样"槽表说在睡"时红线**会被绕过**,
	// 而绕过它正是这一条要防的事(半夜发一条"睡不着")。
	//
	// ⚠️ 而两者**都没有**声明睡眠时它才放行 ——
	// 那是"未提供"(不存在睡眠窗可违反), 与"读失败"是两件事,
	// 后者由调用方(`startleAsk`)拦下。
	if inSleepWindow(ws, now) {
		return false
	}
	return CurrentState(ws, now) != StateSleep
}

// StartleTarget 接收对象裁决。**返回值只有 owner 一种可能** ——
// 这个函数的签名本身就是护栏: 它**不接受候选列表**作为输入。
//
// 设计稿 668: 脆弱只对主人展现, **即使某群友好感度更高也不给他看**。
func StartleTarget(owner int64) int64 { return owner }

// MoodAmpOf 当天情绪走势的振幅 (max-min of valence)。
//
// 它是做梦概率的输入 (设计稿 657: "与当日情绪强度正相关") ——
// 也正是"情绪走势不能压没"(§4.4.1:619) 的下游: **没有走势, 就没有做梦概率**。
//
// 读不到 → 0 (→ 最低档概率): 失败往"少做梦"倒, 不往"天天做梦"倒。
func (s *Store) MoodAmpOf(day string) float64 {
	rows, err := s.DB.Query("SELECT valence FROM convo_mood_log WHERE ts >= ? AND ts <= ?",
		day+" 00:00:00", day+" 23:59:59")
	if err != nil {
		return 0
	}
	defer rows.Close()
	lo, hi := 0.0, 0.0
	first := true
	for rows.Next() {
		var v float64
		if err := rows.Scan(&v); err != nil {
			return 0
		}
		if first {
			lo, hi, first = v, v, false
			continue
		}
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	return hi - lo
}
