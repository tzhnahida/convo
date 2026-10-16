package convo

import "strings"

// ReplyToMsgID 这条消息**引用**的是哪一条 (OneBot 的 reply 段) ✓。
//
// OneBot v11 的引用段形如 {"type":"reply","data":{"id":"12345"}} ✓ ——
// 而它里面**通常只有 id, 没有正文** ✗ ⇒ 想show出被引用的原话,
// 只能拿这个 id **回查我们自己存过的那条** ✓ (见 convo_messages.msg_id)。
//
// ⚠️ 空串 = 不是引用回复 ✓, 与"引用了一条没有 id 的"是两件事 ✓,
// 但这里只回答"引用的是哪一条" ✓ —— id 拿不到就不冒充 ✓。
func (e OneBotEvent) ReplyToMsgID() string {
	segs, err := e.Segments()
	if err != nil {
		return ""
	}
	for _, s := range segs {
		if s.Type != "reply" {
			continue
		}
		return strings.TrimSpace(s.Data["id"])
	}
	return ""
}
