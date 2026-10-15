package convo

import (
	"strings"
	"testing"
)

// ——— §6.10.3 多主体发言流 ———

// TestRecentDialog_MultiParty **群里的发言流必须是多主体的**。
//
// 反面: 群消息也走「你:/她:」二元 → 三个人先后说话被接成同一个人说的
// → **第一句话就接错人** → 红。
func TestRecentDialog_MultiParty(t *testing.T) {
	s := newTestStore(t)
	e := &Engine{Store: s, Cfg: DefaultConfig()}
	for _, m := range []struct{ uin, text string }{
		{"10001", "A 说的"}, {"20002", "B 说的"}, {"30003", "C 说的"},
	} {
		if _, err := s.InsertMessageFull("user", "qq", "text", m.text,
			MsgMeta{Origin: OriginUser, SceneKey: "group:1", SenderUIN: m.uin}); err != nil {
			t.Fatal(err)
		}
	}
	h := e.recentDialog(10)
	// 三个发言人必须**分得开** —— 每行的锚是 uin 尾四位。
	for _, want := range []string{"0001", "0002", "0003"} {
		if !strings.Contains(h, want) {
			t.Fatalf("发言人 %s 没出现在发言流里 (三个人会被接成同一个人):\n%s", want, h)
		}
	}
	if strings.Contains(h, "你: ") {
		t.Fatalf("群里退回了「你:/她:」二元 —— 那正是接错人的病灶:\n%s", h)
	}
	// ⚠️ **缺 uin 的群消息不许拼出「群友????」** —— UinTail("") 就是 "????"。
	// 那既难看又丢了信息: "有个人说的" 与 "uin 是问号" 是两件事。
	if strings.Contains(h, "????") {
		t.Fatalf("发言流里出现了 ???? (空 uin 被拼进了锚):\n%s", h)
	}

	// ⚠️ owner 标记绝不进注入 (§6.10.7): 发言流里不许出现任何特权标记。
	for _, leak := range []string{"owner", "主人", "管理员"} {
		if strings.Contains(h, leak) {
			t.Fatalf("发言流里泄露了特权标记 %q —— 等于在群里公示主人:\n%s", leak, h)
		}
	}
}

// TestRecentDialog_PrivateStaysBinary 反面: **私聊不许被改成多主体**。
//
// 私聊只有一个主体, 加 uin 是噪声; 而且它会**改动每一轮的 system prompt** ——
// 那是拿缓存换没有的信息。
func TestRecentDialog_PrivateStaysBinary(t *testing.T) {
	s := newTestStore(t)
	e := &Engine{Store: s, Cfg: DefaultConfig()}
	if _, err := s.InsertMessage("user", "qq", "text", "私聊说的"); err != nil {
		t.Fatal(err)
	}
	h := e.recentDialog(10)
	if !strings.Contains(h, "你: 私聊说的") {
		t.Fatalf("私聊格式变了 (会改动每一轮的 prompt):\n%s", h)
	}
	if strings.Contains(h, "群友") {
		t.Fatal("私聊被改成了多主体")
	}
}

// TestRecentDialog_MissingSenderUIN 反面: 空 uin 不许拼出「群友????」。
//
// 这个 bug 是**变异测试的输出**意外暴露的 —— 我本来只在测私聊格式,
// 而变异体让私聊走了群分支, 于是屏幕上的 "群友????" 把空 uin 的处理问题打了出来。
func TestRecentDialog_MissingSenderUIN(t *testing.T) {
	s := newTestStore(t)
	e := &Engine{Store: s, Cfg: DefaultConfig()}
	if _, err := s.InsertMessageFull("user", "qq", "text", "没带 uin 的群消息",
		MsgMeta{Origin: OriginUser, SceneKey: "group:1"}); err != nil {
		t.Fatal(err)
	}
	h := e.recentDialog(10)
	if strings.Contains(h, "????") {
		t.Fatalf("空 uin 被拼成了「群友????」:\n%s", h)
	}
	if !strings.Contains(h, "群友: 没带 uin 的群消息") {
		t.Fatalf("缺 uin 时应当退化成「群友」而不是丢掉发言人标识:\n%s", h)
	}
}
