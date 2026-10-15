package convo

import (
	"strings"
	"testing"

	"github.com/tzhnahida/VocabLock/internal/clock"
)

// ─── 校验链 ───

func selfPlanInput(tomorrow DayPlan) DialogInput {
	return DialogInput{
		UserText:        "在吗",
		DayPlanTomorrow: tomorrow,
		Week:            dayPlanFixtureWeek(),
	}
}

func selfPlanFrame(sp *FrameSelfPlan) OutputFrame {
	return OutputFrame{
		Reply:    []FrameItem{{Kind: "text", Text: "那我明天下午去趟书店"}},
		SelfPlan: sp,
	}
}

// TestValidateFrame_SelfPlanNeedsSomethingSayable
//
// ⚠️ §5.5 硬线一: **自主预约的条目必须能说出口。**
// 说不出口的安排 = 借口, 而借口正是这条通道最容易被滥用的方向。
func TestValidateFrame_SelfPlanNeedsSomethingSayable(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜", Template: "bestie"}
	in := selfPlanInput(DayPlan{})

	if v := d.ValidateFrame(selfPlanFrame(&FrameSelfPlan{At: "15:00", What: "去趟书店"}), in, p); len(v) != 0 {
		t.Fatalf("合法自主预约被拦: %v", v)
	}
	for _, bad := range []*FrameSelfPlan{
		{At: "15:00"},              // 没内容
		{At: "15:00", What: "   "}, // 空白内容
		{At: "下午", What: "去趟书店"},   // 时刻解析不出
	} {
		if v := d.ValidateFrame(selfPlanFrame(bad), in, p); len(v) == 0 {
			t.Errorf("%+v 必须被拦", bad)
		}
	}
}

// TestValidateFrame_SelfPlanNeverBeatsAUserPromise
//
// ⚠️ §5.5 硬线三: **不许取消用户约定去成全自主预约**(优先级: 用户约定 > 自主预约)。
// 这里是**事前**拦: 撞了就让重说, 而不是等它真排上了再去回滚。
func TestValidateFrame_SelfPlanNeverBeatsAUserPromise(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜", Template: "bestie"}
	tomorrow := DayPlan{Day: TomorrowKey(clock.Now()), Items: []DayItem{
		{At: "15:00", What: "和 ta 一起吃饭", With: "ta", From: DayFromUser},
	}}
	in := selfPlanInput(tomorrow)

	if v := d.ValidateFrame(selfPlanFrame(&FrameSelfPlan{At: "15:00", What: "去趟书店"}), in, p); len(v) == 0 {
		t.Error("撞上了用户约定 —— 必须拦")
	}
	// 相差 20 分钟也算撞(一个人不能在一个小时里出现在两个地方)。
	if v := d.ValidateFrame(selfPlanFrame(&FrameSelfPlan{At: "15:20", What: "去趟书店"}), in, p); len(v) == 0 {
		t.Error("离用户约定只有 20 分钟, 应当算撞")
	}
	// 但隔开足够远就**必须放行** —— 误判的代价是整轮重说。
	if v := d.ValidateFrame(selfPlanFrame(&FrameSelfPlan{At: "19:00", What: "去趟书店"}), in, p); len(v) != 0 {
		t.Errorf("19:00 离 15:00 足够远, 不该拦: %v", v)
	}
	// ⚠️ 只有 **user** 来源才算约定 —— 它自己排的、夜里生成的不该拦它自己。
	tomorrow2 := DayPlan{Day: TomorrowKey(clock.Now()), Items: []DayItem{
		{At: "15:00", What: "夜里排的", From: DayFromSelf},
		{At: "15:00", What: "配角的事", From: DayFromCast},
	}}
	if v := d.ValidateFrame(selfPlanFrame(&FrameSelfPlan{At: "15:00", What: "去趟书店"}), selfPlanInput(tomorrow2), p); len(v) != 0 {
		t.Errorf("只有用户约定才优先, self/cast 不该拦: %v", v)
	}
}

