package convo

import (
	"strings"
	"testing"
	"time"

	"github.com/tzhnahida/VocabLock/internal/clock"
)

// ─────────────────────────────────────────────────────────────
// #25 行程 undone + #26 自我失信
//
// 「真人报告自己一天时**最高频的句式是未完成**: 本来想看书, 结果刷了一晚上手机。」
// ─────────────────────────────────────────────────────────────

func undonePlan(items ...DayItem) DayPlan {
	return DayPlan{Day: clock.Today(), Items: items}
}

// TestValidateDayPlan_UndoneIsCappedAtOneThird
//
// ⚠️ §4.5.2 硬线: **一天 undone ≤ 1/3**。
// 全都没做 = 没有生活, 也是**废人叙事** —— 那不是"真实", 那是另一种假。
func TestValidateDayPlan_UndoneIsCappedAtOneThird(t *testing.T) {
	ws := dayPlanFixtureWeek()
	base := func(n, undone int) DayPlan {
		p := DayPlan{Day: "thu"}
		for i := 0; i < n; i++ {
			p.Items = append(p.Items, DayItem{
				At: "09:00", What: "x", From: DayFromSelf, Undone: i < undone,
			})
		}
		return p
	}
	// 3 条 1 个没做 → 过 (恰好 1/3)。
	if err := ValidateDayPlan(base(3, 1), ws); err != nil {
		t.Fatalf("3 条里 1 条没做应当合法: %v", err)
	}
	// 3 条 2 个没做 → 拒。
	if err := ValidateDayPlan(base(3, 2), ws); err == nil {
		t.Fatal("3 条里 2 条没做 —— 必须拒")
	}
	// 4 条 2 个没做 → 拒 (1/3 = 1.33, 向下取 1)。
	if err := ValidateDayPlan(base(4, 2), ws); err == nil {
		t.Fatal("4 条里 2 条没做 —— 必须拒")
	}
	// 反面锚点: 一个都没做**不会**被这条规则误伤(无病呻吟的约束比没有更糟)。
	if err := ValidateDayPlan(base(3, 0), ws); err != nil {
		t.Fatalf("一个都没标 undone 却被拒: %v", err)
	}
}

// TestDayPlanLine_SaysUndoneHonestly
//
// ⚠️ §4.5.2 最后一条硬线: **不许把 undone 说成 done**。
// 这一行是它报告一天时唯一的依据 —— 写含糊了它就只好编。
func TestDayPlanLine_SaysUndoneHonestly(t *testing.T) {
	now := time.Date(2026, 10, 15, 20, 0, 0, 0, time.Local)
	line := DayPlanLine(undonePlan(
		DayItem{At: "09:00", What: "去图书馆", From: DayFromSelf, Undone: true},
		DayItem{At: "15:00", What: "把书还了", From: DayFromSelf},
	), now)
	if !strings.Contains(line, "本来打算做, 结果没做成") {
		t.Errorf("undone 没有被诚实标出来:\n%s", line)
	}
	if strings.Count(line, "本来打算做") != 1 {
		t.Errorf("标了 %d 条, 应当恰好 1 条:\n%s", strings.Count(line, "本来打算做"), line)
	}
}

// ——— 校验链 ———

func undoneInput(today DayPlan) DialogInput {
	return DialogInput{UserText: "在吗", DayPlan: today}
}

func undoneFrame(at string) OutputFrame {
	return OutputFrame{
		Reply:      []FrameItem{{Kind: "text", Text: "本来想去图书馆的 结果躺了一天"}},
		PlanUndone: &FramePlanUndone{At: at},
	}
}

// freezeUndoneClock 把时钟钉在当天 15:00, 并在用例结束时还原。
//
// ⚠️ **凡是用"相对现在两小时前/后"造夹具的用例都必须调它。**
//
// 第一版用的是 time.Now() 造 past/future, 而校验器读的是 clock.Now() ——
// 两个时钟一错开, "还没到的那一条"就会落进"已经过去"那一侧,
// 于是**反面用例静默失效**: 测试仍然跑, 只是不再证明任何事。
// (2026-10-15 真实时间跨过一个整点后它就红了 —— 红得晚, 但红得对。)
func freezeUndoneClock(t *testing.T) time.Time {
	t.Helper()
	old := clock.Now
	frozen := time.Date(2026, 10, 15, 15, 0, 0, 0, time.Local)
	clock.Now = func() time.Time { return frozen }
	t.Cleanup(func() { clock.Now = old })
	return frozen
}

