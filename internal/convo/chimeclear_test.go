package convo

import (
	"testing"
)

// ─────────────────────────────────────────────────────────────
// 插话收敛的**安全阀**: 放行一个群 (`ClearChimeComplaint` + 它的接口)
//
// ⚠️ 它存在的理由是一条**生产事实**: 2026-10-16 00:52, 群友 @ 着它说
// 「别刷屏闭嘴」→ 那个群的插话率**永久钉在 1%**, 而当时没有任何撤销口子。
// ─────────────────────────────────────────────────────────────

// TestChimeClear_ReleasesTheScene
//
// 正面: 放行之后**下一次**它是正常的 10%, 而不是 1%。
func TestChimeClear_ReleasesTheScene(t *testing.T) {
	e := gateEngine(t)
	if err := EnsureChimeSchema(e.Store.DB); err != nil {
		t.Fatal(err)
	}
	scene := chimeTestScene
	// 夹具: 它**当时确实在说话** —— 2026-10-18 起判据多要这条事实依据
	// (2026-10-16 那次误判的成因正是"它两小时只说 1 条, 却被一句「别刷屏」永久降级")。
	for i := 0; i < ChimeComplaintMinRecentSelf; i++ {
		if _, err := e.Store.InsertMessageFull("ai", "qq", "text", "随便说说", MsgMeta{SceneKey: scene}); err != nil {
			t.Fatal(err)
		}
	}
	// 再让它被抱怨一次(走生产路径)。
	chimeSay(t, e, "111", "别刷屏", true)
	if got, _ := e.Store.ChimeComplained(e.ownerKey(), scene); !got {
		t.Fatal("夹具坏了: 应当已经被抱怨过")
	}
	// ⚠️ 收敛时掷 5 **不插话**(1% 命中不了 5)。
	if d := DecideChime(ChimeInput{Roll: 5, WakingElapsed: 1.0, Complained: true}); d.Speak {
		t.Fatal("收敛之后掷 5 还插话 —— 夹具不对")
	}

	// 放行。
	if err := e.Store.ClearChimeComplaint(e.ownerKey(), scene); err != nil {
		t.Fatal(err)
	}
	got, err := e.Store.ChimeComplained(e.ownerKey(), scene)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Fatal("**放行之后仍然算被抱怨过** —— 那个安全阀没生效")
	}
	// ⚠️ 而**同一个掷数**现在应当插话(回到 10%)。
	if d := DecideChime(ChimeInput{Roll: 5, WakingElapsed: 1.0, Complained: false}); !d.Speak {
		t.Fatal("放行之后掷 5 仍不插话 —— 概率没有回到 10%")
	}
}

// TestChimeClear_NotComplainedChangesNothing (反面)
//
// ⚠️ 没被抱怨过的群调它**什么都不改** ——
// 尤其**不许插一行 `complained_ts=”` 的假记录**:
// 那会让"从没被说过"与"被说过又放行了"长得一模一样。
func TestChimeClear_NotComplainedChangesNothing(t *testing.T) {
	e := gateEngine(t)
	if err := EnsureChimeSchema(e.Store.DB); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		var n int
		if err := e.Store.DB.QueryRow("SELECT COUNT(*) FROM convo_chime_state").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	if err := e.Store.ClearChimeComplaint(e.ownerKey(), "group:not-complained"); err != nil {
		t.Fatal(err)
	}
	if after := count(); after != before {
		t.Fatalf("**放行一个没被抱怨过的群却插了一行**: %d → %d", before, after)
	}
}

// TestChimeClear_ThenItCanBeComplainedAgain
//
// ⚠️ 放行之后**还能再被抱怨** —— 否则那个阀门就是"用一次就废"。
func TestChimeClear_ThenItCanBeComplainedAgain(t *testing.T) {
	e := gateEngine(t)
	if err := EnsureChimeSchema(e.Store.DB); err != nil {
		t.Fatal(err)
	}
	scene := chimeTestScene
	seedChatty(t, e, scene)
	chimeSay(t, e, "111", "别刷屏", true)
	if err := e.Store.ClearChimeComplaint(e.ownerKey(), scene); err != nil {
		t.Fatal(err)
	}
	// 再来一次抱怨 —— 必须重新收敛。
	chimeSay(t, e, "222", "话太多了", true)
	if got, _ := e.Store.ChimeComplained(e.ownerKey(), scene); !got {
		t.Fatal("放行之后**再也抱怨不动了** —— 那这个阀门只能用一次")
	}
}
