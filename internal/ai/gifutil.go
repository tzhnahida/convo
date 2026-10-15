package ai

import (
	"image"
	"image/draw"
	"image/gif"
	"image/png"
	"os"
	"strings"
)

func gifDecodeAll(f *os.File) (*gif.GIF, error) { return gif.DecodeAll(f) }

// ─────────────────────────────────────────────────────────────
// 动图: 首帧 + 末帧拼一张
//
// ⚠️ 视觉 API 对 GIF **只取第一帧** —— 而表情包的信息恰恰在"动"上:
// 一个"缓缓点头"和一个"疯狂摇头", 首帧可能长得一模一样。
//
// 拼首末两帧是**最便宜的近似**: 一次调用就能看到"从哪到哪"。
// 用纯 Go (image/gif), 不依赖 ffmpeg —— 少一个部署前提。
// ─────────────────────────────────────────────────────────────

// FlattenAnimated 多帧 GIF → 首帧+末帧横向拼接的 PNG。写在与源文件同目录。
//
// 返回 (新路径, 是否真的拼了)。单帧/解码失败 → ("", false), 调用方照原样发。
func FlattenAnimated(src string) (string, bool) {
	f, err := os.Open(src)
	if err != nil {
		return "", false
	}
	defer f.Close()
	g, err := gif.DecodeAll(f)
	if err != nil || len(g.Image) < 2 {
		return "", false
	}
	first, last := g.Image[0], g.Image[len(g.Image)-1]
	make := func(im *image.Paletted) *image.RGBA {
		r := im.Bounds()
		dst := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
		draw.Draw(dst, dst.Bounds(), im, r.Min, draw.Src)
		return dst
	}
	a, b := make(first), make(last)
	w, h := a.Bounds().Dx()+b.Bounds().Dx(), a.Bounds().Dy()
	if b.Bounds().Dy() > h {
		h = b.Bounds().Dy()
	}
	// ⚠️ 上限: 拼出来的图不该比原图大太多 —— API 有大小限制,
	// 而表情包常常是 500x500 的动图, 拼完就 1000x500 了。
	if w > 2048 || h > 2048 {
		return "", false
	}
	canvas := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(canvas, a.Bounds(), a, image.Point{}, draw.Src)
	draw.Draw(canvas, b.Bounds().Add(image.Pt(a.Bounds().Dx(), 0)), b, image.Point{}, draw.Src)
	out := strings.TrimSuffix(src, filepathExt(src)) + ".flat.png"
	of, err := os.Create(out)
	if err != nil {
		return "", false
	}
	defer of.Close()
	if err := png.Encode(of, canvas); err != nil {
		return "", false
	}
	return out, true
}

func filepathExt(p string) string {
	if i := strings.LastIndex(p, "."); i >= 0 {
		return p[i:]
	}
	return ""
}
