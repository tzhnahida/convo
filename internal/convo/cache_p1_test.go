package convo

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tzhnahida/VocabLock/internal/clock"
)

// ——— P1 缓存债的契约回归 ———
//
// 这一组测的不是"函数返回值"而是**提示词前缀的稳定性** —— 它是钱:
// [实测] 前缀稳定度 14% 时 API 侧缓存命中 8%, 稳定度 52% 时输入费按 1/25 计。
// 所以每个正面断言都配一条反面用例, 防止"把功能冻死"也算通过。

func memIDs(ms []Memory) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.MemID)
	}
	return out
}

func hasID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// withFrozenClock 把注入时钟钉在 base, 并在测试内随意推进 (时间旅行)。
func withFrozenClock(t *testing.T, base time.Time) func(time.Time) {
	t.Helper()
	now := base
	old := clock.Now
	clock.Now = func() time.Time { return now }
	t.Cleanup(func() { clock.Now = old })
	return func(v time.Time) { now = v }
}

func seedSixMemories(t *testing.T, store *Store) {
	t.Helper()
	for _, id := range []string{"m_a", "m_b", "m_c", "m_d", "m_e", "m_f"} {
		if err := store.UpsertMemory(MemoryDelta{
			// kind 各不相同: TopMemories 的多样性配额是"同 kind 最多 2 条", 同 kind 会被截断
			MemID: id, K: id, V: "v-" + id, Label: "L", Kind: id,
			Confidence: 0.9, Source: "user_said",
		}, "active"); err != nil {
			t.Fatal(err)
		}
	}
}

