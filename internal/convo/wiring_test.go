package convo

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tzhnahida/VocabLock/internal/clock"
)

// ——— 接线存活测试 ———
//
// 这一组存在的理由: 「写好了 + 单元测试绿 + 从没被调用过」是同一个形态,
// 而这个形态在本项目里已经出现过两次 (BodyLine / ApplyStyle)。
// 所以这里测的不是逻辑, 是**它到底有没有到达那一行**。

func TestBuildSystem_NewSegmentsReachPrompt(t *testing.T) {
	d := &DialogEngine{}
	got := d.buildSystem(DialogInput{
		RoleNote:     "ROLE_MARK",
		Boundaries:   []string{"BOUND_MARK"},
		BondNote:     "BOND_MARK",
		RegisterNote: "REG_MARK",
		IntentNotes:  []string{"INTENT_MARK"},
		Association:  "ASSOC_MARK",
	}, &PersonaProfile{Name: "小夜"}, "ja")
	for _, mark := range []string{"ROLE_MARK", "BOUND_MARK", "BOND_MARK", "REG_MARK", "INTENT_MARK", "ASSOC_MARK"} {
		if !strings.Contains(got, mark) {
			t.Fatalf("%s 没有进提示词 —— 这一层就是死代码", mark)
		}
	}
}

// 段序即缓存策略: 稳定段前置、天档在每轮段之前、轮级段在天档之后。
// 把任何一段挪错位置, 它之后所有段的前缀缓存都会断 —— 而现象只是"变贵了"。
func TestBuildSystem_SegmentOrderIsCachePolicy(t *testing.T) {
	d := &DialogEngine{}
	got := d.buildSystem(DialogInput{
		History:      "你: 在吗",
		RoleNote:     "ROLE_MARK",
		RegisterNote: "REG_MARK",
		Association:  "ASSOC_MARK",
	}, &PersonaProfile{Name: "小夜"}, "ja")
	idx := func(s string) int {
		i := strings.Index(got, s)
		if i < 0 {
			t.Fatalf("%s 不在提示词里", s)
		}
		return i
	}
	role, ex, reg, hist, assoc := idx("ROLE_MARK"), idx("【示例对话】"), idx("REG_MARK"), idx("【最近对话】"), idx("ASSOC_MARK")
	if role > ex {
		t.Fatal("角色位是稳定段, 必须排在段 2 示例之前 (挪到后面会毁掉两个稳定段的前缀)")
	}
	if reg > hist {
		t.Fatal("天档段必须排在每轮变化段之前")
	}
	if assoc < hist {
		t.Fatal("联想是**轮级**段, 必须排在最近对话之后")
	}
}

// 端到端: SpeakInGroup 必须真的**去读**那几张表。
func TestSpeakInGroup_ReadsTheNewTables(t *testing.T) {
	c := &sitCaptureChatter{}
	e, _ := groupSpeakEngine(t, c)
	owner := e.ownerKey()
	sk := "group:12345"
	// §5.1 一份已经很亲近的关系 —— 只有 Loaded 时才会产出一句台词。
	if err := SaveBond(e.Store.DB, owner, sk, Bond{Closeness: 0.9, Regard: 0.6}); err != nil {
		t.Fatal(err)
	}
	// §6.4.3 一条已升格的边界。
	for i := 0; i < FeedbackPromoteThreshold; i++ {
		if _, err := RecordFeedback(e.Store.DB, owner, sk, FeedbackScold, "工资"); err != nil {
			t.Fatal(err)
		}
	}
	// §7.5 一条意图。
	if err := SaveIntent(e.Store.DB, owner, sk, IntentTrack{Raw: "我明天要去医院", Canonical: "明天要去医院", Kind: IntentPlan}); err != nil {
		t.Fatal(err)
	}
	// §4.4.1b 一份基线 + 一份"最近"(更短更硬)。
	for i := 0; i < 12; i++ {
		if err := SaveRegisterVector(e.Store.DB, owner, "2026-03-0"+itoa(i%9+1), RegisterVector{N: 12, AvgRunes: 18, Modal: 1.2, Sticker: 0.4}); err != nil {
			t.Fatal(err)
		}
	}
	if err := SaveRegisterVector(e.Store.DB, owner, "2099-01-01", RegisterVector{N: 12, AvgRunes: 3, Modal: 0, Sticker: 0}); err != nil {
		t.Fatal(err)
	}
	if err := e.SpeakInGroup(context.Background(), sk, 12345, "999", "在吗"); err != nil {
		t.Fatal(err)
	}
	for _, mark := range []string{"【群里·你的位】", "【边界】", "【关系】", "【说话方式】", "【TA 说过的·打算】"} {
		if !strings.Contains(c.lastSystem, mark) {
			t.Fatalf("SpeakInGroup 没有把 %s 注入进去 —— 表建了但没人读", mark)
		}
	}
}

