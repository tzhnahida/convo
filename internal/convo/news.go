package convo

import (
	"context"
	"database/sql"
	"strings"

	"github.com/tzhnahida/VocabLock/internal/clock"
	"github.com/tzhnahida/VocabLock/internal/log"
)

// ─────────────────────────────────────────────────────────────
// 它自己去看世界上发生了什么
//
// §3.5 原文: 「来源(按优先级降级): **真实数据源** → 脚本化时间线 → LLM 生成的合理事件」。
//
// ⚠️ 我实现 §3.6 世界事件时**直接跳到了第三档** —— 全让 LLM 编。
// 于是它对外面的世界一无所知: 问它热点它说"我闭关呢", 问它时事它反问"你说的是哪个"。
// 真人不是这样的 —— 真人**本来就知道**今天发生了什么, 因为他早上刷过手机。
//
// ⚠️ 三条纪律:
//   ① **只存搜到的** —— 搜不到就不存 (与 §2.5 同一条: 搜不到就不学, 绝不编);
//   ② **不许当谈资硬塞** (§3.5 硬约束④: 不拿事件索取注意力);
//   ③ **有保温期** —— 热点过了就不该再提, 隔三天说"最近那个新闻"是假的。
// ─────────────────────────────────────────────────────────────

// NewsKeepHours 新闻的保温期。
//
// ⚠️ 48 小时: 短了它早上看的到晚上就忘, 长了它会拿三天前的事当"最近"。
const NewsKeepHours = 48

// NewsItem 一条它看到的事。
type NewsItem struct {
	Title   string
	URL     string
	Summary string
	TS      string
}

// EnsureNewsSchema 建表。
func EnsureNewsSchema(db *sql.DB) error {
	_, err := db.Exec("CREATE TABLE IF NOT EXISTS convo_news (" +
		"id INTEGER PRIMARY KEY AUTOINCREMENT, owner TEXT NOT NULL DEFAULT '', " +
		"ts TEXT NOT NULL DEFAULT '', title TEXT NOT NULL DEFAULT '', " +
		"url TEXT NOT NULL DEFAULT '', summary TEXT NOT NULL DEFAULT '')")
	return err
}

// SaveNews 存一条。**只存搜到的** —— 调用方不许把编的东西送进来。
func (s *Store) SaveNews(owner string, n NewsItem) error {
	t := strings.TrimSpace(n.Title)
	if t == "" {
		return nil
	}
	// ⚠️ **抓来的文本必须先过 UTF-8 清洗。**
	//
	// 来源(搜索 / 网页)不受我们控制, 它们可能给出**半个字符**:
	// 生产实测 2026-10-14 的决策轨迹里有一条 system_prompt **根本不是合法 UTF-8** ——
	// 坏点是一个三字节汉字**只剩前两字节**(`e8 82`), 而任何只按 UTF-8 读的工具
	// (包括我自己的控制台与排障脚本)在那一行上**直接报错**, 整条轨迹读不出来。
	//
	// ⚠️ 洗在**写入口**而不是渲染时: 洗在渲染时只能救那一个出口,
	// 而脏字节会跟着这条记录流向每一个读它的地方。
	ts := strings.TrimSpace(n.URL)
	sum := strings.ToValidUTF8(strings.TrimSpace(n.Summary), "\uFFFD")
	_, err := s.DB.Exec("INSERT INTO convo_news (owner, ts, title, url, summary) VALUES (?,?,?,?,?)",
		owner, clock.Now().Format(bodyTSLayout), strings.ToValidUTF8(t, "\uFFFD"), ts, sum)
	return err
}

