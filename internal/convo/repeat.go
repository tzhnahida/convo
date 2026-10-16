package convo

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
)

// ─────────────────────────────────────────────────────────────
// 重复闸: 它自己反复捡起来的那几样东西
//
// §3.6.8 原文: **「一致 ≠ 反复主动提」**。
//
// 实测 (2026-10-14 首日): 「枣椰蜜糖」占它当天 73 条发言里的 13 条 (17.8%),
// 08:05-08:27 一小段 13 条里出现 4 次。真人提一次就过去了 ——
// **它每次冷场就捡起来再抛一次。**
//
// 同一个机制还治另一种重复: 目标语点缀 (「えへへ」当天 4 次)。
// 两者形状一样: **从它自己的输出里统计, 超了就告诉它别再说**。
//
// ⚠️ 检测源**只能是它自己说的话**。把用户说的也算进去的话,
// 对方提一次某样东西, 它就再也不敢接了 —— 那正好反了。
// ─────────────────────────────────────────────────────────────

const (
	// RepeatWindowHours 统计窗口。
	RepeatWindowHours = 12
	// RepeatMinCount 一个片段出现几次才算"反复"。
	// ⚠️ 2 次就报警太敏 —— 同一件事说两遍是正常的。
	RepeatMinCount = 3
	// RepeatMinMsgs 必须分布在**至少这么多条**消息里。
	// 防的是"一条长消息里同一个词出现 5 次"被误判成"反复提"。
	RepeatMinMsgs = 2
	// RepeatTopN 最多报几样。报太多它反而会紧张。
	RepeatTopN = 4
)

// RepeatHit 一样被反复提的东西。
type RepeatHit struct {
	Token string
	Count int
	Msgs  int
}

// repeatStopwords 高频功能词 —— 它们反复出现是正常的, 报了就是噪声。
var repeatStopwords = map[string]bool{
	"这个": true, "那个": true, "什么": true, "怎么": true, "可以": true, "知道": true,
	"时候": true, "现在": true, "今天": true, "可能": true, "真的": true, "觉得": true,
	"一下": true, "这样": true, "那样": true, "因为": true, "所以": true, "但是": true,
	"如果": true, "已经": true, "一直": true, "不是": true, "就是": true, "还是": true,
	"我们": true, "你们": true, "他们": true, "自己": true, "这边": true, "那边": true,
	"有点": true, "好像": true, "应该": true, "不能": true, "没有": true, "东西": true,
	"问题": true, "事情": true, "地方": true, "感觉": true, "突然": true,
}

// CountSelfRepeats 从它自己的发言里统计高频汉字片段。**纯函数**, 便于测。
func CountSelfRepeats(msgs []string) []RepeatHit {
	count := map[string]int{}
	perMsg := map[string]int{}
	for _, m := range msgs {
		seen := map[string]bool{}
		for _, tok := range repeatFragments(m) {
			count[tok]++
			if !seen[tok] {
				perMsg[tok]++
				seen[tok] = true
			}
		}
	}
	var cand []RepeatHit
	for tok, n := range count {
		if n < RepeatMinCount || perMsg[tok] < RepeatMinMsgs {
			continue
		}
		cand = append(cand, RepeatHit{Token: tok, Count: n, Msgs: perMsg[tok]})
	}
	// ⚠️ **合并切片**: 同一个词的 2/3/4 字切片会各报一次 ——
	// 实测「枣椰蜜糖」会变成「枣椰」「椰蜜」「蜜糖」「枣椰蜜」「椰蜜糖」五条,
	// 把 TopN 全占满, 而真正该报的「闭关」「笔记」一条都进不来。
	// 先按长度降序: 长的先入选, 短的只要是它的子串就丢。
	sort.Slice(cand, func(i, j int) bool {
		if len([]rune(cand[i].Token)) != len([]rune(cand[j].Token)) {
			return len([]rune(cand[i].Token)) > len([]rune(cand[j].Token))
		}
		return cand[i].Count > cand[j].Count
	})
	var out []RepeatHit
	for _, h := range cand {
		dup := false
		for _, kept := range out {
			if strings.Contains(kept.Token, h.Token) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Token < out[j].Token
	})
	if len(out) > RepeatTopN {
		out = out[:RepeatTopN]
	}
	return out
}

