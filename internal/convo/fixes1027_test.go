package convo

import (
	"strings"
	"testing"
	"time"
)

// A: 对方只发了个表情, 它却追问/解读含义 → 出口硬闸。
func TestReplyGuard_StickerOnlyQuestion(t *testing.T) {
	sticker := "(表情 — 无语) [图: 表情 — 无语 厌]"
	if v := ReplyGuardViolations(sticker, "这表情什么意思"); len(v) == 0 {
		t.Fatal("追问表情含义应当被拦")
	}
	if v := ReplyGuardViolations(sticker, "哈 我也无语"); len(v) != 0 {
		t.Fatalf("正常接话被误判: %v", v)
	}
	// 表情 + 文字 = 有话说; 对方问「这表情什么意思」时解释是正常的。
	if v := ReplyGuardViolations("(表情 — 无语) 你行不行", "什么意思"); len(v) != 0 {
		t.Fatalf("表情带文字被误判: %v", v)
	}
}

// B: 复述系统标记「图没读到」→ 出口硬闸。
func TestReplyGuard_ImageMissingEcho(t *testing.T) {
	user := "[图没读到: 这张图我这边没拿到, 别猜内容]"
	if v := ReplyGuardViolations(user, "？图没读到"); len(v) == 0 {
		t.Fatal("复述「图没读到」应当被拦")
	}
	if v := ReplyGuardViolations(user, "那换个话题"); len(v) != 0 {
		t.Fatalf("正常反应被误判: %v", v)
	}
}

// D: 回顾词表碎片合并 —— 表/情/表情 只留 表情。
func TestFold_SubstringFragmentsCollapse(t *testing.T) {
	base := time.Date(2026, 10, 26, 22, 0, 0, 0, time.Local)
	msgs := []FoldMsg{
		{SenderUIN: 1, Text: "我发个表情", TS: base},
		{SenderUIN: 2, Text: "这个表情好怪", TS: base},
		{SenderUIN: 3, Text: "表情包大战", TS: base},
	}
	r := FoldGroupDay(msgs, []string{"表", "情", "表情"}, 3)
	if len(r.Topics) != 1 || r.Topics[0] != "表情" {
		t.Fatalf("碎片没合并干净: %v", r.Topics)
	}
}

// E: 连着两条纯表情之后, 第三张不再发。
func TestStickersOnlyRecently(t *testing.T) {
	s := newTestStore(t)
	e := &Engine{Store: s}
	if e.stickersOnlyRecently("group:1", 2) {
		t.Fatal("空场景不该判成连着发表情")
	}
	for i := 0; i < 2; i++ {
		if _, err := s.InsertMessageFull("ai", "qq", "sticker", "[表情]", MsgMeta{SceneKey: "group:1"}); err != nil {
			t.Fatal(err)
		}
	}
	if !e.stickersOnlyRecently("group:1", 2) {
		t.Fatal("两条纯表情之后应当判成「只会发表情」")
	}
	if _, err := s.InsertMessageFull("ai", "qq", "text", "说句话", MsgMeta{SceneKey: "group:1"}); err != nil {
		t.Fatal(err)
	}
	if e.stickersOnlyRecently("group:1", 2) {
		t.Fatal("中间说过话就不该再拦")
	}
}

// C: ACG 梗也要进种子 (Ciallo 那天它回了「这什么颜文字」)。
func TestSeedCommonMemes_KnowsACG(t *testing.T) {
	s := newTestStore(t)
	if _, err := SeedCommonMemes(s); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"Ciallo", "魔丸", "典急孝乐绷"} {
		sl, err := s.SlangByPhrase(p)
		if err != nil || sl == nil || !strings.Contains(sl.Meaning, "") {
			t.Fatalf("ACG 梗 %q 没进种子", p)
		}
	}
}
