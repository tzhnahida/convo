package convo

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// ⚠️ **截断必须按字符, 不能按字节** (2026-10-14 实测)。
//
// 线上真出过一次: 表情包标签里的「HEYTEA 喜茶恶搞联名」
// 被截成 12 字节, 正好切在「茶」的中间 —— 渲染出来是
//
//	[图: 表情 — 嘲讽 喜 「HEYTEA 喜�」 — ...]
//
// 那个 `�` 进了提示词, 模型读到的是一段坏掉的 UTF-8。
// 中文一个字 3 字节, 按字节截断**必然**切坏 —— 这不是概率问题。
func TestTruncateStr_CutsRunesNotBytes(t *testing.T) {
	s := "HEYTEA 喜茶恶搞联名海报"
	got := truncateStr(s, 12)
	if !utf8.ValidString(got) {
		t.Fatalf("截出了坏 UTF-8: %q", got)
	}
	if n := utf8.RuneCountInString(got); n > 12 {
		t.Fatalf("按字符算超了: %d 个字符 (%q)", n, got)
	}
	// 反面用例: 按字节切的话这里**一定**坏 —— 7 字节 ASCII + 5 个中文 = 切在第 2 个汉字中间。
	if !strings.HasPrefix(got, "HEYTEA 喜") {
		t.Fatalf("前 12 个字符应该是 %q, 实际 %q", "HEYTEA 喜茶恶搞联", got)
	}
}

func TestTruncateStr_ShortEnoughUnchanged(t *testing.T) {
	for _, s := range []string{"", "a", "喜茶", "HEYTEA 喜茶"} {
		if got := truncateStr(s, 12); got != s {
			t.Fatalf("没超限不该动: %q → %q", s, got)
		}
	}
}

func TestTruncateStr_ASCIIStillByteIdentical(t *testing.T) {
	if got := truncateStr("abcdefghijklmnop", 5); got != "abcde" {
		t.Fatalf("ASCII 结果应为 abcde, 实际 %q", got)
	}
}

// 恰好等于上限时不许少切也不许多切。
func TestTruncateStr_ExactBoundary(t *testing.T) {
	s := strings.Repeat("茶", 12)
	if got := truncateStr(s, 12); got != s {
		t.Fatalf("整 12 个字不该被截: %q", got)
	}
	if got := truncateStr(s, 11); utf8.RuneCountInString(got) != 11 {
		t.Fatalf("应留 11 个字, 实际 %d (%q)", utf8.RuneCountInString(got), got)
	}
}
