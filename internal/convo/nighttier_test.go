package convo

import (
	"testing"
	"time"
)

// ——— §4.4.3 记忆四档 ———

// TestDeriveTier_FourTiers 四档各自的判据。
func TestDeriveTier_FourTiers(t *testing.T) {
	cases := []struct {
		m    MemoryDelta
		want string
	}{
		{MemoryDelta{Kind: "habit", Label: "作息", V: "偏晚, 经常一两点睡", Confidence: 0.75}, TierHabit},
		{MemoryDelta{Kind: "relation", Label: "家人", V: "有个弟弟", Confidence: 0.9}, TierLong},
		{MemoryDelta{Kind: "boundary", Label: "边界", V: "不能提前任", Confidence: 0.9}, TierLong},
		{MemoryDelta{Kind: "food", Label: "生日", V: "3 月 12 日", Confidence: 0.9}, TierLong},
		{MemoryDelta{Kind: "chat", Label: "闲聊", V: "今天天气不错", Confidence: 0.4}, TierForget},
		{MemoryDelta{Kind: "food", Label: "吃的", V: "喜欢吃火锅", Confidence: 0.9}, TierShort},
	}
	for _, c := range cases {
		if got := DeriveTier(c.m); got != c.want {
			t.Fatalf("DeriveTier(%s/%s) = %s, 期望 %s", c.m.Kind, c.m.Label, got, c.want)
		}
	}
	// 兜底必须是 short (最安全的中间档), 不是 forget 也不是 long。
	if got := DeriveTier(MemoryDelta{Kind: "unknownkind", Confidence: 0.9}); got != TierShort {
		t.Fatalf("未命中任何判据时应当是 short 兜底, 得到 %s", got)
	}
	// habit **优先**于其他判据 —— 一条"作息偏晚"既像偏好又像习惯,
	// 而它们的落点完全不同 (一个进记忆, 一个写回日程)。
	if got := DeriveTier(MemoryDelta{Kind: "habit", Label: "生日", V: "3 月 12 日", Confidence: 0.9}); got != TierHabit {
		t.Fatalf("habit 必须优先于 long, 得到 %s", got)
	}
}

// TestTier_ForgetAndHabitNeverPersist **本组最要紧的一条**。
//
// 反面: forget 档落库 → 红。"遗忘"在实现上唯一诚实的做法是
// **它必须发生在写入口** —— 写进去再删是两次 IO, 还留痕。
//
// 反面: habit 档落进记忆表 → 红。那会变成"它记得你熬夜, 但作息一动不动",
// 而第四档的全部意义就是"**我的作息因此变了**"。
//
// ⚠️ **它原来测的是 `TierMemory`(已删)**, 而它钉的是**一条性质**, 不是那个函数 ——
// 所以按"删重复实现时先问它的测试钉什么"那条规矩, 它被**搬到了生产路径**上:
// `ApplySessionDelta` 才是真正会跑的那条(§4.4.3 四档的判据在它里面)。
func TestTier_ForgetAndHabitNeverPersist(t *testing.T) {
	s := newTestStore(t)
	err := s.ApplySessionDelta("sess-1", SessionDelta{Memories: []MemoryDelta{
		{MemID: "forget1", K: "chat", V: "随便说说", Label: "闲聊", Kind: "chat", Confidence: 0.4},
		{MemID: "habit1", K: "habit", V: "作息偏晚, 经常一两点睡", Label: "习惯", Kind: "habit", Confidence: 0.75},
		{MemID: "short1", K: "food", V: "喜欢吃火锅", Label: "吃的", Kind: "food", Confidence: 0.9},
	}})
	if err != nil {
		t.Fatalf("ApplySessionDelta: %v", err)
	}
	count := func(id string) int {
		var c int
		if err := s.DB.QueryRow("SELECT COUNT(*) FROM convo_memory WHERE mem_id=?", id).Scan(&c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	if count("forget1") != 0 {
		t.Fatal("forget 档落库了 —— 遗忘必须发生在写入口, 不是写完再删")
	}
	if count("habit1") != 0 {
		t.Fatal("habit 档落进了记忆表 —— 那会变成「记得你熬夜, 但作息一动不动」")
	}
	if count("short1") != 1 {
		t.Fatal("short 档没落库 —— 过滤不能宽到什么都不存")
	}
}

// TestLongMemoryDecay_BoundaryHasFloor **边界类降权而非禁止** (§7.4:1126)。
//
// 反面: 边界类一路掉到 0 → 它会把"别拿 X 开玩笑"忘干净 → 红。
// 但也不能不掉: 永久禁止会让它越骂越畏缩, 那比复读更不像人。
func TestLongMemoryDecay_BoundaryHasFloor(t *testing.T) {
	old := time.Now().AddDate(-2, 0, 0).Format(bodyTSLayout)
	boundary := Memory{Kind: "boundary", UpdatedTS: old}
	w := LongMemoryDecay(boundary, time.Now())
	if w < 0.5 {
		t.Fatalf("边界类衰减跌破地板 0.5 (得到 %.2f) —— 它会把「不能碰」忘干净", w)
	}
	if w >= 1.0 {
		t.Fatalf("边界类完全不衰减 (%.2f) —— 永久禁止会让它越骂越畏缩", w)
	}
	// 普通长期记忆可以掉得更低。
	normal := Memory{Kind: "food", UpdatedTS: old}
	if wn := LongMemoryDecay(normal, time.Now()); wn >= w {
		t.Fatalf("普通记忆应当比边界衰减更多: %.2f vs %.2f", wn, w)
	}
	// 时间戳读不出 → 1.0 (失败往"照常"倒, 不是"忘光了")。
	if got := LongMemoryDecay(Memory{Kind: "food", UpdatedTS: "坏时间"}, time.Now()); got != 1.0 {
		t.Fatalf("时间戳读不出应当是 1.0, 得到 %.2f", got)
	}
}

// TestHabitFromMemories_NeedsEvidence 反面: 一次熬夜就改作息 → 红。
// 那是把噪声当模式。
func TestHabitFromMemories_NeedsEvidence(t *testing.T) {
	now := time.Now()
	one := []Memory{{Kind: "habit", V: "作息偏晚"}}
	if got := HabitFromMemories(one, now); len(got) != 0 {
		t.Fatalf("一次观察就抽出了模式: %+v", got)
	}
	// 反面: **不含深夜关键词的 habit 不算** —— 廉价判据宁可漏判也不误判。
	var offTopic []Memory
	for i := 0; i < HabitMinEvidence+2; i++ {
		offTopic = append(offTopic, Memory{Kind: "habit", V: "爱喝咖啡"})
	}
	if got := HabitFromMemories(offTopic, now); len(got) != 0 {
		t.Fatalf("与作息无关的习惯被当成了深夜模式: %+v", got)
	}
	var many []Memory
	for i := 0; i < HabitMinEvidence; i++ {
		many = append(many, Memory{Kind: "habit", V: "作息偏晚, 经常一两点睡"})
	}
	got := HabitFromMemories(many, now)
	if len(got) != 1 {
		t.Fatalf("够证据却没抽出模式: %+v", got)
	}
	if got[0].SuggestMin <= 0 || got[0].SuggestMin > 30 {
		t.Fatalf("建议位移 %d 超出幅度 (单次 ≤30 分钟, 与 §4.5.3 一致)", got[0].SuggestMin)
	}
}
