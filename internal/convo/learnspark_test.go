package convo

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// ═══ §2.5 自主学习 ═══

// R1: 空切片被当成「学过但都为空」→ 模型会自己编一个兴趣。
func TestTasteTrail_NoDataNoSegment(t *testing.T) {
	if _, ok := TasteTrailLine(TasteInput{HasLearned: true}, 4); ok {
		t.Fatal("什么都没有时不许产出方向段 —— 空的标题会诱发模型编一个兴趣")
	}
	if _, ok := TasteTrailLine(TasteInput{PersonaInterests: []string{"爵士乐"}}, 4); !ok {
		t.Fatal("有人格兴趣时应当产出")
	}
	line, _ := TasteTrailLine(TasteInput{PersonaInterests: []string{"爵士乐"}}, 4)
	if strings.Contains(line, "我最近在学") {
		t.Fatalf("段 4b 只给方向键, 不给内容: %q", line)
	}
}

func TestTasteTrail_LearnedIsNotMemory(t *testing.T) {
	// learned 来源的权重 0.8 > memory 0.6, 且来源可归因。
	mem := []Memory{{MemID: "m1", Label: "做饭", Source: "user_said"}}
	lr := []LearnedItem{{ID: 1, Kind: LearnFact, TopicKey: "爵士乐", Content: "x"}}
	trail := BuildTasteTrail(TasteInput{Memories: mem, Learned: lr, HasLearned: true})
	if len(trail.Topics) != 2 || trail.Topics[0].Key != "爵士乐" {
		t.Fatalf("want 爵士乐 first, got %+v", trail.Topics)
	}
	// ai_said 的记忆**不算** ta 的兴趣。
	ai := BuildTasteTrail(TasteInput{Memories: []Memory{{MemID: "m2", Label: "别的", Source: "ai_said"}}})
	if len(ai.Topics) != 0 {
		t.Fatalf("ai_said 不该进品味轨迹: %+v", ai.Topics)
	}
}

func splitTestInterests() []string { return splitInterests("爵士乐、老式合成器; 做饭") }

func TestSplitInterests(t *testing.T) {
	got := splitTestInterests()
	if len(got) != 3 || got[0] != "爵士乐" {
		t.Fatalf("拆分失败: %v", got)
	}
	if len(splitInterests("  ")) != 0 {
		t.Fatal("空串应当返回 nil")
	}
}

// R4: 学来的不许当经历。
func TestParseLearnedItem_RefusesFakeExperience(t *testing.T) {
	bad := []string{
		"{\"kind\":\"interest\",\"topic\":\"爵士乐\",\"content\":\"我上周去了一个爵士酒吧\"}",
		"{\"kind\":\"fact\",\"topic\":\"x\",\"content\":\"你应该知道这个\"}",
	}
	for _, s := range bad {
		if _, ok := ParseLearnedItem(s); ok {
			t.Fatalf("红线产出必须拒收: %s", s)
		}
	}
	// S7: 零值/未知 kind 一律拒收。
	if _, ok := ParseLearnedItem("{\"kind\":\"\",\"topic\":\"x\",\"content\":\"y\"}"); ok {
		t.Fatal("零值 kind 不许入库")
	}
	if _, ok := ParseLearnedItem("{\"kind\":\"poetry\",\"topic\":\"x\",\"content\":\"y\"}"); ok {
		t.Fatal("未知 kind 不许入库")
	}
	good := "{\"kind\":\"opinion\",\"topic\":\"爵士乐\",\"content\":\"爵士乐的好在于它不急着解决问题\",\"origin\":\"看了篇科普\"}"
	it, ok := ParseLearnedItem(good)
	if !ok || it.Kind != LearnOpinion || it.TopicKey != "爵士乐" {
		t.Fatalf("合规产出应当收下: %+v ok=%v", it, ok)
	}
}

