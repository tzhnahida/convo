package convo

import (
	"testing"
	"time"
)

// ——— §4.4.5 睡眠质量 ———

// TestSettleSleepQuality_ZeroIsNeutral **B1 + B2** —— 零值 = 没读数 = 一切照常。
//
// 反面(任一条): 零值当作"睡得很差" / 零值给配额加成 → 红。
// 纪律: 失败往"正常说话"倒。反过来的话, 一次读库失败会让它第二天**整天迟钝**,
// 而没人能从表现上看出那是故障。
func TestSettleSleepQuality_ZeroIsNeutral(t *testing.T) {
	_, fx := SettleSleepQuality(SleepQualityInput{})
	if fx.DelayFactor != 1.0 || fx.LengthFactor != 1.0 || fx.QuotaDelta != 0 {
		t.Fatalf("零值必须是中性档, 得到 %+v", fx)
	}
	if fx.Active {
		t.Fatal("零值不该被标记为 Active (那是「有读数」的意思)")
	}
	if fx.Score != 100 {
		t.Fatalf("零值分数应当是满分 (走「一切照常」那一档), 得到 %d", fx.Score)
	}
}

// TestSleepQualityLadder_Directions **B3** —— 三条方向性约束逐行钉死。
// 破了任何一条, 这一层就从"有代价的状态"退化成随机扰动。
func TestSleepQualityLadder_Directions(t *testing.T) {
	for i, r := range SleepQualityLadder {
		if r.DelayFactor < 1.0 {
			t.Fatalf("第 %d 行 DelayFactor=%.2f < 1 — 没睡好不该更快", i, r.DelayFactor)
		}
		if r.LengthFactor > 1.0 || r.LengthFactor <= 0 {
			t.Fatalf("第 %d 行 LengthFactor=%.2f — 没睡好只会更短", i, r.LengthFactor)
		}
		if r.QuotaDelta > 0 {
			t.Fatalf("第 %d 行 QuotaDelta=%d > 0 — 没睡好不该更爱开口", i, r.QuotaDelta)
		}
	}
	// 表必须按 MinScore **降序** —— 否则"第一个命中"不是最好那档。
	for i := 1; i < len(SleepQualityLadder); i++ {
		if SleepQualityLadder[i].MinScore >= SleepQualityLadder[i-1].MinScore {
			t.Fatalf("SleepQualityLadder 未按分数降序: 第 %d 行", i)
		}
	}
}

// TestSettleSleepQuality_WorseIsSlower **B4** —— 单调性。
func TestSettleSleepQuality_WorseIsSlower(t *testing.T) {
	good := SleepQualityInput{HasReadings: true}
	bad := SleepQualityInput{HasReadings: true, LatencyMin: 90, Interrupts: 3, InterruptMax: 2, Insomnia: true}
	_, fg := SettleSleepQuality(good)
	_, fb := SettleSleepQuality(bad)
	if fb.Score >= fg.Score {
		t.Fatalf("更差的一夜分数没更低: %d vs %d", fb.Score, fg.Score)
	}
	if fb.DelayFactor < fg.DelayFactor {
		t.Fatalf("更差的一夜反而更快: %.2f vs %.2f", fb.DelayFactor, fg.DelayFactor)
	}
	if fb.QuotaDelta > fg.QuotaDelta {
		t.Fatalf("更差的一夜配额反而更高: %d vs %d", fb.QuotaDelta, fg.QuotaDelta)
	}
}

// TestSleepEffect_OnlyLastNight **只认"昨夜"**。
//
// 反面: 去掉那个归属日判断 → 三天前没睡好会一直拖累今天 → 红。
// 一个**永远恢复不了**的状态比没有状态更糟 —— 它会把一次失眠变成永久人设。
func TestSleepEffect_OnlyLastNight(t *testing.T) {
	s := newTestStore(t)
	e := &Engine{Store: s, Cfg: DefaultConfig()}
	// 三天前睡得很差 (用一个远古的归属日 —— 它必须**不生效**)。
	if err := s.SaveNight(Night{Night: "2020-01-01", LatencyMin: 120,
		Interrupts: 5, InterruptMax: 2, Insomnia: true}); err != nil {
		t.Fatal(err)
	}
	fx := e.sleepEffect()
	if fx.Active {
		t.Fatalf("三天前的失眠还在影响今天 (%+v) —— 一次失眠会变成永久人设", fx)
	}
	if fx.DelayFactor != 1.0 {
		t.Fatalf("过期的一夜不该改延迟: %.2f", fx.DelayFactor)
	}
	// 正面: 昨夜睡得很差 → 生效。
	if err := s.SaveNight(Night{Night: NightKeyOf(time.Now()), LatencyMin: 120,
		Interrupts: 5, InterruptMax: 2, Insomnia: true}); err != nil {
		t.Fatal(err)
	}
	fx2 := e.sleepEffect()
	if !fx2.Active || fx2.DelayFactor <= 1.0 {
		t.Fatalf("昨夜没睡好却没生效: %+v", fx2)
	}
}

// TestDecideTick_SleepQuotaBinds **接线**: 睡眠质量的配额下调真的进了裁决。
//
// 反面: 删掉 DecideTick 里的 `bodyCut += in.SleepQuotaDelta` → 红。
func TestDecideTick_SleepQuotaBinds(t *testing.T) {
	cfg := SchedulerConfig{ActiveQuotaDay: 1, MoodLowWillingness: 0.45,
		OpenMinGapMin: 240, OpenJitterPct: 10, HeatCostPerMsg: 25}
	base := TickInput{Now: time.Now(), HadSlot: true, State: StateFree,
		HasImpulse: true, OpenImpulse: 0.9, OpenJitterRoll: 0}
	// 配额 1, 昨夜几乎没睡 → -1 → 0 条。
	tired := base
	tired.SleepQuotaDelta = -1
	if got := DecideTick(tired, cfg); got.Kind == ActActiveOpen {
		t.Fatal("没睡好却没压配额 —— SleepQuotaDelta 没接进裁决")
	}
	// 反面: 没给睡眠读数时照常开口 (失败往正常倒)。
	if got := DecideTick(base, cfg); got.Kind != ActActiveOpen {
		t.Fatalf("没读数时应当照常: %s (%s)", got.Kind, got.Reason)
	}
}
