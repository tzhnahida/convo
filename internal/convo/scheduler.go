package convo

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tzhnahida/VocabLock/internal/clock"
	"github.com/tzhnahida/VocabLock/internal/log"
)

// ——— 周日程数据模型 (§8.2: 7 天×时段, state∈{sleep,focus,busy,slack,free}) ———

const (
	StateSleep = "sleep"
	StateFocus = "focus"
	StateBusy  = "busy"
	StateSlack = "slack"
	StateFree  = "free"
)

var validStates = map[string]bool{
	StateSleep: true, StateFocus: true, StateBusy: true, StateSlack: true, StateFree: true,
}

var dayOrder = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

func weekdayKey(t time.Time) string {
	// time.Weekday: Sunday=0 → shift so mon=0 (dayOrder index)
	return dayOrder[(int(t.Weekday())+6)%7]
}

type WeekSchedule struct {
	Days       []DaySchedule `json:"days"`
	SleepStart string        `json:"sleep_start"` // "HH:MM"
	SleepEnd   string        `json:"sleep_end"`   // "HH:MM"
}

type DaySchedule struct {
	Day   string `json:"day"`
	Slots []Slot `json:"slots"`
}

type Slot struct {
	Start string `json:"start"` // "HH:MM"
	End   string `json:"end"`   // "HH:MM"
	State string `json:"state"`
	// Reason 这一格的**结构性标记**, 目前只有 SlotReasonAppointment 一个值。
	//
	// ⚠️ 它是**结构**不是文案: 随机窗与主动开场都要按它让路, 所以必须能判等。
	// 空 = 普通格。老数据没有这个字段 → 反序列化成空串, 行为与从前逐字节一致。
	Reason string `json:"reason,omitempty"`
}

// SleepWindowOf 某一天的睡眠窗, **从槽表解出来** —— 单位是"一天中的第几分钟"。
//
// ⚠️ **槽表是真相源**; `SleepStart`/`SleepEnd` 只是**展示字段**(ValidateWeekSchedule 会拿它
// 交叉核对, 但两者**可以不一致** —— 生产上真出现过「周三整日没有 sleep 槽」的日程,
// 而 `sleep_start` 照样写着 23:30)。
//
// 于是每一个读展示字段的地方都在**撒谎** —— 而"读数撒谎"比"没有读数"更糟:
// 你会照着它去修一个根本没坏的东西。
//
// 判定: 夜里那一段 = 结束于 24:00 的 sleep 槽; 早上那一段 = 从 00:00 开始的 sleep 槽。
// 缺任何一个 → ok=false(**那天它没有睡眠段**, 而不是"用默认值补上")。
func SleepWindowOf(ws *WeekSchedule, day string) (sleepMin, wakeMin int, ok bool) {
	d := dayPlanDay(ws, day)
	if d == nil {
		return 0, 0, false
	}
	var haveSleep, haveWake bool
	for _, sl := range d.Slots {
		if sl.State != StateSleep {
			continue
		}
		s, err1 := parseHM(sl.Start)
		e, err2 := parseHM(sl.End)
		if err1 != nil || err2 != nil {
			continue
		}
		if s == 0 { // 从 00:00 开始 → 早上那一段, 它的结束就是起床点
			wakeMin, haveWake = e, true
		}
		if sl.End == "24:00" { // 结束于 24:00 → 夜里那一段, 它的开始就是睡点
			sleepMin, haveSleep = s, true
		}
	}
	return sleepMin, wakeMin, haveSleep && haveWake
}

func parseHM(s string) (int, error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, fmt.Errorf("bad time %q", s)
	}
	hh, err1 := strconv.Atoi(parts[0])
	mm, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || hh < 0 || hh > 24 || mm < 0 || mm > 59 {
		return 0, fmt.Errorf("bad time %q", s)
	}
	if hh == 24 {
		if mm != 0 {
			return 0, fmt.Errorf("bad time %q", s)
		}
		return 24 * 60, nil // 日终 24:00 = 1440 (槽终点专用)
	}
	return hh*60 + mm, nil
}

