package convo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// ——— 模块 8: 表情包生命周期 (§7.4/§8.7) ———
//
// P0 = 全链实现 (用户拍板: "试跑=正式参数化"):
//   搜索(源可配: builtin/firecrawl/tenor) → 下载 <lang>/ → vision 打分
//   (符合人设?有梗?会用吗 0-1 + NSFW 剔除) → 自选 5-10 张喜欢位 →
//   affinity 加权发送 (单张概率 ≤30% 封顶) → 反馈涨跌 (长回复+0.1/无视-0.05/收藏+0.1)
//   → <0.3 退役; 周期: 启动即搜一轮 + 每周日 02:00 增量 (正式=每月)。

type Sticker struct {
	ID        int64   `json:"id"`
	SrcURL    string  `json:"src_url"`
	LocalPath string  `json:"local_path"`
	Lang      string  `json:"lang"`
	Affinity  float64 `json:"affinity"`
	UseCount  int     `json:"use_count"`
	Score     float64 `json:"score"`
	Source    string  `json:"source"`
	Note      string  `json:"note"` // v2.4: 含义/适用语境 (vision 生成 or 用户梗图收录)
	RetiredTS string  `json:"retired_ts,omitempty"`
}

// ——— store ops ———

func (s *Store) StickerExists(id int64) bool {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM stickers WHERE id=? AND retired_ts IS NULL`, id).Scan(&n)
	return err == nil && n > 0
}

func (s *Store) StickerByID(id int64) (*Sticker, error) {
	row := s.DB.QueryRow(`SELECT id, src_url, local_path, lang, affinity, use_count, score, source, COALESCE(note,''), COALESCE(retired_ts,'')
		FROM stickers WHERE id=?`, id)
	var st Sticker
	if err := row.Scan(&st.ID, &st.SrcURL, &st.LocalPath, &st.Lang, &st.Affinity,
		&st.UseCount, &st.Score, &st.Source, &st.Note, &st.RetiredTS); err != nil {
		return nil, err
	}
	return &st, nil
}

// ActiveStickers 未退役列表 (发送池)。
func (s *Store) ActiveStickers() ([]Sticker, error) {
	rows, err := s.DB.Query(`SELECT id, src_url, local_path, lang, affinity, use_count, score, source, COALESCE(note,''), COALESCE(retired_ts,'')
		FROM stickers WHERE retired_ts IS NULL ORDER BY affinity DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sticker
	for rows.Next() {
		var st Sticker
		if err := rows.Scan(&st.ID, &st.SrcURL, &st.LocalPath, &st.Lang, &st.Affinity,
			&st.UseCount, &st.Score, &st.Source, &st.Note, &st.RetiredTS); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// SetStickerNote v2.4: 表情包含义/适用语境标注。
func (s *Store) SetStickerNote(id int64, note string) error {
	_, err := s.DB.Exec(`UPDATE stickers SET note=? WHERE id=?`, note, id)
	return err
}

// StickerBySrc 按 src_url 查一条 —— 偷图去重要用。
func (s *Store) StickerBySrc(src string) (int64, bool) {
	var id int64
	if err := s.DB.QueryRow("SELECT id FROM stickers WHERE src_url = ? LIMIT 1", src).Scan(&id); err != nil {
		return 0, false
	}
	return id, true
}

// SetAffinity 直接设定 affinity (不是增量)。
//
// ⚠️ 偷图要的是**按流行度重算**, 不是"再加一点" ——
// 增量会让同一张图被反复加成分, 最后不可解释。
func (s *Store) SetAffinity(id int64, v float64) error {
	if v < 0 {
		v = 0
	}
	if v > 1 {
		v = 1
	}
	_, err := s.DB.Exec("UPDATE stickers SET affinity = ? WHERE id = ?", v, id)
	return err
}

// InsertSticker 新候选入库 (打分直入喜欢位 — §8.7 步骤 4: affinity=打分)。
//
// ⚠️ 它**自己不去重** —— 去重在调用方: 先 StickerBySrcURL 问地址, 下完再
// StickerByDigest 问内容。为什么不在这一层做: 这里拿不到「刚下的那份文件要不要删」,
// 而那正是重复文件堆积的入口 (2026-10-16 的 473 行事故)。
//
// digest = 图片内容 md5 (空 = 不知道, 那样这一行永远不参与内容去重)。
func (s *Store) InsertSticker(srcURL, localPath, lang, source string, score float64, digest string) (int64, error) {
	res, err := s.DB.Exec(`INSERT INTO stickers(src_url, local_path, lang, affinity, score, source, born_ts, digest)
		VALUES(?,?,?, ?, ?, ?, ?, ?)`,
		srcURL, localPath, lang, score, score, source, clock.TodayTS(), strings.ToLower(strings.TrimSpace(digest)))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) AdjustAffinity(id int64, delta float64) error {
	_, err := s.DB.Exec(`UPDATE stickers SET affinity=MIN(1.0, MAX(0.0, affinity+?)), use_count=use_count+1 WHERE id=?`,
		delta, id)
	return err
}

func (s *Store) RetireLow(low float64) (int, error) {
	res, err := s.DB.Exec(`UPDATE stickers SET retired_ts=?
		WHERE retired_ts IS NULL AND affinity < ?`, clock.TodayTS(), low)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// TrimActivePool 喜欢位上限: 超过 cap 张 → 按 affinity 退役最低者 (§8.7 轮换规则)。
func (s *Store) TrimActivePool(cap int) (int, error) {
	if cap <= 0 {
		return 0, nil
	}
	res, err := s.DB.Exec(`UPDATE stickers SET retired_ts=?
		WHERE id IN (
			SELECT id FROM (
				SELECT id FROM stickers WHERE retired_ts IS NULL
				ORDER BY affinity DESC, id DESC LIMIT -1 OFFSET ?
			)
		)`, clock.TodayTS(), cap)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// WeightedPick affinity 加权随机选一张; 单张概率 ≤ cap (30%) 防同质。
// 采样: 轮盘 — 每张权重 = min(affinity, capN) — 简化: affinity 上限 = cap。
func (s *Store) WeightedPick(cap float64) (*Sticker, error) {
	pool, err := s.ActiveStickers()
	if err != nil {
		return nil, err
	}
	if len(pool) == 0 {
		return nil, fmt.Errorf("no active stickers")
	}
	total := 0.0
	weights := make([]float64, len(pool))
	for i, st := range pool {
		w := st.Affinity
		if w > cap {
			w = cap
		}
		if w <= 0 {
			w = 0.01
		}
		weights[i] = w
		total += w
	}
	r := DiceFloat() * total
	acc := 0.0
	for i, w := range weights {
		acc += w
		if r <= acc {
			return &pool[i], nil
		}
	}
	return &pool[len(pool)-1], nil
}

// ——— 搜索源 ———

// SearchResult 一个候选图片。
type SearchResult struct {
	URL  string `json:"url"`
	Alt  string `json:"alt"`
	From string `json:"from"` // builtin | firecrawl | tenor
}

// StickerSearcher 搜索源接口 (适配器模式, §8.7 步骤 1 源配置)。
type StickerSearcher interface {
	Search(ctx context.Context, keywords []string, limit int) ([]SearchResult, error)
}

// ——— firecrawl 未认证限额 (零注册兜底, ⚠️ 限量) ———

type FirecrawlSearcher struct {
	Base string // https://api.firecrawl.dev
	Key  string // 空 = 未认证限额
	HTTP *http.Client
}

func (f *FirecrawlSearcher) Search(ctx context.Context, keywords []string, limit int) ([]SearchResult, error) {
	q := strings.Join(keywords, " ")
	// ⚠️ **必须是 POST** —— 与 learnsearch.go 同一条 (2026-10-14 实测)。
	// Firecrawl v2 的 /search 不接受 GET, 拿 GET 打过去是 405,
	// 而那个错误长得像"没配 key": searched 永远是 0。
	// ⚠️ **`sources:["images"]` 是关键** (2026-10-14 实测)。
	//
	// 不带它时, 搜索返回的是**网页地址** (pixiv 作品页 / Pinterest 图钉 / B站视频页),
	// 而 DownloadImage 会去 GET 那些网页 —— 超时、412、或者拿到一坨 HTML
	// 再被 extFromURL 默认命名成 `.gif`。那就是当初那 4 个「内容是网页的 gif」。
	//
	// 带上它之后, 返回的是 **imageUrl 直链** —— 真正能下载的图片。
	// (另一条路 —— scrape 页面再抽 <img> —— 实测噪声太大:
	//  一个 pixiv 页抽出 79 个图, 大部分是 logo 和 UI 图标。)
	reqBody, _ := json.Marshal(map[string]interface{}{"query": q, "limit": 5, "sources": []string{"images"}})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, f.Base+"/v2/search", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	if f.Key != "" {
		req.Header.Set("Authorization", "Bearer "+f.Key)
	}
	resp, err := f.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// ⚠️ 响应结构: `data` 是 `{"web":[...]}` (新版) 或数组 (旧版) —— 两种都收。
	var raw struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	// ⚠️ 两种形状都收:
	//   images 模式 → data.images[].imageUrl (**图片直链**)
	//   web   模式 → data.web[].url        (网页地址, 只在回退时用)
	var wrap struct {
		Images []struct {
			Title    string `json:"title"`
			ImageURL string `json:"imageUrl"`
			URL      string `json:"url"`
			Width    int    `json:"imageWidth"`
			Height   int    `json:"imageHeight"`
		} `json:"images"`
		Web []struct {
			Title string `json:"title"`
			URL   string `json:"url"`
		} `json:"web"`
	}
	if json.Unmarshal(raw.Data, &wrap) != nil {
		// 旧版 data 是数组 —— 当成 web 处理。
		var flat []struct {
			Title string `json:"title"`
			URL   string `json:"url"`
		}
		_ = json.Unmarshal(raw.Data, &flat)
		for _, d := range flat {
			wrap.Web = append(wrap.Web, struct {
				Title string `json:"title"`
				URL   string `json:"url"`
			}{d.Title, d.URL})
		}
	}
	var out []SearchResult
	for _, im := range wrap.Images {
		u := strings.TrimSpace(im.ImageURL)
		if u == "" || IsJunkImageURL(u, im.Width, im.Height) {
			continue
		}
		out = append(out, SearchResult{URL: u, Alt: im.Title, From: "firecrawl-img"})
		if len(out) >= limit {
			return out, nil
		}
	}
	// 回退: images 一条都没用时才用网页地址。
	for _, d := range wrap.Web {
		out = append(out, SearchResult{URL: d.URL, Alt: d.Title, From: "firecrawl"})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// IsJunkImageURL 明显不是表情包的图片 —— 早丢比下完再丢便宜。
//
// ⚠️ 判据都很保守: **宁可多下一次**, 也不要因为一条过严的规则把好图挡在外面。
func IsJunkImageURL(u string, w, h int) bool {
	low := strings.ToLower(u)
	if strings.HasSuffix(strings.Split(low, "?")[0], ".svg") {
		return true // 矢量图: 视觉 API 不吃, 而且多半是 logo
	}
	// 图标尺寸 —— 16~48px 那种是 UI 元素, 不是表情包。
	if w > 0 && h > 0 && w <= 48 && h <= 48 {
		return true
	}
	// 极端长宽比 = 横幅/分割线。
	if w > 0 && h > 0 {
		r := float64(w) / float64(h)
		if r > 8 || r < 0.125 {
			return true
		}
	}
	return false
}

// ——— Tenor 源 (免费 key, 拿到即切) ———

type TenorSearcher struct {
	Key  string
	HTTP *http.Client
}

func (t *TenorSearcher) Search(ctx context.Context, keywords []string, limit int) ([]SearchResult, error) {
	q := strings.Join(keywords, " ")
	url := fmt.Sprintf("https://tenor.com/v2/search?q=%s&key=%s&limit=%d&media_filter=minimal&safesearch=moderate",
		urlQueryEscape(q), t.Key, limit)
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := t.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Results []struct {
			ID           string `json:"id"`
			MediaFormats map[string]struct {
				URL string `json:"url"`
			} `json:"media_formats"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	var out []SearchResult
	for _, r := range body.Results {
		if f, ok := r.MediaFormats["gifmedium"]; ok {
			out = append(out, SearchResult{URL: f.URL, From: "tenor"})
		} else if f, ok := r.MediaFormats["gif"]; ok {
			out = append(out, SearchResult{URL: f.URL, From: "tenor"})
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// NewSearcher 按配置选源 (none/builtin → nil, Run 内短路零网络)。
func NewSearcher(cfg StickerConfig) StickerSearcher {
	http := &http.Client{Timeout: 15 * time.Second} // 2026-09-06 修复: 硬超时防挂死
	switch cfg.SearchDriver {
	case "tenor":
		if cfg.TenorAPIKey != "" {
			return &TenorSearcher{Key: cfg.TenorAPIKey, HTTP: http}
		}
		log.Warn("convo:sticker", "tenor 无 key — 降级 firecrawl")
		return &FirecrawlSearcher{Base: cfg.FirecrawlBase, HTTP: http}
	case "builtin", "none":
		return nil
	default:
		return &FirecrawlSearcher{Base: cfg.FirecrawlBase, HTTP: http}
	}
}

// AbsMediaPath 把媒体路径转成**绝对路径**。
//
// ⚠️ 必须转 —— 桥接方 (nimbus) 用**它自己的 cwd** 解析相对路径,
// 而 convo 的 cwd 是 /home/tzh/convo, nimbus 的是 /home/tzh/.nimbus。
// 同一个 `data/convo/stickers/x.gif` 在两边指向不同文件。
//
// 实测 (2026-10-14): 群表情三次发送失败 —— `image 文件不存在`,
// 而文件在 convo 那边明明是好的。
func AbsMediaPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

// DownloadImage 下载候选到 <dir>/<lang>/ (失败返回错误 — 404 兜底换下一张)。
func DownloadImage(ctx context.Context, hc *http.Client, url, dir, lang string) (string, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("download %s: %d", url, resp.StatusCode)
	}
	ext := extFromURL(url)
	name := fmt.Sprintf("%d_%s%s", clock.Now().UnixNano(), hash8(url), ext)
	path := filepath.Join(dir, name)
	out, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer out.Close()
	if _, err = io.Copy(out, resp.Body); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	// ⚠️ **下完必须验魔数** (2026-10-14 实测)。
	//
	// 搜图下回来的东西里混着 403 页 / 跳转页 / HTML 壳 ——
	// 它们会按 URL 后缀被存成 `.gif`, 然后 vision API 回 400
	// 「请确保是 webp/png/jpeg/gif」。**那个报错是对的, 骗人的是扩展名。**
	//
	// 后果曾经是: searched=5 scored=0 —— 搜到了, 下回来的全不是图,
	// 而日志只说"vision 失败", 看不出是下载下了个网页。
	if _, _, ok := SniffImageFile(path); !ok {
		_ = os.Remove(path)
		return "", fmt.Errorf("下载到的不是图片 (内容与扩展名 %s 不符): %s", ext, url)
	}
	return path, nil
}

func urlQueryEscape(s string) string {
	return url.QueryEscape(s)
}

// hash8 低 32 位 hex。⚠️ 2026-09-07 修: 原来 %08x 再 [:8] 截的是**高**位 hex —
// 差异性落在低位 (h*31 溢出后高位几乎恒定), 导致 remember mem_id 全撞同一 id (测试实证)。
func hash8(s string) string {
	h := 0
	for _, r := range s {
		h = h*31 + int(r)
	}
	return fmt.Sprintf("%08x", uint32(h))
}

func extFromURL(u string) string {
	u = strings.Split(u, "?")[0]
	lower := strings.ToLower(u)
	for _, ext := range []string{".gif", ".png", ".jpg", ".jpeg", ".webp", ".mp4"} {
		if strings.HasSuffix(lower, ext) {
			return ext
		}
	}
	return ".gif"
}
