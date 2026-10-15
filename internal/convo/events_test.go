package convo

import (
	"testing"
	"time"
)

// ——— §3.6 事件池 · B6 开口冲量 ———

func TestDayDrafts_UsesMomentTime(t *testing.T) {
	e := EnvDay{Date: "2026-05-01", Moments: []EnvMoment{{TS: "14:23", Text: "快递到了"}, {Text: "没有时刻"}}}
	d := DayDrafts(e, "2026-05-01")
	if len(d) != 2 {
		t.Fatalf("应有 2 条草稿, 实得 %d", len(d))
	}
	if d[0].TS != "2026-05-01 14:23:00" {
		t.Errorf("带时刻的应按时刻排: %q", d[0].TS)
	}
	// 反面: **时刻缺失要退回当天开始, 不能变成零值时间**
	if d[1].TS != "2026-05-01 00:00:00" {
		t.Errorf("缺时刻应退回当天开始 (与旧行为一致), 实得 %q", d[1].TS)
	}
}

func seedDraftStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(t.TempDir(), "test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func twoDrafts() []WorldEvent {
	return DayDrafts(EnvDay{Moments: []EnvMoment{
		{TS: "14:23", Text: "快递到了"},
		{TS: "19:05", Text: "朋友约周末吃饭"},
	}}, "2026-05-01")
}

// 幂等: tick 每分钟路过, 池子不能越涨越大。
func TestSeedDrafts_Idempotent(t *testing.T) {
	s := seedDraftStore(t)
	n1, err := s.SeedDrafts(twoDrafts())
	if err != nil {
		t.Fatal(err)
	}
	n2, err := s.SeedDrafts(twoDrafts())
	if err != nil {
		t.Fatal(err)
	}
	if n1 != 2 || n2 != 0 {
		t.Fatalf("第二次入池不应新增任何条目 (幂等), 实得 %d / %d", n1, n2)
	}
}

// 到点才触发, 且**只触发一次**。
func TestDrawDueDrafts_OnlyDueAndOnlyOnce(t *testing.T) {
	s := seedDraftStore(t)
	if _, err := s.SeedDrafts(twoDrafts()); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 5, 1, 0, 0, 0, 0, time.Local)

	if due, _ := s.DrawDueDrafts(base.Add(13 * time.Hour)); len(due) != 0 {
		t.Fatalf("13:00 不该有到点事件, 实得 %d", len(due))
	}
	due, _ := s.DrawDueDrafts(base.Add(14*time.Hour + 23*time.Minute))
	if len(due) != 1 || due[0].Text != "快递到了" {
		t.Fatalf("14:23 应触发第一条, 实得 %+v", due)
	}
	// 反面: 再取一次**不许**重复 —— 先标记后返回, 否则每分钟重放会把心情反复推向同一方向
	again, _ := s.DrawDueDrafts(base.Add(14*time.Hour + 24*time.Minute))
	if len(again) != 0 {
		t.Fatalf("同一条事件被重复取出 %d 次", len(again))
	}
	due2, _ := s.DrawDueDrafts(base.Add(20 * time.Hour))
	if len(due2) != 1 {
		t.Fatalf("20:00 应已触发第二条, 实得 %d", len(due2))
	}
}

// ——— B6 开口冲量 ———

func impulseEngine(t *testing.T, base time.Time) (*Engine, func(time.Time)) {
	t.Helper()
	s := seedDraftStore(t)
	setNow := withFrozenClock(t, base)
	return &Engine{Store: s}, setNow
}

func TestOpenImpulse_DecaysWithAge(t *testing.T) {
	base := time.Date(2026, 5, 1, 9, 0, 0, 0, time.Local)
	e, setNow := impulseEngine(t, base)
	setNow(base)
	if _, err := e.Store.IngestWorldEvents([]WorldEvent{{
		TS: base.Format("2006-01-02 15:04:05"), Kind: "environment", Source: "生活",
		Text: "快递到了", Intensity: 0.3, Weight: 1.0, Duration: "pulse",
	}}); err != nil {
		t.Fatal(err)
	}
	if imp, ok := e.OpenImpulse(base); !ok || imp < 0.9 {
		t.Fatalf("刚发生应接近 1, 实得 %v (ok=%v)", imp, ok)
	}
	if imp, _ := e.OpenImpulse(base.Add(2 * time.Hour)); imp < 0.4 || imp > 0.6 {
		t.Fatalf("2 小时后应约 0.5, 实得 %v", imp)
	}
	if imp, ok := e.OpenImpulse(base.Add(time.Duration(ImpulseDecayHours+1) * time.Hour)); !ok || imp != 0 {
		t.Fatalf("超出衰减窗应为 0, 实得 %v (ok=%v)", imp, ok)
	}
}

