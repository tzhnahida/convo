package convo

import (
	"strings"
	"testing"
	"time"

	"github.com/tzhnahida/VocabLock/internal/clock"
)

func validSchedule() *WeekSchedule {
	return &WeekSchedule{
		SleepStart: "23:30",
		SleepEnd:   "07:30",
		Days: []DaySchedule{
			{Day: "mon", Slots: []Slot{{Start: "00:00", End: "07:30", State: "sleep"}, {Start: "07:30", End: "09:00", State: "free"}, {Start: "09:00", End: "12:00", State: "focus"}, {Start: "12:00", End: "13:00", State: "free"}, {Start: "13:00", End: "17:30", State: "focus"}, {Start: "17:30", End: "19:00", State: "busy"}, {Start: "19:00", End: "23:30", State: "free"}, {Start: "23:30", End: "24:00", State: "sleep"}}},
			{Day: "tue", Slots: []Slot{{Start: "00:00", End: "07:30", State: "sleep"}, {Start: "07:30", End: "12:00", State: "free"}, {Start: "12:00", End: "18:00", State: "focus"}, {Start: "18:00", End: "23:30", State: "free"}, {Start: "23:30", End: "24:00", State: "sleep"}}},
			{Day: "wed", Slots: []Slot{{Start: "00:00", End: "07:30", State: "sleep"}, {Start: "07:30", End: "12:00", State: "free"}, {Start: "12:00", End: "18:00", State: "focus"}, {Start: "18:00", End: "23:30", State: "free"}, {Start: "23:30", End: "24:00", State: "sleep"}}},
			{Day: "thu", Slots: []Slot{{Start: "00:00", End: "07:30", State: "sleep"}, {Start: "07:30", End: "12:00", State: "free"}, {Start: "12:00", End: "18:00", State: "focus"}, {Start: "18:00", End: "23:30", State: "free"}, {Start: "23:30", End: "24:00", State: "sleep"}}},
			{Day: "fri", Slots: []Slot{{Start: "00:00", End: "07:30", State: "sleep"}, {Start: "07:30", End: "12:00", State: "free"}, {Start: "12:00", End: "18:00", State: "focus"}, {Start: "18:00", End: "23:30", State: "free"}, {Start: "23:30", End: "24:00", State: "sleep"}}},
			{Day: "sat", Slots: []Slot{{Start: "00:00", End: "07:30", State: "sleep"}, {Start: "07:30", End: "12:00", State: "slack"}, {Start: "12:00", End: "23:30", State: "free"}, {Start: "23:30", End: "24:00", State: "sleep"}}},
			{Day: "sun", Slots: []Slot{{Start: "00:00", End: "07:30", State: "sleep"}, {Start: "07:30", End: "12:00", State: "slack"}, {Start: "12:00", End: "23:30", State: "free"}, {Start: "23:30", End: "24:00", State: "sleep"}}},
		},
	}
}

func TestValidateWeekSchedule_ok(t *testing.T) {
	if err := ValidateWeekSchedule(validSchedule()); err != nil {
		t.Fatalf("valid schedule rejected: %v", err)
	}
}

func TestValidateWeekSchedule_errors(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*WeekSchedule)
		want   string
	}{
		{"missing day", func(ws *WeekSchedule) { ws.Days = ws.Days[:6] }, "want 7"},
		{"duplicate day", func(ws *WeekSchedule) { ws.Days[1].Day = "mon" }, "duplicate"},
		{"unknown day", func(ws *WeekSchedule) { ws.Days[1].Day = "mon2" }, "unknown day"},
		{"bad state", func(ws *WeekSchedule) { ws.Days[1].Slots[1].State = "party" }, "bad state"},
		{"gap", func(ws *WeekSchedule) { ws.Days[1].Slots[1].Start = "08:00" }, "gap/overlap"},
		{"not covering 24h", func(ws *WeekSchedule) {
			ws.Days[1].Slots[len(ws.Days[1].Slots)-1] = Slot{Start: "23:30", End: "23:59", State: "free"}
		}, "1440"},
		{"end<=start", func(ws *WeekSchedule) { ws.Days[1].Slots[1] = Slot{Start: "12:00", End: "12:00", State: "free"} }, "end<=start"},
		{"sleep outside window", func(ws *WeekSchedule) {
			ws.Days[1].Slots[0].End = "08:00"
			ws.Days[1].Slots[1].Start = "08:00"
		}, "outside window"},
		{"first slot not 00:00", func(ws *WeekSchedule) { ws.Days[1].Slots[0].Start = "00:30" }, "00:00"},
		{"no slots", func(ws *WeekSchedule) { ws.Days[1].Slots = nil }, "no slots"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ws := validSchedule()
			c.mutate(ws)
			err := ValidateWeekSchedule(ws)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), c.want)
			}
		})
	}
}

