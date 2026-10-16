package convo

import (
	"testing"

	"github.com/tzhnahida/convo/internal/clock"
)

// ─────────────────────────────────────────────────────────────
// §7.4 「边界类记忆**降权而非永久禁止**」
//
// 设计稿 L1580: 「**衰减**: 久不用的记忆降权, 但**边界类记忆降权而非永久禁止**
// (否则越骂越畏缩)」; §4.4.3 L828 长期档同一条: 「永久, 但边界类降权而非禁止」。
//
// ⚠️ 而 `LongMemoryDecay` 里**已经写着**这条保底 (`w < 0.5 → w = 0.5`),
// 它只是**从没被调过** —— 注入打分走的是 `memoryDecay`, 对所有 kind 一视同仁,
// 90 天以上一律 0.3。所以"别拿 X 开玩笑"和"喜欢吃火锅"老了一样重,
// 设计稿要的那个区别**根本不存在**。
// ─────────────────────────────────────────────────────────────

// oldMem 落一条"两年前就不再用过"的记忆。
//
// ⚠️ 时间列只能直接改库: `MemoryDelta` 刻意不给时间字段 ——
// 那是**系统事实**(它什么时候被用过), 不是调用方的输入。
func oldMem(t *testing.T, s *Store, id, kind string) {
	t.Helper()
	if err := s.UpsertMemory(MemoryDelta{
		MemID: id, K: "fact", V: "内容 " + id, Label: id, Kind: kind,
		Confidence: 0.9, Source: "user_said", Sensitivity: SensitivityPublic,
	}, "active"); err != nil {
		t.Fatal(err)
	}
	old := clock.Now().AddDate(-2, 0, 0).Format(bodyTSLayout)
	if _, err := s.DB.Exec(
		"UPDATE convo_memory SET last_access_ts=?, updated_ts=? WHERE mem_id=?", old, old, id); err != nil {
		t.Fatal(err)
	}
}

// TestTopMemoriesFor_BoundaryDecaysButIsNotBanned
//
// ⚠️ 两条断言，一正一反 —— 而**反面那条比正面重要**:
//
//	正:  同为两年前的记忆, 边界那条必须排在普通那条**前面**;
//	反:  边界那条**不许被过滤掉** —— 降权不是禁止。
//
// ⚠️ mem_id 刻意让普通那条**字典序在前**(a_food / z_boundary):
// 今天两者同分(都 0.3), 破平规则是 mem_id 升序, 所以"边界排在前面"
// **只有在地板真的生效时才成立** —— 这条用例今天就是红的。
func TestTopMemoriesFor_BoundaryDecaysButIsNotBanned(t *testing.T) {
	s := newTestStore(t)
	oldMem(t, s, "a_food", "food")
	oldMem(t, s, "z_boundary", "boundary")

	mems, err := s.TopMemoriesFor(DefaultSceneKey, 10, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	idx := map[string]int{}
	for i, m := range mems {
		idx[m.MemID] = i
	}
	// 反: 两条都还在 (降权 ≠ 禁止)。
	if _, ok := idx["a_food"]; !ok {
		t.Fatal("普通那条被过滤掉了 —— 那是对全部记忆的衰减, 不是边界保底")
	}
	if _, ok := idx["z_boundary"]; !ok {
		t.Fatal("**边界那条被过滤掉了 —— 设计稿说得很清楚: 降权而非永久禁止**")
	}
	// 正: 边界排在前面。
	if idx["z_boundary"] > idx["a_food"] {
		t.Fatalf("边界记忆排在普通记忆**后面** —— 那说明那条保底没生效 "+
			"(两者今天同为 0.3, 破平按 mem_id 升序, 而 a_food 在前); 实得 %v", mems)
	}
}

// TestTopMemoriesFor_FreshMemoriesAreUnchanged
//
// ⚠️ 正面之外的反面: 这条改动**不许**抬高任何一条记忆的权重。
// 刚写过的记忆本来就是满权, 加了地板之后必须**还是一样**。
func TestTopMemoriesFor_FreshMemoriesAreUnchanged(t *testing.T) {
	s := newTestStore(t)
	for _, id := range []string{"a_food", "z_boundary"} {
		kind := "food"
		if id == "z_boundary" {
			kind = "boundary"
		}
		if err := s.UpsertMemory(MemoryDelta{
			MemID: id, K: "fact", V: "内容 " + id, Label: id, Kind: kind,
			Confidence: 0.9, Source: "user_said", Sensitivity: SensitivityPublic,
		}, "active"); err != nil {
			t.Fatal(err)
		}
	}
	mems, err := s.TopMemoriesFor(DefaultSceneKey, 10, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(mems) != 2 {
		t.Fatalf("新记忆应当是 2 条, 实得 %d", len(mems))
	}
	// ⚠️ 都是满权、同置信 → 破平按 mem_id 升序 → a_food 必须在前。
	// 这条钉的是"地板**不抬高**任何东西": 若实现里写成"边界一律抬到某个高值",
	// 这里就会红。
	if mems[0].MemID != "a_food" {
		t.Fatalf("新记忆的排序被改动了: 实得 %s 在前", mems[0].MemID)
	}
}
