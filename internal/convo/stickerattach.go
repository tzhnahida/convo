package convo

import "strings"

// ─────────────────────────────────────────────────────────────
// 文字回复附带一张表情包 (2026-10-20 owner 定: 40%)
//
// 实测: 群友消息里约 **14%** 是表情包, 而它 118 条发言只有 2 张 (**1.7%**)。
// 原因不是池子空 (还有 13 张), 而是"发不发"**完全由模型的帧决定** ——
// 而模型默认偏文字。提示词里见过太多次"说得多、发得少"的漂移,
// 所以这一条走**机制**: 文字回复落定之后, 系统按概率附一张。
//
// ⚠️ 三条边界:
//   ① 模型自己已经发了 sticker → 不重复加 (那是它的选择, 别盖);
//   ② 纯表情帧 (没有文字) → 不加 (那是第二态, 不是"附赠");
//   ③ 池子取不到 → 静默不加 (表情包是锦上添花, 失败不许影响发言)。
// ─────────────────────────────────────────────────────────────

// DefaultStickerAttachPct 默认附带概率 (%)。owner 2026-10-20 定 40。
const DefaultStickerAttachPct = 40

// maybeAttachSticker 文字回复附一张表情包。命中概率 = cfg.Stickers.AttachPct。
func (e *Engine) maybeAttachSticker(frame OutputFrame) OutputFrame {
	if e.Store == nil || frame.Refused || len(frame.Reply) == 0 {
		return frame
	}
	pct := e.Cfg.Stickers.AttachPct
	if pct <= 0 {
		pct = DefaultStickerAttachPct
	}
	hasText := false
	for _, it := range frame.Reply {
		if it.Kind == "sticker" {
			return frame // 它自己已经选了表情 → 尊重它
		}
		if it.Kind == "text" && strings.TrimSpace(it.Text) != "" {
			hasText = true
		}
	}
	if !hasText {
		return frame
	}
	roll := Dice
	if e.StickerRollFn != nil {
		roll = e.StickerRollFn
	}
	if roll(100) >= pct {
		return frame
	}
	st, err := e.Store.WeightedPick(e.Cfg.Stickers.CapPerSticker)
	if err != nil || st == nil || strings.TrimSpace(st.LocalPath) == "" {
		return frame
	}
	frame.Reply = append(frame.Reply, FrameItem{Kind: "sticker", StickerID: st.ID})
	return frame
}