// TestValidateFrame_SelfPlanRefusesSleepHours
func TestValidateFrame_SelfPlanRefusesSleepHours(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜", Template: "bestie"}
	in := selfPlanInput(DayPlan{})
	if v := d.ValidateFrame(selfPlanFrame(&FrameSelfPlan{At: "03:00", What: "去趟书店"}), in, p); len(v) == 0 {
		t.Error("凌晨三点去书店 —— 必须拦")
	}
}

// TestValidateFrame_SelfPlanCapIsNotInTheValidationChain
//
// ⚠️ **这条是 §4.6.1 的守门测试。**
// 上限是**生活上限**, 必须内在化: 校验链的违规文本会被塞进重试提示词,
// 判在那里就等于亲口告诉它"你一天只能排两次"。
func TestValidateFrame_SelfPlanCapIsNotInTheValidationChain(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜", Template: "bestie"}
	in := selfPlanInput(DayPlan{})
	// ⚠️ 这里原来有一行 `in.SelfPlansToday = SelfPlanDailyCap + 5`(早就超了)。
	//
	// 而 `DialogInput` 现在**根本没有那一格** —— 上限在写路径判, 校验链连
	// "今天排过几次"都拿不到。这比原来那条断言**更强**: 不是"看见了不判",
	// 而是**根本没有东西可判** —— 拿不到就报不出来, 也就不会漏进重试提示词。
	v := d.ValidateFrame(selfPlanFrame(&FrameSelfPlan{At: "15:00", What: "去趟书店"}), in, p)
	if len(v) != 0 {
		t.Fatalf("上限漏进了校验链 —— 那会被写进重试提示词, 等于告诉它配额: %v", v)
	}
	// 而上限**必须**真的存在, 且在写路径上判。见下面的 TestAddSelfPlan_CapsPerDay。
}

// ─── 写路径 ───

func selfPlanEngine(t *testing.T) (*Engine, *Store) {
	t.Helper()
	s := agTestStore(t)
	e := &Engine{Store: s, Cfg: DefaultConfig()}
	return e, s
}

// TestAddSelfPlan_LandsInTomorrowsPlan
//
// ⚠️ **接线尺子**: 生成器 + 消费者, 必须断言它真的落进**明天**的行程表。
func TestAddSelfPlan_LandsInTomorrowsPlan(t *testing.T) {
	e, s := selfPlanEngine(t)
	if err := s.SaveSchedule(dayPlanFixtureWeek()); err != nil {
		t.Fatal(err)
	}
	tomorrow := TomorrowKey(clock.Now())
	base := DayPlan{Day: tomorrow, Items: []DayItem{
		{At: "09:00", What: "还书", From: DayFromSelf},
		{At: "12:00", What: "和可颂吃饭", With: "可颂", From: DayFromCast},
		{At: "19:00", What: "散步", From: DayFromEvent},
	}}
	if err := s.SaveDayPlan("owner", base, dayPlanFixtureWeek()); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.LoadDayPlan("owner", tomorrow)

	ok, why := e.AddSelfPlan(&FrameSelfPlan{At: "15:00", What: "去趟书店", Place: "旧书摊"}, got)
	if !ok {
		t.Fatalf("排不进去: %s", why)
	}
	after, has, err := s.LoadDayPlan(e.ownerKey(), tomorrow)
	if err != nil || !has {
		t.Fatalf("读不回来: %v", err)
	}
	if !dayPlanHasWhat(after, "去趟书店") {
		t.Fatalf("新条目没落进明天的行程: %+v", after.Items)
	}
	// 原有的三条必须一条不少 —— 加一条不该挤掉别的。
	if len(after.Items) != 4 {
		t.Fatalf("应当 4 条, 实得 %d: %+v", len(after.Items), after.Items)
	}
}