func TestCurrentState(t *testing.T) {
	ws := validSchedule()
	// 2026-09-07 is a Monday
	cases := []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 9, 7, 1, 0, 0, 0, time.Local), "sleep"},
		{time.Date(2026, 9, 7, 8, 0, 0, 0, time.Local), "free"},
		{time.Date(2026, 9, 7, 10, 0, 0, 0, time.Local), "focus"},
		{time.Date(2026, 9, 7, 18, 0, 0, 0, time.Local), "busy"},
		{time.Date(2026, 9, 7, 20, 0, 0, 0, time.Local), "free"},
		{time.Date(2026, 9, 7, 23, 30, 0, 0, time.Local), "sleep"}, // 睡眠窗边界: 23:30 整点入眠
		{time.Date(2026, 9, 7, 23, 29, 0, 0, time.Local), "free"},
		{time.Date(2026, 9, 6, 10, 0, 0, 0, time.Local), "slack"},  // 周日摸鱼
		{time.Date(2026, 9, 7, 23, 30, 0, 0, time.Local), "sleep"}, // 跨午夜窗口, 周一 01:00 已测
	}
	for _, c := range cases {
		if got := CurrentState(ws, c.at); got != c.want {
			t.Fatalf("CurrentState(%v) = %q, want %q", c.at, got, c.want)
		}
	}
}

func TestCurrentState_noSchedule(t *testing.T) {
	at := time.Date(2026, 9, 7, 3, 0, 0, 0, time.Local)
	if got := CurrentState(nil, at); got != StateFree {
		t.Fatalf("nil schedule = %q, want free", got)
	}
}

func TestWeekdayKey(t *testing.T) {
	cases := []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 9, 7, 0, 0, 0, 0, time.Local), "mon"}, // Monday
		{time.Date(2026, 9, 13, 0, 0, 0, 0, time.Local), "sun"},
		{time.Date(2026, 9, 6, 0, 0, 0, 0, time.Local), "sun"}, // Sunday (Weekday()=0)
	}
	for _, c := range cases {
		if got := weekdayKey(c.at); got != c.want {
			t.Fatalf("weekdayKey(%v) = %q, want %q", c.at, got, c.want)
		}
	}
}

// TestScheduler_tick_transition: 时钟注入, 验证状态变迁落库+回调
func TestScheduler_tick_transition(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir, "test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveSchedule(validSchedule()); err != nil {
		t.Fatal(err)
	}

	var transitions []string
	s := NewScheduler(DefaultConfig().Sched, store)
	s.OnStateChange = func(state string, at time.Time) {
		transitions = append(transitions, state)
	}

	clock.Now = func() time.Time { return time.Date(2026, 9, 7, 8, 0, 0, 0, time.Local) } // Mon 08:00 → free
	defer func() { clock.Now = func() time.Time { return time.Now() } }()
	s.tick()
	if s.Current() != StateFree {
		t.Fatalf("state = %q, want free", s.Current())
	}
	clock.Now = func() time.Time { return time.Date(2026, 9, 7, 10, 0, 0, 0, time.Local) } // Mon 10:00 → focus
	s.tick()
	if s.Current() != StateFocus {
		t.Fatalf("state = %q, want focus", s.Current())
	}
	s.tick() // 同状态再 tick — 不重复回调
	if len(transitions) != 2 {
		t.Fatalf("transitions = %v, want [free focus]", transitions)
	}
	// 落库验证
	if err := store.SaveMood("focus", "tick"); err != nil {
		t.Fatal(err)
	}
}
