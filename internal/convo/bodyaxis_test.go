package convo

import (
	"strings"
	"testing"
	"time"
)

func at(h, m int) time.Time { return time.Date(2026, 10, 15, h, m, 0, 0, time.Local) }

// TestComputeAxes_NoFactsIsBaselineNotZero
//
// ⚠️ **这条是本模块最重要的一条。**
// §4.2.2 的告警: 这三(四)个量**没有"未提供"这一档**, 库里读不到一律折成**基线**,
// **绝不许折成 0** —— 折成 0 就是"精力为零"和"饿死了"。
// 这与 §3 的 WMO code 0 = 晴 是同一个坑。
func TestComputeAxes_NoFactsIsBaselineNotZero(t *testing.T) {
	a := ComputeAxes(AxesInput{Now: at(10, 0)})
	if a.Energy != EnergyBaseline {
		t.Errorf("没有事实时精力应当是基线 %.2f, 实得 %.2f —— 折成 0 就是'精力为零'", EnergyBaseline, a.Energy)
	}
	if a.Energy == 0 || a.Thirst == 0 {
		t.Fatal("折成 0 了")
	}
	if a.HasFacts {
		t.Error("一条事实都没有, HasFacts 不該为 true")
	}
	// 而且三条基线都必须是**正常人的样子** —— 不许有任何一条一上来就是"极值"。
	if EnergyLevel(a.Energy) != "" || HungerLevel(a.Hunger) != "" {
		t.Errorf("基线不该产出任何档位: %+v", a)
	}
}

// TestComputeAxes_BrokenTimestampIsSkippedNotZeroed
//
// ⚠️ 时刻解析不出 → **跳过这条事实**, 不许折成 0 点。
// 折成 0 点等于"凌晨刚吃过" —— 那会让一个整天没吃的角色永远不饿, 而且完全静默。
func TestComputeAxes_BrokenTimestampIsSkippedNotZeroed(t *testing.T) {
	broken := ComputeAxes(AxesInput{Now: at(18, 0), Facts: []BodyAxisFact{
		{Fact: FactMeal, TS: ""},
		{Fact: FactMeal, TS: "乱写"},
	}})
	// ⚠️ 判据是「**一条都用不上** = 未提供」, 所以它必须与"一条都没传"**逐字段相同**。
	// 折成 0 点的话, 一个时刻全是坏数据的库会让它在傍晚凭空饿起来。
	none := ComputeAxes(AxesInput{Now: at(18, 0)})
	if broken != none {
		t.Fatalf("坏时刻的事实被当成真事实用了: %+v vs %+v", broken, none)
	}
	// 对照: 真有一条好使的进食事实时, 它必须**比"今天还没吃"更不饿**。
	fed := ComputeAxes(AxesInput{Now: at(18, 0), Facts: []BodyAxisFact{
		{Fact: FactMeal, TS: "2026-10-15 17:00:00"},
	}})
	nap := ComputeAxes(AxesInput{Now: at(18, 0), Facts: []BodyAxisFact{
		{Fact: FactNap, TS: "2026-10-15 05:00:00"},
	}})
	if fed.Hunger >= nap.Hunger {
		t.Errorf("一小时前刚吃过, 应当比今天没吃更不饿: %.2f vs %.2f", fed.Hunger, nap.Hunger)
	}
}

// TestComputeAxes_MealAndSleepPushTheRightWay
func TestComputeAxes_MealAndSleepPushTheRightWay(t *testing.T) {
	fed := ComputeAxes(AxesInput{Now: at(14, 0), Facts: []BodyAxisFact{
		{Fact: FactMeal, TS: "2026-10-15 13:00:00"},
	}})
	// ⚠️ 对照组**也必须带一条可用事实** —— 一条事实都没有是"未提供", 直接返回基线,
	// 拿它去比就成了"没读数比吃了饭还饱", 那是夹具的错不是模型的错。
	starved := ComputeAxes(AxesInput{Now: at(14, 0), Facts: []BodyAxisFact{
		{Fact: FactNap, TS: "2026-10-15 05:00:00"},
	}})
	if fed.Hunger >= starved.Hunger {
		t.Errorf("刚吃过应当比没吃更不饿: %.2f vs %.2f", fed.Hunger, starved.Hunger)
	}
	// 睡不够 → 精力掉。
	tired := ComputeAxes(AxesInput{Now: at(14, 0), Facts: []BodyAxisFact{
		{Fact: FactSleep, TS: "2026-10-15 07:30:00", Minutes: 240},
	}})
	if tired.Energy >= EnergyBaseline {
		t.Errorf("只睡 4 小时精力应当低于基线: %.2f", tired.Energy)
	}
	// 睡饱 → 不低于基线, 但也不许超过 1。
	rested := ComputeAxes(AxesInput{Now: at(14, 0), Facts: []BodyAxisFact{
		{Fact: FactSleep, TS: "2026-10-15 07:30:00", Minutes: 540},
	}})
	if rested.Energy < EnergyBaseline || rested.Energy > 1 {
		t.Errorf("睡饱之后精力应当 ≥ 基线且 ≤ 1, 实得 %.2f", rested.Energy)
	}
	// 天热 → 口渴。
	nap := []BodyAxisFact{{Fact: FactNap, TS: "2026-10-15 05:00:00"}}
	hot := ComputeAxes(AxesInput{Now: at(14, 0), Hot: true, Facts: nap})
	cool := ComputeAxes(AxesInput{Now: at(14, 0), Facts: nap})
	if hot.Thirst <= cool.Thirst {
		t.Errorf("热天应当更渴: %.2f vs %.2f", hot.Thirst, cool.Thirst)
	}
}

