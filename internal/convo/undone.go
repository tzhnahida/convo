package convo

import (
	"fmt"
	"time"

	"github.com/tzhnahida/VocabLock/internal/clock"
	"github.com/tzhnahida/VocabLock/internal/log"
)

// ─────────────────────────────────────────────────────────────
// §4.5.2 undone + §5.5 **对自己的承诺 (自我失信)** (2026-10-15)
//
// 「毁约是对别人的; 而真人日常里占绝大多数的是**对自己的失信**。」
//   · 「我明天一定早睡」→ 当晚熬到两点
//   · 「这周该去把书还了」→ 拖了三周
//
// ⚠️ **它不是「毁约」的一个子类**, 两者必须分开:
//   - 受害方: 对别人的毁约是对方(影响信用/关系); 对自己的失信**只有它自己**;
//   - 代价:   前者是信用与亲密度下降; 后者**无外部代价 —— 但不许零代价**;
//   - 落点:   前者是 Appointment 的 breached; 后者是**日程层 2 的 undone**。
//
// 代价落在**短期自我评价下调**(一次自我吐槽), 而不是任何形式的对外索取。
// ─────────────────────────────────────────────────────────────

const (
	// SelfBreachForcedAt 连续几次自我失信之后 → **强制兑现一次**。
	//
	// ⚠️ 这是**出口**。没有它, 它会变成一个永远在自责的人 —— 那是抑郁叙事, 不是人格
	// (与 §4.4.6 失眠必须有 ForcedReset 同构)。
	SelfBreachForcedAt = 3
	// undoneStreakKey 连续自我失信的计数器 (**跨天**, 所以 day 用固定键)。
	undoneStreakKey = "undone_streak"
	undoneStreakDay = "streak"
)

// FramePlanUndone §4.5.2: 报**今天**行程里的哪一条没做成。
//
// ⚠️ 它只能指**今天已经过去的那一条** —— 把还没到的安排标成"没做成"是造假。
type FramePlanUndone struct {
	At  string `json:"at"`
	Why string `json:"why,omitempty"`
}

// planItemAt 今天行程里有没有这一条 (而且**已经过去了**)。返回下标。
func planItemAt(plan DayPlan, at string, now time.Time) int {
	m, err := parseHM(at)
	if err != nil {
		return -1
	}
	cur := now.Hour()*60 + now.Minute()
	if m > cur {
		return -1 // 还没到, 谈不上"没做成"
	}
	for i, it := range plan.Items {
		if it.At == at {
			return i
		}
	}
	return -1
}

// UndoneStreak 当前连续自我失信的次数。
func (s *Store) UndoneStreak() int {
	return counterOrZero(s, undoneStreakKey, undoneStreakDay)
}

// MarkPlanUndone 把它报的那一条落成 undone 事实, 并推进自我失信的账。
func (e *Engine) MarkPlanUndone(today DayPlan, at, why string, now time.Time) (bool, string) {
	if e.Store == nil {
		return false, "没有 store"
	}
	idx := planItemAt(today, at, now)
	if idx < 0 {
		return false, "今天没有这一条(或它还没到)"
	}
	plan := today
	plan.Items = append([]DayItem(nil), today.Items...)
	if plan.Items[idx].Undone {
		return false, "这一条已经记过没做成了"
	}
	plan.Items[idx].Undone = true
	plan.Items[idx].Done = false
	ws, _ := e.Store.LoadSchedule()
	if err := e.Store.SaveDayPlan(e.ownerKey(), plan, ws); err != nil {
		return false, "落库失败: " + err.Error()
	}
	// 连续自我失信的账 —— ⚠️ 失败要出声: 它意味着那道出口永远不会触发。
	n := e.Store.UndoneStreak() + 1
	if err := e.Store.SetCounter(undoneStreakKey, undoneStreakDay, n); err != nil {
		log.Warn("convo:undone", "自我失信计数失败(出口将不会触发): %v", err)
	}
	log.Info("convo:undone", "§4.5.2 没做成: %s %s (连续 %d 次)", at, plan.Items[idx].What, n)
	e.maybeForceRedemption(plan.Items[idx], n)
	return true, "记下了"
}

// maybeForceRedemption §5.5 的出口: 连着几次没做成 → **强制兑现一次**。
//
// ⚠️ 落点是**次日行程里一条 forced 条目** —— 写进日程, 而不是只在提示词里喊一句。
// 只喊的话, 它就是"下次一定", 而"下次一定"正是这个机制要治的东西。
func (e *Engine) maybeForceRedemption(item DayItem, streak int) {
	if streak < SelfBreachForcedAt {
		return
	}
	day := TomorrowKey(clock.Now())
	tomorrow, _, _ := e.Store.LoadDayPlan(e.ownerKey(), day)
	ws, _ := e.Store.LoadSchedule()
	plan := tomorrow
	plan.Day = day
	if plan.Items == nil {
		plan.Items = []DayItem{}
	}
	// ⚠️ 已经排过同一件事 → 标成 forced 就够了, 不重复排(重复排会挤成两条)。
	dup := false
	for i := range plan.Items {
		if plan.Items[i].What == item.What {
			plan.Items[i].Forced = true
			plan.Items[i].Undone = false
			dup = true
			break
		}
	}
	if !dup {
		plan.Items = append(plan.Items, DayItem{
			At: item.At, What: item.What, From: item.From, Forced: true,
		})
	}
	plan = NormalizeDayPlan(plan, ws)
	if err := ValidateDayPlan(plan, ws); err != nil {
		log.Warn("convo:undone", "强制兑现排不进明天的行程: %v", err)
		return
	}
	if err := e.Store.SaveDayPlan(e.ownerKey(), plan, ws); err != nil {
		log.Warn("convo:undone", "强制兑现落库失败: %v", err)
		return
	}
	// 出口触发 → 账清零, 重新开始数。
	if err := e.Store.SetCounter(undoneStreakKey, undoneStreakDay, 0); err != nil {
		log.Warn("convo:undone", "出口触发后计数没清零: %v", err)
	}
	// 欠自己的那一件从"今天"挪走了, 所以今天的 undone 也要撤掉, 否则它会两边都在。
	log.Warn("convo:undone", "§5.5 连续 %d 次自我失信 — 强制兑现一次: 明天 %s %s", streak, item.At, item.What)
}

// ——— 注入的两句 ———

// UndoneLine 今天有没做成的事 → 允许**一次**自我吐槽。
//
// ⚠️ 代价落在**自我评价下调**, 不是任何形式的对外索取。硬线全在这一句里:
// 不许归因用户 · 不许当借口 · 不许反复提 (出口在 maybeForceRedemption)。
func UndoneLine(today DayPlan) string {
	if today.UndoneCount() == 0 {
		return ""
	}
	return "\n【今天的事】里有一条**你没做成**。允许你自嘲一句(「我怎么又这样」), 但**只说一次**。\n" +
		"  ⚠️ 不许拿它当借口, 不许说成是 ta 造成的, 也不许反复提(说三次就是卖惨)。\n"
}

// ForcedLine 明天有**必须做成**的事 → 出口那一句。
func ForcedLine(tomorrow DayPlan) string {
	n := 0
	for _, it := range tomorrow.Items {
		if it.Forced {
			n++
		}
	}
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("\n【明天的事】里有 %d 条是**你欠自己的** —— 上次没做成, 这一次要做成。\n"+
		"  ⚠️ 不用跟谁汇报, 也不用拿它换安慰; 做成就是你自己的事。\n", n)
}