// TestValidateFrame_PlanUndoneOnlyForTodayAndThePast
//
// ⚠️ 三条: 只能指**今天的、已经过去的、还没标过**的那一条。
// 把还没到的安排标成"没做成"是**造假**。
func TestValidateFrame_PlanUndoneOnlyForTodayAndThePast(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜", Template: "bestie"}
	// ⚠️ **必须冻结时钟。**
	//
	// 第一版用的是 time.Now() 造 past/future, 而校验器读的是 clock.Now() ——
	// 两个时钟一错开, "还没到的那一条"就会落进"已经过去"那一侧,
	// 于是**反面用例静默失效**: 测试仍然跑, 只是不再证明任何事。
	// (2026-10-15 真实时间跨过一个整点后它就红了 —— 红得晚, 但红得对。)
	oldNow := clock.Now
	clock.Now = func() time.Time { return time.Date(2026, 10, 15, 15, 0, 0, 0, time.Local) }
	defer func() { clock.Now = oldNow }()

	now := clock.Now()
	pastAt := now.Add(-2 * time.Hour).Format("15:04")  // 13:00
	futureAt := now.Add(2 * time.Hour).Format("15:04") // 17:00

	today := undonePlan(
		DayItem{At: pastAt, What: "去图书馆", From: DayFromSelf},
		DayItem{At: futureAt, What: "晚上做饭", From: DayFromSelf},
		DayItem{At: "08:00", What: "晨跑", From: DayFromSelf},
	)
	in := undoneInput(today)
	// 正面: 已经过去的那一条。
	if v := d.ValidateFrame(undoneFrame(pastAt), in, p); len(v) != 0 {
		t.Fatalf("合法的一次「没做成」被拦: %v", v)
	}
	// 反面 1: 还没到的那一条。
	if v := d.ValidateFrame(undoneFrame(futureAt), in, p); len(v) == 0 {
		t.Error("把还没到的安排标成没做成 —— 那是造假, 必须拦")
	}
	// 反面 2: 今天根本没有的时刻。
	if v := d.ValidateFrame(undoneFrame("03:00"), in, p); len(v) == 0 {
		t.Error("指了一个今天不存在的时刻 —— 必须拦")
	}
	_ = now
}

// TestValidateFrame_PlanUndoneRespectsTheOneThirdCap
func TestValidateFrame_PlanUndoneRespectsTheOneThirdCap(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜", Template: "bestie"}
	past := freezeUndoneClock(t).Add(-2 * time.Hour).Format("15:04")
	// 三条里已经有一条没做了 → 再加一条就是 2/3。
	today := undonePlan(
		DayItem{At: past, What: "a", From: DayFromSelf, Undone: true},
		DayItem{At: "08:00", What: "b", From: DayFromSelf},
		DayItem{At: "09:00", What: "c", From: DayFromSelf},
	)
	if v := d.ValidateFrame(undoneFrame(past), undoneInput(today), p); len(v) == 0 {
		t.Error("已经 1/3 了还想再加 —— 必须拦")
	}
}

// ——— 写路径 + 出口 ———

func undoneEngine(t *testing.T) (*Engine, *Store) {
	t.Helper()
	s := agTestStore(t)
	if err := s.SaveSchedule(dayPlanFixtureWeek()); err != nil {
		t.Fatal(err)
	}
	return &Engine{Store: s, Cfg: DefaultConfig()}, s
}

func pastDayPlan(t *testing.T, what string) (DayPlan, string) {
	t.Helper()
	at := freezeUndoneClock(t).Add(-2 * time.Hour).Format("15:04")
	p := DayPlan{Day: clock.Today(), Items: []DayItem{
		{At: at, What: what, From: DayFromSelf},
		{At: "08:00", What: "别的事", From: DayFromSelf},
		{At: "09:00", What: "还有一件事", From: DayFromSelf},
	}}
	return p, at
}

// TestMarkPlanUndone_LandsAndCountsTheStreak
func TestMarkPlanUndone_LandsAndCountsTheStreak(t *testing.T) {
	e, s := undoneEngine(t)
	p, at := pastDayPlan(t, "去图书馆")
	if err := s.SaveDayPlan(e.ownerKey(), p, dayPlanFixtureWeek()); err != nil {
		t.Fatal(err)
	}
	ok, why := e.MarkPlanUndone(p, at, "躺着没动", clock.Now())
	if !ok {
		t.Fatalf("记不上: %s", why)
	}
	got, has, err := s.LoadDayPlan(e.ownerKey(), clock.Today())
	if err != nil || !has {
		t.Fatalf("读不回来: %v", err)
	}
	if got.UndoneCount() != 1 {
		t.Fatalf("undone 没落库: %+v", got.Items)
	}
	if n := s.UndoneStreak(); n != 1 {
		t.Fatalf("连续次数应当 1, 实得 %d", n)
	}
	// 反面: 同一条报两次不许重复计数。
	if ok, _ := e.MarkPlanUndone(got, at, "again", clock.Now()); ok {
		t.Error("同一条报了两次")
	}
	if n := s.UndoneStreak(); n != 1 {
		t.Errorf("重复报也把计数推上去了: %d", n)
	}
}

