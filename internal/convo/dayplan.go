package convo

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
)

// ─────────────────────────────────────────────────────────────
// §4.5 **层 2 · 行程层**: 次日具体行程 (2026-10-15 新增)
//
// 层 1(周日程五档)回答的是「这个时段它愿不愿意说话」—— 那是**意愿配置**。
// 层 2 回答的是「它今天到底要干什么」—— 那是**生活**。两者不许互相推导:
// 从 focus 推不出"它今天在干什么", 行程也不决定它愿不愿意说话。
//
// ⚠️ **五种来源, 没有第六种**: cast / night / event / self / user。
// 模型现编的一天, 白天一被追问就穿帮 —— 所以"哪来的"必须是**结构化字段**,
// 不是一个可以随口填的说明。
//
// ⚠️ 确定性兜底(生成失败时)只用 cast / event / self 三种 ——
// 那三种都能从**库里已有的事实**推出来, 所以它仍然是"有来源", 不是凭空。
// ─────────────────────────────────────────────────────────────

// DayItem 一条具体行程。
type DayItem struct {
	// At 起始时刻 "HH:MM"。
	At string `json:"at"`
	// Dur 时长, 如 "40m" / "1h30m"。空 = 没说。
	Dur string `json:"dur,omitempty"`
	// With 和谁。**空 = 独自** —— 这是合法且常见的一档, 不是缺字段。
	With  string `json:"with,omitempty"`
	What  string `json:"what"`
	Place string `json:"place,omitempty"`
	// From 来源, 见 DayFrom*。
	From string `json:"from"`
	Done bool   `json:"done,omitempty"`
	// Undone §4.5.2: **打算了但没做** —— 合法且常见的一档, 不是失败态。
	//
	// 真人报告自己一天时**最高频的句式就是未完成**:
	// 「本来想看书, 结果刷了一晚上手机」。计划与执行的落差是整个日常质感里最厚的一层,
	// 而它几乎没有成本 —— 一个布尔位 + 一句表达许可。
	Undone bool `json:"undone,omitempty"`
	// Forced §5.5「对自己的承诺」的**出口**: 连着几次没做成之后, 这一条是**必须做成的**。
	//
	// ⚠️ 没有它, 它会变成一个永远在自责的人 —— 那是抑郁叙事, 不是人格。
	Forced bool `json:"forced,omitempty"`
}

// DayPlan 一天的行程。
type DayPlan struct {
	Day   string    `json:"day"`
	Items []DayItem `json:"items"`
}

// 五种来源。
const (
	DayFromCast  = "cast"  // 世界观配角 (它生活里的人)
	DayFromNight = "night" // 前一夜 AI 互聊 / 配角群聊的回流
	DayFromEvent = "event" // 事件池 / 环境
	DayFromSelf  = "self"  // 它自己安排的 (自主预约)
	DayFromUser  = "user"  // 用户约定
)

// DayPlanSources 合法来源集合。
var DayPlanSources = map[string]bool{
	DayFromCast: true, DayFromNight: true, DayFromEvent: true,
	DayFromSelf: true, DayFromUser: true,
}

// 配额。
const (
	DayPlanMinItems = 4 // 太少没有生活感
	DayPlanMaxItems = 7 // 太多变成连续剧 (§3.5 同一条理由)
	// DayPlanSoloEvery 每 N 条里至少 1 条是**独自**的。
	//
	// ⚠️ 要的是"它有生活", 不是"它一天到晚在社交" —— **一个人待着也是生活**。
	DayPlanSoloEvery = 3
)

// DurMinutes 把 "40m" / "1h30m" / "90" 解析成分钟。解析不了 → 0。
//
// ⚠️ 解析不了**不报错**: 时长是可选的("没说"), 而拒绝一条行程的代价
// 远大于不知道它要多久。
func DurMinutes(s string) int {
	v := strings.TrimSpace(strings.ToLower(s))
	if v == "" {
		return 0
	}
	total, num, seen := 0, 0, false
	for _, c := range v {
		switch {
		case c >= '0' && c <= '9':
			num = num*10 + int(c-'0')
			seen = true
		case c == 'h':
			total += num * 60
			num = 0
		case c == 'm':
			total += num
			num = 0
		default:
			// 其它字符(空格 / 中文单位)直接跳过 —— 宽容解析。
		}
	}
	if num > 0 {
		total += num // 光秃秃一个数字: 当分钟。
	}
	if !seen {
		return 0
	}
	return total
}

