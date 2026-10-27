package convo

import (
	"database/sql"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// ─────────────────────────────────────────────────────────────
// §7.1 群消息日终折叠
//
// 一天的群消息在日终压成**一条**，以 origin=ai_to_ai 落库 ——
// 它进「记忆」，不进「对话上下文」(RecentMessages 已经过滤 ai_to_ai)。
//
// ⚠️ 为什么必须折叠而不是全存：群消息是**外部噪声**，全量进记忆会把
// 它自己的记忆冲淡成一个群聊记录仪。折叠是**配额**，不是优化。
// ⚠️ 为什么必须幂等：日终任务是定时器驱动的，重跑/补跑是常态。
// 同一天折两次会让记忆里出现两条互相矛盾的「今天」。
// ─────────────────────────────────────────────────────────────

// FoldMsg 一条待折叠的群消息。
type FoldMsg struct {
	SenderUIN int64
	Nick      string
	Text      string
	TS        time.Time
}

// FoldResult 折叠结果。Text 为空 = 这一天没什么可记的（**不是错误**）。
type FoldResult struct {
	Total    int
	Speakers int
	Topics   []string
	Text     string
}

const FoldDefaultMaxTop = 3

type foldStat struct {
	hits, first int
}

// foldRank 一个话题的排序键。
type foldRank struct {
	key   string
	hits  int
	first int
}

// FoldGroupDay 把一天的消息折成一条。
// keywords = 已知的实体/梗表（来自 entities.go / slang.go）。
// 话题由**关键词命中数**决定，不由模型总结 —— 决策器不接 LLM。
//
// ⚠️ 排序必须是**确定性**的：(命中数降序, 首次出现升序, 词序)。
// 少了"首次出现"这一项，同分话题的顺序会随 map 迭代随机变化 ——
// 同一天折两次会得到两条不同的记忆，"幂等"就成了空话。
func FoldGroupDay(msgs []FoldMsg, keywords []string, maxTop int) FoldResult {
	var r FoldResult
	if len(msgs) == 0 {
		return r
	}
	if maxTop <= 0 {
		maxTop = FoldDefaultMaxTop
	}
	r.Total = len(msgs)
	speakers := map[int64]bool{}
	stats := map[string]*foldStat{}
	for i, m := range msgs {
		if m.SenderUIN != 0 {
			speakers[m.SenderUIN] = true
		}
		for _, k := range keywords {
			k = strings.TrimSpace(k)
			// ⚠️ 2026-10-20: 词表混进了单字/标点碎片, 回顾于是显示「主要在聊：—、[、:」。
			// 话题必须**至少两个实义字**, 且整串不许混标点。
			if k == "" || !saneFoldKeyword(k) || !strings.Contains(m.Text, k) {
				continue
			}
			s, ok := stats[k]
			if !ok {
				s = &foldStat{first: i}
				stats[k] = s
			}
			s.hits++
		}
	}
	r.Speakers = len(speakers)
	ranks := make([]foldRank, 0, len(stats))
	for k, s := range stats {
		ranks = append(ranks, foldRank{key: k, hits: s.hits, first: s.first})
	}
	sort.Slice(ranks, func(i, j int) bool {
		if ranks[i].hits != ranks[j].hits {
			return ranks[i].hits > ranks[j].hits
		}
		if ranks[i].first != ranks[j].first {
			return ranks[i].first < ranks[j].first
		}
		return ranks[i].key < ranks[j].key
	})
	// ⚠️ 2026-10-27: 碎片合并 —— 词表里同时有「表」「情」「表情」时, 回顾会写成
	// 「主要在聊：表、情、表情」。**是别人真子串的, 丢短的**, 只留最长那个。
	kept := ranks[:0]
	for _, rk := range ranks {
		subsumed := false
		for _, other := range ranks {
			if other.key != rk.key && len([]rune(other.key)) > len([]rune(rk.key)) && strings.Contains(other.key, rk.key) {
				subsumed = true
				break
			}
		}
		if !subsumed {
			kept = append(kept, rk)
		}
	}
	ranks = kept
	for i := 0; i < len(ranks) && i < maxTop; i++ {
		r.Topics = append(r.Topics, ranks[i].key)
	}
	r.Text = FoldText(r)
	return r
}

// saneFoldKeyword 这个词配不配当"话题"。
// 单字、纯标点、图片占位一律拒 —— 它们是词表没洗干净的碎片, 不是话题。
func saneFoldKeyword(k string) bool {
	rs := []rune(k)
	if len(rs) < 1 {
		return false
	}
	for _, r := range rs {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.Is(unicode.Han, r) {
			return false
		}
	}
	return true
}

// FoldText 折叠结果 → 一句话。
func FoldText(r FoldResult) string {
	if r.Total == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("今天群里")
	b.WriteString(strconv.Itoa(r.Total))
	b.WriteString("条消息")
	if r.Speakers > 0 {
		b.WriteString("、")
		b.WriteString(strconv.Itoa(r.Speakers))
		b.WriteString("个人")
	}
	if len(r.Topics) > 0 {
		b.WriteString("，主要在聊：")
		b.WriteString(strings.Join(r.Topics, "、"))
	} else {
		b.WriteString("，没什么正经话题，随便扯了几句")
	}
	return b.String()
}

// FoldLine 折叠结果 → 记忆文本。空结果返回 false（**没有这一天**，不是空记忆）。
func FoldLine(r FoldResult) (string, bool) {
	if strings.TrimSpace(r.Text) == "" {
		return "", false
	}
	return r.Text, true
}

// EnsureFoldSchema 建表。PRIMARY KEY(owner, scene_key, day) 是幂等的**承载者** ——
// 幂等不能靠调用方自觉，必须由数据库约束保证。
func EnsureFoldSchema(db *sql.DB) error {
	_, err := db.Exec("CREATE TABLE IF NOT EXISTS convo_fold (" +
		"owner TEXT NOT NULL, " +
		"scene_key TEXT NOT NULL, " +
		"day TEXT NOT NULL, " +
		"total INTEGER NOT NULL DEFAULT 0, " +
		"speakers INTEGER NOT NULL DEFAULT 0, " +
		"topics TEXT NOT NULL DEFAULT '', " +
		"text TEXT NOT NULL DEFAULT '', " +
		"created_ts TEXT NOT NULL DEFAULT '', " +
		"PRIMARY KEY (owner, scene_key, day))")
	return err
}

// SaveFold 落一条日终折叠。inserted=false 表示这天已经折过了（**正常，不是错误**）。
func SaveFold(db *sql.DB, owner, sceneKey, day string, r FoldResult) (bool, error) {
	res, err := db.Exec("INSERT OR IGNORE INTO convo_fold "+
		"(owner, scene_key, day, total, speakers, topics, text, created_ts) VALUES (?,?,?,?,?,?,?,?)",
		owner, sceneKey, day, r.Total, r.Speakers, strings.Join(r.Topics, ","), r.Text,
		time.Now().Format(time.RFC3339))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ParseDay 把 "2006-01-02" 解析成当地零点。
func ParseDay(day string) (time.Time, bool) {
	t, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(day), time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// DailyMessages 取某场景某一天的**真人消息** (折录用)。
// ⚠️ 排除 ai_to_ai: 折叠自己的产物不该成为下一次折叠的输入 (会自我放大)。
func (s *Store) DailyMessages(sceneKey, day string) ([]FoldMsg, error) {
	start, ok := ParseDay(day)
	if !ok {
		return nil, nil
	}
	end := start.Add(24 * time.Hour)
	rows, err := s.DB.Query("SELECT ts, payload_json, sender_uin FROM convo_messages "+
		"WHERE scene_key = ? AND origin <> 'ai_to_ai' AND ts >= ? AND ts < ? ORDER BY id ASC",
		sceneKey, start.Format(bodyTSLayout), end.Format(bodyTSLayout))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FoldMsg
	for rows.Next() {
		var ts, payload, uin string
		if err := rows.Scan(&ts, &payload, &uin); err != nil {
			return nil, err
		}
		m := FoldMsg{Text: payload, SenderUIN: parseInt64(uin)}
		if t, ok := ParseMsgTS(ts); ok {
			m.TS = t
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func parseInt64(s string) int64 {
	var n int64
	neg := false
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "-") {
		neg, s = true, s[1:]
	}
	for _, c := range s {
		if c < 48 || c > 57 {
			return 0
		}
		n = n*10 + int64(c-48)
	}
	if neg {
		return -n
	}
	return n
}

// GroupScenesOnDay 这一天有消息的群场景。
func (s *Store) GroupScenesOnDay(day string) ([]string, error) {
	start, ok := ParseDay(day)
	if !ok {
		return nil, nil
	}
	end := start.Add(24 * time.Hour)
	rows, err := s.DB.Query("SELECT DISTINCT scene_key FROM convo_messages "+
		"WHERE scene_key LIKE 'group:%' AND origin <> 'ai_to_ai' AND ts >= ? AND ts < ?",
		start.Format(bodyTSLayout), end.Format(bodyTSLayout))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sk string
		if err := rows.Scan(&sk); err != nil {
			return nil, err
		}
		out = append(out, sk)
	}
	return out, rows.Err()
}

// CountMessagesInHourRange 某一天 [fromH, toH) 小时区间内的消息条数 (失眠成因要用「半夜还有人找」)。
func (s *Store) CountMessagesInHourRange(day string, fromH, toH int) (int, error) {
	start, ok := ParseDay(day)
	if !ok {
		return 0, nil
	}
	from := start.Add(time.Duration(fromH) * time.Hour)
	to := start.Add(time.Duration(toH) * time.Hour)
	var n int
	err := s.DB.QueryRow("SELECT COUNT(*) FROM convo_messages WHERE origin = 'user' AND ts >= ? AND ts < ?",
		from.Format(bodyTSLayout), to.Format(bodyTSLayout)).Scan(&n)
	return n, err
}

// TopTerms 从当天的消息里抽出现次数最多的 2-gram 作为话题候选。
//
// ⚠️ 为什么不用外部词表: 折叠要回答的是"**今天**群里聊了什么",
// 而词表回答的是"这个系统认识哪些词"。两者不等价 —— 新话题永远进不来。
// 2-gram 是零依赖的, 且**重复出现**本身就是"这是个话题"的证据 (一次的是噪声)。
func TopTerms(msgs []FoldMsg, n int) []string {
	if n <= 0 {
		n = 12
	}
	count := map[string]int{}
	first := map[string]int{}
	seq := 0
	for _, m := range msgs {
		r := []rune(m.Text)
		for i := 0; i+1 < len(r); i++ {
			a, b := string(r[i]), string(r[i+1])
			if assocStopChars[a] || assocStopChars[b] {
				continue
			}
			p := a + b
			if _, ok := first[p]; !ok {
				first[p] = seq
				seq++
			}
			count[p]++
		}
	}
	type kv struct {
		k string
		c int
	}
	all := make([]kv, 0, len(count))
	for k, c := range count {
		if c < 2 {
			continue // 只出现一次的 2-gram 是噪声, 不是话题
		}
		all = append(all, kv{k, c})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].c != all[j].c {
			return all[i].c > all[j].c
		}
		if first[all[i].k] != first[all[j].k] {
			return first[all[i].k] < first[all[j].k]
		}
		return all[i].k < all[j].k
	})
	out := make([]string, 0, n)
	for i := 0; i < len(all) && i < n; i++ {
		out = append(out, all[i].k)
	}
	return out
}

// AllDailyMessages 某一天**全部场景**的消息 (话题抽取用)。
//
// ⚠️ 不能用 DailyMessages("", day) 代替 —— 那只会匹配 scene_key 为空串的行,
// 而写入侧 (insertMessage) 会把空场景键补成默认值, 于是**一条都拿不到**:
// 话题词表永远是空的, 判据① 永远退回梗表, 而编译与测试全绿。
func (s *Store) AllDailyMessages(day string) ([]FoldMsg, error) {
	start, ok := ParseDay(day)
	if !ok {
		return nil, nil
	}
	end := start.Add(24 * time.Hour)
	rows, err := s.DB.Query("SELECT ts, payload_json, sender_uin FROM convo_messages "+
		"WHERE origin <> 'ai_to_ai' AND ts >= ? AND ts < ? ORDER BY id ASC",
		start.Format(bodyTSLayout), end.Format(bodyTSLayout))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FoldMsg
	for rows.Next() {
		var ts, payload, uin string
		if err := rows.Scan(&ts, &payload, &uin); err != nil {
			return nil, err
		}
		m := FoldMsg{Text: payload, SenderUIN: parseInt64(uin)}
		if t, ok := ParseMsgTS(ts); ok {
			m.TS = t
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
