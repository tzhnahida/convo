package convo

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tzhnahida/VocabLock/internal/clock"
	"github.com/tzhnahida/VocabLock/internal/log"
)

// ─────────────────────────────────────────────────────────────
// §4.5 **程序改写层 1 周日程** (2026-10-15)
//
// 在这之前 week_json 只被**人工接口**写过 —— §4.5.3 列的四条自动写入路径一条都没落点:
// 日程习惯档没实现, 失眠修正算出来了没人读, 约定插入根本不存在。
//
// 这个文件把后两条接上, 并且给每一次改动**记一条理由**(revision_history 那个死列)。
//
// ⚠️ 两条纪律:
//   1. **改完必须过 ValidateWeekSchedule, 不过就回滚** —— 一份坏日程会污染一整天, 而它进提示词;
//   2. **每次改动都要有理由** —— 没有理由的日程变化等于"它今天莫名其妙不一样了", 归因不了。
// ─────────────────────────────────────────────────────────────

// ScheduleRevision 一条微调历史。
//
// ⚠️ 这是给 **owner** 看的("它为什么改成这样"), 不是给它的 —— 所以可以带分钟数。
type ScheduleRevision struct {
	TS string `json:"ts"`
	// Source insomnia | appointment | reschedule | reset
	Source string `json:"source"`
	// DeltaMin 这一次动了多少分钟 (0 = 不是时间上的改动, 比如插一条约定)。
	DeltaMin int    `json:"delta_min,omitempty"`
	Reason   string `json:"reason"`
	// Detail 人话补充 (改成了几点 / 插了哪件事)。
	Detail string `json:"detail,omitempty"`
}

// SlotReasonAppointment 层 1 上"这一格有约"的标记。
//
// ⚠️ 它是**结构**不是文案: 随机窗与主动开场都要按它让路, 所以必须能判等。
const SlotReasonAppointment = "appointment"

// ─── 槽表的小工具 ───

// fmtHM 分钟 → "HH:MM"; 1440 → "24:00" (与 parseHM 互逆)。
func fmtHM(min int) string {
	if min >= 24*60 {
		return "24:00"
	}
	if min < 0 {
		min = 0
	}
	return fmt.Sprintf("%02d:%02d", min/60, min%60)
}

// mergeAdjacent 合并相邻的、同 state 同 reason 的槽。
//
// ⚠️ 没有它, 插十次约定之后一天会从 12 个槽碎成 40 个 —— 槽表本身没问题,
// 但**提示词里的"从何时起"会变成一堆碎片**, 而那正是 §8.1 三元组要治的东西。
func mergeAdjacent(slots []Slot) []Slot {
	var out []Slot
	for _, sl := range slots {
		if sl.Start == sl.End {
			continue
		}
		if n := len(out); n > 0 && out[n-1].End == sl.Start &&
			out[n-1].State == sl.State && out[n-1].Reason == sl.Reason {
			out[n-1].End = sl.End
			continue
		}
		out = append(out, sl)
	}
	return out
}

// carveDaySlots 把 [start,end) 这一段**改写成** state/reason。
//
// ⚠️ 与既有槽相交时要切成三片(前 / 覆盖 / 后) —— 直接覆盖整槽会把"午休"这种
// 本来就有意义的槽一起吃掉, 而那属于**伪造事实**。
func carveDaySlots(d *DaySchedule, start, end int, state, reason string) bool {
	if end <= start {
		return false
	}
	var out []Slot
	changed := false
	for _, sl := range d.Slots {
		s, err1 := parseHM(sl.Start)
		e, err2 := parseHM(sl.End)
		if err1 != nil || err2 != nil || e <= s {
			out = append(out, sl)
			continue
		}
		if end <= s || e <= start { // 不相交
			out = append(out, sl)
			continue
		}
		cs, ce := s, e
		if cs < start {
			cs = start
		}
		if ce > end {
			ce = end
		}
		if s < cs {
			out = append(out, Slot{Start: sl.Start, End: fmtHM(cs), State: sl.State, Reason: sl.Reason})
		}
		out = append(out, Slot{Start: fmtHM(cs), End: fmtHM(ce), State: state, Reason: reason})
		if ce < e {
			out = append(out, Slot{Start: fmtHM(ce), End: sl.End, State: sl.State, Reason: sl.Reason})
		}
		if sl.State != state || strings.TrimSpace(sl.Reason) != strings.TrimSpace(reason) {
			changed = true
		}
	}
	if !changed {
		return false
	}
	d.Slots = mergeAdjacent(out)
	return true
}

// ─── 约定插入 (§4.5.3 第三条写入路径) ───