// dayPlanDay 取某一天的槽表。找不到那天 → nil。
//
// ⚠️ **2026-10-15 修 (测试抓出来的)**: 行程的 `day` 是**日期**("2026-10-16"),
// 而周日程的槽表是按**星期**分档的("thu")。原来拿日期直接去槽表里找 ——
// **永远找不到**, 于是"不许落在 sleep 槽"这条约束**一次都没有生效过**。
// 而它是静默的: 行程照排, 只是它会在凌晨三点"去分拣仓搬箱子"。
//
// 兼容两种写法: 传日期先折成星期; 本来就是星期键(测试/内部调用)则直接用。
func dayPlanDay(ws *WeekSchedule, day string) *DaySchedule {
	if ws == nil {
		return nil
	}
	key := strings.TrimSpace(day)
	if contains(dayOrder, key) {
		// 已经是星期键
	} else if t, ok := ParseDay(key); ok {
		key = weekdayKey(t)
	} else {
		return nil
	}
	for i := range ws.Days {
		if ws.Days[i].Day == key {
			return &ws.Days[i]
		}
	}
	return nil
}

// DayItemInSleep 这条行程是否**落在层 1 的 sleep 槽里**。
//
// §4.5.2: 落在 sleep 槽 → **拒绝该条** —— 它在睡觉, 不能同时在搬箱子。
// ⚠️ 跨午夜的 sleep 段(23:30-24:00 与 00:00-07:30)就是靠槽表判的, 不许另写一套。
func DayItemInSleep(ws *WeekSchedule, day, at string, durMin int) bool {
	d := dayPlanDay(ws, day)
	if d == nil {
		return false // 没有那天的槽表 → 判不了 → 不拦 (失败往"能安排"倒)
	}
	s, err := parseHM(at)
	if err != nil {
		return false
	}
	e := s + durMin
	if e <= s {
		e = s + 30 // 没给时长 → 按半小时估 (一条行程总得占点时间)
	}
	for _, sl := range d.Slots {
		if sl.State != StateSleep {
			continue
		}
		ss, err1 := parseHM(sl.Start)
		se, err2 := parseHM(sl.End)
		if err1 != nil || err2 != nil {
			continue
		}
		if s < se && ss < e { // 区间相交
			return true
		}
	}
	return false
}

// NormalizeDayPlan 兜底 + 裁剪: 丢空条目、丢掉落在 sleep 里的、封顶。
//
// ⚠️ 顺序是**先按来源过滤, 再按 sleep 过滤, 最后封顶** ——
// 反过来(先封顶)会让一条落在 sleep 的行程把后面合法的那条挤掉。
func NormalizeDayPlan(p DayPlan, ws *WeekSchedule) DayPlan {
	out := DayPlan{Day: strings.TrimSpace(p.Day)}
	for _, it := range p.Items {
		it.At = strings.TrimSpace(it.At)
		it.What = strings.TrimSpace(it.What)
		it.With = strings.TrimSpace(it.With)
		it.Place = strings.TrimSpace(it.Place)
		it.Dur = strings.TrimSpace(it.Dur)
		it.From = strings.TrimSpace(strings.ToLower(it.From))
		if it.What == "" || !DayPlanSources[it.From] {
			continue
		}
		if _, err := parseHM(it.At); err != nil {
			continue
		}
		if DayItemInSleep(ws, out.Day, it.At, DurMinutes(it.Dur)) {
			continue
		}
		out.Items = append(out.Items, it)
	}
	if len(out.Items) > DayPlanMaxItems {
		out.Items = out.Items[:DayPlanMaxItems]
	}
	return out
}

// SoloCount 一天里"独自"的条目数 (With 为空)。
func (p DayPlan) SoloCount() int {
	n := 0
	for _, it := range p.Items {
		if strings.TrimSpace(it.With) == "" {
			n++
		}
	}
	return n
}

// UndoneCount 一天里"打算了但没做"的条数。
func (p DayPlan) UndoneCount() int {
	n := 0
	for _, it := range p.Items {
		if it.Undone {
			n++
		}
	}
	return n
}

