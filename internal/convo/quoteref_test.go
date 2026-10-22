package convo

import (
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────
// §6.10.2 ③ **引用回复** —— 把被引用的原话附在这一行后面 ✓
//
// 病: OneBot 的 reply 段里只有 id 没有正文 ✗, 而我们连 id 都没存 ✗ ⇒
// 每一条引用回复到了模型那里都是**没有指代对象的一句话** ✓ ——
// 「为什么」「那就睡」各说各的 ⇒ 群里说的"分不清主谓宾" ✓。
//
// 2026-10-16 生产库里的证据: 同一个词两条 —— `' 遥遥领先'` 与 `'遥遥领先'` ✓,
// 只差引用框后面那个空格 ✓。
// ─────────────────────────────────────────────────────────────

// TestReplyRef_AttachesTheQuotedOriginal **本条最重要**
func TestReplyRef_AttachesTheQuotedOriginal(t *testing.T) {
	quoted := map[string]MessageRecord{
		"9001": {MsgID: "9001", DaySeq: 11, Dir: "user", SenderUIN: "351390754",
			Payload: "早点睡", TS: "2026-10-16 22:41:03"},
	}
	m := MessageRecord{Dir: "user", SenderUIN: "2735604537", Payload: "那就睡", ReplyTo: "9001"}
	got := ReplyRefSuffix("群1", m, quoted, map[string]string{"351390754": "傲娇老缇"}, "3487037837")
	for _, want := range []string{"(回群1-11)", "早点睡", "傲娇老缇", "22:41"} {
		if !strings.Contains(got, want) {
			t.Fatalf("引用后缀 %q 里缺 %q —— 那「那就睡」还是没有指代对象", got, want)
		}
	}
}

// TestReplyRef_MissingTargetAddsNothing (反面, 而它是**不许编**那条)
//
// ⚠️ 引用的那条查不到 ⇒ **什么都不加** ✓, 而不是编一句"往上找" ✗。
func TestReplyRef_MissingTargetAddsNothing(t *testing.T) {
	plain := MessageRecord{Dir: "user", SenderUIN: "1", Payload: "在吗"}
	if got := ReplyRefSuffix("群1", plain, nil, nil, ""); got != "" {
		t.Fatalf("不是引用回复却加了后缀: %q", got)
	}
	dangling := MessageRecord{Dir: "user", SenderUIN: "1", Payload: "为什么", ReplyTo: "404"}
	if got := ReplyRefSuffix("群1", dangling, map[string]MessageRecord{}, nil, ""); got != "" {
		t.Fatalf("**引用的那条找不到却编了一句**: %q", got)
	}
}

// 引用它自己的话: 它自己的消息**没存 msg_id** ⇒ 数据库必然查不到,
// 只能靠桥给的 reply_uin + reply_text 兜底, 且必须认成「她」。
func TestReplyRef_QuoteOfSelfUsesBridgeFallback(t *testing.T) {
	m := MessageRecord{Dir: "user", SenderUIN: "111", Payload: "你倒是提醒我了",
		ReplyTo: "7699434272511958271", ReplyText: "老缇你那局挂完了没", ReplyUIN: "3487037837"}
	got := ReplyRefSuffix("群1", m, nil, nil, "3487037837")
	for _, want := range []string{"她", "老缇你那局挂完了没"} {
		if !strings.Contains(got, want) {
			t.Fatalf("引用它自己时 %q 里缺 %q", got, want)
		}
	}
}

// 引用别人、而那条不在库里: 用桥给的 qq 认人 (名字查得到就带名字)。
func TestReplyRef_QuoteOfOtherUsesBridgeUin(t *testing.T) {
	m := MessageRecord{Dir: "user", SenderUIN: "111", Payload: "你刚才说什么",
		ReplyTo: "7699433847545063218", ReplyText: "这个不是被抓走了吗", ReplyUIN: "2973357647"}
	got := ReplyRefSuffix("群1", m, nil, map[string]string{"2973357647": "傲娇老缇"}, "3487037837")
	for _, want := range []string{"傲娇老缇", "这个不是被抓走了吗"} {
		if !strings.Contains(got, want) {
			t.Fatalf("引用别人时 %q 里缺 %q", got, want)
		}
	}
}
