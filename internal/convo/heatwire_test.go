package convo

import (
	"testing"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
)

// ─────────────────────────────────────────────────────────────
// §6.7 热力通道接线
//
// ⚠️ 这一节的病不是"少一个功能", 而是**它再也不会主动开口**:
//
//	没有热力行 → heatNow 返回 (0, true) → HeatQuota(0) = 0
//	→ ActiveToday < quota 永远为假。
//
// 生产实测: ai_opens 在 10-12 / 10-13 各 2 次, 10-14 起彻底消失。
// 而它的表现只是"它变安静了"—— 一个**看起来像性格**的故障。
// ─────────────────────────────────────────────────────────────

func heatEngine(t *testing.T) (*Engine, *Store) {
	t.Helper()
	s := agTestStore(t)
	return &Engine{Store: s, Cfg: DefaultConfig()}, s
}

// TestGateGroupMessage_RecordsHeatOnTheProductionPath
//
// ⚠️ **这条是"消费者在生产路径被调用"的尺子。**
//
// 光测 RecordHeatInteraction 是个库函数, 证明不了**真的有人调它** ——
// 而这一节原来的状态正是"库函数全绿、生产调用者为零"。
// 所以这条用例走的是**群消息裁决**那条真实入口。
func TestGateGroupMessage_RecordsHeatOnTheProductionPath(t *testing.T) {
	e, s := heatEngine(t)
	if _, ok, _ := s.LoadHeat("group:1034712035"); ok {
		t.Fatal("夹具坏了: 一开始就有热力行")
	}
	// 走生产入口 —— 群里来了一条消息。
	e.GateGroupMessage(SceneMsg{
		SceneKey: "group:1034712035",
		UIN:      "111",
		Text:     "晚上吃什么",
		TS:       clock.Now(),
	}, SelfIdentity{UIN: "3487037837"}, DefaultConfig().Group)

	if _, ok, _ := s.LoadHeat("group:1034712035"); !ok {
		t.Fatal("群消息走完了, 热力却一行都没写 —— 通道还是没接上")
	}
	// ⚠️ 而且**沉默档也算互动** —— 群里聊得热火朝天它一直在看, 那也是参与。
	// (反面: 只在"裁决通过"之后入账的话, 一条不打算回的消息就不涨热力,
	//  而热力恰恰是靠"这个场景有多热闹"涨起来的。)
	before, _, _ := s.LoadHeat("group:1034712035")
	e.GateGroupMessage(SceneMsg{SceneKey: "group:1034712035", UIN: "222", Text: "我不饿", TS: clock.Now().Add(time.Minute)},
		SelfIdentity{UIN: "3487037837"}, DefaultConfig().Group)
	after, _, _ := s.LoadHeat("group:1034712035")
	if after.SessionSeq <= before.SessionSeq {
		t.Fatalf("第二条消息没有推进会话序号: %d → %d", before.SessionSeq, after.SessionSeq)
	}
}

// TestHeatWiring_MakesTheProactiveQuotaNonZero
//
// ⚠️ **这条是端到端的那一步**: 互动之后, 主动配额必须**不再是 0**。
// 它是 ai_opens 停摆那件事的直接回归测试。
func TestHeatWiring_MakesTheProactiveQuotaNonZero(t *testing.T) {
	e, s := heatEngine(t)
	cfg := DefaultConfig()
	scene := DefaultSceneKey

	// ⚠️ **前提: 这个场景得不是 stranger。**
	//
	// stranger 的 cap 是 20, 而每条主动消息的代价是 25 → `floor(20/25) = 0`。
	// 那不是 bug, 是设计本意("不向陌生人掏心窝子") —— 但它意味着
	// **光接入账还不够**: 关系量必须真的能涨上去 (见下面那条死锁用例)。
	if err := SaveBond(s.DB, e.ownerKey(), scene, Bond{Closeness: BondFamiliarAt}); err != nil {
		t.Fatal(err)
	}

	// ① 接线**之前**的状态: 没有热力行 → 配额 0 → 永远不开口。
	heatV, hasHeat := e.heatNow(clock.Now())
	quota0 := HeatQuotaFrom(HeatQuotaInput{
		DailyCap: cfg.Sched.ActiveQuotaDay, Heat: heatV, Provided: hasHeat, CostPerMsg: cfg.Sched.HeatCostPerMsg,
	})
	if quota0 != 0 {
		t.Fatalf("夹具前提不对: 没有热力时配额应当是 0, 实得 %d", quota0)
	}

	// ② 来几轮互动 (每条给 5 分左右, 成本 25 → 需要攒够)。
	for i := 0; i < 30; i++ {
		e.NoteSceneHeat(scene, "111", clock.Now().Add(time.Duration(i)*time.Minute))
	}
	heatV2, hasHeat2 := e.heatNow(clock.Now().Add(30 * time.Minute))
	quota1 := HeatQuotaFrom(HeatQuotaInput{
		DailyCap: cfg.Sched.ActiveQuotaDay, Heat: heatV2, Provided: hasHeat2, CostPerMsg: cfg.Sched.HeatCostPerMsg,
	})
	if quota1 <= 0 {
		t.Fatalf("互动了 30 条, 配额仍然是 %d (热力 %.2f) —— 它还是不会主动开口", quota1, heatV2)
	}

	// ③ 反面: **扣费要真的扣** —— 主动发起一条 = 25 热力。
	before, _, _ := s.LoadHeat(scene)
	e.SpendSceneHeat(scene, clock.Now().Add(31*time.Minute))
	after, _, _ := s.LoadHeat(scene)
	if after.Value >= before.Value {
		t.Fatalf("主动发起之后热力没降: %.2f → %.2f", before.Value, after.Value)
	}
}

// TestNoteSceneHeat_FailurePaths
//
// ⚠️ 零值 = 未提供: 空场景键**不许**写一行"全局热力"进去 ——
// 那会让所有私聊共享一份热力, 而症状是"换个群聊就变哑巴"。
func TestNoteSceneHeat_FailurePaths(t *testing.T) {
	e, s := heatEngine(t)
	e.NoteSceneHeat("", "111", clock.Now())
	e.NoteSceneHeat("   ", "111", clock.Now())
	if _, ok, _ := s.LoadHeat(""); ok {
		t.Fatal("空场景键写出了一行热力")
	}
	// 没有 Store → 不许 panic。
	(&Engine{}).NoteSceneHeat("group:1", "111", clock.Now())
	(&Engine{}).SpendSceneHeat("group:1", clock.Now())
}

// TestHeatStageOf_MissingBondFallsToTheSmallestCap
//
// ⚠️ 读不到关系量 → **stranger**(最小的 cap): 失败往"少说"倒, 不往"放开"倒。
func TestHeatStageOf_MissingBondFallsToTheSmallestCap(t *testing.T) {
	e, _ := heatEngine(t)
	if got := e.heatStageOf("group:never-seen"); got != HeatStageStranger {
		t.Fatalf("没有关系量的场景应当是 stranger, 实得 %q", got)
	}
	caps := HeatConfig{}.Normalized()
	if caps.CapOf(HeatStageStranger) >= caps.CapOf(HeatStageIntimate) {
		t.Fatal("档位表坏了: stranger 的 cap 不该 ≥ intimate")
	}
}
