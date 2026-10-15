package convo

import (
	"fmt"
	"strings"
	"time"
)

// ——— A2 状态是系统事实, 不是模型的自述 ———
//
// 病灶: 21:46 一句「刚醒」, 21:47 一句「刚忙完, 摸鱼中」—— 一分钟内自相矛盾, 而且两条
// 跟日程表都对不上。根因不是模型爱撒谎, 是**它手里根本没有事实**: 旧注入只给了一个状态词
// ("【你现在】摸鱼中 (15:30-18:00)"), 剩下全靠它自己发挥。
//
// 修法 (§1 事实由系统管、语感由模型管): 系统把 **三元组 (状态, 从何时起, 为什么)** 交给它,
// 模型只负责**润色成话**, 不许发明状态。
//
// ⚠️ 为什么 `Since` 必须**合并相邻同状态槽**: 否则会出现"从 15:30 起在摸鱼", 而它其实
// 从 13:00 就闲着了 —— 那是修完 A2 又新造的一处自相矛盾。
type StateFact struct {
	State  string // 系统持有的状态 (sleep/focus/busy/slack/free)
	Since  string // "HH:MM" —— 该状态**连续**从何时开始
	Reason string // 为什么是这个状态 (哪条机制定的 / 到几点)
	// WakeAt = 今天起床时间 (睡眠窗结束)。
	// 多这一个字段是**被病灶逼出来的**: "刚醒"这句话, 只靠 (状态,起点,原因) 判不了 ——
	// 状态是 idle 并不排除刚起床。有了起床时间, "已经起了 13 小时还说刚醒"才是可算的。
	WakeAt string // "HH:MM"; 空 = 未知
}

// asleep 判断睡眠窗 (支持跨午夜)。
func inSleepWindow(ws *WeekSchedule, at time.Time) bool {
	if ws == nil || ws.SleepStart == "" || ws.SleepEnd == "" {
		return false
	}
	s, err1 := parseHM(ws.SleepStart)
	e, err2 := parseHM(ws.SleepEnd)
	if err1 != nil || err2 != nil {
		return false
	}
	min := at.Hour()*60 + at.Minute()
	if s <= e {
		return min >= s && min < e
	}
	return min >= s || min < e
}

// BuildStateFact 把日程表 + 睡眠窗折算成一条**系统事实**。
func BuildStateFact(ws *WeekSchedule, now time.Time) StateFact {
	f := StateFact{Since: "—", Reason: "没有日程表"}
	if ws == nil {
		return f
	}
	if inSleepWindow(ws, now) {
		f.State = StateSleep
		f.Since = ws.SleepStart
		f.Reason = "睡眠窗, 到 " + ws.SleepEnd
		return f
	}
	f.WakeAt = ws.SleepEnd

	day := daySchedule(ws, now)
	if day == nil {
		f.State = StateFree
		f.Reason = "日程表上没有今天"
		return f
	}
	idx := slotIndexAt(day, now)
	if idx < 0 {
		f.State = StateFree
		f.Reason = "日程表上这段没安排"
		return f
	}
	// ⚠️ **先把"连续同一状态"的整段定下来, 再据此写 Since/Reason**。
	// 顺序写反 (先写 Since 再往回并) 会得到"从 13:00 起"配"到 15:30"——
	// 那正是 A2 要消灭的自相矛盾, 只不过换成了我们自己造的。
	state := day.Slots[idx].State
	lo, hi := idx, idx
	for lo > 0 && day.Slots[lo-1].State == state {
		lo--
	}
	for hi+1 < len(day.Slots) && day.Slots[hi+1].State == state {
		hi++
	}
	f.State = state
	f.Since = day.Slots[lo].Start
	f.Reason = "日程表安排, 到 " + day.Slots[hi].End
	return f
}

func daySchedule(ws *WeekSchedule, at time.Time) *DaySchedule {
	if ws == nil {
		return nil
	}
	key := weekdayKey(at)
	for i := range ws.Days {
		if ws.Days[i].Day == key {
			return &ws.Days[i]
		}
	}
	return nil
}

func slotIndexAt(day *DaySchedule, at time.Time) int {
	min := at.Hour()*60 + at.Minute()
	for i, sl := range day.Slots {
		s, err1 := parseHM(sl.Start)
		e, err2 := parseHM(sl.End)
		if err1 != nil || err2 != nil {
			continue
		}
		if min >= s && min < e {
			return i
		}
	}
	return -1
}

// Line 渲染成注入用的一行。**必须把"这是事实"说出来** —— 否则模型仍会当成背景音。
func (f StateFact) Line() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n【你现在】%s", stateLabel(f.State))
	parts := []string{}
	if f.Since != "" && f.Since != "—" {
		parts = append(parts, "从 "+f.Since+" 起")
	}
	if f.Reason != "" {
		parts = append(parts, f.Reason)
	}
	if f.WakeAt != "" {
		parts = append(parts, "今天 "+f.WakeAt+" 起的床")
	}
	if len(parts) > 0 {
		b.WriteString(" (" + strings.Join(parts, "; ") + ")")
	}
	b.WriteString("。⚠️ 以上是**系统事实**, 只许润色成话, 不许说成别的状态 —— 「刚醒」「正在忙」这类与事实不符的自述一律算错。")
	return b.String()
}

// ——— 校验: 禁止自述与事实不符的状态 ———
//
// 词表刻意收得很窄 ("我在忙" 而不是 "忙"): 宽表会把"我昨天上课的时候"这种**过去叙述**
// 一起误杀。宁可漏放, 不可误伤 —— 与 A3 同一取向。
var claimBusy = []string{"我在忙", "我正在忙", "我这边正忙", "我还在忙", "忙着呢",
	"我在开会", "我在上课", "我在上班", "我在加班", "我这会儿没空"}
var claimIdle = []string{"我在摸鱼", "我在发呆", "我闲着", "我没事干", "我躺着呢", "我在放空"}
var claimJustWoke = []string{"刚醒", "刚睡醒", "刚起床", "才爬起来", "还没起"}

// StateViolations 返回状态自述与系统事实冲突的项 (空 = 通过)。
func StateViolations(fact StateFact, now time.Time, text string) []string {
	var out []string
	hit := func(list []string) string {
		for _, p := range list {
			if strings.Contains(text, p) {
				return p
			}
		}
		return ""
	}

	switch fact.State {
	case StateBusy, StateFocus:
		if p := hit(claimIdle); p != "" {
			out = append(out, "状态自述与事实冲突 (系统: "+fact.State+", 它说: "+p+")")
		}
	case StateSlack, StateFree:
		if p := hit(claimBusy); p != "" {
			out = append(out, "状态自述与事实冲突 (系统: "+fact.State+", 它说: "+p+")")
		}
	case StateSleep:
		if p := hit(claimIdle); p != "" {
			out = append(out, "睡着却自述清醒 ("+p+")")
		} else if p := hit(claimBusy); p != "" {
			out = append(out, "睡着却自述在忙 ("+p+")")
		}
	}

	// "刚醒" 只在**能算**的时候才算错: 有起床时间, 且已经起了 2 小时以上。
	if p := hit(claimJustWoke); p != "" && fact.WakeAt != "" && fact.State != StateSleep {
		if wm, err := parseHM(fact.WakeAt); err == nil {
			mins := now.Hour()*60 + now.Minute() - wm
			if mins > 120 {
				out = append(out, fmt.Sprintf("自称刚醒, 实际已起床 %d 小时 (%s)", mins/60, p))
			}
		}
	}
	return out
}
