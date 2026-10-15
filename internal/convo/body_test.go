package convo

import (
	"strings"
	"testing"
	"time"

	"github.com/tzhnahida/VocabLock/internal/clock"
)

// ——— §4.2 身体: 契约测试 ———
//
// 这一批的核心不是"能生病", 而是三条**方向性**约束与两条**闸门**。
// 反面用例比正面重要 —— 正面只说"机制在跑", 反面才说"它不会跑歪"。

func bodyAt(t *testing.T, onset string, courseH float64) BodyState {
	t.Helper()
	return BodyState{Kind: BodyCold, Cause: "昨天淋了雨", Note: "嗓子有点哑", OnsetTS: onset, CourseH: courseH}
}

// ——— 阶梯式恢复 ———

// TestBodyLadder_NoFullHealthJump 反面: **不许开关式满血** (§4.2 原文)。
// 相邻两档的落差必须远小于整个病程的幅度, 否则"病好了"就没有过程。
func TestBodyLadder_NoFullHealthJump(t *testing.T) {
	prev := 1.01 // 起点略高于最高档, 免得第 0 档跟自己比
	for i, s := range BodySeverityLadder {
		if s >= prev {
			t.Fatalf("第 %d 档 %.2f 没有比上一档 %.2f 更低", i, s, prev)
		}
		prev = s
	}
	// 最大单步落差: 连它都吃掉整个病程了, 那还是开关。
	prev = 1.0
	worst := 0.0
	for _, s := range BodySeverityLadder {
		if d := prev - s; d > worst {
			worst = d
		}
		prev = s
	}
	if worst > 0.5 {
		t.Fatalf("单步落差 %.2f 太大 — 这就是开关式满血", worst)
	}
	// 从最后一档到 0 (病程结束) 必须是**最小**的一跳。
	last := BodySeverityLadder[len(BodySeverityLadder)-1]
	if last >= worst {
		t.Fatalf("最后一档 %.2f >= 最大单步落差 %.2f — 结束不该是最陡的那一跳", last, worst)
	}
}

// TestBodySeverityAt_StepsDownNotRamp 正面: 病程内每四分之一段刚好落在相邻档上。
func TestBodySeverityAt_StepsDownNotRamp(t *testing.T) {
	onset := time.Date(2026, 3, 1, 9, 0, 0, 0, time.Local)
	course := 100 * time.Hour
	var got []float64
	for _, f := range []float64{0.0, 0.21, 0.41, 0.61, 0.81} {
		sev, tail := BodySeverityAt(onset, course, onset.Add(time.Duration(f*float64(course))))
		if tail {
			t.Fatalf("进度 %.2f 不该在尾巴期", f)
		}
		got = append(got, sev)
	}
	for i := 1; i < len(got); i++ {
		if got[i] >= got[i-1] {
			t.Fatalf("严重度没有逐档下降: %v", got)
		}
	}
	if got[0] != BodySeverityLadder[0] {
		t.Fatalf("病程起点应为最重档, 得到 %v", got[0])
	}
}

// TestBodySeverityAt_TailThenGone 尾巴期: severity 归零但**还没结束**。
func TestBodySeverityAt_TailThenGone(t *testing.T) {
	onset := time.Date(2026, 3, 1, 9, 0, 0, 0, time.Local)
	course := 10 * time.Hour
	// 刚过病程 → 尾巴
	sev, tail := BodySeverityAt(onset, course, onset.Add(course+time.Hour))
	if sev != 0 || !tail {
		t.Fatalf("刚过病程应进尾巴期, 得到 sev=%.2f tail=%v", sev, tail)
	}
	// 尾巴走完 → 彻底没事
	sev, tail = BodySeverityAt(onset, course, onset.Add(course+(BodyTailHours+1)*time.Hour))
	if sev != 0 || tail {
		t.Fatalf("尾巴走完该彻底结束, 得到 sev=%.2f tail=%v", sev, tail)
	}
	// 反面: 尾巴不是"病还在" —— 它不许产生配额下调。
	b := bodyAt(t, onset.Format(bodyTSLayout), 10)
	b.Refresh(onset.Add(course + time.Hour))
	if !b.Active || !b.Tail {
		t.Fatalf("尾巴期应 Active 且 Tail, 得到 %+v", b)
	}
}

