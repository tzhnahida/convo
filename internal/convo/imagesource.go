package convo

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/log"
)

// 图片从哪来 —— 这一层只负责「把候选图找来」, 它**不决定能不能用**。
//
// ⚠️ 两道关分开的理由: 图源是可替换的第三方接口 (owner 2026-10-16:
// 「留好接口可以让别人不去 pixiv 去他们自己想去的网站扒」), 而
// 「每张图都必须经过 AI 视觉」是**与图源无关的硬规矩** —— 换站点不该把闸也换掉。

// ImageCandidate 一张候选图 (视觉闸前后的信息都挂在这里, 便于落库追溯)。
type ImageCandidate struct {
	URL       string   `json:"url"`
	PageURL   string   `json:"page_url"`
	Title     string   `json:"title"`
	Author    string   `json:"author"`
	Tags      []string `json:"tags"`
	Source    string   `json:"source"`
	R18       bool     `json:"r18"`
	LocalPath string   `json:"local_path"`
	Digest    string   `json:"digest"`
	Score     float64  `json:"score"`
	NSFW      bool     `json:"nsfw"`
	Note      string   `json:"note"`
}

// ImageSource 图源接口。
//
// 想接自己的网站: 要么用内置的 json / html 两种配置 (不写代码),
// 要么实现这两个方法挂进 NewImageSource 的分支。
type ImageSource interface {
	Name() string
	Fetch(ctx context.Context, query string, n int) ([]ImageCandidate, error)
}

// NewImageSource 按配置造图源。none / 空 = 返回 nil (调用方不必判空)。
func NewImageSource(cfg ImageSourceConfig) (ImageSource, error) {
	cfg = cfg.Normalized()
	switch strings.ToLower(strings.TrimSpace(cfg.Source)) {
	case "", "none":
		return nil, nil
	case "pixiv":
		return &pixivSource{cfg: cfg, hc: newImageHTTPClient(cfg)}, nil
	case "json":
		if strings.TrimSpace(cfg.JSON.URL) == "" {
			return nil, errors.New("images.json.url 为空")
		}
		return &jsonSource{cfg: cfg, hc: newImageHTTPClient(cfg)}, nil
	case "html":
		if strings.TrimSpace(cfg.HTML.URL) == "" || strings.TrimSpace(cfg.HTML.Pattern) == "" {
			return nil, errors.New("images.html.url / pattern 不能为空")
		}
		re, err := regexp.Compile(cfg.HTML.Pattern)
		if err != nil {
			return nil, fmt.Errorf("images.html.pattern: %w", err)
		}
		if re.NumSubexp() < 1 {
			return nil, errors.New("images.html.pattern 必须有**一个捕获组** = 图片地址")
		}
		var r18 *regexp.Regexp
		if p := strings.TrimSpace(cfg.HTML.R18Pattern); p != "" {
			if r18, err = regexp.Compile(p); err != nil {
				return nil, fmt.Errorf("images.html.r18_pattern: %w", err)
			}
		}
		return &htmlSource{cfg: cfg, hc: newImageHTTPClient(cfg), re: re, r18: r18}, nil
	}
	return nil, fmt.Errorf("未知图源 %q (支持 none|pixiv|json|html)", cfg.Source)
}

