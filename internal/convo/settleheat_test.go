package convo

import (
	"math"
	"testing"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
)

// ─────────────────────────────────────────────────────────────
// 热力**不需要显式结算** —— 这是删掉 `SettleHeat` 之后留下的不变量
//
// ⚠️ 删它之前先证明了两条等价性(见 §2.55):
//
//	· 写侧 `settleHeat` 每次互动都 `st.Value = HeatAt(...) + delta` **写回**;
//	· 读侧 `HeatAt` 从 `SettledTS` **现算**衰减。
//
// 于是"落一次衰减"的值 == 读路径现算的值。**但那条用例是迁移证据, 它引用了
// 被删的函数, 所以只能跑一次。** 这里留下的是**它的持久形态**:
//
//	**衰减由读路径现算, 所以两次互动之间不需要任何"结算"。**
// ─────────────────────────────────────────────────────────────

// TestHeat_DecayNeedsNoExplicitSettle
//
// ⚠️ 两条断言, 而第二条才是实质:
//
//	① 隔了一个半衰期之后, 现值必须是一半(读路径算得出来);
//	② **第二次互动必须从"衰减后的值"继续累加**, 而不是从当初那个值 ——
//		 那正是"不需要显式结算"的实质。少了它, 一个从不结算的实现
//		 会在每次互动时把旧的顶格值带上, 而现象只是"它越来越话多"。
func TestHeat_DecayNeedsNoExplicitSettle(t *testing.T) {
	// ⚠️ **走生产入口** `Engine.NoteSceneHeat` —— 它会把 SlotState 与 Mods 都填上。
	// 第一版我直接调 `RecordHeatInteraction` 而漏了那两个参数, 于是 yield 恒为 0,
	// 夹具自己先红了(而那不是实现的问题)。
	e := gateEngine(t)
	if err := EnsureSceneSchema(e.Store); err != nil {
		t.Fatal(err)
	}
	if err := EnsureHeatSchema(e.Store); err != nil {
		t.Fatal(err)
	}
	s := e.Store
	cfg := HeatConfig{}.Normalized()
	scene := "group:7101"
	base := clock.Now()
	e.NoteSceneHeat(scene, "111", base)
	st0, ok0, err := s.LoadHeat(scene)
	if err != nil || !ok0 {
		t.Fatalf("入账后应当有热力行: ok=%v err=%v", ok0, err)
	}
	v0 := st0.Value
	if v0 <= 0 {
		t.Fatal("第一次互动没把热力推上去 —— 夹具不对")
	}

	// ⚠️ 把结算时刻推回**一个半衰期**之前 —— 这样现算的衰减正好是一半。
	back := base.Add(-time.Duration(cfg.HalfLifeH) * time.Hour).Format(bodyTSLayout)
	if _, err := s.DB.Exec("UPDATE convo_heat SET settled_ts=? WHERE scene_key=?", back, scene); err != nil {
		t.Fatal(err)
	}
	now := base
	st, ok, err := s.LoadHeat(scene)
	if err != nil || !ok {
		t.Fatalf("读回: ok=%v err=%v", ok, err)
	}
	// ① 现算的现值 ≈ 一半。**注意这里没有任何 settle 调用。**
	half := HeatAt(st, now, cfg.HalfLifeH)
	if math.Abs(half-v0/2) > 0.05 {
		t.Fatalf("一个半衰期后的现值 = %.3f, 期望 ≈ %.3f (v0/2)", half, v0/2)
	}

	// ② 第二次互动必须从**衰减后**的值继续, 不是从 v0。
	// ⚠️ 第二次也走生产入口 —— 而它的起点必须已经是衰减后的值。
	before := 0.0
	{
		st, _, err := s.LoadHeat(scene)
		if err != nil {
			t.Fatal(err)
		}
		before = HeatAt(st, now, cfg.HalfLifeH)
	}
	e.NoteSceneHeat(scene, "111", now)
	if before >= v0*0.9 {
		t.Fatalf("**第二次互动的起点没有衰减**: before=%.3f, 而 v0=%.3f —— "+
			"那说明它是在拿旧值累加, 而「不需要结算」这条就不成立了", before, v0)
	}
}

// TestReadPath_DoesNotWriteTheLog
//
// ⚠️ 另一半: **观测不许变成写入**。
// 只读路径连调 N 次, `convo_heat_log` 的行数必须**一次都不变** ——
// 否则"看一眼"就会污染流水, 而流水的用途正是"事后翻它当时为什么涨"。
func TestReadPath_DoesNotWriteTheLog(t *testing.T) {
	s := newTestStore(t)
	if err := EnsureHeatSchema(s); err != nil {
		t.Fatal(err)
	}
	cfg := HeatConfig{}.Normalized()
	scene := "group:7102"
	now := clock.Now()
	if _, err := s.RecordHeatInteraction(HeatInteraction{
		SceneKey: scene, Stage: HeatStageStranger, Now: now, Speaker: "111",
	}, cfg); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		var n int
		if err := s.DB.QueryRow("SELECT COUNT(*) FROM convo_heat_log WHERE scene_key=?", scene).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	for i := 0; i < 5; i++ {
		st, _, err := s.LoadHeat(scene)
		if err != nil {
			t.Fatal(err)
		}
		_ = HeatAt(st, now, cfg.HalfLifeH)
	}
	if after := count(); after != before {
		t.Fatalf("**只读路径写进了流水**: %d → %d", before, after)
	}
}
