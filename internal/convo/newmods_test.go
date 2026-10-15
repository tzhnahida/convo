package convo

import (
	"strings"
	"testing"
	"time"
)

// ── §4.4.1b 语域漂移 ─────────────────────────────────────

func TestRegister_EmptyBaselineNeverDrifts(t *testing.T) {
	base := RegisterVector{}
	recent := RegisterFromMsgs([]RegisterMsg{{Text: "嗯"}}[0:1])
	recent.N = 20
	if d := DetectRegisterDrift(base, recent); d.Drifted {
		t.Fatalf("no baseline must never drift, got %s (%.2f)", d.Dir, d.Magnitude)
	}
}

func TestRegister_TooFewSamplesNeverDrifts(t *testing.T) {
	long := []RegisterMsg{}
	for i := 0; i < 20; i++ {
		long = append(long, RegisterMsg{Text: "今天真的好累啊，你说是不是嘛，哈哈"})
	}
	short := []RegisterMsg{{Text: "嗯"}, {Text: "哦"}, {Text: "嗯"}}
	if d := DetectRegisterDrift(RegisterFromMsgs(long), RegisterFromMsgs(short)); d.Drifted {
		t.Fatalf("3 samples is below the gate, must not drift")
	}
}

func TestRegister_HarderDetected(t *testing.T) {
	var baseMsgs, recentMsgs []RegisterMsg
	for i := 0; i < 12; i++ {
		baseMsgs = append(baseMsgs, RegisterMsg{Text: "今天真的好累啊，你说是不是嘛，哈哈"})
		recentMsgs = append(recentMsgs, RegisterMsg{Text: "嗯"})
	}
	d := DetectRegisterDrift(RegisterFromMsgs(baseMsgs), RegisterFromMsgs(recentMsgs))
	if !d.Drifted || d.Dir != RegisterDirHarder {
		t.Fatalf("want harder, got drifted=%v dir=%s mag=%.2f", d.Drifted, d.Dir, d.Magnitude)
	}
	if _, ok := RegisterDriftLine(d); !ok {
		t.Fatal("harder drift must produce a line")
	}
	if !DriftDisablesNightSocial(d.Dir) {
		t.Fatal("harder must disable night social")
	}
}

func TestRegister_SofterDetected(t *testing.T) {
	var baseMsgs, recentMsgs []RegisterMsg
	for i := 0; i < 12; i++ {
		baseMsgs = append(baseMsgs, RegisterMsg{Text: "嗯"})
		recentMsgs = append(recentMsgs, RegisterMsg{Text: "今天真的好累啊，你说是不是嘛，哈哈哈"})
	}
	d := DetectRegisterDrift(RegisterFromMsgs(baseMsgs), RegisterFromMsgs(recentMsgs))
	if !d.Drifted || d.Dir != RegisterDirSofter {
		t.Fatalf("want softer, got drifted=%v dir=%s", d.Drifted, d.Dir)
	}
	if DriftDisablesNightSocial(d.Dir) {
		t.Fatal("softer must NOT disable night social")
	}
}

func TestRegister_StickerIsNotText(t *testing.T) {
	msgs := []RegisterMsg{{Text: "很长很长很长很长很长很长的话", IsSticker: true}}
	v := RegisterFromMsgs(msgs)
	if v.Sticker != 1 || v.AvgRunes != 0 {
		t.Fatalf("sticker must not contribute runes, got %+v", v)
	}
}

func TestRegister_LoadBaselineIsZeroWhenEmpty(t *testing.T) {
	s := newTestStore(t)
	v, err := LoadRegisterBaseline(s.DB, "owner-a", 7)
	if err != nil {
		t.Fatal(err)
	}
	if v.N != 0 {
		t.Fatalf("no rows must be N=0 (not a zero-valued baseline), got %+v", v)
	}
	if d := DetectRegisterDrift(v, RegisterVector{N: 10, AvgRunes: 1}); d.Drifted {
		t.Fatal("an empty baseline must never produce a drift")
	}
}

