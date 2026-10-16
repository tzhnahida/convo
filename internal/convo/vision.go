package convo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/ai"
	"github.com/tzhnahida/convo/internal/log"
)

var osRemove = os.Remove // test hook

// ——— 表情包 vision 打分 (§8.7 步骤 3) ———

// VisionScorer 打分接口 (测试可 mock)。v2.4: note = 图意/适用语境一句话 (可空)。
type VisionScorer interface {
	Score(ctx context.Context, imagePath string) (score float64, nsfw bool, note string, err error)
}

// DeepSeekScorer 用 deepseek-v4-flash-vision-exp (同 key, ~0.001 元/图; ⚠️ Exp 实验态)。
type DeepSeekScorer struct {
	Client *ai.Client
	Model  string
	Mem    string // persona 摘要 (符合人设判定用)
}

const visionSystem = `你是表情包鉴赏家。看这张图, 评估它适不适合作为聊天表情包发给朋友。

标准:
- 符合人设/有梗/这张图会不会被朋友觉得好笑或可爱
- 输出单行 JSON: {"score":0.0,"nsfw":false,"note":"<一句话: 这图表达的意思/适用语境, ≤15 字; 说不清就给空串>","reason":"<一句话>"}
- score 0-1: 0.7+ 值得入库, 0.4-0.7 一般, <0.4 不适合
- nsfw: 擦边/血腥/政治/广告水印 → true`

