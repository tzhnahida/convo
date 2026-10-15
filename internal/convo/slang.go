package convo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tzhnahida/VocabLock/internal/clock"
	"github.com/tzhnahida/VocabLock/internal/log"
)

// ——— 模块 11: 梗学习 (§v2.4 设计) ———
//
// 通道 A 被动学: 每日提取帧带 memes → 用户原话梗直接 active (他说过就是他的梗);
// 通道 B 主动学: 每周日热榜(零鉴权) → LLM 滤梗 → 萌娘百科查释义 → LLM 例句
//              → candidate 试用 (联网学的必须先过"有出处"关, 防编造);
// 通道 C 接住: 用户话语命中梗库 → 注入"你懂这梗" → 自然接住;
// 生命周期: 30 天没用降权 / active 90 天退役 / candidate 21 天没用退役。
//
// 用户定规: 联网学来的自动进库+试用 (管理页可退役), 不做人工确认门槛。

type Slang struct {
	ID          int64   `json:"id"`
	Phrase      string  `json:"phrase"`
	Meaning     string  `json:"meaning"`
	Example     string  `json:"example"`
	SourceQuote string  `json:"source_quote"` // 用户原话证据 (source=user)
	Source      string  `json:"source"`       // user | web
	State       string  `json:"state"`        // active | candidate | retired
	Confidence  float64 `json:"confidence"`
	UseCount    int     `json:"use_count"`
	LastUsedTS  string  `json:"last_used_ts"`
	CreatedTS   string  `json:"created_ts"`
	UpdatedTS   string  `json:"updated_ts"`
}

// SlangDelta 每日提取帧的梗条目 (LLM 输出)。
type SlangDelta struct {
	Phrase      string  `json:"phrase"`
	Meaning     string  `json:"meaning"`
	Example     string  `json:"example"`
	SourceQuote string  `json:"source_quote"`
	Confidence  float64 `json:"confidence"`
}

const (
	SlangActive    = "active"
	SlangCandidate = "candidate"
	SlangRetired   = "retired"
)

// ——— store ops ———