// TestBodySeverityAt_FailSafe 反面: 读不出的时刻 / 非正病程 → **当作没病**。
// 与 MoodPacingFactor(0)=1.0 同一条原则: 失败往"正常"倒。
func TestBodySeverityAt_FailSafe(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.Local)
	if sev, tail := BodySeverityAt(now, 0, now); sev != 0 || tail {
		t.Fatal("零病程该返回没病")
	}
	var empty BodyState
	empty.Refresh(now)
	if empty.Active {
		t.Fatal("空身体状态不该 Active")
	}
	if e := empty.Effect(now); e.Active || e.DelayFactor != 1.0 || e.LengthFactor != 1.0 || e.QuotaDelta != 0 {
		t.Fatalf("空状态必须是一张白纸, 得到 %+v", e)
	}
	bad := BodyState{Kind: BodyCold, OnsetTS: "不是时间", CourseH: 72}
	bad.Refresh(now)
	if bad.Active {
		t.Fatal("时刻读不出必须当作没病, 不能倒推成'正在病'")
	}
}

// ——— 行为效果的方向性 ———

// TestBodyEffectTable_Directions 反面: 三条方向性约束逐行钉死。
// 破了任何一条, "身体"这一层就从"有代价的状态"退化成随机扰动。
func TestBodyEffectTable_Directions(t *testing.T) {
	for i, r := range BodyEffectTable {
		if r.DelayFactor < 1.0 {
			t.Fatalf("第 %d 行 DelayFactor=%.2f < 1 — 生病不该更快", i, r.DelayFactor)
		}
		if r.LengthFactor > 1.0 || r.LengthFactor <= 0 {
			t.Fatalf("第 %d 行 LengthFactor=%.2f — 生病只会更短", i, r.LengthFactor)
		}
		if r.QuotaDelta > 0 {
			t.Fatalf("第 %d 行 QuotaDelta=%d > 0 — 生病不该更爱开口", i, r.QuotaDelta)
		}
	}
	// 表必须按 MinSeverity 降序 —— 否则"第一个命中"就不是最重那档。
	for i := 1; i < len(BodyEffectTable); i++ {
		if BodyEffectTable[i].MinSeverity >= BodyEffectTable[i-1].MinSeverity {
			t.Fatalf("BodyEffectTable 未按严重度降序: 第 %d 行", i)
		}
	}
}

// TestBodyEffect_Monotone 越重越慢 / 越短 / 配额越低 (跨整条 severity 轴)。
func TestBodyEffect_Monotone(t *testing.T) {
	onset := time.Date(2026, 3, 1, 9, 0, 0, 0, time.Local)
	course := 100 * time.Hour
	var prevDelay, prevLen float64
	var prevQuota int
	first := true
	for _, f := range []float64{0.0, 0.25, 0.45, 0.65, 0.85, 1.5} {
		b := bodyAt(t, onset.Format(bodyTSLayout), 100)
		e := b.Effect(onset.Add(time.Duration(f * float64(course))))
		if first {
			prevDelay, prevLen, prevQuota, first = e.DelayFactor, e.LengthFactor, e.QuotaDelta, false
			continue
		}
		// 时间往前走 = 病在好: 延迟只会缩短, 长度只会回升, 配额只会回来。
		// ⚠️ 方向别写反 —— 正着写会"通过", 只有反着写才测得出恢复曲线。
		if e.DelayFactor > prevDelay {
			t.Fatalf("进度 %.2f: 越好反而越慢 (%.2f -> %.2f)", f, prevDelay, e.DelayFactor)
		}
		if e.LengthFactor < prevLen {
			t.Fatalf("进度 %.2f: 越好反而越短 (%.2f -> %.2f)", f, prevLen, e.LengthFactor)
		}
		if e.QuotaDelta < prevQuota {
			t.Fatalf("进度 %.2f: 越好配额反而更低 (%d -> %d)", f, prevQuota, e.QuotaDelta)
		}
		prevDelay, prevLen, prevQuota = e.DelayFactor, e.LengthFactor, e.QuotaDelta
	}
}

