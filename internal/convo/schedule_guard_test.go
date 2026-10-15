package convo

import (
	"strings"
	"testing"
)

// ——— 周日程: 校验的**下界** ———

// TestValidateWeekSchedule_BuiltinTemplatesAreLegal
//
// 正面用例: 三个内置模板本身必须是合法的 —— 否则"兜底"兜的是个坏表。
func TestValidateWeekSchedule_BuiltinTemplatesAreLegal(t *testing.T) {
	for _, id := range []string{"student", "office_worker", "night_owl"} {
		if err := ValidateWeekSchedule(builtinSchedule(id)); err != nil {
			t.Errorf("内置模板 %s 自己就不过校验: %v", id, err)
		}
	}
}

// TestValidateWeekSchedule_RejectsADayWithNoSleep
//
// ⚠️ 这条是**生产事故的复刻**, 不是假想用例。
//
// 2026-10-15 解生产库发现: 周三**一整天没有一个 sleep 槽** —— 周二只睡 30 分钟就被叫醒,
// 周三通宵。而当时的校验只写了「sleep 槽必须落在声明的睡眠窗内」这个**单向**约束:
// **一个 sleep 槽都不写的日子, 它压根没有槽可以违反** —— 校验、重试、内置兜底一路绿灯。
//
// 所以判据必须补一条**下界**。这条测试锁的就是那个下界。
func TestValidateWeekSchedule_RejectsADayWithNoSleep(t *testing.T) {
	ws := builtinSchedule("student")
	stripped := false
	for i := range ws.Days {
		if ws.Days[i].Day != "wed" {
			continue
		}
		for j := range ws.Days[i].Slots {
			if ws.Days[i].Slots[j].State == StateSleep {
				ws.Days[i].Slots[j].State = StateFree
				stripped = true
			}
		}
	}
	if !stripped {
		t.Fatal("内置模板的周三没有 sleep 槽 —— 这个用例已经失去意义, 必须重写")
	}
	// 先把"改完之后还满足旧的几条规则"确认一下, 免得测试因为别的原因红。
	if err := ValidateWeekSchedule(ws); err == nil {
		t.Fatal("周三 0 分钟 sleep 必须被拒 —— 这正是生产库里那份日程的形状")
	}
}

// TestValidateWeekSchedule_RejectsTooLittleSleep
//
// 反面: 只睡 5 小时也要拒。**下界不是"有没有", 是"够不够"。**
func TestValidateWeekSchedule_RejectsTooLittleSleep(t *testing.T) {
	ws := builtinSchedule("student")
	if !replaceDaySlots(ws, "wed", []Slot{
		{Start: "00:00", End: "05:00", State: StateSleep},
		{Start: "05:00", End: "24:00", State: StateFree},
	}) {
		t.Fatal("内置模板里没有 wed")
	}
	if err := ValidateWeekSchedule(ws); err == nil {
		t.Fatalf("5 小时睡眠(%d 分钟)必须被拒", 5*60)
	}
}

func replaceDaySlots(ws *WeekSchedule, day string, slots []Slot) bool {
	for i := range ws.Days {
		if ws.Days[i].Day == day {
			ws.Days[i].Slots = slots
			return true
		}
	}
	return false
}

// ——— 夜间私聊的场景键 ———

// TestNightPeersFromCast_HaveDistinctSceneKeys
//
// ⚠️ 反面用例比正面重要: 断言的是「两个配角的私聊场景键**不相等**」,
// 而不是「场景个数是 2」—— 后者在两边都塌成 night:dm:0 时**仍然为真**。
func TestNightPeersFromCast_HaveDistinctSceneKeys(t *testing.T) {
	b := WorldBible{Cast: []CastMember{
		{Name: "德克萨斯", Relation: RelationCompanion, Status: "在仓库点货"},
		{Name: "可颂", Relation: RelationCompanion, Status: "在算这个月的账"},
	}}
	peers := NightPeersFromCast(b)
	if len(peers) != 2 {
		t.Fatalf("配角数不对: %d", len(peers))
	}
	byUIN := map[int64]string{}
	for _, p := range peers {
		if p.UIN == 0 {
			t.Errorf("配角 %q 的 UIN 是 0 —— 所有私聊会塌成同一个场景键", p.Name)
		}
		if p.UIN > 0 {
			t.Errorf("配角 %q 的 UIN 是正数 %d —— 会和真人 QQ 号撞", p.Name, p.UIN)
		}
		if prev, dup := byUIN[p.UIN]; dup {
			t.Errorf("%q 与 %q 撞了同一个 UIN %d", p.Name, prev, p.UIN)
		}
		byUIN[p.UIN] = p.Name
	}
	seen := map[string]bool{}
	for _, s := range nightSessions(peers) {
		if s.Kind != NightSessionDM {
			continue
		}
		if seen[s.Scene] {
			t.Errorf("两个私聊用了同一个场景键 %q —— §3.7.2「成对私密」在存储层失效", s.Scene)
		}
		seen[s.Scene] = true
	}
	if len(seen) != 2 {
		t.Fatalf("应有 2 个**不同**的私聊场景, 实得 %d", len(seen))
	}
	// 稳定性: 同名的两次调用必须同号 —— 重启后换号会把同一段私聊切成两半, 而且是静默的。
	if a, c := castUIN("德克萨斯"), castUIN("德克萨斯"); a != c {
		t.Fatalf("同一个名字两次算出不同 UIN: %d vs %d", a, c)
	}
	if castUIN("德克萨斯") == castUIN("可颂") {
		t.Fatal("两个不同的名字撞了 UIN")
	}
	if castUIN(" 可颂 ") != castUIN("可颂") {
		t.Fatal("首尾空格不该改变 UIN")
	}
}

// ——— 提示词: 拼接符与时间注入 ———

// TestSchedulePromptsCarryNowLine
//
// 正面: 两条提示词必须**真的带上当前时间**。
// 修之前它们是 `...JSON。 +` / `) + NowLine()` —— NowLine() 从未被调用, 提示词里只有一堆残骸。
func TestSchedulePromptsCarryNowLine(t *testing.T) {
	p := BuildProfile(&ProfileAnswers{
		Name: "阿米娅", Identity: "student", Template: "bestie",
		Q1: "medium", Q2: "bestie", Q3: "daily",
	})
	now := NowLine()
	for name, s := range map[string]string{"schedulePrompt": schedulePrompt(p), "demoPrompt": demoPrompt(p)} {
		if !strings.Contains(s, now) {
			t.Errorf("%s 里没有当前时间 —— NowLine() 没被调用", name)
		}
		if strings.Contains(s, ") + NowLine()") {
			t.Errorf("%s 里躺着字面的 \") + NowLine()\"", name)
		}
	}
	if now == "" {
		t.Fatal("NowLine() 返回空串 —— 这条测试会变成恒真")
	}
}
