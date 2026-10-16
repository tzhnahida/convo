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
	got := ReplyRefSuffix(m, quoted, map[string]string{"351390754": "傲娇老缇"})
	for _, want := range []string{"(回11)", "早点睡", "傲娇老缇", "22:41"} {
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
	if got := ReplyRefSuffix(plain, nil, nil); got != "" {
		t.Fatalf("不是引用回复却加了后缀: %q", got)
	}
	dangling := MessageRecord{Dir: "user", SenderUIN: "1", Payload: "为什么", ReplyTo: "404"}
	if got := ReplyRefSuffix(dangling, map[string]MessageRecord{}, nil); got != "" {
		t.Fatalf("**引用的那条找不到却编了一句**: %q", got)
	}
}
