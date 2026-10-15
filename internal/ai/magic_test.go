package ai

import (
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// ⚠️ **永远不信扩展名**。实测: 搜图下回来的 4 个 ".gif" 内容是 HTML;
// 群里的贴纸名叫 .jpg 实际是 GIF。
func TestSniffImage(t *testing.T) {
	cases := []struct {
		head []byte
		mime string
		ok   bool
	}{
		{[]byte("\x89PNG\r\n\x1a\n"), "image/png", true},
		{[]byte("\xff\xd8\xff\xe0"), "image/jpeg", true},
		{[]byte("GIF89a"), "image/gif", true},
		{[]byte("GIF87a"), "image/gif", true},
		{[]byte("RIFF\x00\x00\x00\x00WEBP"), "image/webp", true},
		{[]byte("<!DOCTYPE html><html>"), "", false},
		{[]byte("{\"error\":\"403\"}"), "", false},
		{[]byte(""), "", false},
	}
	for _, c := range cases {
		mime, _, ok := SniffImage(c.head)
		if ok != c.ok || (ok && mime != c.mime) {
			t.Fatalf("%q → (%q,%v), want (%q,%v)", c.head, mime, ok, c.mime, c.ok)
		}
	}
}

// ⚠️ 一张**名叫 .png 的 HTML** 必须以内容为准 —— 这正是当初那次 400。
func TestMimeOfFile_IgnoresExtension(t *testing.T) {
	d := t.TempDir()
	html := filepath.Join(d, "fake.png")
	if err := os.WriteFile(html, []byte("<!DOCTYPE html><html><body>403</body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := MimeOfFile(html); got != "" {
		t.Fatalf("网页被当成了图片: %q —— 这正是当初 vision 回 400 的原因", got)
	}
	// 反向: 一张**名叫 .jpg 的 GIF** 必须以内容为准。
	p := filepath.Join(d, "actually.gif.jpg")
	if err := os.WriteFile(p, []byte("GIF89a\x01\x00\x01\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := MimeOfFile(p); got != "image/gif" {
		t.Fatalf("内容优先失效: %q (扩展名说 jpeg, 内容是 gif)", got)
	}
}

// ⚠️ 兼容入口: 内容认不出来才退回扩展名 (老路径不能因此全部拒发)。
func TestMimeByContentOrExt_FallsBack(t *testing.T) {
	d := t.TempDir()
	junk := filepath.Join(d, "x.png")
	if err := os.WriteFile(junk, []byte("not an image at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := MimeByContentOrExt(junk); got != "image/png" {
		t.Fatalf("兜底没生效: %q", got)
	}
	// 内容认得出时**以内容为准**。
	g := filepath.Join(d, "y.jpg")
	if err := os.WriteFile(g, []byte("GIF89a\x01\x00\x01\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := MimeByContentOrExt(g); got != "image/gif" {
		t.Fatalf("内容优先失效: %q", got)
	}
}

func writeGIF(t *testing.T, path string, frames int) {
	t.Helper()
	g := &gif.GIF{}
	for i := 0; i < frames; i++ {
		p := image.NewPaletted(image.Rect(0, 0, 8, 8), color.Palette{color.Black, color.White})
		c := uint8(i % 2)
		for x := 0; x < 8; x++ {
			for y := 0; y < 8; y++ {
				p.SetColorIndex(x, y, c)
			}
		}
		g.Image = append(g.Image, p)
		g.Delay = append(g.Delay, 10)
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

// ⚠️ 视觉 API 对 GIF **只取第一帧** —— 而表情包的信息恰恰在"动"上。
func TestFlattenAnimated(t *testing.T) {
	d := t.TempDir()
	multi := filepath.Join(d, "anim.gif")
	writeGIF(t, multi, 3)
	out, ok := FlattenAnimated(multi)
	if !ok {
		t.Fatal("多帧 GIF 应当被摊平")
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, err := png.DecodeConfig(f)
	if err != nil {
		t.Fatalf("摊平产物不是 PNG: %v", err)
	}
	if cfg.Width != 16 {
		t.Fatalf("应当是首末两帧横向拼接 (8+8=16), got %d", cfg.Width)
	}
	if cfg.Height != 8 {
		t.Fatalf("高度该保持 8, got %d", cfg.Height)
	}
}

// 单帧不该被摊平 —— 拼一张一模一样的图只是白花一次 IO。
func TestFlattenAnimated_SingleFrameIsNoop(t *testing.T) {
	d := t.TempDir()
	one := filepath.Join(d, "one.gif")
	writeGIF(t, one, 1)
	if _, ok := FlattenAnimated(one); ok {
		t.Fatal("单帧不该被摊平")
	}
	// 非 GIF 也不该炸。
	junk := filepath.Join(d, "x.gif")
	_ = os.WriteFile(junk, []byte("not a gif"), 0o644)
	if _, ok := FlattenAnimated(junk); ok {
		t.Fatal("坏文件不该被当成动图")
	}
}