func (s *DeepSeekScorer) Score(ctx context.Context, imagePath string) (float64, bool, string, error) {
	prompt := "人设背景: " + s.Mem + "\n\n评估这张表情包。"
	out, err := s.Client.ChatVision(ctx, s.Model, visionSystem, prompt, imagePath)
	if err != nil {
		return 0, false, "", err
	}
	raw, err := extractJSON(out)
	if err != nil {
		return 0, false, "", err
	}
	var res struct {
		Score  float64 `json:"score"`
		NSFW   bool    `json:"nsfw"`
		Note   string  `json:"note"`
		Reason string  `json:"reason"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, false, "", err
	}
	if res.Score < 0 || res.Score > 1 {
		return 0, false, "", nil // 模型跑飞 → 视为无效
	}
	if res.NSFW {
		log.Info("convo:sticker", "nsfw filtered: %s", imagePath)
	}
	if runes := []rune(res.Note); len(runes) > 15 {
		res.Note = string(runes[:15]) + "…"
	}
	return res.Score, res.NSFW, res.Note, nil
}

// ——— 自搜自选链 (§8.7 全流程, P0 周期: 启动即搜 + 每周日) ———

type StickerCycle struct {
	Cfg      StickerConfig
	Searcher StickerSearcher
	Scorer   VisionScorer
	Mem      string // persona 摘要 (关键词/人设)
	// Download 可注入 (仿真用假下载避免网络); 默认 DownloadImage。
	Download func(ctx context.Context, hc *http.Client, url, dir, lang string) (string, error)
}

// Run 一轮: 搜 → 下载 → 打分 → 自选 5-10 张喜欢位 → 退役低分。
//
// ⚠️ 2026-09-06 端到端修复: search_driver 必须真正短路 —
// "none" 直接跳过 (零网络); "builtin" 只做内置目录种子 (零网络);
// 仅 firecrawl/tenor 走网络链。此前配置无效 → boot cycle 无条件调
// firecrawl, 无超时 + HTML 拦截页未处理 → goroutine 挂死。
func (c *StickerCycle) Run(ctx context.Context, store *Store) error {
	switch c.Cfg.SearchDriver {
	case "none":
		log.Info("convo:sticker", "search_driver=none — 周期跳过 (零网络)")
		return nil
	case "builtin":
		return c.seedBuiltin(store)
	}
	if c.Searcher == nil {
		return fmt.Errorf("searcher nil (driver 未配置或为 none/builtin)")
	}
	// ⚠️ **逐条搜, 不拼成一句** —— 见 keywords 的注释。
	// 上限 3 条: 再多只是同一批图的不同排法, 而每次搜索都真花钱。
	seenURL := map[string]bool{}
	var results []SearchResult
	for i, q := range c.keywords(store) {
		if i >= 3 || ctx.Err() != nil {
			break
		}
		rs, err := c.Searcher.Search(ctx, []string{q}, 15)
		if err != nil {
			log.Warn("convo:sticker", "search %q: %v", q, err)
			continue
		}
		for _, r := range rs {
			if !seenURL[r.URL] {
				seenURL[r.URL] = true
				results = append(results, r)
			}
		}
	}
	if len(results) == 0 {
		log.Info("convo:sticker", "三条查询一条都没搜到 — 跳过本轮")
		return nil
	}
	hc := &http.Client{Timeout: 15 * time.Second} // 2026-09-06 修复: 下载/搜索硬超时防挂死
	dir := c.Cfg.Dir + "/" + c.Cfg.Lang
	scored := 0
	dl := c.Download
	if dl == nil {
		dl = DownloadImage
	}
	for _, r := range results {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		path, err := dl(ctx, hc, r.URL, dir, c.Cfg.Lang)
		if err != nil {
			log.Debug("convo:sticker", "download skip %s: %v", r.URL, err)
			continue // 404 兜底: 换下一张 (§8.7 风险)
		}
		var score float64
		var note string
		nsfw := false
		if c.Scorer != nil && c.Cfg.VisionEnabled {
			score, nsfw, note, err = c.Scorer.Score(ctx, path)
			if err != nil {
				log.Warn("convo:sticker", "vision %s: %v", path, err)
				score = 0.5 // 降级: 白名单直配 (⚠️ 没真看过)
			}
		} else {
			score = 0.5 // ⚠️ 未配 vision: 直配 0.5, 明确标注
		}
		if nsfw {
			_ = osRemove(path)
			continue
		}
		if score < c.Cfg.ScoreMin {
			continue
		}
		id, err := store.InsertSticker(r.URL, path, c.Cfg.Lang, r.From, score)
		if err != nil {
			log.Warn("convo:sticker", "insert: %v", err)
			continue
		}
		if note != "" { // v2.4: 图意标注
			_ = store.SetStickerNote(id, note)
		}
		scored++
		if scored >= 10 {
			break
		}
	}
	// 退役清理 (喜欢位外): <0.3 退役 + 池上限 (§8.7 喜欢位受控)
	if n, err := store.RetireLow(0.3); err == nil && n > 0 {
		log.Info("convo:sticker", "retired %d (affinity<0.3)", n)
	}
	if n, err := store.TrimActivePool(c.Cfg.PoolCap); err == nil && n > 0 {
		log.Info("convo:sticker", "trimmed %d (pool cap %d)", n, c.Cfg.PoolCap)
	}
	log.Info("convo:sticker", "cycle done: searched=%d scored=%d", len(results), scored)
	return nil
}

// keywords: 有角色 profile → 角色名+萌点 (二次元表情包海量且贴人设);
// 否则通用关键词。
// seedBuiltin 内置包: 从 BuiltinDir 目录扫文件入库 (零网络)。
func (c *StickerCycle) seedBuiltin(store *Store) error {
	dir := c.Cfg.BuiltinDir
	if dir == "" {
		dir = c.Cfg.Dir + "/" + c.Cfg.Lang
	}
	if n, err := store.RetireLow(0.3); err == nil && n > 0 {
		log.Info("convo:sticker", "retired %d (affinity<0.3)", n)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Info("convo:sticker", "builtin: 目录 %s 不存在或为空 (%v)，仅执行退役清理", dir, err)
		return nil
	}
	added := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".gif" && ext != ".png" && ext != ".jpg" && ext != ".jpeg" && ext != ".webp" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if _, err := store.InsertSticker("builtin://"+e.Name(), path, c.Cfg.Lang, "builtin", 0.5); err == nil {
			added++
		}
	}
	log.Info("convo:sticker", "builtin seeded: %d", added)
	return nil
}

// keywords 表情包的搜索词。
//
// ⚠️ **短查询, 而且分开搜** (2026-10-14 实测)。
//
// 原来它们会被拼成一句:
//
//	"纳西妲 表情包 ja かわいい 纳西妲 表情 二次元 可愛い"
//
// 而图片搜索对长查询几乎无解 —— 要匹配到同时含全部词的页面, 那种页面不存在。
// 短查询各自都能命中, 合并去重后再下载。
func (c *StickerCycle) keywords(store *Store) []string {
	if store != nil {
		if p, err := store.LoadProfile(); err == nil && p != nil {
			name := strings.TrimSpace(p.Name)
			if name == "" {
				name = "二次元"
			}
			return []string{
				name + " 表情包",
				name + " 表情 gif",
				"二次元 表情包 gif",
			}
		}
	}
	return []string{"二次元 表情包 gif", "可爱 表情包", "meme sticker gif"}
}
