package convo

import (
	"os"
	"path/filepath"
	"strings"
)

// ─────────────────────────────────────────────────────────────
// 图片的本地路径: **以"文件真的在盘上"为准**
//
// ⚠️ 桥接方给的字段**不可信** (2026-10-14 实测):
//   · `path_exists` 是"存在才给" —— 文件不在时它**索性不带这个字段**,
//     而不是给 "0";
//   · 于是任何 `!= "0"` 的判断在缺字段时都会放行一个坏路径。
//
// 判据只有一条: **os.Stat 能过**。
// ─────────────────────────────────────────────────────────────

// bridgeMediaDir 桥接方导出图片的目录。
//
// ⚠️ 这是**约定的布局**, 不是配置项: nimbus 把收/发的图按
// <md5>.<ext> 导出到 <home>/.nimbus/media/。我们手里有 md5,
// 所以即使它没给 path (或给错了), 也能自己拼出来试一次 ——
// 而"能自己拼出来"这件事在实测里救回过一张图。
func bridgeMediaDir() string {
	if d := strings.TrimSpace(os.Getenv("NIMBUS_MEDIA_DIR")); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".nimbus", "media")
}

// localCandidates 一张图片段可能对应的本地路径, **按可信度排序**。
//
// 顺序: 桥接方给的 path → 用 md5 按约定拼出来 → file 字段原样 → url。
func localCandidates(data map[string]string) []string {
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		for _, x := range out {
			if x == p {
				return
			}
		}
		out = append(out, p)
	}
	// ① path —— nimbus 系导出后的**绝对路径** (首选)。
	//
	// ⚠️ 判据是 **`== "1"`**, 不是 `!= "0"` (桥接方 2026-10-14 定的口径)。
	//
	// 原来写 `!= "0"` 是因为桥接方的 `path_exists` 曾经是"存在才给" ——
	// 文件不在时它**索性不带这个字段**, 于是 `"" != "0"` 成立,
	// 我们照样采用一个不存在的路径。那个坑已经堵了: **现在它永远有值**。
	//
	// ⚠️ 缺字段现在按"没下"处理 —— **保守方向是去下载, 不是假装有图**。
	if data["path_exists"] == "1" {
		add(data["path"])
	}
	// ② url —— NapCat/LLBot 系的可下载地址。
	//
	// ⚠️ **它排在 file 前面是老契约, 不许改** (cq_test / ws_test 钉着)。
	// 老桥接方里 file 常常是本机缓存路径, url 才是能跨机取到的那一个。
	add(data["url"])
	// ③ 用 md5 按约定拼出来的路径 —— nimbus 的导出目录布局。
	//
	// 桥接方的导出**时灵时不灵**, 而"文件其实在, 只是它没给对路径"
	// 是很常见的一种。我们手里有 md5, 就能自己试一次。
	if dir := bridgeMediaDir(); dir != "" {
		if m := strings.ToLower(strings.TrimSpace(data["md5"])); m != "" {
			ext := strings.ToLower(filepath.Ext(data["file"]))
			if ext == "" {
				ext = ".jpg"
			}
			add(filepath.Join(dir, m+ext))
			// ⚠️ 桥接方会把动图导出成 .jpg —— 几种扩展名都试。
			// 反正最后由 Stat 说了算, 试错零成本。
			for _, alt := range []string{".jpg", ".jpeg", ".png", ".gif", ".webp"} {
				if alt != ext {
					add(filepath.Join(dir, m+alt))
				}
			}
		}
	}
	// ④ file 原样 —— 可能是绝对路径, 也可能是裸文件名 (nimbus 就是裸的)。
	add(data["file"])
	return out
}

// FirstExistingFile 返回第一个**真的存在**的非空普通文件; 都不在 → 空串。
func FirstExistingFile(cands []string) string {
	for _, p := range cands {
		st, err := os.Stat(p)
		if err == nil && !st.IsDir() && st.Size() > 0 {
			return p
		}
	}
	return ""
}
