package convo

import (
	"database/sql"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────
// §2.6.3 迸发时机② 「被顶」的识别器
//
// 设计稿只说「用户反驳 → 重新组织立场 → 新观点」, 没给判据。
//
// ⚠️ **裁定 (2026-10-13): 用廉价判据, 不加 LLM 分类。**
//   理由一: 「决策器不接 LLM」是硬纪律, 为它开一个**每轮都调**的口子
//           是成本结构的变化, 不是一次实现选择。
//   理由二: 被顶的判据在**文本表面**就已经足够清楚 ——
//           「不是」「不对」+ 与它上一句共享线索, 两条同时满足。
//
// ⚠️ 第二条判据 (共享线索) 不是锦上添花: 少了它, 用户在**别的话题上**
// 说一句「不是这样的」就会被算成「它被顶了」, 于是它去重组织一个跟这事无关的立场。
// ─────────────────────────────────────────────────────────────

// challengeNegWords 否定标记。
// ⚠️ 表里**不许**放单字「不」——「不错」「不错啊」全是肯定。
var challengeNegWords = []string{
	"不是", "不对", "错了", "哪有", "才不", "谁说的", "没有吧", "瞎说",
	"不成立", "不同意", "你搞错", "说反了", "并非",
}

// ChallengedOf 判断这一句是不是在顶它上一句。返回被顶的那句话。
//
// ⚠️ lastAIText 为空 → 恒 false: 它**什么都没说**的时候, 谈不上被顶。
func ChallengedOf(userText, lastAIText string) (string, bool) {
	u := strings.TrimSpace(userText)
	a := strings.TrimSpace(lastAIText)
	if u == "" || a == "" {
		return "", false
	}
	neg := false
	for _, wd := range challengeNegWords {
		if strings.Contains(u, wd) {
			neg = true
			break
		}
	}
	if !neg {
		return "", false
	}
	// 必须与它上一句有**共享的表层线索** (与 §7.2 联想同一把尺子)。
	set := map[string]bool{}
	for _, c := range BigramCues(a, 0) {
		set[c.Token] = true
	}
	// ⚠️ 否定词本身不算线索, 否则「不是」两个字就自证了。
	for _, c := range BigramCues(u, 0) {
		if !set[c.Token] {
			continue
		}
		bad := false
		for _, wd := range challengeNegWords {
			if strings.Contains(wd, c.Token) {
				bad = true
				break
			}
		}
		if !bad {
			return u, true
		}
	}
	return "", false
}

// ── 被顶素材的落库 ────────────────────────────────────────
//
// ⚠️ **裁定: 被顶不在热路径上生成 Spark, 而是记成素材, 由夜间批消费。**
//   设计稿的三路时机都通往「产生一个灵光」, 但第 ② 路如果当场生成,
//   就是**每一次用户反驳都多一次 LLM 调用** —— 而「重新组织立场」这件事
//   当轮的回复已经做完了。Spark 是事后的沉淀物, 不是当场的产物。

// EnsureChallengeSchema 建表。
func EnsureChallengeSchema(db *sql.DB) error {
	_, err := db.Exec("CREATE TABLE IF NOT EXISTS convo_challenge (" +
		"id INTEGER PRIMARY KEY AUTOINCREMENT, owner TEXT NOT NULL, day TEXT NOT NULL, " +
		"text TEXT NOT NULL, ts TEXT NOT NULL DEFAULT '')")
	return err
}

// SaveChallenge 记一次被顶。
func (s *Store) SaveChallenge(owner, day, text string, at time.Time) error {
	t := strings.TrimSpace(text)
	if t == "" {
		return nil
	}
	_, err := s.DB.Exec("INSERT INTO convo_challenge (owner, day, text, ts) VALUES (?,?,?,?)",
		owner, day, t, at.Format("2006-01-02 15:04:05"))
	return err
}

// LoadChallenges 取某一天的被顶记录。
func (s *Store) LoadChallenges(owner, day string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 5
	}
	rows, err := s.DB.Query("SELECT text FROM convo_challenge WHERE owner = ? AND day = ? ORDER BY id DESC LIMIT ?",
		owner, day, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
