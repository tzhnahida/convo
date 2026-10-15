package convo

import (
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────
// §6.9 信号 ②「互相 @」的存储层
//
// ⚠️ 在 at_uins 这一列存在之前, MsgMeta / MessageRecord **都不带 @ 目标** ——
// 于是信号 ② 在生产窗口上**永远算不出来**。
//
// 后果不是"判不出冲突", 而是**门槛实际是 3/3**:
// SignalsRequired 默认 3, 而能算的只剩 ①连续对呛 ③负面密度 ④间隔骤短。
// ─────────────────────────────────────────────────────────────

// TestAtUINs_RoundTripThroughTheStore
//
// ⚠️ **这一条是"库里真的存下来了"的尺子** ——
// 光测 Join/Split 两个纯函数证明不了写入路径接上了。
func TestAtUINs_RoundTripThroughTheStore(t *testing.T) {
	e := gateEngine(t)
	s := e.Store
	scene := "group:12345"
	cflSceneAt(t, s, scene, ConflictStageIntimate)

	if _, err := s.InsertMessageFull("user", "qq", "text", "你俩别吵了",
		MsgMeta{Origin: OriginUser, SceneKey: scene, SenderUIN: "u1", AtUINs: []string{"u2", "777"}}); err != nil {
		t.Fatal(err)
	}
	msgs, err := s.RecentMessagesIn(scene, 5)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("读回失败: n=%d err=%v", len(msgs), err)
	}
	got := msgs[0].AtUINs
	if len(got) != 2 || got[0] != "u2" || got[1] != "777" {
		t.Fatalf("@ 目标没存住: %v", got)
	}
	// 没 @ 任何人的那条 → nil (零值 = 没 @, 不是"@ 了空字符串")。
	if _, err := s.InsertMessageFull("user", "qq", "text", "今天天气不错",
		MsgMeta{Origin: OriginUser, SceneKey: scene, SenderUIN: "u1"}); err != nil {
		t.Fatal(err)
	}
	msgs2, _ := s.RecentMessagesIn(scene, 5)
	for _, m := range msgs2 {
		if m.Payload == "今天天气不错" && len(m.AtUINs) != 0 {
			t.Fatalf("没 @ 人的消息却有 @ 目标: %v", m.AtUINs)
		}
	}
}

// TestJoinAtUINs_DedupesAndTrims
func TestJoinAtUINs_DedupesAndTrims(t *testing.T) {
	if got := JoinAtUINs([]string{"u1", " u1 ", "", "  ", "u2"}); got != "u1,u2" {
		t.Fatalf("去重/去空白不对: %q", got)
	}
	if got := JoinAtUINs(nil); got != "" {
		t.Fatalf("空输入应当存空串: %q", got)
	}
	if got := SplitAtUINs(""); got != nil {
		t.Fatalf("空串应当拆成 nil(没 @ 任何人), 实得 %v", got)
	}
	if got := SplitAtUINs("u1,,u2"); len(got) != 2 {
		t.Fatalf("空段没被跳过: %v", got)
	}
}

// TestMutualAtSignal_NowComputable
//
// ⚠️ **这条钉的是那个门槛**: ② 能算之后, 四信号里就有三个可用了。
// 而在此之前它恒为 false —— 一个恒 false 的信号,
// 等于**静默地**把门槛抬高了一个。
func TestMutualAtSignal_NowComputable(t *testing.T) {
	pol := DefaultConflictPolicy()
	ts := time.Date(2026, 3, 1, 20, 0, 0, 0, time.Local)
	// 两个人**互相 @** —— 这是 ② 的定义。
	msgs := []ConflictMsg{
		{Uin: "u1", Text: "你说什么呢", Ats: []string{"u2"}, TS: ts},
		{Uin: "u2", Text: "我说你呢", Ats: []string{"u1"}, TS: ts.Add(10 * time.Second)},
	}
	base := ConflictBaselineFrom(msgs, pol)
	sig := ConflictSignalsOf(msgs, base, pol)
	if !sig.MutualAt {
		t.Fatalf("互相 @ 却没有判出信号 ②: %+v", sig)
	}
	// ⚠️ 反面: **单向 @ 不算** —— 一个人追着骂不是"互相"。
	oneWay := []ConflictMsg{
		{Uin: "u1", Text: "你说什么呢", Ats: []string{"u2"}, TS: ts},
		{Uin: "u2", Text: "嗯", TS: ts.Add(10 * time.Second)},
	}
	if s2 := ConflictSignalsOf(oneWay, ConflictBaselineFrom(oneWay, pol), pol); s2.MutualAt {
		t.Fatal("单向 @ 被当成了互相 @ —— 那会把单方面的抱怨判成冲突")
	}
}

// TestConflictWindowOf_CarriesAtUINsToTheJudge
//
// ⚠️ **接线尺子**: 库里有了不算数, 要**真的传进判定**才算。
func TestConflictWindowOf_CarriesAtUINsToTheJudge(t *testing.T) {
	e := gateEngine(t)
	s := e.Store
	scene := "group:12345"
	cflSceneAt(t, s, scene, ConflictStageIntimate)
	if _, err := s.InsertMessageFull("user", "qq", "text", "你说话啊",
		MsgMeta{Origin: OriginUser, SceneKey: scene, SenderUIN: "u1", AtUINs: []string{"u2"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertMessageFull("user", "qq", "text", "我在说",
		MsgMeta{Origin: OriginUser, SceneKey: scene, SenderUIN: "u2", AtUINs: []string{"u1"}}); err != nil {
		t.Fatal(err)
	}
	cmsgs, _, ok := e.conflictWindowOf(scene, 20)
	if !ok || len(cmsgs) != 2 {
		t.Fatalf("窗口没读到: ok=%v n=%d", ok, len(cmsgs))
	}
	for _, m := range cmsgs {
		if len(m.Ats) == 0 {
			t.Fatalf("%s 的 @ 目标在窗口里丢了 —— 信号 ② 还是算不出来", m.Uin)
		}
	}
}
