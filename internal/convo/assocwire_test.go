package convo

import (
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────
// §7.2 联想式检索的接线
//
// 设计稿：「两者都要, 但**联想式才是人味的来源**」——
// 而它原来**只接在群路径上**, 举的例子却是私聊那只猫。
// ─────────────────────────────────────────────────────────────

func assocEngine(t *testing.T) *Engine {
	t.Helper()
	return gateEngine(t)
}

// TestAssocNote_FiresOnSharedCues
//
// ⚠️ 判据是**表层线索共享**(bigram), 不是"语义相似" ——
// 这就是"没有线索不许联想"的形态。
func TestAssocNote_FiresOnSharedCues(t *testing.T) {
	e := assocEngine(t)
	scene := DefaultSceneKey
	if err := e.Store.UpsertMemory(MemoryDelta{
		MemID: "m-cat", K: "fact", V: "用户养的那只猫叫团子", Label: "猫",
		Confidence: 0.8, Source: "user_said", Sensitivity: SensitivityPublic,
	}, "active"); err != nil {
		t.Fatal(err)
	}
	line := e.assocNote(scene, "我今天路过宠物店看见一只猫")
	if line == "" {
		t.Fatal("共享了「猫」这条线索却没有联想 —— 那条路没接上")
	}
	if !strings.Contains(line, "团子") {
		t.Fatalf("联想出来的不是那条记忆:\n%s", line)
	}
}

// TestAssocNote_NoSharedCueMeansNoAssociation (反面, 而且它比正面重要)
//
// ⚠️ 没有共享线索 → **一个字都不加**。
// 联想最怕的不是想不起来, 是**硬联想** —— 那会让每一轮都插一句不相干的旧事。
func TestAssocNote_NoSharedCueMeansNoAssociation(t *testing.T) {
	e := assocEngine(t)
	scene := DefaultSceneKey
	if err := e.Store.UpsertMemory(MemoryDelta{
		MemID: "m-cat2", K: "fact", V: "用户养的那只猫叫团子", Label: "猫",
		Confidence: 0.8, Source: "user_said", Sensitivity: SensitivityPublic,
	}, "active"); err != nil {
		t.Fatal(err)
	}
	if got := e.assocNote(scene, "今天天气不错"); got != "" {
		t.Fatalf("毫不相干的一句话也联想出了东西:\n%s", got)
	}
	// 空文本不联想 (它连线索都没有)。
	if got := e.assocNote(scene, "   "); got != "" {
		t.Fatalf("空文本联想出了东西:\n%s", got)
	}
	// 没有任何记忆时不联想。
	if got := e.assocNote("private:nobody", "猫"); got != "" {
		t.Fatalf("没有记忆却说联想到了:\n%s", got)
	}
}

// TestAssocNote_IsSceneScoped (§7.3 维一)
//
// ⚠️ 措辞必须与记忆**共享一个 bigram** —— 第一版我写的"我今天看见一只猫"与“...那只猫...”
// 不共享任何二字片段, 于是"自己场景里也联想不出来"那条断言红了 —— **而那是判据在正确工作**。
//
// ⚠️ 别人场景的记忆**不许**被联想进来 ——
// 那正是 §7.3 说的"跨人转述", 而它的表现只是"它怎么知道那件事"。
func TestAssocNote_IsSceneScoped(t *testing.T) {
	e := assocEngine(t)
	// 一条**关系级**记忆, 绑在群场景上 (零值 sensitivity = relation = 绑场景)。
	if err := e.Store.UpsertMemory(MemoryDelta{
		MemID: "m-scoped", K: "fact", V: "群友养的那只猫叫团子", Label: "猫",
		Confidence: 0.9, Source: "user_said", SceneKey: "group:999",
	}, "active"); err != nil {
		t.Fatal(err)
	}
	if got := e.assocNote(DefaultSceneKey, "我今天路过宠物店看见一只猫"); got != "" {
		t.Fatalf("把别场景的关系记忆联想进来了:\n%s", got)
	}
	// 而在它自己的场景里可以。
	if got := e.assocNote("group:999", "我今天路过宠物店看见一只猫"); got == "" {
		t.Fatal("在它自己的场景里也联想不出来 —— 那这条接线是空的")
	}
}

// TestAssocNote_SharedFunctionCoversBothPaths
//
// ⚠️ **接线尺子**: 光测 assocNote 证明不了两条路都过它。
//
// 判据: `SpeakInGroupSeeing` 与 `dialogReply` 里都不再**各自内联**那段联想,
// 而是调同一个函数 —— 所以这里断的是"群路径那 6 行内联已经没了"。
func TestAssocNote_SharedFunctionCoversBothPaths(t *testing.T) {
	// 群路径: 它现在调 assocNote。
	if !strings.Contains(readFileForTest(t, "value.go"), "e.assocNote(sceneKey, text)") {
		t.Fatal("群路径没有调 assocNote —— 两条路又分叉了")
	}
	// 私聊(共享的 dialogReply): 它也必须有一处。
	if !strings.Contains(readFileForTest(t, "convo.go"), "e.assocNote(in.SceneKey, in.UserText)") {
		t.Fatal("dialogReply 里没有 assocNote —— 私聊那条路不会有联想")
	}
}