// TestComputeAxes_NoMealMeansSinceMidnight
//
// ⚠️ 没记录 ≠ 刚吃过。没有进食事实时从**当天 00:00** 起算 —— 那是"今天还没吃"。
// 折成"刚吃过"会让一个整天没吃的角色永远不饿; 折成"饿了很久"则是凭空。
func TestComputeAxes_NoMealMeansSinceMidnight(t *testing.T) {
	nap := []BodyAxisFact{{Fact: FactNap, TS: "2026-10-15 05:00:00"}}
	early := ComputeAxes(AxesInput{Now: at(1, 0), Facts: nap})
	late := ComputeAxes(AxesInput{Now: at(20, 0), Facts: nap})
	if late.Hunger <= early.Hunger {
		t.Fatalf("同样没吃东西, 晚上应当比凌晨更饿: %.2f vs %.2f", late.Hunger, early.Hunger)
	}
	// 凌晨 1 点只走了 1 小时 → 涨不到"有点饿"那一档 (§4.2.3: 不许一上来就顶格)。
	if HungerLevel(early.Hunger) != "" {
		t.Errorf("凌晨 1 点不该已经饿了: %.2f", early.Hunger)
	}
}

// TestBodyAxesLine_OnlyThirstMayBeSpoken
//
// ⚠️ §4.2.3/§4.2.4: 三条量的落点**必须互不重叠**, 而且**只有口渴**允许说出口 ——
// 因为它不指向对方(说饿了像在等投喂, 说累了像在等安慰, 说渴了就只是去喝水)。
// 这条边界不写进提示词, 它就会把"有点乏"说成"我好累", 而那是**索取**。
func TestBodyAxesLine_OnlyThirstMayBeSpoken(t *testing.T) {
	line := BodyAxesLine(Axes{Energy: 0.1, Hunger: 0.9, Thirst: 0.9}, true)
	if line == "" {
		t.Fatal("三条都不正常却不注入 —— 那这一段就白建了")
	}
	idxThirst := strings.Index(line, "口渴")
	if idxThirst < 0 {
		t.Fatal("口渴那一条不见了")
	}
	// 精力 / 饥饿 那两条必须写明"不许说出口"。
	for _, seg := range []string{"精力", "饥饿"} {
		i := strings.Index(line, seg)
		if i < 0 {
			t.Fatalf("%s 那一条不见了", seg)
		}
		if !strings.Contains(line[i:idxThirst], "不许说") {
			t.Errorf("%s 那一条没有「不许说出口」的约束:\n%s", seg, line)
		}
	}
	// 口渴那条必须明确**允许**, 同时挡住"升级成索取"。
	if !strings.Contains(line[idxThirst:], "可以说出口") {
		t.Errorf("口渴那一条没有开这个例外:\n%s", line)
	}
	if !strings.Contains(line[idxThirst:], "我好渴") {
		t.Errorf("口渴那一条没挡住「变成索取」的写法:\n%s", line)
	}
}

// TestBodyAxesLine_IsSilentWhenNormal
//
// ⚠️ 正常人不会时刻报告自己「不饿不渴」—— 三档全空必须**一个字都不加**。
func TestBodyAxesLine_IsSilentWhenNormal(t *testing.T) {
	if got := BodyAxesLine(Axes{Energy: EnergyBaseline, Hunger: HungerBaseline, Thirst: ThirstBaseline}, true); got != "" {
		t.Fatalf("基线状态不该产出段: %q", got)
	}
	if got := BodyAxesLine(Axes{}, true); got != "" {
		t.Fatalf("零值(全是 0 精力)也不该产出段 —— 它是「未提供」, 不是「精疲力尽」: %q", got)
	}
}

