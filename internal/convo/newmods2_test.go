package convo

import (
	"strings"
	"testing"
	"time"

	"github.com/tzhnahida/VocabLock/internal/clock"
)

// ── §5.1 三个量 ───────────────────────────────────────────

func TestBond_NegativeIsFasterThanPositive(t *testing.T) {
	dcShare, _ := BondDelta(BondSignal{SharedSecret: true})
	dcRebuff, _ := BondDelta(BondSignal{Rebuffed: true})
	if dcShare <= 0 || dcRebuff >= 0 {
		t.Fatalf("signs wrong: share=%v rebuff=%v", dcShare, dcRebuff)
	}
	if -dcRebuff <= dcShare {
		t.Fatalf("negative must move faster than positive (share=%v rebuff=%v)", dcShare, dcRebuff)
	}
}

// 熟悉度只增不减 —— 「今天吵架所以没那么熟了」是把两个量揉成一个。
func TestBond_FamiliarityOnlyGrows(t *testing.T) {
	b := Bond{Closeness: 0.5, Familiarity: 0.2}
	for i := 0; i < 10; i++ {
		b = ApplyBond(b, BondSignal{Rebuffed: true, Corrected: true, Msgs: 5})
	}
	if b.Familiarity <= 0.2 {
		t.Fatalf("familiarity must keep growing through bad turns, got %v", b.Familiarity)
	}
	if b.Closeness >= 0.5 {
		t.Fatalf("closeness should have dropped, got %v", b.Closeness)
	}
}

func TestBond_ClampsAtBounds(t *testing.T) {
	b := Bond{}
	for i := 0; i < 100; i++ {
		b = ApplyBond(b, BondSignal{SharedSecret: true, Thanked: true, Msgs: 100})
	}
	if b.Closeness != 1 || b.Regard != 1 || b.Familiarity != 1 {
		t.Fatalf("must clamp at 1, got %+v", b)
	}
	for i := 0; i < 100; i++ {
		b = ApplyBond(b, BondSignal{Rebuffed: true, Corrected: true})
	}
	if b.Closeness != 0 || b.Regard != 0 {
		t.Fatalf("must clamp at 0, got %+v", b)
	}
}

func TestBond_StageAndLine(t *testing.T) {
	if BondStageOf(Bond{}) != BondStageStranger {
		t.Fatal("zero bond must be a stranger")
	}
	if _, ok := BondLine(Bond{Closeness: 0.2}); ok {
		t.Fatal("a non-intimate bond must not put a relationship line in the prompt")
	}
	if _, ok := BondLine(Bond{Closeness: 0.8, Regard: 0.6}); !ok {
		t.Fatal("an intimate bond with high regard must produce a line")
	}
}

// 零值 = 未提供: 没有记录必须与「关系为零」区分开。
func TestBond_MissingRowIsNotLoaded(t *testing.T) {
	s := newTestStore(t)
	if err := EnsureBondSchema(s.DB); err != nil {
		t.Fatal(err)
	}
	r, err := LoadBond(s.DB, "o", "private:1")
	if err != nil {
		t.Fatal(err)
	}
	if r.Loaded {
		t.Fatal("no row must report Loaded=false, not a zero-valued bond")
	}
	if err := SaveBond(s.DB, "o", "private:1", Bond{Closeness: 0.3, Regard: 0.2, Familiarity: 0.5}); err != nil {
		t.Fatal(err)
	}
	r, err = LoadBond(s.DB, "o", "private:1")
	if err != nil || !r.Loaded {
		t.Fatalf("want loaded, got %v %v", r.Loaded, err)
	}
	if r.Closeness != 0.3 || r.Regard != 0.2 || r.Familiarity != 0.5 {
		t.Fatalf("round trip mismatch: %+v", r)
	}
}

// ── §7.5 双轨存储 ─────────────────────────────────────────

func TestIntent_ClassifyKind(t *testing.T) {
	cases := map[string]string{
		"我明天要去医院": IntentPlan,
		"我受不了香菜":  IntentPreference,
		"别再提工资了":  IntentBoundary,
		"我家在杭州":   IntentFact,
	}
	for raw, want := range cases {
		if got := ClassifyIntentKind(raw); got != want {
			t.Fatalf("%q: want %s, got %s", raw, want, got)
		}
	}
	if ClassifyIntentKind("   ") != "" {
		t.Fatal("empty text must classify to nothing, not to fact")
	}
}

func TestIntent_CanonicalDropsPureFiller(t *testing.T) {
	if _, ok := CanonicalIntent("那个就是然后其实", IntentFact); ok {
		t.Fatal("a sentence made only of fillers is not an intent")
	}
	got, ok := CanonicalIntent("那个我明天要去医院", IntentPlan)
	if !ok || strings.Contains(got, "那个") {
		t.Fatalf("fillers must be stripped, got %q ok=%v", got, ok)
	}
	long := strings.Repeat("很", 200)
	got, _ = CanonicalIntent(long, IntentFact)
	if len([]rune(got)) != IntentCanonicalMaxRunes {
		t.Fatalf("want truncation to %d runes, got %d", IntentCanonicalMaxRunes, len([]rune(got)))
	}
}