// repeatFuncChars 功能字 —— 片段**首尾**是它们的基本不是名词。
//
// ⚠️ 实测噪声 (2026-10-14): 「我这」×9 「我闭」×9 「糖都」×8 全都是跨词边界的切片。
// 它们会占掉 TopN 的额度, 把真正该报的「闭关」「笔记」挤出去。
const repeatFuncChars = "我你他她它这那的了吗呢吧啊是在有就都也还很难太不没和跟给把被让从到对向以为过要会能想着说做去来上下里外个些么什怎谁哪每各又再只才更最很真别"

// ⚠️ 只抽汉字: 混进标点/数字会把「真的?」和「真的。」算成两个词。
func repeatFragments(s string) []string {
	r := []rune(s)
	var out []string
	for i := 0; i < len(r); i++ {
		for n := 2; n <= 4; n++ {
			if i+n > len(r) {
				continue
			}
			ok := true
			for j := i; j < i+n; j++ {
				if r[j] < 0x4E00 || r[j] > 0x9FFF {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			tok := string(r[i : i+n])
			// 首尾是功能字 → 大概率是跨词切片, 不是词。
			if !repeatStopwords[tok] && !strings.ContainsRune(repeatFuncChars, r[i]) &&
				!strings.ContainsRune(repeatFuncChars, r[i+n-1]) {
				out = append(out, tok)
			}
		}
	}
	return out
}

// CountKana 目标语 (假名) 出现次数 —— 治「风格锚点飘移」。
//
// 实测: 「えへへ」当天 4 次, 和「枣椰蜜糖」(须弥甜点)、「闭关」(修仙梗)、
// 原神人设拼在一起 —— 真人不会三种文化符号随机切换。
func CountKana(msgs []string) int {
	n := 0
	for _, m := range msgs {
		for _, c := range m {
			if (c >= 0x3040 && c <= 0x309F) || (c >= 0x30A0 && c <= 0x30FF) {
				n++
			}
		}
	}
	return n
}

// RepeatNote 渲染注入。没东西可报时返回空串 (**不占位**)。
func RepeatNote(hits []RepeatHit, kana int, kanaMax int) string {
	if len(hits) == 0 && kana <= kanaMax {
		return ""
	}
	var b strings.Builder
	if len(hits) > 0 {
		b.WriteString("\n\n【这几样你今天已经反复提过了 —— 从现在起**不许再主动提**】\n")
		for _, h := range hits {
			b.WriteString(fmt.Sprintf("- 「%s」已经说了 %d 次\n", h.Token, h.Count))
		}
		b.WriteString("真人提一次就过去了。**冷场也别捡起来再抛一次** —— 那是最明显的复读。\n")
	}
	if kana > kanaMax {
		b.WriteString(fmt.Sprintf("\n【目标语点缀】今天已经用了 %d 次 —— 够了。接下来**只说中文**, 一个假名都别加。\n", kana))
	}
	return b.String()
}

// kanaMaxPerWindow 目标语点缀的窗口上限。
//
// ⚠️ 设计稿说「每句话最多 1 个」—— 那条只管**密度**, 不管总量,
// 于是它可以在每句话里各塞一个, 一天几十次。这里管的是**总量**。
const kanaMaxPerWindow = 3

// repeatNote 取窗口内它自己的发言并统计。
func (e *Engine) repeatNote(sceneKey string) string {
	if e.Store == nil {
		return ""
	}
	since := clock.Now().Add(-time.Duration(RepeatWindowHours) * time.Hour).Format(bodyTSLayout)
	rows, err := e.Store.DB.Query("SELECT payload_json FROM convo_messages "+
		"WHERE dir = 'ai' AND kind = 'text' AND ts >= ? ORDER BY id DESC LIMIT 200", since)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var msgs []string
	for rows.Next() {
		var raw string
		if rows.Scan(&raw) == nil && strings.TrimSpace(raw) != "" {
			msgs = append(msgs, raw)
		}
	}
	return RepeatNote(CountSelfRepeats(msgs), CountKana(msgs), kanaMaxPerWindow)
}