// InsertAppointment 把一条约定**写进层 1**: 该时段变成 busy + reason=appointment。
//
// ⚠️ 这就是设计稿说的「约定插入 → **该时段从随机窗豁免**」的落点:
//
//	① busy 让随机事件不再占用它;
//	② reason=appointment 让主动开场也绕开它(有约的时候它不该在群里闲聊)。
//
// 落在 sleep 里 → **不改**, 因为那是它在睡觉。
func InsertAppointment(ws *WeekSchedule, weekday, at string, durMin int) (bool, string) {
	if ws == nil {
		return false, "没有周日程"
	}
	d := dayPlanDay(ws, weekday)
	if d == nil {
		return false, "日程里没有这一天: " + weekday
	}
	s, err := parseHM(at)
	if err != nil {
		return false, "时刻解析不出: " + at
	}
	if durMin <= 0 {
		durMin = 30
	}
	e := s + durMin
	if e > 24*60 {
		e = 24 * 60
	}
	if DayItemInSleep(ws, weekday, at, durMin) {
		return false, "那个时段它在睡觉"
	}
	if !carveDaySlots(d, s, e, StateBusy, SlotReasonAppointment) {
		return false, "那个时段已经是约了"
	}
	return true, fmt.Sprintf("%s %s-%s 有约", weekday, fmtHM(s), fmtHM(e))
}

// ─── 作息偏移落地 (§4.4.6 的"一周后日程表悄悄变了") ───

// ApplySleepShift 把睡点按 deltaMin 分钟搬到**日程表**上 (正 = 推迟, 负 = 提前)。
//
// ⚠️ 语义与 SleepPointState.OffsetMin 对齐: **睡点动, 起床点不动** ——
// 所以只搬"夜里那一段"的起点, 早上那一段一个字不动。
//
// ⚠️ 搬完**必须过校验, 不过就回滚**: 一份不连续的槽表会让 CurrentState 判错,
// 而症状是"它今天的时间感全乱了"。
func ApplySleepShift(ws *WeekSchedule, deltaMin int) (bool, string) {
	if ws == nil || deltaMin == 0 {
		return false, ""
	}
	base, err := parseHM(ws.SleepStart)
	if err != nil {
		return false, "睡点解析不出: " + ws.SleepStart
	}
	target := base + deltaMin
	if target < 0 || target >= 24*60 {
		return false, "睡点会跑到一天之外"
	}
	// ⚠️ **在副本上试, 成了才提交。**
	//
	// 直接用 ws 改的话, 任何一步失败都会留下"改了一半"的周表 ——
	// 而调用方手里的那份 ws 是它**准备接着用**的对象, 它不会知道要重新读。
	// (第一版就是这么错的: 搬完槽没同步 SleepStart 就去校验, 被自己的校验判死,
	//  而槽已经改了 —— 测试当场抓住。)
	cand := cloneWeek(ws)
	cand.SleepStart = fmtHM(target)
	if err := shiftLateSleepBoundary(cand, target); err != nil {
		return false, err.Error()
	}
	if err := ValidateWeekSchedule(cand); err != nil {
		return false, "搬完之后槽表不合法: " + err.Error()
	}
	*ws = *cand
	return true, fmt.Sprintf("睡点 %s → %s", fmtHM(base), ws.SleepStart)
}

// cloneWeek 深拷贝一份周表 (Days 是切片, 浅拷贝会改到原件)。
func cloneWeek(ws *WeekSchedule) *WeekSchedule {
	if ws == nil {
		return nil
	}
	out := &WeekSchedule{SleepStart: ws.SleepStart, SleepEnd: ws.SleepEnd}
	out.Days = make([]DaySchedule, len(ws.Days))
	for i, d := range ws.Days {
		out.Days[i] = DaySchedule{Day: d.Day, Slots: append([]Slot(nil), d.Slots...)}
	}
	return out
}

