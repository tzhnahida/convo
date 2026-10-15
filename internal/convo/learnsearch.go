package convo

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────
// §2.5 自主学习: **真去搜**
//
// 原来的 runLearning 根本没搜 —— 它让 LLM 直接产出一条"知识",
// origin 还是模型自己写的「看了篇科普」。也就是说:
// **它学到的每一条都可能是编的, 而且来历也是编的。**
//
// 这是「学来的东西不许当经历」那条硬线的**同胞**:
// 学来的东西也不许是**编**的 —— 否则它会在某个群里当成谈资说出来,
// 而那句话是假的, 且没有人能查。
//
// ⚠️ 所以这一版的铁律是: **搜不到就不学**。绝不退回"让模型自己编一条"。
// ─────────────────────────────────────────────────────────────

// WebHit 一条搜索结果。
// ⚠️ 与 stickers 的 SearchResult **刻意不同**: 那个只需要 图URL + alt,
// 而学习需要**摘要** —— 没有摘要, 模型只能拿到标题去猜内容, 那还是编。
type WebHit struct {
	Title string
	URL   string
	Desc  string
}

// LearnSearcher 学习用的搜索口。
type LearnSearcher interface {
	SearchWeb(ctx context.Context, query string, limit int) ([]WebHit, error)
}

// FirecrawlLearnSearch 复用 stickers 那套 Firecrawl 通道, 但**保留摘要**。
type FirecrawlLearnSearch struct {
	Base string
	Key  string // 空 = 未认证限额
	HTTP *http.Client
}

// SearchWeb 真搜一次。返回空 = 没搜到 (调用方据此**不产出碎片**)。
func (f *FirecrawlLearnSearch) SearchWeb(ctx context.Context, query string, limit int) ([]WebHit, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 5
	}
	base := strings.TrimRight(f.Base, "/")
	if base == "" {
		base = "https://api.firecrawl.dev"
	}
	// ⚠️ **必须是 POST** (2026-10-14 实测)。
	// Firecrawl v2 的 /search 已经**不接受 GET** —— 拿 GET 打过去是 405:
	//   {"success":false,"code":"METHOD_NOT_ALLOWED","error":"GET /v2/search is not supported."}
	//
	// 而这个错误**长得像"没配 key"**: 搜索一直返回空, 于是
	//   · 自主学习永远学不到东西 (searched=0)
	//   · 对话内搜索永远走"搜不到就反问"
	//   · 表情包循环永远 searched=0
	// 排查时我一度以为要配 key —— 其实**不需要 key, 是方法错了**。
	reqBody, _ := json.Marshal(map[string]interface{}{"query": q, "limit": limit})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v2/search", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if f.Key != "" {
		req.Header.Set("Authorization", "Bearer "+f.Key)
	}
	cli := f.HTTP
	if cli == nil {
		cli = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// ⚠️ 响应结构也变了: `data` 从**数组**变成了 `{"web":[...]}`。
	// 两种都收 —— 服务端回滚时不该让整条管线再死一次。
	var raw struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	type fhit struct {
		Title       string `json:"title"`
		URL         string `json:"url"`
		Description string `json:"description"`
	}
	var flat []fhit
	if json.Unmarshal(raw.Data, &flat) != nil {
		var wrap struct {
			Web []fhit `json:"web"`
		}
		_ = json.Unmarshal(raw.Data, &wrap)
		flat = wrap.Web
	}
	var out []WebHit
	for _, d := range flat {
		if strings.TrimSpace(d.URL) == "" {
			continue
		}
		out = append(out, WebHit{Title: d.Title, URL: d.URL, Desc: d.Description})
	}
	return out, nil
}

// learnSearcher 从配置里现造一个搜索口。
//
// ⚠️ 它**不缓存**: 每次学习跑一次, 一周 2-3 次, 造一个 http.Client 的代价可以忽略,
// 而缓存会让"改了配置要重启"这件事变成一个隐藏的坑。
func (e *Engine) learnSearcher() LearnSearcher {
	// 可注入 (测试用): 设了就赢 —— 否则测试要真的打网络。
	if e.LearnSearch != nil {
		return e.LearnSearch
	}
	base := strings.TrimSpace(e.Cfg.Stickers.FirecrawlBase)
	if base == "" {
		base = "https://api.firecrawl.dev"
	}
	return &FirecrawlLearnSearch{
		Base: base,
		Key:  strings.TrimSpace(os.Getenv("FIRECRAWL_API_KEY")),
		HTTP: &http.Client{Timeout: 20 * time.Second},
	}
}

// FormatHits 把搜索结果摊成给模型看的文本。
func FormatHits(hits []WebHit, max int) string {
	if max <= 0 {
		max = 5
	}
	var b strings.Builder
	for i, h := range hits {
		if i >= max {
			break
		}
		b.WriteString("- ")
		b.WriteString(strings.TrimSpace(h.Title))
		b.WriteString("\n  ")
		b.WriteString(strings.TrimSpace(h.Desc))
		b.WriteString("\n  来源: ")
		b.WriteString(h.URL)
		b.WriteString("\n")
	}
	return b.String()
}
