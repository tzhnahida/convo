package convo

import (
	"strings"
	"testing"
)

// 真机原文 (桥修好后): reply 段的 id/seq/text 全是真的。
func TestReplyQuote_ParsesRealBridgeSegment(t *testing.T) {
	raw := `[{"type":"reply","data":{"id":"7699434272511958271","seq":"561708","text":"老缇你那局挂完了没"}},
		{"type":"at","data":{"qq":"3487037837"}},
		{"type":"text","data":{"text":" 你倒是提醒我了"}}]`
	id, text, sender := OneBotEvent{Message: []byte(raw)}.ReplyQuote()
	if id != "7699434272511958271" {
		t.Fatalf("id 没解析出来: %q", id)
	}
	if text != "老缇你那局挂完了没" {
		t.Fatalf("被引用原话没解析出来: %q", text)
	}
	if sender != "" {
		t.Fatalf("没有 qq 字段时 sender 应当是空: %q", sender)
	}
}

// 修好之前桥的 id 恒为 "0" —— 必须当"没有", 不许拿它去回查。
func TestReplyQuote_ZeroIDIsNotAnID(t *testing.T) {
	raw := `[{"type":"reply","data":{"id":"0","seq":"561708","text":"老缇你那局挂完了没"}}]`
	id, text, _ := OneBotEvent{Message: []byte(raw)}.ReplyQuote()
	if id != "" {
		t.Fatalf("假 id 应当被当成没有: %q", id)
	}
	if text != "老缇你那局挂完了没" {
		t.Fatalf("原话仍应可用: %q", text)
	}
}

// 引用那条不在库里 (窗口外/老消息) 时, 用桥上给的原话兜底。
func TestReplyRefSuffix_FallsBackToBridgeText(t *testing.T) {
	m := MessageRecord{ReplyTo: "7699434272511958271", ReplyText: "老缇你那局挂完了没"}
	got := ReplyRefSuffix("群1", m, nil, nil)
	if !strings.Contains(got, "老缇你那局挂完了没") {
		t.Fatalf("数据库查不到时应当用桥给的原话: %q", got)
	}
}

// 引用的是它自己说的话 → 与 @ 同级, 直接接。
func TestIsAddressedToMe_QuoteSelf(t *testing.T) {
	ok, why := IsAddressedToMe(SceneMsg{
		SceneKey: "group:1", UIN: "111", Text: "你倒是提醒我了", QuoteToSelf: true,
	}, SelfIdentity{UIN: "3487037837"}, GroupConfig{Enabled: true})
	if !ok || why != ReasonQuoteSelf {
		t.Fatalf("引用它自己却没有判成接: ok=%v why=%q", ok, why)
	}
}

// reply_text 落库并读回。
func TestStore_ReplyTextRoundTrip(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.InsertMessageFull("user", "qq", "text", "你倒是提醒我了", MsgMeta{
		SceneKey: sceneWireKey, SenderUIN: "111", MsgID: "m2", ReplyTo: "m1", ReplyText: "老缇你那局挂完了没",
	}); err != nil {
		t.Fatal(err)
	}
	recs, err := s.RecentMessagesIn(sceneWireKey, 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.MsgID == "m2" {
			if r.ReplyTo != "m1" || r.ReplyText != "老缇你那局挂完了没" {
				t.Fatalf("引用信息没读回来: reply_to=%q reply_text=%q", r.ReplyTo, r.ReplyText)
			}
			return
		}
	}
	t.Fatal("没找到刚写入的那条")
}