// TestBodyEffect_SevereDayCanVanish 重感冒 = 配额 -2 ⇒ 当天 0 次主动开场
// (设计稿的"允许整天不出现"由配额落地, 不需要额外的闸门)。
func TestBodyEffect_SevereDayCanVanish(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.Local)
	b := bodyAt(t, now.Format(bodyTSLayout), 72)
	e := b.Effect(now)
	if !e.MayVanish || e.QuotaDelta > -2 {
		t.Fatalf("最重档应允许整天不出现, 得到 %+v", e)
	}
	cfg := SchedulerConfig{ActiveQuotaDay: 2, MoodLowWillingness: 0.45, OpenMinGapMin: 240}
	in := TickInput{Now: now, HadSlot: true, State: StateFree, OpenJitterRoll: 0,
		HasImpulse: true, OpenImpulse: 0.9, HasBody: true, BodyQuotaDelta: e.QuotaDelta}
	if got := DecideTick(in, cfg); got.Kind != ActNone {
		t.Fatalf("重感冒当天不该主动开场, 得到 %s (%s)", got.Kind, got.Reason)
	}
}

// ——— §4.2 硬线 ———

// TestBodySolicitHits_RequestsOnly 反面: 只抓"索取", 不抓"陈述"。
// 误判会把它正常的示弱也打掉 —— 那是另一种不像人。
func TestBodySolicitHits_RequestsOnly(t *testing.T) {
	allowed := []string{
		"今天不太舒服 早点睡",
		"嗯 有点感冒 你别担心",
		"嗓子有点哑 少说两句",
		"我今天不想聊", // §4.3 拒绝权: 它是被允许的
	}
	for _, s := range allowed {
		if hits := BodySolicitHits(s); len(hits) > 0 {
			t.Fatalf("这句是允许的, 却被判违规: %q → %v", s, hits)
		}
	}
	bad := []string{
		"你能不能安慰我一下",
		"你怎么都不问问我",
		"抱抱我哄哄我嘛",
		"我好可怜 可怜可怜我",
	}
	for _, s := range bad {
		if hits := BodySolicitHits(s); len(hits) == 0 {
			t.Fatalf("这句是索取安慰, 却没认出来: %q", s)
		}
	}
}

// ——— 接线: §4.2 身体必须真的进提示词 ———
//
// ⚠️ 这一条是本批最容易被漏掉的: BodyLine 写好了、单测也过了, 但**没人调用它** ——
// 库里有个 severity, 提示词里一个字都没有, 表现上就是"这一层根本不存在"。
// 单测证明函数对, 证明不了**它被接上了**; 这条测的就是"接上了"。

func bodyWiredEngine() (*DialogEngine, *PersonaProfile) {
	return &DialogEngine{}, &PersonaProfile{Name: "小夜", Template: "bestie"}
}

func TestBuildSystem_BodyReachesPrompt(t *testing.T) {
	d, p := bodyWiredEngine()
	now := clock.Now()
	b := BodyState{Kind: BodyCold, Cause: "昨天淋了雨", Note: "嗓子有点哑",
		OnsetTS: now.Format(bodyTSLayout), CourseH: 72}
	b.Refresh(now)
	if !b.Active {
		t.Fatal("前置: 身体状态应当是 Active")
	}
	sys := d.buildSystem(DialogInput{UserText: "在吗", Body: &b}, p, "ja")
	for _, must := range []string{"不要主动说自己不舒服", "不许要安慰", "不许反复说", "不许给医疗建议"} {
		if !strings.Contains(sys, must) {
			t.Fatalf("身体段没进提示词, 缺 %q —— 机制等于没接线", must)
		}
	}
}