// R12: 学习 Job 失败 → **不产生任何碎片** (绝不编一个凑数)。
func TestRunLearning_FailureProducesNothing(t *testing.T) {
	e, _ := groupSpeakEngine(t, &fakeChatter{err: errors.New("boom")})
	if err := e.runLearning(context.Background(), &PersonaProfile{Name: "小夜"}); err != nil {
		t.Fatalf("失败应当吞掉只 warn, 得到 %v", err)
	}
	items, _ := e.Store.RecentLearned(10)
	if len(items) != 0 {
		t.Fatalf("失败却产出了碎片: %+v", items)
	}
}
func TestLearned_IsInItsOwnTable(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	if _, err := e.Store.SaveLearned(LearnedItem{Kind: LearnFact, TopicKey: "爵士乐", Content: "x", BornTS: "2026-03-01 10:00:00"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.SaveLearned(LearnedItem{Kind: "", TopicKey: "y", Content: "z"}); err != nil {
		t.Fatal(err)
	}
	e.Store.SeedTopics(3)
	items, _ := e.Store.RecentLearned(10)
	if len(items) != 1 {
		t.Fatalf("SeedTopics 清空或多余写入: %+v", items)
	}
	// 也必须**不在** convo_memory 里 (否则会掉进归属 switch 的 default → 被当成关于 ta 的事)。
	var n int
	if err := e.Store.DB.QueryRow("SELECT COUNT(*) FROM convo_memory WHERE v = ?", "x").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("学习碎片进了 convo_memory —— 它会对着用户说「你最近在看爵士乐」")
	}
}

// R2 / R3: 透露的骰子与 7 天硬冷却。
func TestDecideReveal_ZeroRollIsNotProvided(t *testing.T) {
	it := LearnedItem{ID: 1, Kind: LearnFact, Content: "x"}
	if _, ok := DecideReveal(RevealInput{Candidates: []LearnedItem{it}, Roll: 0, HasRoll: false}); ok {
		t.Fatal("忘了掷 ≠ 掷出 0 —— 未提供时必须不透露")
	}
	if _, ok := DecideReveal(RevealInput{Candidates: []LearnedItem{it}, Roll: 0, HasRoll: true}); !ok {
		t.Fatal("掷出 0 应当透露")
	}
	if _, ok := DecideReveal(RevealInput{Candidates: []LearnedItem{it}, Roll: RevealMinRoll, HasRoll: true}); ok {
		t.Fatal("掷到阈值之上不该透露")
	}
}

// R3: 「即使骰子掷中也要硬拒」。
func TestDecideReveal_CooldownBeatsTheDice(t *testing.T) {
	now := time.Now()
	it := LearnedItem{ID: 1, Kind: LearnFact, Content: "x", LastSaidTS: now.Add(-2 * 24 * time.Hour).Format(bodyTSLayout)}
	if _, ok := DecideReveal(RevealInput{Candidates: []LearnedItem{it}, Roll: 0, HasRoll: true, Now: now}); ok {
		t.Fatal("2 天前说过 —— 7 天冷却必须硬拒, 即使骰子掷中")
	}
	if !RevealEligible(LearnedItem{LastSaidTS: now.Add(-8 * 24 * time.Hour).Format(bodyTSLayout)}, now, 0) {
		t.Fatal("8 天前说过应当可以再说")
	}
	if RevealEligible(LearnedItem{LastSaidTS: "不是时间"}, now, 0) {
		t.Fatal("解析不了的时刻 → 宁可不说")
	}
	// 第一条被冷却挡掉时, 应当顺延到下一条可用的。
	second := LearnedItem{ID: 2, Kind: LearnFact, Content: "y"}
	d, ok := DecideReveal(RevealInput{Candidates: []LearnedItem{it, second}, Roll: 0, HasRoll: true, Now: now})
	if !ok || d.Item.ID != 2 {
		t.Fatalf("应当顺延到未冷却的那条, got %+v ok=%v", d.Item, ok)
	}
}

func TestLearnedLine_ThreeWordingsDiffer(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range []LearnKind{LearnInterest, LearnFact, LearnOpinion} {
		l, ok := LearnedLine(RevealDecision{Item: LearnedItem{Content: "X_MARK"}, Kind: k, Matched: true})
		if !ok {
			t.Fatalf("%s 应当有措辞", k)
		}
		if seen[l] {
			t.Fatalf("%s 与前面某一档措辞**一模一样** —— 三档的形态不同, 不是同一句话换个词", k)
		}
		seen[l] = true
	}
	if _, ok := LearnedLine(RevealDecision{}); ok {
		t.Fatal("未命中不许产出")
	}
}

// R13 / S11: 分享周上限必须是**一个**计数器。
func TestDecideShare_WeeklyCapIsShared(t *testing.T) {
	cand := []ShareCandidate{{Who: 1, Scene: "qq", Match: 1, Reachable: true}}
	base := ShareInput{Candidates: cand, Heat: 9, HasHeat: true, Slot: StateFree, Roll: 0, HasRoll: true}
	if _, ok := DecideShare(base); !ok {
		t.Fatal("条件齐备时应当分享")
	}
	full := base
	full.WeeklyUsed = ShareWeeklyCap
	if _, ok := DecideShare(full); ok {
		t.Fatal("周上限用满后两类分享**都必须**停下")
	}
	full.Kind = ShareSpark
	if _, ok := DecideShare(full); ok {
		t.Fatal("灵光分享也必须吃同一个计数器 —— 两个计数器会叠加成 3/周")
	}
	learn := base
	learn.Kind = ShareLearn
	learn.LearnUsed = LearnShareWeeklyCap
	if _, ok := DecideShare(learn); ok {
		t.Fatal("学习内容分享每周 ≤1 次")
	}
}

func TestDecideShare_GatesAndGroupAnonymity(t *testing.T) {
	cand := []ShareCandidate{{Who: 1, Scene: "qq", Match: 1, Reachable: true}}
	base := ShareInput{Candidates: cand, Heat: 9, HasHeat: true, Slot: StateFree, Roll: 0, HasRoll: true}
	noHeat := base
	noHeat.HasHeat = false
	if _, ok := DecideShare(noHeat); ok {
		t.Fatal("未提供热力 ≠ 热力为零 —— 分享是例外, 失败往不做倒")
	}
	lowHeat := base
	lowHeat.Heat = ShareMinHeat - 0.1
	if _, ok := DecideShare(lowHeat); ok {
		t.Fatal("热力不足不该分享")
	}
	busy := base
	busy.Slot = StateFocus
	if _, ok := DecideShare(busy); ok {
		t.Fatal("不在可发言槽不该分享")
	}
	noRoll := base
	noRoll.HasRoll = false
	if _, ok := DecideShare(noRoll); ok {
		t.Fatal("忘了掷 = 未提供")
	}
	reach := base
	reach.Candidates = []ShareCandidate{{Who: 1, Scene: "qq", Match: 9, Reachable: false},
		{Who: 2, Scene: "qq", Match: 1, Reachable: true}}
	d, ok := DecideShare(reach)
	if !ok || d.Who != 2 {
		t.Fatalf("必须是「够得到的那一个」, got %+v ok=%v", d, ok)
	}
	grp := base
	grp.Candidates = []ShareCandidate{{Who: 1, Scene: "group:9", Match: 1, Reachable: true}}
	d2, ok := DecideShare(grp)
	if !ok {
		t.Fatal("群里也可以分享")
	}
	if d2.WhetherToName {
		t.Fatal("群里**默认不指名** —— 只对一个人分享会让旁观者尴尬")
	}
}

// ═══ §2.6 灵光 ═══

// S1 / S2: 没有素材 → 恒 false, 与骰子无关。
func TestDecideBurst_NoMaterialNoSpark(t *testing.T) {
	in := SparkBurstInput{Roll: 0, HasRoll: true}
	if _, ok := DecideBurst(in, SparkBurstConfig{}); ok {
		t.Fatal("三路素材全空时必须恒 false —— 禁止空转生成金句")
	}
	// 空切片 ≠ 有素材。
	in2 := SparkBurstInput{HasDream: true, DreamMaterial: nil, Roll: 0, HasRoll: true}
	if _, ok := DecideBurst(in2, SparkBurstConfig{}); ok {
		t.Fatal("HasDream=true 但素材为空, 仍然不许生成")
	}
	// 忘了掷 → 不生成。
	in3 := SparkBurstInput{HasDream: true, DreamMaterial: []Memory{{MemID: "m", V: "x"}}}
	if _, ok := DecideBurst(in3, SparkBurstConfig{}); ok {
		t.Fatal("忘了掷 = 未提供")
	}
	// 有素材 + 掷中 → 生成, 且带凭据。
	in4 := SparkBurstInput{HasDream: true, DreamMaterial: []Memory{{MemID: "m", V: "x"}}, Roll: 0, HasRoll: true}
	d4, ok4 := DecideBurst(in4, SparkBurstConfig{})
	if !ok4 || !d4.Source.HasRef || d4.Source.RefID != "m" {
		t.Fatalf("有素材掷中应当生成并带凭据: %+v ok=%v", d4, ok4)
	}
}

// S3 / S7: 无凭据 / 零值 kind 一律拒收。
func TestSaveSpark_RejectsNoRefAndBadKind(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	if _, err := e.Store.SaveSpark(Spark{Kind: SparkIdea, Content: "x", Source: SparkSource{HasRef: false}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.SaveSpark(Spark{Kind: "", Content: "x", Source: SparkSource{HasRef: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.SaveSpark(Spark{Kind: SparkIdea, Content: "", Source: SparkSource{HasRef: true}}); err != nil {
		t.Fatal(err)
	}
	all, _ := e.Store.AllSparks(10)
	if len(all) != 0 {
		t.Fatalf("三条不合规的都被收下了: %+v", all)
	}
	if _, err := e.Store.SaveSpark(Spark{Kind: SparkIdea, Content: "会唱歌的电阻", TalkKey: "合成器", Source: SparkSource{Kind: "dream", RefID: "m1", HasRef: true}, HeatDelta: 0.5}); err != nil {
		t.Fatal(err)
	}
	all, _ = e.Store.AllSparks(10)
	if len(all) != 1 {
		t.Fatalf("合规的应当收下: %+v", all)
	}
}

// S8 / S16: status 是 heat + 时间的**纯函数**, 不是每日清零。
func TestSparkStatus_DecaysNotResets(t *testing.T) {
	now := time.Now()
	fresh := SparkHeatInput{HeatDelta: 0.9, LastTouchTS: now.Format(bodyTSLayout), Now: now}
	old := SparkHeatInput{HeatDelta: 0.9, LastTouchTS: now.Add(-200 * time.Hour).Format(bodyTSLayout), Now: now}
	cfg := DefaultSparkStatusConfig()
	if SparkStatusAt(fresh, false, cfg) != SparkFresh {
		t.Fatal("刚生成的应当在 fresh 档")
	}
	if SparkStatusAt(old, false, cfg) == SparkFresh {
		t.Fatal("200 小时没被接住还是 fresh —— heat 没有衰减")
	}
	if SparkHeat(old) >= SparkHeat(fresh) {
		t.Fatal("时间衰减方向错了")
	}
	if SparkHeat(SparkHeatInput{HeatDelta: -5, Now: now}) < 0 {
		t.Fatal("heat 不许为负 —— 永不归零到 -∞")
	}
	// 时钟回拨不许把热度**加**上去。
	future := SparkHeatInput{HeatDelta: 0.5, LastTouchTS: now.Add(time.Hour).Format(bodyTSLayout), Now: now}
	if SparkHeat(future) != 0.5 {
		t.Fatal("时钟回拨把热度加上去了")
	}
	// 说过的冷掉是 settled, 没说的冷掉是 dropped —— 两者都不删除。
	if SparkStatusAt(old, true, cfg) != SparkSettled {
		t.Fatal("说过的应当沉降")
	}
	if SparkStatusAt(old, false, cfg) != SparkDropped {
		t.Fatal("没说的应当放下")
	}
}

// S9 / S10: 被接住是**状态更新**, 不是新 Spark; 没人接是沉降, 不是删除。
func TestTouchSpark_IsUpdateNotNewRow(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	id, err := e.Store.SaveSpark(Spark{Kind: SparkIdea, Content: "x", TalkKey: "k",
		Source: SparkSource{Kind: "dream", RefID: "m", HasRef: true}, HeatDelta: 0.5,
		LastTouchTS: time.Now().Format(bodyTSLayout)})
	if err != nil || id == 0 {
		t.Fatalf("save: %v id=%d", err, id)
	}
	before, _ := e.Store.AllSparks(10)
	if err := e.Store.TouchSpark(id, 0.4, time.Now()); err != nil {
		t.Fatal(err)
	}
	after, _ := e.Store.AllSparks(10)
	if len(after) != len(before) {
		t.Fatalf("被接住产生了新行 (无限繁殖): %d → %d", len(before), len(after))
	}
	if after[0].HeatDelta <= before[0].HeatDelta {
		t.Fatal("被接住应当抬高 heat")
	}
	// 冷掉之后行**还在**。
	long := time.Now().Add(500 * time.Hour)
	sp := after[0]
	st := SparkStatusAt(SparkHeatInput{HeatDelta: sp.HeatDelta, LastTouchTS: sp.LastTouchTS, Now: long}, false, DefaultSparkStatusConfig())
	if st != SparkDropped && st != SparkSettled {
		t.Fatalf("应当已经冷掉, got %s", st)
	}
	still, _ := e.Store.AllSparks(10)
	if len(still) != 1 {
		t.Fatal("沉降**不是删除** —— 行必须留在库里, 将来还能被联想触发")
	}
}

// S4: 硬线②「不许把组合当事实」。
func TestSparkLine_SeenPhraseOnlyForRealSources(t *testing.T) {
	dream := Spark{Kind: SparkConnection, Content: "x", Source: SparkSource{Kind: "dream", HasRef: true}}
	if sparkSeenAllowed(dream) {
		t.Fatal("梦来源不许说「我看过」")
	}
	mem := Spark{Kind: SparkConnection, Content: "x", Source: SparkSource{Kind: "memory", HasRef: true}}
	if !sparkSeenAllowed(mem) {
		t.Fatal("记忆来源可以说「看到过」")
	}
	if dream.Source.Kind != "memory" && sparkContentClean("我在哪看过这个", dream) {
		t.Fatal("梦来源的内容出现「我看过」应当判为不干净")
	}
	if !sparkContentClean("会唱歌的电阻", dream) {
		t.Fatal("干净的内容被误判")
	}
	line, ok := SparkLine(dream)
	if !ok {
		t.Fatal("应当产出注入文本")
	}
	// S12: 不许配解释。
	if !strings.Contains(line, "不要解释") {
		t.Fatalf("缺了「不要解释」的禁止条款 —— 模型会接着解释 X: %q", line)
	}
	// S5 / S6: 不许索要评价, 不许表露失望。
	// ⚠️ 这里**不许**查「你觉得怎么样」—— 我们自己的禁令句里就有这句话。
	for _, wd := range []string{"可惜", "失望", "上次我说的", "你上次说"} {
		if strings.Contains(line, wd) {
			t.Fatalf("注入文本含禁区措辞 %q: %q", wd, line)
		}
	}
	if _, ok := SparkLine(Spark{Kind: SparkIdea}); ok {
		t.Fatal("空内容不许产出")
	}
}

// ═══ §3.7 夜间多 AI 社会模拟 ═══

func TestAllocateNightBudget_SumNeverExceedsTotal(t *testing.T) {
	sessions := []NightSession{
		{Kind: NightSessionGroup, Scene: "group:night", Peers: []NightPeer{{UIN: 1, Weight: 1}, {UIN: 2, Weight: 1}}},
		{Kind: NightSessionDM, Scene: "dm:1", Peers: []NightPeer{{UIN: 1, Weight: 3}}},
		{Kind: NightSessionDM, Scene: "dm:2", Peers: []NightPeer{{UIN: 2, Weight: 1}}},
	}
	lines := AllocateNightBudget(100, sessions)
	sum := 0
	for _, l := range lines {
		sum += l.Rounds
	}
	if sum > 100 {
		t.Fatalf("预算超支: %d", sum)
	}
	if len(lines) != 3 {
		t.Fatalf("三条会话都应当分到预算: %+v", lines)
	}
	var group, dm int
	for _, l := range lines {
		if l.Kind == NightSessionGroup {
			group = l.Rounds
		} else {
			dm += l.Rounds
		}
	}
	if group < dm {
		t.Fatalf("群聊应当占大头 (60-70%%): group=%d dm=%d", group, dm)
	}
	// 权重高的私聊分得多。
	var w3, w1 int
	for _, l := range lines {
		if l.Scene == "dm:1" {
			w3 = l.Rounds
		}
		if l.Scene == "dm:2" {
			w1 = l.Rounds
		}
	}
	if w3 <= w1 {
		t.Fatalf("关系权重高的应当分得多: %d vs %d", w3, w1)
	}
	if AllocateNightBudget(100, nil) != nil {
		t.Fatal("没有会话就没有预算")
	}
}

func TestSpreadNightTurns_DeterministicAndInWindow(t *testing.T) {
	first := SpreadNightTurns("2026-03-02", 20, "group:night")
	if len(first) != 20 {
		t.Fatalf("want 20, got %d", len(first))
	}
	for _, ts := range first {
		if ts.Hour() < NightWindowStartH || ts.Hour() >= NightWindowEndH {
			t.Fatalf("落在窗口外: %v", ts)
		}
		if ts.Format("2006-01-02") != "2026-03-02" {
			t.Fatalf("落在别的日子: %v", ts)
		}
	}
	for i := 0; i < 10; i++ {
		again := SpreadNightTurns("2026-03-02", 20, "group:night")
		for j := range again {
			if !again[j].Equal(first[j]) {
				t.Fatalf("第 %d 次重算时刻变了 —— 补算会产生不一致的时间线", i)
			}
		}
	}
	if SpreadNightTurns("2026-03-02", 0, "x") != nil {
		t.Fatal("0 轮不该有时刻")
	}
}

func TestCleanNightTurn(t *testing.T) {
	bad := []string{"", "   ", "我梦到你了", "你上次说的那个", strings.Repeat("长", 61), "第一行\n第二行"}
	for _, s := range bad {
		if _, ok := CleanNightTurn(s); ok {
			t.Fatalf("应当丢弃: %q", s)
		}
	}
	got, ok := CleanNightTurn("「今天好累啊」")
	if !ok || got != "今天好累啊" {
		t.Fatalf("应当剥掉引号: %q ok=%v", got, ok)
	}
}

// 护栏 2: 用户没在场就是没在场。
func TestNightMentionsForbidden(t *testing.T) {
	if !NightMentionsForbidden("小明的猫好可爱", []string{"小明"}) {
		t.Fatal("提到真人必须被拦下")
	}
	if NightMentionsForbidden("今天好累", []string{"小明", ""}) {
		t.Fatal("空名字不该匹配一切")
	}
	if NightMentionsForbidden("随便一句话", nil) {
		t.Fatal("没有禁名时不该拦")
	}
}

// 护栏 5: 失败 / 超时 → **整批丢弃**, 不留半天记录。
func TestRunNightSocial_FailureDiscardsEverything(t *testing.T) {
	sessions := []NightSession{{Kind: NightSessionGroup, Scene: "g", Peers: []NightPeer{{UIN: 1, Name: "阿七", Brief: "话少"}}}}
	n := 0
	gen := func(ctx context.Context, sys, user string) (string, error) {
		n++
		if n > 3 {
			return "", errors.New("boom")
		}
		return "第 " + itoa(n) + " 句", nil
	}
	res := RunNightSocial(context.Background(), gen, NightSocialInput{Night: "2026-03-02", SelfName: "小夜", Sessions: sessions})
	if !res.Skipped {
		t.Fatal("失败必须整批跳过")
	}
	if len(res.Turns) != 0 {
		t.Fatalf("失败却留下 %d 轮 —— 时间线会错位, 而错位不可修复", len(res.Turns))
	}
	if NightWriteBack(res, "小夜", 6) != nil {
		t.Fatal("跳过的批次不许写回任何东西")
	}
}

func TestRunNightSocial_BudgetStopsAndWriteBackIsAttributed(t *testing.T) {
	sessions := []NightSession{{Kind: NightSessionGroup, Scene: "g", Peers: []NightPeer{{UIN: 1, Name: "小夜", Brief: "话少"}}}}
	gen := func(ctx context.Context, sys, user string) (string, error) { return "嗯", nil }
	res := RunNightSocial(context.Background(), gen, NightSocialInput{
		Night: "2026-03-02", SelfName: "小夜", Sessions: sessions, Budget: 7,
	})
	if res.Skipped {
		t.Fatalf("不该跳过: %s", res.Reason)
	}
	if res.Spent > 7 {
		t.Fatalf("超预算: %d", res.Spent)
	}
	if res.Spent != 7 {
		t.Fatalf("应当跑到预算耗尽即停, got %d", res.Spent)
	}
	mems := NightWriteBack(res, "小夜", 6)
	if len(mems) == 0 {
		t.Fatal("应当有写回")
	}
	for _, m := range mems {
		if m.Source != "ai_said" {
			t.Fatalf("来源归属必须是 ai_said, got %q —— 否则它会把 AI 之间的经历说成「你上次说…」", m.Source)
		}
	}
	// 没配会话 → 跳过而且要出声 (这里只验证返回值)。
	if r2 := RunNightSocial(context.Background(), gen, NightSocialInput{Night: "2026-03-02"}); !r2.Skipped {
		t.Fatal("没有会话必须跳过")
	}
	if r3 := RunNightSocial(context.Background(), gen, NightSocialInput{Sessions: sessions}); !r3.Skipped {
		t.Fatal("没有 night key 必须跳过")
	}
}

// ═══ 接线存活 ═══

func TestBuildSystem_TasteAndRevealReachPrompt(t *testing.T) {
	d := &DialogEngine{}
	got := d.buildSystem(DialogInput{
		TasteNote:  "TASTE_MARK",
		RevealNote: "REVEAL_MARK",
		History:    "你: 在吗",
	}, &PersonaProfile{Name: "小夜"}, "ja")
	for _, m := range []string{"TASTE_MARK", "REVEAL_MARK"} {
		if !strings.Contains(got, m) {
			t.Fatalf("%s 没有进提示词", m)
		}
	}
	// 段序: 段 4b 是周档, 必须排在段 5(记忆)与段 7.1(最近对话)**之前**。
	if strings.Index(got, "TASTE_MARK") > strings.Index(got, "【最近对话】") {
		t.Fatal("段 4b 是周档, 排在每轮段之后就是白砍缓存")
	}
	if strings.Index(got, "REVEAL_MARK") < strings.Index(got, "【最近对话】") {
		t.Fatal("段 7.4 是每轮档, 必须排在最近对话之后")
	}
}

func TestBuildSystem_SparkTopicBranch(t *testing.T) {
	d := &DialogEngine{}
	got := d.buildSystem(DialogInput{Topic: Topic{Kind: "spark", Text: "SPARK_MARK"}}, &PersonaProfile{Name: "小夜"}, "ja")
	if !strings.Contains(got, "SPARK_MARK") {
		t.Fatal("灵光没有进段 7.3")
	}
	if strings.Contains(got, "自然地聊, 别硬转") {
		t.Fatal("灵光走了普通话题分支 —— 灵光分享最忌讳配解释")
	}
	plain := d.buildSystem(DialogInput{Topic: Topic{Kind: "chat", Text: "PLAIN"}}, &PersonaProfile{Name: "小夜"}, "ja")
	if !strings.Contains(plain, "自然地聊, 别硬转") {
		t.Fatal("普通话题分支被改坏了")
	}
}

// 没有素材 → 一次 LLM 都不发 (硬线①)。
func TestRunSparkBurst_NoMaterialNoLLMCall(t *testing.T) {
	c := &sitCaptureChatter{}
	e, _ := groupSpeakEngine(t, c)
	e.runSparkBurst(context.Background(), "2026-03-02", &PersonaProfile{Name: "小夜"})
	if c.n != 0 {
		t.Fatalf("没有素材却调了 %d 次 LLM —— 那是空转生成金句", c.n)
	}
	all, _ := e.Store.AllSparks(10)
	if len(all) != 0 {
		t.Fatal("没有素材却产出了灵光")
	}
}
