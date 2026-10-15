package convo

import (
	"strings"
	"testing"
	"time"
)

func mkWeek(slots []Slot) *WeekSchedule {
	return &WeekSchedule{
		SleepStart: "01:00",
		SleepEnd:   "08:10",
		Days:       []DaySchedule{{Day: "mon", Slots: slots}},
	}
}

// 2026-05-11 是周一。
func monday(hh, mm int) time.Time {
	return time.Date(2026, 5, 11, hh, mm, 0, 0, time.Local)
}

// 🔴 这条是本文件最重要的一个测试: Since 与 Reason **必须来自同一段**。
// 只往回合并、Reason 却写着原槽的结束时间, 就会产出"从 13:00 起…到 15:30"这种
// 自相矛盾 —— 那正是 A2 要消灭的东西, 只不过换成了我们自己造的。
func TestStateFact_MergesAdjacentSameState(t *testing.T) {
	ws := mkWeek([]Slot{
		{Start: "13:00", End: "15:30", State: StateFree},
		{Start: "15:30", End: "18:00", State: StateFree},
		{Start: "18:00", End: "22:00", State: StateSlack},
	})
	f := BuildStateFact(ws, monday(16, 0))
	if f.State != StateFree {
		t.Fatalf("state = %q", f.State)
	}
	if f.Since != "13:00" {
		t.Errorf("Since 应合并到 13:00, 实得 %q", f.Since)
	}
	if f.Reason != "日程表安排, 到 18:00" {
		t.Errorf("Reason 必须与 Since 同属一段 (到 18:00), 实得 %q", f.Reason)
	}
}

func TestStateFact_SleepWindow(t *testing.T) {
	ws := mkWeek([]Slot{{Start: "13:00", End: "18:00", State: StateFree}})
	f := BuildStateFact(ws, monday(3, 0)) // 01:00-08:10 睡眠窗内
	if f.State != StateSleep {
		t.Fatalf("state = %q, want sleep", f.State)
	}
	if f.WakeAt != "" {
		t.Errorf("睡着时不该给起床时间 (会算出负的已醒时长): %q", f.WakeAt)
	}
}

func TestStateFact_NoSlot(t *testing.T) {
	ws := mkWeek([]Slot{{Start: "13:00", End: "15:00", State: StateBusy}})
	f := BuildStateFact(ws, monday(16, 0))
	if f.State != StateFree {
		t.Fatalf("空档应为 free, 实得 %q", f.State)
	}
}

// ——— 校验: 冲突要抓, 事实与过去叙述不许误伤 ———
func TestStateViolations(t *testing.T) {
	busy := StateFact{State: StateBusy, Since: "13:00", Reason: "日程表安排, 到 18:00", WakeAt: "08:10"}
	idle := StateFact{State: StateSlack, Since: "13:00", Reason: "日程表安排, 到 22:00", WakeAt: "08:10"}

	// 正面: 系统说忙, 它说在摸鱼 → 冲突
	if got := StateViolations(busy, monday(14, 0), "我在摸鱼"); len(got) == 0 {
		t.Error("系统是 busy 却说在摸鱼, 应当冲突")
	}
	// 正面: 系统说闲, 它说在忙 → 冲突
	if got := StateViolations(idle, monday(14, 0), "我在开会"); len(got) == 0 {
		t.Error("系统是 slack 却说在开会, 应当冲突")
	}
	// 🔴 反面: **说的就是事实, 不算错** —— 误伤会让它连真话都不敢说
	if got := StateViolations(idle, monday(14, 0), "我在摸鱼"); len(got) != 0 {
		t.Errorf("自述与事实一致不该判错: %v", got)
	}
	// 🔴 反面: **过去叙述**不许误伤 (词表刻意收窄就是为这个)
	if got := StateViolations(idle, monday(14, 0), "我昨天上课的时候也在走神"); len(got) != 0 {
		t.Errorf("过去叙述被误伤: %v", got)
	}
	// 正面: 起床 13 小时后说"刚醒" → 冲突 (这是病灶原话)
	if got := StateViolations(idle, monday(21, 0), "刚醒 还有点懵"); len(got) == 0 {
		t.Error("已起床 13 小时还说刚醒, 应当冲突")
	}
	// 🔴 反面: 起床 40 分钟说"刚醒"完全正常
	if got := StateViolations(idle, monday(8, 50), "刚醒 还有点懵"); len(got) != 0 {
		t.Errorf("确实刚起床, 不该判错: %v", got)
	}
	// 反面: 没有起床时间就不许猜
	noWake := StateFact{State: StateSlack, Since: "13:00"}
	if got := StateViolations(noWake, monday(21, 0), "刚醒"); len(got) != 0 {
		t.Errorf("没有起床时间时不该判错: %v", got)
	}
}

// 注入行必须**明说这是事实** —— 否则模型仍会当成背景音。
func TestStateFactLine_StatesItIsFact(t *testing.T) {
	f := StateFact{State: StateSlack, Since: "13:00", Reason: "日程表安排, 到 22:00", WakeAt: "08:10"}
	line := f.Line()
	for _, want := range []string{"【你现在】", "13:00", "系统事实", "08:10"} {
		if !strings.Contains(line, want) {
			t.Errorf("注入行缺少 %q: %s", want, line)
		}
	}
}
