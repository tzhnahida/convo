package ai

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

// ─────────────────────────────────────────────────────────────
// 永远不信扩展名
//
// 实测 (2026-10-14):
//   · 搜图下回来的 4 个 ".gif" **内容是 HTML 网页** —— vision API 回 400
//     「请确保是 webp/png/jpeg/gif」, 而**那个报错是对的**: 骗人的是扩展名。
//   · 群里反复发的那张贴纸名叫 79b2858e….jpg, **内容却是 GIF**。
//
// ⚠️ 凡是要判格式的地方, **必须以文件头为准**。
// 扩展名是别人给的, 内容是自己看到的。
// ─────────────────────────────────────────────────────────────

var imageMagics = []struct {
	Prefix []byte
	MIME   string
	Ext    string
}{
	{[]byte("\x89PNG\r\n\x1a\n"), "image/png", ".png"},
	{[]byte("\xff\xd8\xff"), "image/jpeg", ".jpg"},
	{[]byte("GIF87a"), "image/gif", ".gif"},
	{[]byte("GIF89a"), "image/gif", ".gif"},
}

// SniffImage 按**文件头**判格式。ok=false = 这不是一张能用的图。
func SniffImage(head []byte) (mime, ext string, ok bool) {
	for _, m := range imageMagics {
		if bytes.HasPrefix(head, m.Prefix) {
			return m.MIME, m.Ext, true
		}
	}
	if len(head) >= 12 && bytes.Equal(head[0:4], []byte("RIFF")) && bytes.Equal(head[8:12], []byte("WEBP")) {
		return "image/webp", ".webp", true
	}
	return "", "", false
}

// MimeOfFile 读文件头给出真实 MIME。认不出来 → 空串 (调用方据此拒发)。
//
// ⚠️ 返回空串**不是**"退回默认值"的理由: 送一个不是图片的东西给 API
// 只会拿到 400, 而且报错信息会把责任指向 API 而不是那个坏文件。
func MimeOfFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	head := make([]byte, 16)
	n, _ := f.Read(head)
	if n <= 0 {
		return ""
	}
	mime, _, ok := SniffImage(head[:n])
	if !ok {
		return ""
	}
	return mime
}

// IsAnimatedGIF 是不是多帧 GIF。
//
// ⚠️ 判据是**能不能解码出多帧**, 不是扩展名 —— 见文件头的注释。
func IsAnimatedGIF(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	g, err := gifDecodeAll(f)
	if err != nil {
		return false
	}
	return len(g.Image) > 1
}

// MimeByContentOrExt 兼容入口: 内容优先, 认不出来才看扩展名。
//
// ⚠️ 只在**不能拒发**的老路径上用 (它们是既有行为, 改了会牵动别处)。
// 新代码请直接用 MimeOfFile。
func MimeByContentOrExt(path string) string {
	if m := MimeOfFile(path); m != "" {
		return m
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	}
	return "image/png"
}
