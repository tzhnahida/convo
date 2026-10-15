package convo

import (
	"strings"
	"testing"
	"time"
)

// dayPlanFixtureWeek 一份"晚上睡、白天醒"的周表, 用来判 sleep 冲突。
//
// ⚠️ **必须铺满七天**。原来只写了 thu, 而用例里的日期是 2026-10-16(**周五**)——
// 于是槽表查不到、sleep 判定整条失效, 而测试还以为自己在验它。
// 这正是被测的 bug 本身(日期 vs 星期键)在**测试夹具里**的翻版: 两边都得对齐。
func dayPlanFixtureWeek() *WeekSchedule {
	ws := &WeekSchedule{SleepStart: "23:30", SleepEnd: "07:30"}
	for _, d := range dayOrder {
		ws.Days = append(ws.Days, DaySchedule{Day: d, Slots: []Slot{
			{Start: "00:00", End: "07:30", State: StateSleep},
			{Start: "07:30", End: "23:30", State: StateFree},
			{Start: "23:30", End: "24:00", State: StateSleep},
		}})
	}
	return ws
}

func dayItem(at, what, with, from string) DayItem {
	return DayItem{At: at, What: what, With: with, From: from}
}

// TestDayPlan_RejectsSixthSource
//
// ⚠️ §4.5.2 的硬线: **五种来源, 没有第六种。**
// 模型现编的一天, 白天一被追问就穿帮 —— 所以"哪来的"是结构化字段, 不是一句说明。
func TestDayPlan_RejectsSixthSource(t *testing.T) {
	ws := dayPlanFixtureWeek()
	p := DayPlan{Day: "thu", Items: []DayItem{
		dayItem("09:00", "去公司", "", DayFromEvent),
		dayItem("11:00", "开会", "", DayFromCast),
		dayItem("15:00", "买咖啡", "可颂", DayFromCast),
		dayItem("19:00", "看书", "", "imagination"), // ← 第六种
	}}
	if err := ValidateDayPlan(p, ws); err == nil {
		t.Fatal("凭空生成的来源必须被拒 —— 那是「模型现编的一天」的入口")
	}
	// 正面: 换成合法来源必须过。
	p.Items[3].From = DayFromSelf
	if err := ValidateDayPlan(p, ws); err != nil {
		t.Fatalf("合法来源却被拒: %v", err)
	}
}

// TestDayPlan_RejectsItemsInSleep
//
// ⚠️ 它在睡觉的时候不能同时在搬箱子。跨午夜的 sleep(23:30-24:00 / 00:00-07:30)
// 就是靠**槽表**判的, 不许另写一套时间窗。
func TestDayPlan_RejectsItemsInSleep(t *testing.T) {
	ws := dayPlanFixtureWeek()
	// 凌晨 03:00 落在 00:00-07:30 的 sleep 里。
	if !DayItemInSleep(ws, "thu", "03:00", 30) {
		t.Fatal("03:00 应当判为落在 sleep 槽里")
	}
	// 23:45 落在 23:30-24:00 的 sleep 里。
	if !DayItemInSleep(ws, "thu", "23:45", 30) {
		t.Fatal("23:45 应当判为落在 sleep 槽里")
	}
	// 白天的 15:00 不该被拦。
	if DayItemInSleep(ws, "thu", "15:00", 60) {
		t.Fatal("15:00 被误判成 sleep —— 那会把正常行程全删掉")
	}
	// 归一化应当**删掉**冲突项而不是整份作废。
	// ⚠️ 构造刻意让**被删的那条是唯一的独自项** —— 这样才验得到那条真性质:
	// **删完之后必须重新过一遍校验**, 不许"删完就算数"。
	p := DayPlan{Day: "thu", Items: []DayItem{
		dayItem("03:00", "半夜搬箱子", "", DayFromEvent), // ← 唯一的独自项, 会被 sleep 冲突删掉
		dayItem("09:00", "去公司", "甲", DayFromCast),
		dayItem("11:00", "开会", "乙", DayFromCast),
		dayItem("15:00", "买咖啡", "丙", DayFromCast),
	}}
	got := NormalizeDayPlan(p, ws)
	for _, it := range got.Items {
		if it.At == "03:00" {
			t.Fatal("落在 sleep 里的那条没有被删掉")
		}
	}
	if len(got.Items) != 3 {
		t.Fatalf("应当剩 3 条, 实得 %d", len(got.Items))
	}
	if got.SoloCount() != 0 {
		t.Fatalf("用例构造有误: 删完之后不该还有独自项, 实得 %d", got.SoloCount())
	}
	// 删光独自项之后, 校验必须拦下来。
	if err := ValidateDayPlan(got, ws); err == nil {
		t.Fatal("删掉唯一的独自项之后一条独自都没有, 应当被拒")
	}
}

// TestDayPlan_RequiresSoloShare
//
// ⚠️ **一个人待着也是生活。**全是"和某人"的一天不是生活, 是社交任务表。
func TestDayPlan_RequiresSoloShare(t *testing.T) {
	ws := dayPlanFixtureWeek()
	allWith := DayPlan{Day: "thu", Items: []DayItem{
		dayItem("09:00", "a", "甲", DayFromCast),
		dayItem("11:00", "b", "乙", DayFromCast),
		dayItem("15:00", "c", "丙", DayFromCast),
	}}
	if err := ValidateDayPlan(allWith, ws); err == nil {
		t.Fatal("一条独自都没有必须被拒")
	}
	// 3 条里 1 条独自 → 达标 (向上取整)。
	oneSolo := DayPlan{Day: "thu", Items: []DayItem{
		dayItem("09:00", "a", "甲", DayFromCast),
		dayItem("11:00", "b", "乙", DayFromCast),
		dayItem("15:00", "c", "", DayFromSelf),
	}}
	if err := ValidateDayPlan(oneSolo, ws); err != nil {
		t.Fatalf("3 条里 1 条独自应当达标: %v", err)
	}
}

