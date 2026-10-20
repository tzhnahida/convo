package convo

import (
	"strings"
	"testing"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
)

const threadSelfUIN = "3487037837"

// ⚠️ 2026-10-20 用户报: 「有时候没有 @ 但是话题没结束他就不回消息了」。
//
// 根因不是性格, 是接线: §6.10.2 ③「无 @ 按线程归属」要求 HasPrev/PrevUIN/PrevTS,
// 而 cmd/convo/main.go 构造 SceneMsg 时**从来没填过**这三个字段 ——
// 于是每条无 @ 的群消息都掉进"判不出来 → 随机插话 → 绝大多数沉默"。
// 更早一层: 就算判据返回了线程理由, 裁决 switch 里也没有 case 接住它。
//
// 这一组用例钉的就是: 材料填上、case 接上之后, 它真的会接话。
func TestGate_ThreadOursSpeaks(t *testing.T) {
	e := gateEngine(t)
	now := clock.Now()
	res := e.GateGroupMessage(SceneMsg{
		SceneKey: sceneWireKey, UIN: "111", Text: "哈哈 那你接着说",
		HasPrev: true, PrevUIN: threadSelfUIN, PrevTS: now.Add(-2 * time.Minute), TS: now,
	}, SelfIdentity{UIN: threadSelfUIN}, sceneWireCfg())
	if res.AddressedWhy != ReasonThreadOurs {
		t.Fatalf("上一条是它说的, 却没判成线程延续: %q", res.AddressedWhy)
	}
	if res.Stance != StanceSpeak {
		t.Fatalf("线程延续却没有接话: %s (%s)", res.Stance, res.Reason)
	}
}

// 它刚说完话又接一句 = 刷屏 —— 间隔太近要缓一次。
func TestGate_ThreadOursJustSpokeIsSilent(t *testing.T) {
	e := gateEngine(t)
	now := clock.Now()
	res := e.GateGroupMessage(SceneMsg{
		SceneKey: sceneWireKey, UIN: "111", Text: "嗯",
		HasPrev: true, PrevUIN: threadSelfUIN, PrevTS: now.Add(-3 * time.Second), TS: now,
	}, SelfIdentity{UIN: threadSelfUIN}, sceneWireCfg())
	if res.Stance != StanceSilent || !strings.Contains(res.Reason, "缓一下") {
		t.Fatalf("刚说完话又接了一句: %s (%s)", res.Stance, res.Reason)
	}
}

// 没 @ 但被点名 → 必须**有反应**。
//
// ⚠️ 是表情档, 不是开口: 「被叫到」只值一个动作, 开口仍要过价值判断。
// (2026-10-20 我一度把它改成直接开口, 三条既有用例当场红 —— 那条纪律是对的。)
func TestGate_NamedWithoutAtReacts(t *testing.T) {
	e := gateEngine(t)
	res := e.GateGroupMessage(SceneMsg{
		SceneKey: sceneWireKey, UIN: "111", Text: "小夜 你觉得呢", TS: clock.Now(),
	}, SelfIdentity{UIN: threadSelfUIN, Nickname: "小夜"}, sceneWireCfg())
	if res.AddressedWhy != ReasonNamed {
		t.Fatalf("被点名却没判出来: %q", res.AddressedWhy)
	}
	if res.Stance != StanceReact {
		t.Fatalf("被点名应当是表情档, 得到 %s (%s)", res.Stance, res.Reason)
	}
}

// 线程判据的唯一数据源: 必须取「当前这条的前一条」, 错一位整个判据就错。
func TestPrevSceneMessage_TakesTheOneBeforeTheLatest(t *testing.T) {
	s := newTestStore(t)
	insert := func(dir, uin, text string) {
		t.Helper()
		meta := MsgMeta{SceneKey: sceneWireKey}
		if uin != "" {
			meta.SenderUIN = uin
		}
		if _, err := s.InsertMessageFull(dir, "qq", "text", text, meta); err != nil {
			t.Fatal(err)
		}
	}
	insert("user", "111", "第一条")
	insert("ai", "", "它说的那句")
	insert("user", "222", "第三条") // = 当前这条

	dir, uin, _, ok, err := s.PrevSceneMessage(sceneWireKey)
	if err != nil || !ok {
		t.Fatalf("取不到上一条: ok=%v err=%v", ok, err)
	}
	if dir != "ai" || uin != "" {
		t.Fatalf("取错了那一条: dir=%q uin=%q (期望它自己说的那句)", dir, uin)
	}
}
