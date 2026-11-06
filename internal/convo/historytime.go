package convo

import (
	"fmt"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────
// B (2026-11-06 用户批准 "A-E"): 历史里的**时间锚**。
//
// 事故 (11-05 22:30 私聊): 延迟回复醒来后, 把 10-31 10:29 的旧对话
// 当成"刚才在聊的"接着演了一遍 —— 因为注入的【最近对话】只有当天序号:
//
//	1 她: 早 起了没
//	2 你: 起了
//	3 她: 嗯 起来了
//	1 你: （表情 — 示好）
//
// 序号 1,2,3 之后又出现 1 —— 那是**另一天**。模型没有任何线索能看出来,
// 于是它接着四天前的话头说话。
//
// 修法: 不改行首那套格式 (有 21 处用例钉着), 只在**跨天/断档**处插一行分隔。
// ─────────────────────────────────────────────────────────────

// historyTSLayout 落库的时间格式 (TEXT 时间戳, 本地时区)。
const historyTSLayout = "2006-01-02 15:04:05"

// HistoryGapAfterHours 同一天里断档超过这么久, 也要标一句 ——
// 07:20 和 19:40 在同一个"今天"里, 但不是同一段对话。
const HistoryGapAfterHours = 3

// HistoryTimeGap 两条相邻消息之间要不要插一行时间分隔。空串 = 不插。
//
// prevTS / curTS 是落库格式的时间戳 (旧的在先), today 是 YYYY-MM-DD。
// 解析不出来 → 不插: 一行坏数据不该把整段历史搅乱。
func HistoryTimeGap(prevTS, curTS, today string) string {
	p, perr := time.ParseInLocation(historyTSLayout, strings.TrimSpace(prevTS), time.Local)
	c, cerr := time.ParseInLocation(historyTSLayout, strings.TrimSpace(curTS), time.Local)
	if perr != nil || cerr != nil || !c.After(p) {
		return ""
	}
	pd := p.Format("2006-01-02")
	cd := c.Format("2006-01-02")
	if pd != cd {
		// ⚠️ 分隔描述的是**上面那段** (prev), 不是下面那段: 危险的是旧话,
		// 而「——」出现在新消息之前 —— 写成「下面是今天」会让模型以为旧话是今天说的。
		return fmt.Sprintf("—— 上面那些是 %s（%s）说的 ——", p.Format("01-02"), dayDistance(pd, strings.TrimSpace(today)))
	}
	if h := int(c.Sub(p).Hours()); h >= HistoryGapAfterHours {
		return fmt.Sprintf("—— 隔了 %d 小时 ——", h)
	}
	return ""
}

// dayDistance 那一天相对今天是几天前 —— 说明它有多旧。
func dayDistance(day, today string) string {
	d, derr := time.Parse("2006-01-02", day)
	t, terr := time.Parse("2006-01-02", today)
	if derr != nil || terr != nil {
		return "更早"
	}
	// 按日历日算差, 不按小时 —— 23:50 到次日 00:10 是"昨天", 不是"0 天前"。
	gap := int(t.Sub(d).Hours() / 24)
	if t.Sub(d) > time.Duration(gap)*24*time.Hour {
		gap++ // 不足整天的余数算作多一天 (00:10 看 23:50 的消息, 它就是昨天的)
	}
	switch {
	case gap <= 0:
		return "今天"
	case gap == 1:
		return "昨天"
	case gap == 2:
		return "前天"
	default:
		return fmt.Sprintf("%d 天前", gap)
	}
}

// historyGapLegend 表头里那句说明 —— 让模型知道「——」开头的行不是谁说的话。
const historyGapLegend = "（以「——」开头的行是时间分隔: 它说明**上面**那段话是多久以前说的。跨天或隔了几小时的, 别当成刚才说的。）"