// 反面: **没病时提示词必须逐字节不变**。
//
// 改坏成: 把注入写成"无条件注入"(例如忘了判 Active) → 这条红。
// 身体段常驻的后果是"它天天在暗示自己不舒服", 比不接还糟。
func TestBuildSystem_NoBodyNoSegment(t *testing.T) {
	d, p := bodyWiredEngine()
	now := clock.Now()

	// 已过尾巴期: Refresh 后 Active=false
	dead := BodyState{Kind: BodyCold, Note: "嗓子有点哑",
		OnsetTS: now.AddDate(0, 0, -30).Format(bodyTSLayout), CourseH: 72}
	dead.Refresh(now)
	if dead.Active {
		t.Fatal("前置: 这条应当在尾巴期之外")
	}
	withDead := d.buildSystem(DialogInput{UserText: "在吗", Body: &dead}, p, "ja")
	without := d.buildSystem(DialogInput{UserText: "在吗"}, p, "ja")
	if withDead != without {
		t.Fatalf("过期身体状态改变了提示词 —— 身体段必须是「没病就一个字都不注入」")
	}
	if strings.Contains(without, "不舒服") {
		t.Fatal("没病却出现了身体段")
	}
}

// TestBodyLine_HardlineIsStated 注入段必须**把硬线写进去**, 且不提供可聊的素材。
func TestBodyLine_HardlineIsStated(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.Local)
	b := bodyAt(t, now.Format(bodyTSLayout), 72)
	line := b.Line(now)
	if line == "" {
		t.Fatal("病中必须注入")
	}
	for _, must := range []string{"不要主动说自己不舒服", "不许要安慰", "不许反复说", "不许给医疗建议"} {
		if !strings.Contains(line, must) {
			t.Fatalf("注入段缺硬线约束 %q: \n%s", must, line)
		}
	}
	// 反面: 没病就一个字都不注入 —— 否则"身体"会变成一个常驻话题。
	var none BodyState
	if l := none.Line(now); l != "" {
		t.Fatalf("没病不该注入, 得到: \n%s", l)
	}
	// 尾巴期: 提这件事本身就是违规, 所以注入段必须说"不要提"。
	tailB := bodyAt(t, now.Format(bodyTSLayout), 10)
	tailLine := tailB.Line(now.Add(11 * time.Hour))
	if !strings.Contains(tailLine, "不要提这件事") {
		t.Fatalf("尾巴期注入段必须禁止提它: \n%s", tailLine)
	}
}

// ——— 周期型 ———

// TestBodyCycleEpisode_AbsolutePhase 反面: 周期必须是**绝对相位**, 不能从上一次往后推 ——
// 后者会让经期随病程与冷却的抖动整体漂移。
func TestBodyCycleEpisode_AbsolutePhase(t *testing.T) {
	anchor := time.Date(2026, 3, 1, 12, 0, 0, 0, time.Local)
	c := BodyCycle{Enabled: true, Days: 28, Length: 5}
	// 第 0 天命中, 第 1..27 天不命中, 第 28 天再命中。
	for d := 0; d < 56; d++ {
		_, ok := BodyCycleEpisode(c, anchor, anchor.AddDate(0, 0, d))
		want := d%28 == 0
		if ok != want {
			t.Fatalf("第 %d 天: 命中=%v, 期望 %v", d, ok, want)
		}
	}
	// 同一天问一百次都是同一个答案 (tick 每分钟路过)。
	day3 := anchor.AddDate(0, 0, 1).Add(7 * time.Hour)
	for i := 0; i < 100; i++ {
		if _, ok := BodyCycleEpisode(c, anchor, day3); ok {
			t.Fatal("非首日不该命中")
		}
	}
	ep, _ := BodyCycleEpisode(c, anchor, anchor)
	if ep.Course() != 5*24*time.Hour {
		t.Fatalf("经期病程应为 5 天, 得到 %v", ep.Course())
	}
}