// 同分记忆必须有确定性全序 —— 否则同一份数据两次组装会得到不同的记忆段。
func TestTopMemoriesDeterministicOrder(t *testing.T) {
	store, err := OpenStore(t.TempDir(), "test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	base := time.Date(2026, 5, 1, 10, 0, 0, 0, time.Local)
	setNow := withFrozenClock(t, base)

	// 三条**同分** (同 confidence、同从未访问/注入), 只有 mem_id 字典序不同;
	// kind 各不相同以免触发"同 kind 最多 2 条"的多样性配额。
	for _, m := range []MemoryDelta{
		{MemID: "ccc", K: "food", Kind: "food", V: "v", Label: "L", Confidence: 0.9, Source: "user_said"},
		{MemID: "aaa", K: "pet", Kind: "pet", V: "v", Label: "L", Confidence: 0.9, Source: "user_said"},
		{MemID: "bbb", K: "work", Kind: "work", V: "v", Label: "L", Confidence: 0.9, Source: "user_said"},
	} {
		if err := store.UpsertMemory(m, "active"); err != nil {
			t.Fatal(err)
		}
	}
	setNow(base)

	want := []string{"aaa", "bbb", "ccc"}
	for i := 0; i < 3; i++ {
		got, err := store.TopMemories(10, base)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(memIDs(got), want) {
			t.Fatalf("第 %d 次: 同分记忆顺序应为 %v (mem_id 升序), 实得 %v", i+1, want, memIDs(got))
		}
	}
}

// 正面: 同一冻结窗口内反复取, 必须拿到**同一批、同一顺序** (前缀缓存的全部依据)。
// 反面: 跨窗口必须真的轮换 —— 否则"检索诱导遗忘"被冻死了, 道具依赖会回来。
func TestMemoryBatchFreeze(t *testing.T) {
	store, err := OpenStore(t.TempDir(), "test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	base := time.Date(2026, 5, 1, 10, 0, 0, 0, time.Local)
	setNow := withFrozenClock(t, base)
	seedSixMemories(t, store)

	setNow(base.Add(1 * time.Minute))
	batch1, err := store.TouchAndTopMemoriesFor("", 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch1) != 4 {
		t.Fatalf("应取满 4 条, 实得 %d", len(batch1))
	}

	// 同一窗口 (10:00-10:30) 里推进 20 分钟 —— 批次必须不变
	setNow(base.Add(20 * time.Minute))
	batch2, err := store.TouchAndTopMemoriesFor("", 4)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(memIDs(batch1), memIDs(batch2)) {
		t.Fatalf("冻结窗口内批次不得变化: %v → %v", memIDs(batch1), memIDs(batch2))
	}

	// 跨窗口 (10:40) —— 上一批已被 MarkInjected 降温, 必须换人
	setNow(base.Add(40 * time.Minute))
	batch3, err := store.TouchAndTopMemoriesFor("", 4)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(memIDs(batch1), memIDs(batch3)) {
		t.Fatalf("跨窗口应当轮换 (检索诱导遗忘失效, 道具依赖会回来): %v", memIDs(batch3))
	}
}

// 冻结不能变成"刚记住的事要等 30 分钟才进提示词" —— 写入必须立刻可见。
func TestMemoryBatchInvalidatedOnWrite(t *testing.T) {
	store, err := OpenStore(t.TempDir(), "test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	base := time.Date(2026, 5, 1, 10, 0, 0, 0, time.Local)
	setNow := withFrozenClock(t, base)
	seedSixMemories(t, store)

	setNow(base.Add(1 * time.Minute))
	if _, err := store.TouchAndTopMemoriesFor("", 4); err != nil {
		t.Fatal(err)
	}

	store.memMu.Lock()
	frozen := store.memBatch
	store.memMu.Unlock()
	if frozen == nil {
		t.Fatal("前置条件不成立: 应当已有冻结批次")
	}

	// ⚠️ 这条只能**白盒**断言: memoryDecay("")=0.8 让任何新记忆天生比被访问过的旧记忆低 20%
	// (0.99×0.8 = 0.792 < 0.9×1.0), 所以"新记忆顶掉旧记忆"在黑盒上观察不到。
	// 断言机制本身: 写入必须清空冻结批次 —— 否则刚说"记住这个"要等 30 分钟才进提示词。
	if err := store.UpsertMemory(MemoryDelta{
		MemID: "m_zz", K: "zz", V: "刚说的", Label: "新", Kind: "zz",
		Confidence: 0.99, Source: "user_said",
	}, "active"); err != nil {
		t.Fatal(err)
	}
	store.memMu.Lock()
	frozen = store.memBatch
	store.memMu.Unlock()
	if frozen != nil {
		t.Fatalf("写入后冻结批次未清 —— 新记忆要等窗口到期才可见: %v", memIDs(frozen))
	}
}

// §8.1 段序契约: 记忆/历史/话题全变时, 稳定段仍必须逐字节相同。
// 这条一旦破, 缓存命中会静默掉回个位数 —— 是本批最该盯住的不变式。
func TestBuildSystemStablePrefixFirst(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{
		Name: "初音", Description: "一个有点毒舌的朋友", Personality: "外冷内热",
		Catchphrase: "嘛", SentenceLen: "偏短", ToneWords: "嗯/诶",
	}
	a := DialogInput{
		Memories: []Memory{{MemID: "alpha", V: "喜欢火锅", Label: "食物", Source: "user_said"}},
		History:  "你: 今天好累\n她: 累就睡\n",
	}
	b := DialogInput{
		Memories: []Memory{{MemID: "beta", V: "养了只猫", Label: "宠物", Source: "user_said"}},
		History:  "你: 在干嘛\n她: 躺着刷视频\n",
	}
	sa := d.buildSystem(a, p, "ja")
	sb := d.buildSystem(b, p, "ja")
	if sa == sb {
		t.Fatal("用例无效: 两个输入应当产生不同的提示词")
	}
	n := 0
	for n < len(sa) && n < len(sb) && sa[n] == sb[n] {
		n++
	}
	ratio := float64(n) / float64(len(sa))
	if ratio < 0.30 {
		t.Fatalf("稳定前缀只占 %.0f%% (%d/%d 字节) —— 段序被改回「变化段前置」了?", ratio*100, n, len(sa))
	}
	// 反面: 变化段必须真的在后面 —— 稳定前缀里不许出现本轮记忆
	if strings.Contains(sa[:n], "alpha") || strings.Contains(sa[:n], "beta") {
		t.Fatalf("记忆段落进了稳定前缀 (说明它被排到了前面): %q", sa[:n])
	}
}

// 🔴 这条是**变异测试逼出来的**, 补的是一个真实缺口。
//
// `TestMemoryBatchFreeze` 的窗口内断言靠的是**缓存**(memBatch 直接返回), 根本没走到
// `injectPenalty` 的窗口豁免。而那个豁免只在**窗口内发生重算**时才起作用 —— 也就是
// 记忆被写入、缓存被清掉之后。把豁免删掉, 原来那组测试**全绿**(实测)。
//
// 契约: **同窗口内的重算必须得到同一批**。没有它, 任何一次记忆写入都会把整批换成
// 不相交的另一批 —— 提示词从记忆段起再次断裂, 而那正是 P1 花了一整批修掉的东西。
func TestMemoryBatch_SameWindowRecomputeIsIdempotent(t *testing.T) {
	store, err := OpenStore(t.TempDir(), "test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	base := time.Date(2026, 5, 1, 10, 0, 0, 0, time.Local)
	setNow := withFrozenClock(t, base)
	seedSixMemories(t, store)

	setNow(base.Add(1 * time.Minute))
	batch1, err := store.TouchAndTopMemoriesFor("", 4)
	if err != nil {
		t.Fatal(err)
	}

	// 模拟"窗口内写入了一条记忆" —— 缓存被清掉, 下一次取数会真的重算。
	store.InvalidateMemoryBatch()

	setNow(base.Add(2 * time.Minute)) // 仍在同一冻结窗口 (10:00-10:30)
	batch2, err := store.TouchAndTopMemoriesFor("", 4)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(memIDs(batch1), memIDs(batch2)) {
		t.Fatalf("同窗口内重算必须得到同一批 (否则任何一次记忆写入都会打断缓存前缀): %v → %v",
			memIDs(batch1), memIDs(batch2))
	}
}
