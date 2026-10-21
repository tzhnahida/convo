package convo

import (
	"strings"
	"testing"

	"github.com/tzhnahida/convo/internal/clock"
)

func TestCollapseOwnBursts_MergesConsecutiveSelfLines(t *testing.T) {
	in := []string{
		"【群1=group:1】1=甲(1111) 我=它自己",
		"1389 1: 靠你一个数列题目",
		"1390 我: 这不是找规律吗",
		"1391 我: ？",
		"1392 我: 这啥题",
		"1393 我: 你直接说吧",
		"1399 1: 找规律",
	}
	got := collapseOwnBursts(in)
	want := []string{
		"【群1=group:1】1=甲(1111) 我=它自己",
		"1389 1: 靠你一个数列题目",
		"1390 我: 这不是找规律吗 / ？ / 这啥题 / 你直接说吧",
		"1399 1: 找规律",
	}
	if len(got) != len(want) {
		t.Fatalf("行数不对: %d → %d: %v", len(in), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 行不对:\n got=%q\nwant=%q", i, got[i], want[i])
		}
	}
}

// 上限: 一次合并最多保留 OwnBurstLimit 段, 多出来的丢掉 (窗口要留给人)。
func TestCollapseOwnBursts_CapsLongBursts(t *testing.T) {
	in := []string{"1 我: a", "2 我: b", "3 我: c", "4 我: d", "5 我: e", "6 我: f"}
	got := collapseOwnBursts(in)
	if len(got) != 1 {
		t.Fatalf("连续发言应当合成一行, 得到 %d 行: %v", len(got), got)
	}
	if n := strings.Count(got[0], " / "); n != OwnBurstLimit-1 {
		t.Fatalf("合并段数超出上限: %d 段 (%q)", n+1, got[0])
	}
}

// 同一句长问题 2 分钟内重复 → 只答一次。
func TestGate_RepeatedQuestionAnsweredOnce(t *testing.T) {
	e := gateEngine(t)
	if err := EnsureSpeechGuardSchema(e.Store.DB); err != nil {
		t.Fatal(err)
	}
	q := "0 1 6 8 9 11 69 88 96 下一位是什么"
	for i := 0; i < 2; i++ {
		if _, err := e.Store.InsertMessageFull("user", "qq", "text", q, MsgMeta{SceneKey: sceneWireKey, SenderUIN: "111"}); err != nil {
			t.Fatal(err)
		}
	}
	res := e.GateGroupMessage(SceneMsg{SceneKey: sceneWireKey, UIN: "111", Text: q, AtUINs: []string{"3487037837"}, TS: clock.Now()},
		SelfIdentity{UIN: "3487037837"}, sceneWireCfg())
	if res.Stance != StanceSilent || !strings.Contains(res.Reason, "同一句重复") {
		t.Fatalf("重复问题被答了第二遍: %s (%s)", res.Stance, res.Reason)
	}
}

// 短句重复是聊天常态, 不许拦。
func TestGate_ShortRepeatStillAllowed(t *testing.T) {
	e := gateEngine(t)
	if err := EnsureSpeechGuardSchema(e.Store.DB); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := e.Store.InsertMessageFull("user", "qq", "text", "哈哈", MsgMeta{SceneKey: sceneWireKey, SenderUIN: "111"}); err != nil {
			t.Fatal(err)
		}
	}
	if e.isRepeatedQuestion(SceneMsg{SceneKey: sceneWireKey, UIN: "111", Text: "哈哈"}) {
		t.Fatal("短句重复被当成了重复问题")
	}
}
