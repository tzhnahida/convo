package convo

import (
	"context"
	"strings"
	"testing"
)

// ⚠️ **本批的头号契约**: 空文本 + kind=text, 在群里就是**一张图**。
//
// 2026-10-14 实测: 420 条群消息里 115 条是这样 (27%), 拿 nimbus 历史逐条对过,
// 它们 **segments 全是 image**。
//
// 我一度把它们当噪声删掉 —— 那等于把"有人发了张图"从它记忆里抹掉,
// 而它之后接的话会完全不搭。
func TestRecentDialog_EmptyTextRendersAsImage(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	sk := "group:1"
	for _, m := range []string{"在识别人类", "", "妈的"} {
		if _, err := e.Store.InsertMessageFull("user", "qq", "text", m, MsgMeta{SceneKey: sk, SenderUIN: "1001"}); err != nil {
			t.Fatal(err)
		}
	}
	got := e.recentDialogIn(sk, 20)
	if !strings.Contains(got, "[图") {
		t.Fatalf("空条目没有渲染成图片 —— 群友发的那张图从上下文里消失了:\n%q", got)
	}
	if strings.Contains(got, "群友: \n") {
		t.Fatalf("还有真正的空行:\n%q", got)
	}
	if !strings.Contains(got, "在识别人类") || !strings.Contains(got, "妈的") {
		t.Fatalf("有内容的不该被动:\n%q", got)
	}
}

// ⚠️ **`kind=image` 必须进上下文** —— 原来只放行 text/ask, 图片被整个滤掉,
// 于是它看不到"有人发了张图", 只能看到前后两句话, 而那两句往往正是在说那张图。
func TestRecentDialog_IncludesImageKind(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	sk := "group:2"
	if _, err := e.Store.InsertMessageFull("user", "qq", "image", "[图片]", MsgMeta{SceneKey: sk, SenderUIN: "2002"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.InsertMessageFull("user", "qq", "text", "真好看", MsgMeta{SceneKey: sk, SenderUIN: "2002"}); err != nil {
		t.Fatal(err)
	}
	got := e.recentDialogIn(sk, 20)
	if !strings.Contains(got, "[图") {
		t.Fatalf("kind=image 被滤掉了 —— 它看不到那张图:\n%q", got)
	}
}

// 表情同理: 群里发个表情也是"发生了什么"。
func TestRecentDialog_IncludesStickerKind(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	sk := "group:3"
	if _, err := e.Store.InsertMessageFull("user", "qq", "sticker", ImageStickerMark, MsgMeta{SceneKey: sk, SenderUIN: "3003"}); err != nil {
		t.Fatal(err)
	}
	if got := e.recentDialogIn(sk, 10); !strings.Contains(got, "[图") {
		t.Fatalf("sticker 被滤掉了: %q", got)
	}
}

// ⚠️ **接线测试** (2026-10-14): 上面那组只测了折叠函数本身,
// 而"函数对"和"真的被调用"是两件事 —— 这个项目里我犯过不止一次:
// 写好的东西一行都没被生产代码走到 (worldNote / maybeNews 都是)。
//
// 所以这里走**真实的入库 → 拼历史**这条路。
//
// 用户定的口径: 同一张图第一次给完整内容, 之后只留「内容往上找」。
func TestRecentDialog_CollapsesRepeatedImage(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	sk := "group:4"
	full := "(照片) [图: 一张被干黄的杯子]"
	for i, m := range []string{full, "妈的", full} {
		uin := "400" + string(rune('1'+i))
		if _, err := e.Store.InsertMessageFull("user", "qq", "image", m,
			MsgMeta{SceneKey: sk, SenderUIN: uin}); err != nil {
			t.Fatal(err)
		}
	}
	got := e.recentDialogIn(sk, 20)
	if strings.Count(got, "一张被干黄的杯子") != 1 {
		t.Fatalf("完整内容应该只出现一次 (第一次), 实际:\n%s", got)
	}
	if !strings.Contains(got, "往上找") {
		t.Fatalf("第二次该折叠成指针:\n%s", got)
	}
	if !strings.Contains(got, "妈的") {
		t.Fatalf("中间那句文本被吃了:\n%s", got)
	}
}

// ⚠️ **反面用例, 比正面重要**: 窗口里只有一次 → 一个字都不许折叠。
//
// 线上那个悬空指针就是这条反例没被守住的结果。
func TestRecentDialog_SingleImageStaysFull(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	sk := "group:5"
	full := "(照片) [图: 只有这一张]"
	if _, err := e.Store.InsertMessageFull("user", "qq", "image", full,
		MsgMeta{SceneKey: sk, SenderUIN: "5001"}); err != nil {
		t.Fatal(err)
	}
	got := e.recentDialogIn(sk, 20)
	if strings.Contains(got, "往上找") {
		t.Fatalf("只出现一次却被换成了指针 —— 这就是悬空:\n%s", got)
	}
	if !strings.Contains(got, "只有这一张") {
		t.Fatalf("内容丢了:\n%s", got)
	}
}

// ⚠️ **群里每个人必须分得开** (2026-10-14 定案)。
//
// sender_uin 一直是空的 —— 因为群路径走的是 InsertMessageEx,
// 它把 uin 硬编码成 ""。于是三个人说话在它眼里是**同一个人**,
// 多主体发言流 (§6.10.3) 等于白建, 接的话只能驴头不对马嘴。
func TestRecentDialog_GroupKeepsSpeakersApart(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	sk := "group:6"
	for _, p := range []struct{ uin, text string }{
		{"111111", "茶渍为什么这么重"},
		{"222222", "不好"},
		{"333333", "给我杯子都干黄了"},
	} {
		if _, err := e.Store.InsertMessageFull("user", "qq", "text", p.text,
			MsgMeta{SceneKey: sk, SenderUIN: p.uin}); err != nil {
			t.Fatal(err)
		}
	}
	got := e.recentDialogIn(sk, 20)
	// uin 后四位是锚 —— 三个人必须落成三个不同的前缀。
	for _, tail := range []string{"1111", "2222", "3333"} {
		if !strings.Contains(got, tail) {
			t.Fatalf("发言人 %s 没区分出来 —— 它会把三条当成同一个人说的:\n%s", tail, got)
		}
	}
}

// ⚠️ **既没文本也没图** = 真的什么都没发生 (撤回 / 桥接方没给内容)。
// ⚠️ 但这与"空文本**有图**"是两回事 —— 后者绝不能在这里挡掉。
func TestOnUserMessage_TrulyEmptyIsIgnored(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	for _, bad := range []string{"", "   ", "\n"} {
		if err := e.OnUserMessage(context.Background(), bad, nil); err != nil {
			t.Fatal(err)
		}
	}
	msgs, err := e.Store.RecentMessages(20)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if strings.TrimSpace(m.Payload) == "" {
			t.Fatalf("既没文本也没图的消息入库了: %+v", m)
		}
	}
}
