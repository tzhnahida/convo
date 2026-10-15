package convo

import (
	"strings"
	"testing"
)

// ⚠️ **这一条就是线上那个悬空指针** (2026-10-14)。
//
// 同一张图连发两次, 而窗口里**根本不含**它的完整标记 ——
// 去重之后必须仍然留着完整内容, 否则模型只能回
// 「你发的那图我这边看不清内容」(原话)。
func TestCollapse_FirstInWindowStaysFull(t *testing.T) {
	full := "(照片) [图: 一张恶搞喜茶联名的海报]"
	lines := []string{"群友1234: " + full, "群友5678: " + full}
	got := CollapseRepeatedImageMarks(lines)
	if !strings.Contains(got[0], "恶搞喜茶联名") {
		t.Fatalf("窗口里第一次必须留完整内容: %q", got[0])
	}
	if strings.Contains(got[1], "恶搞喜茶联名") {
		t.Fatalf("第二次该换成指针: %q", got[1])
	}
	if !strings.Contains(got[1], "往上找") {
		t.Fatalf("第二次要指向前面: %q", got[1])
	}
	// ⚠️ 短标要留着, 而且**括号不能多也不能少** ——
	// 我第一版把开头的 `(` 留在了行里, 渲染成 `([图: ...](照片)`。
	if !strings.Contains(got[1], "](照片)") {
		t.Fatalf("引用式后半截应是 (照片): %q", got[1])
	}
	if strings.Contains(got[1], "([图:") {
		t.Fatalf("多了一个左括号 —— 替换时漏掉了它: %q", got[1])
	}
}

// 反面用例: **窗口里没有完整那份**时, 谁都不许被换成指针。
//
// 这是这次修复最要紧的反例 —— 就是它把"悬空指针"钉死在门外。
func TestCollapse_OnlyOneOccurrenceStaysFull(t *testing.T) {
	line := "群友1234: (表情 — 嘲讽) [图: 表情 — 嘲讽 厌 「许多猪」 — 骂一群人是猪]"
	got := CollapseRepeatedImageMarks([]string{line})
	if got[0] != line {
		t.Fatalf("只出现一次不该动它:\n  want %q\n  got  %q", line, got[0])
	}
}

// 不同的图**不许**被互相去重 —— 两条不同的完整标记各自都是第一次。
func TestCollapse_DifferentImagesBothStayFull(t *testing.T) {
	a := "群友1: (照片) [图: 一只猫]"
	b := "群友2: (照片) [图: 一只狗]"
	got := CollapseRepeatedImageMarks([]string{a, b})
	if got[0] != a || got[1] != b {
		t.Fatalf("不同的图被误判成同一张:\n  %q\n  %q", got[0], got[1])
	}
}

// 第 3、4、5 次也都是指针, 且只有**第一次**是完整的。
func TestCollapse_ThirdAndLaterAlsoReference(t *testing.T) {
	full := "(表情 — 附和) [图: 表情 — 附和 喜 「好耶」 — 表示赞同]"
	lines := make([]string, 5)
	for i := range lines {
		lines[i] = "群友9: " + full
	}
	got := CollapseRepeatedImageMarks(lines)
	if !strings.Contains(got[0], "好耶") {
		t.Fatalf("第一次要完整: %q", got[0])
	}
	for i := 1; i < 5; i++ {
		if strings.Contains(got[i], "好耶") {
			t.Fatalf("第 %d 次不该铺内容: %q", i+1, got[i])
		}
	}
}

// 非图片行一律原样 —— **尤其是纯文本重复**, 那必须留着。
func TestCollapse_TextLinesUntouched(t *testing.T) {
	lines := []string{"群友1: 草", "群友2: 草", "群友1: 又是一张 (照片) [图: 猫]"}
	got := CollapseRepeatedImageMarks(lines)
	if got[0] != lines[0] || got[1] != lines[1] {
		t.Fatalf("重复的纯文本不该被折叠: %q / %q", got[0], got[1])
	}
	if got[2] != lines[2] {
		t.Fatalf("第一次出现的图必须完整: %q", got[2])
	}
}

