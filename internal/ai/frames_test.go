package ai

import (
	"image"
	"image/color"
	"image/gif"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAnimGIF(t *testing.T, path string, frames int) {
	t.Helper()
	g := &gif.GIF{}
	for i := 0; i < frames; i++ {
		p := image.NewPaletted(image.Rect(0, 0, 6, 6), color.Palette{color.Black, color.White})
		for x := 0; x < 6; x++ {
			for y := 0; y < 6; y++ {
				p.SetColorIndex(x, y, uint8(i%2))
			}
		}
		g.Image = append(g.Image, p)
		g.Delay = append(g.Delay, 8)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := gif.EncodeAll(f, g); err != nil {
		t.Fatal(err)
	}
}

// ⚠️ **动图必须抽成多帧分开传**, 不是拼成一张。
//
// 官方最佳实践 (karminski, 2026-08-22): 「模型只识别 gif 的第一帧;
// 抽帧的正确做法是抽成 PNG **按时间顺序作为多个 image_url 传入**」。
//
// 我第一版拼成一张横向长图 —— 模型会把它读成**左右对比梗图**。
func TestExtractFrames_OrderAndCount(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "a.gif")
	writeAnimGIF(t, src, 30)
	fs, ok := ExtractFrames(src, 4)
	if !ok {
		t.Fatal("动图应当被抽帧")
	}
	if len(fs) != 4 {
		t.Fatalf("应当抽 4 帧, got %d", len(fs))
	}
	// ⚠️ **顺序就是时间轴** —— 文件名里的帧号必须递增, 且首尾都在。
	var idx []int
	for _, p := range fs {
		if !strings.Contains(p, ".f") {
			t.Fatalf("帧文件名没有帧号: %q", p)
		}
		var n int
		if _, err := fmtSscan(p[strings.LastIndex(p, ".f")+2:], &n); err != nil {
			t.Fatalf("帧号解析失败 %q", p)
		}
		idx = append(idx, n)
	}
	if idx[0] != 0 {
		t.Fatalf("第一帧必须是首帧: %v", idx)
	}
	if idx[len(idx)-1] != 29 {
		t.Fatalf("最后一帧必须是末帧: %v", idx)
	}
	for i := 1; i < len(idx); i++ {
		if idx[i] <= idx[i-1] {
			t.Fatalf("帧号必须严格递增 (顺序即时间轴): %v", idx)
		}
	}
}

// ⚠️ 抽出来的帧**不许写在源文件旁边** —— 源常常是表情库里的表情包,
// 在旁边生成 PNG 会让库里多出一堆看起来像表情的文件。
func TestExtractFrames_NotNextToSource(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "sticker.gif")
	writeAnimGIF(t, src, 10)
	fs, ok := ExtractFrames(src, 3)
	if !ok {
		t.Fatal("应当抽帧")
	}
	for _, p := range fs {
		if filepath.Dir(p) == d {
			t.Fatalf("帧写在了源文件旁边, 会污染表情库: %q", p)
		}
	}
	// 用完即删。
	CleanupFrames(fs)
	for _, p := range fs {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("CleanupFrames 没删掉: %q", p)
		}
	}
	// ⚠️ **绝不能把源文件删了** —— 那是一条不可逆的数据损失。
	if _, err := os.Stat(src); err != nil {
		t.Fatal("源文件被 CleanupFrames 删掉了")
	}
}

// 单帧图不该被当成动图 (抽出来只有一张, 与直接送没区别, 却多两次 IO)。
func TestExtractFrames_SingleFrameIsNoop(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "one.gif")
	writeAnimGIF(t, src, 1)
	if _, ok := ExtractFrames(src, 4); ok {
		t.Fatal("单帧不该被抽")
	}
	junk := filepath.Join(d, "x.gif")
	_ = os.WriteFile(junk, []byte("not a gif"), 0o644)
	if _, ok := ExtractFrames(junk, 4); ok {
		t.Fatal("坏文件不该被抽")
	}
}

func TestFrameCount(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "n.gif")
	writeAnimGIF(t, p, 7)
	if got := FrameCount(p); got != 7 {
		t.Fatalf("帧数应当是 7, got %d", got)
	}
}