// UpsertSlang 词条入库 (phrase 唯一): 命中 → 合并 (置信度取高, 不复活已退役的词,
// 除非用户又说了它 — source=user 时重新激活)。
func (s *Store) UpsertSlang(m SlangDelta, source string) (int64, error) {
	state := SlangActive
	if source != "user" {
		state = SlangCandidate
	}
	if m.Confidence < 0.7 && source == "user" {
		state = SlangCandidate // 提取方不确定 → 先试用
	}
	conf := m.Confidence
	if conf <= 0 {
		conf = 0.6
	}
	res, err := s.DB.Exec(`INSERT INTO convo_slang(phrase, meaning, example, source_quote, source, state,
			confidence, use_count, last_used_ts, created_ts, updated_ts)
		VALUES(?, ?, ?, ?, ?, ?, ?, 0, '', ?, ?)
		ON CONFLICT(phrase) DO UPDATE SET
			meaning=CASE WHEN excluded.meaning<>'' THEN excluded.meaning ELSE meaning END,
			example=CASE WHEN excluded.example<>'' THEN excluded.example ELSE example END,
			source_quote=CASE WHEN excluded.source_quote<>'' THEN excluded.source_quote ELSE source_quote END,
			confidence=MAX(confidence, excluded.confidence),
			state=CASE WHEN excluded.source='user' THEN 'active'
			           WHEN state='retired' AND excluded.source='web' THEN state
			           ELSE state END,
			updated_ts=excluded.updated_ts`,
		m.Phrase, m.Meaning, m.Example, m.SourceQuote, source, state, conf,
		clock.TodayTS(), clock.TodayTS())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) SlangAll() ([]Slang, error) {
	rows, err := s.DB.Query(`SELECT id, phrase, meaning, example, COALESCE(source_quote,''),
		source, state, confidence, use_count, COALESCE(last_used_ts,''),
		COALESCE(created_ts,''), COALESCE(updated_ts,'')
		FROM convo_slang ORDER BY CASE state WHEN 'active' THEN 0 WHEN 'candidate' THEN 1 ELSE 2 END, confidence DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Slang
	for rows.Next() {
		var sl Slang
		if err := rows.Scan(&sl.ID, &sl.Phrase, &sl.Meaning, &sl.Example, &sl.SourceQuote,
			&sl.Source, &sl.State, &sl.Confidence, &sl.UseCount, &sl.LastUsedTS,
			&sl.CreatedTS, &sl.UpdatedTS); err != nil {
			return nil, err
		}
		out = append(out, sl)
	}
	return out, rows.Err()
}

func slangDecay(lastUsed string, now time.Time) float64 {
	if lastUsed == "" {
		return 0.8
	}
	ts, err := time.Parse("2006-01-02 15:04:05", lastUsed)
	if err != nil {
		return 0.8
	}
	days := now.Sub(ts).Hours() / 24
	switch {
	case days >= 90:
		return 0.3
	case days >= 30:
		return 0.6
	default:
		return 1.0
	}
}

// TopSlang 注入用 top-K: active + candidate, active 优先 (candidate ×0.6 降权试用),
// confidence × 衰减排序。
func (s *Store) TopSlang(limit int) ([]Slang, error) {
	all, err := s.SlangAll()
	if err != nil {
		return nil, err
	}
	now := clock.Now()
	type scored struct {
		sl    Slang
		score float64
	}
	var list []scored
	for _, sl := range all {
		if sl.State == SlangRetired {
			continue
		}
		sc := sl.Confidence * slangDecay(sl.LastUsedTS, now)
		if sl.State == SlangCandidate {
			sc *= 0.6
		}
		list = append(list, scored{sl: sl, score: sc})
	}
	for i := 0; i < len(list) && i < limit; i++ {
		for j := i + 1; j < len(list); j++ {
			if list[j].score > list[i].score {
				list[i], list[j] = list[j], list[i]
			}
		}
	}
	out := make([]Slang, 0, min(limit, len(list)))
	for i := 0; i < len(list) && i < limit; i++ {
		out = append(out, list[i].sl)
	}
	return out, nil
}

// FindSlangHit 用户话语里的已知梗 (接住感知): 返回最长的 2 个命中。
func (s *Store) FindSlangHit(text string) ([]Slang, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	slangs, err := s.TopSlang(50)
	if err != nil {
		return nil, err
	}
	var hits []Slang
	for _, sl := range slangs {
		if len([]rune(sl.Phrase)) < 2 {
			continue
		}
		if strings.Contains(text, sl.Phrase) {
			hits = append(hits, sl)
		}
	}
	// 长词条优先 (越具体越可信)
	for i := 0; i < len(hits); i++ {
		for j := i + 1; j < len(hits); j++ {
			if len([]rune(hits[j].Phrase)) > len([]rune(hits[i].Phrase)) {
				hits[i], hits[j] = hits[j], hits[i]
			}
		}
	}
	if len(hits) > 2 {
		hits = hits[:2]
	}
	return hits, nil
}

// TouchSlang 使用/命中计数 (衰减重置信号)。
func (s *Store) TouchSlang(id int64) error {
	_, err := s.DB.Exec(`UPDATE convo_slang SET use_count=use_count+1, last_used_ts=? WHERE id=?`,
		clock.TodayTS(), id)
	return err
}

// TouchSlangText AI 回复文本里命中的已知梗 → 计数 (生命周期信号)。
func (s *Store) TouchSlangText(text string) {
	slangs, err := s.FindSlangHit(text)
	if err != nil {
		return
	}
	for _, sl := range slangs {
		_ = s.TouchSlang(sl.ID)
	}
}

func (s *Store) SlangByPhrase(phrase string) (*Slang, error) {
	row := s.DB.QueryRow(`SELECT id, phrase, meaning, example, COALESCE(source_quote,''),
		source, state, confidence, use_count, COALESCE(last_used_ts,''),
		COALESCE(created_ts,''), COALESCE(updated_ts,'')
		FROM convo_slang WHERE phrase=?`, phrase)
	var sl Slang
	if err := row.Scan(&sl.ID, &sl.Phrase, &sl.Meaning, &sl.Example, &sl.SourceQuote,
		&sl.Source, &sl.State, &sl.Confidence, &sl.UseCount, &sl.LastUsedTS,
		&sl.CreatedTS, &sl.UpdatedTS); err != nil {
		return nil, err
	}
	return &sl, nil
}

// SetSlangState 管理页: 启用/退役/转正。
func (s *Store) SetSlangState(id int64, state string) error {
	_, err := s.DB.Exec(`UPDATE convo_slang SET state=?, updated_ts=? WHERE id=?`, state, clock.TodayTS(), id)
	return err
}

// UpdateSlang 管理页: 编辑词条内容。
func (s *Store) UpdateSlang(id int64, phrase, meaning, example string) error {
	_, err := s.DB.Exec(`UPDATE convo_slang SET phrase=?, meaning=?, example=?, updated_ts=? WHERE id=?`,
		phrase, meaning, example, clock.TodayTS(), id)
	return err
}

func (s *Store) DeleteSlang(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM convo_slang WHERE id=?`, id)
	return err
}

