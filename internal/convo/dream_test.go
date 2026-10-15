package convo

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// ——— §4.4.4 做梦 ———

// TestDreamRoll_CorrelatesWithMoodArc **C3** —— 与当日情绪强度正相关 (设计稿 657)。
//
// 反面: 概率与情绪无关 (恒 30%) → 两组天数接近 → 红。
// 它同时是"情绪走势不能压没"(§4.4.1:619)的下游证据: 没有走势就没有做梦概率。
func TestDreamRoll_CorrelatesWithMoodArc(t *testing.T) {
	count := func(amp float64) int {
		n := 0
		for i := 0; i < 60; i++ {
			night := fmt.Sprintf("2026-03-%02d", i%28+1)
			if d, _ := DreamRoll(night, amp); d {
				n++
			}
		}
		return n
	}
	low, high := count(0.0), count(1.2)
	if high <= low {
		t.Fatalf("高情绪振幅没有提高做梦概率: 低 %d 天 vs 高 %d 天", low, high)
	}
}

// TestDreamRoll_DeterministicPerDay **C4** —— 同 night 两次结果相同。
//
// 反面: 用 Dice 代替 DayRoll → 夜间批被重入会**多做几个梦** → 红。
// 这是幂等闸门之外的第二道保险。
func TestDreamRoll_DeterministicPerDay(t *testing.T) {
	for i := 0; i < 50; i++ {
		night := fmt.Sprintf("2026-04-%02d", i%28+1)
		d1, n1 := DreamRoll(night, 0.6)
		d2, n2 := DreamRoll(night, 0.6)
		if d1 != d2 || n1 != n2 {
			t.Fatalf("同一天两次结果不同: %v/%v vs %v/%v", d1, n1, d2, n2)
		}
	}
}

// TestMakeDream_ForbiddenContentDropped **护栏②** —— 内容克制。
//
// 反面: 把越界的梦存下来 → 红。"睡不着"可以, "我梦见你离开我了"不行 ——
// 前者是状态, 后者是**拿梦去绑关系**。
func TestMakeDream_ForbiddenContentDropped(t *testing.T) {
	s := newTestStore(t)
	c := &awChatter{out: "我梦见你离开我了"}
	e := &Engine{Store: s, Cfg: DefaultConfig(), Chatter: c}
	mats := []Memory{{MemID: "m1", V: "今天聊了工作"}}
	// 找到一天会做梦的 night。
	found := false
	for i := 0; i < 60 && !found; i++ {
		night := fmt.Sprintf("2026-05-%02d", i%28+1)
		if d, _ := DreamRoll(night, 1.0); !d {
			continue
		}
		found = true
		if _, ok, err := e.MakeDream(context.Background(), night, 1.0, mats); err != nil {
			t.Fatal(err)
		} else if ok {
			t.Fatal("越界的梦被存下来了 —— 它次日会变成「它以为发生过的事」")
		}
		if _, has, _ := s.LastDream(); has {
			t.Fatal("越界的梦进了库")
		}
	}
	if !found {
		t.Fatal("60 天里一天都不做梦 —— 概率表坏了")
	}
}

// TestMakeDream_NoMaterialNoDream **没有素材就没有梦**。
// 反面: 空素材也生成 → 编出来的梦会变成"它以为发生过的事" → 红。
func TestMakeDream_NoMaterialNoDream(t *testing.T) {
	s := newTestStore(t)
	e := &Engine{Store: s, Cfg: DefaultConfig(), Chatter: &awChatter{out: "一个梦"}}
	for i := 0; i < 60; i++ {
		night := fmt.Sprintf("2026-06-%02d", i%28+1)
		if _, ok, _ := e.MakeDream(context.Background(), night, 1.0, nil); ok {
			t.Fatal("没有素材却生成了梦 —— 那是编的")
		}
	}
	if _, has, _ := s.LastDream(); has {
		t.Fatal("空素材的梦进了库")
	}
}

// TestDream_NeverPersistsAsMemory **C1 / 红线: 梦不许成为事实** (设计稿 659)。
//
// 反面: 梦写进 convo_memory → 它会沿"对话→摘要→上下文"变成真事 → 红。
// 这是整节最硬的一条。
//
// ⚠️ **这条测试第一版写错了, 是变异测试抓出来的。**
// 我原本直接调 s.SaveDream() 然后查 convo_memory —— 那测的是"SaveDream 不管记忆表",
// **当然永远通过**。真正要测的是 **MakeDream 这条路径**有没有顺手把梦写进记忆。
// (在 MakeDream 里加一句 UpsertMemory 的变异, 第一版测试照样绿。)
func TestDream_NeverPersistsAsMemory(t *testing.T) {
	s := newTestStore(t)
	e := &Engine{Store: s, Cfg: DefaultConfig(), Chatter: &awChatter{out: "梦见在下雨的车站等车"}}
	mats := []Memory{{MemID: "m1", V: "今天聊了工作"}}
	found := false
	for i := 0; i < 60 && !found; i++ {
		night := fmt.Sprintf("2026-07-%02d", i%28+1)
		if d, _ := DreamRoll(night, 1.0); !d {
			continue
		}
		found = true
		d, ok, err := e.MakeDream(context.Background(), night, 1.0, mats)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatal("应当做成一个梦 (素材与概率都够)")
		}
		// ① 梦本身**要**留下来 —— 否则被问到时它只能编。
		if _, has, _ := s.LastDream(); !has {
			t.Fatal("梦没被记下来 —— 那被问到时它就只能编")
		}
		// ② **但它绝不许变成事实** —— 这是红线。
		var n int
		if err := s.DB.QueryRow("SELECT COUNT(*) FROM convo_memory WHERE v LIKE ?",
			"%"+d.Text+"%").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatal("梦进了记忆表 —— 它会沿「对话→摘要→上下文」变成真事")
		}
	}
	if !found {
		t.Fatal("60 天里一天都不做梦 —— 概率表坏了")
	}
}

// TestStartlePermit_MonthlyCap **B7 / 护栏①** —— 噩梦求助 ≤1 次/月。
// 反面: 去掉闸门只按概率 → 30 夜里会问好几次 → 红。
func TestStartlePermit_MonthlyCap(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()
	ok, err := s.StartlePermit(now)
	if err != nil || !ok {
		t.Fatalf("第一次应当允许: ok=%v err=%v", ok, err)
	}
	if _, err := s.CounterInc("nightmare_ask", now.Format("2006-01")); err != nil {
		t.Fatal(err)
	}
	ok2, err := s.StartlePermit(now)
	if err != nil {
		t.Fatal(err)
	}
	if ok2 {
		t.Fatal("一个月内第二次求助被放行了 —— 会变成月底的例行撒娇")
	}
}

// TestStartleTarget_OnlyOwner 接收对象**硬绑定 owner** (设计稿 668)。
// 反面: 按好感度选人 → 群友也能收到它最脆弱的一面 → 红。
func TestStartleTarget_OnlyOwner(t *testing.T) {
	if got := StartleTarget(12345); got != 12345 {
		t.Fatalf("接收对象不是 owner: %d", got)
	}
}