// ValidateWeekSchedule enforces the §8.2 rules:
//  1. exactly 7 days (mon..sun), unique;
//  2. each day's slots: valid state, start<end, sorted, contiguous 00:00→24:00;
//  3. sleep slots must sit inside the sleep window (⊆ SleepStart-SleepEnd);
//  4. **每一天至少有 SleepPointSleepFloor 分钟的 sleep**(2026-10-15 补, 见下)。
func ValidateWeekSchedule(ws *WeekSchedule) error {
	if ws == nil {
		return fmt.Errorf("nil schedule")
	}
	if len(ws.Days) != 7 {
		return fmt.Errorf("days: want 7, got %d", len(ws.Days))
	}
	seen := map[string]bool{}
	for _, d := range ws.Days {
		if !contains(dayOrder, d.Day) {
			return fmt.Errorf("unknown day %q", d.Day)
		}
		if seen[d.Day] {
			return fmt.Errorf("duplicate day %q", d.Day)
		}
		seen[d.Day] = true
		if len(d.Slots) == 0 {
			return fmt.Errorf("day %q: no slots", d.Day)
		}
		var prevEnd int
		for i, sl := range d.Slots {
			if !validStates[sl.State] {
				return fmt.Errorf("day %q slot %d: bad state %q", d.Day, i, sl.State)
			}
			s, err := parseHM(sl.Start)
			if err != nil {
				return fmt.Errorf("day %q slot %d: %v", d.Day, i, err)
			}
			e, err := parseHM(sl.End)
			if err != nil {
				return fmt.Errorf("day %q slot %d: %v", d.Day, i, err)
			}
			if e <= s {
				return fmt.Errorf("day %q slot %d: end<=start", d.Day, i)
			}
			if i == 0 {
				if s != 0 {
					return fmt.Errorf("day %q: first slot does not start at 00:00 (starts %s)", d.Day, sl.Start)
				}
			} else if s != prevEnd {
				return fmt.Errorf("day %q slot %d: gap/overlap at %s (prev ends %d)", d.Day, i, sl.Start, prevEnd)
			}
			prevEnd = e
		}
		if prevEnd != 24*60 {
			return fmt.Errorf("day %q: slots end at %d, want 1440", d.Day, prevEnd)
		}
		// ⚠️ 2026-10-15 补: **「有 sleep 槽」必须是一条下界, 上面那三条管不住它。**
		//
		// 第 3 条只说了「sleep 槽必须落在声明的睡眠窗内」—— 那是个**单向**约束:
		// **一个 sleep 槽都不写的日子, 它压根没有槽可以违反**, 于是照样通过。
		// 生产库里就真的长这样: 周三整日 0 分钟 sleep(周二只睡 30 分钟就被叫醒, 周三通宵),
		// 而校验 / 重试 / 内置兜底一路绿灯 —— 因为它确实没违反任何一条已写的规则。
		//
		// 判据: §4.5.4-3「睡眠窗不得短于 6 小时」。与作息自修正共用同一个常量,
		// 免得两处各写一个 6 小时然后慢慢漂开。
		if mins := sleepMinutesOfDay(d); mins < SleepPointSleepFloor {
			return fmt.Errorf("day %q: 只有 %d 分钟 sleep, 至少要 %d 分钟 —— 「一整天不睡」也是合法的, 那正是这个校验原来的漏洞",
				d.Day, mins, SleepPointSleepFloor)
		}
	}
	// sleep window ⊆ 23:30-07:30 style boundary: sleep slots inside window
	sStart, sErr := parseHM(ws.SleepStart)
	sEnd, sErr2 := parseHM(ws.SleepEnd)
	if sErr != nil || sErr2 != nil {
		return fmt.Errorf("bad sleep window")
	}
	for _, d := range ws.Days {
		for i, sl := range d.Slots {
			if sl.State != StateSleep {
				continue
			}
			s, _ := parseHM(sl.Start)
			e, _ := parseHM(sl.End)
			if !insideSleepWindow(s, e, sStart, sEnd) {
				return fmt.Errorf("day %q slot %d: sleep slot %s-%s outside window %s-%s", d.Day, i, sl.Start, sl.End, ws.SleepStart, ws.SleepEnd)
			}
		}
	}
	return nil
}

