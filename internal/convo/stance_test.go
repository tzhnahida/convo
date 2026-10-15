package convo

import "testing"

// ——— §12 三态计数 ———

// TestGateGroupMessage_CountsAllThreeStances **L910「让'不说'可解释、可回归」**。
//
// 反面: 只在"说话"时埋点 → 沉默永远不被计数 → 沉默率永远看着是 100% → 红。
// 沉默是**设计内的一态**, 不是"没有发生的事"; 不计数就等于它不存在,
// 于是"它九成判否"这句设计前提永远没法验证。
func TestGateGroupMessage_CountsAllThreeStances(t *testing.T) {
	ResetFrameStats()
	t.Cleanup(ResetFrameStats)
	e := gateEngine(t)
	self := SelfIdentity{UIN: "777", Nickname: "小夜"}
	cfg := gateCfg()

	// 三条, 分别落到三态。
	// ⚠️ 契约已变: 单纯 @我 只到「接一下」(react)。要拿到 speak, 用**危机短路**那条路径。
	e.GateGroupMessage(SceneMsg{SceneKey: "group:12345", UIN: "999", Text: "救命 我不想活了",
		AtUINs: []string{"777"}}, self, cfg) // Speak (@我 + 危机, 硬门槛仍短路)
	e.GateGroupMessage(SceneMsg{SceneKey: "group:12345", UIN: "999",
		Text: "小夜 你觉得呢"}, self, cfg) // React (被点名, 1 条命中)
	e.GateGroupMessage(SceneMsg{SceneKey: "group:12345", UIN: "999",
		Text: "今天天气不错"}, self, cfg) // Silent (0 条命中)

	fs := FrameStatsSnapshot()
	if fs.StanceSpeak != 1 || fs.StanceReact != 1 || fs.StanceSilent != 1 {
		t.Fatalf("三态计数不对: silent=%d react=%d speak=%d",
			fs.StanceSilent, fs.StanceReact, fs.StanceSpeak)
	}
	// 沉默率必须能算出来 —— 它才是"九成判否"这个设计前提的读数。
	if r := fs.SilentRate(); r <= 0 || r >= 1 {
		t.Fatalf("沉默率应当在 (0,1) 之间, 得到 %v", r)
	}
}

// TestSilentRate_NoSamplesIsZero 反面: 没有样本时返回 100% → 红。
//
// 那会让「引擎还没接到群事件」看起来像「它一句话都不说」——
// 一个把"没数据"读成"极差"的零值 bug (纪律: 零值 = 未提供 ≠ 极值)。
func TestSilentRate_NoSamplesIsZero(t *testing.T) {
	var z FrameStats
	if got := z.SilentRate(); got != 0 {
		t.Fatalf("无样本时沉默率应当是 0 (未提供 ≠ 100%%), 得到 %v", got)
	}
	// 全是沉默 → 1.0 (这才叫"九成判否")。
	all := FrameStats{StanceSilent: 9, StanceReact: 1}
	if got := all.SilentRate(); got != 0.9 {
		t.Fatalf("9/10 沉默应当是 0.9, 得到 %v", got)
	}
}