// TestDayPlan_RejectsTooManyItems
func TestDayPlan_RejectsTooManyItems(t *testing.T) {
	ws := dayPlanFixtureWeek()
	var items []DayItem
	for i := 0; i < DayPlanMaxItems+1; i++ {
		items = append(items, dayItem("09:00", "x", "", DayFromEvent))
	}
	if err := ValidateDayPlan(DayPlan{Day: "thu", Items: items}, ws); err == nil {
		t.Fatalf("%d 条必须被拒 (上限 %d)", len(items), DayPlanMaxItems)
	}
	// 反面锚点: 恰好到上限必须过。
	if err := ValidateDayPlan(DayPlan{Day: "thu", Items: items[:DayPlanMaxItems]}, ws); err != nil {
		t.Fatalf("恰好 %d 条却被拒: %v", DayPlanMaxItems, err)
	}
}

// TestDayPlan_EmptyIsRejectedNotSilentlyKept
//
// ⚠️ 零值 ≠ 空行程: LoadDayPlan 的第二个返回值是"有没有"。
// 而 SaveDayPlan 遇到空行程必须**拒写**, 不许写一份空进去。
func TestDayPlan_EmptyIsRejectedNotSilentlyKept(t *testing.T) {
	ws := dayPlanFixtureWeek()
	if err := ValidateDayPlan(DayPlan{Day: "thu"}, ws); err == nil {
		t.Fatal("空行程必须被拒")
	}
	s := newTestStore(t)
	if err := EnsureDayPlanSchema(s); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveDayPlan("owner", DayPlan{Day: "thu"}, ws); err == nil {
		t.Fatal("空行程不该落库")
	}
	// 没写过 → 有没有 = false, 而不是"有一份空的"。
	if _, ok, err := s.LoadDayPlan("owner", "thu"); err != nil || ok {
		t.Fatalf("没写过应当 ok=false, 实得 ok=%v err=%v", ok, err)
	}
	// 写一份合法的 → 读回来一致。
	good := DayPlan{Day: "thu", Items: []DayItem{
		dayItem("09:00", "去公司", "", DayFromEvent),
		dayItem("11:00", "开会", "可颂", DayFromCast),
		dayItem("15:00", "把书还了", "", DayFromSelf),
		dayItem("19:00", "买咖啡", "可颂", DayFromCast),
	}}
	if err := s.SaveDayPlan("owner", good, ws); err != nil {
		t.Fatalf("合法行程写不进去: %v", err)
	}
	got, ok, err := s.LoadDayPlan("owner", "thu")
	if err != nil || !ok {
		t.Fatalf("读不回来: ok=%v err=%v", ok, err)
	}
	if len(got.Items) != len(good.Items) {
		t.Fatalf("条数对不上: %d vs %d", len(got.Items), len(good.Items))
	}
	// 另一个 owner 读不到 —— 分区必须生效。
	if _, ok, _ := s.LoadDayPlan("someone-else", "thu"); ok {
		t.Fatal("行程跨 owner 泄漏了")
	}
}

// TestDayPlanLine_MarksPastAndKeepsFacts
//
// ⚠️ 行程是**日程/系统事实**, 按 §4.6.2 的边界必须给全 —— 这条测它确实给全了,
// 而且过去的那几条被标出来(否则 22:00 它还会说"我待会儿去搬箱子")。
func TestDayPlanLine_MarksPastAndKeepsFacts(t *testing.T) {
	now := time.Date(2026, 10, 16, 18, 0, 0, 0, time.Local)
	p := DayPlan{Day: "thu", Items: []DayItem{
		{At: "09:00", What: "去公司", From: DayFromEvent},
		{At: "19:30", What: "陪可颂搬箱子", With: "可颂", Place: "3 号仓", From: DayFromCast},
	}}
	line := DayPlanLine(p, now)
	if line == "" {
		t.Fatal("有行程却不注入 —— 那层 2 就白建了")
	}
	for _, must := range []string{"09:00", "去公司", "19:30", "陪可颂搬箱子", "可颂", "3 号仓", "系统事实"} {
		if !strings.Contains(line, must) {
			t.Errorf("行程段缺了 %q:\n%s", must, line)
		}
	}
	// 09:00 已经过去, 19:30 还没有。
	if !strings.Contains(line, "已经过去了") {
		t.Errorf("过去的那条没被标出来:\n%s", line)
	}
	// 只许标一次(19:30 那条不该被标)。
	if n := strings.Count(line, "已经过去了"); n != 1 {
		t.Errorf("应当恰好标 1 条, 实得 %d:\n%s", n, line)
	}
	// 空行程 → 不注入 (段必须是"没有就一个字都不加")。
	if got := DayPlanLine(DayPlan{Day: "thu"}, now); got != "" {
		t.Fatalf("空行程不该产出段: %q", got)
	}
}

// TestDurMinutes_TolerantParsing
func TestDurMinutes_TolerantParsing(t *testing.T) {
	cases := map[string]int{
		"": 0, "40m": 40, "1h": 60, "1h30m": 90, "90": 90,
		"2h": 120, " 45m ": 45, "半小时": 0,
	}
	for in, want := range cases {
		if got := DurMinutes(in); got != want {
			t.Errorf("DurMinutes(%q) = %d, 想要 %d", in, got, want)
		}
	}
}
