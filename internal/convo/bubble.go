package convo

import (
	"regexp"
	"strings"
)

// ─────────────────────────────────────────────────────────────
// §8.4 出口整形: 一条 text 项 = **一次发送**。
//
// 2026-11-03 用户实测 (2204 条真人消息 vs 它自己的):
//   · 含换行 24 条 (1.1%)、含空行 3 条 —— 而它把两句话用空行糊在一条里:
//     「烤肉拌饭 わかる〜 + 空行 + 19:11才到 你先忍一会儿」;
//   · 以省略号开头/结尾 4.6%, 人类 0.2% —— 「唔……」「真的……这表情一模一样」;
//   · 一轮两个问号 = 采访。
//
// 这三条都在**出口**做, 不在提示词里: 能机器判定的就别指望模型自觉。
// ─────────────────────────────────────────────────────────────

// BubbleMaxQuestions 一轮最多几个问句。
const BubbleMaxQuestions = 1

// bubbleEdgeCut 首尾要削掉的字符 (省略号腔)。
//
// ⚠️ **不削句号**: style.go 的不变式是「健康 + 心情好时一个字符都不改」
// (stylewire_test.go 钉着), 而句号在人类里占 5.9%, 本来就不是 AI 腔。
const bubbleEdgeCut = "…·・. 　\t"

// stickerPathRe 旧 sticker 载荷: `486|data/convo/stickers/ja/x.gif`。
var stickerPathRe = regexp.MustCompile(`^\d+\|`)

// NormalizeBubble 把模型的 text 项整理成一次发送的样子:
//
//	① 换行/空行 → 空格 (一条消息就是一口气);
//	② 首尾的省略号/句号 → 削掉;
//	③ 最多 maxQ 个问句 (多出来的整句丢);
//	④ 收尾的空格。
//
// 空串 = 这条不该发 (调用方静默丢弃)。
func NormalizeBubble(text string, maxQ int) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	// ① 换行与连续空格压成一个空格。
	t := strings.Join(strings.Fields(text), " ")
	// ② 首尾省略号。
	t = strings.Trim(t, bubbleEdgeCut)
	// ③ 问句上限。
	t = capQuestions(t, maxQ)
	return strings.TrimSpace(t)
}

// capQuestions 只保留前 maxQ 个问句 (按句末标点切, 后面的问句整句丢)。
//
// ⚠️ **整句丢**而不是把问号改成句号: 改标点会把「你去不去？」变成一句陈述,
// 语义变了 (style.go 的不变式就是"只减不改")。
func capQuestions(s string, maxQ int) string {
	if maxQ < 0 {
		return s
	}
	var b strings.Builder
	q := 0
	for _, seg := range splitSentences(s) {
		if isQuestionSegment(seg) {
			if q >= maxQ {
				continue
			}
			q++
		}
		b.WriteString(seg)
	}
	return b.String()
}

// splitSentences 按句末标点切句 (标点跟着前一句走)。
func splitSentences(s string) []string {
	var out []string
	var cur strings.Builder
	for _, r := range s {
		cur.WriteRune(r)
		switch r {
		case '？', '?', '！', '!', '。', '…':
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// isQuestionSegment 这一段是不是问句。
func isQuestionSegment(seg string) bool {
	return strings.ContainsAny(seg, "？?")
}

// StickerHistoryLine kind=sticker 的历史行归一 (F, 2026-11-03)。
//
// ⚠️ 旧格式是 `486|data/convo/stickers/ja/1792505954267645894_232b9b63.gif` ——
// 那是**落库的载荷**, 不是人说的话。而 `recentDialogIn` 直接 `line := m.Payload`,
// 于是磁盘路径成了"它以前说过的话", 迟早有一天会被原样复述出来。
// 按 kind 归一, 不按内容猜: 新记录已经写「[表情]」, 这里只兜旧的。
func StickerHistoryLine(kind, payload string) string {
	if kind != "sticker" {
		return payload
	}
	t := strings.TrimSpace(payload)
	if t == "" || strings.HasPrefix(t, "[") {
		return payload
	}
	if stickerPathRe.MatchString(t) || strings.Contains(t, "stickers/") {
		return "[表情]"
	}
	return payload
}
