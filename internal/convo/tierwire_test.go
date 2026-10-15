package convo

import "testing"

// TestApplySessionDelta_TiersGateTheWritePath **接线**: 四档真的挡在写入口上。
//
// 反面: 删掉 ApplySessionDelta 里那段四档 switch → forget/habit 会落库 → 红。
//
// ⚠️ 这条测的不是 DeriveTier 判得对不对 (那有单测), 而是**它有没有被调用**。
// BodyLine 当了一整轮死代码的教训: 单测全绿与"接上了"是两回事。
func TestApplySessionDelta_TiersGateTheWritePath(t *testing.T) {
	s := newTestStore(t)
	ResetNightTierStats()
	t.Cleanup(ResetNightTierStats)

	err := s.ApplySessionDelta("sess-tier", SessionDelta{
		Summary: "今天聊了聊",
		Memories: []MemoryDelta{
			{MemID: "t_forget", K: "chat", V: "随便说说", Label: "闲聊", Kind: "chat", Confidence: 0.4},
			{MemID: "t_habit", K: "habit", V: "作息偏晚", Label: "习惯", Kind: "habit", Confidence: 0.9},
			{MemID: "t_short", K: "food", V: "喜欢吃火锅", Label: "吃的", Kind: "food", Confidence: 0.9},
		},
	})
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
	if count("t_forget") != 0 {
		t.Fatal("forget 档从会话提取里漏进了记忆表 —— 写入口没挡")
	}
	if count("t_habit") != 0 {
		t.Fatal("habit 档漏进了记忆表 —— 它该去改日程, 不是被记住")
	}
	if count("t_short") != 1 {
		t.Fatal("short 档被误杀了 —— 分档不能宽到什么都不存")
	}
	// 可观测: 丢了两条必须**看得见** —— 否则"它记不住事"和"它今天没什么可记的"长得一样。
	st := NightTierStatsSnapshot()
	if st[TierForget] != 1 || st[TierHabit] != 1 {
		t.Fatalf("丢弃计数不对 (看不见的遗忘 = 没法调): %v", st)
	}
}
