package convo

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/tzhnahida/VocabLock/internal/log"
)

// ─────────────────────────────────────────────────────────────
// §6.3 判据① / §2.5 的话题词表
//
// 原先两者的「话题」都只能取**命中的梗** —— 于是「它对什么话题有话想说」
// 实际退化成「它对哪些梗有话想说」, 而梗表是外来的、稀疏的、跟当天聊什么无关。
//
// ⚠️ **裁定 (2026-10-13): 单开一张词表, 但形态是「夜间一次 LLM 抽取 + 确定性兜底」。**
//   理由: 白天只做**字符串匹配**, 热路径一次 LLM 都不加 ——
//   判据① 每一条群消息都要跑, 让它接 LLM 就是成本结构的变化。
//   而抽取一天只要一次, 失败还能退回 TopTerms (确定性, 零成本)。
// ─────────────────────────────────────────────────────────────

// EnsureTopicKeySchema 建表。
func EnsureTopicKeySchema(db *sql.DB) error {
	_, err := db.Exec("CREATE TABLE IF NOT EXISTS convo_topic_key (" +
		"owner TEXT NOT NULL, day TEXT NOT NULL, key TEXT NOT NULL, " +
		"PRIMARY KEY (owner, day, key))")
	return err
}

// SaveTopicKeys 落一天的话题键 (同一天重算则**先清后写** —— 它是读数不是流水)。
func (s *Store) SaveTopicKeys(owner, day string, keys []string) error {
	if _, err := s.DB.Exec("DELETE FROM convo_topic_key WHERE owner = ? AND day = ?", owner, day); err != nil {
		return err
	}
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if len([]rune(k)) < 2 {
			continue
		}
		if _, err := s.DB.Exec("INSERT OR IGNORE INTO convo_topic_key (owner, day, key) VALUES (?,?,?)",
			owner, day, k); err != nil {
			return err
		}
	}
	return nil
}

// RecentTopicKeys 取最近 days 天的话题键 (去重, 新近优先)。
func (s *Store) RecentTopicKeys(owner string, days int) ([]string, error) {
	if days <= 0 {
		days = 7
	}
	rows, err := s.DB.Query("SELECT key FROM convo_topic_key WHERE owner = ? "+
		"GROUP BY key ORDER BY MAX(day) DESC", owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// MatchTopicKey 在某段文本里找命中的话题键。
//
// ⚠️ **最长优先**: 「爵士乐历史」和「爵士乐」同时在表里时,
// 报长的那个才有信息量 —— 报短的会让判据① 的粒度糊掉。
// ⚠️ 同长度按字典序, 保证确定性 (map 迭代顺序是随机的)。
func MatchTopicKey(text string, keys []string) (string, bool) {
	t := strings.TrimSpace(text)
	if t == "" || len(keys) == 0 {
		return "", false
	}
	best := ""
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if len([]rune(k)) < 2 || !strings.Contains(t, k) {
			continue
		}
		if len([]rune(k)) > len([]rune(best)) || (len(k) == len(best) && k < best) {
			best = k
		}
	}
	if best == "" {
		return "", false
	}
	return best, true
}

// topicKeySystemPrompt 话题抽取的提示词。
func topicKeySystemPrompt() string {
	return "下面是一天的群聊与私聊记录。抽出 3-5 个**当天真正在聊的话题词**。\n" +
		"严格输出单行 JSON: {\"keys\":\"[\"话题1\",\"话题2\"]}\n" +
		"要求: 每个话题 2-6 个字的名词或短语; **不要**输出「聊天」「日常」「其他」这类空词;" +
		"不要输出人名; 按重要程度排序。"
}

// ParseTopicKeys 解析抽取结果。
func ParseTopicKeys(raw string) ([]string, bool) {
	b, err := extractJSON(raw)
	if err != nil {
		return nil, false
	}
	var f struct {
		Keys []string `json:"keys"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, false
	}
	var out []string
	seen := map[string]bool{}
	for _, k := range f.Keys {
		k = strings.TrimSpace(k)
		n := len([]rune(k))
		if n < 2 || n > 8 || seen[k] || topicKeyStop(k) {
			continue
		}
		seen[k] = true
		out = append(out, k)
		if len(out) >= 5 {
			break
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// topicKeyStop 空词表。⚠️ 这些词进了表, 判据① 会对**每一句话**都命中。
var topicKeyStopWords = []string{"聊天", "日常", "其他", "话题", "内容", "对话", "生活", "事情"}

func topicKeyStop(k string) bool {
	for _, wd := range topicKeyStopWords {
		if k == wd {
			return true
		}
	}
	return false
}

// ExtractTopicKeys 夜间抽一次话题键。**失败退回 TopTerms** (确定性, 零成本)。
//
// ⚠️ 退回不是降级告警, 是设计的一部分: TopTerms 的 2-gram 统计本来就是
// 一个够用的兜底, 而「今天群里聊了什么」这句话在 LLM 挂了的时候也应当有人回答。
func ExtractTopicKeys(ctx context.Context, chatter Chatter, msgs []FoldMsg) []string {
	fallback := TopTerms(msgs, 8)
	if chatter == nil || len(msgs) == 0 {
		return fallback
	}
	var b strings.Builder
	for i, m := range msgs {
		if i >= 200 {
			break
		}
		b.WriteString(m.Text + "\n")
	}
	out, err := chatter.Chat(ctx, topicKeySystemPrompt(), b.String())
	if err != nil {
		log.Warn("convo:topic", "话题抽取失败, 退回 2-gram 统计: %v", err)
		return fallback
	}
	keys, ok := ParseTopicKeys(out)
	if !ok {
		log.Warn("convo:topic", "话题抽取结果不合规, 退回 2-gram 统计")
		return fallback
	}
	return keys
}

// TopicKeysSorted 排序去重 (测试与管理页读数用)。
func TopicKeysSorted(keys []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// topicKeyDay 供夜间批使用 (与 FoldMsg 的日期无关, 显式传)。
func topicKeyDay(day string) string {
	if _, ok := ParseDay(day); !ok {
		return time.Now().Format("2006-01-02")
	}
	return day
}
