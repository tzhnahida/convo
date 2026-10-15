package convo

import (
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────
// §6.10.3 **主体分区注入**: 只注入当前发言人的关系记忆 + 群公共记忆
//
//   设计稿: 「A 在群里说话 → 只注入 A 的记忆;
//            **B 的私事在上下文里根本不存在**」
//
// ⚠️ 而 `DialogInput.SpeakerUIN` 原来**一路传到注入层就蒸发了** ——
// `buildSystem` 一个字节都没读它, 于是段 5 装的是**按场景**取的记忆
// (整个群的), 不是"关于这个人"的。
// ─────────────────────────────────────────────────────────────

// TestPersonNote_OnlyTheSpeaker (本条最重要)
//
// ⚠️ 这是 **§5.4 跨人转述红线的注入侧**:
// 存储侧由 `SavePersonMemory` 分区保证, 而"取的时候只取对的人"要靠这里。
func TestPersonNote_OnlyTheSpeaker(t *testing.T) {
	s := newTestStore(t)
	if err := EnsureSceneSchema(s); err != nil {
		t.Fatal(err)
	}
	scene := "group:6001"
	mk := func(uin, v string) {
		if ok, err := s.SavePersonMemory(PersonMemory{
			MemID: "m-" + uin, UIN: uin, SceneKey: scene, K: "work", V: v, Label: "职业",
		}); err != nil || !ok {
			t.Fatalf("写入失败: ok=%v err=%v", ok, err)
		}
	}
	mk("10001", "甲是个牙医")
	mk("10002", "乙是个护士")

	// ⚠️ 直接测渲染器 —— `dialogInput` 那条路要一个完整的 Engine, 而
	// 这条不变量(谁的在、谁的不在)**在渲染器上就能钉死**。
	a, _ := s.PersonMemories(scene, "10001")
	note := PersonNoteLine("", "", "10001", a, nil)
	if !strings.Contains(note, "甲是个牙医") {
		t.Fatal("当前发言人的记忆没进注入")
	}
	if strings.Contains(note, "乙是个护士") {
		t.Fatalf("**别人在这个群里的事进了上下文** —— 跨人转述红线:\n%s", note)
	}

	// ⚠️ 而**从库里按另一个人取**时, 那条也不该出现(证明分区是真的)。
	b, _ := s.PersonMemories(scene, "10002")
	if len(b) != 1 || strings.Contains(b[0].V, "牙医") {
		t.Fatalf("按 10002 取到了 10001 的记忆: %+v", b)
	}
}

// TestPersonNote_ZeroValuesAddNothing (反面)
//
// ⚠️ 空段会白占 token, 而且会让模型以为"关于他什么都不知道"是条信息。
func TestPersonNote_ZeroValuesAddNothing(t *testing.T) {
	if got := PersonNoteLine("小明", "", "10001", nil, nil); got != "" {
		t.Fatalf("没有记忆时不应当是空串以外的东西: %q", got)
	}
	if got := PersonNoteLine("", "", "", nil, nil); got != "" {
		t.Fatalf("全零时不应当是空串以外的东西: %q", got)
	}
}

// TestPersonNote_NoOwnerMarkerAndNoFullUIN (§6.10.7)
//
// ⚠️ 「**owner 标记绝不进注入**」 —— 把主人标进去等于在群里公示。
// 与段 7 的发言流同一套锚: **只用 uin 尾号**。
func TestPersonNote_NoOwnerMarkerAndNoFullUIN(t *testing.T) {
	note := PersonNoteLine("纳西妲", "", "3487037837", []PersonMemory{
		{MemID: "m1", Label: "职业", V: "牙医"},
	}, nil)
	if !strings.Contains(note, "7837") {
		t.Fatal("尾号没进标题 —— 那两个人同名就分不开了")
	}
	if strings.Contains(note, "3487037837") {
		t.Fatal("**完整 uin 进了提示词** —— 锚应当只用尾号")
	}
	for _, bad := range []string{"owner", "主人", "管理员", "admin"} {
		if strings.Contains(strings.ToLower(note), strings.ToLower(bad)) {
			t.Fatalf("注入里出现了特权标记 %q:\n%s", bad, note)
		}
	}
}

// TestPersonNote_StableFactsAreSeparate
//
// ⚠️ §5.6「全局级只放**这个人的稳定事实**」—— 它与关系记忆**取舍不同**:
// 前者是"他是个什么样的人", 后者是"关于他的一件事"。
func TestPersonNote_StableFactsAreSeparate(t *testing.T) {
	mems := []PersonMemory{{MemID: "m1", Label: "职业", V: "牙医"}}
	facts := []PersonMemory{{MemID: "f1", Label: "城市", V: "上海"}}
	note := PersonNoteLine("小明", "", "10001", mems, facts)
	if !strings.Contains(note, "牙医") || !strings.Contains(note, "上海") {
		t.Fatalf("两块都要在:\n%s", note)
	}
	if !strings.Contains(note, "稳定事实") {
		t.Fatal("稳定事实没有与关系记忆分开标 —— 那它们读起来就是同一批东西")
	}
}

// TestPersonNote_ReachesThePrompt (接线尺子)
//
// ⚠️ 光有渲染器不算数 —— **它必须真的进提示词**。
func TestPersonNote_ReachesThePrompt(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜"}
	base := d.buildSystem(DialogInput{UserText: "在吗", SceneKey: "group:1"}, p, "ja")
	if strings.Contains(base, "关于**这一轮说话的人**") {
		t.Fatal("没给 PersonNote 却渲染出了那一块")
	}
	with := d.buildSystem(DialogInput{
		UserText: "在吗", SceneKey: "group:1",
		PersonNote: PersonNoteLine("小明", "", "10001", []PersonMemory{{MemID: "m1", Label: "职业", V: "牙医"}}, nil),
	}, p, "ja")
	if !strings.Contains(with, "牙医") {
		t.Fatal("**PersonNote 没进提示词** —— 那条接线是空的")
	}
}