// TestBodyCycleEpisode_Disabled 反面: 关掉就是关掉 —— 关掉时必须**一次都不产生**,
// 而不是靠概率小到看不见 (那正是"永不触发的闸门").
func TestBodyCycleEpisode_Disabled(t *testing.T) {
	anchor := time.Date(2026, 3, 1, 12, 0, 0, 0, time.Local)
	for d := 0; d < 400; d++ {
		if _, ok := BodyCycleEpisode(BodyCycle{Enabled: false}, anchor, anchor.AddDate(0, 0, d)); ok {
			t.Fatalf("第 %d 天: 已关闭却仍产生周期事件", d)
		}
	}
	// 默认值 (零值) = 关闭 —— 未指定性别的人格不加戏。
	if _, ok := BodyCycleEpisode(BodyCycle{}, anchor, anchor); ok {
		t.Fatal("零值 BodyCycle 必须是关闭")
	}
}

// ——— 事件型起因 ———

// TestBodyColdProb_WeatherDriven 概率只由**看得见的因**决定 (天气 / 温度)。
func TestBodyColdProb_WeatherDriven(t *testing.T) {
	warm := BodyColdProb(EnvDay{Weather: "晴", TempC: 24})
	cold := BodyColdProb(EnvDay{Weather: "晴", TempC: 2})
	rain := BodyColdProb(EnvDay{Weather: "小雨", TempC: 2})
	if !(warm < cold && cold <= rain) {
		t.Fatalf("概率没随天气恶化单调上升: 晴24=%d 晴2=%d 雨2=%d", warm, cold, rain)
	}
	if rain > 15 {
		t.Fatalf("概率上限失守: %d", rain)
	}
	if warm < 1 {
		t.Fatalf("晴天也该有非零概率, 得到 %d", warm)
	}
}

// TestBodyOnsetFromEnv_BoozeBeatsCold 反面: 有**明确外部证据**(酒局)时必须优先,
// 否则"昨天喝了酒"这条因永远被概率更高的感冒吃掉。
func TestBodyOnsetFromEnv_BoozeBeatsCold(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.Local)
	env := EnvDay{Weather: "暴雨", TempC: -2, Events: []string{"昨晚朋友聚餐"}}
	// 骰子全部打在"必然命中"的位置。
	ep, ok := BodyOnsetFromEnv(env, BodyRoll{Cold: 0, Insomnia: 0, Booze: 0}, now)
	if !ok || ep.Kind != BodyHangover {
		t.Fatalf("有酒局证据时应判宿醉, 得到 %+v ok=%v", ep, ok)
	}
	// 没有酒局 → 同样的骰子落到感冒。
	env2 := EnvDay{Weather: "暴雨", TempC: -2}
	ep2, ok2 := BodyOnsetFromEnv(env2, BodyRoll{Cold: 0, Insomnia: 0, Booze: 0}, now)
	if !ok2 || ep2.Kind != BodyCold {
		t.Fatalf("无酒局应判感冒, 得到 %+v ok=%v", ep2, ok2)
	}
	// 反面: 三个骰子都打满 → 不发作。
	env3 := EnvDay{Weather: "晴", TempC: 22}
	if _, ok3 := BodyOnsetFromEnv(env3, BodyRoll{Cold: 99, Insomnia: 99, Booze: 99}, now); ok3 {
		t.Fatal("骰子全打满不该发作")
	}
}

