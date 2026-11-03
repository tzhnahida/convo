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

// openerBuckets 各时段的问候候选 —— **一天一句, 相邻两天必定不同**。
//
// ⚠️ 2026-11-03 用户实测: 10-26/10-27/10-29/11-03 的私聊开场全在 07:25,
// 句式也只有「早啊 起了没」一种 —— 整点 + 复读, 那是个闹钟, 不是人。
// 时间抖动在调度侧 (`previewLeadMinutes`), 句式轮换在这里: 两件事分开, 各自可测。
var openerBuckets = []struct {
	from, to int
	opts     []string
}{
	{5, 11, []string{"早 起了没", "早啊 昨晚睡得怎么样", "早上好 今天忙吗", "早 今天有安排吗"}},
	{11, 14, []string{"中午好 吃了没", "在吗 中午吃了没", "午好 这会儿忙不忙"}},
	{14, 18, []string{"下午好 在忙吗", "嗨 下午在干嘛", "下午好 今天还顺利吗"}},
	{18, 23, []string{"晚上好 今天怎么样", "晚上好 吃了吗", "嗨 今天累不累"}},
	{-1, 5, []string{"还没睡呀 在忙吗", "还没睡 在忙什么", "在吗 还没睡吧"}},
}

// openersFor 这个小时能用的问候句。
func openersFor(hour int) []string {
	for _, bk := range openerBuckets {
		if bk.from == -1 {
			if hour < 5 {
				return bk.opts
			}
			continue
		}
		if hour >= bk.from && hour < bk.to {
			return bk.opts
		}
	}
	return openerBuckets[0].opts
}

// dayOpener 今天这一句问候 (按天轮换, 同一天内恒定)。
//
// ⚠️ 用「年×366 + 年内第几天」而不是随机数: 同一天内**重生成也必须是同一句**
// (预告写不出问候会再生成一次), 而相邻两天必然换 —— 随机数两条都不满足。
func dayOpener(now time.Time) string {
	opts := openersFor(now.Hour())
	if len(opts) == 0 {
		return "在吗"
	}
	idx := (now.Year()*366 + now.YearDay()) % len(opts)
	if idx < 0 {
		idx += len(opts)
	}
	return opts[idx]
}

// fallbackGreeting 模型连续两次都写不出问候时的兜底 —— **就是今天那句**。
//
// ⚠️ 宁可发一句朴素的「早 起了没」, 也不要让它把「我在干什么」当问候。
// ⚠️ 兜底也必须跟着天走, 否则它每天都是同一句 (那就成了提示词里讨厌的那种闹钟)。
func fallbackGreeting(now time.Time) string {
	return dayOpener(now)
}
