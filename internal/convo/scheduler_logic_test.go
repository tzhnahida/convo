package convo

import (
	"testing"
	"time"
)

func testCfg() SchedulerConfig {
	return SchedulerConfig{
		TickEverySec: 60, ActiveQuotaDay: 2, SleepStart: "23:30", SleepEnd: "07:30",
		DegradeDaysNoReply: 3, BusyDelayMin: 30, BusyDelayMax: 90, SlackStealPct: 10,
	}
}

// tue 14:00 为 focus 槽 (validSchedule: tue 12:00-18:00 focus)
func tueAt(h, m int) time.Time { return time.Date(2026, 9, 8, h, m, 0, 0, time.Local) }

func base(now time.Time, state string) TickInput {
	return TickInput{Now: now, Sched: validSchedule(), State: state, HadSlot: true}
}

func TestDecideTick_previewWindow(t *testing.T) {
	// tue focus 槽 12:00 开始 → 11:56 处于预告窗
	in := base(tueAt(11, 56), StateFree)
	if got := DecideTick(in, testCfg()).Kind; got != ActPreview {
		t.Fatalf("11:56 → %q, want preview", got)
	}
	// 11:50 (6 分钟前) 不在窗内
	if got := DecideTick(base(tueAt(11, 50), StateFree), testCfg()).Kind; got != ActActiveOpen {
		t.Fatalf("11:50 → %q, want active_open", got)
	}
	// 预告只允许一次/日
	in.PreviewToday = 1
	if got := DecideTick(in, testCfg()).Kind; got == ActPreview {
		t.Fatal("preview twice prevented")
	}
}

func TestDecideTick_sleep(t *testing.T) {
	// mon 01:00 sleep → 无主动动作
	in := base(time.Date(2026, 9, 7, 1, 0, 0, 0, time.Local), StateSleep)
	if got := DecideTick(in, testCfg()).Kind; got != ActNone {
		t.Fatalf("sleep → %q, want none", got)
	}
}

func TestDecideTick_focusBreak(t *testing.T) {
	in := base(tueAt(14, 0), StateFocus)
	in.ConsecutiveUserMsgs = 3
	if got := DecideTick(in, testCfg()).Kind; got != ActBreakFocus {
		t.Fatalf("3 consecutive → %q, want break_focus", got)
	}
	in = base(tueAt(14, 0), StateFocus)
	in.UrgentWordHit = true
	if got := DecideTick(in, testCfg()).Kind; got != ActBreakFocus {
		t.Fatalf("urgent → %q, want break_focus", got)
	}
	// 普通 focus 无输入 → none
	in = base(tueAt(14, 0), StateFocus)
	if got := DecideTick(in, testCfg()).Kind; got != ActNone {
		t.Fatalf("focus quiet → %q, want none", got)
	}
}

func TestDecideTick_delayedReply(t *testing.T) {
	in := base(tueAt(14, 0), StateBusy)
	in.PendingReply = 1
	if got := DecideTick(in, testCfg()).Kind; got != ActDelayedReply {
		t.Fatalf("pending → %q, want delayed_reply", got)
	}
}

func TestDecideTick_activeQuota(t *testing.T) {
	in := base(time.Date(2026, 9, 7, 19, 0, 0, 0, time.Local), StateFree) // mon 19:00 free
	if got := DecideTick(in, testCfg()).Kind; got != ActActiveOpen {
		t.Fatalf("free 0/2 → %q, want active_open", got)
	}
	in.ActiveToday = 2
	if got := DecideTick(in, testCfg()).Kind; got != ActNone {
		t.Fatalf("free 2/2 → %q, want none", got)
	}
	// focus/busy/sleep 无配额开口
	in = base(tueAt(14, 0), StateFocus)
	if got := DecideTick(in, testCfg()).Kind; got != ActNone {
		t.Fatalf("focus → %q, want none", got)
	}
}

func TestDecideTick_noInput(t *testing.T) {
	in := base(time.Date(2026, 9, 7, 19, 0, 0, 0, time.Local), StateFree)
	in.SilentDays = 1
	if got := DecideTick(in, testCfg()).Kind; got != ActNoInput {
		t.Fatalf("24h quiet → %q, want noinput", got)
	}
	in.NoInputToday = 1
	// 当天已发 noInput → 回到配额开口路径
	if got := DecideTick(in, testCfg()).Kind; got != ActActiveOpen {
		t.Fatalf("noinput used → %q, want active_open", got)
	}
}

func TestDecideTick_returnGreet(t *testing.T) {
	// 3-5 天静默: 回归仪式优先于 noInput/配额
	in := base(time.Date(2026, 9, 7, 19, 0, 0, 0, time.Local), StateFree)
	in.SilentDays = 4
	if got := DecideTick(in, testCfg()).Kind; got != ActReturnGreet {
		t.Fatalf("4d → %q, want return_greet", got)
	}
	in.SilentDays = 7
	if got := DecideTick(in, testCfg()).Kind; got != ActReturnGreet {
		t.Fatalf("7d → %q, want return_greet", got)
	}
	in.SilentDays = 12
	if got := DecideTick(in, testCfg()).Kind; got != ActNone {
		t.Fatalf("12d → %q, want none (pause)", got)
	}
}

func TestDecideTick_slackSteal(t *testing.T) {
	in := base(time.Date(2026, 9, 6, 10, 0, 0, 0, time.Local), StateSlack) // sun 10:00 slack
	in.FunnyMarked = true
	in.StealRoll = 5 // < 10 → 偷
	if got := DecideTick(in, testCfg()).Kind; got != ActStealReply {
		t.Fatalf("roll 5 → %q, want steal_reply", got)
	}
	in.StealRoll = 50 // ≥10 → 不偷, 走配额开口(如还有)
	if got := DecideTick(in, testCfg()).Kind; got != ActActiveOpen {
		t.Fatalf("roll 50 → %q, want active_open", got)
	}
}

func TestDecideTick_noSchedule(t *testing.T) {
	in := TickInput{Now: tueAt(14, 0), Sched: nil, HadSlot: false, State: StateFree}
	if got := DecideTick(in, testCfg()).Kind; got != ActNone {
		t.Fatalf("no schedule → %q, want none", got)
	}
}
