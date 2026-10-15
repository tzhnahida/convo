package convo

import (
	"testing"
)

// ─────────────────────────────────────────────────────────────
// 热力配置的**唯一出口**: `(*Engine).heatCfg`
//
// ⚠️ 这一组用例是**删掉 `HeatCap` / `DefaultHeatConfig` 那一轮补的** ——
// 删它们的理由不是"默认值不重要", 而恰恰是**默认值很重要**:
//
//   · `HeatCap` 写死 `HeatConfig{}` → **忽略 yaml overlay**,
//     谁拿它做读数就会显示一个与真实上限不同的数;
//   · `DefaultHeatConfig` 把兜底放进一个"默认值函数"里,
//     而 config.go 的裁决是**兜底必须在 overlay 之后**。
//
// 所以删完之后必须**反过来钉住两件事**:
//   ① 配置没写 → 内置默认仍然生效(**"没配"不等于"上限为零"**);
//   ② 配置写了 → overlay 赢(而那正是 `HeatCap` 会说谎的方向)。
// ─────────────────────────────────────────────────────────────

// TestHeatCfg_ZeroConfigStillHasDefaults
//
// ⚠️ 零值 = 未提供 ≠ 极值: `Heat` 整段没配时, 它必须落回**内置默认**,
// 而**不是**变成 0(那会让它一条主动消息都发不出, 而现象只是"它变安静了")。
func TestHeatCfg_ZeroConfigStillHasDefaults(t *testing.T) {
	e := &Engine{Cfg: Config{}} // Heat 与 Sched 全零
	n := e.heatCfg()
	if n.CostPerMsg != 25 {
		t.Fatalf("没配时每条主动消息的代价应当是 25, 实得 %.0f", n.CostPerMsg)
	}
	if got := n.CapOf(HeatStageStranger); got != 20 {
		t.Fatalf("没配时陌生人档上限应当是 20, 实得 %.0f", got)
	}
	// ⚠️ 档位必须**单调**(不然"更熟"反而更能攒)。
	if n.CapOf(HeatStageIntimate) <= n.CapOf(HeatStageStranger) {
		t.Fatalf("档位不单调: stranger=%.0f intimate=%.0f",
			n.CapOf(HeatStageStranger), n.CapOf(HeatStageIntimate))
	}
}

// TestHeatCfg_OverlayWins (⚠️ 这一条钉的正是 `HeatCap` 说谎的那个方向)
//
// ⚠️ 删掉 `HeatCap` 的**全部意义**就在这里:
// 它写死 `HeatConfig{}`, 所以哪怕 yaml 把上限配成 999, 它仍然返回内置的那个数。
func TestHeatCfg_OverlayWins(t *testing.T) {
	cfg := Config{}
	cfg.Heat.CapFamiliar = 999
	e := &Engine{Cfg: cfg}
	if got := e.heatCfg().CapOf(HeatStageFamiliar); got != 999 {
		t.Fatalf("overlay 没生效: %.0f —— **那正是被删掉的 HeatCap 会说谎的方向**", got)
	}

	// ⚠️ 而"每条消息的代价"那条覆盖线也必须在 ——
	// 它来自 `Sched` 而不是 `Heat`, 是**两个旋钮**。
	cfg2 := Config{}
	cfg2.Sched.HeatCostPerMsg = 7
	e2 := &Engine{Cfg: cfg2}
	if got := e2.heatCfg().CostPerMsg; got != 7 {
		t.Fatalf("Sched.HeatCostPerMsg 的覆盖没生效: %.0f", got)
	}
}