// RetireOldSlang 生命周期退役: active 90 天没被用过/没被命中 → retired;
// candidate 21 天没用 → retired (试用期出局)。
func (s *Store) RetireOldSlang(activeDays, trialDays int) (int, error) {
	n := 0
	// ⚠️ COALESCE 不够: last_used_ts 是 '' (非 NULL) → NULLIF 兜住空串
	// 否则 julianday('') = NULL → 永不匹配 (2026-09-07 探针实证)
	if res, err := s.DB.Exec(`UPDATE convo_slang SET state=?, updated_ts=?
		WHERE state='active' AND julianday(COALESCE(NULLIF(last_used_ts,''), created_ts)) < julianday(?)`,
		SlangRetired, clock.TodayTS(), cutoffTS(activeDays)); err == nil {
		if c, _ := res.RowsAffected(); c > 0 {
			n += int(c)
		}
	}
	if res, err := s.DB.Exec(`UPDATE convo_slang SET state=?, updated_ts=?
		WHERE state='candidate' AND julianday(COALESCE(NULLIF(last_used_ts,''), created_ts)) < julianday(?)`,
		SlangRetired, clock.TodayTS(), cutoffTS(trialDays)); err == nil {
		if c, _ := res.RowsAffected(); c > 0 {
			n += int(c)
		}
	}
	return n, nil
}

func cutoffTS(days int) string {
	return clock.Now().AddDate(0, 0, -days).Format("2006-01-02 15:04:05")
}

// ——— 通道 B: 主动学梗 (零鉴权热榜链) ———

type HotMemeCycle struct {
	Chatter Chatter     // LLM (滤梗/释义)
	Store   *Store      // 词条落库
	Moegirl *MoegirlWeb // 萌娘百科释义查询 (有出处才学)
	HTTP    *http.Client
}

// Run 一轮: 热榜 → LLM 挑 ≤3 个真梗 → 萌娘查释义 (查不到=丢) → LLM 例句 → candidate 入库。
func (h *HotMemeCycle) Run(ctx context.Context) error {
	if h.Chatter == nil || h.Store == nil {
		return fmt.Errorf("hotmeme: chatter/store 未配置")
	}
	hc := h.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	terms, err := h.hotTerms(ctx, hc)
	if err != nil {
		return err
	}
	if len(terms) == 0 {
		return fmt.Errorf("hotmeme: 热榜为空")
	}
	picked, err := h.pickMemes(ctx, terms)
	if err != nil || len(picked) == 0 {
		log.Info("convo:slang", "hotmeme: 无候选通过 LLM 过滤 (err=%v)", err)
		return nil
	}
	added := 0
	for _, phrase := range picked {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		excerpt, err := h.lookup(ctx, phrase)
		if err != nil {
			log.Warn("convo:slang", "hotmeme: 「%s」百科无出处 — 跳过 (防编造)", phrase)
			continue
		}
		meaning, example, err := h.explain(ctx, phrase, excerpt)
		if err != nil {
			log.Warn("convo:slang", "hotmeme: 释义失败 %s: %v", phrase, err)
			continue
		}
		if _, err := h.Store.UpsertSlang(SlangDelta{
			Phrase: phrase, Meaning: meaning, Example: example, Confidence: 0.6,
		}, "web"); err != nil {
			log.Warn("convo:slang", "hotmeme: insert %s: %v", phrase, err)
			continue
		}
		added++
		log.Info("convo:slang", "hotmeme: 新词入库 「%s」 (%s)", phrase, meaning)
	}
	log.Info("convo:slang", "hotmeme cycle done: terms=%d picked=%d added=%d", len(terms), len(picked), added)
	return nil
}

// hotTerms 热榜候选: B站热搜 → 微博热搜 (双源, 都零鉴权; ⚠️ 接口形态以 Pi 实测为准)。
func (h *HotMemeCycle) hotTerms(ctx context.Context, http *http.Client) ([]string, error) {
	if terms, err := h.bilibiliHot(ctx, http); err == nil && len(terms) > 0 {
		return terms, nil
	} else if err != nil {
		log.Warn("convo:slang", "bilibili hot: %v — 换微博热搜", err)
	}
	return h.weiboHot(ctx, http)
}

