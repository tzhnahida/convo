package convo

import (
	"context"
	"strings"
	"testing"
)

// ⚠️ 2026-10-18 生产事故: preview 那轮的 DialogInput 没带 SceneKey,
// 于是 recentDialogIn("") 退回**全局历史**(群 + 私混在一起), 而出口是私聊 ——
// 它在私聊里接着说群里聊的「26 层楼梯」。修法是在 proactive 里把空场景归一。
func TestProactive_EmptySceneReadsPrivateHistoryOnly(t *testing.T) {
	ch := &sitCaptureChatter{}
	e, _ := groupSpeakEngine(t, ch)
	e.DayMoodFn = func(string) DayMood { return DayMoodFlat } // 别让"今天是什么日子"决定这条测试

	if _, err := e.Store.InsertMessageFull("user", "qq", "text", "群友在聊二十六层楼梯", MsgMeta{SceneKey: "group:1034712035"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.InsertMessageFull("user", "qq", "text", "主人这边在说数学题", MsgMeta{SceneKey: DefaultSceneKey}); err != nil {
		t.Fatal(err)
	}
	p, err := e.Store.LoadProfile()
	if err != nil || p == nil {
		t.Fatalf("夹具没有 profile: %v", err)
	}

	// 空场景 = 私聊 —— 正是 preview / 主动开口那 10 个调用点的形状。
	if err := e.proactive(context.Background(), p,
		DialogInput{ActionKind: ActPreview}, "previews", "2026-10-18"); err != nil {
		t.Fatal(err)
	}
	if ch.n == 0 {
		t.Fatal("一次模型调用都没有 —— 这条测试的前提变了")
	}
	if !strings.Contains(ch.lastSystem, "主人这边在说数学题") {
		t.Fatal("私聊历史没进来 —— 归一之后应该取到它")
	}
	if strings.Contains(ch.lastSystem, "群友在聊二十六层楼梯") {
		t.Fatal("群里的内容漏进了私聊上下文 —— 场景隔离没生效")
	}
}
