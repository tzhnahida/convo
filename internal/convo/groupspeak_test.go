package convo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ——— §6.10 群执行路径 ———

type groupCapSender struct {
	groupTexts  []string
	groupImages []string
	groupID     int64
	privateHits int // ⚠️ 私聊调用计数 —— 它必须恒为 0
}

func (s *groupCapSender) SendText(_ int64, _ string) error  { s.privateHits++; return nil }
func (s *groupCapSender) SendImage(_ int64, _ string) error { s.privateHits++; return nil }
func (s *groupCapSender) SendGroupText(gid int64, text string) error {
	s.groupID = gid
	s.groupTexts = append(s.groupTexts, text)
	return nil
}
func (s *groupCapSender) SendGroupImage(gid int64, p string) error {
	s.groupID = gid
	s.groupImages = append(s.groupImages, p)
	return nil
}

func groupSpeakEngine(t *testing.T, c Chatter) (*Engine, *groupCapSender) {
	t.Helper()
	s := newTestStore(t)
	snd := &groupCapSender{}
	if err := s.SaveProfile(&PersonaProfile{Name: "小夜", Template: "bestie",
		CreatedTS: time.Now().Format(bodyTSLayout)}); err != nil {
		t.Fatal(err)
	}
	// 一张可用表情 (第二态要用)。
	// ⚠️ **必须是磁盘上真的存在的文件** —— ReactInGroup 现在会 os.Stat 它。
	// 夹具写死一个不存在的路径, 测出来的就是"发不出表情", 而那是夹具的错。
	gif := filepath.Join(t.TempDir(), "s.gif")
	if err := os.WriteFile(gif, []byte("GIF89a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertSticker("https://fake/x.gif", gif, "ja", "builtin", 0.5); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: s, Cfg: DefaultConfig(), Dialog: &DialogEngine{Store: s},
		Chatter: c, Sender: snd, UserID: 1, Lang: "ja"}
	return e, snd
}

// TestSpeakInGroup_GoesToGroupNotPrivate **本组最要紧的一条**。
//
// 反面: 群执行里用 SendText 而不是 SendGroupText → 一条本该进群的消息
// **私聊发给主人** → 群里什么都没发生, 而 owner 收到莫名其妙的私聊。红。
func TestSpeakInGroup_GoesToGroupNotPrivate(t *testing.T) {
	c := &sitCaptureChatter{}
	e, snd := groupSpeakEngine(t, c)
	if err := e.SpeakInGroup(context.Background(), "group:12345", 12345, "999", "在吗"); err != nil {
		t.Fatalf("SpeakInGroup: %v", err)
	}
	if len(snd.groupTexts) == 0 {
		t.Fatal("群里一条都没发出去")
	}
	if snd.groupID != 12345 {
		t.Fatalf("发到了错误的群: %d", snd.groupID)
	}
	if snd.privateHits != 0 {
		t.Fatalf("**私聊通道被调用了 %d 次** —— 群消息私聊发给主人比不发更糟", snd.privateHits)
	}
}

// TestSpeakInGroup_HistoryIsSceneScoped **跨场景泄漏口**。
//
// 反面: 群路径用全局 RecentMessages → 把 B 群的话读进 A 群的上下文 → 红。
// 它的表现是**静默**的: "它怎么知道那件事"。
func TestSpeakInGroup_HistoryIsSceneScoped(t *testing.T) {
	c := &sitCaptureChatter{}
	e, _ := groupSpeakEngine(t, c)
	// A 群有一句很显眼的话。
	if _, err := e.Store.InsertMessageFull("user", "qq", "text", "甲群机密暗号XYZ",
		MsgMeta{Origin: OriginUser, SceneKey: "group:111", SenderUIN: "50001"}); err != nil {
		t.Fatal(err)
	}
	// 在 B 群发言。
	if err := e.SpeakInGroup(context.Background(), "group:222", 222, "60002", "B 群问一句"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(c.lastSystem, "甲群机密暗号XYZ") {
		t.Fatalf("A 群的话进了 B 群的上下文 —— 那正是 §6.4.2 禁止的跨人转述:\n%.500s", c.lastSystem)
	}
}

// TestReactInGroup_ZeroLLMCalls §6.1 **第二态: 0 次 LLM 调用**。
//
// 反面: 表情档退化成"发一句文本" → 一次本该免费的反应花掉一次生成 → 红。
func TestReactInGroup_ZeroLLMCalls(t *testing.T) {
	c := &sitCaptureChatter{}
	e, snd := groupSpeakEngine(t, c)
	if err := e.ReactInGroup("group:12345", 12345); err != nil {
		t.Fatalf("ReactInGroup: %v", err)
	}
	if c.n != 0 {
		t.Fatalf("表情档调用了 %d 次 LLM —— 它必须是 0 次 (那是「九成判否」作为成本前提的落法)", c.n)
	}
	if len(snd.groupImages) != 1 {
		t.Fatalf("应当发出一张表情, 实际 %d 张", len(snd.groupImages))
	}
	if len(snd.groupTexts) != 0 {
		t.Fatal("表情档发了文本 —— 那就变成第三态了")
	}
	if snd.privateHits != 0 {
		t.Fatal("表情档走了私聊通道")
	}
}

// TestSpeakInGroup_RefusedIsSilentlyDropped A6 硬拒: **刻意不说**, 静默丢弃。
//
// ⚠️ **变异测试发现这条测试测不出它以为在测的东西。**
// 把 `if frame.Refused { return nil }` 整个删掉, 这条**照样通过** ——
// 因为 A6 硬拒返回的帧是 OutputFrame{Refused:true}, **Reply 恒为空**,
// 循环里一件东西都拿不到。
//
// 所以"硬拒不发送"真正靠的是**上游那条契约** (硬拒帧必须为空), 不是这里的 return。
// 那个 return 是**防御性的**, 不是功能性的 —— 这条测试验证的也只是
// "不发、不报错" 这个可观测行为, 它抓不住上面的变异, 也不该假装能。
// 上游契约由 TestDialogReply_HardRejectIsEmptyFrame 单独钉住。
func TestSpeakInGroup_RefusedIsSilentlyDropped(t *testing.T) {
	c := &awChatter{out: `{"reply":[{"kind":"text","text":"你最近都不理我"}]}`}
	e, snd := groupSpeakEngine(t, c)
	if err := e.SpeakInGroup(context.Background(), "group:12345", 12345, "999", "在吗"); err != nil {
		t.Fatalf("硬拒不该报错: %v", err)
	}
	if len(snd.groupTexts) != 0 {
		t.Fatalf("硬拒却发出去了: %v —— 刻意不说该静默丢弃", snd.groupTexts)
	}
	if len(snd.groupImages) != 0 {
		t.Fatal("硬拒却发了表情")
	}
	if snd.privateHits != 0 {
		t.Fatal("硬拒走了私聊通道")
	}
}

// TestDialogReply_HardRejectIsEmptyFrame **「硬拒帧必须为空」** ——
// 这条才是"硬拒不发送"的真正保证。
//
// ⚠️ 它为什么必须单独存在: 下游每一处都要写 `if frame.Refused { return }`,
// 而那些 return 全都**可以被删掉而测试仍然通过** (Reply 是空的)。
// 也就是说: 只要这条契约破了, 下游**所有**防线会**同时**失效 ——
// 而且是静默的。把契约钉在它真正的来源上, 才不用靠每个调用方都记得写 return。
func TestDialogReply_HardRejectIsEmptyFrame(t *testing.T) {
	c := &awChatter{out: `{"reply":[{"kind":"text","text":"你最近都不理我"}]}`}
	e, _ := groupSpeakEngine(t, c)
	p, err := e.Store.LoadProfile()
	if err != nil || p == nil {
		t.Fatal("前置: 需要有 profile")
	}
	frame, err := e.dialogReply(context.Background(), DialogInput{UserText: "在吗", State: StateFree}, p)
	if err != nil {
		t.Fatalf("硬拒不该报错: %v", err)
	}
	if !frame.Refused {
		t.Fatalf("内疚话术没被硬拒 —— 上游的判据变了? frame.Reply=%+v", frame.Reply)
	}
	if len(frame.Reply) != 0 {
		t.Fatalf("硬拒帧里有 %d 条内容 —— 下游任何一处漏了 return 就会把它发出去", len(frame.Reply))
	}
	if frame.RefuseReason == "" {
		t.Fatal("硬拒必须带原因 —— 否则回放时不知道它为什么不说")
	}
}
