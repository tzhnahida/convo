package convo

import (
	"os"
	"path/filepath"
	"testing"
)

// ——— §6.10 群配置接线 ———
//
// 接线测试: 证明 Load 真的把 yaml 折进了 Config.Group。
// 没有它, GroupConfig 就是个**没人验过**的配置结构 —— 而白名单是群聊的硬门槛。

// TestLoad_GroupConfigNormalizedAfterOverlay 反面: 把 Load 里那句
// cfg.Group = NormalizeGroupConfig(cfg.Group) 删掉 → 红。
//
// 为什么它必须放在 **overlay 之后**而不是 DefaultConfig 里:
// 放进 DefaultConfig 的话, yaml 里显式写 0 的项会被"默认值 + yaml 的 0"覆盖成 0 ——
// 于是"配错了"与"没配"长得一模一样。
func TestLoad_GroupConfigNormalizedAfterOverlay(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "convo.yaml")

	// yaml 显式写 0 → 必须被折成默认, 不是留在 0。
	if err := os.WriteFile(p, []byte("group:\n  thread_window_min: 0\n  history_subjects: 0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Group.ThreadWindowMin != DefaultThreadWindowMin {
		t.Fatalf("yaml 里的 0 没被折成默认 (%d) —— 配错了和没配会长得一样", cfg.Group.ThreadWindowMin)
	}
	if cfg.Group.HistorySubjects != DefaultHistorySubjects {
		t.Fatalf("history_subjects 的 0 没被折成默认 (%d)", cfg.Group.HistorySubjects)
	}

	// 反面: **非零值必须被保留** —— 兜底不能宽到"永远用默认, yaml 写了也不算"。
	if err := os.WriteFile(p, []byte("group:\n  thread_window_min: 9\n  history_subjects: 40\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg2, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.Group.ThreadWindowMin != 9 {
		t.Fatalf("yaml 的显式值被默认值盖掉了: %d, 期望 9", cfg2.Group.ThreadWindowMin)
	}
	if cfg2.Group.HistorySubjects != 40 {
		t.Fatalf("yaml 的显式值被默认值盖掉了: %d, 期望 40", cfg2.Group.HistorySubjects)
	}
}

// TestConfig_GroupWhitelist_ThreeGates 白名单是**两道闸门**, 都得过。
//
// ⚠️ 我第一版这条测试写漏了 Enabled —— 结果"白名单里明明有它却被挡了"。
// 那不是 bug, 是**两个闸门**的设计: Enabled 管"群聊功能开没开",
// Whitelist 管"具体接哪个群"。把两者合成一个, 就没法表达"功能开着但一个群都还没加"。
//
// 反面: 删掉 `if !cfg.Enabled { return false }` → 第一种组合会放行 → 红。
func TestConfig_GroupWhitelist_ThreeGates(t *testing.T) {
	// ① 功能没开: 即使白名单里有, 也一律不接。
	off := NormalizeGroupConfig(GroupConfig{Enabled: false, Whitelist: []string{"12345"}})
	if WhitelistAllows(off, "12345") {
		t.Fatal("群聊功能没开却放行了 —— 功能开关是第一道闸门")
	}
	// ② 功能开着但白名单为空: 一个都不接 (fail-closed)。
	empty := NormalizeGroupConfig(GroupConfig{Enabled: true})
	if WhitelistAllows(empty, "12345") {
		t.Fatal("空白名单放行了 —— 门槛必须 fail-closed, 否则被谁拉进群它都会开口")
	}
	// ③ 两道都过: 放行; 名单外仍然挡。
	on := NormalizeGroupConfig(GroupConfig{Enabled: true, Whitelist: []string{"12345"}})
	if !WhitelistAllows(on, "12345") {
		t.Fatal("两道闸门都过了却被挡")
	}
	if WhitelistAllows(on, "99999") {
		t.Fatal("白名单外的群放行了")
	}
	// ④ 私聊不受群白名单约束 —— 拿群门槛去卡私聊等于给旧通路加新门槛。
	if !SceneAllowed(GroupConfig{}, "private:owner") {
		t.Fatal("私聊被群白名单挡住了 —— 它是升级前就有的通路")
	}
}