func TestRegister_SaveLoadWeightsByN(t *testing.T) {
	s := newTestStore(t)
	if err := EnsureRegisterSchema(s.DB); err != nil {
		t.Fatal(err)
	}
	if err := SaveRegisterVector(s.DB, "o", "2026-01-01", RegisterVector{N: 10, AvgRunes: 10}); err != nil {
		t.Fatal(err)
	}
	if err := SaveRegisterVector(s.DB, "o", "2026-01-02", RegisterVector{N: 30, AvgRunes: 20}); err != nil {
		t.Fatal(err)
	}
	v, err := LoadRegisterBaseline(s.DB, "o", 7)
	if err != nil {
		t.Fatal(err)
	}
	if v.N != 40 {
		t.Fatalf("want N=40, got %d", v.N)
	}
	if want := (10*10 + 20*30) / 40.0; v.AvgRunes != want {
		t.Fatalf("want weighted mean %v, got %v", want, v.AvgRunes)
	}
}

// ── §4.4.6 失眠成因 ───────────────────────────────────────

func TestInsomnia_NotProvidedIsNotInsomnia(t *testing.T) {
	ok, score, _ := InsomniaFrom(InsomniaInput{Conflict: 5, Dream: true})
	if ok || score != 0 {
		t.Fatalf("unprovided input is NOT insomnia, got ok=%v score=%d", ok, score)
	}
}

func TestInsomnia_SingleSignalCannotTrigger(t *testing.T) {
	cases := []InsomniaInput{
		{Provided: true, Conflict: 1},
		{Provided: true, BodyCycle: true},
		{Provided: true, Dream: true},
		{Provided: true, Booze: 100},
		{Provided: true, Overwork: 1},
	}
	for _, in := range cases {
		if ok, score, _ := InsomniaFrom(in); ok {
			t.Fatalf("%+v alone must stay under the threshold (score=%d)", in, score)
		}
	}
}

func TestInsomnia_CausesAccumulate(t *testing.T) {
	ok, score, cause := InsomniaFrom(InsomniaInput{Provided: true, Conflict: 2, LateMsg: 1})
	if !ok || score < InsomniaThreshold {
		t.Fatalf("fight + late message must be insomnia, got ok=%v score=%d", ok, score)
	}
	if !strings.Contains(cause, "吵") || !strings.Contains(cause, "半夜") {
		t.Fatalf("cause should name both, got %q", cause)
	}
}

func TestInsomniaCauseLine_RefusesToBlameUser(t *testing.T) {
	for _, c := range []string{"因为你昨晚一直发消息", "都怪你", "你让我睡不着"} {
		if _, ok := InsomniaCauseLine(c); ok {
			t.Fatalf("blaming the user must be dropped entirely: %q", c)
		}
	}
	if _, ok := InsomniaCauseLine("白天吵了一架"); !ok {
		t.Fatal("a self-caused reason must produce a line")
	}
	if _, ok := InsomniaCauseLine(""); ok {
		t.Fatal("empty cause must produce nothing")
	}
}

func TestAdjustSleepPoint_Guardrails(t *testing.T) {
	base := SleepPointState{Day: "d1"}
	if _, err := AdjustSleepPoint(base, -15, 480, "  ", "d1"); err != ErrSleepPointReasonRequired {
		t.Fatalf("want reason-required, got %v", err)
	}
	if _, err := AdjustSleepPoint(base, -60, 480, "没睡好", "d1"); err != ErrSleepPointSingleTooBig {
		t.Fatalf("want single-cap, got %v", err)
	}
	st, err := AdjustSleepPoint(base, -30, 480, "没睡好", "d1")
	if err != nil {
		t.Fatal(err)
	}
	if st.OffsetMin != -30 || st.WeekUsedMin != 30 || st.TodayUsed != 1 {
		t.Fatalf("unexpected state %+v", st)
	}
	if _, err := AdjustSleepPoint(st, -10, 480, "又没睡好", "d1"); err != ErrSleepPointDailyCap {
		t.Fatalf("want daily-cap, got %v", err)
	}
	if _, err := AdjustSleepPoint(st, -10, 480, "又没睡好", "d2"); err != nil {
		t.Fatalf("next day should be allowed, got %v", err)
	}
	week := SleepPointState{WeekUsedMin: 110, Day: "d3"}
	if _, err := AdjustSleepPoint(week, -30, 480, "没睡好", "d3"); err != ErrSleepPointWeekTooBig {
		t.Fatalf("want week-cap, got %v", err)
	}
	// 推迟睡才会缩短窗口; 提前睡是变长。夹具原先把符号写反了。
	floor := SleepPointState{OffsetMin: 100, Day: "d4"}
	if _, err := AdjustSleepPoint(floor, -30, 480, "没睡好", "d4"); err != ErrSleepPointFloor {
		t.Fatalf("want sleep-floor, got %v", err)
	}
}

