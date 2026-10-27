package convo

import "strings"

// ─────────────────────────────────────────────────────────────
// 出口硬闸: 两条「把系统说明书当话读」的违规 (2026-10-27 用户实测)
//
//  ① 群友只发了个表情包 → 它回「这表情什么意思」。
//     prompt 里已经写了两遍「别追问、表情包没具体意思」, 模型照犯 ——
//     说明软约束不够, 这一条必须**在出口拦** (重试一次, 仍中就闭嘴)。
//  ② 群友发的图没拿到, 系统注入「[图没读到: …]」→ 它回「？图没读到」。
//     那是给它看的提示, 不是一句可以复述的话。

// ⚠️ 这些词只在「对方只发表情」的那一轮才算违规 —— 对方问「这表情什么意思」时,
// 它照着解释是正常的。

var stickerAskMarkers = []string{"什么意思", "啥意思", "没看懂", "看不懂", "这表情", "什么表情"}

// imageMissingMark 系统注入的「图没读到」标记 (含在提示词里, 不许被复述)。
const imageMissingMark = "图没读到"

// ReplyGuardViolations 出口硬闸。空切片 = 通过。
func ReplyGuardViolations(userText, reply string) []string {
	var out []string
	if isStickerOnlyMessage(userText) {
		for _, m := range stickerAskMarkers {
			if strings.Contains(reply, m) {
				out = append(out, "对方只发了个表情, 却追问/解读它的含义 ("+m+") —— 表情包不一定是话")
				break
			}
		}
	}
	if strings.Contains(userText, imageMissingMark) && strings.Contains(reply, imageMissingMark) {
		out = append(out, "复述了系统标记「图没读到」—— 它是提示, 不是话")
	}
	return out
}
