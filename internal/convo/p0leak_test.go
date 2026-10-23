package convo

import (
	"strings"
	"testing"
)

// 2026-10-23 实测泄漏原文: 工具调用语法被降级路径原样发进群。
// 现在必须换成兜底文案 —— 内部协议**绝不**出现在聊天里。
func TestDegradeFrame_ProtoLeakBecomesFallback(t *testing.T) {
	raw := "<tool calls>\n<tool_name>web_search</｜｜DSML｜｜ parameter>"
	f := degradeFrame(raw)
	txt := FrameText(f)
	if strings.Contains(txt, "<") || strings.Contains(txt, "DSML") || strings.Contains(txt, "web_search") {
		t.Fatalf("协议泄漏仍被发出: %q", txt)
	}
	if !f.Degraded {
		t.Fatal("降级帧应当标 Degraded")
	}
	found := false
	for _, l := range fallbackLines {
		if txt == l {
			found = true
		}
	}
	if !found {
		t.Fatalf("应当是兜底文案, 得到: %q", txt)
	}
}

// 正常的人话降级仍然要发出去 (失败方向: 别把能力问题当违规打死)。
func TestDegradeFrame_KeepsNaturalText(t *testing.T) {
	raw := "我这边网卡了一下 重说"
	f := degradeFrame(raw)
	if got := FrameText(f); got != raw {
		t.Fatalf("正常人话被改动了: %q", got)
	}
	if !f.Degraded {
		t.Fatal("应当标 Degraded")
	}
}

// JSON 帧里夹带协议片段 → 整段降级成兜底, 不许把残片发出去。
func TestDegradeFrame_LeakInsideJSONFrame(t *testing.T) {
	raw := `{"reply":[{"kind":"text","text":"<tool_name>web_search</｜｜DSML｜｜>"}]}`
	got := FrameText(degradeFrame(raw))
	if strings.Contains(got, "tool") || strings.Contains(got, "DSML") {
		t.Fatalf("JSON 帧里的协议残片被发出: %q", got)
	}
}