// newImageHTTPClient 抓图专用的 client —— **只有它**走 images.proxy。
//
// ⚠️ 代理只挂在抓图上, 不碰 AI 调用与通道: 那两条各有各的网络路径,
// 顺手把整进程的代理改掉是那种"平时没事、出事查三天"的写法。
func newImageHTTPClient(cfg ImageSourceConfig) *http.Client {
	tr := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if p := strings.TrimSpace(cfg.Proxy); p != "" {
		u, err := url.Parse(p)
		if err != nil {
			log.Warn("convo:self", "images.proxy 解析失败 (%v) — 退回环境代理", err)
		} else {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	return &http.Client{Timeout: 25 * time.Second, Transport: tr}
}

// ——— pixiv ———

// pixivThumbSize 搜索结果给的是缩略图 URL, 形如
//
//	https://i.pximg.net/c/360x360_70/img-master/.../123_p0_master1200.jpg
//
// 去掉 /c/<尺寸>_<质量> 段就是原图的 master1200 版本 (约 1200px)。
// master1200 对头像与说说都够用, 而且**不用登录**就能拿。
var pixivThumbSize = regexp.MustCompile("/c/\\d+x\\d+(_\\d+)?/")

type pixivSource struct {
	cfg ImageSourceConfig
	hc  *http.Client
}

func (p *pixivSource) Name() string { return "pixiv" }

func (p *pixivSource) Fetch(ctx context.Context, query string, n int) ([]ImageCandidate, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	// ⚠️ mode=safe 是**源头那层** R18 关: 搜都别搜进来。
	// 显式开 r18 时才放宽成 all —— 但图后的视觉闸照样拦 (两层, 不互相代替)。
	mode := "safe"
	if p.cfg.R18 {
		mode = "all"
	}
	base := strings.TrimRight(p.cfg.Pixiv.Base, "/")
	u := fmt.Sprintf("%s/ajax/search/artworks/%s?word=%s&order=%s&mode=%s&p=1&s_mode=s_tag&type=all&lang=zh",
		base, url.PathEscape(query), url.QueryEscape(query), p.cfg.Pixiv.Order, mode)
	body, err := p.get(ctx, u)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Error   bool   `json:"error"`
		Message string `json:"message"`
		Body    struct {
			IllustManga struct {
				Data []struct {
					ID         string   `json:"id"`
					Title      string   `json:"title"`
					URL        string   `json:"url"`
					UserName   string   `json:"userName"`
					Tags       []string `json:"tags"`
					XRestrict  int      `json:"xRestrict"`
					IllustType int      `json:"illustType"`
				} `json:"data"`
			} `json:"illustManga"`
		} `json:"body"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("pixiv 返回不是 JSON (可能被拦/要登录): %w", err)
	}
	if raw.Error {
		return nil, fmt.Errorf("pixiv 报错: %s", raw.Message)
	}
	out := make([]ImageCandidate, 0, n)
	blocked := 0
	for _, it := range raw.Body.IllustManga.Data {
		tags := it.Tags
		if len(tags) == 0 && it.XRestrict == 0 {
			continue
		}
		c := ImageCandidate{
			URL:     pixivThumbSize.ReplaceAllString(it.URL, "/"),
			PageURL: base + "/artworks/" + it.ID,
			Title:   it.Title,
			Author:  it.UserName,
			Tags:    tags,
			Source:  "pixiv",
		}
		if r18q(it.XRestrict, tags, it.Title, p.cfg.R18Words) {
			c.R18 = true
			blocked++
			if !p.cfg.R18 {
				continue
			}
		}
		if c.URL == "" {
			continue
		}
		out = append(out, c)
		if len(out) >= n {
			break
		}
	}
	if blocked > 0 {
		// 计数日志: 不然「R18 关掉了」只是嘴上说。
		log.Info("convo:self", "图源 pixiv 关键词 %q: 命中 R18 %d 张 (r18=%v)", query, blocked, p.cfg.R18)
	}
	return out, nil
}

func (p *pixivSource) get(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	// ⚠️ pixiv 的接口与 i.pximg.net 都认 Referer —— 没有它一律 403。
	req.Header.Set("Referer", strings.TrimRight(p.cfg.Pixiv.Base, "/")+"/")
	req.Header.Set("User-Agent", p.cfg.UserAgent)
	if ck := strings.TrimSpace(p.cfg.Pixiv.Cookie); ck != "" {
		req.Header.Set("Cookie", ck)
	}
	return readAllLimited(p.hc, req, 4<<20)
}

// r18q 源头判定: 分级 + 标签/标题词表。
func r18q(xrestrict int, tags []string, title string, words []string) bool {
	if xrestrict != 0 {
		return true
	}
	hay := strings.ToLower(title + " " + strings.Join(tags, " "))
	for _, w := range words {
		w = strings.ToLower(strings.TrimSpace(w))
		if w == "" {
			continue
		}
		if strings.Contains(hay, w) {
			return true
		}
	}
	return false
}

// ——— json: 任何站点的 JSON 接口 ———

type jsonSource struct {
	cfg ImageSourceConfig
	hc  *http.Client
}

func (j *jsonSource) Name() string { return "json" }

func (j *jsonSource) Fetch(ctx context.Context, query string, n int) ([]ImageCandidate, error) {
	u := strings.NewReplacer("{query}", url.QueryEscape(query), "{n}", fmt.Sprint(n)).Replace(j.cfg.JSON.URL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", j.cfg.UserAgent)
	for k, v := range j.cfg.JSON.Headers {
		req.Header.Set(k, v)
	}
	body, err := readAllLimited(j.hc, req, 4<<20)
	if err != nil {
		return nil, err
	}
	var doc interface{}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("json 图源返回不是 JSON: %w", err)
	}
	arr := dotPath(doc, j.cfg.JSON.ArrayPath)
	items, _ := arr.([]interface{})
	out := make([]ImageCandidate, 0, n)
	for _, it := range items {
		m, _ := it.(map[string]interface{})
		if m == nil {
			continue
		}
		c := ImageCandidate{
			URL:     strField(m, j.cfg.JSON.URLField),
			PageURL: strField(m, j.cfg.JSON.PageField),
			Title:   strField(m, j.cfg.JSON.TitleField),
			Author:  strField(m, j.cfg.JSON.AuthorField),
			Source:  "json",
		}
		if f := strings.TrimSpace(j.cfg.JSON.TagsField); f != "" {
			if ts, ok := m[f].([]interface{}); ok {
				for _, t := range ts {
					c.Tags = append(c.Tags, fmt.Sprint(t))
				}
			}
		}
		if f := strings.TrimSpace(j.cfg.JSON.R18Field); f != "" {
			switch v := m[f].(type) {
			case bool:
				c.R18 = v
			case float64:
				c.R18 = v != 0
			case string:
				c.R18 = r18q(0, nil, v, j.cfg.R18Words)
			}
		}
		if c.R18 || r18q(0, c.Tags, c.Title, j.cfg.R18Words) {
			c.R18 = true
			if !j.cfg.R18 {
				continue
			}
		}
		if c.URL == "" {
			continue
		}
		out = append(out, c)
		if len(out) >= n {
			break
		}
	}
	return out, nil
}

// ——— html: 任何站点的页面 (零依赖) ———

type htmlSource struct {
	cfg ImageSourceConfig
	hc  *http.Client
	re  *regexp.Regexp
	r18 *regexp.Regexp
}

func (h *htmlSource) Name() string { return "html" }

func (h *htmlSource) Fetch(ctx context.Context, query string, n int) ([]ImageCandidate, error) {
	u := strings.NewReplacer("{query}", url.QueryEscape(query), "{n}", fmt.Sprint(n)).Replace(h.cfg.HTML.URL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", h.cfg.UserAgent)
	for k, v := range h.cfg.HTML.Headers {
		req.Header.Set(k, v)
	}
	body, err := readAllLimited(h.hc, req, 4<<20)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]ImageCandidate, 0, n)
	for _, m := range h.re.FindAllStringSubmatch(string(body), -1) {
		if len(m) < 2 {
			continue
		}
		raw := strings.TrimSpace(m[1])
		if raw == "" {
			continue
		}
		if h.r18 != nil && h.r18.MatchString(m[0]) {
			continue
		}
		abs := raw
		if strings.HasPrefix(raw, "//") {
			abs = "https:" + raw
		} else if strings.HasPrefix(raw, "/") && h.cfg.HTML.Base != "" {
			abs = strings.TrimRight(h.cfg.HTML.Base, "/") + raw
		} else if !strings.HasPrefix(raw, "http") && h.cfg.HTML.Base != "" {
			abs = strings.TrimRight(h.cfg.HTML.Base, "/") + "/" + raw
		}
		if !strings.HasPrefix(abs, "http") || seen[abs] {
			continue
		}
		seen[abs] = true
		out = append(out, ImageCandidate{URL: abs, PageURL: u, Source: "html"})
		if len(out) >= n {
			break
		}
	}
	return out, nil
}

// readAllLimited 读一个响应 (限长, 防一个坏接口把内存吃光)。
func readAllLimited(hc *http.Client, req *http.Request, limit int64) ([]byte, error) {
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s 返回 %d", req.URL.Host, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// dotPath 走一个点路径 (data.items / result.list …)。空路径 = 文档自身。
func dotPath(doc interface{}, path string) interface{} {
	path = strings.TrimSpace(path)
	if path == "" {
		return doc
	}
	cur := doc
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return nil
		}
		cur = m[seg]
	}
	return cur
}

func strField(m map[string]interface{}, k string) string {
	k = strings.TrimSpace(k)
	if k == "" {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(m[k]))
}

// ——— 下载 + 视觉闸 ———

// DownloadCandidate 把一张候选图下到本地 (按**内容**命名: 同一张图只有一个文件)。
//
// ⚠️ 文件名用内容 md5 —— 这是表情包那次事故 (§3.10) 的教训:
// 名字带纳秒的主儿能在盘上躺 47 份一模一样的图。
func DownloadCandidate(ctx context.Context, cfg ImageSourceConfig, hc *http.Client, c *ImageCandidate) error {
	if c == nil || strings.TrimSpace(c.URL) == "" {
		return errors.New("候选图没有 URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", cfg.UserAgent)
	if c.Source == "pixiv" {
		req.Header.Set("Referer", strings.TrimRight(cfg.Pixiv.Base, "/")+"/")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载 %s: %d", c.URL, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return errors.New("下到 0 字节")
	}
	sum := md5.Sum(data)
	digest := hex.EncodeToString(sum[:])
	dir := strings.TrimSpace(cfg.Dir)
	if dir == "" {
		dir = "data/convo/self"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ext := imageExt(c.URL, resp.Header.Get("Content-Type"))
	path := filepath.Join(dir, digest[:12]+ext)
	if isRegularFile(path) {
		// 同一张图以前下过 —— 直接用, 不再写一遍。
		c.LocalPath, c.Digest = path, digest
		return nil
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	c.LocalPath, c.Digest = path, digest
	return nil
}

// imageExt 从 URL / Content-Type 猜扩展名 (猜不出给 .jpg)。
func imageExt(rawURL, ctype string) string {
	u := strings.ToLower(rawURL)
	if i := strings.IndexByte(u, '?'); i >= 0 {
		u = u[:i]
	}
	for _, e := range []string{".jpg", ".jpeg", ".png", ".gif", ".webp"} {
		if strings.HasSuffix(u, e) {
			if e == ".jpeg" {
				return ".jpg"
			}
			return e
		}
	}
	ct := strings.ToLower(ctype)
	switch {
	case strings.Contains(ct, "png"):
		return ".png"
	case strings.Contains(ct, "webp"):
		return ".webp"
	case strings.Contains(ct, "gif"):
		return ".gif"
	}
	return ".jpg"
}

// ScreenCandidate 视觉闸 —— **每张图必须过这一道**, 而且是 fail-closed。
//
// 判据三条, 缺一不可:
//
//	① 视觉能用 (没配 / 报错 → 不用这张图, 不是"跳过检查继续用")
//	② 不是 NSFW
//	③ 分数 >= 阈值
//
// ⚠️ 为什么 fail-closed: owner 2026-10-16 的原话是「每张图都必须经过 ai 视觉!!!」。
// 一个"视觉挂了就放行"的闸, 等于没有闸 —— 它会在最需要它的那天失效。
// 返回 (能不能用, 不能用的原因)。
func ScreenCandidate(ctx context.Context, sc VisionScorer, min float64, required bool, c *ImageCandidate) (bool, string) {
	if c == nil || strings.TrimSpace(c.LocalPath) == "" {
		return false, "没有可看的本地图"
	}
	if sc == nil {
		if required {
			return false, "没配视觉 —— 每张图必须过视觉, 所以这张不能用"
		}
		return true, "视觉未配, 且 vision_required=false (已明确放弃那道闸)"
	}
	score, nsfw, note, err := sc.Score(ctx, c.LocalPath)
	if err != nil {
		return false, fmt.Sprintf("视觉调用失败 (%v) — fail-closed, 不用这张", err)
	}
	if nsfw {
		return false, "视觉判定 NSFW"
	}
	if score < min {
		return false, fmt.Sprintf("视觉分数 %.2f < %.2f", score, min)
	}
	c.Score, c.NSFW, c.Note = score, nsfw, note
	return true, ""
}
