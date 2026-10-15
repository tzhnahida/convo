package convo

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/tzhnahida/VocabLock/internal/clock"
)

// scriptChatter 按脚本逐次作答 (第 N 次调用返回 replies[N])。
//
// ⚠️ 与 sitCaptureChatter 的区别: 那个永远返回同一句, 只适合测"接线"。
// 这里要测**重试**与**失败路径**, 所以必须能逐次给不同答案。
type scriptChatter struct {
	replies    []string
	errs       []error
	n          int
	lastSystem string
	lastUser   string
}

func (f *scriptChatter) Chat(_ context.Context, system, user string) (string, error) {
	f.lastSystem, f.lastUser = system, user
	i := f.n
	f.n++
	if i < len(f.errs) && f.errs[i] != nil {
		return "", f.errs[i]
	}
	if i < len(f.replies) {
		return f.replies[i], nil
	}
	if len(f.replies) > 0 {
		return f.replies[len(f.replies)-1], nil
	}
	return "", fmt.Errorf("脚本用完了")
}

// 用普通字符串拼 JSON, **不用 Go raw string** —— 免得和调用方的转义打架。
func dayItemJSON(at, dur, with, what, place, from string) string {
	s := "{\"at\":\"" + at + "\""
	if dur != "" {
		s += ",\"dur\":\"" + dur + "\""
	}
	if with != "" {
		s += ",\"with\":\"" + with + "\""
	}
	s += ",\"what\":\"" + what + "\""
	if place != "" {
		s += ",\"place\":\"" + place + "\""
	}
	s += ",\"from\":\"" + from + "\"}"
	return s
}

func dayPlanJSON(day string, items ...string) string {
	return "{\"day\":\"" + day + "\",\"items\":[" + strings.Join(items, ",") + "]}"
}

func validDayPlanJSON(day string) string {
	return dayPlanJSON(day,
		dayItemJSON("09:00", "1h", "", "把这周攒的书还了", "", DayFromSelf),
		dayItemJSON("11:00", "40m", "可颂", "去分拣仓帮可颂搬箱子", "3 号仓", DayFromCast),
		dayItemJSON("15:00", "1h", "", "在旧书摊翻一会儿", "", DayFromSelf),
		dayItemJSON("19:30", "1h", "德克萨斯", "一起吃个饭", "", DayFromCast),
	)
}

func dayPlanTestInput(day string) DayPlanInput {
	return DayPlanInput{
		Day:        day,
		Weekday:    "thu",
		Profile:    &PersonaProfile{Name: "能天使", Description: "企鹅物流的信使"},
		Bible:      WorldBible{Self: "企鹅物流的信使", Cast: []CastMember{{Name: "可颂", Relation: RelationCompanion, Status: "在算账"}, {Name: "德克萨斯", Relation: RelationCompanion, Status: "在点货"}}},
		HasBible:   true,
		Events:     []EventNote{{Text: "楼下在装修"}},
		SleepStart: "23:30", SleepEnd: "07:30",
	}
}

// TestGenerateDayPlan_AcceptsValidPlan
func TestGenerateDayPlan_AcceptsValidPlan(t *testing.T) {
	day := "2026-10-16"
	c := &scriptChatter{replies: []string{validDayPlanJSON(day)}}
	p, err := GenerateDayPlan(context.Background(), c, dayPlanTestInput(day), dayPlanFixtureWeek())
	if err != nil {
		t.Fatalf("合法输出却被拒: %v", err)
	}
	if p.Day != day || len(p.Items) != 4 {
		t.Fatalf("行程不对: day=%q items=%d", p.Day, len(p.Items))
	}
	if c.n != 1 {
		t.Fatalf("合法输出不该重试, 实调 %d 次", c.n)
	}
}

// TestGenerateDayPlan_RefusesWrongDay
//
// ⚠️ 排错日子必须拦: 一份"明天"的行程写到后天, **明天就没有行程**,
// 而日志一切正常 —— 这是静默失效最爱的形状。
func TestGenerateDayPlan_RefusesWrongDay(t *testing.T) {
	day := "2026-10-16"
	c := &scriptChatter{replies: []string{validDayPlanJSON("2026-10-17")}}
	if _, err := GenerateDayPlan(context.Background(), c, dayPlanTestInput(day), dayPlanFixtureWeek()); err == nil {
		t.Fatal("day 写错了却被采用 —— 明天会静默地没有行程")
	}
	if c.n != 2 {
		t.Fatalf("应当重试一次, 实调 %d 次", c.n)
	}
	// ⚠️ 约定与 GenerateSchedule 一致: 短指令走 **system**, 大段素材走 user。
	if !strings.Contains(c.lastSystem, "2026-10-17") {
		t.Errorf("重试时没告诉它错在哪: %q", c.lastSystem)
	}
}

// TestGenerateDayPlan_NoFallbackWhenModelFails
//
// ⚠️ **这条是本模块最重要的一条**: 模型挂了**不许编一份行程**。
// 与事件池(NightPool 永远非空)故意相反 —— 行程是事实陈述, 编出来会穿帮;
// 而"今天没排上"的代价只是段消失。
func TestGenerateDayPlan_NoFallbackWhenModelFails(t *testing.T) {
	day := "2026-10-16"
	c := &scriptChatter{errs: []error{fmt.Errorf("网络抖动"), fmt.Errorf("网络抖动")}}
	p, err := GenerateDayPlan(context.Background(), c, dayPlanTestInput(day), dayPlanFixtureWeek())
	if err == nil {
		t.Fatalf("模型全挂却返回了一份行程 —— 那就是编的: %+v", p)
	}
	if len(p.Items) != 0 {
		t.Fatalf("失败路径必须返回空行程, 实得 %d 条", len(p.Items))
	}
}

