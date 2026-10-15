package convo

import (
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────
// **配置读了没生效** —— `no_input_max_per_day` 这个旋钮
//
// ⚠️ `config.go` 里那个字段与它的默认值都在 ✓, 而 `DecideTick` 里写的是**字面量 1** ✗
// ⇒ **那个 yaml 旋钮什么都不做** ✗ —— owner 把它设成 3, 行为还是 1 ✓。
//
// ⚠️ 而这一类比"没接线"更难发现:
//
// · 没接线 —— 那条机制整个不动 ✓ (容易注意到);
// · **配了不生效** —— 机制**照常跑**, 只是**不按你配的数** ✓ (看起来一切正常 ✗)。
// ─────────────────────────────────────────────────────────────

// TestNoInputQuota_ZeroMeansDefault (零值纪律)
//
// ⚠️ **≤0 → 内置 1** ✓ —— "没配"不等于"配成 0" ✓。
func TestNoInputQuota_ZeroMeansDefault(t *testing.T) {
	for _, bad := range []int{0, -1, -100} {
		if got := NoInputQuota(bad); got != 1 {
			t.Fatalf("NoInputQuota(%d) = %d, 期望内置默认 1 —— 否则漏写一行配置就关掉了这条机制", bad, got)
		}
	}
	if got := NoInputQuota(3); got != 3 {
		t.Fatalf("配了 3 却得到 %d —— 那这个旋钮还是不起作用", got)
	}
}

// TestNoInputQuota_ReachesDecideTick (接线尺子)
//
// ⚠️ 光有那个函数不算数 —— **判据必须真的用它** ✓。
func TestNoInputQuota_ReachesDecideTick(t *testing.T) {
	base := TickInput{
		Now: time.Now(), Sched: &WeekSchedule{}, State: StateFree,
		SilentDays: 2, NoInputToday: 2, HadSlot: true,
	}

	// ① 默认 (没配) ⇒ 上限 1 ⇒ 今天已经 2 条 ⇒ **不再发**。
	if a := DecideTick(base, SchedulerConfig{}); a.Kind == ActNoInput {
		t.Fatal("**没配时上限不是 1** —— 那哨兵没生效")
	}

	// ② 配成 3 ⇒ 上限 3 ⇒ 2 < 3 ⇒ **要发**。
	if a := DecideTick(base, SchedulerConfig{NoInputMaxDay: 3}); a.Kind != ActNoInput {
		t.Fatalf("**配了 3 还是不发** (得到 %s) —— 那这个旋钮没接到判据上", a.Kind)
	}
}
