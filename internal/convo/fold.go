package convo

import (
	"database/sql"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// foldLabelPrefixRe 视觉标注的前缀：`(表情 — 陈述)`、`（照片）` 这种。
//
// ⚠️ 只剥**开头**的、带括号的 —— 正文里出现「表情」两个字是真在说话，不能动。
var foldLabelPrefixRe = regexp.MustCompile(`^[（(]\s*(表情|照片|图片|视频|文件|语音)[^）)]{0,24}[）)]\s*`)

// foldImageMarkRe 图注标记：「[图: 一个表情]」「[图: 一张图, 没细看]」。
//
// ⚠️ 折叠话题前必须先剥掉它 —— 否则图注里的词会变成"今天在聊什么"。
var foldImageMarkRe = regexp.MustCompile(`\[图[:：][^\]]*\]`)

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
	// Quote 当天最具体的一句话 (真人原话的截断, 取自最热话题那一条)。
	//
	// ⚠️ 2026-11-03 用户实测: 日终回顾每天写成「今天群里128条消息、9个人、主要在聊：表情、照片」,
	// 那是**监控周报**, 不是"我记得今天"。人回忆一天只会想起**一件具体的事**,
	// 所以这里带一句原话 (确定性挑选, 不接 LLM —— 决策器不接 LLM)。
	Quote string
	Text  string
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
		// ⚠️ 2026-11-03: 只在**去掉图注之后**的正文里找话题。
		//
		// 实测回顾写成「主要在聊：表情、照片」—— 这两个词来自图片标注
		// (「[图: 一个表情]」那种), 不是群里聊的内容。图注是"发生了什么",
		// 不是"在说什么", 混进话题 = 把相机日志当成聊天记录。
		plain := FoldPlainText(m.Text)
		if plain == "" {
			continue
		}
		for _, k := range keywords {
			k = strings.TrimSpace(k)
			// ⚠️ 2026-10-20: 词表混进了单字/标点碎片, 回顾于是显示「主要在聊：—、[、:」。
			// 话题必须**至少两个实义字**, 且整串不许混标点。
			if k == "" || !saneFoldKeyword(k) || !strings.Contains(plain, k) {
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
	if len(r.Topics) > 0 {
		r.Quote = foldQuote(msgs, r.Topics[0])
	}
	r.Text = FoldText(r)
	return r
}

// foldQuote 当天关于 topic 的**第一句像样的话** (确定性挑选)。
//
// ⚠️ 不选最长/最热闹的: 那需要打分, 而打分需要一个尺度 —— 确定性挑选只要
// "第一句够长、且不是只提了这个词本身"。重复运行时结果必须逐字节相同。
func foldQuote(msgs []FoldMsg, topic string) string {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return ""
	}
	for _, m := range msgs {
		plain := FoldPlainText(m.Text)
		if !strings.Contains(plain, topic) {
			continue
		}
		rs := []rune(plain)
		// 太短的 (只有「表情」「耳机」这种) 算不上"一句具体的话"。
		if len(rs) < 6 {
			continue
		}
		// 折进去的是记忆, 不是来回对话 —— 截断到 28 字够表意。
		if len(rs) > 28 {
			return string(rs[:28]) + "…"
		}
		return plain
	}
	return ""
}

// FoldPlainText 去掉图注/占位标记之后的正文，折录用。
//
// ⚠️ 保留一句话的其余部分: 「[图: 一个表情] 这也太草了」剥完还剩「这也太草了」,
// 那是真的在说话; 而纯图消息剥完是空串 → 它不进话题统计 (也就不会造出
// 「主要在聊：表情」这种相机日志)。
func FoldPlainText(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	// C1 (2026-11-06 用户批准): **先剥视觉标注的前缀**。
	//
	// ⚠️ 图注落库的样子是 `(表情 — 陈述) [图: …]` —— 剥了 [图: …] 之后,
	// 剩下的 `(表情 — 陈述)` 还留在正文里, 于是「表情」成了当天最高频的话题词。
	// 这正是 11-03～11-05 每天都折出「主要在聊：表情」的原因 (不是群友在聊表情,
	// 是**标注自己在当话题**)。
	out := s
	for i := 0; i < 3; i++ { // 最多剥三层 (偶尔会叠两个标注)
		next := foldLabelPrefixRe.ReplaceAllString(out, " ")
		if next == out {
			break
		}
		out = next
	}
	out = foldImageMarkRe.ReplaceAllString(out, " ")
	out = strings.NewReplacer("[图片]", " ", "[表情]", " ", "[图]", " ", "[图没读到]", " ", "[语音]", " ").Replace(out)
	return strings.Join(strings.Fields(out), " ")
}

// saneFoldKeyword 这个词配不配当"话题"。
// 单字、纯标点、图片占位一律拒 —— 它们是词表没洗干净的碎片, 不是话题。
func saneFoldKeyword(k string) bool {
	rs := []rune(k)
	// ⚠️ 2026-11-03 试过改成「至少两个实义字」来挡「表」「情」「示」——**退回**了:
	// 「猫」「狗」「饭」都是真话题, 一刀切会把它们一起砍掉 (TestFold_TopicsRankedAndFallback 钉着)。
	// 碎片由另外两道闸收拾: ① 下面这条"必须全是字母/数字/汉字"挡住 —、[、:;
	// ② 折叠时的子串合并把「表」「情」并进「表情」。
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
//
// ⚠️ 2026-11-03 用户实测: 原来的「今天群里128条消息、9个人、主要在聊：表情、照片」
// 是**监控周报**, 不是回忆。人回想一天只会记得**一件具体的事**:
// 所以现在说的是"聊了什么、有人说了哪句话", 条目数与人头数只留作读数 (落库), 不进这句话。
func FoldText(r FoldResult) string {
	if r.Total == 0 {
		return ""
	}
	if len(r.Topics) == 0 {
		return "今天群里没什么正经话题，随便扯了几句"
	}
	topic := r.Topics[0]
	// C2 (2026-11-06): 「聊得最多的是」还是排行榜口吻 —— 人回想一天不会说"最多的是",
	// 只会说"在聊啥"。所以去掉排名腔, 保留「有人说到」那句具体的话。
	if r.Quote == "" {
		return "今天群里在聊「" + topic + "」"
	}
	return "今天群里在聊「" + topic + "」，有人说到「" + r.Quote + "」"
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