func (h *HotMemeCycle) bilibiliHot(ctx context.Context, hc *http.Client) ([]string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET",
		"https://s.search.bilibili.com/main/hotword?limit=20", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("bilibili %d", resp.StatusCode)
	}
	var body struct {
		Code int `json:"code"`
		List []struct {
			Keyword  string `json:"keyword"`
			ShowName string `json:"show_name"`
		} `json:"list"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	var out []string
	for _, it := range body.List {
		kw := it.Keyword
		if it.ShowName != "" {
			kw = it.ShowName
		}
		if kw != "" {
			out = append(out, kw)
		}
	}
	return out, nil
}

func (h *HotMemeCycle) weiboHot(ctx context.Context, hc *http.Client) ([]string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET",
		"https://weibo.com/ajax/side/hotSearch", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Referer", "https://weibo.com/")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("weibo %d", resp.StatusCode)
	}
	var body struct {
		OK   int `json:"ok"`
		Data struct {
			Realtime []struct {
				Word string `json:"word"`
			} `json:"realtime"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	var out []string
	for _, it := range body.Data.Realtime {
		if it.Word != "" {
			out = append(out, it.Word)
		}
	}
	return out, nil
}

// pickMemes LLM 挑真梗: 排除事件/人名/影视/商品/纯英文/过气老梗; 最多 3 个。
func (h *HotMemeCycle) pickMemes(ctx context.Context, terms []string) ([]string, error) {
	if len(terms) > 30 {
		terms = terms[:30]
	}
	out, err := h.Chatter.Chat(ctx, "你输出单行 JSON。", `下面是一批今日热搜词。
挑 1-3 个真正是"网络梗/流行语/大家都在复读的话"的。排除: 时事事件、人名、明星八卦、影视剧名、商品名、纯英文词、已经过气很久的老梗。
输出单行 JSON: {"pick":["词"]} (一个都不合适 → {"pick":[]})

热搜词:
`+strings.Join(terms, "\n"))
	if err != nil {
		return nil, err
	}
	raw, err := extractJSON(out)
	if err != nil {
		return nil, err
	}
	var r struct {
		Pick []string `json:"pick"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	var picked []string
	for _, p := range r.Pick {
		p = strings.TrimSpace(p)
		if p == "" || len([]rune(p)) > 12 {
			continue
		}
		picked = append(picked, p)
	}
	if len(picked) > 3 {
		picked = picked[:3]
	}
	return picked, nil
}

// lookup 萌娘百科查释义 (有出处才学 — 防编造铁律)。
func (h *HotMemeCycle) lookup(ctx context.Context, phrase string) (string, error) {
	if h.Moegirl == nil {
		return "", fmt.Errorf("moegirl 未配置")
	}
	titles, err := h.Moegirl.SearchTitles(ctx, phrase)
	if err != nil {
		return "", err
	}
	for _, title := range titles {
		text, err := h.Moegirl.Scrape(ctx, title)
		// ⚠️ **按字符判、按字符切。** `len(text)` 是字节, `text[:1500]` 也是字节 ——
		// 中文一个字三字节, 切一半就是**非法 UTF-8**。
		//
		// ⚠️ 而这条文本会流进**黑话学习**, 再作为注入段进提示词。
		// 2026-10-15 那次"轨迹里有一行读不出来"就是同一类脏字节。
		if err != nil || len([]rune(text)) < 40 {
			continue
		}
		text = truncateStr(text, 1500)
		return text, nil
	}
	return "", fmt.Errorf("无可用条目")
}

// explain LLM 生成含义+用例 (方言: 拟人口吻的微信式用例)。
func (h *HotMemeCycle) explain(ctx context.Context, phrase, excerpt string) (string, string, error) {
	out, err := h.Chatter.Chat(ctx, "你输出单行 JSON。", fmt.Sprintf(`「%s」是一个网络梗。下面是百科摘录:
%s
用一两句话解释这梗的意思/用法 (中文, ≤40 字), 再给一个"动漫少女和好友微信聊天"里的自然用例 (一句话, ≤30 字, 口语)。
输出单行 JSON: {"meaning":"...","example":"..."}`, phrase, excerpt))
	if err != nil {
		return "", "", err
	}
	raw, err := extractJSON(out)
	if err != nil {
		return "", "", err
	}
	var r struct {
		Meaning string `json:"meaning"`
		Example string `json:"example"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", "", err
	}
	if r.Meaning == "" {
		return "", "", fmt.Errorf("空释义")
	}
	return r.Meaning, r.Example, nil
}
