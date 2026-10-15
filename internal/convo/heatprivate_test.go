package convo

import (
	"context"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────
// §6.7 **私聊的热力入账** —— 仿真器抓到的那个 bug
//
// ⚠️ 2026-10-16 用 `cmd/convo-sim` 跑 10 天, 汇总里只有一句:
//
//	**她主动 0** · 你说话 10
//
// 根因: `NoteSceneHeat` **只挂在群裁决上**, 私聊一次都没有 ⇒
// 私聊场景没有热力行 ⇒ `heatNow` 返 `(0,true)` ⇒ `HeatQuota(0) = 0` ⇒
// **主动配额恒为 0**。
//
// ⚠️ 而 `heatwire.go` 的头部**逐字写过这个病** —— 它只修了群。
// ⚠️ 单测测的是"`NoteSceneHeat` 的行为对不对",
// **没有人问"私聊路径有没有调它"** —— 而那是两件不同的事。
// ─────────────────────────────────────────────────────────────

// TestHeat_PrivateMessageIsRecorded (这一条就是那个问题的尺子)
//
// ⚠️ 它问的不是"入账函数对不对", 而是「**走一遍私聊, 热力有没有涨**」。
func TestHeat_PrivateMessageIsRecorded(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	if err := EnsureHeatSchema(e.Store); err != nil {
		t.Fatal(err)
	}
	if err := EnsureSceneSchema(e.Store); err != nil {
		t.Fatal(err)
	}
	if err := EnsureBondSchema(e.Store.DB); err != nil {
		t.Fatal(err)
	}

	// 先钉住基线: 没说过话时不该有热力行。
	if _, ok, _ := e.Store.LoadHeat(DefaultSceneKey); ok {
		t.Fatal("还没说话就有热力行 —— 夹具不干净")
	}

	if err := e.OnUserText(context.Background(), "在吗"); err != nil {
		t.Fatal(err)
	}

	// ⚠️ 这条不变量: **走过一条私聊消息, 私聊场景必须有热力**。
	st, ok, err := e.Store.LoadHeat(DefaultSceneKey)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("**私聊消息没有产生热力行** —— 那 `heatNow` 永远返 0, 主动配额恒为 0 (她再也不会主动开口)")
	}
	if st.Value <= 0 {
		t.Fatalf("私聊入账后热力仍是 %.3f —— 那和「没有热力行」是同一个后果", st.Value)
	}

	// ⚠️ 而读取口也必须跟着变 —— 只写库不改读数等于没接。
	v, has := e.heatNow(time.Now())
	if !has || v <= 0 {
		t.Fatalf("**私聊热力读出来是 0** (has=%v v=%.3f) —— 配额还是 0", has, v)
	}
}
