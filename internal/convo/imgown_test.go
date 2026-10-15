package convo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeBytes(t *testing.T, p string, b []byte) string {
	t.Helper()
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// ⚠️ **本批的头号契约**: 任何要长期留存的图, convo 自己留一份。
//
// 桥接方回的两类路径都不是我们能依赖的:
//
//	· nimbus 的 media/ 目录 (它那边还在修);
//	· QQ 的 nt_data/Pic 缓存 —— **会被清理**。
//
// 拿后者当长期存储 = 把"这张图我看过"建在一个随时会被删的文件上,
// 而现象是"标签库里的图过几天全没了", 且不会有任何报错。
func TestOwnImage_CopiesIntoInbox(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	src := writeBytes(t, filepath.Join(t.TempDir(), "x.jpg"), []byte{0xFF, 0xD8, 0xFF, 0xE0, 1, 2, 3})
	got := e.OwnImage(src)
	if got == "" {
		t.Fatal("没留档")
	}
	if got == src {
		t.Fatal("还是原路径 —— QQ 清理缓存后它就没了")
	}
	if !strings.Contains(got, "inbox") {
		t.Fatalf("没进 convo 自己的目录: %q", got)
	}
	if _, err := os.Stat(got); err != nil {
		t.Fatalf("留档后的文件不存在: %v", err)
	}
	// 内容必须一致。
	orig, _ := os.ReadFile(src)
	copyB, _ := os.ReadFile(got)
	if string(orig) != string(copyB) {
		t.Fatal("复制的内容不一致")
	}
}

// ⚠️ **按内容哈希命名 → 幂等**: 同一张图重复调用只命中已有文件, 不重复复制。
func TestOwnImage_IdempotentByContent(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	d := t.TempDir()
	b := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 9, 9}
	a1 := e.OwnImage(writeBytes(t, filepath.Join(d, "one.png"), b))
	a2 := e.OwnImage(writeBytes(t, filepath.Join(d, "two.png"), b)) // 同名内容, 不同路径
	if a1 != a2 {
		t.Fatalf("同内容应当命中同一份: %q vs %q", a1, a2)
	}
	// ⚠️ 内容不同就该是两份 —— 只按文件名去重会把不同的图合并掉。
	a3 := e.OwnImage(writeBytes(t, filepath.Join(d, "three.png"), []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 7}))
	if a3 == a1 {
		t.Fatal("内容不同的两张图被当成同一张")
	}
}

// 已经在自己的目录里 → 不再复制一次 (否则每次读都多一份)。
func TestOwnImage_AlreadyOwnedIsNoop(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	src := writeBytes(t, filepath.Join(t.TempDir(), "a.jpg"), []byte{1, 2, 3, 4, 5})
	once := e.OwnImage(src)
	twice := e.OwnImage(once)
	if once != twice {
		t.Fatalf("自己的图又被复制了一遍: %q → %q", once, twice)
	}
}

// ⚠️ 复制失败时**退回原路径**而不是报错 ——
// 让整条图片链因为一次复制失败而断掉, 是更糟的选择。
func TestOwnImage_UnreadableReturnsEmptyNotPanic(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	if got := e.OwnImage("/nonexistent/nope.jpg"); got != "" {
		t.Fatalf("不存在的文件该返回空串, got %q", got)
	}
	if got := e.OwnImage("   "); got != "" {
		t.Fatalf("空路径该返回空串, got %q", got)
	}
	// 0 字节的文件也不算数 —— 半截下载读不出图。
	d := t.TempDir()
	empty := filepath.Join(d, "e.jpg")
	_ = os.WriteFile(empty, nil, 0o644)
	if got := e.OwnImage(empty); got != "" {
		t.Fatalf("0 字节不该留档: %q", got)
	}
}

func TestFileMD5_StableAcrossPaths(t *testing.T) {
	d := t.TempDir()
	b := []byte("hello image bytes")
	a := writeBytes(t, filepath.Join(d, "a"), b)
	c := writeBytes(t, filepath.Join(d, "b"), b)
	ma, err := fileMD5(a)
	if err != nil {
		t.Fatal(err)
	}
	mc, _ := fileMD5(c)
	if ma != mc {
		t.Fatalf("同内容不同路径的哈希不同: %s vs %s", ma, mc)
	}
	md, _ := fileMD5(writeBytes(t, filepath.Join(d, "c"), []byte("different")))
	if md == ma {
		t.Fatal("不同内容的哈希相同")
	}
}
