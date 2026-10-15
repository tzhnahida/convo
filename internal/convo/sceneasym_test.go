package convo

import (
	"testing"
	"time"

	"github.com/tzhnahida/VocabLock/internal/clock"
)

// ─────────────────────────────────────────────────────────────
// #27 关系之间的不对称
//
// 「在 A 面前**少提** B(不是心虚, 是**读空气**)。」
//
// ⚠️ 是**降权**不是**过滤**: 真人在 A 面前也会提 B, 只是提得少。
// 硬切掉会让它变成一个"每换一个人就失忆一次"的角色 —— 那比说错话更假。
// ─────────────────────────────────────────────────────────────

// TestSceneAffinity_ThreeCasesMustBeDistinct
//
// ⚠️ 三种情况**必须分开**, 尤其是第三种:
// 零值 = 未提供 ≠ "别的场景"。把全局记忆也降权的话,
// 它会在私聊里几乎想不起任何长期的事。
func TestSceneAffinity_ThreeCasesMustBeDistinct(t *testing.T) {
	cur := DefaultSceneKey
	same := sceneAffinity(Memory{SceneKey: cur}, cur)
	other := sceneAffinity(Memory{SceneKey: "group:1034712035"}, cur)
	global := sceneAffinity(Memory{SceneKey: ""}, cur)

	if !(same > global) {
		t.Errorf("当前场景的记忆没有加权: same=%.2f global=%.2f", same, global)
	}
	if !(other < global) {
		t.Errorf("**别的场景**的记忆没有被降权: other=%.2f global=%.2f —— §5.7 就落在这里", other, global)
	}
	if global != SceneAffinityGlobal {
		t.Errorf("没有场景标签的记忆应当是**中性**: %.2f", global)
	}
	// 没有当前场景 (群路径没传 / 老调用方) → 一律中性, **零回归**。
	for _, m := range []Memory{{SceneKey: cur}, {SceneKey: "x"}, {}} {
		if got := sceneAffinity(m, ""); got != SceneAffinityGlobal {
			t.Errorf("不知道当前场景时不该动排序: %+v → %.2f", m, got)
		}
	}
}

// TestSceneAffinity_DoesNotReplaceTheHardGate
//
// ⚠️ §5.7 **不是** §7.3 的替代: 那条是**硬闸**(关系级记忆不许跨场景),
// 这条只是排序。两条同时在场, 谁也不许把谁吃掉。
func TestSceneAffinity_DoesNotReplaceTheHardGate(t *testing.T) {
	now := clock.Now()
	m := Memory{Sensitivity: SensitivityRelation, SceneKey: "group:999"}
	if MemoryUsableIn(m, DefaultSceneKey, now) {
		t.Fatal("§7.3 的硬闸被 §5.7 的降权吃掉了 —— 关系级记忆跨场景必须仍然**不可用**")
	}
	// 而 public 的可以跨场景用 —— 只是会被**降权**。
	p := Memory{Sensitivity: SensitivityPublic, SceneKey: "group:999"}
	if !MemoryUsableIn(p, DefaultSceneKey, now) {
		t.Fatal("public 记忆应当可以跨场景使用")
	}
	if sceneAffinity(p, DefaultSceneKey) >= SceneAffinityGlobal {
		t.Error("public 但属于别的场景 —— 应当降权")
	}
}

// ——— 排序真的会变 (端到端) ———

func putMem(t *testing.T, s *Store, id, v, scene, sens string, conf float64) {
	t.Helper()
	if err := s.UpsertMemory(MemoryDelta{
		MemID: id, K: "fact", V: v, Label: v, Confidence: conf, Source: "user_said",
		SceneKey: scene, Sensitivity: sens,
	}, "active"); err != nil {
		t.Fatal(err)
	}
}

