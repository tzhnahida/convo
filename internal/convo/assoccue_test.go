package convo

import (
	"testing"
)

// TestBigramCues_RejectsPunctuationAndSpace
//
// ⚠️ **这条尺子的夹具来自生产数据。**
//
// 2026-10-15 的决策轨迹里抓到一条真实的反例:
//
//	【联想】刚才提到了「:」，你由此想起：纳西妲: 明天见，记得给我留一颗呀。。
//
// 线索是一个**冒号** —— 匹配上的那条记忆里也恰好有个冒号。
// 那不是联想, 是巧合; 而冒号在对话里到处都是, 于是它每一轮都可能发生。
//
// ⚠️ 单字停用词表挡得住「我」「的」, 却挡不住一个两字标点对。
func TestBigramCues_RejectsPunctuationAndSpace(t *testing.T) {
	cues := BigramCues("纳西妲: 明天见，记得给我留一颗呀。", 0)
	if len(cues) == 0 {
		t.Fatal("整句都抽不出线索 —— 那判据太严了")
	}
	for _, c := range cues {
		r := []rune(c.Token)
		if len(r) != 2 {
			t.Fatalf("线索长度不对: %q", c.Token)
		}
		if !assocCueRune(r[0]) || !assocCueRune(r[1]) {
			t.Fatalf("线索里有标点或空白: %q", c.Token)
		}
	}
	// 而它必须抽到**真正的内容词**。
	found := false
	for _, c := range cues {
		if c.Token == "明天" || c.Token == "记得" {
			found = true
		}
	}
	if !found {
		t.Fatalf("真正的内容词没被抽出来: %v", cues)
	}
}

// TestAssocNote_PunctuationAloneIsNotAnAssociation
//
// ⚠️ 端到端那一面: 两条**只共享标点**的记忆与文本, 不该产生联想。
func TestAssocNote_PunctuationAloneIsNotAnAssociation(t *testing.T) {
	e := assocEngine(t)
	scene := DefaultSceneKey
	if err := e.Store.UpsertMemory(MemoryDelta{
		MemID: "m-punct", K: "fact", V: "纳西妲: 明天见", Label: "x",
		Confidence: 0.8, Source: "user_said", Sensitivity: SensitivityPublic,
	}, "active"); err != nil {
		t.Fatal(err)
	}
	// 这句话与那条记忆**只共享一个冒号**(内容词完全不同)。
	if got := e.assocNote(scene, "派蒙: 早上好"); got != "" {
		t.Fatalf("只共享一个冒号就联想了 —— 那不是联想是巧合:\n%s", got)
	}
	// ⚠️ 反面锚点: **真的共享内容词时仍然要联想到** ——
	// 修这个洞不许把整条路关掉。
	if got := e.assocNote(scene, "你明天见谁呀"); got == "" {
		t.Fatal("共享了「明天」却没有联想 —— 洞修过头了")
	}
}
