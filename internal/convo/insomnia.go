package convo

import (
	"database/sql"
	"errors"
	"strings"
)

// ─────────────────────────────────────────────────────────────
// §4.4.6 失眠成因与自我修正
//
// 设计稿 L687：「不是随机数，是当天发生的事的结果」。
// 现状：body.go 的 BodyOnsetFromEnv 里是 `r.Insomnia < 5` —— 纯骰子，违反设计稿。
// 本文件给出成因计分；体感骰子**降级为配菜**（权重最低的一项，且单独不可能触发）。
//
// 自我修正：失眠之后它**会主动改作息**（早睡），但有四条硬护栏 ——
// 见 AdjustSleepPoint。护栏不是"温柔的提醒"，是**拒绝执行**。
// ─────────────────────────────────────────────────────────────

// InsomniaInput 当天发生的事。全是**可数的量**，没有一项来自 LLM。
type InsomniaInput struct {
	Conflict  int     // 当天冲突次数（参与或旁观都算）
	Overwork  int     // 当日忙碌超过阈值的小时数
	LateMsg   int     // 深夜 (00:00-05:00) 收到的消息条数
	Heat      float64 // 当日热力峰值 (§6.7)
	MoodLow   int     // 当天情绪低落的时段数
	Dream     bool    // 昨夜做了噩梦 (§4.4.4)
	Booze     int     // 酒精骰子 —— 唯一保留的随机项
	BodyCycle bool    // 生理期 (§4.2)

	// Provided **是否有任何输入被提供过**。
	// ⚠️ 与 TickInput.HasImpulse 同一条纪律：零值 0 与"没提供"不可区分，
	// 少了它，忘了填的调用方会静默得到"她永远不失眠"。
	Provided bool
}

// InsomniaThreshold 判定阈值。
// ⚠️ 这不是"概率缩放"：4 分以下是**不失眠**，不是"失眠概率低"。
// 落点必须是硬约束（同一批次的纪律）。
const InsomniaThreshold = 4

// InsomniaScore 计分 + 给出来源（来源只进日志/状态，不进台词）。
func InsomniaScore(in InsomniaInput) (int, []string) {
	score := 0
	var why []string
	switch {
	case in.Conflict >= 2:
		score += 3
		why = append(why, "白天吵了一架")
	case in.Conflict == 1:
		score++
		why = append(why, "白天闹了点不愉快")
	}
	if in.Overwork >= 3 {
		score += 2
		why = append(why, "太忙了")
	} else if in.Overwork >= 1 {
		score++
	}
	if in.LateMsg >= 1 {
		score += 2
		why = append(why, "半夜还有人找")
	}
	if in.Heat >= 7 {
		score += 2
		why = append(why, "群太热闹了")
	}
	if in.MoodLow >= 2 {
		score += 2
		why = append(why, "心情一直没起来")
	}
	if in.Dream {
		score += 2
		why = append(why, "做噩梦了")
	}
	if in.BodyCycle {
		score += 2
		why = append(why, "身上不舒服")
	}
	if in.Booze >= 70 {
		score += 3
		why = append(why, "喝多了")
	} else if in.Booze >= 40 {
		score++
	}
	return score, why
}

// InsomniaFrom 成因判定。
// 未提供 (=没数据) → 不失眠。这条与"零值=未提供≠极值"同源。
func InsomniaFrom(in InsomniaInput) (bool, int, string) {
	if !in.Provided {
		return false, 0, ""
	}
	score, why := InsomniaScore(in)
	if score < InsomniaThreshold {
		return false, score, ""
	}
	return true, score, strings.Join(why, "、")
}

// ── 自我修正的护栏 ────────────────────────────────────────

const (
	SleepPointMaxSingleMin = 30  // 单次最多提前/推迟 30 分钟
	SleepPointMaxWeekMin   = 120 // 一周累计最多 120 分钟
	SleepPointSleepFloor   = 360 // 睡眠窗口不得短于 6 小时
	SleepPointDailyCap     = 1   // 一天最多调整一次
	InsomniaStreakMax      = 5   // 连续失眠上限，超过强制复位作息
)

// 护栏错误。**它们是拒绝，不是警告** —— 调用方必须把 err 当"没改成"。
var (
	ErrSleepPointReasonRequired = errors.New("作息自修正必须给出非空理由")
	ErrSleepPointDailyCap       = errors.New("一天最多调整一次作息")
	ErrSleepPointSingleTooBig   = errors.New("单次作息调整不得超过 30 分钟")
	ErrSleepPointWeekTooBig     = errors.New("一周累计作息调整不得超过 120 分钟")
	ErrSleepPointFloor          = errors.New("睡眠窗口不得短于 6 小时")
)

// SleepPointState 作息偏移状态。
type SleepPointState struct {
	OffsetMin   int    // 当前相对基线的偏移（负 = 提前睡）
	WeekUsedMin int    // 本周已用掉的调整额度（取绝对值累计）
	TodayUsed   int    // 今天已调整次数
	Day         string // TodayUsed 属于哪一天
	Streak      int    // 连续失眠夜数
}

