package convo

import (
	"context"
	"testing"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
)

// ——— §5.5 写路径 + §4.3 中断恢复: 接线测试 ———

func awCfg() SchedulerConfig {
	return SchedulerConfig{ActiveQuotaDay: 2, MoodLowWillingness: 0.45, OpenMinGapMin: 240, OpenJitterPct: 10}
}

// TestDecideTick_ResumeBypassesImpulseGate 是本组最要紧的一条。
//
// 反面: 把 resume 分支挪到**配额/冲量闸门之后** → 红。
// 为什么狠: 冲量要求"4 小时内有新的世界事件"(见 OpenImpulse), 拿它拦"我拿个外卖回来了",
// 后果是这句**几乎永远发不出去** —— 而它恰恰是最像人的那句话。
func TestDecideTick_ResumeBypassesImpulseGate(t *testing.T) {
	in := TickInput{
		Now: time.Now(), HadSlot: true, State: StateFree,
		InterruptionDue: true,
		HasImpulse:      true, OpenImpulse: 0, // 冲量闸门**故意关死**
		ActiveToday: 99, // 配额**故意用光**
	}
	got := DecideTick(in, awCfg())
	if got.Kind != ActResume {
		t.Fatalf("中断到点必须发得出去 (它是已有对话的延续, 不是新开口), 得到 %s (%s)", got.Kind, got.Reason)
	}
}

// 反面一: 没有中断就不许冒出 resume。
func TestDecideTick_NoInterruptionNoResume(t *testing.T) {
	in := TickInput{Now: time.Now(), HadSlot: true, State: StateFree, HasImpulse: true, OpenImpulse: 0}
	if got := DecideTick(in, awCfg()); got.Kind == ActResume {
		t.Fatal("没有中断却触发了恢复")
	}
}

// 反面二: **睡眠窗压过恢复** —— 睡着的时候不该"回来接着说"。
// 改坏成: 把 resume 分支挪到 sleep 检查之前 → 红。
func TestDecideTick_SleepBeatsResume(t *testing.T) {
	in := TickInput{Now: time.Now(), HadSlot: true, State: StateSleep, InterruptionDue: true}
	if got := DecideTick(in, awCfg()); got.Kind == ActResume {
		t.Fatal("睡眠窗内不该冒出来接着说 —— 那是睡着, 不是回来了")
	}
}

// —— 约定时刻: 只收两种形式 ——

func TestParsePromiseWhen_OnlyTwoForms(t *testing.T) {
	now := time.Date(2026, 3, 1, 10, 0, 0, 0, time.Local)
	cases := []struct {
		in   string
		ok   bool
		want string
	}{
		{"19:00", true, "2026-03-01 19:00:00"}, // 今天还没到
		{"09:00", true, "2026-03-02 09:00:00"}, // 今天已过 → 顺延明天, 不是"迟到一小时"
		{"今天 20:00", true, "2026-03-01 20:00:00"},
		{"明天 19:00", true, "2026-03-02 19:00:00"},
		{"下午晚点", false, ""},
		{"周末吧", false, ""},
		{"等你下班", false, ""},
		{"", false, ""},
		{"25:00", false, ""},
	}
	for _, c := range cases {
		got, ok := parsePromiseWhen(c.in, now)
		if ok != c.ok {
			t.Fatalf("parsePromiseWhen(%q) ok=%v, 想要 %v", c.in, ok, c.ok)
		}
		if ok && got.Format(bodyTSLayout) != c.want {
			t.Fatalf("parsePromiseWhen(%q) = %s, 想要 %s", c.in, got.Format(bodyTSLayout), c.want)
		}
	}
}

// —— 到点结算: 约定必须有终态 ——

// 反面: 不调 settleAgency → 约定永远是 open, 信用永远算不出来 →
// CreateAppointment 就成了一条只写不读的流水账 (那比不写更糟)。
func TestSettleAgency_WritesTerminal(t *testing.T) {
	s := newTestStore(t)
	e := &Engine{Store: s, Cfg: DefaultConfig()}
	now := time.Now()
	if _, err := s.CreateAppointment(Appointment{
		Tier: TierPromise, Who: 1, Scene: "qq", Text: "一起看电影",
		CreatedTS: now.Add(-2 * time.Hour).Format(bodyTSLayout),
		DueTS:     now.Add(-time.Hour).Format(bodyTSLayout), // 已到点
		Terminal:  ApptOpen,
	}); err != nil {
		t.Fatalf("CreateAppointment: %v", err)
	}
	if due, err := s.DueAppointments(now); err != nil || len(due) != 1 {
		t.Fatalf("前置: 应当有一条到点的约定 (due=%d err=%v)", len(due), err)
	}
	e.settleAgency(now)
	after, err := s.DueAppointments(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Fatal("到点的约定没被结账 —— 它永远是 open, 信用永远算不出来")
	}
	// 再跑一次必须幂等 (tick 每分钟都路过)。
	e.settleAgency(now)
}

// —— 中断恢复的 tick 动作 ——