// shiftLateSleepBoundary 把每天"夜里那一段 sleep"的起点搬到 newStart。
//
// 早上那一段(00:00-起床)**不动** —— 这与 OffsetMin 的语义一致(睡点动、起床点不动)。
func shiftLateSleepBoundary(ws *WeekSchedule, newStart int) error {
	// ⚠️ 先整体试一遍, 任何一天对不上就整份不改 —— 要么全成、要么不动,
	// 不许出现"改了一半"的周表(那是最难查的一种)。
	type edit struct{ day, prev int }
	var edits []edit
	for i := range ws.Days {
		d := &ws.Days[i]
		found := -1
		for j := range d.Slots {
			sl := d.Slots[j]
			if sl.State != StateSleep || sl.End != "24:00" {
				continue
			}
			found = j
			break
		}
		if found < 0 {
			return fmt.Errorf("day %s: 找不到夜里那一段 sleep(结束于 24:00)", d.Day)
		}
		if found == 0 {
			return fmt.Errorf("day %s: 夜里那一段 sleep 前面没有槽可以接", d.Day)
		}
		prev := d.Slots[found-1]
		ps, perr := parseHM(prev.Start)
		if perr != nil {
			return fmt.Errorf("day %s: 前一个槽的起点解析不出", d.Day)
		}
		if newStart <= ps {
			return fmt.Errorf("day %s: 睡点 %s 会吃掉前一个槽(从 %s 开始)", d.Day, fmtHM(newStart), prev.Start)
		}
		edits = append(edits, edit{day: i, prev: found - 1})
	}
	for _, ed := range edits {
		d := &ws.Days[ed.day]
		d.Slots[ed.prev].End = fmtHM(newStart)
		d.Slots[ed.prev+1].Start = fmtHM(newStart)
	}
	return nil
}

// sleepWindowMinutes 从"睡点 / 起床点"算窗口长度 (分钟)。
//
// ⚠️ **睡点在数值上大于起床点**(23:30 = 1410 vs 07:30 = 450),
// 所以是 `1440 - sleep + wake`, **不是** `sleep - wake`。
//
// 写成后者会得到 960 分钟(16 小时) —— 而它**不会报错**:
// 那个数只是喂给护栏 / 比例式的参数, 偏大只会让"不许睡太少"永远不触发、
// 或让"醒着的比例"永远算小一半。
//
// ⚠️ **这个公式在同一个包里错过两次**(`baseSleepWindowMin` 与 `wakingElapsed`),
// 所以它现在只有这一份实现 —— 两处各写一遍的下场已经见过了。
func sleepWindowMinutes(sleep, wake int) int {
	if sleep <= wake {
		return wake - sleep // 同一天内 (少见): 睡点早于起床点
	}
	return 24*60 - sleep + wake
}

// SleepWindowFallbackMin 读不到日程时的兜底窗口 (8 小时)。
const SleepWindowFallbackMin = 480

// SleepWindowFloorMin 低于它就认为"解出来的窗口不可信", 退回兜底。
//
// ⚠️ 这个下限不是随手取的: `SleepPointSleepFloor` 本身就要求 ≥360 分钟,
// 一条比它还短的解只可能是槽表坏了一半 —— 那正是该退回兜底而不是照用的情形。
const SleepWindowFloorMin = 240

// baseSleepWindowMin **基准**睡眠窗有多少分钟 —— 从槽表解, 不是写死的 480。
//
// ⚠️ 为什么要把偏移**加回去**: 日程表上那一份是**含偏移**的当前窗口
// (§4.4.6 的偏移会真的搬到日程上, 见 applySleepShiftToSchedule),
// 而 SleepPointSleepFloor 那四条护栏比的是**基线** ——
// `baseSleepMin - OffsetMin + deltaMin < 360`。
//
// ⚠️ 读不到 → 退回 480 (与从前一致): 失败往"照旧"倒, 不往"窗口为零"倒。
func (e *Engine) baseSleepWindowMin(offsetMin int) int {
	if e.Store == nil {
		return SleepWindowFallbackMin
	}
	ws, err := e.Store.LoadSchedule()
	if err != nil || ws == nil {
		return SleepWindowFallbackMin
	}
	sleep, wake, ok := SleepWindowOf(ws, weekdayKey(clock.Now()))
	if !ok {
		return SleepWindowFallbackMin
	}
	cur := sleepWindowMinutes(sleep, wake)
	base := cur + offsetMin // 把已经搬上去的偏移加回去 = 基线
	if base < SleepWindowFloorMin {
		return SleepWindowFallbackMin
	}
	return base
}

// ─── 引擎侧的两条写入路径 ───