// 双轨的分界线: intent 轨永远不参与检索。
func TestIntent_NeverRetrievable(t *testing.T) {
	if IntentRetrievable() {
		t.Fatal("the intent track must never enter retrieval - that is the whole point of two tracks")
	}
}

func TestIntentLine_EmptyIsFalse(t *testing.T) {
	if _, ok := IntentLine(IntentTrack{Raw: "x"}); ok {
		t.Fatal("an empty canonical must produce no line")
	}
	l, ok := IntentLine(IntentTrack{Canonical: "明天要去医院", Kind: IntentPlan})
	if !ok || !strings.Contains(l, "TA 说过的") {
		t.Fatalf("the line must mark it as the user words, got %q", l)
	}
}

// ── §6.6 群聊角色位 ───────────────────────────────────────

// 位是固定的: 不能每天换、不能随重启换。
func TestGroupRole_Deterministic(t *testing.T) {
	first := AssignGroupRole("小满", "group:123")
	for i := 0; i < 200; i++ {
		if got := AssignGroupRole("小满", "group:123"); got != first {
			t.Fatalf("role must be fixed, run %d gave %s vs %s", i, got, first)
		}
	}
	if first == GroupRoleNone {
		t.Fatal("assignment must always land on a real role")
	}
}

func TestGroupRole_SpreadsAcrossGroups(t *testing.T) {
	seen := map[GroupRole]bool{}
	for i := 0; i < 40; i++ {
		seen[AssignGroupRole("小满", "group:"+itoa(i))] = true
	}
	if len(seen) < 3 {
		t.Fatalf("roles should spread across groups, got only %v", seen)
	}
}

func TestGroupRole_NeedBonusMovesTheGate(t *testing.T) {
	if GroupRoleNeedBonus(GroupRoleLurker) <= 0 {
		t.Fatal("the lurker must need MORE hits to speak - otherwise it is not a lurker")
	}
	if GroupRoleNeedBonus(GroupRoleFoiler) >= 0 {
		t.Fatal("the foiler must need FEWER hits to speak")
	}
	if GroupRoleNeedBonus(GroupRoleNone) != 0 {
		t.Fatal("no role must not shift the gate")
	}
}

func TestGroupRole_NoneIsSilentAndContrarianCapped(t *testing.T) {
	if _, ok := GroupRoleLine(GroupRoleNone, 0); ok {
		t.Fatal("no role must produce no line")
	}
	loose, _ := GroupRoleLine(GroupRoleContrarian, 0)
	capped, ok := GroupRoleLine(GroupRoleContrarian, GroupRoleContrarianDailyMax)
	if !ok || loose == capped {
		t.Fatal("the contrarian must get a different instruction once the daily cap is hit")
	}
	if !strings.Contains(capped, "顺着说") {
		t.Fatalf("the capped instruction must reverse it, got %q", capped)
	}
}

// ── §6.4.3 社交反馈闭环 ───────────────────────────────────

func TestFeedback_Detect(t *testing.T) {
	if k, ok := DetectFeedback("闭嘴吧你", true, 0); !ok || k != FeedbackScold {
		t.Fatalf("want scold, got %v %v", k, ok)
	}
	if k, ok := DetectFeedback("不是这样, 你搞错了", true, 0); !ok || k != FeedbackCorrect {
		t.Fatalf("want correct, got %v %v", k, ok)
	}
	if k, ok := DetectFeedback("", false, feedbackColdMinutes); !ok || k != FeedbackCold {
		t.Fatalf("want cold, got %v %v", k, ok)
	}
	if _, ok := DetectFeedback("今天天气不错", false, 1); ok {
		t.Fatal("a normal short silence must not be read as negative feedback")
	}
	if _, ok := DetectFeedback("", true, 999); ok {
		t.Fatal("a reply landed - that is not cold")
	}
}

// 归因红线: 边界指向行为, 不指向人。
func TestFeedback_BoundaryPointsAtBehaviour(t *testing.T) {
	for _, k := range []FeedbackKind{FeedbackScold, FeedbackCorrect, FeedbackCold} {
		for _, topic := range []string{"工资", ""} {
			b, ok := FeedbackToBoundary(k, topic)
			if !ok {
				t.Fatalf("%s/%q must produce a boundary", k, topic)
			}
			for _, wd := range feedbackPersonWords {
				if strings.Contains(b, wd) {
					t.Fatalf("boundary blames the person: %q", b)
				}
			}
			if strings.TrimSpace(b) == "" {
				t.Fatal("empty boundary with ok=true")
			}
		}
	}
	if _, ok := FeedbackToBoundary(FeedbackNone, "x"); ok {
		t.Fatal("none must produce nothing")
	}
}

