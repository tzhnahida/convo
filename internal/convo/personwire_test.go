package convo

import (
	"testing"
)

// ─────────────────────────────────────────────────────────────
// §5.4「记忆挂在**人**身上(谁说的、关于谁)」
//
// ⚠️ 唯一的主要记忆生产路径是 `SideEffects` 的 remember 分支,
// 而它只写**旧的单场景表** `convo_memory` —— 于是 §5.4 的"归属"
// 目前**只有 scene_key, 没有 uin**: `convo_memory_person` 零行。
//
// ⚠️ 而这条链的消费者(`PersonMemories` / `PersonGlobalFacts`)**早就接好了** ——
// 又是"有调用者、数据源是空的"那副面孔。
// ─────────────────────────────────────────────────────────────

func personWireEngine(t *testing.T) (*DialogEngine, *Store) {
	t.Helper()
	s := newTestStore(t)
	if err := EnsureSceneSchema(s); err != nil {
		t.Fatal(err)
	}
	return &DialogEngine{Store: s}, s
}

func rememberFrame(v string) OutputFrame {
	return OutputFrame{Remember: &FrameRemember{K: "work", V: v, Label: "职业"}}
}

// TestPersonMemory_RememberLandsOnTheSpeaker
//
// ⚠️ **接线尺子**: 群场景里 remember 一条 → 必须能按**当前发言人**取到;
// 而**换一个人取不到** —— 那是 §5.4 的"跨人转述是绝对红线"。
func TestPersonMemory_RememberLandsOnTheSpeaker(t *testing.T) {
	d, s := personWireEngine(t)
	in := DialogInput{SceneKey: "group:9001", SpeakerUIN: "10001"}
	d.SideEffects(rememberFrame("甲是个牙医"), in, nil)

	a, err := s.PersonMemories("group:9001", "10001")
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 1 {
		t.Fatalf("**remember 没有落到人身上** —— convo_memory_person 还是零行: %d", len(a))
	}
	if a[0].V != "甲是个牙医" {
		t.Fatalf("内容不对: %q", a[0].V)
	}
	// ⚠️ 别人**取不到**。
	b, err := s.PersonMemories("group:9001", "10002")
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 0 {
		t.Fatalf("**甲的记忆被别人取到了** —— 跨人转述红线: %+v", b)
	}
}

// TestPersonMemory_NoSpeakerMeansNoPersonRow (反面, 而且它每天都跑)
//
// ⚠️ 私聊**没有 SpeakerUIN**, 而 `SavePersonMemory` 在 uin 为空时返回 error。
// 不判就调的话, 每一轮私聊都会写一行 Warn —— 噪音, 而且它会把真问题淹掉。
func TestPersonMemory_NoSpeakerMeansNoPersonRow(t *testing.T) {
	d, s := personWireEngine(t)
	// 私聊场景: SceneKey 是私聊, SpeakerUIN 为空。
	d.SideEffects(rememberFrame("它自己喜欢喝奶茶"),
		DialogInput{SceneKey: DefaultSceneKey}, nil)
	// 旧表那边照常写(那条路不该被改动) —— 直接数行, 别绕检索层
	// (第一版我用了 TopMemories, 而它的过滤不是我想的那样, 白红了一次)。
	var old int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM convo_memory").Scan(&old); err != nil {
		t.Fatal(err)
	}
	if old == 0 {
		t.Fatal("旧表那条路被改坏了 —— 它本来应当照常写")
	}
	// 而新表**一行都不该有**(它没有主体)。
	var n int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM convo_memory_person").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("没有发言人的 remember 也写进了关系层: %d 行", n)
	}
}

// TestPersonMemory_GlobalFactStaysFalse
//
// ⚠️ **零值 = 未提供 ≠ 稳定事实。**
// `FrameRemember` 里**没有**"这是稳定事实"的标志 —— 所以它必须恒为 false。
// 默认成 true 等于把每一句闲聊都升级成"这个人的稳定事实",
// 而那个东西是**只在他本人在场时**才可用的(R7), 门槛高得多。
func TestPersonMemory_GlobalFactStaysFalse(t *testing.T) {
	d, s := personWireEngine(t)
	d.SideEffects(rememberFrame("甲今天吃了拉面"),
		DialogInput{SceneKey: "group:9001", SpeakerUIN: "10001"}, nil)
	g, err := s.PersonGlobalFacts("10001")
	if err != nil {
		t.Fatal(err)
	}
	if len(g) != 0 {
		t.Fatalf("**一句闲聊被当成了稳定事实**: %+v —— 那会把 R7 的门槛悄悄放低", g)
	}
}
