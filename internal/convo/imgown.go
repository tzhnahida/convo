package convo

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tzhnahida/VocabLock/internal/log"
)

// ─────────────────────────────────────────────────────────────
// 图片: **convo 自己留一份**
//
// ⚠️ 桥接方给的两类路径**都不是我们能依赖的**:
//   · `~/.nimbus/media/<md5>.<ext>`  —— 桥的导出目录 (它那边还在修);
//   · `<QQ>/nt_data/Pic/...`         —— **QQ 客户端的缓存, 会被清理**。
//
// `get_image` 下完之后回的是后者。拿它当长期存储等于把"这张图我看过"
// 建在一个随时会被删的文件上 —— 而现象是"标签库里的图过几天全没了",
// 且不会有任何报错。
//
// 所以: 任何要长期留存的图, **复制进 convo 自己的 inbox**。
// 复制之后桥那边做不做导出、QQ 清不清理缓存, 都与我们无关。
// ─────────────────────────────────────────────────────────────

// InboxMaxFiles inbox 的容量上限。
//
// ⚠️ inbox 是按内容哈希命名的 (同一张图只存一份), 所以增长本来就慢。
// 但"慢"不是"不会" —— 没有上限的目录迟早会撑爆树莓派的 SD 卡,
// 而那种故障的表现是"所有写入都失败", 不是"图片变多"。
const InboxMaxFiles = 2000

// OwnImage 把一张本地图**复制进 convo 自己的 inbox**, 返回稳定路径。
//
// 按**内容哈希**命名 → 天然幂等: 同一张图重复调用只会命中已有文件, 不重复复制。
//
// ⚠️ 复制失败时**退回原路径**而不是报错 —— 原路径至少现在还能读。
// 让整条图片链因为一次复制失败而断掉, 是更糟的选择。
func (e *Engine) OwnImage(src string) string {
	src = strings.TrimSpace(src)
	if src == "" {
		return ""
	}
	st, err := os.Stat(src)
	if err != nil || st.IsDir() || st.Size() == 0 {
		return ""
	}
	// 已经在自己的目录里 → 不用再复制一次。
	if strings.Contains(src, filepath.Join("convo", "inbox")) {
		return src
	}
	sum, err := fileMD5(src)
	if err != nil {
		log.Warn("convo:engine", "算哈希失败, 沿用原路径: %v", err)
		return src
	}
	ext := strings.ToLower(filepath.Ext(src))
	if ext == "" {
		ext = ".bin"
	}
	dir := filepath.Join(e.Cfg.DataDir, "convo", "inbox")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Warn("convo:engine", "建 inbox 失败, 沿用原路径: %v", err)
		return src
	}
	dst := filepath.Join(dir, sum+ext)
	if _, err := os.Stat(dst); err == nil {
		return dst // 已经有这一份 (幂等)
	}
	if err := copyFile(src, dst); err != nil {
		log.Warn("convo:engine", "复制进 inbox 失败, 沿用原路径: %v", err)
		return src
	}
	log.Info("convo:engine", "留档: %s → %s", filepath.Base(src), filepath.Base(dst))
	e.pruneInbox(dir)
	return dst
}

// fileMD5 算文件内容哈希。
func fileMD5(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// pruneInbox 超上限时删最旧的。
//
// ⚠️ 删的是**最旧的副本**, 而不是"最近没被引用的" —— 后者要回查图片表,
// 而清理这种后台动作**不该依赖另一张表的状态** (它自己出错时会更难查)。
func (e *Engine) pruneInbox(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) <= InboxMaxFiles {
		return
	}
	type fi struct {
		name string
		mod  int64
	}
	var all []fi
	for _, en := range ents {
		if en.IsDir() {
			continue
		}
		info, err := en.Info()
		if err != nil {
			continue
		}
		all = append(all, fi{en.Name(), info.ModTime().UnixNano()})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].mod < all[j].mod })
	for i := 0; i < len(all)-InboxMaxFiles; i++ {
		_ = os.Remove(filepath.Join(dir, all[i].name))
	}
	log.Info("convo:engine", "inbox 超上限, 清掉最旧的 %d 张", len(all)-InboxMaxFiles)
}

var _ = fmt.Sprintf
