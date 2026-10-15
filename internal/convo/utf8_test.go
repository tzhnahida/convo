package convo

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// ─────────────────────────────────────────────────────────────
// 注入文本必须是合法 UTF-8
//
// ⚠️ 这条尺子的夹具来自**生产事故**:
//
// 2026-10-15 我用 python 拉决策轨迹的段位清单, 而它在某一行上直接抛
// `Could not decode to UTF-8 column 'system_prompt'` ——
// 坏点是 `e8 82`: 一个三字节汉字**只剩前两字节**。
//
// 也就是说那条 prompt **根本不是合法 UTF-8**, 而它已经躺了一整天。
// 后果不是"模型看到乱码"(它大概会忽略), 而是
// **排障与回放的唯一入口在那一行上读不出来** —— 而那正是最需要它的时候。
// ─────────────────────────────────────────────────────────────

// TestSaveNews_SanitizesForeignBytes
//
// ⚠️ 搜索/网页来源不受我们控制, 它们可能给出半个字符。
func TestSaveNews_SanitizesForeignBytes(t *testing.T) {
	s := newTestStore(t)
	if err := EnsureNewsSchema(s.DB); err != nil {
		t.Fatal(err)
	}
	// 一个被截断的三字节汉字: e8 82 是「育」的前两字节。
	broken := "华尔街日报 —— 高利率冲击下" + string([]byte{0xe8, 0x82}) + "芯片"
	if utf8.ValidString(broken) != false {
		t.Fatal("夹具本身是合法 UTF-8 —— 那这条用例什么都没测")
	}
	if err := s.SaveNews("o", NewsItem{Title: "标题" + string([]byte{0xe8, 0x82}), Summary: broken}); err != nil {
		t.Fatal(err)
	}
	items, err := s.RecentNews("o", 5)
	if err != nil || len(items) != 1 {
		t.Fatalf("读回失败: n=%d err=%v", len(items), err)
	}
	if !utf8.ValidString(items[0].Title) || !utf8.ValidString(items[0].Summary) {
		t.Fatal("脏字节进了库 —— 它会跟着这条记录流向每一个读它的地方")
	}
	// 而**内容不能丢光**: 清洗是替换坏字节, 不是把整段扔掉。
	if !strings.Contains(items[0].Summary, "芯片") {
		t.Fatalf("清洗把好内容也扔了: %q", items[0].Summary)
	}
}

// TestSanitizePrompt_IsTheOneChokePoint
//
// ⚠️ 断言的是**咽喉点**那一条不变量, 不是"每个渲染器都干净"那条更强的 ——
// 后者要给几十个渲染器各加一行, 而它们里任何一个漏了都不会有人发现。
func TestSanitizePrompt_IsTheOneChokePoint(t *testing.T) {
	broken := "高利率冲击下" + string([]byte{0xe8, 0x82}) + "芯片"
	if utf8.ValidString(broken) {
		t.Fatal("夹具本身是合法 UTF-8")
	}
	got := sanitizePrompt(broken)
	if !utf8.ValidString(got) {
		t.Fatal("洗完之后仍然不是合法 UTF-8")
	}
	// ⚠️ 清洗是**替换坏字节**, 不是把整段扔掉 —— 好内容必须留下来。
	if !strings.Contains(got, "芯片") || !strings.Contains(got, "高利率") {
		t.Fatalf("清洗把好内容也扔了: %q", got)
	}
	// 合法输入**逐字节不变**(绝大多数轮次走这一条, 不许有任何改动)。
	clean := "完全正常的一段话"
	if sanitizePrompt(clean) != clean {
		t.Fatal("合法输入被改动了 —— 那会让前缀缓存每一轮都断")
	}
}

// TestBuildSystem_ActuallyGoesThroughTheChokePoint
//
// ⚠️ 光有那个函数不算数 —— 组装那一侧必须真的调它。
func TestBuildSystem_ActuallyGoesThroughTheChokePoint(t *testing.T) {
	if !strings.Contains(readFileForTest(t, "dialog.go"), "return sanitizePrompt(b.String())") {
		t.Fatal("buildSystem 没有过 sanitizePrompt —— 那道闸是空的")
	}
}
