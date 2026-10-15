package convo

import (
	"context"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────
// §5.4 **跨人转述红线** —— 一整段扫描
//
// 设计稿: 「**A 在群里说话 → 只注入 A 的记忆;
//           B 的私事在上下文里根本不存在**」。
//
// ⚠️ §2.52 修的是**一个点**(`PersonNoteLine` 只取当前发言人)。
// 而这一条要求的是**整段提示词里都没有 B 的私事** ——
// 因为漏的点可以是任何一个: 记忆段、边界段、意图段、待求证段…
//
// ⚠️ 而它漏掉的表现是**它在群里把 B 的私事说给 A 听** ——
// 对 B 来说那是背刺, 而且**不可撤销**。
// ─────────────────────────────────────────────────────────────

// crossPersonEngine 造一个"群里有两个人, 其中一个人有私事"的局面。
func crossPersonEngine(t *testing.T) (*Engine, *sitCaptureChatter, string) {
	t.Helper()
	cap := &sitCaptureChatter{}
	e, _ := groupSpeakEngine(t, cap)
	if err := EnsureSceneSchema(e.Store); err != nil {
		t.Fatal(err)
	}
	if err := EnsureHeatSchema(e.Store); err != nil {
		t.Fatal(err)
	}
	if err := EnsureBondSchema(e.Store.DB); err != nil {
		t.Fatal(err)
	}
	scene := "group:9901"
	if err := e.Store.EnsureScene(scene); err != nil {
		t.Fatal(err)
	}
	return e, cap, scene
}

const (
	xaUIN = "2735604537"
	xbUIN = "351390754"
	// xSecret = B 的私事; xPublic = 这个群里**公开**的梗。
	xSecret = "在准备跳槽"
	xPublic = "群里都爱喝柠檬茶"
)

// TestCrossPerson_RedsLine (本条最重要)
//
// ⚠️ **双向都测**, 而且**每一向都有一个正向对照** ——
// 否则"没扫到"可能只是因为那一段一直是空的(一条永远绿的用例)。
func TestCrossPerson_RedsLine(t *testing.T) {
	e, cap, scene := crossPersonEngine(t)
	// B 的私事(只跟它说过)。
	if _, err := e.Store.SavePersonMemory(PersonMemory{
		MemID: "pb", UIN: xbUIN, SceneKey: scene, K: "work",
		V: xSecret, Label: "职业",
	}); err != nil {
		t.Fatal(err)
	}
	// 群公共的那条 —— 它是**对照**: 它必须在, 证明补全真的跑了。
	if err := e.Store.UpsertMemory(MemoryDelta{
		MemID: "gm", K: "topic", V: xPublic, Label: "群梗",
		Source: "user_said", SceneKey: scene, Confidence: 0.9,
	}, "active"); err != nil {
		t.Fatal(err)
	}

	// ——— A 说话: B 的私事**不许**出现, 而 B 自己的那次**必须**出现 ———
	if err := e.SpeakInGroupSeeing(context.Background(), scene, 9901, xaUIN, "你们在聊什么", ""); err != nil {
		t.Fatalf("A 那轮失败: %v", err)
	}
	aSys := cap.lastSystem
	if len(aSys) < 500 {
		t.Fatalf("A 那轮的提示词只有 %d 字节 —— 夹具没造起来", len(aSys))
	}
	if !strings.Contains(aSys, xPublic) {
		t.Fatal("对照失败: 群公共那条没进提示词 —— 那这一轮扫的是空的")
	}
	if strings.Contains(aSys, xSecret) {
		t.Errorf("**A 说话时 B 的私事进了上下文** —— 跨人转述红线:\n%s",
			lineWith(aSys, xSecret))
	}

	// ⚠️ 反向: **B 说话时, B 自己的私事必须在** ——
	// 否则上面那条"没出现"可能只是因为那条路根本不注入任何按人记忆。
	if err := e.SpeakInGroupSeeing(context.Background(), scene, 9901, xbUIN, "今天好累", ""); err != nil {
		t.Fatalf("B 那轮失败: %v", err)
	}
	bSys := cap.lastSystem
	if !strings.Contains(bSys, xSecret) {
		t.Fatal("**B 说话时它自己的私事没进上下文** —— 「只注入当前发言人」这条没生效",
			lineWith(bSys, xPublic))
	}
}

// lineWith 找出含某串的那一行(报错时给人看)。
func lineWith(s, sub string) string {
	for _, ln := range strings.Split(s, "\n") {
		if strings.Contains(ln, sub) {
			return ln
		}
	}
	return "(没找到含该串的行)"
}