// TestBodyResting_Cooldown 反面: 冷却期内不再发作 —— 概率是"每天掷一次", 而人不会连着感冒。
func TestBodyResting_Cooldown(t *testing.T) {
	onset := time.Date(2026, 3, 1, 9, 0, 0, 0, time.Local)
	b := bodyAt(t, onset.Format(bodyTSLayout), 72)
	if b.Resting(onset.Add(24 * time.Hour)) {
		t.Fatal("病程第 2 天不该算'休息好了'")
	}
	end := onset.Add(72*time.Hour + time.Duration(BodyTailHours)*time.Hour)
	if b.Resting(end.Add(24 * time.Hour)) {
		t.Fatal("尾巴刚走完还在冷却期内")
	}
	if !b.Resting(end.AddDate(0, 0, BodyCooldownDays+1)) {
		t.Fatal("冷却走完后应可再次发作")
	}
	// 空记录 = 从没病过 = 随时可以发作。
	var none BodyState
	if !none.Resting(onset) {
		t.Fatal("空记录必须是可发作状态")
	}
}

// ——— 持久化 ———

// TestSaveBodyEpisode_ActuallyPersists 只断言"写了"会漏掉读取侧的同类错误 ——
// 必须同时断言**存得进**且**读得出** (P7 的教训: 静默的不是数据库, 是调用方)。
func TestSaveBodyEpisode_ActuallyPersists(t *testing.T) {
	s := newTestStore(t)
	onset := time.Date(2026, 3, 1, 9, 0, 0, 0, time.Local)
	if err := s.SaveBodyEpisode(BodyCold, "昨天淋了雨", "嗓子有点哑", onset, 72*time.Hour); err != nil {
		t.Fatalf("写入失败 (列名与值的个数对不上就是这种错): %v", err)
	}
	got, err := s.LoadBody()
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got.Kind != BodyCold || got.Cause != "昨天淋了雨" || got.Note != "嗓子有点哑" {
		t.Fatalf("读回来的不是写进去的: %+v", got)
	}
	if got.OnsetTS != onset.Format(bodyTSLayout) {
		t.Fatalf("发作时刻不一致: %q", got.OnsetTS)
	}
	if got.CourseH != 72 {
		t.Fatalf("病程不一致: %v", got.CourseH)
	}
	// 严重度**不许**入库 —— 它是现算的。
	got.Refresh(onset.Add(10 * time.Hour))
	if got.Severity != BodySeverityLadder[0] {
		t.Fatalf("现算严重度不对: %v", got.Severity)
	}
	// 覆盖写: 同 id 只有一行。
	if err := s.SaveBodyEpisode(BodyInsomnia, "昨晚没睡好", "有点犯困", onset, 24*time.Hour); err != nil {
		t.Fatalf("覆盖写失败: %v", err)
	}
	again, _ := s.LoadBody()
	if again.Kind != BodyInsomnia {
		t.Fatalf("覆盖写没生效: %+v", again)
	}
}

// TestDayRoll_Deterministic 日骰必须**一天之内恒定** ——
// 用 Dice 逐 tick 掷会让行为闪烁。
func TestDayRoll_Deterministic(t *testing.T) {
	a := DayRoll("2026-03-01", 100)
	for i := 0; i < 50; i++ {
		if DayRoll("2026-03-01", 100) != a {
			t.Fatal("同一个 key 必须给出同一个值")
		}
	}
	if a < 0 || a >= 100 {
		t.Fatalf("越界: %d", a)
	}
	if DayRoll("x", 0) != 0 {
		t.Fatal("n<=0 应返回 0")
	}
	// 不同日子不该全部相同 (不要求分布均匀, 只要求真的在动)。
	seen := map[int]bool{}
	for d := 1; d <= 28; d++ {
		seen[DayRoll(time.Date(2026, 3, d, 0, 0, 0, 0, time.Local).Format("2006-01-02"), 100)] = true
	}
	if len(seen) < 10 {
		t.Fatalf("28 天只产生 %d 个不同的值 — 不像在动", len(seen))
	}
}
