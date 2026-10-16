package convo

import "testing"

// ——— 群消息裁决链 (§6.10 资格闸门 + §6.3 价值判断) ———

func gateEngine(t *testing.T) *Engine {
	t.Helper()
	e := &Engine{Store: newTestStore(t), Cfg: DefaultConfig()}
	// ⚠️ **把骰子钉死** —— 插话闸门接进 undecidable 之后, 它成了群裁决里唯一的随机源。
	// 不钉的话, "今天天气不错"这类用例会**随机**变成 speak,
	// 而现象是"测试偶尔红" —— 那比一直红更难查。
	//
	// 99 = 永不命中随机通道; 话题通道在本 helper 的库里也没有素材 (没灵光/没学过的),
	// 所以基线行为就是"判不出 → 沉默"。要测插话的用例自己设 ChimeRollFn。
	e.ChimeRollFn = func(n int) int { return 99 }
	// ⚠️ 心情也要钉住 —— 与骰子同一个理由, 而且这次是真的踩过:
	// 「无来由的低意愿日」按**日期**确定性抽取 (约 15%), 抽中的那天里
	// 「被 @ 必答」的断言会必红。2026-10-17 00:06 跨过午夜时套件就红了那两条。
	e.DayMoodFn = func(string) DayMood { return DayMoodFlat }
	return e
}

func gateCfg() GroupConfig {
	return NormalizeGroupConfig(GroupConfig{Enabled: true, Whitelist: []string{"12345"},
		SelfAliases: []string{"小夜"}})
}

// TestGateGroupMessage_AtSelfShortCircuits **V1** —— 硬门槛「被 @ 必答」。
//
// 反面: 让价值判断**先于**资格闸门跑 → 被 @ 但五条全不命中 → 判沉默 → 红。
// 这是整条链最要紧的一条顺序契约 (设计稿 L906 硬门槛在 L907 价值判断**之上**)。
func TestGateGroupMessage_AtSelfSpeaks(t *testing.T) {
	// ⚠️ **契约在 2026-10-14 变回来过一次, 这条测试记录的是最终形态。**
	//
	// 中间那一版是「被 @ 必接」(只发个表情) —— 它按"真人被 @ 也会嗯一句"设计,
	// **生产实测被否决**: 群里 @ 它, 看到的是"它不理我"。
	//
	// 复盘: 「客服味」不在于**答不答**, 在于**答得像不像人** —— 那由长度和语气决定。
	// 拿"少回一句"去换真实感, 换来的是"它死了"。
	e := gateEngine(t)
	self := SelfIdentity{UIN: "777", Nickname: "小夜"}
	cfg := gateCfg()
	got := e.GateGroupMessage(SceneMsg{SceneKey: "group:12345", UIN: "999", Text: "在吗",
		AtUINs: []string{"777"}}, self, cfg)
	if got.Stance != StanceSpeak {
		t.Fatalf("被 @ 必须开口, got %s (%s)", got.Stance, got.Reason)
	}
	// 危机仍然短路 (与"必答"同一条路)。
	crisis := e.GateGroupMessage(SceneMsg{SceneKey: "group:12345", UIN: "999",
		Text: "救命 我不想活了", AtUINs: []string{"777"}}, self, cfg)
	if crisis.Stance != StanceSpeak {
		t.Fatal("危机被 @ 必须开口")
	}
	// @别人 仍然连表情都不给。
	others := e.GateGroupMessage(SceneMsg{SceneKey: "group:12345", UIN: "999",
		Text: "你看看这个", AtUINs: []string{"888"}}, self, cfg)
	if others.Stance != StanceSilent {
		t.Fatalf("@ 别人却接了: %s", others.Stance)
	}
}

func TestGateGroupMessage_AtOthersNeverEvenReacts(t *testing.T) {
	e := gateEngine(t)
	sc := SceneMsg{SceneKey: "group:12345", UIN: "999", Text: "你看看这个",
		AtUINs: []string{"888"}} // @ 的是别人
	self := SelfIdentity{UIN: "777", Nickname: "小夜"}
	got := e.GateGroupMessage(sc, self, gateCfg())
	if got.Stance != StanceSilent {
		t.Fatalf("@ 别人却接了 (%s) —— 表情档会变成绕过 §6.10.2② 的后门", got.Stance)
	}
}

// TestGateGroupMessage_WhitelistFirst **V15** —— 白名单是**第一道门**, 在任何 IO 之前。
func TestGateGroupMessage_WhitelistFirst(t *testing.T) {
	e := gateEngine(t)
	sc := SceneMsg{SceneKey: "group:99999", UIN: "999", Text: "在吗", AtUINs: []string{"777"}}
	self := SelfIdentity{UIN: "777"}
	got := e.GateGroupMessage(sc, self, gateCfg())
	if got.Stance != StanceSilent {
		t.Fatal("白名单外的群却接了 —— 门槛必须 fail-closed")
	}
	// 反面: 即使是 @我, 白名单外也不许接 (门槛在资格闸门之前)。
	if got.Addressed {
		t.Fatal("白名单外的消息不该走到资格闸门")
	}
}

// TestGateGroupMessage_FailureSpeaksNotSilent **V5 —— 本清单里最容易写错的一条**。
//
// 用"判不出来 → 沉默"顺手把**读库失败**也吞掉, 就是纪律的反面:
// 一次读库失败会让它在群里彻底闭嘴, 而没人能从表现上看出那是故障。
//
// 反面: 把 *_speak 的理由并进"判不出来 → 沉默" → 红。
func TestGateGroupMessage_FailureSpeaksNotSilent(t *testing.T) {
	e := gateEngine(t)
	// scene_key 为空 —— ResolveAddressee 的契约是"未提供 → 按故障往说话倒"。
	sc := SceneMsg{SceneKey: "", UIN: "999", Text: "随便说说"}
	self := SelfIdentity{UIN: "777"}
	got := e.GateGroupMessage(sc, self, gateCfg())
	if got.Stance != StanceSpeak {
		t.Fatalf("系统故障被当成了沉默 (%s: %s) —— 故障要往说话倒", got.Stance, got.Reason)
	}
}

// TestGateGroupMessage_ThreeStances 三态在真路径上分得开。
func TestGateGroupMessage_ThreeStances(t *testing.T) {
	e := gateEngine(t)
	self := SelfIdentity{UIN: "777", Nickname: "小夜"}
	cfg := gateCfg()
	// 被点名 (无 @) + 无梗 → 1 条命中 → 表情档 (不是沉默, 也不是发言)。
	named := SceneMsg{SceneKey: "group:12345", UIN: "999", Text: "小夜 你觉得呢"}
	if got := e.GateGroupMessage(named, self, cfg); got.Stance != StanceReact {
		t.Fatalf("被点名 + 无梗应当是表情档, 得到 %s (%s)", got.Stance, got.Reason)
	}
	// 完全不指向它 → 0 条 → 沉默。
	plain := SceneMsg{SceneKey: "group:12345", UIN: "999", Text: "今天天气不错"}
	if got := e.GateGroupMessage(plain, self, cfg); got.Stance != StanceSilent {
		t.Fatalf("判据全不命中却没有沉默: %s (%s)", got.Stance, got.Reason)
	}
}