// 一次就升格 = 被说一句就再也不提, 那是畏缩不是长记性。
func TestFeedback_PromoteNeedsRepetition(t *testing.T) {
	s := newTestStore(t)
	if err := EnsureFeedbackSchema(s.DB); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= FeedbackPromoteThreshold; i++ {
		n, err := RecordFeedback(s.DB, "o", "group:1", FeedbackScold, "工资")
		if err != nil {
			t.Fatal(err)
		}
		if n != i {
			t.Fatalf("want count %d, got %d", i, n)
		}
		// ⚠️ 走**活着的那条路** (LoadBoundaryLines) —— 它比旧的那个多一层 §7.4 的衰减渲染,
		// 而那正是提示词实际看到的东西。原来这两处用例喂的是 LoadBoundaries,
		// 而那个函数**生产上没有任何调用者**(只被这两条用例养着) ——
		// 于是"边界升格"这件事被验证的是一条**已经没人走的路**。
		bs := s.LoadBoundaryLines("o", "group:1", clock.Now())
		if i < FeedbackPromoteThreshold && len(bs) != 0 {
			t.Fatalf("promoted too early at count %d: %v", i, bs)
		}
		if i == FeedbackPromoteThreshold && len(bs) != 1 {
			t.Fatalf("must promote at count %d, got %v", FeedbackPromoteThreshold, bs)
		}
	}
}

// ── §6.3 判据① 品味 / 判据④ 冷场 ─────────────────────────

func TestTaste_TooFewSamplesIsNotATaste(t *testing.T) {
	// ⚠️ 字面量 2, **不许**写 TasteMinSeen-1: 用被测量的常量构造夹具,
	// 常量一旦被改小, 夹具会跟着缩水, 于是"没有样本下限"这个变异体就能活下来。
	// —— 这是被测常量与夹具耦合的经典盲区, 变异测试抓出来的。
	hits := []TasteHit{{Topic: "猫", Seen: 2, Spoke: 2}}
	if _, ok := TasteOf(hits, "猫"); ok {
		t.Fatal("a hot streak of 2 must not become a taste")
	}
	if _, ok := TasteCounts(hits, "猫"); ok {
		t.Fatal("TasteCounts must respect the sample floor too")
	}
	hits[0].Seen = 10
	hits[0].Spoke = 6
	if r, ok := TasteOf(hits, "猫"); !ok || r != 0.6 {
		t.Fatalf("want 0.6, got %v ok=%v", r, ok)
	}
	if _, ok := TasteCounts(hits, "狗"); ok {
		t.Fatal("an unknown topic has no taste")
	}
	if _, ok := TasteOf(hits, "猫不理"); ok {
		t.Fatal("topic must match exactly, not by prefix")
	}
}

// 判据④ 的关键反面: 新场景不是冷场。
func TestSceneColdGap_NoLastIsNotCold(t *testing.T) {
	now := time.Now()
	gap, has, valid, ceil := SceneColdGap(time.Time{}, false, now, time.Hour, 8*time.Hour)
	if has || valid || ceil || gap != 0 {
		t.Fatalf("a scene with no history must be has=false, got %v/%v/%v/%v", gap, has, valid, ceil)
	}
}

func TestSceneColdGap_Tiers(t *testing.T) {
	now := time.Now()
	min, max := time.Hour, 8*time.Hour
	if _, has, valid, _ := SceneColdGap(now.Add(-10*time.Minute), true, now, min, max); !has || valid {
		t.Fatal("10 minutes is not a cold scene, but the scene does have history")
	}
	if _, _, valid, _ := SceneColdGap(now.Add(-2*time.Hour), true, now, min, max); !valid {
		t.Fatal("2 hours inside [1h,8h] must be a valid cold hit")
	}
	if _, has, valid, ceil := SceneColdGap(now.Add(-48*time.Hour), true, now, min, max); !has || valid || !ceil {
		t.Fatal("48 hours is the ceiling - long gone, not a fresh cold scene")
	}
	if gap, _, valid, _ := SceneColdGap(now.Add(time.Hour), true, now, min, max); gap != 0 || valid {
		t.Fatal("a clock skew must not produce a negative gap or a cold hit")
	}
}

func TestLastSceneMessageAt_IgnoresAItoAI(t *testing.T) {
	s := newTestStore(t)
	ins := "INSERT INTO convo_messages (ts, dir, channel, kind, payload_json, origin, scene_key, sender_uin) VALUES (?,?,?,?,?,?,?,?)"
	recent := time.Now().Format(time.RFC3339)
	old := time.Now().Add(-6 * time.Hour).Format(time.RFC3339)
	if _, err := s.DB.Exec(ins, old, "in", "group", "text", "{}", "user", "group:1", "11"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ins, recent, "in", "group", "text", "{}", "ai_to_ai", "group:1", "0"); err != nil {
		t.Fatal(err)
	}
	got, ok, err := LastSceneMessageAt(s.DB, "group:1")
	if err != nil || !ok {
		t.Fatalf("want a hit, got ok=%v err=%v", ok, err)
	}
	if time.Since(got) < time.Hour {
		t.Fatal("the ai_to_ai row must not reset the cold clock")
	}
	if _, ok, _ := LastSceneMessageAt(s.DB, "group:999"); ok {
		t.Fatal("an unknown scene must report no history")
	}
	if _, ok, _ := LastSceneMessageAt(s.DB, ""); ok {
		t.Fatal("an empty scene key must report no history, not the global last message")
	}
}