// RecentNews 保温期内的新闻, 新的在前。
func (s *Store) RecentNews(owner string, limit int) ([]NewsItem, error) {
	if limit <= 0 {
		limit = 6
	}
	since := clock.Now().Add(-NewsKeepHours * 3600 * 1e9).Format(bodyTSLayout)
	rows, err := s.DB.Query("SELECT COALESCE(title,''), COALESCE(url,''), COALESCE(summary,''), COALESCE(ts,'') "+
		"FROM convo_news WHERE owner = ? AND ts >= ? ORDER BY id DESC LIMIT ?", owner, since, limit)
	if err != nil {
		// 表还没建 = 还没抓过。**不是错误。**
		return nil, nil
	}
	defer rows.Close()
	var out []NewsItem
	for rows.Next() {
		var n NewsItem
		if rows.Scan(&n.Title, &n.URL, &n.Summary, &n.TS) == nil {
			out = append(out, n)
		}
	}
	return out, nil
}

// NewsNote 渲染注入。空 = **一个字都不注入** (样本不足不占位)。
//
// ⚠️ 措辞里必须带"别硬塞"那条 —— 否则它会变成每日新闻播报,
// 而那是 §3.5 硬约束④ 明确禁止的: **不许当工具**。
func NewsNote(items []NewsItem) string {
	// ⚠️ **先过滤, 再判空** —— 反过来的话, 一个全空标题的列表会渲染出
	// 一个只有头尾、中间什么都没有的段。那是占位, 不是内容。
	var kept []NewsItem
	for _, it := range items {
		if strings.TrimSpace(it.Title) != "" {
			kept = append(kept, it)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n【你这两天刷到的】(不是别人告诉你的, 是你自己看到的)\n")
	for _, n := range kept {
		t := strings.TrimSpace(n.Title)
		b.WriteString("- ")
		b.WriteString(t)
		if s := strings.TrimSpace(n.Summary); s != "" {
			b.WriteString(" —— ")
			b.WriteString(truncateStr(s, 60))
		}
		b.WriteString("\n")
	}
	b.WriteString("⚠️ **别硬塞。** 聊到了顺口提一句就行; 没人聊就让它过去。\n")
	b.WriteString("也**别当成你亲历的** —— 你是刷到的, 不是你在场。")
	return b.String()
}

// newsQueries 抓取用的查询词。
//
// ⚠️ 两条就够: 一条是"大家都在聊什么"(能接群里的茬),
// 一条是"我这行有什么新的"(与 §2.5 的品味方向对齐)。
// 多了会把它变成新闻聚合器 —— 而它是个朋友, 不是资讯台。
func newsQueries(p *PersonaProfile) []string {
	qs := []string{"今日热点新闻"}
	if p != nil {
		if v := strings.TrimSpace(p.Attrs["interests"]); v != "" {
			q := truncateStr(v, 12)
			if q != "" {
				qs = append(qs, q+" 最新")
			}
		}
	}
	return qs
}

// FetchDailyNews 去看一眼世界。**搜不到就不存** —— 与 §2.5 学习同一条纪律。
func (e *Engine) FetchDailyNews(ctx context.Context, p *PersonaProfile) int {
	if e.Store == nil {
		return 0
	}
	searcher := e.learnSearcher()
	owner := e.ownerKey()
	n := 0
	for _, q := range newsQueries(p) {
		hits, err := searcher.SearchWeb(ctx, q, 5)
		if err != nil {
			log.Warn("convo:news", "「%s」搜索失败 —— 不存: %v", q, err)
			continue
		}
		if len(hits) == 0 {
			log.Info("convo:news", "「%s」没搜到东西 —— 不存", q)
			continue
		}
		for _, h := range hits {
			if err := e.Store.SaveNews(owner, NewsItem{
				Title: h.Title, URL: h.URL, Summary: h.Desc,
			}); err != nil {
				log.Warn("convo:news", "落库失败: %v", err)
			} else {
				n++
			}
		}
	}
	if n > 0 {
		log.Info("convo:news", "§3.5 刷到 %d 条 (真实来源, 保温 %d 小时)", n, NewsKeepHours)
	}
	return n
}

// newsNote 取保温期内的新闻并渲染。
func (e *Engine) newsNote() string {
	if e.Store == nil {
		return ""
	}
	items, err := e.Store.RecentNews(e.ownerKey(), 6)
	if err != nil {
		return ""
	}
	return NewsNote(items)
}