// 🔴 反面(本轮最重要的一条): **冲量 0 是「确实没事发生」, 不是「没有这个机制」**。
// 上一轮我把阈值调成 0.001 当作"关闭闸门"的对照 —— 那是误判: 冲量 0 时 0 < 0.001 仍成立,
// 照样拦。**零值在这个设计里是有意义的状态, 不是中性值。**
func TestOpenImpulse_ZeroMeansNoEventNotMissing(t *testing.T) {
	base := time.Date(2026, 5, 1, 9, 0, 0, 0, time.Local)
	e, setNow := impulseEngine(t, base)
	setNow(base)
	imp, ok := e.OpenImpulse(base)
	if !ok || imp != 0 {
		t.Fatalf("没有任何事件时应为 (0,true), 实得 (%v,%v) —— ok=false 就说明被当成了未提供", imp, ok)
	}
}

// 反面: 读不到 (库坏了) 必须给 (0,false) = 闸门不生效。
// **失败往「正常说话」倒, 不往「不说话」倒。**
func TestOpenImpulse_ReadErrorDisablesGate(t *testing.T) {
	s, err := OpenStore(t.TempDir(), "test.db")
	if err != nil {
		t.Fatal(err)
	}
	s.Close() // 故意弄坏
	e := &Engine{Store: s}
	if _, ok := e.OpenImpulse(time.Now()); ok {
		t.Fatal("读不到时必须 ok=false (闸门不生效), 否则它会变成哑巴")
	}
}

// ——— 闸门在决策器里的行为 ———

func impulseCfg() SchedulerConfig {
	c := moodOpenCfg()
	c.OpenImpulseMin = 0.25
	return c
}

func TestDecideTick_ImpulseGate(t *testing.T) {
	in := openableTick()
	in.HasImpulse, in.OpenImpulse = true, 0.8
	in.MinutesSinceLastOpen = 999
	in.ActiveToday = 0
	if got := DecideTick(in, impulseCfg()).Kind; got != ActActiveOpen {
		t.Fatalf("冲量足、配额够应放行, 实得 %q", got)
	}
	in.OpenImpulse = 0.1
	if got := DecideTick(in, impulseCfg()).Kind; got == ActActiveOpen {
		t.Fatal("冲量不足时不该开口 —— 否则配额仍然是触发")
	}
}

// 🔴 反面: **未提供冲量 ≠ 没冲量**。零值必须被解释成"没接这个机制", 而不是"没事发生"。
// 少了这条, 任何还没接冲量的调用方都会静默变成哑巴。
func TestDecideTick_MissingImpulseDoesNotBlock(t *testing.T) {
	in := openableTick()
	in.ActiveToday = 0
	in.MinutesSinceLastOpen = 999
	// HasImpulse 故意不设
	if got := DecideTick(in, impulseCfg()).Kind; got != ActActiveOpen {
		t.Fatalf("未提供冲量时不该拦, 实得 %q", got)
	}
}

// 顺序契约: 冲量闸门必须在**配额之前** —— 反过来"上限允许"仍会自动变成触发。
func TestDecideTick_ImpulseBlocksEvenWhenQuotaFree(t *testing.T) {
	in := openableTick()
	in.HasImpulse, in.OpenImpulse = true, 0.0
	in.ActiveToday = 0
	in.MinutesSinceLastOpen = 999
	a := DecideTick(in, impulseCfg())
	if a.Kind == ActActiveOpen {
		t.Fatal("配额全空也不许绕开冲量 —— 那说明闸门排在了配额后面")
	}
	if a.Reason != "no impulse" {
		t.Errorf("应给出 no impulse 的理由, 实得 %q", a.Reason)
	}
}
