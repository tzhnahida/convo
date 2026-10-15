package convo

import (
	"strings"
	"time"

	"github.com/tzhnahida/VocabLock/internal/log"
)

// ─────────────────────────────────────────────────────────────
// §2.6.5 **被接住之后** —— 设计稿说这才是"迸发"的真正机制 (2026-10-15 接线)
//
//   > 被接住(回应 / 追问 / 反驳) → heat 上升 → 这个方向被强化 → **它会在这个方向上想更多**
//   > 没人接                      → heat 衰减 → 放下(不是删除, 是沉降进长期记忆)
//
// 前半段(说出来)在 §2.37 接了; 这里接的是**后半段**: `TouchSpark` 原来零调用。
//
// ⚠️ 而"想更多"必须**有个落点**, 否则热力涨了也只是个数字:
// 它的落点是**下一次迸发的提示词** —— 见 ReinforcedTopics / sparkSystemPrompt。
// ─────────────────────────────────────────────────────────────

const (
	// SparkPickupDelta 被接住一次加多少热。
	//
	// ⚠️ 与新生基线(ParseSpark 给的 0.5)同量级: 一次接住 ≈ 让这条再"新"一遍。
	// 给小了等于没接住, 给大了会让一条被随口提过一次的灵光压过所有新的。
	SparkPickupDelta = 0.4
	// SparkPickupWindow 说出去之后**多久之内**算"这一轮的在聊"。
	//
	// ⚠️ 24 小时: 隔了一天再提起来, 那多半是它自己又想起来, 不是"被接住"。
	SparkPickupWindow = 24 * time.Hour
	// SparkReinforcedAbove 热力**高过新生基线**才算被强化过。
	//
	// ⚠️ 判据是"比基线高", 而不是"高于某个绝对阈值" ——
	// 后者会把**刚从夜里生出来**的那些也算成"被接住过"。
	SparkReinforcedAbove = 0.5
)

// LastSaidSpark 最近说过的那条灵光 (在 within 之后说的)。
//
// ⚠️ 返回的是**说过的**, 不是最新生成的 —— "被接住"只能发生在说出去之后。
func (s *Store) LastSaidSpark(within time.Time) (Spark, bool) {
	if s == nil || s.DB == nil {
		return Spark{}, false
	}
	var id int64
	q := `SELECT id FROM convo_spark WHERE said_ts <> '' AND said_ts >= ? ORDER BY said_ts DESC, id DESC LIMIT 1`
	if err := s.DB.QueryRow(q, within.Format(bodyTSLayout)).Scan(&id); err != nil {
		return Spark{}, false
	}
	sp, ok := s.SparkByID(id)
	return sp, ok
}

// SparkByID 按 id 取一条。
func (s *Store) SparkByID(id int64) (Spark, bool) {
	if s == nil || s.DB == nil || id <= 0 {
		return Spark{}, false
	}
	all, err := s.AllSparks(500)
	if err != nil {
		return Spark{}, false
	}
	for _, sp := range all {
		if sp.ID == id {
			return sp, true
		}
	}
	return Spark{}, false
}

// noteSparkPickup §2.6.5 的**写入侧**: 这一句有没有接住刚才那条灵光。
//
// 判据只有一条: **用户这句话里出现了那条灵光的 talk_key**。
//
// ⚠️ 刻意**不做语义推断**: 猜错的代价是"在一个它其实没被接住的方向上越想越多",
// 而那比"没接住"糟得多 —— 后者只是放下(设计稿说得很清楚: 放下不是删除)。
//
// ⚠️ 第二条护栏: **一条灵光只记一次接住**。
// 用"最近一次触碰早于说出去的时刻"来判 —— 否则用户连说三句带这个关键词的话,
// 它会以为被接住了三次。
func (e *Engine) noteSparkPickup(text string, now time.Time) {
	if e.Store == nil {
		return
	}
	t := strings.TrimSpace(text)
	if len([]rune(t)) < 2 {
		return
	}
	sp, ok := e.Store.LastSaidSpark(now.Add(-SparkPickupWindow))
	if !ok {
		return
	}
	key := strings.TrimSpace(sp.TalkKey)
	if len([]rune(key)) < 2 || !strings.Contains(t, key) {
		return // 没提到那个方向 → 不算接住(也**不扣** —— 没人接就是自然衰减)
	}
	// ⚠️ 只记一次: 已经触碰过(而且晚于说出去的时刻)就不再加。
	if lt, ok2 := ParseMsgTS(sp.LastTouchTS); ok2 && sp.SaidTS != "" {
		if st, ok3 := ParseMsgTS(sp.SaidTS); ok3 && !lt.Before(st) {
			return
		}
	}
	if err := e.Store.TouchSpark(sp.ID, SparkPickupDelta, now); err != nil {
		log.Warn("convo:spark", "被接住记不上(那个方向不会被强化): %v", err)
		return
	}
	log.Info("convo:spark", "§2.6.5 灵光被接住 (方向 %q, +%.1f)", key, SparkPickupDelta)
}

// ReinforcedTopics 被接住过的**方向** (talk_key), 新的在前。
//
// ⚠️ 这就是"它会在这个方向上想更多"的**数据来源** ——
// 没有消费者的话, TouchSpark 加的那点热力只是一个没人看的数字。
func (s *Store) ReinforcedTopics(limit int) []string {
	if s == nil || s.DB == nil {
		return nil
	}
	if limit <= 0 {
		limit = 3
	}
	all, err := s.AllSparks(200)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, sp := range all {
		if sp.HeatDelta <= SparkReinforcedAbove {
			continue // 没被接住过(还是新生基线或更低)
		}
		k := strings.TrimSpace(sp.TalkKey)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// ReinforcedLine 把被强化的方向折成提示词里的一行。空 → 不加一个字。
func ReinforcedLine(topics []string) string {
	if len(topics) == 0 {
		return ""
	}
	return "\n最近你总往这些方向上想: " + strings.Join(topics, " / ") +
		" —— 顺着它再想一层, 但**别重复已经说过的那些**。"
}