// noteAppointmentInSchedule 把一条刚落库的约定**写进层 1**。
//
// ⚠️ 失败**不阻断约定本身** —— 约定是事实(它真的答应了), 日程只是"那一格被占住"。
// 反过来的话, 一次日程写失败会把它答应的事一起吞掉。
func (e *Engine) noteAppointmentInSchedule(what string, due time.Time) {
	if e.Store == nil || due.IsZero() {
		return
	}
	ws, err := e.Store.LoadSchedule()
	if err != nil || ws == nil {
		log.Warn("convo:schedule", "读不到日程, 约定没写进层 1: %v", err)
		return
	}
	// ⚠️ 时长不知道就是不知道: 传 0 → InsertAppointment 按 30 分钟估,
	// 与 DayItemInSleep 同一个约定。**不许**为了好看编一个"大概一小时"。
	changed, why := InsertAppointment(ws, weekdayKey(due), due.Format("15:04"), 0)
	if !changed {
		log.Info("convo:schedule", "约定没插进日程(%s): %s", why, what)
		return
	}
	if serr := e.Store.SaveScheduleWithRevision(ws, ScheduleRevision{
		Source: "appointment",
		Reason: "答应了 ta 一件事: " + what,
		Detail: why,
	}); serr != nil {
		log.Warn("convo:schedule", "日程改写落库失败: %v", serr)
		return
	}
	log.Info("convo:schedule", "§4.5.3 约定插进日程: %s", why)
}

// applySleepShiftToSchedule 把作息偏移**真的搬到日程表上** (§4.4.6 → §4.5.3)。
//
// ⚠️ 这就是「一周后日程表**悄悄**变了」的落点。只记在作息表里的话,
// 层 1 永远是老样子 —— 而层 1 才是"它今天几点睡"对用户可见的那一面。
func (e *Engine) applySleepShiftToSchedule(deltaMin int, reason string) {
	if e.Store == nil || deltaMin == 0 {
		return
	}
	ws, err := e.Store.LoadSchedule()
	if err != nil || ws == nil {
		log.Warn("convo:schedule", "读不到日程, 作息偏移没落上: %v", err)
		return
	}
	changed, why := ApplySleepShift(ws, deltaMin)
	if !changed {
		// ⚠️ 出声: 静默的话, "偏移没落上"和"根本没这条路径"长得一模一样。
		log.Warn("convo:schedule", "作息偏移 %d 分钟没能落到日程上: %s", deltaMin, why)
		return
	}
	if serr := e.Store.SaveScheduleWithRevision(ws, ScheduleRevision{
		Source: "insomnia", DeltaMin: deltaMin, Reason: reason, Detail: why,
	}); serr != nil {
		log.Warn("convo:schedule", "日程改写落库失败: %v", serr)
		return
	}
	log.Info("convo:schedule", "§4.5.3 日程按作息偏移改写: %s (%s)", why, reason)
}

// ─── 存储: 改 + 记理由 ───

// SaveScheduleWithRevision 改日程并记下理由。
//
// ⚠️ **先落日程再落理由**: 反过来的话, 日程保存失败会留下一条"改过了"的假历史。
// ⚠️ revision_history 列一直是死的(有定义、无写入、无读取), 于是「每次微调必须有理由」
// 这条护栏**前半截生效、后半截被丢掉** —— 代码强制要求理由非空, 然后把理由扔了。
func (s *Store) SaveScheduleWithRevision(ws *WeekSchedule, rev ScheduleRevision) error {
	if err := ValidateWeekSchedule(ws); err != nil {
		return err
	}
	if err := s.SaveSchedule(ws); err != nil {
		return err
	}
	return s.AppendScheduleRevision(rev)
}

// AppendScheduleRevision 往 revision_history 追一条 (**只增**, 最多留最近 50 条)。
func (s *Store) AppendScheduleRevision(rev ScheduleRevision) error {
	if strings.TrimSpace(rev.Reason) == "" {
		return fmt.Errorf("日程微调必须有理由 (§4.5.4-2)")
	}
	if rev.TS == "" {
		rev.TS = clock.TodayTS()
	}
	list, err := s.ScheduleRevisions(50)
	if err != nil {
		return err
	}
	list = append([]ScheduleRevision{rev}, list...)
	if len(list) > 50 {
		list = list[:50]
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`UPDATE convo_schedule SET revision_history=? WHERE id=1`, string(raw))
	return err
}

// ScheduleRevisions 读最近 limit 条微调历史 (最新在前)。
//
// ⚠️ 坏了/空 → 返回空表 + 不报错: 这是**观测面**, 不该因为它读不出来就让日程改动失败。
func (s *Store) ScheduleRevisions(limit int) ([]ScheduleRevision, error) {
	var raw string
	err := s.DB.QueryRow(`SELECT COALESCE(revision_history,'[]') FROM convo_schedule WHERE id=1`).Scan(&raw)
	if err != nil || strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var list []ScheduleRevision
	if jerr := json.Unmarshal([]byte(raw), &list); jerr != nil {
		log.Warn("convo:schedule", "微调历史解析失败(当空的处理): %v", jerr)
		return nil, nil
	}
	if limit > 0 && len(list) > limit {
		list = list[:limit]
	}
	return list, nil
}