// TestAddSelfPlan_CapsPerDay
//
// ⚠️ §5.5 硬线二: 「不许为了显得忙而堆 —— 每天 ≤2 条是**硬上限**, 不是建议值」。
// 超限的处理是**默默不加 + 出声记日志**, 不是"训它一顿让它改"。
func TestAddSelfPlan_CapsPerDay(t *testing.T) {
	e, s := selfPlanEngine(t)
	if err := s.SaveSchedule(dayPlanFixtureWeek()); err != nil {
		t.Fatal(err)
	}
	ok1, _ := e.AddSelfPlan(&FrameSelfPlan{At: "15:00", What: "去趟书店"}, DayPlan{})
	ok2, _ := e.AddSelfPlan(&FrameSelfPlan{At: "16:00", What: "去还书"}, DayPlan{})
	if !ok1 || !ok2 {
		t.Fatalf("前两条应当都能排 (%v / %v)", ok1, ok2)
	}
	ok3, why := e.AddSelfPlan(&FrameSelfPlan{At: "17:00", What: "去看电影"}, DayPlan{})
	if ok3 {
		t.Fatal("第三条必须被上限拦住")
	}
	if strings.TrimSpace(why) == "" {
		t.Error("被拦了却不说为什么 —— 日志里会变成空白")
	}
	// 反面锚点: 上限**真的**是 2 (而不是"碰巧拦住了一次")。
	if SelfPlanDailyCap != 2 {
		t.Errorf("上限应当是 2 (§5.5), 实得 %d", SelfPlanDailyCap)
	}
}

// TestAddSelfPlan_RefusesSleepAndUserConflict
func TestAddSelfPlan_RefusesSleepAndUserConflict(t *testing.T) {
	e, s := selfPlanEngine(t)
	if err := s.SaveSchedule(dayPlanFixtureWeek()); err != nil {
		t.Fatal(err)
	}
	if ok, _ := e.AddSelfPlan(&FrameSelfPlan{At: "03:00", What: "半夜去书店"}, DayPlan{}); ok {
		t.Error("凌晨三点必须被拒")
	}
	tomorrow := DayPlan{Day: TomorrowKey(clock.Now()), Items: []DayItem{
		{At: "15:00", What: "和 ta 吃饭", From: DayFromUser},
	}}
	if ok, _ := e.AddSelfPlan(&FrameSelfPlan{At: "15:00", What: "去书店"}, tomorrow); ok {
		t.Error("撞用户约定必须被拒")
	}
	// ⚠️ 上面两次被拒**都不该消耗配额** —— 否则它一上午的笔误就把今天的额度用光了。
	if n := counterOrZero(s, "selfplan", clock.Today()); n != 0 {
		t.Errorf("被拒的预约不该计数, 实得 %d", n)
	}
}

// TestTomorrowPlanLine_GivesItItsOwnReasonToSayNo
//
// ⚠️ §5.5 用途 2: **它是拒绝的依据**。
// 看不见明天的安排, "那个时间我约了自己去书店"就说不出来 —— 而**无依据的拒绝 = 噪声**。
func TestTomorrowPlanLine_GivesItItsOwnReasonToSayNo(t *testing.T) {
	line := TomorrowPlanLine(DayPlan{Day: "2026-10-16", Items: []DayItem{
		{At: "15:00", What: "去趟书店", Place: "旧书摊", From: DayFromSelf},
		{At: "19:00", What: "和可颂吃饭", With: "可颂", From: DayFromCast},
	}})
	if line == "" {
		t.Fatal("明天有安排却不注入 —— 拒绝就没有依据")
	}
	for _, must := range []string{"【明天的事】", "15:00", "去趟书店", "旧书摊", "可颂", "系统事实"} {
		if !strings.Contains(line, must) {
			t.Errorf("明天那段缺了 %q:\n%s", must, line)
		}
	}
	// 空 → 一个字都不加。
	if got := TomorrowPlanLine(DayPlan{}); got != "" {
		t.Fatalf("空行程不该产出段: %q", got)
	}
}

// TestBuildSystem_CarriesTomorrowOnlyWhenThereIsOne
func TestBuildSystem_CarriesTomorrowOnlyWhenThereIsOne(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜", Template: "bestie"}
	empty := d.buildSystem(DialogInput{UserText: "在吗"}, p, "ja")
	if strings.Contains(empty, "【明天的事】") {
		t.Fatal("没有明天的安排却注入了段头")
	}
	with := d.buildSystem(DialogInput{UserText: "在吗", DayPlanTomorrow: DayPlan{Items: []DayItem{
		{At: "15:00", What: "去趟书店", From: DayFromSelf},
	}}}, p, "ja")
	if !strings.Contains(with, "【明天的事】") || !strings.Contains(with, "去趟书店") {
		t.Fatal("明天的安排没进提示词")
	}
}