// TestTopMemoriesFor_PrefersTheCurrentScene
//
// ⚠️ **端到端的那条尺子**: 光测 sceneAffinity 是个纯函数, 证明不了排序真的用了它。
func TestTopMemoriesFor_PrefersTheCurrentScene(t *testing.T) {
	s := agTestStore(t)
	// 两条**同置信度**的记忆, 只有场景不同 → 当前场景那条必须排前面。
	//
	// ⚠️ 内容必须**不一样**: UpsertMemory 会按内容去重, 写两条一样的只会剩一条
	// (第一版就是这么写的, 于是用例报"应当取到 2 条, 实得 1" —— 那是夹具的错)。
	putMem(t, s, "m-here", "我们上次聊到的那个书店(私聊)", DefaultSceneKey, SensitivityPublic, 0.8)
	putMem(t, s, "m-there", "我们上次聊到的那个书店(群里)", "group:1034712035", SensitivityPublic, 0.8)

	got, err := s.TopMemoriesFor(DefaultSceneKey, 10, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 {
		t.Fatalf("应当取到 2 条, 实得 %d", len(got))
	}
	if got[0].MemID != "m-here" {
		t.Fatalf("当前场景的记忆没有排在前面: %s", got[0].MemID)
	}
	// ⚠️ 而**别的场景那条仍然在**(降权 ≠ 过滤)。
	found := false
	for _, m := range got {
		if m.MemID == "m-there" {
			found = true
		}
	}
	if !found {
		t.Fatal("别的场景的记忆被**切掉**了 —— 那是过滤, 不是降权")
	}
}

// TestTopMemoriesFor_PrivateSceneSeesItsOwnMemories
//
// ⚠️ **这条是这一项的立案 bug。**
// 私聊取记忆原来走的是全局口径(sceneKey=""), 而 §7.3 的规则是"关系级记忆绑场景" ——
// 空场景意味着**带场景标签的记忆一条都取不到**。
// 而私聊的消息本来就带着 DefaultSceneKey 入库, 所以那些记忆是**存在但取不到**。
func TestTopMemoriesFor_PrivateSceneSeesItsOwnMemories(t *testing.T) {
	s := agTestStore(t)
	putMem(t, s, "m-dm", "ta 说过想去看海", DefaultSceneKey, SensitivityRelation, 0.8)

	// 旧口径 (空场景): 取不到 —— 这就是那个 bug。
	old, err := s.TopMemoriesFor("", 10, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range old {
		if m.MemID == "m-dm" {
			t.Fatal("夹具坏了: 空场景不该能取到关系级记忆")
		}
	}
	// 新口径 (私聊场景键): 取得到。
	got, err := s.TopMemoriesFor(DefaultSceneKey, 10, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range got {
		if m.MemID == "m-dm" {
			found = true
		}
	}
	if !found {
		t.Fatal("私聊带着自己的场景键, 却仍然取不到自己的记忆")
	}
}

// TestTouchAndTopMemoriesFor_CacheIsKeyedByScene
//
// ⚠️ 冻结窗口的缓存键原来只有**时间**, §5.7 之后不同场景取到的**不是同一批** ——
// 只按时间做键的话, 群里那批会被私聊直接复用, 而那是**跨人转述的读侧入口**。
func TestTouchAndTopMemoriesFor_CacheIsKeyedByScene(t *testing.T) {
	s := agTestStore(t)
	putMem(t, s, "m-dm", "私聊里的事", DefaultSceneKey, SensitivityPublic, 0.9)
	putMem(t, s, "m-grp", "群里的事", "group:1034712035", SensitivityPublic, 0.9)

	dm, err := s.TouchAndTopMemoriesFor(DefaultSceneKey, 5)
	if err != nil {
		t.Fatal(err)
	}
	grp, err := s.TouchAndTopMemoriesFor("group:1034712035", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(dm) == 0 || len(grp) == 0 {
		t.Fatalf("夹具没取到东西 (%d/%d)", len(dm), len(grp))
	}
	// 两个场景的头名必须是各自的 —— 缓存串了的话这里会相等。
	if dm[0].MemID == grp[0].MemID {
		t.Fatalf("两个场景取到了同一批(缓存没按场景做键): %s", dm[0].MemID)
	}
	// 同一个场景再取一次 → 命中缓存, 逐字节一致。
	again, _ := s.TouchAndTopMemoriesFor(DefaultSceneKey, 5)
	if len(again) != len(dm) || again[0].MemID != dm[0].MemID {
		t.Fatal("同一个场景第二次取到的东西不一样 —— 前缀缓存会每轮断")
	}
}

var _ = time.Now