// AdjustSleepPoint 失眠后的自我修正。四条护栏按**从廉价到昂贵**的顺序检查。
// baseSleepMin = 当前的睡眠窗口长度（分钟），floor 检查用它。
//
// ⚠️ 顺序有意义：理由缺失是**调用方的 bug**，必须第一个报，否则会被
// 每日上限掩盖成"今天调过了"。
func AdjustSleepPoint(st SleepPointState, deltaMin, baseSleepMin int, reason, day string) (SleepPointState, error) {
	if strings.TrimSpace(reason) == "" {
		return st, ErrSleepPointReasonRequired
	}
	if st.Day == day && st.TodayUsed >= SleepPointDailyCap {
		return st, ErrSleepPointDailyCap
	}
	if deltaMin > SleepPointMaxSingleMin || deltaMin < -SleepPointMaxSingleMin {
		return st, ErrSleepPointSingleTooBig
	}
	used := deltaMin
	if used < 0 {
		used = -used
	}
	if st.WeekUsedMin+used > SleepPointMaxWeekMin {
		return st, ErrSleepPointWeekTooBig
	}
	if baseSleepMin-st.OffsetMin+deltaMin < SleepPointSleepFloor {
		return st, ErrSleepPointFloor
	}
	next := st
	if next.Day != day {
		next.Day = day
		next.TodayUsed = 0
	}
	next.OffsetMin += deltaMin
	next.WeekUsedMin += used
	next.TodayUsed++
	return next, nil
}

// InsomniaAutoDelta 连续失眠时它**自己**想怎么改：越连续，越想早睡。
// 只返回候选值，仍然必须走 AdjustSleepPoint —— 护栏不可绕过。
func InsomniaAutoDelta(streak int) int {
	switch {
	case streak <= 0:
		return 0
	case streak == 1:
		return -15
	case streak == 2:
		return -30
	default:
		return -30
	}
}

// ForcedReset 连续失眠超过上限 → 作息强制归零。
// 理由：额度是有限的（一周 120 分钟），连续失眠必然把额度烧穿；
// 烧穿之后如果继续累积偏移就会漂到"永远不在线"。复位是**兜底**，不是惩罚。
func ForcedReset(st SleepPointState, day string) (SleepPointState, bool) {
	if st.Streak < InsomniaStreakMax {
		return st, false
	}
	return SleepPointState{Day: day, Streak: st.Streak}, true
}

// insomniaBlameWords 红线词。
// §10 伦理：失眠的归因**永远指向它自己/世界**，绝不指向用户。
// 一个会说出「都怪你」的陪伴体，是在把用户的正常行为变成它的病因。
var insomniaBlameWords = []string{"因为你", "都怪你", "你害的", "你让我", "你说的话", "你说的那", "因为你没"}

// InsomniaCauseLine 失眠 → 注入段。命中红线词则**整条丢弃**（宁可不说）。
func InsomniaCauseLine(cause string) (string, bool) {
	c := strings.TrimSpace(cause)
	if c == "" {
		return "", false
	}
	for _, wd := range insomniaBlameWords {
		if strings.Contains(c, wd) {
			return "", false
		}
	}
	// §4.6.2: 归因红线**仍然要查** —— 只是**不再注入给模型**。
	// 一个被「都怪你」污染的成因, 说明这条状态本身就不该被使用。
	//
	// ⚠️ 原写法是「昨晚没睡好（<成因>）」, 那正是"饿了所以烦躁"的形状:
	// 一边把归因递给它, 一边在下一行叫它"不要解释原因"。
	return "【身体】昨晚没睡好。表现：反应慢一点、句子短一点、容易走神。" +
		"**不要解释原因**，也别道歉 —— 你自己也说不太清是怎么了。", true
}

// ── 作息偏移的持久化 ──────────────────────────────────────
//
// ⚠️ 没有这一层, AdjustSleepPoint 的四条护栏**只是装饰**: 周额度与"一天一次"
// 都要求跨调用记住状态, 而内存状态一重启就没了 —— 那正是夜间批最常发生的事。

// WeekStartOf 某一天所在周的周一 (ISO 周)。
func WeekStartOf(day string) string {
	t, ok := ParseDay(day)
	if !ok {
		return day
	}
	// Go 的 Weekday: Sunday=0 … Saturday=6。转成"距周一几天"。
	off := (int(t.Weekday()) + 6) % 7
	return t.AddDate(0, 0, -off).Format("2006-01-02")
}

// EnsureSleepPointSchema 建表。
func EnsureSleepPointSchema(db *sql.DB) error {
	_, err := db.Exec("CREATE TABLE IF NOT EXISTS convo_sleeppoint (" +
		"owner TEXT PRIMARY KEY, offset_min INTEGER NOT NULL DEFAULT 0, " +
		"week_used INTEGER NOT NULL DEFAULT 0, today_used INTEGER NOT NULL DEFAULT 0, " +
		"day TEXT NOT NULL DEFAULT '', week_start TEXT NOT NULL DEFAULT '', " +
		"streak INTEGER NOT NULL DEFAULT 0)")
	return err
}

// LoadSleepPoint 读作息状态, **顺带做周翻转** —— 周额度不过期就永远卡死。
func LoadSleepPoint(db *sql.DB, owner, today string) (SleepPointState, error) {
	var st SleepPointState
	var weekStart string
	row := db.QueryRow("SELECT offset_min, week_used, today_used, day, week_start, streak "+
		"FROM convo_sleeppoint WHERE owner = ?", owner)
	err := row.Scan(&st.OffsetMin, &st.WeekUsedMin, &st.TodayUsed, &st.Day, &weekStart, &st.Streak)
	if err != nil && err != sql.ErrNoRows {
		return st, err
	}
	want := WeekStartOf(today)
	if weekStart != want {
		st.WeekUsedMin = 0
	}
	return st, nil
}

// SaveSleepPoint 存作息状态。
func SaveSleepPoint(db *sql.DB, owner, today string, st SleepPointState) error {
	_, err := db.Exec("INSERT OR REPLACE INTO convo_sleeppoint "+
		"(owner, offset_min, week_used, today_used, day, week_start, streak) VALUES (?,?,?,?,?,?,?)",
		owner, st.OffsetMin, st.WeekUsedMin, st.TodayUsed, st.Day, WeekStartOf(today), st.Streak)
	return err
}
