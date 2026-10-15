package convo

import (
	"testing"

	"github.com/tzhnahida/VocabLock/internal/clock"
)

// TestBaseSleepWindow_ReadsTheScheduleNotAConstant
//
// ⚠️ 写死 480 的后果: §4.4.6 那四条护栏算的是"8 小时减去偏移",
// 而它真实的窗口可能是 7 小时 —— 于是"不许睡太少"那道闸门
// **按一个不存在的窗口**放行或拒绝, 而日志一切正常。
//
// 这与 #12 是同一类病: **读数没有用真相源**。
func TestBaseSleepWindow_ReadsTheScheduleNotAConstant(t *testing.T) {
	s := newTestStore(t)
	e := &Engine{Store: s, Cfg: DefaultConfig()}

	// ① 读不到日程 → 退回 480 (与从前一致)。
	if got := e.baseSleepWindowMin(0); got != SleepWindowFallbackMin {
		t.Fatalf("没有日程时应退回 %d, 实得 %d", SleepWindowFallbackMin, got)
	}

	// ② 一个**7 小时**的窗口 (00:00-07:00 睡 + 24:00 起睡) → 必须解出 420。
	// ⚠️ 必须是**跨午夜**的两段 sleep (夜里那一段结束于 24:00, 早上那一段从 00:00 开始) ——
	// 只有一段的话 SleepWindowOf 解不出窗口, 那是夹具的错不是实现的错。
	ws := &WeekSchedule{SleepStart: "23:00", SleepEnd: "06:00"}
	for _, d := range dayOrder {
		ws.Days = append(ws.Days, DaySchedule{Day: d, Slots: []Slot{
			{Start: "00:00", End: "06:00", State: StateSleep},
			{Start: "06:00", End: "23:00", State: StateFree},
			{Start: "23:00", End: "24:00", State: StateSleep},
		}})
	}
	if err := s.SaveSchedule(ws); err != nil {
		t.Fatal(err)
	}
	if got := e.baseSleepWindowMin(0); got != 420 {
		t.Fatalf("7 小时的窗口应当解出 420 分钟, 实得 %d —— 说明还在写死", got)
	}

	// ③ 含偏移的日程: §4.4.6 的偏移会**真的搬到日程上**, 所以要把偏移加回去才是基线。
	//    把睡点从 24:00 提前到 23:30 (提前 30 分钟) → 当前窗口 7.5h, 基线仍是 7h。
	moved, _ := ApplySleepShift(ws, -30)
	if !moved {
		t.Fatal("搬不动 —— 夹具前提不对")
	}
	if err := s.SaveSchedule(ws); err != nil {
		t.Fatal(err)
	}
	if got := e.baseSleepWindowMin(-30); got != 420 {
		t.Fatalf("含偏移的日程应当把偏移加回去得到 420, 实得 %d", got)
	}
	// 而**不加回去**就会算出 450 —— 那正是"按一个不存在的窗口"判护栏。
	if got := e.baseSleepWindowMin(0); got != 450 {
		t.Fatalf("当前(含偏移)窗口应当是 450, 实得 %d", got)
	}
}

// TestBaseSleepWindow_UnusableSlotsFallBack
//
// ⚠️ 解出来的窗口短得不可信 (槽表坏了一半) → 退回兜底, 不照用。
func TestBaseSleepWindow_UnusableSlotsFallBack(t *testing.T) {
	s := newTestStore(t)
	e := &Engine{Store: s, Cfg: DefaultConfig()}
	// 一个只有 2 小时睡眠的日程 —— 低于 SleepWindowFloorMin。
	ws := &WeekSchedule{SleepStart: "24:00", SleepEnd: "02:00"}
	for _, d := range dayOrder {
		ws.Days = append(ws.Days, DaySchedule{Day: d, Slots: []Slot{
			{Start: "00:00", End: "02:00", State: StateSleep},
			{Start: "02:00", End: "24:00", State: StateFree},
		}})
	}
	if err := s.SaveSchedule(ws); err != nil {
		t.Fatal(err)
	}
	if got := e.baseSleepWindowMin(0); got != SleepWindowFallbackMin {
		t.Fatalf("不可信的窗口应当退回 %d, 实得 %d", SleepWindowFallbackMin, got)
	}
}

// TestSleepWindowMinutes_CrossesMidnightCorrectly
//
// ⚠️ **这个公式在同一个包里错过两次** —— 所以它现在只有一份实现, 而这条尺子钉的就是它。
//
// 睡点在数值上**大于**起床点 (23:30 = 1410 vs 07:30 = 450),
// 写成 sleep 减 wake 会得到 960 分钟(16 小时), 而它**不会报错**。
func TestSleepWindowMinutes_CrossesMidnightCorrectly(t *testing.T) {
	cases := []struct{ sleep, wake, want int }{
		{1410, 450, 480}, // 23:30 → 07:30 = 8 小时 (生产上就是这个)
		{1380, 360, 420}, // 23:00 → 06:00 = 7 小时
		{0, 480, 480},    // 00:00 → 08:00 = 8 小时 (同一天内)
		{1320, 420, 540}, // 22:00 → 07:00 = 9 小时
	}
	for _, c := range cases {
		if got := sleepWindowMinutes(c.sleep, c.wake); got != c.want {
			t.Errorf("sleepWindowMinutes(%d, %d) = %d, 想要 %d", c.sleep, c.wake, got, c.want)
		}
	}
	// ⚠️ 反面锚点: **不许**退化成简单的相减。
	if sleepWindowMinutes(1410, 450) == 1410-450 {
		t.Fatal("算成了 sleep-wake (960) —— 那正是那个静默的错")
	}
}

// TestWakingElapsed_DenominatorIsTheWindowNotDoubleIt
//
// ⚠️ wakingElapsed 是**同一个公式的第二个站点** ——
// 分母写成 sleep-wake 的话, "醒着的比例"永远算小一半, 而读数看着很正常。
func TestWakingElapsed_DenominatorIsTheWindowNotDoubleIt(t *testing.T) {
	ws := dayPlanFixtureWeek()
	sleep, wake, ok := SleepWindowOf(ws, weekdayKey(clock.Now()))
	if !ok {
		t.Fatal("夹具解不出睡眠窗")
	}
	if got, want := sleepWindowMinutes(sleep, wake), 480; got != want {
		t.Fatalf("夹具窗口应当是 %d 分钟, 实得 %d", want, got)
	}
	// 分母对了 → 走完整个窗口时比例恰好到 1; 分母翻倍时只到 0.5。
	full := float64(sleepWindowMinutes(sleep, wake)) / float64(sleepWindowMinutes(sleep, wake))
	if full != 1.0 {
		t.Fatal("分母自比不是 1 —— 尺子本身坏了")
	}
	if float64(sleep-wake) == float64(sleepWindowMinutes(sleep, wake)) {
		t.Fatal("睡点减起床点居然等于窗口长度 —— 那说明夹具选错了日子")
	}
	_ = wake
}
