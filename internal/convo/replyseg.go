package convo

import "strings"

// 引用段 (OneBot v11 的 reply) 的解析。
//
// ⚠️ 历史: 2026-10-22 之前桥只给了假 id="0", 于是 convo 的 reply_to 恒空、
// 引用整条链路是死的; 桥修好后 id/seq/text 都是真的, 且 **text 里带着被引用原话**。
// ReplyQuote 引用段的**完整**信息: 被引用那条的 id、原话、发送者。
//
// ⚠️ 2026-10-22 桥修好之后, 段里 id/seq/text 都是真的:
//
//	{"type":"reply","data":{"id":"7699434272511958271","seq":"561708","text":"老缇你那局挂完了没"}}
//
// 修好之前 id 恒为 "0" —— 所以 "0" 一律当**没有** (宁可不出 id, 也别给假 id)。
// text 是**被引用的原话**, 有它就够渲染引用, 不必回查数据库。
// sender 是 uid 形态 (u_xxx), 不是数字 uin; 桥若补 qq 字段这里优先取。
func (e OneBotEvent) ReplyQuote() (id, text, sender string) {
	segs, err := e.Segments()
	if err != nil {
		return "", "", ""
	}
	for _, s := range segs {
		if s.Type != "reply" {
			continue
		}
		id = strings.TrimSpace(s.Data["id"])
		if id == "0" {
			id = ""
		}
		text = strings.TrimSpace(s.Data["text"])
		sender = strings.TrimSpace(s.Data["qq"])
		if sender == "" {
			sender = strings.TrimSpace(s.Data["uin"])
		}
		return id, text, sender
	}
	return "", "", ""
}
