package convo

import (
	"fmt"
	"regexp"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
)

// NowLine 给**所有**提示词用的时间锚。
//
// ⚠️ 为什么必须有这个函数, 而不是各写各的:
//
// 实测 (2026-10-14, 生产): 它写出「2026年9月6日 —— 这个日期离现在还有好几年」,
// 而那天就是**上个月**。根因不是模型笨, 是**它的提示词里从来没有日期** ——
// 于是它退回训练先验, 认为「现在」是它被训练的那一年。
//
// 对话路径 (dialog.go 段 9) 一直有时间锚, 所以私聊看不出问题;
// 但后台任务 (灵光 / 学习 / 做梦 / 夜间社交 / 世界观生成) 全都漏了 ——
// 而**后台产物会流进对话** (记忆 · 灵光 · 梦), 所以它们的错误时间感会
// 原封不动地说给群里的人听。
//
// ⚠️ 格式与 dialog.go 段 9 **必须一致**: 两处不同的话, 模型会当成两个时间。
func NowLine() string {
	now := clock.Now()
	_, offset := now.Zone()
	return fmt.Sprintf("\n\n【当前时间】%s (%s, UTC%+03d:%02d)",
		now.Format("2006-01-02 15:04:05"),
		now.Format("Monday"),
		offset/3600, (offset%3600)/60)
}

// PreciseNowLine 精确到分钟的当前时间。
//
// ⚠️ §4.6.3: **它只在"看了一下时间"之后才配拿到这一行。**
// 对话路径的默认注入是 VagueNowLine(模糊时段), 精确时刻要它主动索取 ——
// 真人不随身带着一个绝对时钟, 他"掏手机看一眼"。
func PreciseNowLine() string {
	now := clock.Now()
	_, offset := now.Zone()
	return fmt.Sprintf("\n\n【当前时间】%s (%s, UTC%+03d:%02d)",
		now.Format("2006-01-02 15:04:05"),
		now.Format("Monday"),
		offset/3600, (offset%3600)/60)
}

// VagueNowLine 对话路径的**模糊时段**。§4.6.3
//
// ⚠️ 它给的是「晚上十点多」, 不是「22:13」。
// 真人对自己处在一天里哪个位置有感觉, 但**没有分钟级的读数** ——
// 正是这份模糊让「我以为才过了十分钟」「啊都这个点了」这类话**可能发生**。
//
// ⚠️ 日期与星期**保留**: 那是"今天是哪天", 与"现在几点几分"不是一回事,
// 而且日程表按星期分档(它得知道今天是周三)。
// ⚠️ 系统侧(摘要 / 日志 / 记忆 / 后台任务)**仍然用精确时间戳** ——
// NowLine() 一个字都没改, 后台那几条路照旧。**糊的只是"面向它"的那一份。**
func VagueNowLine() string {
	now := clock.Now()
	cn := [...]string{"日", "一", "二", "三", "四", "五", "六"}[int(now.Weekday())]
	return fmt.Sprintf("\n\n【大概什么时候】%d月%d日 周%s, %s。",
		int(now.Month()), now.Day(), cn, VagueSlot(now))
}

// preciseClockRE 分钟级时刻。
//
// ⚠️ 判据**刻意只抓分钟级**(HH:MM / N点M分):
// 真人说不出"现在 22:13", 但说得出"都十一点了" —— 小时级的估算是**允许的**。
// 抓宽了会把"都这个点了""挺晚了"一起误伤, 而那正是 §4.6.3 想让它们**发生**的话。
var preciseClockRE = regexp.MustCompile(`\d{1,2}[:：]\d{2}|[0-9零一二三四五六七八九十两]{1,3}\s*点\s*[0-9零一二三四五六七八九十两]{1,3}\s*分`)

// PreciseClockHit 报出分钟级时刻 → 返回命中的那一段; 没有则空串。
func PreciseClockHit(text string) string { return preciseClockRE.FindString(text) }

// VagueSlot 一天里的时段, 用**人话**说。
//
// ⚠️ 分档刻意**粗**: 粗到报不出分钟, 细到够它判断"该不该这时候冒出来"。
// ⚠️ 19 点之后带上"几点多" —— 真人晚上对时间的感知本来就是小时级的。
func VagueSlot(now time.Time) string {
	h := now.Hour()
	switch {
	case h < 5:
		return "后半夜"
	case h < 8:
		return "一大早"
	case h < 11:
		return "上午"
	case h < 13:
		return "中午"
	case h < 17:
		return "下午"
	case h < 19:
		return "傍晚"
	case h < 22:
		return "晚上" + cnHour(h-12) + "点多"
	default:
		return "夜里" + cnHour(h-12) + "点多"
	}
}

// cnHour 12-23 → 十二/十一/十/九/八/七/六/五/四/三/二/一。
func cnHour(h int) string {
	nums := [...]string{"零", "一", "二", "三", "四", "五", "六", "七", "八", "九", "十", "十一"}
	if h >= 12 {
		return "十二"
	}
	if h < 1 {
		return "十二"
	}
	return nums[h]
}
