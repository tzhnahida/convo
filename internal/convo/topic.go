package convo

import (
	"database/sql"
	"encoding/json"
	"github.com/tzhnahida/VocabLock/internal/clock"
)

// ——— 模块 4: 话题引擎 (P0 降级版, §8.4 步骤 1-3 锚定词检索砍) ———
//
// 只留: 内置话题包 + 10-20% 概率强制纯聊。话题预生成 Job (睡点) 从内置包
// 抽 2 个备用话题写 convo_topics; Next() 取一个未用话题; 骰子 < PureChatPct
// → 纯聊 (无话题, 内容由对话引擎自由发挥 — 拟人核心的"无目的噪声")。

type Topic struct {
	Kind string `json:"kind"` // news | life | fun | memory | ask | pure
	Text string `json:"text"` // 话题种子 (注入"本轮任务"块)
}

var BuiltinTopics = []Topic{
	{Kind: "life", Text: "今天过得怎么样?说说今天一件小事"},
	{Kind: "fun", Text: "最近有没有刷到什么好玩的,让我也乐一下"},
	{Kind: "news", Text: "最近在忙什么新东西?还是老样子"},
	{Kind: "life", Text: "今天吃了什么好吃的?"},
	{Kind: "life", Text: "周末有什么计划吗?"},
	{Kind: "fun", Text: "如果我今天心情好,想听个冷笑话,讲一个呗"},
	{Kind: "memory", Text: "还记得我们上次聊的那件事吗?后来怎么样了"},
	{Kind: "news", Text: "最近有什么想吐槽的事?"},
	{Kind: "life", Text: "你那边天气怎么样?我这边…(按你身份说)"},
	{Kind: "fun", Text: "给你发个梗,看你能不能接住"},
	{Kind: "life", Text: "今天学到什么新东西没?"},
	{Kind: "memory", Text: "说起来,你之前说过的那件事我一直记着呢"},
}

// PureChatPct 强制纯聊概率 (10-20%) — 拟人六要素之"自主噪声"。
const PureChatPct = 15

// SeedTopics 内置话题包 → convo_topics 备选池 (启动/睡点调用)。
func (s *Store) SeedTopics(limit int) error {
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM convo_topics WHERE used=0`).Scan(&n); err != nil {
		return err
	}
	if n >= 3 {
		return nil
	}
	// 清空旧记录再灌 (内置包内容变化时保持新鲜)
	if _, err := s.DB.Exec(`DELETE FROM convo_topics`); err != nil {
		return err
	}
	for _, t := range BuiltinTopics {
		if _, err := s.DB.Exec(`INSERT INTO convo_topics(kind, payload_json, used, created_ts)
			VALUES(?, ?, 0, ?)`, t.Kind, jsonTopic(t), clock.TodayTS()); err != nil {
			return err
		}
	}
	return nil
}

func jsonTopic(t Topic) string {
	b, _ := json.Marshal(t)
	return string(b)
}

// NextTopic 取一个未用话题; 骰子 < PureChatPct → 纯聊。无剩余 → 重置复用。
func (s *Store) NextTopic(roll int) (Topic, error) {
	if roll < PureChatPct {
		return Topic{Kind: "pure", Text: ""}, nil
	}
	row := s.DB.QueryRow(`SELECT id, kind, payload_json FROM convo_topics WHERE used=0 ORDER BY RANDOM() LIMIT 1`)
	var id int64
	var kind, payload string
	if err := row.Scan(&id, &kind, &payload); err == sql.ErrNoRows {
		// 池空 → 重置
		if err := s.SeedTopics(3); err != nil {
			return Topic{Kind: "pure"}, err
		}
		return s.NextTopic(roll)
	} else if err != nil {
		return Topic{}, err
	}
	var t Topic
	_ = json.Unmarshal([]byte(payload), &t)
	if t.Kind == "" {
		t.Kind = kind
	}
	if _, err := s.DB.Exec(`UPDATE convo_topics SET used=1 WHERE id=?`, id); err != nil {
		return t, err
	}
	return t, nil
}

// RollTopicDice 掷话题骰子 (0-99) — 全局 rand, 无并发热点。
func RollTopicDice() int {
	return Dice(100)
}
