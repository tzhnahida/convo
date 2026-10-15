package convo

import (
	"testing"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════════
// §4.3 注意力与疲劳 —— 叶子模块的测试。
//
// 这里的**主体是反面用例**: 每一条契约都配一句"改坏成 X → 这条会红", 写在断言旁边。
// 正面用例只负责证明"机制确实会动" —— 没有反面用例的正面用例, 测的是同义反复。
//
// 辅助函数一律带 fatigueTest 前缀 (合并到主分支时不会与别的模块撞名)。
// ═══════════════════════════════════════════════════════════════════════════

// fatigueTestStore 开一个临时库。**故意不建疲劳表** —— 建表要走 EnsureFatigueSchema,
// 那条路径本身也要测 (TestLoadInterruption_WithoutEnsureIsLoud)。
func fatigueTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(t.TempDir(), "fatigue-test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// fatigueTestEnsuredStore 开库 + 建表 (模拟主控已在 migrate 里接过一次之后的库)。
func fatigueTestEnsuredStore(t *testing.T) *Store {
	t.Helper()
	s := fatigueTestStore(t)
	if err := EnsureFatigueSchema(s); err != nil {
		t.Fatalf("EnsureFatigueSchema: %v", err)
	}
	return s
}

// fatigueTestWeek 一张合法的周日程: 00:00-07:30 sleep / 07:30-12:00 focus /
// 12:00-14:00 free / 14:00-18:00 busy / 18:00-23:30 slack / 23:30-24:00 sleep。
// 用 ValidateWeekSchedule 自检, 免得整批测试建立在一张不合法的日程上。
func fatigueTestWeek(t *testing.T) *WeekSchedule {
	t.Helper()
	slots := []Slot{
		{Start: "00:00", End: "07:30", State: StateSleep},
		{Start: "07:30", End: "12:00", State: StateFocus},
		{Start: "12:00", End: "14:00", State: StateFree},
		{Start: "14:00", End: "18:00", State: StateBusy},
		{Start: "18:00", End: "23:30", State: StateSlack},
		{Start: "23:30", End: "24:00", State: StateSleep},
	}
	ws := &WeekSchedule{SleepStart: "23:30", SleepEnd: "07:30"}
	for _, d := range dayOrder {
		ws.Days = append(ws.Days, DaySchedule{Day: d, Slots: append([]Slot(nil), slots...)})
	}
	if err := ValidateWeekSchedule(ws); err != nil {
		t.Fatalf("测试日程本身不合法: %v", err)
	}
	return ws
}

// fatigueTestMonday 2026-03-02 是周一 (dayOrder[0]) —— 日程查表靠这个前提。
func fatigueTestMonday(h, m int) time.Time {
	at := time.Date(2026, 3, 2, h, m, 0, 0, time.Local)
	if weekdayKey(at) != "mon" {
		panic("测试前提不成立: 2026-03-02 不是周一")
	}
	return at
}

func fatigueTestCfg() SchedulerConfig {
	return SchedulerConfig{ActiveQuotaDay: 2, MoodLowWillingness: 0.45}
}

// ───────────────────────────────────────────────────────────────────────────
// 正面: 机制确实会动
// ───────────────────────────────────────────────────────────────────────────

func TestDeclinePermit_LowMoodHighArousalAllows(t *testing.T) {
	ok, reason := DeclinePermit(DeclineInput{
		Valence: -0.40, Arousal: 0.80, Tier: "A", State: StateFree,
	}, fatigueTestCfg())
	if !ok {
		t.Fatalf("低心情 + 高唤醒应当给「明说不想聊」的许可, 得到: %s", reason)
	}
	if reason == "" {
		t.Fatal("许可必须带依据 —— 归因 (今天为什么允许) 全靠这句")
	}
}

func TestDeclinePermit_BodyGroundMergesIntoOneExit(t *testing.T) {
	// 身体许可 (body.go:262) 与心情判据必须走**同一个出口** (C1):
	// 心情完全没到门槛, 但身体说可以 —— 结论仍然是可以。
	ok, reason := DeclinePermit(DeclineInput{
		Valence: 0.5, Arousal: 0.5, BodyDecline: true, State: StateFree,
	}, fatigueTestCfg())
	if !ok {
		t.Fatalf("身体许可没被合并进来: %s", reason)
	}
}

func TestDeclinePermit_FatigueBoostLowersArousalBar(t *testing.T) {
	in := DeclineInput{Valence: -0.30, Arousal: 0.55, State: StateFree} // 低心情, 唤醒卡在中间
	in.FatigueTier = 0
	if ok, reason := DeclinePermit(in, fatigueTestCfg()); ok {
		t.Fatalf("档 0 不该下移门槛: %s", reason)
	}
	in.FatigueTier = FatigueBoostTier
	ok, reason := DeclinePermit(in, fatigueTestCfg())
	if !ok {
		t.Fatalf("疲劳档 %d 应把唤醒门槛下移到 %.2f: %s", FatigueBoostTier, DeclineFatigueArousalMin, reason)
	}
}

func TestDeclineHits_RecognizesSpokenDeclineOnly(t *testing.T) {
	if hits := DeclineHits("今天不想聊 我先去忙了"); len(hits) == 0 {
		t.Fatal("明说的拒绝必须被认出来 (当日 declines 计数靠它)")
	}
	// 判据是**收窄**的: 普通的推脱/忙碌不是行使拒绝权。
	if hits := DeclineHits("等下说 我在忙"); len(hits) != 0 {
		t.Fatalf("普通推脱被误判成拒绝权: %v", hits)
	}
}

func TestDecideFatigue_LadderFromBothReadings(t *testing.T) {
	cases := []struct {
		name string
		in   FatigueInput
		want int
	}{
		{"零值 = 不疲劳", FatigueInput{}, 0},
		{"日程上忙 2 点", FatigueInput{BusyMinutes: 300}, 1},
		{"日程上忙一整天", FatigueInput{BusyMinutes: 480}, 2},
		{"只有社交上忙 (轮次)", FatigueInput{RoundsToday: 45}, 1},
		{"只有社交上忙 (消息)", FatigueInput{UserMsgsToday: 120}, 1},
		{"两种读法都很忙", FatigueInput{BusyMinutes: 480, RoundsToday: 80, UserMsgsToday: 100}, 3},
	}
	for _, c := range cases {
		tier, reason := DecideFatigue(c.in)
		if tier != c.want {
			t.Fatalf("%s: 档 = %d, 想要 %d (%s)", c.name, tier, c.want, reason)
		}
		if reason == "" {
			t.Fatalf("%s: 档位必须带归因理由", c.name)
		}
	}
	// "两种读法都实现"的证明: 社交侧**单独**也能抬档 (只实现日程侧会在这里红)。
	if tier, _ := DecideFatigue(FatigueInput{RoundsToday: 90, UserMsgsToday: 150}); tier < 2 {
		t.Fatalf("社交上忙单独抬不动档 (档 %d) —— 规格 §7.3 要求两种读法都算", tier)
	}
}

func TestBusyMinutesToday_CountsElapsedFocusAndBusy(t *testing.T) {
	ws := fatigueTestWeek(t)
	// 15:00: focus 07:30-12:00 全过 = 270; busy 14:00-18:00 过了 60 → 330。
	// free/slack/sleep 一分钟都不算。
	if got := BusyMinutesToday(ws, fatigueTestMonday(15, 0)); got != 330 {
		t.Fatalf("15:00 的已忙分钟 = %d, 想要 330", got)
	}
	// 08:00: focus 只过了 30 分钟, busy 还没开始。
	if got := BusyMinutesToday(ws, fatigueTestMonday(8, 0)); got != 30 {
		t.Fatalf("08:00 的已忙分钟 = %d, 想要 30", got)
	}
}

func TestInterrupt_RoundTripAndClear(t *testing.T) {
	s := fatigueTestEnsuredStore(t)
	if _, ok, err := s.LoadInterruption(); err != nil || ok {
		t.Fatalf("空库应当是「没有中断」 (ok=false, err=nil), 得到 ok=%v err=%v", ok, err)
	}
	it := Interruption{
		AnchorTS: "2026-03-02 14:00:00",
		Reason:   "等下 我拿个外卖",
		DueTS:    "2026-03-02 14:10:00",
	}
	if err := s.SaveInterruption(it); err != nil {
		t.Fatal(err)
	}
	back, ok, err := s.LoadInterruption()
	if err != nil || !ok {
		t.Fatalf("存了读不回来: ok=%v err=%v", ok, err)
	}
	if back != it {
		t.Fatalf("往返丢字段: %+v != %+v", back, it)
	}
	if err := s.ClearInterruption(); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.LoadInterruption(); err != nil || ok {
		t.Fatalf("清掉之后还在: ok=%v err=%v", ok, err)
	}
}

func TestInterrupt_SuspendedAndDue(t *testing.T) {
	it := Interruption{AnchorTS: "2026-03-02 14:00:00", DueTS: "2026-03-02 14:10:00"}
	before := time.Date(2026, 3, 2, 14, 5, 0, 0, time.Local)
	after := time.Date(2026, 3, 2, 14, 10, 0, 0, time.Local)
	if !it.SuspendedAt(before) {
		t.Fatal("14:05 应当处于挂起态")
	}
	if it.DueAt(before) {
		t.Fatal("还没到点就触发恢复 —— 恢复的落点是时钟, 不是「存了就算到点」")
	}
	if !it.DueAt(after) || it.SuspendedAt(after) {
		t.Fatal("14:10 应当到点且不再挂起")
	}
	done := it
	done.ResumeTS = "2026-03-02 14:11:00"
	if done.SuspendedAt(after) || done.DueAt(after) {
		t.Fatal("已恢复的中断不许再触发")
	}
}

func TestEnsureFatigueSchema_Idempotent(t *testing.T) {
	s := fatigueTestStore(t)
	for i := 0; i < 3; i++ { // 老库升级会重复走这条路, 幂等是硬要求
		if err := EnsureFatigueSchema(s); err != nil {
			t.Fatalf("第 %d 次建表失败: %v", i+1, err)
		}
	}
	if err := EnsureFatigueSchema(nil); err == nil {
		t.Fatal("nil store 必须报错, 不许静默成功")
	}
}

// ───────────────────────────────────────────────────────────────────────────
// 反面: 逐条 "改坏成 X → 这条红"
// ───────────────────────────────────────────────────────────────────────────

// 改坏成: 判据写成"唤醒**低于**门槛就给许可" (或"零值当成烦躁") → 红。
// 这条钉的是本层最容易做歪的一处: 零值 = 未提供 ≠ 极值。
func TestDeclinePermit_ZeroValueDenies(t *testing.T) {
	if ok, reason := DeclinePermit(DeclineInput{}, fatigueTestCfg()); ok {
		t.Fatalf("零值输入拿到了拒绝许可 (零值被当成了极值): %s", reason)
	}
	// Arousal = 0 是**低**唤醒那一侧: 低心情 + 低唤醒 = 低落型, 走"慢/短/少表情",
	// 不走"明说不想聊"。把判据写成 arousal < min 的实现会在这里许可 → 红。
	if ok, reason := DeclinePermit(DeclineInput{Valence: -0.9, Arousal: 0, State: StateFree}, fatigueTestCfg()); ok {
		t.Fatalf("低落型拿到了「明说不想聊」的许可: %s", reason)
	}
}

// 改坏成: 只看心情低 (漏掉高唤醒), 或拿 Willingness 阈值当判据 → 红。
func TestDeclinePermit_RequiresBothLowValenceAndHighArousal(t *testing.T) {
	now := time.Now()
	low := MoodState{Valence: -0.5, Arousal: 0.2}  // 低落型
	high := MoodState{Valence: -0.5, Arousal: 0.9} // 烦躁型
	// 自证前提: Willingness 里 arousal 是**正项** —— 高唤醒的意愿反而更高。
	if low.Willingness(now) >= high.Willingness(now) {
		t.Fatalf("前提不成立: Willingness 里 arousal 应当是正项 (%.3f vs %.3f)",
			low.Willingness(now), high.Willingness(now))
	}
	// 所以拒绝许可必须与意愿**反向**: 低唤醒不给, 高唤醒才给。
	// 把判据接在 Willingness 阈值上的实现, 这两条断言必然一条红。
	if ok, reason := DeclinePermit(DeclineInput{Valence: -0.5, Arousal: 0.2, State: StateFree}, fatigueTestCfg()); ok {
		t.Fatalf("低唤醒(低落)不该拿到许可: %s", reason)
	}
	if ok, reason := DeclinePermit(DeclineInput{Valence: -0.5, Arousal: 0.9, State: StateFree}, fatigueTestCfg()); !ok {
		t.Fatalf("高唤醒(烦躁)应当拿到许可: %s", reason)
	}
}

// 改坏成: 拒绝权一天可以用多次 (比如只查心情不查计数) → 红。
func TestDeclinePermit_OncePerDay(t *testing.T) {
	used := DeclineInput{
		Valence: -0.9, Arousal: 1.0, BodyDecline: true,
		FatigueTier: FatigueBoostTier, UsedToday: true, State: StateFree,
	}
	if ok, reason := DeclinePermit(used, fatigueTestCfg()); ok {
		t.Fatalf("当天已用过还给许可 (配额 1/天): %s", reason)
	}
}

// 改坏成: 睡眠窗内也给许可 (睡着被当成拒绝) → 红。
func TestDeclinePermit_NotInSleepWindow(t *testing.T) {
	asleep := DeclineInput{
		Valence: -0.9, Arousal: 1.0, BodyDecline: true,
		FatigueTier: FatigueBoostTier, State: StateSleep,
	}
	if ok, reason := DeclinePermit(asleep, fatigueTestCfg()); ok {
		t.Fatalf("睡眠窗内给了许可 —— 那是睡着, 不是拒绝: %s", reason)
	}
}

// 改坏成: C 层 (安全/求助) 也被允许拒绝 → 红 (硬线)。
func TestDeclinePermit_SafetyNeverDeclined(t *testing.T) {
	in := DeclineInput{
		Valence: -0.9, Arousal: 1.0, BodyDecline: true,
		FatigueTier: FatigueBoostTier, Tier: DeclineTierSafety, State: StateFree,
	}
	if ok, reason := DeclinePermit(in, fatigueTestCfg()); ok {
		t.Fatalf("C 层拿到了拒绝许可 (硬线破了): %s", reason)
	}
}

// 改坏成: 把 Tier == "" (未分类) 也当成 C 层 → 红。
// 零值 = 未提供 ≠ 极值 (纪律 1): ClassifyRequest 的契约是"判不准 → TierSafety",
// 所以生产路径上永远不会递进来 ""; 把 "" 当 C 层反而是"未提供当成极值"。
func TestDeclinePermit_UnclassifiedTierIsNotSafety(t *testing.T) {
	in := DeclineInput{Valence: -0.6, Arousal: 0.9, Tier: "", State: StateFree}
	if ok, reason := DeclinePermit(in, fatigueTestCfg()); !ok {
		t.Fatalf("未分类的层级被当成了 C 层: %s", reason)
	}
}

// 改坏成: 阈值直接用 0 (DeclineArousalMin=0 → 拒绝权天天 armed) → 红。
// 规格 §3 明写: 这两个配置项的**零值必须回落到默认值, 不能用 0**。
func TestDeclinePermit_ZeroThresholdFallsBack(t *testing.T) {
	oldV, oldA := DeclineValenceMax, DeclineArousalMin
	defer func() { DeclineValenceMax, DeclineArousalMin = oldV, oldA }()
	DeclineValenceMax, DeclineArousalMin = 0, 0

	// 心情只低一点点 (-0.05), 唤醒很高 —— 用 0 当门槛的实现会给许可 → 红。
	in := DeclineInput{Valence: -0.05, Arousal: 0.9, State: StateFree}
	if ok, reason := DeclinePermit(in, fatigueTestCfg()); ok {
		t.Fatalf("阈值零值没有回落 —— 0 被当成了门槛: %s", reason)
	}
	// 回落之后原本该给的仍然给 (证明回落的不是"一律不给")。
	strong := DeclineInput{Valence: -0.4, Arousal: 0.9, State: StateFree}
	if ok, reason := DeclinePermit(strong, fatigueTestCfg()); !ok {
		t.Fatalf("回落过头了, 正常判据被压死: %s", reason)
	}
}

// 改坏成: 判据里引入骰子/概率 (比如"烦躁时 60% 允许") → 红。
func TestDeclinePermit_Deterministic(t *testing.T) {
	in := DeclineInput{Valence: -0.5, Arousal: 0.9, State: StateFree}
	ok0, r0 := DeclinePermit(in, fatigueTestCfg())
	for i := 0; i < 100; i++ {
		ok, r := DeclinePermit(in, fatigueTestCfg())
		if ok != ok0 || r != r0 {
			t.Fatalf("第 %d 次结果不同 —— 决策器里进了随机: %v/%q vs %v/%q", i, ok, r, ok0, r0)
		}
	}
}

// 改坏成: armed 时仍走"排延迟回复然后 return"(装死) → 红。
// 接线在 dialog/tick (主控做), 这里钉的是它该用的**那条判据**。
func TestDeclineForcesImmediate_ExceptSleep(t *testing.T) {
	for _, st := range []string{StateFree, StateSlack, StateFocus, StateBusy, ""} {
		if !DeclineForcesImmediate(st) {
			t.Fatalf("槽 %q: armed 的拒绝权必须把这一轮顶成即时, 否则「明说」发不出去", st)
		}
	}
	if DeclineForcesImmediate(StateSleep) {
		t.Fatal("睡眠窗不该被顶成即时 —— 睡着的静默不是拒绝")
	}
}

// ⭐ 改坏成: 给疲劳加一个配额减项 (第三个"今天不说话"的诱因) → 红。
//
// 这条是本模块最要紧的一条: 配额已经被心情 -1 与身体 -2..0 减了两轮,
// 再加疲劳, "今天为什么不说话"就有三个可能的原因, 归因直接失效 (规格 C4)。
func TestFatigue_DoesNotCutQuota(t *testing.T) {
	// 两种背景都要跑:
	//   pressed —— 已经被心情/身体压到 0。它复现真实处境, 但**夹紧会把小改动吃掉**,
	//              所以光看它是不够的 (疲劳偷偷 -1 仍然得到 0, 测试会假绿);
	//   roomy   —— 有富余 (配额满 2)。任何偷偷的减项在这里都藏不住。
	pressed := fatigueTestQuotaInputs{
		ActiveQuotaDay: 2, MoodLowWillingness: 0.45,
		MoodWillingness: 0.30, // 心情低 → -1
		HasBody:         true,
		BodyQuotaDelta:  -1, // 身体 → -1
	}
	roomy := fatigueTestQuotaInputs{
		ActiveQuotaDay: 2, MoodLowWillingness: 0.45,
		MoodWillingness: 0.90, // 心情好、没病 → 配额满
	}
	if raw, q := fatigueTestQuota(pressed); raw != 0 || q != 0 {
		t.Fatalf("前提不成立: pressed = (%d,%d), 想要 (0,0)", raw, q)
	}
	if raw, q := fatigueTestQuota(roomy); raw != 2 || q != 2 {
		t.Fatalf("前提不成立: roomy = (%d,%d), 想要 (2,2)", raw, q)
	}

	for _, base := range []fatigueTestQuotaInputs{pressed, roomy} {
		wantRaw, wantQuota := fatigueTestQuota(base)
		for tier := 0; tier <= 3; tier++ {
			fx := FatigueEffectsFor(tier)
			// ① 档位表不许带配额项
			if fx.QuotaDelta != 0 {
				t.Fatalf("档 %d 带上了配额减项 %d —— 疲劳变成第三个减配额项", tier, fx.QuotaDelta)
			}
			// ② 通向配额的唯一接口必须返回「没有压力」
			if d, has := FatigueQuotaProjection(fx); d != 0 || has {
				t.Fatalf("档 %d 的配额投影非空: (%d, %v)", tier, d, has)
			}
			// ③ 配额计算输入**逐位相同**
			if got := fatigueTestQuotaWith(base, fx); got != base {
				t.Fatalf("档 %d 改动了配额输入: %+v != %+v", tier, got, base)
			}
			// ④ 配额本体也必须一样 —— 夹紧前那个数一起看, 夹紧就掩盖不了减项
			raw, q := fatigueTestQuota(fatigueTestQuotaWith(base, fx))
			if raw != wantRaw || q != wantQuota {
				t.Fatalf("档 %d 改动了配额: (%d,%d) != (%d,%d) —— 疲劳的落点是延迟/拒绝权, 不是配额",
					tier, raw, q, wantRaw, wantQuota)
			}
		}
	}

	// ⑤ 手搓的 struct 也得被 Apply 夹回去 (绕过档位表也走私不进来)
	forged := FatigueEffects{Tier: 3, DelayFactor: 1.25, DeclineBoost: true, QuotaDelta: -1}.
		Apply(fatigueTestMonday(15, 0))
	if forged.QuotaDelta != 0 {
		t.Fatalf("Apply 没有夹掉手搓的配额项: %d", forged.QuotaDelta)
	}

	// ⑥ 灵敏度自检: 上面那组「逐位相同」**不是空的** ——
	// 真把疲劳接进配额 (哪怕是伪造的输入), 夹紧前的配额就变了, ③④ 立刻红。
	bogus := roomy
	bogus.FatigueDelta, bogus.HasFatiguePressure = -1, true
	if raw, _ := fatigueTestQuota(bogus); raw == 2 {
		t.Fatal("配额表达式对疲劳不敏感 —— 这条测试是空的")
	}
}

// 改坏成: FatigueTier 零值被当成最高档 (或档位定义成 1..3) → 红。
func TestFatigue_ZeroValueIsNormalPath(t *testing.T) {
	if tier, _ := DecideFatigue(FatigueInput{}); tier != 0 {
		t.Fatalf("零值输入得到档 %d, 想要 0", tier)
	}
	fx := FatigueEffects{}.Apply(fatigueTestMonday(15, 0))
	if fx.DelayFactor != 1.0 || fx.QuotaDelta != 0 || fx.DeclineBoost || fx.Tier != 0 {
		t.Fatalf("零值 FatigueEffects 不是「照常」: %+v", fx)
	}
	// 档位表里没有的档位 (负数 / 越界) = 未提供 → 也走正常路径。
	for _, tier := range []int{-1, 9, 99} {
		if got := FatigueEffectsFor(tier); got.DelayFactor != 1.0 || got.DeclineBoost {
			t.Fatalf("档 %d 落在表外, 应当回落到「不疲劳」: %+v", tier, got)
		}
	}
}

// 改坏成: 昨天的疲劳沿用到今天 (派生量不现算) → 红。
// 零值口径: Day == "" 是**未提供** → 不判过期 (不是"过期")。
func TestFatigue_StaleDayIsNormalPath(t *testing.T) {
	now := fatigueTestMonday(15, 0)
	stale := FatigueEffectsFor(FatigueBoostTier).WithDay(now.AddDate(0, 0, -1).Format("2006-01-02"))
	if got := stale.Apply(now); got.DelayFactor != 1.0 || got.DeclineBoost {
		t.Fatalf("昨天的疲劳被沿用到今天: %+v", got)
	}
	fresh := FatigueEffectsFor(FatigueBoostTier).WithDay(now.Format("2006-01-02")).Apply(now)
	if fresh.DelayFactor <= 1.0 || !fresh.DeclineBoost {
		t.Fatalf("今天的疲劳必须生效: %+v", fresh)
	}
	// Day 未提供 ≠ 过期 —— 少了这条区分, 忘了填 Day 的调用方会静默失去全部疲劳效果。
	if got := FatigueEffectsFor(FatigueBoostTier).Apply(now); got.DelayFactor <= 1.0 {
		t.Fatalf("Day 未提供被当成了过期: %+v", got)
	}
	// 零值 DelayFactor 必须被夹回 1.0 (否则乘法链会把延迟乘成 0)。
	if got := (FatigueEffects{Tier: 1}).Apply(now); got.DelayFactor != 1.0 {
		t.Fatalf("零值 DelayFactor 没被归一化: %v", got.DelayFactor)
	}
}

// 改坏成: 档位改成每轮微调的连续量 → 红。同一档内输入连续变化, 行为参数逐字段相同。
func TestFatigue_TieredNotContinuous(t *testing.T) {
	a, ra := DecideFatigue(FatigueInput{BusyMinutes: 450})
	b, rb := DecideFatigue(FatigueInput{BusyMinutes: 599})
	if a != b {
		t.Fatalf("同一档内输入微调就换了档: %d vs %d", a, b)
	}
	if ra == rb {
		t.Fatalf("输入变了而理由一字不改 —— 这条测试没在测东西 (%q)", ra)
	}
	if FatigueEffectsFor(a) != FatigueEffectsFor(b) {
		t.Fatalf("同一档的行为参数不相等: %+v vs %+v", FatigueEffectsFor(a), FatigueEffectsFor(b))
	}
}

// 改坏成: 延迟逐档递增 (与身体乘子叠成"常驻慢速") → 红。
func TestFatigue_DelayStepsAtMostOnce(t *testing.T) {
	seen := map[float64]bool{}
	byTier := map[int]fatigueEffectRow{}
	for _, r := range FatigueEffectTable {
		seen[r.DelayFactor] = true
		byTier[r.Tier] = r
		if r.DelayFactor < 1 {
			t.Fatalf("档 %d 的 DelayFactor %v < 1 —— 疲劳只会更慢, 绝不更快", r.Tier, r.DelayFactor)
		}
		// 抬延迟与下移拒绝权门槛各自只在约定的档位之上出现。
		if (r.DelayFactor != 1.0) != (r.Tier >= FatigueDelayTierMin) {
			t.Fatalf("档 %d 的延迟与 FatigueDelayTierMin=%d 不一致: %v", r.Tier, FatigueDelayTierMin, r.DelayFactor)
		}
		if r.DeclineBoost != (r.Tier >= FatigueBoostTier) {
			t.Fatalf("档 %d 的 DeclineBoost 与 FatigueBoostTier=%d 不一致", r.Tier, FatigueBoostTier)
		}
	}
	if len(seen) > 2 {
		t.Fatalf("延迟取值有 %d 种 —— 疲劳对延迟**至多抬一档**: %v", len(seen), seen)
	}
	if _, ok := seen[1.0]; !ok {
		t.Fatal("表里没有 1.0 —— 低档必须是不抬延迟")
	}
}

// 改坏成: 跨午夜的 sleep 段被算进 busy → 红。
func TestBusyMinutesToday_CrossesMidnight(t *testing.T) {
	ws := fatigueTestWeek(t)
	// 00:30 只被睡眠段覆盖 —— 一分钟都不算忙。
	if got := BusyMinutesToday(ws, fatigueTestMonday(0, 30)); got != 0 {
		t.Fatalf("跨午夜的 sleep 被算成忙: %d", got)
	}
	// 23:45: focus 270 + busy 240 = 510; 23:30-24:00 的 sleep 一分钟不算。
	if got := BusyMinutesToday(ws, fatigueTestMonday(23, 45)); got != 510 {
		t.Fatalf("23:45 的已忙分钟 = %d, 想要 510 (sleep 段不许计入)", got)
	}
}

// 改坏成: BusyMinutes 落库成一列 / 带缓存 → 红。
func TestBusyMinutesToday_RecomputedFromSchedule(t *testing.T) {
	ws := fatigueTestWeek(t)
	at := fatigueTestMonday(15, 0)
	if got := BusyMinutesToday(ws, at); got != 330 {
		t.Fatalf("前提不成立: 改之前是 %d, 想要 330", got)
	}
	// 手改日程: 把 busy 段改成 slack。派生量必须**当次调用**就变。
	for i := range ws.Days {
		for j := range ws.Days[i].Slots {
			if ws.Days[i].Slots[j].State == StateBusy {
				ws.Days[i].Slots[j].State = StateSlack
			}
		}
	}
	if got := BusyMinutesToday(ws, at); got != 270 {
		t.Fatalf("改日程之后仍是 %d —— 说明这个值是存下来的, 不是现算的", got)
	}
}

// 改坏成: 日程读失败/nil 时返回"很忙" → 红 (失败往"不疲劳"倒)。
func TestBusyMinutesToday_NilAndBadSlots(t *testing.T) {
	if got := BusyMinutesToday(nil, fatigueTestMonday(15, 0)); got != 0 {
		t.Fatalf("没有日程却报出 %d 分钟忙", got)
	}
	bad := &WeekSchedule{Days: []DaySchedule{{
		Day: "mon",
		Slots: []Slot{
			{Start: "25:00", End: "26:00", State: StateFocus}, // 解析不出 → 跳过
			{Start: "乱写", End: "18:00", State: StateBusy},     // 同上
		},
	}}}
	if got := BusyMinutesToday(bad, fatigueTestMonday(15, 0)); got != 0 {
		t.Fatalf("解析不出的槽被算成了 %d 分钟忙 —— 失败必须往「不疲劳」倒", got)
	}
}

// ⭐ 改坏成: 中断-恢复复用 pending_reply_due 那个单槽 → 红 (规格 C2)。
//
// 单槽的病灶: convo.go:1002 用 INSERT ... ON CONFLICT DO UPDATE 覆盖同一个 key,
// 中断与 "busy 30-90 分钟后的延迟回复" 会互相顶掉, 谁后写谁赢。
func TestInterrupt_CoexistsWithBusyDelayedReply(t *testing.T) {
	s := fatigueTestEnsuredStore(t)
	const busyKey = "pending_reply_due"
	const busyVal = "2026-03-02 15:30:00"
	if err := s.MetaSet(busyKey, busyVal); err != nil {
		t.Fatal(err)
	}
	it := Interruption{
		AnchorTS: "2026-03-02 14:00:00",
		Reason:   "等下 我拿个外卖",
		DueTS:    "2026-03-02 14:10:00",
	}
	if err := s.SaveInterruption(it); err != nil {
		t.Fatal(err)
	}
	// ① 存中断不许动 busy 延迟回复那个 key。
	if got, ok, err := s.MetaGet(busyKey); err != nil || !ok || got != busyVal {
		t.Fatalf("中断顶掉了延迟回复: got=%q ok=%v err=%v", got, ok, err)
	}
	// ② 清中断也不许顺手清掉它。
	if err := s.ClearInterruption(); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := s.MetaGet(busyKey); err != nil || !ok || got != busyVal {
		t.Fatalf("清中断时把延迟回复一起清了: got=%q ok=%v err=%v", got, ok, err)
	}
}

// 改坏成: 零值/读不出的中断被当成"正在挂起" → 红 (整天不开口)。
func TestInterrupt_ZeroValueIsNotSuspended(t *testing.T) {
	now := time.Now()
	zero := Interruption{}
	if zero.SuspendedAt(now) || zero.DueAt(now) {
		t.Fatal("零值中断被当成了挂起/到点 —— 未提供必须走正常路径")
	}
	// 时刻读不出 = 没有这件事, 不是"立刻恢复"。
	bad := Interruption{DueTS: "不是时间"}
	if bad.SuspendedAt(now) || bad.DueAt(now) {
		t.Fatal("DueTS 读不出却被触发")
	}
	// 到点判据是**时钟**: 存了不代表到点。
	future := Interruption{DueTS: now.Add(time.Hour).Format(fatigueTSLayout)}
	if future.DueAt(now) {
		t.Fatal("还没到点就触发了 —— 恢复的落点必须是时钟")
	}
}

// 改坏成: LoadInterruption 把"表不存在"吞成 ok=false → 红。
// 漏接线必须是**看得见的**: 报错 → 调用方走"没有中断"的正常路径, 而不是当成有中断。
//
// ⚠️ 这里**显式 DROP 表**, 而不是指望"OpenStore 还没建表" ——
// migrate 接上 EnsureFatigueSchema 之后, 那个前提就没了 (实测: 接线后本用例变红)。
// 显式造出"表不存在"这个局面, 测的东西才和接线状态无关。
func TestLoadInterruption_WithoutEnsureIsLoud(t *testing.T) {
	s := fatigueTestStore(t)
	if _, err := s.DB.Exec(`DROP TABLE IF EXISTS convo_interruption`); err != nil {
		t.Fatalf("造局面失败: %v", err)
	}
	if _, _, err := s.LoadInterruption(); err == nil {
		t.Fatal("表不存在却不报错 —— 接线漏掉会变成静默失败")
	}
	if err := s.SaveInterruption(Interruption{DueTS: "2026-03-02 14:10:00"}); err == nil {
		t.Fatal("表不存在却写得进去")
	}
}

// ───────────────────────────────────────────────────────────────────────────
// 配额表达式的复刻 (只给 TestFatigue_DoesNotCutQuota 用)
// ───────────────────────────────────────────────────────────────────────────

// fatigueTestQuotaInputs 一次配额裁决的**全部输入**。逐位相等 = 疲劳没碰到配额。
type fatigueTestQuotaInputs struct {
	ActiveQuotaDay     int
	MoodWillingness    float64
	MoodLowWillingness float64
	HasBody            bool
	BodyQuotaDelta     int
	// 下面两个只能由 FatigueQuotaProjection 填 —— 疲劳进配额的唯一通道。
	FatigueDelta       int
	HasFatiguePressure bool
}

// fatigueTestQuotaWith 把疲劳投影进配额输入 —— 这是疲劳通向配额的**唯一**通道。
func fatigueTestQuotaWith(in fatigueTestQuotaInputs, fx FatigueEffects) fatigueTestQuotaInputs {
	in.FatigueDelta, in.HasFatiguePressure = FatigueQuotaProjection(fx)
	return in
}

// fatigueTestQuota 复刻 scheduler_logic.go:168-179 的配额表达式, 返回 (夹紧前, 夹紧后)。
//
// ⚠️ 这里是测试侧的复制品, **不是**生产路径; 它存在的意义是让"疲劳有没有进配额"
// 这件事在测试里可判定 —— 生产侧一旦被改坏, FatigueQuotaProjection 会先把值递过来。
//
// ⚠️ 两个返回值都要断言: 只看夹紧后的那个, "quota 已经是 0" 会把偷偷的 -1 掩盖成假绿。
func fatigueTestQuota(in fatigueTestQuotaInputs) (raw, quota int) {
	raw = in.ActiveQuotaDay
	if in.MoodWillingness > 0 && in.MoodWillingness < in.MoodLowWillingness {
		raw--
	}
	if in.HasBody {
		raw += in.BodyQuotaDelta
	}
	if in.HasFatiguePressure { // 只有"疲劳有配额落点"时才会走到这里
		raw += in.FatigueDelta
	}
	quota = raw
	if quota < 0 {
		quota = 0
	}
	return raw, quota
}