// sleepMinutesOfDay 一天里 state=sleep 的总分钟数。
//
// 解析不了的槽按 0 计 —— 它们会在别处被拒, 这里不重复报错。
func sleepMinutesOfDay(d DaySchedule) int {
	total := 0
	for _, sl := range d.Slots {
		if sl.State != StateSleep {
			continue
		}
		s, err1 := parseHM(sl.Start)
		e, err2 := parseHM(sl.End)
		if err1 != nil || err2 != nil || e <= s {
			continue
		}
		total += e - s
	}
	return total
}

// insideSleepWindow reports whether [s,e) is entirely within the (possibly
// cross-midnight) sleep window [ws,we).
func insideSleepWindow(s, e, ws, we int) bool {
	if e <= s {
		return false
	}
	if ws < we { // same-day window (shouldn't happen for default 23:30-07:30 but be safe)
		return s >= ws && e <= we
	}
	// cross-midnight: window = [ws, 1440) ∪ [0, we)
	if s >= ws {
		return e <= 1440
	}
	return e <= we
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// CurrentState resolves the schedule state at the given instant.
// No schedule (or uninitialized) → "free": 未创建朋友前不裁决,一切按自由态。
func CurrentState(ws *WeekSchedule, at time.Time) string {
	if ws == nil {
		return StateFree
	}
	key := weekdayKey(at)
	min := at.Hour()*60 + at.Minute()
	for _, d := range ws.Days {
		if d.Day != key {
			continue
		}
		for _, sl := range d.Slots {
			s, err := parseHM(sl.Start)
			if err != nil {
				continue
			}
			e, err := parseHM(sl.End)
			if err != nil {
				continue
			}
			if min >= s && min < e {
				return sl.State
			}
		}
	}
	return StateFree
}

// ——— 运行时: 每分钟 tick (§8.3) ———

// Scheduler drives the five-state machine. It owns the current mood and
// reports state transitions upward (e.g. 签名联动 / 事件预告 / 主动开场) via
// OnStateChange. All time goes through clock.Now so tests/simulation can
// time-travel.
type Scheduler struct {
	Cfg           SchedulerConfig
	OnStateChange func(state string, at time.Time) // nil-safe; fired on transitions (incl. boot)
	onChange      func(state string, at time.Time)
	store         *Store
	current       string
	tickEvery     time.Duration
}

func NewScheduler(cfg SchedulerConfig, store *Store) *Scheduler {
	s := &Scheduler{Cfg: cfg, store: store, current: ""}
	tick := cfg.TickEverySec
	if tick <= 0 {
		tick = 60
	}
	s.tickEvery = time.Duration(tick) * time.Second
	return s
}

func (s *Scheduler) Start(ctx context.Context) {
	go func() {
		// 启动即 tick 一次: 任何状态都先落一次 (状态变迁回调由 OnStateChange 处理)
		s.tick()
		t := time.NewTicker(s.tickEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.tick()
			}
		}
	}()
}

func (s *Scheduler) tick() {
	now := clock.Now()
	ws, err := s.store.LoadSchedule()
	if err != nil {
		log.Error("convo:schedule", "load: %v", err)
		return
	}
	state := CurrentState(ws, now)
	if state == s.current {
		return
	}
	prev := s.current
	s.current = state
	// ⚠️ 这里曾经写 `SaveMood(state, "tick")` —— 把**日程状态**存进了一张叫 mood 的表。
	// 那不是心情, 也没有任何代码读它; 设计稿说的"心情等于随机数", 病灶就在这一行。
	// 心情现在由 `mood.go` + `convo_mood_state` 持有 (成因/半衰期/变化率上限), 与日程状态无关。
	log.Info("convo:schedule", "state %s -> %s", prev, state)
	if s.OnStateChange != nil {
		s.OnStateChange(state, now)
	}
}

// Current returns the in-memory current state ("" until first tick).
func (s *Scheduler) Current() string { return s.current }
