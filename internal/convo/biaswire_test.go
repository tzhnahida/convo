package convo

import (
	"strings"
	"testing"
)

// ⚠️ **这一批的全部意义在这条测试上**:
// `ValueWithBias` 曾经只在 bias_test.go 里被调用 —— 单元测试全绿, 而机制从未生效。
// 所以这里测的不是 Δ 算得对不对 (那有 bias_test.go), 而是**它到底有没有到达裁决**。
func TestGateGroupMessage_BiasIsOnTheDecisionPath(t *testing.T) {
	e := gateEngine(t)
	self := SelfIdentity{UIN: "777", Nickname: "小夜"}
	cfg := gateCfg()
	msg := SceneMsg{SceneKey: "group:12345", UIN: "999", Text: "小夜 你觉得呢"}

	base := e.GateGroupMessage(msg, self, cfg)
	if base.Stance != StanceReact {
		t.Fatalf("基线应当是「够表情不够开口」, got %s (%s)", base.Stance, base.Reason)
	}

	// 建立一段有正面证据的关系 (§5.1)。
	if err := SaveBond(e.Store.DB, e.ownerKey(), "group:12345",
		Bond{Closeness: 0.9, Regard: 0.9, Familiarity: 0.5}); err != nil {
		t.Fatal(err)
	}
	got := e.GateGroupMessage(msg, self, cfg)
	if got.Stance != StanceSpeak {
		t.Fatalf("高亲密度应当把 1 条命中抬过门槛 —— §6.8 偏向没上裁决路径 (%s: %s)",
			got.Stance, got.Reason)
	}
	if !strings.Contains(got.Reason, "偏向") {
		t.Fatalf("归因里必须说明是偏向起了作用 (护栏 3「偏向可见」), got %q", got.Reason)
	}
}

// ⚠️ 这条是**回归尺子**, 对应一个真实抓到的 bug:
// recordGateOutcome 在第一条消息之后就写一行 Closeness=0 的关系记录,
// 于是第二条消息读到的不是「还没建立」而是「疏远」→ Δ=-1 → 刚认识就被扣分。
func TestGroupBias_ZeroBondIsNotDistance(t *testing.T) {
	e := gateEngine(t)
	self := SelfIdentity{UIN: "777", Nickname: "小夜"}
	cfg := gateCfg()
	// 一条消息 → 触发 recordGateOutcome 写一行 Closeness=0 的记录。
	e.GateGroupMessage(SceneMsg{SceneKey: "group:12345", UIN: "999", Text: "今天天气不错"}, self, cfg)
	br, err := LoadBond(e.Store.DB, e.ownerKey(), "group:12345")
	if err != nil || !br.Loaded {
		t.Fatalf("前置条件不成立 (应当已有一行记录): %v %v", br.Loaded, err)
	}
	if _, ok := e.groupBias(SceneMsg{SceneKey: "group:12345"}); ok {
		t.Fatal("Closeness=0 被当成了「疏远」—— 零值是未提供, 不是极值")
	}
	// 有正面证据之后才带偏向。
	if err := SaveBond(e.Store.DB, e.ownerKey(), "group:12345", Bond{Closeness: 0.8, Familiarity: 0.5}); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.groupBias(SceneMsg{SceneKey: "group:12345"}); !ok {
		t.Fatal("有正面证据之后应当给出偏向")
	}
}

// ——— ① 提示词差分尺子 ———

func TestSegmentPrompt_FindsSegmentsInOrder(t *testing.T) {
	d := &DialogEngine{}
	got := d.buildSystem(DialogInput{History: "你: 在吗"}, &PersonaProfile{Name: "小夜"}, "ja")
	segs := SegmentPrompt(got)
	if len(segs) < 4 {
		t.Fatalf("只认出 %d 个段: %v", len(segs), segs)
	}
	for i := 1; i < len(segs); i++ {
		if segs[i].Start < segs[i-1].Start {
			t.Fatalf("段位没有按出现位置排序: %v", segs)
		}
	}
	if p := PromptStructureProblem(got); p != "" {
		t.Fatalf("真实提示词违反了结构契约: %s", p)
	}
	// ⚠️ 真实提示词的标记本来就按文档顺序声明, 所以**排序没被观察到**。
	// 用一段乱序的输入才能钉住它 (变异测试发现的第一版测试只测了真实提示词)。
	shuffled := SegmentPrompt("【当前时间】t【最近对话】z【示例对话】x")
	for i := 1; i < len(shuffled); i++ {
		if shuffled[i].Start < shuffled[i-1].Start {
			t.Fatalf("乱序输入没有按出现位置排序: %v", shuffled)
		}
	}
}

// ⚠️ 这条尺子的意义: **不需要模型**就能抓到段序事故。
func TestPromptStructure_CatchesMovedOutputProtocol(t *testing.T) {
	bad := "【输出】帧协议\n【示例对话】x\n【语言规则】y\n【最近对话】z\n【当前时间】t"
	if p := PromptStructureProblem(bad); p == "" {
		t.Fatal("输出协议被挪到最前面却没报错 —— 实测那会让帧失败率 37%→75%")
	}
	bad2 := "【示例对话】x\n【最近对话】z\n【语言规则】y\n【输出】o\n【当前时间】t"
	if p := PromptStructureProblem(bad2); p == "" {
		t.Fatal("稳定段排到了每轮变化段之后却没报错 —— 段序即缓存策略")
	}
	bad3 := "【示例对话】x\n【输出】o\n【当前时间】t\n【本轮话题】q"
	if p := PromptStructureProblem(bad3); p == "" {
		t.Fatal("时间不在最后却没报错")
	}
	if p := PromptStructureProblem("什么都没有"); p == "" {
		t.Fatal("一个段都认不出来却没报错")
	}
}

func TestPromptDiff_ReportsPrefixAndMoves(t *testing.T) {
	base := "AAA【示例对话】B【最近对话】D【输出】E【当前时间】F"
	cur := "AAA【示例对话】B【最近对话】D_LONGER【输出】E【当前时间】F"
	r := PromptDiff(base, cur)
	// ⚠️ 必须断言**精确值**: 只写 ">20" 的话, 把实现改成「前缀恒等于全长」照样通过
	// (变异测试发现的第二版测试缺陷)。
	want := len("AAA【示例对话】B【最近对话】D")
	if r.CommonPrefix != want {
		t.Fatalf("公共前缀应当是 %d (到分歧点为止), 得到 %d", want, r.CommonPrefix)
	}
	if len(r.Moved) == 0 {
		fatalIfNotMoved(t, r)
	}
	if r.BaseBytes != len(base) || r.CurBytes != len(cur) {
		t.Fatal("字节数不对")
	}
	// 完全相同的两份 → 前缀等于全长, 没有位移。
	same := PromptDiff(base, base)
	if same.CommonPrefix != len(base) || len(same.Moved) != 0 {
		t.Fatalf("同一份应当 prefix=full / moved=0, got %+v", same)
	}
}

func fatalIfNotMoved(t *testing.T, r PromptDiffResult) {
	t.Helper()
	t.Fatalf("段位在它之后整体位移了, 却没报出来: %+v", r)
}
