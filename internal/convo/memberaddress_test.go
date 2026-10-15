package convo

import (
	"context"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────
// §6.10.4 **称呼按场景各存一份** —— 设计稿 L1298 逐字:
//
// 「**称呼与禁区** | 按场景: 在 A 群叫外号, 在 B 群叫全名, **两边不许串**」
//
// ⚠️ 而 `address_as` 这一列在此之前**只有写者(且没接)、没有读者** ✗ ——
// 它被 Scan 进结构体之后就**被丢掉了** ✓。
// ⇒ 所以这条尺子钉的是**整条链**: 库里那一列 → 真的进提示词 → 且不串场景。
// ─────────────────────────────────────────────────────────────

// TestMemberAddress_PerSceneNoCrossTalk (本条最重要)
func TestMemberAddress_PerSceneNoCrossTalk(t *testing.T) {
	e, cap, sceneA := crossPersonEngine(t)
	sceneB := "group:9902"
	if err := e.Store.EnsureScene(sceneB); err != nil {
		t.Fatal(err)
	}
	// 同一个人在两个群里、两个称呼。
	for _, sc := range []string{sceneA, sceneB} {
		if _, err := e.Store.SavePersonMemory(PersonMemory{
			MemID: "p-" + sc, UIN: xbUIN, SceneKey: sc, K: "work", V: "牙医", Label: "职业",
		}); err != nil {
			t.Fatal(err)
		}
	}
	// ⚠️ 先把成员**登记出来** —— `SetMemberAddress` 的注释写着
	// 「先 ObserveMember 再写沉淀字段」✓, 而且它**报错**而不是静默不写 ✓。
	for _, sc := range []string{sceneA, sceneB} {
		if _, err := e.Store.ObserveMember(sc, xbUIN, "缇", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Store.SetMemberAddress(sceneA, xbUIN, "老缇"); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.SetMemberAddress(sceneB, xbUIN, "缇先生"); err != nil {
		t.Fatal(err)
	}

	// A 群那一轮: 只许出现 A 的称呼。
	if err := e.SpeakInGroupSeeing(context.Background(), sceneA, 9901, xbUIN, "在吗", ""); err != nil {
		t.Fatal(err)
	}
	aSys := cap.lastSystem
	if !strings.Contains(aSys, "老缇") {
		t.Fatal("**A 群的称呼没进提示词** —— 那一列还是被丢掉的")
	}
	if strings.Contains(aSys, "缇先生") {
		t.Fatal("**两个群的称呼串了** —— 设计稿说「两边不许串」")
	}

	// 反过来也要成立。
	if err := e.SpeakInGroupSeeing(context.Background(), sceneB, 9902, xbUIN, "在吗", ""); err != nil {
		t.Fatal(err)
	}
	bSys := cap.lastSystem
	if !strings.Contains(bSys, "缇先生") {
		t.Fatal("B 群的称呼没进提示词")
	}
	if strings.Contains(bSys, "老缇") {
		t.Fatal("**串回 A 群了**")
	}
}

// TestMemberAddress_NoAddressAddsNothing (反面)
//
// ⚠️ **零值 = 未提供**: 没设称呼时那一行**一个字都不该出现** ✓。
func TestMemberAddress_NoAddressAddsNothing(t *testing.T) {
	got := PersonNoteLine("小明", "", "10001", nil, nil)
	if got != "" {
		t.Fatalf("没记忆、没称呼时应当是空串, 实得 %q", got)
	}
	// ⚠️ 而**只有称呼、没有记忆**时, 它必须仍然渲染出来 ——
	// "他叫什么"与"我知道他什么"是两件事 ✓。
	only := PersonNoteLine("小明", "老缇", "10001", nil, nil)
	if !strings.Contains(only, "老缇") {
		t.Fatal("只有称呼时那一句没渲染 —— 那「叫他什么」就只在有记忆时才生效")
	}
}