// TestMarkPlanUndone_ForcedRedemptionIsAnExit
//
// ⚠️ §5.5 硬线四: **必须有出口**。
// 「没有出口, 它会变成一个永远在自责的人 —— 那是抑郁叙事, 不是人格。」
//
// ⚠️ 而且出口落在**次日行程里一条 forced 条目**, 不是提示词里喊一句 ——
// 只喊的话它就是"下次一定", 而"下次一定"正是这个机制要治的东西。
func TestMarkPlanUndone_ForcedRedemptionIsAnExit(t *testing.T) {
	e, s := undoneEngine(t)
	var lastAt string
	for i := 0; i < SelfBreachForcedAt; i++ {
		p, at := pastDayPlan(t, "去图书馆")
		// 每次重置当天那一位, 才能连着记三次 (真实场景里这是三天)。
		if err := s.SaveDayPlan(e.ownerKey(), p, dayPlanFixtureWeek()); err != nil {
			t.Fatal(err)
		}
		lastAt = at
		if ok, why := e.MarkPlanUndone(p, at, "又没去", clock.Now()); !ok {
			t.Fatalf("第 %d 次记不上: %s", i+1, why)
		}
	}
	_ = lastAt
	// 出口: 明天的行程里必须出现一条 forced。
	tomorrow, has, err := s.LoadDayPlan(e.ownerKey(), TomorrowKey(clock.Now()))
	if err != nil || !has {
		t.Fatalf("强制兑现没写进明天的行程 (has=%v err=%v)", has, err)
	}
	forced := 0
	for _, it := range tomorrow.Items {
		if it.Forced {
			forced++
		}
	}
	if forced == 0 {
		t.Fatalf("连续 %d 次自我失信却没强制兑现: %+v", SelfBreachForcedAt, tomorrow.Items)
	}
	// 出口触发之后账要清零 —— 否则它每次都触发, 变成"天天强制兑现"。
	if n := s.UndoneStreak(); n != 0 {
		t.Fatalf("出口触发后计数没清零: %d", n)
	}
}

// ——— 注入 ———

// TestUndoneLine_And_ForcedLine
//
// ⚠️ 自我失信的代价落在**自我评价下调**, 不是任何形式的对外索取 ——
// 所以允许自嘲, 但四条硬线必须在这两句里。
func TestUndoneLine_And_ForcedLine(t *testing.T) {
	today := undonePlan(DayItem{At: "09:00", What: "去图书馆", From: DayFromSelf, Undone: true})
	line := UndoneLine(today)
	if line == "" {
		t.Fatal("今天有没做成的事却不注入 —— 那它就只会编")
	}
	if !strings.Contains(line, "只说一次") {
		t.Errorf("没有挡住「反复提」(说三次就是卖惨):\n%s", line)
	}
	if !strings.Contains(line, "当借口") {
		t.Errorf("没有挡住「拿它当借口」:\n%s", line)
	}
	// 没做成的事 → 一个字都不加。
	if got := UndoneLine(undonePlan(DayItem{At: "09:00", What: "x", From: DayFromSelf})); got != "" {
		t.Fatalf("没有 undone 却注入了: %q", got)
	}
	// 出口那一句。
	tomorrow := DayPlan{Day: "2026-10-16", Items: []DayItem{
		{At: "09:00", What: "去图书馆", From: DayFromSelf, Forced: true},
	}}
	f := ForcedLine(tomorrow)
	if f == "" {
		t.Fatal("有 forced 条目却不注入出口那一句")
	}
	if !strings.Contains(f, "欠自己") {
		t.Errorf("出口那一句没说清这是「对自己的承诺」:\n%s", f)
	}
	if got := ForcedLine(DayPlan{Items: []DayItem{{At: "09:00", What: "x"}}}); got != "" {
		t.Fatalf("没有 forced 却注入了: %q", got)
	}
}

// TestBuildSystem_CarriesUndoneAndForcedOnlyWhenPresent
func TestBuildSystem_CarriesUndoneAndForcedOnlyWhenPresent(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜", Template: "bestie"}
	plain := d.buildSystem(DialogInput{UserText: "在吗"}, p, "ja")
	if strings.Contains(plain, "你没做成") || strings.Contains(plain, "你欠自己的") {
		t.Fatal("什么都没有却注入了 undone/forced 的提示")
	}
	with := d.buildSystem(DialogInput{
		UserText: "在吗",
		DayPlan:  undonePlan(DayItem{At: "09:00", What: "去图书馆", From: DayFromSelf, Undone: true}),
		DayPlanTomorrow: DayPlan{Items: []DayItem{
			{At: "09:00", What: "去图书馆", From: DayFromSelf, Forced: true},
		}},
	}, p, "ja")
	if !strings.Contains(with, "你没做成") {
		t.Error("今天有 undone 却没注入")
	}
	if !strings.Contains(with, "你欠自己的") {
		t.Error("明天有 forced 却没注入")
	}
}