func TestSleepPoint_ForcedReset(t *testing.T) {
	st := SleepPointState{OffsetMin: -120, WeekUsedMin: 120, Streak: InsomniaStreakMax - 1}
	if _, reset := ForcedReset(st, "d9"); reset {
		t.Fatal("below the streak cap must not reset")
	}
	st.Streak = InsomniaStreakMax
	nxt, reset := ForcedReset(st, "d9")
	if !reset || nxt.OffsetMin != 0 || nxt.WeekUsedMin != 0 {
		t.Fatalf("expected a hard reset, got reset=%v %+v", reset, nxt)
	}
}

// ── §10.3 依赖护栏 ────────────────────────────────────────

func TestGuard_CrisisShortCircuits(t *testing.T) {
	lv, why := GuardLevelOf(GuardSignal{CrisisWords: 1})
	if lv != GuardCrisis {
		t.Fatalf("crisis must short-circuit, got %v (%v)", lv, why)
	}
	for _, s := range []string{"我不想活了", "想自杀", "活着没意思"} {
		if !CrisisHit(s) {
			t.Fatalf("CrisisHit missed %q", s)
		}
	}
	if CrisisHit("今天真开心") {
		t.Fatal("false positive on a happy message")
	}
}

func TestGuard_NormalIsSilent(t *testing.T) {
	lv, _ := GuardLevelOf(GuardSignal{NightMsgs: 1, DailyStreak: 3})
	if lv != GuardNormal {
		t.Fatalf("want normal, got %v", lv)
	}
	if _, ok := GuardLine(lv, "小明"); ok {
		t.Fatal("guard must not appear in the prompt most of the time")
	}
}

// 反面用例: 护栏只影响"说什么", 绝不影响"是否回应"。
func TestGuard_LineNeverRefuses(t *testing.T) {
	for _, lv := range []GuardLevel{GuardNormal, GuardNotice, GuardNudge, GuardCrisis} {
		line, ok := GuardLine(lv, "小明")
		if !ok {
			continue
		}
		for _, wd := range guardRefuseWords {
			if strings.Contains(line, wd) {
				t.Fatalf("level %v refuses to talk: %q", lv, line)
			}
		}
		for _, wd := range guardDiagnoseWords {
			if strings.Contains(line, wd) {
				t.Fatalf("level %v diagnoses the user: %q", lv, line)
			}
		}
	}
}

// ── §7.2 联想式检索 ───────────────────────────────────────

func assocFixture() *AssocIndex {
	return BuildAssocIndex([]AssocItem{
		{Key: "cat", Text: "你家那只猫上次把花瓶推下去了", Weight: 0.1,
			Cues: []AssocCue{{Token: "猫", Kind: "entity"}, {Token: "宠物店", Kind: "place"}}},
		{Key: "heavy", Text: "一段权重极高但没有任何线索的记忆", Weight: 99},
		{Key: "exam", Text: "你上次说要去考的那个证", Weight: 0.5,
			Cues: []AssocCue{{Token: "考试", Kind: "entity"}}},
	})
}

// 头号反面用例: 没有线索就不许联想。
func TestAssoc_NoCueNoAssociation(t *testing.T) {
	ix := assocFixture()
	if _, _, ok := ix.Associate("今天天气不错啊", nil); ok {
		t.Fatal("nothing in the cue index was mentioned - must not associate")
	}
	if _, _, ok := ix.Associate("", nil); ok {
		t.Fatal("empty text must not associate")
	}
}

// 第二条反面用例: 权重再高, 没有线索也不许被选中。
func TestAssoc_WeightIsNotTheEntry(t *testing.T) {
	ix := assocFixture()
	it, _, ok := ix.Associate("我刚路过宠物店", nil)
	if !ok || it.Key != "cat" {
		t.Fatalf("want cat via the place cue, got ok=%v key=%s", ok, it.Key)
	}
	if it.Key == "heavy" {
		t.Fatal("a weight-99 item with no cue must never be picked")
	}
}