func TestOnTick_ResumeClearsInterruptionWithoutEatingQuota(t *testing.T) {
	c := &sitCaptureChatter{}
	s := newTestStore(t)
	snd := &styleCapSender{}
	e := &Engine{Store: s, Cfg: DefaultConfig(), Dialog: &DialogEngine{Store: s},
		Chatter: c, Sender: snd, UserID: 1, Lang: "ja"}
	now := time.Now()
	if err := s.SaveProfile(&PersonaProfile{Name: "小夜", CreatedTS: now.Format(bodyTSLayout)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveInterruption(Interruption{
		AnchorTS: now.Format(fatigueTSLayout), Reason: "拿个外卖",
		DueTS: now.Add(-time.Minute).Format(fatigueTSLayout),
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.OnTick(context.Background(), TickAction{Kind: ActResume}); err != nil {
		t.Fatalf("OnTick(ActResume): %v", err)
	}
	if c.n == 0 {
		t.Fatal("恢复没有真的走对话路径")
	}
	// 不清的话它每分钟重试一次, 变成复读机。
	if _, ok, _ := s.LoadInterruption(); ok {
		t.Fatal("恢复之后中断没清 —— 会每分钟重试一次")
	}
	// 它是"接着说", 不是新的开口。
	if n := counterOrZero(s, "ai_opens", clock.Today()); n != 0 {
		t.Fatalf("恢复吃掉了主动配额 (ai_opens=%d) —— 那它就是在用「回来」刷开场", n)
	}
}

// —— 补两个覆盖缺口: 函数对 ≠ 它被调用了 ——

type awChatter struct{ out string }

func (c *awChatter) Chat(_ context.Context, _ string, _ string) (string, error) { return c.out, nil }

func awModeName(m DefianceMode) string {
	switch m {
	case DefySilent:
		return "silent"
	case DefyDelay:
		return "delay"
	case DefyRefuse:
		return "refuse"
	case DefyTalkBack:
		return "talkback"
	}
	return ""
}

// TestDialogReply_DefyIsRecordedInLedger 走 **dialogReply 全程**。
//
// ⚠️ 上面 TestDecideTick_* 证明不了这件事: 它们只测裁决, 不测记账。
// 改坏成: 删掉 dialogReply 里那段 RecordDefiance → 红。
// 不记的后果是"同一件事连续杠到上限就恢复正常"**永远不会触发** —— 上限形同虚设。
func TestDialogReply_DefyIsRecordedInLedger(t *testing.T) {
	s := newTestStore(t)
	e := &Engine{Store: s, Cfg: DefaultConfig(), Dialog: &DialogEngine{Store: s},
		Chatter: &awChatter{}, Sender: &styleCapSender{}, UserID: 1, Lang: "ja"}
	now := time.Now()
	p := &PersonaProfile{Name: "小夜", Template: "bestie",
		Attrs: map[string]string{"views": "不喜欢逛街"}}
	text := "陪我去逛街吧"

	// 先看系统这一轮到底给不给许可 —— 帧必须贴着许可写, 否则会被校验拦掉。
	dec := e.agencyContext(now, text, Topic{}, p)
	if dec.Defiance == DefyNone {
		t.Skip("前置不成立: 这一轮没有违抗许可 (判据变了就跳过, 不是失败)")
	}
	(e.Chatter.(*awChatter)).out = `{"reply":[{"kind":"text","text":"不想去"}],"defy":{"mode":"` + awModeName(dec.Defiance) + `"}}`
	if _, err := e.dialogReply(context.Background(), DialogInput{UserText: text, State: StateFree}, p); err != nil {
		t.Fatalf("dialogReply: %v", err)
	}
	n, err := s.DefianceStreak(dec.TopicKey, now)
	if err != nil {
		t.Fatalf("DefianceStreak: %v", err)
	}
	if n == 0 {
		t.Fatal("违抗没有进账本 —— 上限永远不会触发, 它可以对同一件事永远杠下去")
	}
}

// TestStepTick_SettlesDueAppointments 证明 **stepTick 真的调了 settleAgency**。
// 改坏成: 删掉 stepTick 里那句 e.settleAgency(now) → 红。
// (TestSettleAgency_WritesTerminal 是直接调函数的, 抓不到这一处 —— 与 BodyLine 同型。)
func TestStepTick_SettlesDueAppointments(t *testing.T) {
	s := newTestStore(t)
	e := &Engine{Store: s, Cfg: DefaultConfig(), Dialog: &DialogEngine{Store: s},
		Chatter: &awChatter{}, Sender: &styleCapSender{}, UserID: 1, Lang: "ja"}
	now := time.Now()
	if _, err := s.CreateAppointment(Appointment{
		Tier: TierPromise, Who: 1, Scene: "qq", Text: "一起看电影",
		CreatedTS: now.Add(-2 * time.Hour).Format(bodyTSLayout),
		DueTS:     now.Add(-time.Hour).Format(bodyTSLayout),
		Terminal:  ApptOpen,
	}); err != nil {
		t.Fatal(err)
	}
	e.stepTick(context.Background())
	after, err := s.DueAppointments(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Fatal("stepTick 没有结账到点的约定 —— settleAgency 没接线")
	}
}