// §7.2 端到端: 当下这句话与某段记忆共享表层线索时才联想。
func TestSpeakInGroup_AssociationNeedsASharedCue(t *testing.T) {
	c := &sitCaptureChatter{}
	e, _ := groupSpeakEngine(t, c)
	if err := e.Store.UpsertMemory(MemoryDelta{MemID: "m1", K: "pet", V: "你那只猫上次把杯子推下去了",
		Label: "猫", Confidence: 0.9, Source: "user_said"}, "active"); err != nil {
		t.Fatal(err)
	}
	if err := e.SpeakInGroup(context.Background(), "group:12345", 12345, "999", "我刚路过宠物店, 里面有只猫"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.lastSystem, "【联想】") {
		t.Fatal("共享了「猫」这个线索却没有联想 —— 联想式检索没接上")
	}
	// 反面: 完全不相干的一句话**不许**把这段记忆硬扯出来。
	c2 := &sitCaptureChatter{}
	e2, _ := groupSpeakEngine(t, c2)
	if err := e2.Store.UpsertMemory(MemoryDelta{MemID: "m1", K: "pet", V: "你那只猫上次把杯子推下去了",
		Label: "猫", Confidence: 0.9, Source: "user_said"}, "active"); err != nil {
		t.Fatal(err)
	}
	if err := e2.SpeakInGroup(context.Background(), "group:12345", 12345, "999", "今天下雨了路上好堵"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(c2.lastSystem, "【联想】") {
		t.Fatal("没有任何共享线索却硬扯出一段旧事 —— 那比不提更伤, 它暴露了她在翻档案")
	}
}

// §6.3 判据④: 冷场必须**能触发**。
func TestCollectValueHits_ColdGapFires(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	old := time.Now().Add(-3 * time.Hour).Format(time.RFC3339)
	ins := "INSERT INTO convo_messages (ts, dir, channel, kind, payload_json, origin, scene_key, sender_uin) VALUES (?,?,?,?,?,?,?,?)"
	if _, err := e.Store.DB.Exec(ins, old, "in", "group", "text", "上一条", "user", "group:1", "11"); err != nil {
		t.Fatal(err)
	}
	in, _ := e.collectValueHits(SceneMsg{SceneKey: "group:1", UIN: "22", Text: "现在这句"}, false)
	if !in.HasGap || !in.GapValid || in.ColdCeiling {
		t.Fatalf("3 小时前的上一条消息必须构成一次冷场, got has=%v valid=%v ceil=%v", in.HasGap, in.GapValid, in.ColdCeiling)
	}
	if in.Gap < ColdGapMin || in.Gap > ColdGapMax {
		t.Fatalf("gap %v 落在窗口外", in.Gap)
	}
}

// ⚠️ 这条是本组最要紧的**反面**: 调用方先落库再裁决,
// 所以"上一条消息"如果取最新一条, 差值恒为 0, 判据④ 永不触发。
func TestCollectValueHits_ExcludesTheJustInsertedMessage(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	ins := "INSERT INTO convo_messages (ts, dir, channel, kind, payload_json, origin, scene_key, sender_uin) VALUES (?,?,?,?,?,?,?,?)"
	now := time.Now().Format(time.RFC3339)
	if _, err := e.Store.DB.Exec(ins, now, "in", "group", "text", "就是这一句", "user", "group:7", "11"); err != nil {
		t.Fatal(err)
	}
	in, _ := e.collectValueHits(SceneMsg{SceneKey: "group:7", UIN: "11", Text: "就是这一句"}, false)
	if in.HasGap {
		t.Fatal("刚插进去的这一条被当成了上一条 —— 判据④ 会永远量到 0, 于是永不触发")
	}
}

// 新场景不是冷场。
func TestCollectValueHits_NewSceneIsNotCold(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	in, _ := e.collectValueHits(SceneMsg{SceneKey: "group:404", UIN: "11", Text: "第一次来"}, false)
	if in.HasGap || in.GapValid {
		t.Fatal("没有任何历史的场景不是冷场")
	}
}

// §6.6 角色位落在**门槛**上: 潜水位更难开口, 捧哏位更容易。
func TestEffectiveValueConfig_RoleMovesTheGate(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	base := ValueConfigFrom(DefaultGroupConfig())
	seen := map[GroupRole]int{}
	for i := 0; i < 60; i++ {
		sk := "group:" + itoa(i)
		role := AssignGroupRole("小夜", sk)
		got := e.effectiveValueConfig(SceneMsg{SceneKey: sk}, DefaultGroupConfig())
		want := base.SpeakMinHits + GroupRoleNeedBonus(role)
		if want < 2 {
			want = 2
		}
		if got.SpeakMinHits != want {
			t.Fatalf("%s(%s): 门槛应当是 %d, 实际 %d", sk, role, want, got.SpeakMinHits)
		}
		seen[role]++
	}
	if seen[GroupRoleLurker] == 0 || seen[GroupRoleFoiler] == 0 {
		t.Fatalf("60 个群应当覆盖到潜水与捧哏两个位, 实际 %v", seen)
	}
}

// §6.3 判据① 的**闭环**: 沉默也要记, 否则接话率恒为 100%。
func TestRecordGateOutcome_RecordsSilentToo(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	if _, err := e.Store.UpsertSlang(SlangDelta{Phrase: "麻了", Meaning: "累", Example: "麻了",
		SourceQuote: "麻了", Confidence: 0.9}, "user"); err != nil {
		t.Fatal(err)
	}
	e.recordGateOutcome(SceneMsg{SceneKey: "group:1", Text: "今天麻了"}, GroupGateResult{Stance: StanceSilent})
	e.recordGateOutcome(SceneMsg{SceneKey: "group:1", Text: "又麻了"}, GroupGateResult{Stance: StanceSpeak})
	hits, err := LoadTaste(e.Store.DB, e.ownerKey())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, h := range hits {
		if h.Topic == "麻了" {
			found = true
			if h.Seen != 2 || h.Spoke != 1 {
				t.Fatalf("沉默也必须进分母: want seen=2 spoke=1, got %+v", h)
			}
		}
	}
	if !found {
		t.Fatal("品味轨迹一条都没落 —— 判据① 的闭环没接上")
	}
}

// §6.4.3 骂 → 关系量**掉得比涨得快**; 熟悉度照涨。
func TestRecordGateOutcome_ScoldMovesBond(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	for i := 0; i < 5; i++ {
		e.recordGateOutcome(SceneMsg{SceneKey: "group:9", Text: "闭嘴吧你"}, GroupGateResult{Stance: StanceSilent})
	}
	br, err := LoadBond(e.Store.DB, e.ownerKey(), "group:9")
	if err != nil || !br.Loaded {
		t.Fatalf("关系量没落库: %v %v", br.Loaded, err)
	}
	if br.Closeness != 0 {
		t.Fatalf("五次被骂, 亲密度应当被压到 0, 实际 %v", br.Closeness)
	}
	if br.Familiarity <= 0 {
		t.Fatal("熟悉度只增不减 —— 被骂也照样涨")
	}
	// ⚠️ 同上: 测**活着的那条路**。
	bs := e.Store.LoadBoundaryLines(e.ownerKey(), "group:9", clock.Now())
	if len(bs) != 1 {
		t.Fatalf("五次同类负反馈应当升格出一条边界, 实际 %v", bs)
	}
}

// §7.5 意图轨: 只有 plan/preference/boundary 才收, fact 走记忆那条路。
func TestRecordGateOutcome_IntentTrackIsSelective(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	e.recordGateOutcome(SceneMsg{SceneKey: "group:3", Text: "我明天要去医院复查"}, GroupGateResult{Stance: StanceSilent})
	e.recordGateOutcome(SceneMsg{SceneKey: "group:3", Text: "我家住在杭州"}, GroupGateResult{Stance: StanceSilent})
	its, err := LoadIntents(e.Store.DB, e.ownerKey(), "group:3", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(its) != 1 || its[0].Kind != IntentPlan {
		t.Fatalf("只该收下那条 plan, 实际 %+v", its)
	}
	if IntentRetrievable() {
		t.Fatal("意图轨进了检索 —— 双轨就退化成单轨了")
	}
}

// W9 变异体被 AssociationLine 的空线索检查**挡在了第二道防线**上, 于是接线测试看不见它。
// 这条直接断言第一道防线本身 —— 两道防线都要有各自的尺子。
func TestAssociateMemories_NoSharedCueIsFalse(t *testing.T) {
	mems := []Memory{{MemID: "m1", Label: "猫", V: "你那只猫上次把杯子推下去了"}}
	if _, _, ok := AssociateMemories(mems, "今天下雨了路上好堵", nil); ok {
		t.Fatal("没有任何共享线索时必须 false —— 否则联想退化成随机挑一段记忆, 那比不提更伤")
	}
	if _, _, ok := AssociateMemories(mems, "", nil); ok {
		t.Fatal("空文本不许联想")
	}
	if _, cue, ok := AssociateMemories(mems, "你那只猫怎么了", nil); !ok || cue.Token == "" {
		t.Fatal("共享了线索就必须给出**是哪条线索** —— 空线索等于没说")
	}
	if _, _, ok := AssociateMemories(mems, "你那只猫怎么了", map[string]bool{"m1": true}); ok {
		t.Fatal("skip 必须生效")
	}
}
