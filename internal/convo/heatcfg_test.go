package convo

import (
	"testing"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
)

// ─────────────────────────────────────────────────────────────
// ③ §6.7 的旋钮接 convo.yaml
//
// ⚠️ HeatConfig 原来**一个 yaml tag 都没有**, 而它的注释写着
// 「主控 overlay convo.yaml 时以它为底」—— 那句话描述的是一件**没做的事**:
// 这一节只能吃内置默认, 一行都配不了。
// ─────────────────────────────────────────────────────────────

// TestHeatCfg_MissingSectionIsNotAllZero
//
// ⚠️ **零值 = 未提供 ≠ 全部归零。**
// 半衰期为零意味着热力**永不衰减** —— 一条静默的规则失效,
// 而它看起来只是"越聊越粘"。
func TestHeatCfg_MissingSectionIsNotAllZero(t *testing.T) {
	e := &Engine{Cfg: DefaultConfig()} // 完全不写 heat: 段
	n := e.heatCfg()
	if n.HalfLifeH <= 0 {
		t.Fatalf("半衰期成了 %.1f —— 热力永不衰减", n.HalfLifeH)
	}
	if n.CostPerMsg <= 0 || n.CapStranger <= 0 || n.CapFamiliar <= 0 || n.CapIntimate <= 0 {
		t.Fatalf("cap / 代价里有零值: %+v", n)
	}
	if n.GainOpen <= 0 || n.GainFloor <= 0 || n.SessionGapMin <= 0 {
		t.Fatalf("增益里有零值: %+v", n)
	}
	if len(n.GainByState) == 0 {
		t.Fatal("槽位权重表是空的 —— 每次互动都会加 0 热力")
	}
	// 档位表必须是有序的, 否则 cap 那三档形同虚设。
	if !(n.CapStranger < n.CapFamiliar && n.CapFamiliar < n.CapIntimate) {
		t.Fatalf("三档 cap 不是递增的: %.0f / %.0f / %.0f", n.CapStranger, n.CapFamiliar, n.CapIntimate)
	}
}

// TestHeatCfg_YamlOverridesOnlyWhatIsWritten
//
// ⚠️ 覆盖是**逐项**的: 写了一项不许把其它项一起归零。
func TestHeatCfg_YamlOverridesOnlyWhatIsWritten(t *testing.T) {
	cfg := DefaultConfig()
	base := (&Engine{Cfg: cfg}).heatCfg()

	cfg.Heat.CapStranger = 999
	cfg.Heat.HalfLifeH = 12
	got := (&Engine{Cfg: cfg}).heatCfg()
	if got.CapStranger != 999 {
		t.Fatalf("写了 cap_stranger 却没生效: %.0f", got.CapStranger)
	}
	if got.HalfLifeH != 12 {
		t.Fatalf("写了 half_life_h 却没生效: %.1f", got.HalfLifeH)
	}
	// 没写的那些必须还是默认值。
	if got.CapFamiliar != base.CapFamiliar || got.GainOpen != base.GainOpen {
		t.Fatalf("没写的项被顺手改了: familiar %.0f→%.0f, gainOpen %.1f→%.1f",
			base.CapFamiliar, got.CapFamiliar, base.GainOpen, got.GainOpen)
	}
}

// TestHeatCfg_CostHasExactlyOneSource
//
// ⚠️ 每条主动消息的代价只许有**一个**来源: scheduler.heat_cost_per_msg。
// 两个旋钮管同一件事迟早分叉, 而分叉的表现是
// **"配额按 25 算、扣费按 50 扣"** —— 两边的账永远对不上, 且没人会去比这两处。
func TestHeatCfg_CostHasExactlyOneSource(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Heat.CostPerMsg = 50 // 热力段自己写了一个
	cfg.Sched.HeatCostPerMsg = 30
	got := (&Engine{Cfg: cfg}).heatCfg()
	if got.CostPerMsg != 30 {
		t.Fatalf("代价应当由 scheduler.heat_cost_per_msg 说了算 (30), 实得 %.0f", got.CostPerMsg)
	}
	// 而调度器那一侧读的也必须是同一个数 —— 否则配额与扣费会对不上账。
	q := HeatQuotaFrom(HeatQuotaInput{
		DailyCap: cfg.Sched.ActiveQuotaDay, Heat: 100, Provided: true, CostPerMsg: cfg.Sched.HeatCostPerMsg,
	})
	if want := HeatQuota(100, got.CostPerMsg, cfg.Sched.ActiveQuotaDay); q != want {
		t.Fatalf("配额算的代价 (%d) 与扣费的代价对不上", q)
	}
}

// TestHeatCfg_IsActuallyUsedByTheProductionPaths
//
// ⚠️ **接线尺子**: 光测 heatCfg() 的返回值证明不了**真的有人用它** ——
// 而这一节整轮的教训正是"函数写好了、没人调"。
func TestHeatCfg_IsActuallyUsedByTheProductionPaths(t *testing.T) {
	e, s := heatEngine(t)
	e.Cfg.Heat.CapStranger = 500 // 把 stranger 的 cap 抬到买得起一条主动消息
	scene := DefaultSceneKey
	for i := 0; i < 5; i++ {
		e.NoteSceneHeat(scene, "111", clock.Now().Add(time.Duration(i)*time.Minute))
	}
	row, ok, err := s.LoadHeat(scene)
	if err != nil || !ok {
		t.Fatalf("热力行不在: ok=%v err=%v", ok, err)
	}
	// 默认 cap 是 20; 配成 500 之后必须**真的**涨过 20。
	if row.Value <= HeatDefaultCapStranger {
		t.Fatalf("配置里的 cap 没被入账路径用上: value=%.1f (默认 cap %.0f)", row.Value, HeatDefaultCapStranger)
	}
}
