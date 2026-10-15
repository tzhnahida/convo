package convo

import (
	"strings"
	"testing"
	"time"
)

// ⚠️ **接线测试: 【你那边】必须真的进 system prompt。**
//
// EnvSource 此前只在仿真和测试里被赋值, 生产从没接过 —— 于是整段不存在。
// "函数能跑"和"它被接上了"是两件事, 这个项目里后者翻过好几次车
// (worldNote / maybeNews / EnvSource 都是同一个形状的 bug)。
func TestEnv_ReachesPromptWhenSourceConfigured(t *testing.T) {
	old := EnvSource
	EnvSource = func(time.Time) (EnvDay, bool) {
		return EnvDay{City: "龙门", Weather: "雨", TempC: 12,
			Events: []string{"附近 4.5 级地震"}}, true
	}
	defer func() { EnvSource = old }()

	sys := (&DialogEngine{}).buildSystem(DialogInput{UserText: "在吗"}, &PersonaProfile{Name: "小夜"}, "ja")
	for _, want := range []string{"你那边", "龙门", "雨", "12", "地震"} {
		if !strings.Contains(sys, want) {
			t.Fatalf("提示词里缺 %q —— 它的一天又是空的:\n%s", want, sys[:min(len(sys), 700)])
		}
	}
}

// 反面: 没有数据源 → **整段消失**, 不许留空骨架。
//
// ⚠️ 这条比上面那条重要: 空骨架会让模型以为"那边什么都没有",
// 于是它编一个天气出来 —— 那是凭空断言。
func TestEnv_NoSourceMeansNoSegment(t *testing.T) {
	old := EnvSource
	EnvSource = nil
	defer func() { EnvSource = old }()
	sys := (&DialogEngine{}).buildSystem(DialogInput{UserText: "在吗"}, &PersonaProfile{Name: "小夜"}, "ja")
	if strings.Contains(sys, "你那边") {
		t.Fatal("没有数据源却渲染出了【你那边】")
	}
}

// 取数失败 (ok=false) 同样不许渲染 —— 天气不知道 ≠ 今天没天气。
func TestEnv_FailingSourceMeansNoSegment(t *testing.T) {
	old := EnvSource
	EnvSource = func(time.Time) (EnvDay, bool) { return EnvDay{}, false }
	defer func() { EnvSource = old }()
	sys := (&DialogEngine{}).buildSystem(DialogInput{UserText: "在吗"}, &PersonaProfile{Name: "小夜"}, "ja")
	if strings.Contains(sys, "你那边") {
		t.Fatal("取数失败却渲染出了【你那边】—— 那是编的")
	}
}

// ⚠️ **【他们现在】必须用今天刷新过的日程** ——
// 那是"角色说自己的生活"的素材源, 停在生成时那一份就等于没有。
func TestCastStatus_RefreshedStatusReachesPrompt(t *testing.T) {
	b := WorldBible{
		Self: "小夜",
		Cast: []CastMember{{Name: "阿岚", Relation: RelationCompanion,
			Status: "在阳台修那盆快死的薄荷", StatusDay: "2026-10-14"}},
	}
	note := WorldPrompt(b)
	if !strings.Contains(note, "在阳台修那盆快死的薄荷") {
		t.Fatalf("【他们现在】没带上今天的日程:\n%s", note)
	}
	sys := (&DialogEngine{}).buildSystem(DialogInput{WorldNote: note, UserText: "在吗"},
		&PersonaProfile{Name: "小夜"}, "ja")
	if !strings.Contains(sys, "薄荷") {
		t.Fatalf("配角日程没进提示词 —— 它又只能回「就那样」了:\n%s", sys[:min(len(sys), 700)])
	}
}