func TestAssoc_SkipAndRune(t *testing.T) {
	ix := assocFixture()
	if _, _, ok := ix.Associate("宠物店", map[string]bool{"cat": true}); ok {
		t.Fatal("skip must remove the only matching item")
	}
	// 停用字必须丢, 但单字**实词**必须留 —— 长度不是判据。
	stop := BuildAssocIndex([]AssocItem{{Key: "x", Text: "t", Cues: []AssocCue{{Token: "我"}}}})
	if stop.TokenCount() != 0 {
		t.Fatal("single-rune function words are noise and must be dropped")
	}
	keep := BuildAssocIndex([]AssocItem{{Key: "cat", Text: "你那只猫", Cues: []AssocCue{{Token: "猫"}}}})
	if keep.TokenCount() != 1 {
		t.Fatal("a single-rune CONTENT word must survive - dropping it means it never recalls your cat")
	}
	if _, _, ok := keep.Associate("我刚才看到一只猫", nil); !ok {
		t.Fatal("a single-rune content cue must be able to fire")
	}
}

func TestAssociationLine_EmptyIsFalse(t *testing.T) {
	if _, ok := AssociationLine(AssocItem{Text: "x"}, AssocCue{}); ok {
		t.Fatal("empty cue must produce nothing")
	}
	if _, ok := AssociationLine(AssocItem{}, AssocCue{Token: "猫"}); ok {
		t.Fatal("empty memory must produce nothing")
	}
}

// ── §6.9 冲突执行 ─────────────────────────────────────────

func TestConflictAct_Gates(t *testing.T) {
	if a, why := DecideConflictAct(ConflictActInput{Grade: 3, CoolingDown: true}); a != ConflictActNone || why != "cooldown" {
		t.Fatalf("want cooldown gate, got %s/%s", a, why)
	}
	if a, why := DecideConflictAct(ConflictActInput{Grade: 3, ActedToday: ConflictActDailyCap}); a != ConflictActNone || why != "daily_cap" {
		t.Fatalf("want daily cap, got %s/%s", a, why)
	}
	if a, why := DecideConflictAct(ConflictActInput{Grade: 3, SceneKind: "private:123"}); a != ConflictActNone || why != "private_no_mediation" {
		t.Fatalf("want private gate, got %s/%s", a, why)
	}
}

func TestConflictAct_OwnerIsNeverJudgedPublicly(t *testing.T) {
	// ⚠️ 必须用 Grade 2: Grade 3 时"群主"分支与"太凶"分支**殊途同归**都是 DM,
	// 把群主分支整个删掉测试照样绿 —— 这正是变异测试抓出来的盲区。
	a, why := DecideConflictAct(ConflictActInput{Grade: 2, SceneKind: "group:1", IsOwnerInvolved: true})
	if a != ConflictActDM || why != "owner_involved_no_public_side" {
		t.Fatalf("owner in a fight must go private, got %s (%s)", a, why)
	}
	if a2, why2 := DecideConflictAct(ConflictActInput{Grade: 2, SceneKind: "group:1"}); a2 != ConflictActDivert {
		t.Fatalf("the same grade without the owner must diverge, got %s (%s)", a2, why2)
	}
}
func TestConflictAct_HeatAndMediation(t *testing.T) {
	if a, _ := DecideConflictAct(ConflictActInput{Grade: 3, SceneKind: "group:1"}); a != ConflictActDM {
		t.Fatalf("an unmediated grade-3 fight must go private, got %s", a)
	}
	if a, _ := DecideConflictAct(ConflictActInput{Grade: 3, SceneKind: "group:1", MediationHit: true}); a != ConflictActAbsorb {
		t.Fatalf("someone already mediated - do not mediate again, got %s", a)
	}
	if a, _ := DecideConflictAct(ConflictActInput{Grade: 2, SceneKind: "group:1"}); a != ConflictActDivert {
		t.Fatalf("want divert, got %s", a)
	}
	if a, _ := DecideConflictAct(ConflictActInput{Grade: 1, SceneKind: "group:1"}); a != ConflictActAbsorb {
		t.Fatalf("want absorb, got %s", a)
	}
}

