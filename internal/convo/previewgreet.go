package convo

import (
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────
// 预告(07:25 那种) 只能是**问候或关照** (2026-10-30 用户: 「问候类呢?！」)
//
// 实测产出全是评论/提问: 「这么早就坐着看书了」「周末赶什么衣服」 —— 没人这么跟人打招呼。
// prompt 已经说了「问一句 ta / 关照一句」, 模型仍旧写评论, 所以加**出口硬闸**:
//   ① 必须以问候语开头 (prefix 白名单), 否则重生成一次, 仍不中 → 用兜底问候;
//   ② 预告不许带 ask_share (那是审问, 不是打招呼)。

// greetingPrefixes 允许的开头 (问候/关照)。
var greetingPrefixes = []string{
	"早", "早安", "早上好", "中午好", "午好", "下午好", "晚上好", "晚安",
	"在吗", "在不在", "哈喽", "嗨", "hi", "hello",
	"醒了", "起了", "起床", "睡醒", "昨晚", "睡得好", "还没睡", "没睡",
	"今天", "最近", "忙吗", "在忙", "累吗", "吃了吗", "吃了没", "还好吗", "怎么样",
}

// looksLikeGreeting 这句是不是在问候/关照对方 (以问候语开头)。
func looksLikeGreeting(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	low := strings.ToLower(t)
	for _, p := range greetingPrefixes {
		if strings.HasPrefix(low, p) {
			return true
		}
	}
	return false
}

// fallbackGreeting 模型连续两次都写不出问候时的兜底 (按小时选一句)。
//
// ⚠️ 宁可发一句朴素的「早 起了没」, 也不要让它把「我在干什么」当问候。
func fallbackGreeting(now time.Time) string {
	switch h := now.Hour(); {
	case h >= 5 && h < 11:
		return "早 起了没"
	case h >= 11 && h < 14:
		return "在吗 中午吃了没"
	case h >= 14 && h < 18:
		return "下午好 在忙吗"
	case h >= 18 && h < 23:
		return "晚上好 今天怎么样"
	default:
		return "还没睡呀 在忙吗"
	}
}
