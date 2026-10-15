package convo

import (
	"strings"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────
// #13 角色模式下身份骨架
//
// 「事件池出「宿舍」「食堂」, 而角色是须弥的小吉祥草王。」
// ─────────────────────────────────────────────────────────────

// 学生档的**招牌词** —— 角色档里一个都不许出现。
var studentSignatureWords = []string{"宿舍", "食堂", "早课", "图书馆", "室友", "同学", "小组作业", "点名", "教学楼", "选修"}

func TestBuildRoleProfile_SwitchesTheIdentitySkeleton(t *testing.T) {
	p := BuildRoleProfile(&ProfileAnswers{Q1: "daily", Q3: "daily", Template: "bestie"}, RoleCard{
		Name: "纳西妲", Series: "原神", Desc: "须弥的小吉祥草王",
	})
	if p.Identity != IdentityRole {
		t.Fatalf("角色卡建出来的人格身份应当是 %q, 实得 %q —— 它会继续过学生的日子", IdentityRole, p.Identity)
	}
	if _, ok := identityLabels[p.Identity]; !ok {
		t.Errorf("身份档 %q 没有标签", p.Identity)
	}
	// 反面锚点: **没给角色卡**时不该被改成 role (问卷路径保持原样)。
	q := BuildProfile(&ProfileAnswers{Q1: "daily", Q3: "daily", Template: "bestie"})
	if q.Identity == IdentityRole {
		t.Error("没有角色卡却切成了角色档 —— 那会把学生/上班族的地道素材一起关掉")
	}
}

// TestEventPoolForARoleNeverSpeaksOfDormsOrCanteens
//
// ⚠️ **这条是这一项的立案 bug。**
// 身份骨架不跟着角色走的话, 事件池会照出「早课点名」「图书馆」「室友打游戏」——
// 而那个角色的设定可能是"须弥的小吉祥草王"。
// **一个人的日常素材和它是谁对不上, 是这一层最刺眼的假。**
func TestEventPoolForARoleNeverSpeaksOfDormsOrCanteens(t *testing.T) {
	p := &PersonaProfile{Name: "纳西妲", Identity: IdentityRole}
	env := EnvDay{City: "须弥", Weather: "晴", TempC: 24}
	hits := 0
	base := time.Date(2026, 10, 15, 12, 0, 0, 0, time.Local)
	for i := 0; i < 40; i++ {
		for _, ev := range EventGenFallbackPool(env, p, base.AddDate(0, 0, i)) {
			texts := ev.Text + " " + ev.Source
			for _, w := range studentSignatureWords {
				if strings.Contains(texts, w) {
					hits++
					t.Errorf("角色档的事件池里出现了学生档的词 %q: %s", w, ev.Text)
				}
			}
		}
	}
	if hits > 0 {
		t.Fatalf("四十天里命中 %d 次 —— 身份骨架没跟着角色走", hits)
	}
	// ⚠️ 反面锚点: **学生档必须仍然有**这些素材 ——
	// 修 #13 不该把正常身份的地道素材一起关掉(那是最容易犯的"修一个坏一个")。
	sp := &PersonaProfile{Name: "小夜", Identity: "student"}
	found := false
	for i := 0; i < 40 && !found; i++ {
		for _, ev := range EventGenFallbackPool(env, sp, base.AddDate(0, 0, i)) {
			for _, w := range studentSignatureWords {
				if strings.Contains(ev.Text, w) {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("学生档的招牌素材不见了 —— 修 #13 把正常那一档也关掉了")
	}
}

// TestStateLabel_RoleIsNeitherSchoolNorOffice
func TestStateLabel_RoleIsNeitherSchoolNorOffice(t *testing.T) {
	got := eventGenStateLabel(StateFocus, IdentityRole)
	for _, bad := range []string{"上课", "上班"} {
		if strings.Contains(got, bad) {
			t.Errorf("角色档的 focus 说成了 %q —— 那正是 #13 的病", got)
		}
	}
	if strings.TrimSpace(got) == "" {
		t.Error("角色档的 focus 没有标签")
	}
	// 反面锚点: 另外两档不许被改动。
	if eventGenStateLabel(StateFocus, "office_worker") != "上班" {
		t.Error("上班族那档被改了")
	}
	if eventGenStateLabel(StateFocus, "student") != "上课" {
		t.Error("学生那档被改了")
	}
}

// TestBuiltinSchedule_RoleFallbackIsNeutral
//
// ⚠️ 角色档的兜底日程不许落到学生作息(09:00-17:30 focus = 上课)。
// 全 free 意味着"随时可以说话"—— 比"假装它在上课"好得多(失败往正常说话倒)。
func TestBuiltinSchedule_RoleFallbackIsNeutral(t *testing.T) {
	ws := BuiltinScheduleJSON(IdentityRole)
	if ws == nil {
		t.Fatal("角色档没有兜底日程")
	}
	if err := ValidateWeekSchedule(ws); err != nil {
		t.Fatalf("角色档的兜底日程不合法: %v", err)
	}
	for _, d := range ws.Days {
		for _, sl := range d.Slots {
			if sl.State == StateFocus {
				t.Fatalf("角色档的兜底日程里有 focus 槽 (%s %s-%s) —— 那等于替它编了一份作息", d.Day, sl.Start, sl.End)
			}
		}
	}
	// 反面锚点: 学生档必须仍然是 focus 作息。
	s := BuiltinScheduleJSON("student")
	hasFocus := false
	for _, d := range s.Days {
		for _, sl := range d.Slots {
			if sl.State == StateFocus {
				hasFocus = true
			}
		}
	}
	if !hasFocus {
		t.Error("学生档的 focus 作息被改掉了")
	}
}
