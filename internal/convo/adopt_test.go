package convo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ⚠️ **判据是"群里发得多", 不是"看起来像表情包"** ——
// 一张图是不是梗图, 它自己说了不算; **群里反复在用它, 那才是共识**。
func TestAdoptAffinity_GrowsWithPopularity(t *testing.T) {
	if AdoptAffinity(1) != 0 || AdoptAffinity(AdoptMinSeen-1) != 0 {
		t.Fatal("不够流行的不该收 —— 有人手滑发两次不算")
	}
	a := AdoptAffinity(AdoptMinSeen)
	b := AdoptAffinity(AdoptMinSeen + 5)
	if a <= 0 || b <= a {
		t.Fatalf("越流行 affinity 该越高: %.2f vs %.2f", a, b)
	}
	// ⚠️ 封顶 0.85: 全群最火的那张也不该被无限次发 (那是刷屏)。
	if got := AdoptAffinity(9999); got > 0.85 {
		t.Fatalf("没有封顶, 它会可着最火的那张猛发: %.2f", got)
	}
}

func TestAdoptSticker_NeedsPopularity(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	src := filepath.Join(t.TempDir(), "s.gif")
	if err := os.WriteFile(src, []byte("GIF89a"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 不够流行 → 什么都不做。
	if _, adopted, err := e.AdoptSticker(nil, src, "aabbccdd", "嘲讽", AdoptMinSeen-1); err != nil || adopted {
		t.Fatalf("不够流行却收了: adopted=%v err=%v", adopted, err)
	}
	// ⚠️ 夹具里本来就有一张 (groupSpeakEngine 为三态测试种的) —— 比**增量**。
	base, _ := e.Store.ActiveStickers()
	if len(base) != 1 {
		t.Fatalf("夹具前提变了: %d", len(base))
	}
	// 够流行 → 收。
	_, adopted, err := e.AdoptSticker(nil, src, "aabbccdd", "嘲讽", AdoptMinSeen)
	if err != nil || !adopted {
		t.Fatalf("够流行却没收到: adopted=%v err=%v", adopted, err)
	}
	pool, _ := e.Store.ActiveStickers()
	if len(pool) != len(base)+1 {
		t.Fatalf("应当多收 1 张: %d → %d", len(base), len(pool))
	}
	var got *Sticker
	for i := range pool {
		if pool[i].Source == "adopted" {
			got = &pool[i]
		}
	}
	if got == nil {
		t.Fatalf("来源没标注: %+v", pool)
	}
	if !strings.Contains(got.LocalPath, "adopt_") {
		t.Fatalf("文件命名不对: %q", got.LocalPath)
	}
}

// ⚠️ **同一张图不许收两次** —— 但流行度涨了要**提高 affinity**。
// 增量会不可解释, 所以是"按流行度重算"。
func TestAdoptSticker_DedupesAndRaisesAffinity(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	src := filepath.Join(t.TempDir(), "s.gif")
	if err := os.WriteFile(src, []byte("GIF89a"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := e.AdoptSticker(nil, src, "aabbccdd", "嘲讽", AdoptMinSeen)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := e.Store.ActiveStickers()
	pool1, _ := e.Store.ActiveStickers()
	if len(pool1) != len(base) {
		t.Fatalf("第一次没收到: %d → %d", len(base), len(pool1))
	}
	find := func() *Sticker {
		p, _ := e.Store.ActiveStickers()
		for i := range p {
			if p[i].Source == "adopted" {
				return &p[i]
			}
		}
		return nil
	}
	a1 := find()
	if a1 == nil {
		t.Fatal("没有 adopted 来源的表情")
	}
	first := a1.Affinity
	// 同一张 (大小写不同) → 不重复收, 但 affinity 要涨。
	_, adopted, err := e.AdoptSticker(nil, src, "AABBCCDD", "嘲讽", AdoptMinSeen+5)
	if err != nil {
		t.Fatal(err)
	}
	if adopted {
		t.Fatal("同一张图被收了两次 —— 库里会出现重复贴纸")
	}
	adopted2 := 0
	for _, p := range func() []Sticker { q, _ := e.Store.ActiveStickers(); return q }() {
		if p.Source == "adopted" {
			adopted2++
		}
	}
	if adopted2 != 1 {
		t.Fatalf("同一张图被收了两次 —— 库里有 %d 条 adopted", adopted2)
	}
	a2 := find()
	if a2 == nil {
		t.Fatal("adopted 表情没了")
	}
	if a2.Affinity <= first {
		t.Fatalf("变流行了但 affinity 没涨: %.2f → %.2f", first, a2.Affinity)
	}
}

// md5 缺失时不许收 —— 没法去重, 收进去就是重复贴纸。
func TestAdoptSticker_NoHashNoAdopt(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	src := filepath.Join(t.TempDir(), "s.gif")
	_ = os.WriteFile(src, []byte("GIF89a"), 0o644)
	if _, adopted, err := e.AdoptSticker(nil, src, "", "嘲讽", 99); err != nil || adopted {
		t.Fatalf("没哈希却收了: adopted=%v err=%v", adopted, err)
	}
}