// ValidateDayPlan 硬约束。**写入前必须过这一关。**
//
// ⚠️ 上界(7)在这里卡死; 下界(4)是**生成器的目标**, 校验只要求非空 ——
// 因为"今天确实只有三件事"不该让整份行程作废, 而"一条都没有"才是真的坏。
func ValidateDayPlan(p DayPlan, ws *WeekSchedule) error {
	if strings.TrimSpace(p.Day) == "" {
		return fmt.Errorf("行程缺 day")
	}
	if len(p.Items) == 0 {
		return fmt.Errorf("行程为空 —— 一份空行程和白天的追问对不上")
	}
	if len(p.Items) > DayPlanMaxItems {
		return fmt.Errorf("行程 %d 条, 上限 %d", len(p.Items), DayPlanMaxItems)
	}
	for i, it := range p.Items {
		if strings.TrimSpace(it.What) == "" {
			return fmt.Errorf("第 %d 条没有内容", i)
		}
		if !DayPlanSources[it.From] {
			return fmt.Errorf("第 %d 条来源非法 %q —— **五种来源, 没有第六种**", i, it.From)
		}
		if _, err := parseHM(it.At); err != nil {
			return fmt.Errorf("第 %d 条时刻非法 %q", i, it.At)
		}
		if DayItemInSleep(ws, p.Day, it.At, DurMinutes(it.Dur)) {
			return fmt.Errorf("第 %d 条落在 sleep 槽里: %s %s", i, it.At, it.What)
		}
	}
	// §4.5.2 ⚠️ **不许每一条都 undone**(全都没做 = 没有生活, 也是废人叙事)。
	// 一天 undone ≤ 1/3 —— 硬闸, 不是建议值。
	if n := p.UndoneCount(); n*3 > len(p.Items) {
		return fmt.Errorf("undone 有 %d 条 (%d 条里超过 1/3) —— 全都没做不是生活, 是废人叙事", n, len(p.Items))
	}
	// 独自至少占 1/N。
	if need := (len(p.Items) + DayPlanSoloEvery - 1) / DayPlanSoloEvery; p.SoloCount() < need {
		return fmt.Errorf("独自条目只有 %d 条, 至少要 %d 条 (%d 条里每 %d 条一条) —— 一个人待着也是生活",
			p.SoloCount(), need, len(p.Items), DayPlanSoloEvery)
	}
	return nil
}

// ——— 注入 ———

// DayPlanLine 渲染行程段。空串 = 不注入。
//
// ⚠️ 这是**日程 / 系统事实**, 按 §4.6.2 的边界**必须给全**:
// 它就是被"它手里根本没有事实"那个病灶逼出来的那一类东西 ——
// 糊掉它, 21:46 说刚醒 / 21:47 说刚忙完就会立刻回来。
func DayPlanLine(p DayPlan, now time.Time) string {
	if len(p.Items) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n【今天的事】(系统事实 —— 你今天的安排; 说到就说具体的那件事)\n")
	cur := now.Hour()*60 + now.Minute()
	for _, it := range p.Items {
		with := "自己"
		if it.With != "" {
			with = it.With
		}
		past := ""
		if m, err := parseHM(it.At); err == nil && m <= cur {
			past = " (已经过去了)"
		}
		// ⚠️ §4.5.2: **不许把 undone 说成 done** —— 那才是造假。
		// 这一行是它报告一天时唯一的依据, 所以要显式写出来。
		if it.Undone {
			past += " **本来打算做, 结果没做成**"
		}
		place := ""
		if it.Place != "" {
			place = " @" + it.Place
		}
		fmt.Fprintf(&sb, "- %s %s (%s)%s%s\n", it.At, it.What, with, place, past)
	}
	return sb.String()
}

// ——— 存储 ———

// EnsureDayPlanSchema 幂等建表 (老库缺表也能补)。
func EnsureDayPlanSchema(s *Store) error {
	_, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS convo_day_plan (` +
		`owner TEXT NOT NULL, day TEXT NOT NULL, json TEXT NOT NULL DEFAULT '',` +
		`created_ts TEXT NOT NULL DEFAULT '', PRIMARY KEY (owner, day))`)
	return err
}

// SaveDayPlan 落库。**校验不过不许写** —— 一份坏行程会污染一整天的对话。
func (s *Store) SaveDayPlan(owner string, p DayPlan, ws *WeekSchedule) error {
	p = NormalizeDayPlan(p, ws)
	if err := ValidateDayPlan(p, ws); err != nil {
		return err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`INSERT INTO convo_day_plan (owner, day, json, created_ts) VALUES (?,?,?,?) `+
		`ON CONFLICT(owner, day) DO UPDATE SET json = excluded.json, created_ts = excluded.created_ts`,
		owner, p.Day, string(raw), clock.TodayTS())
	return err
}

// LoadDayPlan 读。第二个返回值 = **有没有** (零值 ≠ 空行程)。
func (s *Store) LoadDayPlan(owner, day string) (DayPlan, bool, error) {
	var raw string
	err := s.DB.QueryRow(`SELECT json FROM convo_day_plan WHERE owner = ? AND day = ?`, owner, day).Scan(&raw)
	if err == sql.ErrNoRows {
		return DayPlan{}, false, nil
	}
	if err != nil {
		return DayPlan{}, false, err
	}
	var p DayPlan
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return DayPlan{}, false, err
	}
	return p, true, nil
}