// 红线: 它绝不评理。
func TestConflictActFrame_NeverJudges(t *testing.T) {
	for _, act := range []ConflictAct{ConflictActDivert, ConflictActAbsorb, ConflictActDM} {
		f, ok := ConflictActFrame(act, "小王", "刚才那事")
		if !ok {
			t.Fatalf("%s must have a frame", act)
		}
		if !ConflictActFrameIsClean(f) {
			t.Fatalf("%s frame judges someone: %q", act, f)
		}
	}
	if _, ok := ConflictActFrame(ConflictActNone, "小王", ""); ok {
		t.Fatal("none must have no frame")
	}
}

// ── §7.1 日终折叠 ─────────────────────────────────────────

func foldFixture() []FoldMsg {
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	return []FoldMsg{
		{SenderUIN: 11, Text: "有人在吗", TS: base},
		{SenderUIN: 22, Text: "猫粮买了吗", TS: base},
		{SenderUIN: 33, Text: "猫又胖了", TS: base},
		{SenderUIN: 22, Text: "考试周真难熬", TS: base},
		{SenderUIN: 44, Text: "哈哈", TS: base},
	}
}

func TestFold_EmptyIsEmpty(t *testing.T) {
	r := FoldGroupDay(nil, []string{"猫"}, 3)
	if r.Total != 0 {
		t.Fatalf("want zero result, got %+v", r)
	}
	if _, ok := FoldLine(r); ok {
		t.Fatal("an empty day must not become a memory")
	}
}

func TestFold_TopicsRankedAndFallback(t *testing.T) {
	r := FoldGroupDay(foldFixture(), []string{"猫", "考试", "不存在的词"}, 3)
	if len(r.Topics) != 2 || r.Topics[0] != "猫" || r.Topics[1] != "考试" {
		t.Fatalf("want [猫 考试] by hit count, got %v", r.Topics)
	}
	if r.Speakers != 4 || r.Total != 5 {
		t.Fatalf("want 4 speakers / 5 msgs, got %d/%d", r.Speakers, r.Total)
	}
	if !strings.Contains(r.Text, "猫") {
		t.Fatalf("text should mention the top topic, got %q", r.Text)
	}
	no := FoldGroupDay(foldFixture(), []string{"完全不沾边的词"}, 3)
	if len(no.Topics) != 0 || !strings.Contains(no.Text, "随便扯") {
		t.Fatalf("no keyword hit should fall back, got %+v", no)
	}
}

// 确定性: 同一天折两次必须是同一个结果, 否则"幂等"没有意义。
func TestFold_Deterministic(t *testing.T) {
	first := FoldGroupDay(foldFixture(), []string{"猫", "考试", "哈哈", "在吗"}, 3)
	for i := 0; i < 20; i++ {
		r := FoldGroupDay(foldFixture(), []string{"猫", "考试", "哈哈", "在吗"}, 3)
		if r.Text != first.Text || len(r.Topics) != len(first.Topics) {
			t.Fatalf("run %d differs: %q vs %q", i, r.Text, first.Text)
		}
		for j := range r.Topics {
			if r.Topics[j] != first.Topics[j] {
				t.Fatalf("run %d topic order differs: %v vs %v", i, r.Topics, first.Topics)
			}
		}
	}
}

func TestFold_SaveIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	if err := EnsureFoldSchema(s.DB); err != nil {
		t.Fatal(err)
	}
	r := FoldGroupDay(foldFixture(), []string{"猫", "考试"}, 3)
	ins, err := SaveFold(s.DB, "o", "group:1", "2026-03-01", r)
	if err != nil || !ins {
		t.Fatalf("first save should insert, got ins=%v err=%v", ins, err)
	}
	ins2, err := SaveFold(s.DB, "o", "group:1", "2026-03-01", FoldResult{Total: 999, Text: "另一次折叠"})
	if err != nil {
		t.Fatal(err)
	}
	if ins2 {
		t.Fatal("second save on the same day must be ignored")
	}
	got, ok, err := LoadFold(s.DB, "o", "group:1", "2026-03-01")
	if err != nil || !ok {
		t.Fatalf("load failed: ok=%v err=%v", ok, err)
	}
	if got.Total != r.Total || got.Text != r.Text {
		t.Fatalf("the first fold must survive, got %+v", got)
	}
	if _, ok, _ := LoadFold(s.DB, "o", "group:1", "2026-03-02"); ok {
		t.Fatal("a missing day must report not-found, not an empty memory")
	}
}
