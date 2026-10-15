package convo

import (
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────
// ① §4.4.6 失眠成因的日级读数
//
// 阈值 4 分, 而冲突/加班/热力三项满分 2+2+2 ——
// 少了它们, **一个白天吵了两架、加班四小时、群里热到爆的日子, 依然判不出失眠**。
// ─────────────────────────────────────────────────────────────

func metricStore(t *testing.T) *Store {
	t.Helper()
	s := newTestStore(t)
	if err := EnsureDayMetricSchema(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestInsomniaScore_TheThreeMissingReadingsCanCarryItOverTheLine
//
// ⚠️ **这条是这一项的立案理由。**
// 这一天的成因**全在**那三个字段上(LateMsg/MoodLow/Dream 都是空的),
// 而在补读数之前它们恒为 0 —— 于是这样的一天判不出失眠, 日志里只看得到"成因分 0"。
func TestInsomniaScore_TheThreeMissingReadingsCanCarryItOverTheLine(t *testing.T) {
	day := InsomniaInput{
		Provided: true,
		Conflict: 2, // 白天吵了一架 (3 分)
		Overwork: 4, // 加班四小时 (2 分)
		Heat:     8, // 群太热闹了 (2 分)
	}
	insomnia, score, _ := InsomniaFrom(day)
	if !insomnia {
		t.Fatalf("吵两架 + 加班四小时 + 群热到爆, 判不出失眠 (成因分 %d, 阈值 %d)", score, InsomniaThreshold)
	}
	// 反面锚点: **单独**一项不该够 —— 阈值不是摆设。
	for name, one := range map[string]InsomniaInput{
		"只有冲突": {Provided: true, Conflict: 1},
		"只有加班": {Provided: true, Overwork: 1},
		"只有热力": {Provided: true, Heat: 8},
	} {
		if ins, sc, _ := InsomniaFrom(one); ins {
			t.Errorf("%s 就判失眠了 (成因分 %d) —— 那阈值形同虚设", name, sc)
		}
	}
}

// TestOverworkHours_CountsOnlyBeyondTheDay
//
// ⚠️ 8 小时是**基线**不是硬指标: 它是"一个正常工作日有多长"。
func TestOverworkHours_CountsOnlyBeyondTheDay(t *testing.T) {
	ws := &WeekSchedule{}
	for _, d := range dayOrder {
		ws.Days = append(ws.Days, DaySchedule{Day: d, Slots: []Slot{
			{Start: "00:00", End: "08:00", State: StateSleep},
			{Start: "08:00", End: "24:00", State: StateFocus}, // 16 小时全忙
		}})
	}
	// 到 20:00 → 忙碌 12 小时 → 加班 (720-480)/60 = **4 小时**。
	at := time.Date(2026, 10, 15, 20, 0, 0, 0, time.Local)
	if got := OverworkHours(ws, at); got != 4 {
		t.Fatalf("忙了 12 小时应当算 4 小时加班, 实得 %d", got)
	}
	// ⚠️ 到 23:59 → 忙碌 959 分钟 (差一分钟满 16 小时) → 加班 (959-480)/60 = **7**。
	// 这条钉的是**向下取整**: 7.98 小时算 7, 不算 8 —— 加班时长不是四舍五入的估计量。
	late := time.Date(2026, 10, 15, 23, 59, 0, 0, time.Local)
	if got := OverworkHours(ws, late); got != 7 {
		t.Fatalf("959 分钟忙碌应当算 7 小时加班(向下取整), 实得 %d", got)
	}
	// 到中午 12:00 → 忙碌 4 小时 → **不到基线, 加班 0**。
	noon := time.Date(2026, 10, 15, 12, 0, 0, 0, time.Local)
	if got := OverworkHours(ws, noon); got != 0 {
		t.Fatalf("只忙了 4 小时不该算加班, 实得 %d", got)
	}
	// 没有日程 → 0 (未提供 = 不加班)。
	if got := OverworkHours(nil, at); got != 0 {
		t.Fatalf("没有日程却算出了加班: %d", got)
	}
}

// TestDayMetric_PeakKeepsTheMaxAndAccumulateSums
func TestDayMetric_PeakKeepsTheMaxAndAccumulateSums(t *testing.T) {
	s := metricStore(t)
	day := "2026-10-15"
	// 峰值只升不降。
	for _, v := range []float64{3, 9, 4, 7} {
		if err := s.BumpDayMax(day, dayMetricHeatPeak, v); err != nil {
			t.Fatal(err)
		}
	}
	if got, ok := s.HeatPeakToday(mustDay(t, day)); !ok || got != 9 {
		t.Fatalf("峰值应当是 9, 实得 %v (ok=%v)", got, ok)
	}
	// 累加。
	for i := 0; i < 3; i++ {
		if err := s.AddDayMetric(day, dayMetricConflicts, 1); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.ConflictsToday(mustDay(t, day)); got != 3 {
		t.Fatalf("冲突次数应当是 3, 实得 %d", got)
	}
}

// TestDayMetric_NeverRecordedIsNotProvided
//
// ⚠️ **零值 = 未提供 ≠ 0**: 没记过热力峰值 → ok=false,
// 而调用方据此**不填**那一项 —— 填个 0 会让判据看起来在工作。
func TestDayMetric_NeverRecordedIsNotProvided(t *testing.T) {
	s := metricStore(t)
	if _, ok := s.HeatPeakToday(mustDay(t, "2026-10-15")); ok {
		t.Fatal("从没记过却报 ok=true")
	}
	// 而冲突次数**是**"今天吵了几次", 没记过就是 0 —— 两个量的默认不一样。
	if got := s.ConflictsToday(mustDay(t, "2026-10-15")); got != 0 {
		t.Fatalf("没记过的冲突次数应当是 0, 实得 %d", got)
	}
	// 分区: 别的日子读不到今天的读数。
	if err := s.BumpDayMax("2026-10-14", dayMetricHeatPeak, 9); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.HeatPeakToday(mustDay(t, "2026-10-15")); ok {
		t.Fatal("读到了别的日子的读数")
	}
}

// TestInsomniaReadings_FillsFromTheDayNotFromNow
//
// ⚠️ 夜里跑的时候已经是**第二天凌晨**了, 拿 time.Now() 去数会数到新的一天 ——
// 而它的症状是"昨天的失眠永远算不出来", 静默。
func TestInsomniaReadings_FillsFromTheDayNotFromNow(t *testing.T) {
	s := metricStore(t)
	day := "2026-10-15"
	if err := s.BumpDayMax(day, dayMetricHeatPeak, 8); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDayMetric(day, dayMetricConflicts, 2); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: s, Cfg: DefaultConfig()}
	in := InsomniaInput{Provided: true}
	e.InsomniaReadings(&in, day)
	if in.Conflict != 2 {
		t.Errorf("冲突次数没填上: %d", in.Conflict)
	}
	if in.Heat != 8 {
		t.Errorf("热力峰值没填上: %v", in.Heat)
	}
	// ⚠️ 而且这一天的成因**必须真的能把它推过阈值** —— 端到端那一步。
	if ins, score, _ := InsomniaFrom(in); !ins {
		t.Fatalf("填完之后仍然判不出失眠 (成因分 %d)", score)
	}
}

// TestNoteHeatPeak_KeepsTheDailyMax
func TestNoteHeatPeak_KeepsTheDailyMax(t *testing.T) {
	s := metricStore(t)
	e := &Engine{Store: s, Cfg: DefaultConfig()}
	now := time.Now()
	e.NoteHeatPeak(3, now)
	e.NoteHeatPeak(9, now)
	e.NoteHeatPeak(2, now)
	got, ok := s.HeatPeakToday(now)
	if !ok || got != 9 {
		t.Fatalf("峰值应当是 9, 实得 %v (ok=%v)", got, ok)
	}
	// 零/负值不许记 (它只会污染峰值)。
	e.NoteHeatPeak(0, now)
	e.NoteHeatPeak(-5, now)
	if got, _ := s.HeatPeakToday(now); got != 9 {
		t.Fatalf("零/负值把峰值改掉了: %v", got)
	}
}

func mustDay(t *testing.T, day string) time.Time {
	t.Helper()
	tm, ok := ParseDay(day)
	if !ok {
		t.Fatalf("解析不出 %q", day)
	}
	return tm
}