// TestGenerateDayPlan_NormalizesSleepConflict
func TestGenerateDayPlan_NormalizesSleepConflict(t *testing.T) {
	day := "2026-10-16"
	c := &scriptChatter{replies: []string{dayPlanJSON(day,
		dayItemJSON("03:00", "", "", "半夜起来搬箱子", "", DayFromEvent),
		dayItemJSON("09:00", "", "", "还书", "", DayFromSelf),
		dayItemJSON("11:00", "", "可颂", "搬箱子", "", DayFromCast),
		dayItemJSON("15:00", "", "", "翻旧书摊", "", DayFromSelf),
		dayItemJSON("19:30", "", "德克萨斯", "吃饭", "", DayFromCast),
	)}}
	p, err := GenerateDayPlan(context.Background(), c, dayPlanTestInput(day), dayPlanFixtureWeek())
	if err != nil {
		t.Fatalf("去掉 sleep 冲突后应当合法: %v", err)
	}
	for _, it := range p.Items {
		if it.At == "03:00" {
			t.Fatal("落在 sleep 里的那条没有被清掉")
		}
	}
	if len(p.Items) != 4 {
		t.Fatalf("应当剩 4 条, 实得 %d", len(p.Items))
	}
}

// TestDayPlanPrompt_StatesTheRules
//
// ⚠️ 生成提示词里**必须**说清五种来源与睡眠窗 ——
// 只写硬校验不写规则, 模型会白跑一趟才发现被拒; 只写规则不写校验, 它会偶尔越界。
func TestDayPlanPrompt_StatesTheRules(t *testing.T) {
	p := dayPlanPrompt(dayPlanTestInput("2026-10-16"))
	for _, must := range []string{
		DayFromCast, DayFromNight, DayFromEvent, DayFromSelf, DayFromUser,
		"23:30", "07:30",
		"一个人待着也是生活",
		"4-7 条",
		"可颂", "德克萨斯",
	} {
		if !strings.Contains(p, must) {
			t.Errorf("生成提示词缺了 %q", must)
		}
	}
}

// TestPlanTomorrow_WritesTomorrowsPlan
//
// ⚠️ **接线尺子**: 生成器 + 消费者, 必须断言消费者在**生产路径上被调用**。
// 只测 GenerateDayPlan 能返回东西是不够的 —— 那样"夜里根本没调它"也照样绿。
func TestPlanTomorrow_WritesTomorrowsPlan(t *testing.T) {
	day := TomorrowKey(clock.Now())
	c := &scriptChatter{replies: []string{validDayPlanJSON(day)}}
	e, _ := groupSpeakEngine(t, c)
	if err := e.Store.SaveSchedule(dayPlanFixtureWeek()); err != nil {
		t.Fatal(err)
	}
	p, err := e.Store.LoadProfile()
	if err != nil || p == nil {
		t.Fatalf("夹具没有 profile: %v", err)
	}
	e.planTomorrow(context.Background(), p, []string{"昨晚跟可颂聊到她下周要考试"})
	got, ok, err := e.Store.LoadDayPlan(e.ownerKey(), day)
	if err != nil || !ok {
		t.Fatalf("次日行程没落库 —— 层 2 等于没接线: ok=%v err=%v", ok, err)
	}
	if got.Day != day {
		t.Fatalf("落到 %q 去了, 应当落明天 %q", got.Day, day)
	}
	if len(got.Items) == 0 {
		t.Fatal("落了一份空行程")
	}
	// 反面: 模型挂掉 → **不许落库**(宁可没有, 不可现编)。
	day2 := TomorrowKey(clock.Now().AddDate(0, 0, 1))
	c2 := &scriptChatter{errs: []error{fmt.Errorf("挂了"), fmt.Errorf("挂了")}}
	e2, _ := groupSpeakEngine(t, c2)
	if err := e2.Store.SaveSchedule(dayPlanFixtureWeek()); err != nil {
		t.Fatal(err)
	}
	p2, _ := e2.Store.LoadProfile()
	e2.planTomorrow(context.Background(), p2, nil)
	if _, ok, _ := e2.Store.LoadDayPlan(e2.ownerKey(), day2); ok {
		t.Fatal("模型全挂却落了一份行程 —— 那就是编的")
	}
}

// TestDayPlan_ReachesDialogPrompt
//
// 时间 / 行程这类**系统事实**必须真的进提示词 —— 否则它还是会现编,
// 而"今天没有安排"和"段没接上"在现象上分不出来。
func TestDayPlan_ReachesDialogPrompt(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜", Template: "bestie"}
	plan := DayPlan{Day: clock.Today(), Items: []DayItem{
		{At: "15:30", What: "去分拣仓帮可颂搬箱子", With: "可颂", Place: "3 号仓", From: DayFromCast},
		{At: "19:00", What: "把书还了", From: DayFromSelf},
	}}
	sys := d.buildSystem(DialogInput{UserText: "在吗", DayPlan: plan}, p, "ja")
	for _, must := range []string{"【今天的事】", "15:30", "去分拣仓帮可颂搬箱子", "3 号仓", "19:00"} {
		if !strings.Contains(sys, must) {
			t.Errorf("行程没进提示词, 缺 %q", must)
		}
	}
	// 反面: 没有行程时提示词**逐字节不变**(段必须是"没有就一个字都不加")。
	plain := d.buildSystem(DialogInput{UserText: "在吗"}, p, "ja")
	if strings.Contains(plain, "【今天的事】") {
		t.Fatal("没有行程却注入了段头")
	}
}
