package convo

import (
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// 池的身份: 一张图 = 一行。
//
// ⚠️ 2026-10-16 实测 (owner 第一个发现的): 表情包页 473 张卡片长得一模一样。
//
// 查库的真数字:
//
//	473 行 · 按内容 md5 去重只剩 21 种 · src_url 去重只剩 24 个
//	local_path 却是 473 个不同文件 —— 同一张图被写盘 47 遍
//
// 根因两条, 缺一条都不至于:
//
//	① download() 文件名是 <纳秒>_<url hash> → **每跑一轮都是新文件名**;
//	② InsertSticker 是裸 INSERT —— 入库前不问「这个 URL / 这张图有没有」。
//
// 于是爬虫每轮把同一批 URL 重下一遍、重入库一遍 (461 行来自那 24 个 URL),
// 最后靠 affinity<0.3 自动退役兜住 433 行 —— 看起来"只是有点重复"。
//
// 修法: 池的身份是**内容**, 不是文件名、不是 URL。
// 入库前先问 URL (省一次下载), 下完再问内容 (省一行一文件)。

// FileDigest 算一个文件的内容指纹 (md5 十六进制小写)。
//
// ⚠️ 这是**去重指纹**, 不是安全边界 —— 别拿它当防篡改用。
func FileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New() // #nosec G401 -- 只用于去重, 不用于完整性校验
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// StickerByDigest 这个池里有没有这张图 (按内容)。
// 返回 id 与"有没有"; 摘要为空 → 一律当没有 (调用方不必先判空)。
func (s *Store) StickerByDigest(lang, digest string) (int64, bool, error) {
	digest = strings.ToLower(strings.TrimSpace(digest))
	if digest == "" {
		return 0, false, nil
	}
	var id int64
	err := s.DB.QueryRow("SELECT id FROM stickers WHERE lang=? AND digest=? ORDER BY id LIMIT 1",
		lang, digest).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// StickerBySrcURL 这个 URL 收过没有 —— 在**下载之前**问, 省一次网络与一个新文件。
func (s *Store) StickerBySrcURL(lang, srcURL string) (int64, bool, error) {
	srcURL = strings.TrimSpace(srcURL)
	if srcURL == "" {
		return 0, false, nil
	}
	var id int64
	err := s.DB.QueryRow("SELECT id FROM stickers WHERE lang=? AND src_url=? ORDER BY id LIMIT 1",
		lang, srcURL).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// DedupeReport 一次收拢的结果 (dryRun 时是「将会」)。
type DedupeReport struct {
	RowsBefore int // 收拢前多少行
	RowsAfter  int // 收拢后多少行
	Dropped    int // 合并掉几行
	FilesGone  int // 删掉几个重复文件
	NoFile     int // 文件已不在、算不出指纹的行 (不动, 只报)
	Groups     int // 收拢成几组 (= 几张不同的图)
}

type dedupeRow struct {
	id     int64
	lang   string
	path   string
	aff    float64
	uses   int
	digest string // 库里记着的 (老库可能是空)
	live   string // 现算出来的 (空 = 文件没了)
}

// DedupeStickers 把池里「同一张图的多行」收拢成一行。
//
// 保留哪一个: affinity 最高 → use_count 最多 → id 最小 (先来后到)。
// use_count **求和**合并到保留行 —— 被喜欢过的次数不该因为合并而蒸发。
//
// 文件: 只有当一个被合并行的文件**没有别的存活行指着它**时才删 (保守)。
// 文件已经不在的行: 算不出指纹 → 各自成组、**一律不动**, 只在报告里计 NoFile。
// (它的文件可能在别的盘上; 替用户删行等于替用户删东西。)
//
// dryRun=true 只算不写 —— 先看清楚要动什么, 再决定动不动。
func (s *Store) DedupeStickers(dryRun bool) (DedupeReport, error) {
	var rep DedupeReport
	rows, err := s.DB.Query("SELECT id, COALESCE(lang,''), COALESCE(local_path,''), " +
		"COALESCE(affinity,0), COALESCE(use_count,0), COALESCE(digest,'') FROM stickers ORDER BY id")
	if err != nil {
		return rep, err
	}
	var all []*dedupeRow
	for rows.Next() {
		r := &dedupeRow{}
		if err := rows.Scan(&r.id, &r.lang, &r.path, &r.aff, &r.uses, &r.digest); err != nil {
			rows.Close()
			return rep, err
		}
		all = append(all, r)
	}
	if err := rows.Close(); err != nil {
		return rep, err
	}
	rep.RowsBefore = len(all)

	groups := map[string][]*dedupeRow{}
	var order []string
	for _, r := range all {
		r.live = strings.ToLower(strings.TrimSpace(r.digest))
		if r.live == "" {
			if d, derr := FileDigest(r.path); derr == nil {
				r.live = d
			}
		}
		var key string
		if r.live == "" {
			rep.NoFile++
			key = r.lang + "\x00missing\x00" + r.path // 各自成组: 永不合并
		} else {
			key = r.lang + "\x00" + r.live
		}
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], r)
	}
	rep.Groups = len(order)

	alive := map[string]bool{} // 存活行指着哪些文件
	var doomed []string
	for _, key := range order {
		g := groups[key]
		sort.SliceStable(g, func(i, j int) bool {
			if g[i].aff != g[j].aff {
				return g[i].aff > g[j].aff
			}
			if g[i].uses != g[j].uses {
				return g[i].uses > g[j].uses
			}
			return g[i].id < g[j].id
		})
		keep := g[0]
		alive[keep.path] = true
		sum := 0
		for _, r := range g {
			sum += r.uses
		}
		if !dryRun {
			if _, err := s.DB.Exec("UPDATE stickers SET use_count=?, digest=? WHERE id=?",
				sum, keep.live, keep.id); err != nil {
				return rep, err
			}
		}
		rep.Dropped += len(g) - 1
		for _, r := range g[1:] {
			if !dryRun {
				if _, err := s.DB.Exec("DELETE FROM stickers WHERE id=?", r.id); err != nil {
					return rep, err
				}
			}
			if r.path != "" && r.path != keep.path {
				doomed = append(doomed, r.path)
			}
		}
	}
	// 第二轮才动文件: 此时存活行的文件集合已经算全了。
	for _, p := range doomed {
		if alive[p] || !isRegularFile(p) {
			continue
		}
		rep.FilesGone++
		if !dryRun {
			_ = os.Remove(p)
		}
	}
	rep.RowsAfter = rep.RowsBefore - rep.Dropped
	return rep, nil
}

// orphanDownloadRe 认出**下载器自己**写下的文件名: <纳秒>_<url hash 8 位>.<ext>。
//
// ⚠️ 只删匹配这个形状的文件。池目录**默认就是内置包目录** —— 用户往里丢一张图
// 完全合法 (下次启动会被种子化)。为了"扫个孤儿"把用户刚放进去的图删掉, 是绝对
// 不许发生的事; 形状不对就一律不碰。
var orphanDownloadRe = regexp.MustCompile("^[0-9]{10,}_[0-9a-f]{8}\\.(gif|png|jpe?g|webp)$")

// PruneOrphanDownloads 清掉"下载了但没入池"的残留 (分数没过门槛 / 中途换下一张)。
//
// 判据只有一条: **池里没有任何行指着这个文件**, 且文件名是下载器的形状。
// 目录不存在 / 是空的都算没有孤儿 —— 那不是错误, 不该让运维命令失败。
func (s *Store) PruneOrphanDownloads(dir string, dryRun bool) (int, error) {
	if strings.TrimSpace(dir) == "" {
		return 0, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, nil
	}
	have := map[string]bool{}
	rows, err := s.DB.Query("SELECT COALESCE(local_path,'') FROM stickers")
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return 0, err
		}
		have[filepath.Base(p)] = true
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || have[e.Name()] || !orphanDownloadRe.MatchString(e.Name()) {
			continue
		}
		n++
		if !dryRun {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	return n, nil
}

func isRegularFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}
