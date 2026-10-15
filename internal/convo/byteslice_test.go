package convo

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

// ─────────────────────────────────────────────────────────────
// 「按字节切字符串」这一类
//
// ⚠️ 2026-10-15 那条读不出来的轨迹让我去找根因, 而找出来的是**同一个坑的第三次**:
//
//   ① role_search.go  —— 注释里记着"原来写的是 text[:4000], 会把汉字切成坏 UTF-8"
//   ② dialog.go       —— 降级回复 `t[:800]`, 而它的产物**直接发给用户**
//   ③ slang.go        —— 黑话抓取 `text[:1500]`, 流进学习再进提示词
//
// ⚠️ 前两次是人眼发现的, 第三次是"生产轨迹读不出来"逼出来的。
// 所以这一条要变成尺子 —— 这个形状是**可检测的**。
// ─────────────────────────────────────────────────────────────

// TestDegradeFrame_NeverCutsAMultibyteCharacter
//
// ⚠️ 行为那一面: 降级回复是**直接发给用户的**, 所以切坏了用户会看到半个字。
func TestDegradeFrame_NeverCutsAMultibyteCharacter(t *testing.T) {
	// 一段纯中文, 长度刚好让"按字节切 800"落在一个字中间。
	long := strings.Repeat("这是一段用来测试截断的中文文本", 200)
	if !utf8.ValidString(long) || len(long) <= 800 {
		t.Fatal("夹具不对")
	}
	g := degradeFrame(long)
	if len(g.Reply) == 0 {
		t.Fatal("降级帧里一条都没有")
	}
	for _, it := range g.Reply {
		if !utf8.ValidString(it.Text) {
			t.Fatalf("降级回复不是合法 UTF-8 —— 用户会看到半个字: %q", it.Text)
		}
		// ⚠️ 上限是 **802** 而不是 801: 「……」是**两个**字符(U+2026 U+2026)。
		// 我第一版写的是 801, 而它红得对 —— 那是我算错了, 不是代码错了。
		if n := len([]rune(it.Text)); n > 802 {
			t.Fatalf("截断后还有 %d 个字 (上限 800 + 省略号两字)", n)
		}
	}
}

// byteCutRE 「按字节判长度 + 按字节切」这个形状。
//
// ⚠️ 判据刻意**只看形状**: `len(x) > N` 与 `x[:N]` 出现在同一小段里。
// 它不区分 x 是 string 还是 slice —— slice 的误报很少且一眼能排掉,
// 而漏掉一个字符串的代价是"半个汉字发给用户"。
var byteCutRE = regexp.MustCompile(`(?s)len\\(\\s*(\\w+)\\s*\\)\\s*>\\s*\\d+.{0,160}?\\b\\1\\s*\\[\\s*:\\s*\\d+\\s*\\]`)

// TestNoByteSlicingOnLengthCheckedStrings
//
// ⚠️ **扫源码**的尺子: 这个形状在运行期没有统一的可观测点
// (只在输入恰好含多字节字符且恰好越界时才现形), 所以只能静态查。
func TestNoByteSlicingOnLengthCheckedStrings(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var hits []string
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		b, rerr := os.ReadFile(n)
		if rerr != nil {
			continue
		}
		for _, m := range byteCutRE.FindAllStringSubmatch(string(b), -1) {
			hits = append(hits, n+": "+strings.Join(strings.Fields(m[0]), " "))
		}
	}
	sort.Strings(hits)
	if len(hits) > 0 {
		t.Fatalf("这些地方**按字节判长度又按字节切** —— 中文会被切成非法 UTF-8; 改成 []rune 或 truncateStr:%s%s", "\n  ", strings.Join(hits, "\n  "))
	}
}