// 标记后面**还跟着别的字** → 认不准哪一段是完整标记 → **不折叠**。
//
// ⚠️ 这是有意的"什么都不做", 不是漏了 —— 折叠的前提是切得准,
// 切不准时唯一安全的动作是原样保留 (多花几个字, 但句子是完整的)。
func TestCollapse_TrailingTextStaysFull(t *testing.T) {
	full := "(照片) [图: 一张截图]"
	lines := []string{"群友1: " + full, "群友2: 这是谁 " + full + " 帮我看看"}
	got := CollapseRepeatedImageMarks(lines)
	if got[1] != lines[1] {
		t.Fatalf("认不准时该原样保留:\n  want %q\n  got  %q", lines[1], got[1])
	}
}

// 标记后面只有空白 → 仍然算行尾, 照常折叠 (真实 payload 就是这个形状:
// 群里是 `文字 + " " + 标记`, 标记永远在最后)。
func TestCollapse_TrailingSpaceStillCollapses(t *testing.T) {
	full := "(照片) [图: 一张截图]"
	lines := []string{"群友1: " + full, "群友2: 这是谁 " + full + " "}
	got := CollapseRepeatedImageMarks(lines)
	if strings.Contains(got[1], "一张截图") {
		t.Fatalf("行尾只有空白时应当折叠: %q", got[1])
	}
	if !strings.HasPrefix(got[1], "群友2: 这是谁 ") {
		t.Fatalf("前面的文字被吃了: %q", got[1])
	}
}

// 完整标记里自带 `]` (LLM 写含义时真会出现) —— 贪婪 + 行尾锚定必须
// 咬到**最后一个** `]`, 而不是切一半。
//
// ⚠️ 这是我第一版的真实 bug: 用 `[^\]]*` 时这里匹配成
// `[图: 一张带 [方括号]`, 替换后行尾剩一句 ` 的图]` —— 句子被切坏。
func TestCollapse_MarkWithInnerBracketIsCutWhole(t *testing.T) {
	line := "群友1: (照片) [图: 一张带 [方括号] 的图]"
	got := CollapseRepeatedImageMarks([]string{line, line})
	if got[0] != line {
		t.Fatalf("第一次必须原样: %q", got[0])
	}
	if strings.Contains(got[1], "方括号") {
		t.Fatalf("第二次该整体换成指针: %q", got[1])
	}
	if strings.Contains(got[1], "的图]") {
		t.Fatalf("切剩了残渣 —— 正则只咬了半截: %q", got[1])
	}
}

// ⚠️ **一行里有两个方括号段** → 贪婪正则会咬过头 → 必须整体放弃。
//
// `(照片) [图: 猫] 然后 [这样]` 若被整段吃掉, ` 然后 [这样]` 就
// **静默消失**了 —— 丢字比不去重严重得多。
func TestCollapse_TwoBracketGroupsStaysFull(t *testing.T) {
	line := "群友1: (照片) [图: 猫] 然后 [这样]"
	got := CollapseRepeatedImageMarks([]string{line, line})
	if got[1] != line {
		t.Fatalf("可能有第二个方括号段时必须原样保留: %q", got[1])
	}
	if _, _, _, ok := ImageMarkSpan(line); ok {
		t.Fatal("这种行不该被认成可折叠的图片标记")
	}
}

// 入参不许被改 —— 调用方可能还在用那一份。
func TestCollapse_DoesNotMutateInput(t *testing.T) {
	full := "(照片) [图: 猫]"
	lines := []string{"a: " + full, "b: " + full}
	_ = CollapseRepeatedImageMarks(lines)
	if lines[1] != "b: "+full {
		t.Fatalf("入参被改了: %q", lines[1])
	}
}

// 短标必须和首次那份**逐字节一致**, 否则"往上找"对不上。
func TestImageMarkSpan_ShortTagMatchesFirstRender(t *testing.T) {
	l := ImageLabel{Kind: "photo", Meaning: "一只猫"}
	first := l.RenderSeen(1)
	shortTag, full, whole, ok := ImageMarkSpan("x: " + first)
	if !ok {
		t.Fatalf("认不出首次渲染: %q", first)
	}
	if whole != first {
		t.Fatalf("整段应该等于首次渲染本身: %q vs %q", whole, first)
	}
	if shortTag != "照片" {
		t.Fatalf("短标应为 照片, 实际 %q", shortTag)
	}
	if full != "[图: 一只猫]" {
		t.Fatalf("完整标记应为 [图: 一只猫], 实际 %q", full)
	}
}
