package convo

import "testing"

// ⚠️ **层决定归属** (2026-10-14 用户定规):
//
//	群 (公开层) 说过的话 —— 大家都听见了, **可以互相说、互相引用**;
//	私聊 (成对私密) —— 只进当事双方的私事, 别人看不见。
func TestNightSessionKind_IsPublic(t *testing.T) {
	if !NightSessionGroup.IsPublic() {
		t.Fatal("群是公开层")
	}
	if NightSessionDM.IsPublic() {
		t.Fatal("私聊不是公开层 —— 这是隔离私域那条硬线")
	}
	// 未知值一律按**私密**处理 (fail-closed): 不认识的东西不该被当成可以公开引用。
	if NightSessionKind("").IsPublic() || NightSessionKind("weird").IsPublic() {
		t.Fatal("未知层必须 fail-closed 到私密")
	}
}

// 产物必须**带层标签** —— 否则下游分不出"群里说的"和"私下说的",
// 而那个区别正是"能不能被别的 AI 引用"的唯一依据。
func TestNightWriteBack_TagsLayer(t *testing.T) {
	res := NightSocialResult{Turns: []NightTurn{
		{SceneKey: "group:1", Kind: NightSessionGroup, Sender: "散兵", Text: "在吗"},
		{SceneKey: "group:1", Kind: NightSessionGroup, Sender: "小夜", Text: "在"},
		{SceneKey: "dm:2", Kind: NightSessionDM, Sender: "兰那罗", Text: "悄悄话"},
		{SceneKey: "dm:2", Kind: NightSessionDM, Sender: "小夜", Text: "我听着"},
	}}
	mems := NightWriteBack(res, "小夜", 6)
	if len(mems) == 0 {
		t.Fatal("应当写出记忆")
	}
	var pub, priv int
	for _, m := range mems {
		if m.SceneKey == "group:1" && !m.Public {
			t.Fatalf("群里的产物应当是公开的: %+v", m)
		}
		if m.SceneKey == "dm:2" && m.Public {
			t.Fatalf("私聊的产物被标成公开 —— 隔离私域破了: %+v", m)
		}
		if m.Public {
			pub++
		} else {
			priv++
		}
	}
	if pub == 0 || priv == 0 {
		t.Fatalf("两层都要有产物, got pub=%d priv=%d", pub, priv)
	}
}
