package convo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// 偷图: 群友爱用的, 它也可以自己用
//
// 真人就是这么学会用表情包的 —— 看群里谁老发哪张, 觉得好用就存下来自己发。
//
// ⚠️ **判据是"群里发得多", 不是"看起来像表情包"。**
// 一张图是不是梗图, 它自己说了不算 —— **群里反复在用它, 那才是共识**。
// 这条正好接上哈希判重: `seen_n` 就是流行度。
//
// ⚠️ 与 `captureUserSticker` 的区别:
//   那个是**所有者发的第一张**就收 (affinity 固定 0.55, 不问来历);
//   这个是**群里流通的**才收, 而且 **affinity 随流行度涨** ——
//   发得越多 = 越好用 = 它越愿意发。
// ─────────────────────────────────────────────────────────────

// AdoptMinSeen 收进自己库所需的**最低流行度**。
//
// ⚠️ 比 ImageStickerRepeats 高 —— "有人手滑发了两次"不该算流行。
const AdoptMinSeen = 4

// AdoptAffinity 按流行度给 affinity。
//
// ⚠️ 封顶 0.85 而不是 1.0:
// 全群最火的那张也不该被无限次发 —— 那是刷屏, 而 §8.7 有 cap_per_sticker 兜着。
func AdoptAffinity(seen int) float64 {
	if seen < AdoptMinSeen {
		return 0
	}
	a := 0.45 + 0.08*float64(seen-AdoptMinSeen+1)
	if a > 0.85 {
		a = 0.85
	}
	return a
}

// stickerSrc md5 → 库里那条记录的 src_url。
//
// ⚠️ 用 md5 做去重键, 而不是文件名 —— 同一张图被不同人发出来时,
// 桥接方给的文件名可能不同 (大小写/扩展名), 但 md5 是一样的。
func stickerSrc(md5 string) string { return "md5://" + strings.ToLower(strings.TrimSpace(md5)) }

// AdoptSticker 把群里流行的表情包收进它自己的库。
//
// 返回 (本地路径, 是不是新收的, 错误)。已经收过 → 只更新 affinity, 不重复复制文件。
func (e *Engine) AdoptSticker(ctx context.Context, imgPath, md5, note string, seen int) (string, bool, error) {
	aff := AdoptAffinity(seen)
	if aff <= 0 || strings.TrimSpace(md5) == "" {
		return "", false, nil
	}
	src := stickerSrc(md5)
	// 已经收过 → 只把 affinity 提上去 (它变流行了)。
	if id, ok := e.Store.StickerBySrc(src); ok {
		if err := e.Store.SetAffinity(id, aff); err != nil {
			return "", false, err
		}
		if note != "" {
			_ = e.Store.SetStickerNote(id, note)
		}
		return "", false, nil
	}
	dir := filepath.Join(e.Cfg.Stickers.Dir, e.Cfg.Stickers.Lang)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, err
	}
	ext := strings.ToLower(filepath.Ext(imgPath))
	if ext == "" {
		ext = ".gif"
	}
	dst := filepath.Join(dir, fmt.Sprintf("adopt_%s%s", strings.ToLower(strings.TrimSpace(md5))[:8], ext))
	if err := copyFile(imgPath, dst); err != nil {
		return "", false, err
	}
	// 指纹就用桥接方给的 md5 (它与 src 同源, 已经证明是同一张图)。
	id, err := e.Store.InsertSticker(src, dst, e.Cfg.Stickers.Lang, "adopted", aff,
		strings.ToLower(strings.TrimSpace(md5)))
	if err != nil {
		return "", false, err
	}
	if note != "" {
		_ = e.Store.SetStickerNote(id, note)
	}
	log.Info("convo:sticker", "偷到一张 (群里发了 %d 次, affinity %.2f): %s", seen, aff, note)
	return dst, true, nil
}