// TestBodyAxesLine_NoNumbers
//
// ⚠️ §4.2.3: **注入的是档位, 不是数字。** 写「有点饿」, 绝不写「饥饿度 63%」——
// 给了数字模型就会照着报数, 那是最假的写法。数值只对 owner 可见。
func TestBodyAxesLine_NoNumbers(t *testing.T) {
	line := BodyAxesLine(Axes{Energy: 0.13, Hunger: 0.67, Thirst: 0.91}, true)
	for _, bad := range []string{"0.13", "0.67", "0.91", "13%", "67%", "91%", "63%"} {
		if strings.Contains(line, bad) {
			t.Errorf("身体段里出现了数字 %q —— 给了数字它就会照着报数:\n%s", bad, line)
		}
	}
}

// ——— 事实的写入 ———

func TestRecordBodyFact_RejectsUnknownKind(t *testing.T) {
	s := agTestStore(t)
	if err := EnsureBodyAxisSchema(s); err != nil {
		t.Fatal(err)
	}
	// ⚠️ 放进去的话, 一个拼错的 fact 会**永远不被任何分支读到**,
	// 而症状是"吃了却没回血" —— 静默且极难查。
	if err := s.RecordBodyFact(BodyAxisFact{Fact: "eat"}); err == nil {
		t.Fatal("未知的事实种类必须被拒")
	}
	if err := s.RecordBodyFact(BodyAxisFact{Fact: FactMeal, TS: "2026-10-15 12:00:00"}); err != nil {
		t.Fatalf("合法事实写不进去: %v", err)
	}
}

// TestRecordBodyFact_DedupesBySrc
//
// ⚠️ 夜里那批会重跑(睡眠状态转换可能触发两次)—— 没有去重键就是**吃了两顿**。
// 靠唯一索引, 不靠"先查后插"(那中间有一个窗口)。
func TestRecordBodyFact_DedupesBySrc(t *testing.T) {
	s := agTestStore(t)
	if err := EnsureBodyAxisSchema(s); err != nil {
		t.Fatal(err)
	}
	f := BodyAxisFact{Src: "plan:2026-10-15:12:00", Day: "2026-10-15", Fact: FactMeal, TS: "2026-10-15 12:00:00"}
	for i := 0; i < 3; i++ {
		if err := s.RecordBodyFact(f); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.RecentBodyFacts(at(23, 0), 48)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("同一个 src 写了 3 次, 应当只有 1 条, 实得 %d", len(got))
	}
}

// TestRecordPlanFacts_OnlyPastItemsAndIdempotent
func TestRecordPlanFacts_OnlyPastItemsAndIdempotent(t *testing.T) {
	s := agTestStore(t)
	if err := EnsureBodyAxisSchema(s); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: s, Cfg: DefaultConfig()}
	plan := DayPlan{Day: "2026-10-15", Items: []DayItem{
		{At: "08:00", What: "在食堂吃了碗面", From: DayFromSelf},
		{At: "15:00", What: "去楼下买杯美式", From: DayFromSelf},
		{At: "22:00", What: "夜宵 烧烤", From: DayFromSelf}, // 还没到
	}}
	meals, drinks := e.RecordPlanFacts("2026-10-15", plan, at(18, 0))
	if meals != 1 || drinks != 1 {
		t.Fatalf("应当吃 1 喝 1, 实得 %d/%d", meals, drinks)
	}
	// ⚠️ 还没到的那条**不许**记: 把晚上的安排当成"已经吃了", 会让它在下午就不饿。
	got, _ := s.RecentBodyFacts(at(23, 0), 48)
	if len(got) != 2 {
		t.Fatalf("应当只记 2 条(22:00 那条还没发生), 实得 %d", len(got))
	}
	// ⚠️ 重跑一次不许变成吃了两顿。
	e.RecordPlanFacts("2026-10-15", plan, at(18, 0))
	again, _ := s.RecentBodyFacts(at(23, 0), 48)
	if len(again) != 2 {
		t.Fatalf("重跑之后变成了 %d 条 —— 去重键没生效", len(again))
	}
}

// TestBuildSystem_CarriesBodyAxesOnlyWhenAbnormal
func TestBuildSystem_CarriesBodyAxesOnlyWhenAbnormal(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜", Template: "bestie"}
	normal := d.buildSystem(DialogInput{UserText: "在吗", BodyAxes: Axes{Energy: EnergyBaseline, Hunger: HungerBaseline, Thirst: ThirstBaseline}}, p, "ja")
	if strings.Contains(normal, "【身体】") {
		t.Fatal("一切正常却注入了身体段")
	}
	odd := d.buildSystem(DialogInput{UserText: "在吗", BodyAxes: Axes{Energy: 0.2, Hunger: 0.8, Thirst: 0.8}}, p, "ja")
	if !strings.Contains(odd, "【身体】") {
		t.Fatal("身体不正常却没注入 —— 那三条量就没有落点")
	}
}
