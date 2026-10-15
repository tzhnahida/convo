package convo

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// resetEventGenMemGate 清掉进程内闸门。
//
// ⚠️ 它原来住在 `eventgen.go`(生产文件)里 —— 那是**放错了地方**:
// 棘轮按"生产代码里有没有调用者"判, 于是每一个测试辅助都会被它当成死代码。
// 挪到这里之后, 那个信号就干净了。
func resetEventGenMemGate() {
	eventGenMemMu.Lock()
	eventGenMemDay, eventGenMemN = "", 0
	eventGenMemMu.Unlock()
}

// ═══════════════════════════════════════════════════════════════════════════
// §3.6 生成器与预算闸门 —— 叶子模块的测试。
//
// **主体是反面用例**: 每一条契约旁边写一句"改坏成 X → 这条会红"。
// 正面用例只负责证明"机制确实会动" —— 没有反面用例的正面用例, 测的是同义反复。
//
// 辅助函数一律带 evgTest 前缀 (合并到主分支时不会与别的模块撞名)。
// ═══════════════════════════════════════════════════════════════════════════

// ——— 测试脚手架 ———

// evgTestChatter 是脚本化的 Chatter: 想让它怎么坏, 就怎么坏。
type evgTestChatter struct {
	mu     sync.Mutex
	reply  string
	err    error
	block  bool // true = 一直等到 ctx 被取消 (模拟挂死的服务)
	calls  int
	system []string
	user   []string
}

func (c *evgTestChatter) Chat(ctx context.Context, system, user string) (string, error) {
	c.mu.Lock()
	c.calls++
	c.system = append(c.system, system)
	c.user = append(c.user, user)
	reply, err, block := c.reply, c.err, c.block
	c.mu.Unlock()
	if block {
		<-ctx.Done() // 只有超时/取消能救它 —— 没有超时的话, 这个用例会挂死
		return "", ctx.Err()
	}
	return reply, err
}

func (c *evgTestChatter) n() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *evgTestChatter) lastUser() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.user) == 0 {
		return ""
	}
	return c.user[len(c.user)-1]
}

// evgTestIsolate 复位包级状态 (预算 / 读数 / 进程内闸门), 并在用例结束时再复位一次。
// 它们都是包级状态 (与 FrameStats 同款), 不复位就会互相污染 —— 那种失败最难查。
func evgTestIsolate(t *testing.T) {
	t.Helper()
	SetEventGenBudget(EventGenBudget{})
	ResetEventGenStats()
	resetEventGenMemGate()
	t.Cleanup(func() {
		SetEventGenBudget(EventGenBudget{})
		ResetEventGenStats()
		resetEventGenMemGate()
	})
}

func evgTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(t.TempDir(), "eventgen-test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := EnsureEventGenSchema(s); err != nil {
		t.Fatalf("EnsureEventGenSchema: %v", err)
	}
	return s
}

// evgTestNow 固定一个夜晚的时刻 (2026-05-01 是周五)。
func evgTestNow() time.Time {
	return time.Date(2026, 5, 1, 23, 0, 0, 0, time.Local)
}

func evgTestProfile() *PersonaProfile {
	return &PersonaProfile{
		Name: "小夜", Identity: "student", Template: "bestie",
		Description: "在校大学生,和用户是闺蜜关系", Personality: "话多, 爱吐槽",
	}
}

func evgTestEnv() EnvDay {
	return EnvDay{Date: "2026-05-01", City: "东京", Weather: "小雨", TempC: 18}
}

// evgTestWeek 一张合法的周日程 (七天同款), 供提示词取材用。
func evgTestWeek(t *testing.T) *WeekSchedule {
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

// evgTestFrameHappy 五条候选: 三条合格, 一条时刻不可解析, 一条 kind 不在枚举里。
// p_trigger 一律 1.0 —— 让"被 p_trigger 掷掉"这件事不出现在这个用例里 (它有自己的用例)。
const evgTestFrameHappy = `{"events":[
 {"kind":"stranger","desc":"路上遇到一个遛狗的老太太","ts":"08:40","window":"07:00-10:00","valence":0.2,"arousal":0.5,"intensity":0.3,"duration":"pulse","p_trigger":1.0,"source":"遛狗的老太太"},
 {"kind":"work","desc":"临时被拉进一个会, 开到很晚","ts":"18:30","valence":-0.3,"arousal":0.6,"intensity":0.4,"duration":"pulse","p_trigger":1.0},
 {"kind":"weather","desc":"下班路上雨停了, 空气挺好闻","ts":"19:10","valence":0.25,"arousal":0.5,"intensity":0.3,"duration":"background","p_trigger":1.0},
 {"kind":"social","desc":"同事临时拉你去喝酒","ts":"晚上八点","valence":0.4,"arousal":0.7,"intensity":0.5,"p_trigger":1.0},
 {"kind":"hallucination","desc":"这个 kind 不在枚举里","ts":"21:00","p_trigger":1.0}
]}`

// ——— 正面: 生成出来的池子**真的能喂给 SeedDrafts** ———

func TestEventGen_HappyPath_PoolIsSeedable(t *testing.T) {
	evgTestIsolate(t)
	st := evgTestStore(t)
	now := evgTestNow()
	c := &evgTestChatter{reply: evgTestFrameHappy}

	pool, err := GenerateNightPool(context.Background(), c, st, evgTestEnv(), evgTestProfile(), now)
	if err != nil {
		t.Fatalf("不该报错 (降级不是错误): %v", err)
	}
	if len(pool) != 3 {
		t.Fatalf("三条合格候选应全部收下, 实得 %d: %+v", len(pool), pool)
	}
	// 按时刻排序 + 时刻是真的 (这一条是"世界在日内会动"的地基)
	if pool[0].TS != "2026-05-01 08:40:00" || pool[1].TS != "2026-05-01 18:30:00" || pool[2].TS != "2026-05-01 19:10:00" {
		t.Fatalf("时刻/排序不对: %q %q %q", pool[0].TS, pool[1].TS, pool[2].TS)
	}
	// kind 必须映射成**下游认识的枚举**, 不是设计稿的素材分类
	if pool[0].Kind != "social" || pool[0].Source != "遛狗的老太太" {
		t.Errorf("stranger 应折成 social + 模型给的 source, 实得 %+v", pool[0])
	}
	if pool[1].Kind != "calendar" || pool[1].Source != "工作" {
		t.Errorf("work 应折成 calendar + 档位默认 source, 实得 %+v", pool[1])
	}
	if pool[2].Kind != "environment" || pool[2].Duration != "background" {
		t.Errorf("weather 应折成 environment, duration 应保留 background, 实得 %+v", pool[2])
	}
	if pool[0].Weight != 1.0 {
		t.Errorf("关系权重恒 1.0 (那是关系层的量), 实得 %v", pool[0].Weight)
	}

	// 落池: 幂等入池 → 到点触发。这是"可直接喂 SeedDrafts"的硬证据。
	n, err := st.SeedDrafts(pool)
	if err != nil || n != 3 {
		t.Fatalf("SeedDrafts 应入池 3 条, 实得 %d (err=%v)", n, err)
	}
	before, _ := st.DrawDueDrafts(time.Date(2026, 5, 1, 8, 39, 0, 0, time.Local))
	if len(before) != 0 {
		t.Fatalf("08:39 不该有到点事件, 实得 %d", len(before))
	}
	due, _ := st.DrawDueDrafts(time.Date(2026, 5, 1, 8, 40, 0, 0, time.Local))
	if len(due) != 1 || due[0].Text != "路上遇到一个遛狗的老太太" {
		t.Fatalf("08:40 应触发第一条, 实得 %+v", due)
	}

	// 读数: 成功路径不该记成降级
	s := EventGenStatsSnapshot()
	if s.Calls != 1 || s.Success != 1 || s.Fallbacks != 0 || s.Produced != 3 {
		t.Fatalf("读数不对: %+v", s)
	}
	if s.Dropped[evgDropBadTime] != 1 || s.Dropped[evgDropKind] != 1 {
		t.Fatalf("丢弃原因分布不对: %+v", s.Dropped)
	}
	// 账本: 一次调用, 终态 ok, 收下 3 条
	if got, _ := st.EventGenCallsOn("2026-05-01"); got != 1 {
		t.Errorf("账本应有 1 次调用, 实得 %d", got)
	}
	led, _ := st.EventGenLedger("2026-05-01", 5)
	if len(led) != 1 || led[0].State != "ok" || led[0].Events != 3 || led[0].Source != "llm" {
		t.Fatalf("账本内容不对: %+v", led)
	}
}

// ——— 反面: 失败**必须留痕**, 不能被记成"今天没事发生" ———

func TestEventGen_ModelFailureRecordsFallbackNotSilence(t *testing.T) {
	evgTestIsolate(t)
	st := evgTestStore(t)
	now := evgTestNow()
	c := &evgTestChatter{err: context.DeadlineExceeded} // 网络/服务错误

	pool, err := GenerateNightPool(context.Background(), c, st, evgTestEnv(), evgTestProfile(), now)
	if err != nil {
		t.Fatalf("失败应退回模板而不是报错: %v", err)
	}
	// 反面: 把"生成失败"记成"今天没事发生" (返回空池 / 不计数) → 这三条会红。
	if len(pool) == 0 {
		t.Fatal("失败时池子**必须**非空 —— 世界停摆比事件平庸糟得多")
	}
	s := EventGenStatsSnapshot()
	if s.Fallbacks != 1 {
		t.Errorf("失败必须记成一次'退回' (否则报告里和'世界很平静'长得一样), 实得 %+v", s)
	}
	if s.Success != 0 || s.LastReason != EventGenReasonChatError {
		t.Errorf("失败原因必须落在读数上, 实得 success=%d last=%q", s.Success, s.LastReason)
	}
	// 重试也占配额: 默认 2 次尝试 → 2 次调用, 2 条失败的账
	if s.Calls != EventGenDefaultAttempts {
		t.Errorf("默认应尝试 %d 次, 实得 %d", EventGenDefaultAttempts, s.Calls)
	}
	if len(s.Samples) == 0 {
		t.Error("失败样本要留下 (决定该改提示词还是改解析器的唯一依据)")
	}
	led, _ := st.EventGenLedger("2026-05-01", 5)
	if len(led) != EventGenDefaultAttempts {
		t.Fatalf("账本应有 %d 行, 实得 %d", EventGenDefaultAttempts, len(led))
	}
	for _, l := range led {
		if l.State != "fail" || l.Reason != EventGenReasonChatError {
			t.Errorf("账本行应记失败原因, 实得 %+v", l)
		}
	}
	calls, fails, events, err := st.EventGenDaySummary("2026-05-01")
	if err != nil || calls != 2 || fails != 2 || events != 0 {
		t.Errorf("一天的总结应是 2 调 2 败 0 条, 实得 %d/%d/%d (err=%v)", calls, fails, events, err)
	}
	if len(pool) != EventGenFallbackCount {
		t.Errorf("退回的是 %d 条模板, 实得 %d", EventGenFallbackCount, len(pool))
	}
	if s.TemplateEvts != len(pool) {
		t.Errorf("模板产出条数也要计数, 实得 %d vs %d", s.TemplateEvts, len(pool))
	}
}

func TestEventGen_BadJSONAndThinPoolStillLeaveFallback(t *testing.T) {
	evgTestIsolate(t)
	st := evgTestStore(t)
	now := evgTestNow()

	// ① 压根不是 JSON: 反面 —— 解析失败若返回空池 (而不是退回模板) → 红。
	c1 := &evgTestChatter{reply: "嗯, 今天天气不错, 你那边呢?"}
	p1, _ := GenerateNightPool(context.Background(), c1, st, evgTestEnv(), evgTestProfile(), now)
	if len(p1) == 0 {
		t.Fatal("解析失败也必须退回模板")
	}
	if got := EventGenStatsSnapshot().Reasons[EventGenReasonBadJSON]; got == 0 {
		t.Fatalf("bad_json 必须落在原因分布上, 实得 %+v", EventGenStatsSnapshot().Reasons)
	}

	// ② 形状对、但一条都过不了硬校验: 反面 —— 把它记成"今天没事发生"(空池) → 红,
	//    它其实是**协议失败**, 必须能和"世界很平静"分开。
	ResetEventGenStats()
	c2 := &evgTestChatter{reply: `{"events":[{"kind":"social","desc":"时刻不可解析","ts":"傍晚"},{"kind":"social","desc":"时刻缺失"}]}`}
	p2, _ := GenerateNightPool(context.Background(), c2, st, evgTestEnv(), evgTestProfile(), now)
	if len(p2) == 0 {
		t.Fatal("一条都没过硬校验时也必须退回模板")
	}
	s2 := EventGenStatsSnapshot()
	if s2.Reasons[EventGenReasonNoEvents] == 0 {
		t.Errorf("应记 no_events, 实得 %+v", s2.Reasons)
	}
	if s2.Dropped[evgDropBadTime] != 2 {
		t.Errorf("两条都应因时刻不可解析被丢, 实得 %+v", s2.Dropped)
	}
	for _, ev := range p2 {
		if strings.Contains(ev.Text, "时刻") {
			t.Errorf("被丢掉的候选不许出现在池子里: %+v", ev)
		}
	}
}

// 超时闸门: 服务挂死时必须在有限时间内退回模板。
// 反面: 去掉 context 超时 (或直接用 ctx 不派生) → 这个用例挂死 → 红。
func TestEventGen_TimeoutIsBoundedAndFallsBack(t *testing.T) {
	evgTestIsolate(t)
	st := evgTestStore(t)
	SetEventGenBudget(EventGenBudget{Timeout: 60 * time.Millisecond, Attempts: 1})
	c := &evgTestChatter{block: true}

	start := time.Now()
	pool, err := GenerateNightPool(context.Background(), c, st, evgTestEnv(), evgTestProfile(), evgTestNow())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("超时应退回模板而不是报错: %v", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("超时闸门没生效, 用了 %v", elapsed)
	}
	if len(pool) == 0 {
		t.Fatal("超时也必须留下非空池子")
	}
	s := EventGenStatsSnapshot()
	if s.Reasons[EventGenReasonTimeout] == 0 || s.Fallbacks != 1 {
		t.Fatalf("超时要记成 timeout + 退回, 实得 %+v", s)
	}
	led, _ := st.EventGenLedger("2026-05-01", 5)
	if len(led) != 1 || led[0].Reason != EventGenReasonTimeout {
		t.Fatalf("账本要记 timeout, 实得 %+v", led)
	}
}

// ——— 反面(最贵的一条): 零值 = 默认, **不是"不限制"** ———

func TestEventGen_ZeroBudgetMeansDefaultNotUnlimited(t *testing.T) {
	evgTestIsolate(t)
	SetEventGenBudget(EventGenBudget{}) // 全零 = 没配
	b := EffectiveEventGenBudget()
	if b.CallsPerDay != EventGenDefaultCallsPerDay {
		t.Fatalf("零值必须折成默认 %d, 实得 %d (0 = 不限制 是错的, 0 = 不生成 也是错的)",
			EventGenDefaultCallsPerDay, b.CallsPerDay)
	}
	if b.Candidates != EventGenDefaultCandidates || b.Attempts != EventGenDefaultAttempts ||
		b.Timeout != EventGenCallTimeout || b.LookbackDays != EventGenDefaultLookback {
		t.Fatalf("其余零值也要折成默认, 实得 %+v", b)
	}
	if huge := (EventGenBudget{Candidates: 999}).withDefaults(); huge.Candidates != EventGenHardMaxCandidates {
		t.Fatalf("候选条数有硬上限 %d, 实得 %d", EventGenHardMaxCandidates, huge.Candidates)
	}

	// 行为侧: 一个"每分钟都来调"的调用方 (最典型的接线错误) 只能烧掉默认次数。
	st := evgTestStore(t)
	c := &evgTestChatter{err: context.DeadlineExceeded}
	for i := 0; i < 5; i++ {
		pool, _ := GenerateNightPool(context.Background(), c, st, evgTestEnv(), evgTestProfile(), evgTestNow())
		if len(pool) == 0 {
			t.Fatalf("第 %d 次调用拿到了空池子", i+1)
		}
	}
	if c.n() != EventGenDefaultCallsPerDay {
		t.Fatalf("一天最多 %d 次调用, 实得 %d", EventGenDefaultCallsPerDay, c.n())
	}
	if got := EventGenStatsSnapshot().BudgetDenied; got == 0 {
		t.Fatalf("被闸门挡下的次数要计数 (否则看不出'是配额挡的'还是'模型坏了'), 实得 %d", got)
	}
}

// 按天封顶是**硬约束**: 库里的账, 不是内存里的计数器。
func TestEventGen_DailyCapIsHardAcrossRuns(t *testing.T) {
	evgTestIsolate(t)
	st := evgTestStore(t)
	SetEventGenBudget(EventGenBudget{CallsPerDay: 2, Attempts: 1})
	now := evgTestNow()
	c := &evgTestChatter{err: context.DeadlineExceeded}

	for i := 0; i < 3; i++ {
		pool, _ := GenerateNightPool(context.Background(), c, st, evgTestEnv(), evgTestProfile(), now)
		if len(pool) == 0 {
			t.Fatalf("第 %d 次调用拿到了空池子 —— 配额用尽也必须退回模板", i+1)
		}
	}
	// 反面: 配额不生效 (或零值被当成不限制) → 这里会是 3 次 → 红。
	if c.n() != 2 {
		t.Fatalf("配额 2 次, 实际调了 %d 次", c.n())
	}
	if got, _ := st.EventGenCallsOn("2026-05-01"); got != 2 {
		t.Fatalf("账本应只有 2 次预留, 实得 %d", got)
	}
	s := EventGenStatsSnapshot()
	if s.Calls != 2 || s.BudgetDenied != 1 || s.Fallbacks != 3 {
		t.Fatalf("读数应为 2 调 / 1 挡 / 3 退回, 实得 %+v", s)
	}

	// 换一天 → 配额重新开始 (按天封顶, 不是一次性总额)。
	next := now.AddDate(0, 0, 1)
	p, _ := GenerateNightPool(context.Background(), c, st, evgTestEnv(), evgTestProfile(), next)
	if len(p) == 0 {
		t.Fatal("新的一天必须还能生成")
	}
	if c.n() != 3 {
		t.Fatalf("新的一天应放行第 3 次调用, 实得 %d", c.n())
	}
	if got, _ := st.EventGenCallsOn("2026-05-02"); got != 1 {
		t.Fatalf("新一天的账应另起一行, 实得 %d", got)
	}
}

// ——— 反面: 时刻解析不出就必须丢那一条 ———

func TestEventGen_TimeIsHardConstraint(t *testing.T) {
	// 单位: 只认 HH:MM / HH:MM:SS。
	// 反面: 放宽成"解析不出就用 00:00 兜底" → 下面这些 reject 会红。
	for _, ok := range []string{"18:30", "9:05", "18:30:00", " 07:00 "} {
		if got, fine := eventGenParseHHMM(ok); !fine {
			t.Errorf("%q 应被接受", ok)
		} else if len(got) != 5 || got[2] != ':' {
			t.Errorf("%q 应被规范化成 HH:MM, 实得 %q", ok, got)
		}
	}
	for _, bad := range []string{"", "18:00-23:00", "晚上八点", "25:00", "18:60", "8点", "morning"} {
		if got, fine := eventGenParseHHMM(bad); fine {
			t.Errorf("%q 不该被接受 (实得 %q) —— 假时刻比没有时刻更糟", bad, got)
		}
	}

	evgTestIsolate(t)
	st := evgTestStore(t)
	c := &evgTestChatter{reply: `{"events":[
	  {"kind":"social","desc":"时刻缺失的那条","p_trigger":1.0},
	  {"kind":"social","desc":"时刻可解析的那条","ts":"14:20","p_trigger":1.0}
	]}`}
	pool, _ := GenerateNightPool(context.Background(), c, st, evgTestEnv(), evgTestProfile(), evgTestNow())
	if len(pool) != 1 || pool[0].Text != "时刻可解析的那条" {
		t.Fatalf("只该留下可解析的那条, 实得 %+v", pool)
	}
	// 反面: 缺时刻的事件若被塞进当天零点, 就是"看着在动、其实时间全是假的" → 红。
	if strings.Contains(pool[0].TS, "00:00:00") {
		t.Fatalf("不许把缺时刻的事件塞到当天零点: %q", pool[0].TS)
	}
	if EventGenStatsSnapshot().Dropped[evgDropBadTime] != 1 {
		t.Fatalf("丢掉的条数要计数, 实得 %+v", EventGenStatsSnapshot().Dropped)
	}
}

// 反面: window 认得出就必须自洽 —— 时刻不在自己的窗口里 = 自相矛盾的一条。
func TestEventGen_WindowContainmentIsHard(t *testing.T) {
	evgTestIsolate(t)
	st := evgTestStore(t)
	SetEventGenBudget(EventGenBudget{CallsPerDay: 1})
	c := &evgTestChatter{reply: `{"events":[
	  {"kind":"social","desc":"时刻在窗口外的那条","ts":"15:00","window":"18:00-23:00","p_trigger":1.0},
	  {"kind":"social","desc":"窗口分隔符全角的那条","ts":"19:00","window":"18:00～23:00","p_trigger":1.0},
	  {"kind":"social","desc":"窗口认不出的那条","ts":"20:00","window":"晚上六点到十一点","p_trigger":1.0}
	]}`}
	pool, _ := GenerateNightPool(context.Background(), c, st, evgTestEnv(), evgTestProfile(), evgTestNow())
	if len(pool) != 2 {
		t.Fatalf("应留下 2 条 (窗口外的丢掉), 实得 %+v", pool)
	}
	for _, ev := range pool {
		if ev.Text == "时刻在窗口外的那条" {
			t.Error("时刻不在自己的窗口里 —— 这条必须丢")
		}
	}
	s := EventGenStatsSnapshot()
	if s.Dropped[evgDropWindow] != 1 {
		t.Errorf("应记一条 out_of_window, 实得 %+v", s.Dropped)
	}
	// 认不出的窗口: **忽略但留痕** —— 为一个可选字段的格式瑕疵丢掉好事件不划算。
	if s.Dropped[evgNoteWinIgnore] != 1 {
		t.Errorf("窗口认不出要留痕, 实得 %+v", s.Dropped)
	}
}

// ——— §3.6.4 红线 ———

func TestEventGen_PublicFactRedLine(t *testing.T) {
	evgTestIsolate(t)
	st := evgTestStore(t)
	c := &evgTestChatter{reply: `{"events":[
	  {"kind":"accident","desc":"今天东京发生 6 级地震","ts":"09:00","p_trigger":1.0},
	  {"kind":"social","desc":"同事临时拉你去喝酒","ts":"18:30","p_trigger":1.0}
	]}`}
	pool, _ := GenerateNightPool(context.Background(), c, st, evgTestEnv(), evgTestProfile(), evgTestNow())
	if len(pool) != 1 || pool[0].Text != "同事临时拉你去喝酒" {
		t.Fatalf("公共可证伪的事件必须被挡掉, 实得 %+v", pool)
	}
	// 反面: 去掉红线过滤 → 池子里会出现"地震" → 红。
	for _, ev := range pool {
		if strings.Contains(ev.Text, "地震") {
			t.Fatalf("红线漏了: %+v", ev)
		}
	}
	if EventGenStatsSnapshot().Dropped[evgDropPublic] != 1 {
		t.Fatalf("红线命中要计数, 实得 %+v", EventGenStatsSnapshot().Dropped)
	}
}

// ——— §3.6.3 的 p_trigger: 概率可以削减条数, 不能清零 ———

func TestEventGen_PTriggerRollsButPoolNeverEmpty(t *testing.T) {
	evgTestIsolate(t)
	st := evgTestStore(t)
	SetEventGenBudget(EventGenBudget{CallsPerDay: 200, Attempts: 1}) // 这条用例跨 60 天
	c := &evgTestChatter{reply: `{"events":[
	  {"kind":"social","desc":"候选一","ts":"09:00","p_trigger":0.02},
	  {"kind":"social","desc":"候选二","ts":"11:00","p_trigger":0.02},
	  {"kind":"social","desc":"候选三","ts":"13:00","p_trigger":0.02},
	  {"kind":"social","desc":"候选四","ts":"15:00","p_trigger":0.02},
	  {"kind":"social","desc":"候选五","ts":"17:00","p_trigger":0.02}
	]}`}
	base := evgTestNow()
	for i := 0; i < 60; i++ {
		day := base.AddDate(0, 0, i)
		pool, _ := GenerateNightPool(context.Background(), c, st, evgTestEnv(), evgTestProfile(), day)
		// 反面 A: 全掷空就返回空池 (丢掉"池底保 1 条") → 这里会红。
		if len(pool) == 0 {
			t.Fatalf("第 %d 天池子空了 —— 概率可以削减条数, 不能清零 (世界停摆最糟)", i)
		}
		for _, ev := range pool {
			if !strings.HasPrefix(ev.TS, day.Format("2006-01-02")) {
				t.Fatalf("第 %d 天的时刻串了日期: %q", i, ev.TS)
			}
		}
	}
	s := EventGenStatsSnapshot()
	// 反面 B: 把 p_trigger 忽略掉 (全收) → RolledOut 恒 0 → 红。
	if s.RolledOut == 0 {
		t.Fatalf("p_trigger 完全没起作用 (一条都没被掷掉): %+v", s)
	}
	if s.Fallbacks != 0 || s.Success != 60 {
		t.Fatalf("池底保 1 条意味着**不该**有降级: %+v", s)
	}
	if s.Produced >= 60*5 {
		t.Fatalf("概率没削减任何条数: 收下 %d 条 / 60 天", s.Produced)
	}
}

// 反面: 用 float64 接 p_trigger → "没给" 与 "给了 0" 分不开 → 那条会红。
func TestEventGen_PTriggerZeroDiffersFromMissing(t *testing.T) {
	missing := eventGenDraft{Kind: "social", Desc: "没给概率的那条", TS: "10:00"}
	_, p, why := eventGenValidate(missing, "2026-05-01")
	if why != "" || p != EventGenDefaultPTrigger {
		t.Fatalf("未提供 p_trigger 应取默认 %.2f 并**收下**, 实得 p=%.2f why=%q", EventGenDefaultPTrigger, p, why)
	}
	zero := 0.0
	explicit := eventGenDraft{Kind: "social", Desc: "明说不会发生的那条", TS: "10:00", PTrigger: &zero}
	if _, _, why := eventGenValidate(explicit, "2026-05-01"); why != evgDropPZero {
		t.Fatalf("模型明说 p=0 的候选不该占池位, 实得 why=%q", why)
	}
}

// ——— 模板兜底: 确定性 + 永远非空 ———

func TestEventGen_FallbackPoolDeterministicAndNeverEmpty(t *testing.T) {
	now := evgTestNow()
	a := EventGenFallbackPool(evgTestEnv(), evgTestProfile(), now)
	b := EventGenFallbackPool(evgTestEnv(), evgTestProfile(), now)
	// 反面: 用全局 rng 挑模板 → 同一天两次结果不同 → 红 (重跑会让池子漂移)。
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("同一天同人格必须是同一张池子: %+v vs %+v", a, b)
	}
	if len(a) != EventGenFallbackCount {
		t.Fatalf("兜底应给 %d 条, 实得 %d", EventGenFallbackCount, len(a))
	}

	// 跨天/跨人格: 永远非空、时刻可解析、kind 在枚举里; 组合确实在换。
	combos := map[string]bool{}
	for _, id := range []string{"student", "office_worker", "night_owl", ""} {
		p := &PersonaProfile{Identity: id}
		for i := 0; i < 40; i++ {
			pool := EventGenFallbackPool(EnvDay{Weather: "小雨"}, p, now.AddDate(0, 0, i))
			if len(pool) == 0 {
				t.Fatalf("兜底池空了 (人格 %q 第 %d 天)", id, i)
			}
			var texts []string
			for _, ev := range pool {
				switch ev.Kind {
				case "environment", "calendar", "social", "relation", "self":
				default:
					t.Fatalf("模板事件的 Kind 不在下游枚举里: %+v", ev)
				}
				// 反面: Duration 留空 → SettleStage 把**非 pulse** 的一律当 background,
				// 兜底事件会悄悄留在自传体里 30 天 → 红。
				if ev.Duration != "pulse" && ev.Duration != "background" {
					t.Fatalf("模板事件的 Duration 不能留空 (会被当成 background): %+v", ev)
				}
				if _, ok := eventGenParseHHMM(ev.TS[11:16]); !ok {
					t.Fatalf("模板事件的时刻不可解析: %q", ev.TS)
				}
				if ev.Text == "" || eventGenPublicFactWord(ev.Text) != "" {
					t.Fatalf("模板事件不能为空或踩红线: %+v", ev)
				}
				texts = append(texts, ev.Text)
			}
			combos[strings.Join(texts, "|")] = true
		}
	}
	if len(combos) < 10 {
		t.Fatalf("模板组合几乎不变 (%d 种), 那就成了道具依赖", len(combos))
	}

	// 反面: 表被清空就返回空池 → 红。这是最后一道兜底, 它自己不许失败。
	oldCommon, oldIdent := eventGenCommonPool, eventGenIdentityPools
	defer func() { eventGenCommonPool, eventGenIdentityPools = oldCommon, oldIdent }()
	eventGenCommonPool = nil
	eventGenIdentityPools = map[string][]eventGenTemplate{}
	if floor := EventGenFallbackPool(EnvDay{}, evgTestProfile(), now); len(floor) != 1 || floor[0].Text == "" {
		t.Fatalf("表清空时也必须给出一条中性事件, 实得 %+v", floor)
	}
}

// 身份自洽 (§3.6.7.3): 模板也要按身份分岔。
func TestEventGen_FallbackPoolRespectsIdentity(t *testing.T) {
	now := evgTestNow()
	seen := map[string]bool{}
	for i := 0; i < 60; i++ {
		for _, ev := range EventGenFallbackPool(EnvDay{}, &PersonaProfile{Identity: "student"}, now.AddDate(0, 0, i)) {
			seen[ev.Text] = true
		}
	}
	// 反面: 身份池不接进去 (所有人格同一张表) → 这几条学生专属的事永远不会出现 → 红。
	found := false
	for _, want := range eventGenIdentityPools["student"] {
		if seen[want.Text] {
			found = true
		}
	}
	if !found {
		t.Fatal("学生人格的模板池里一条学生专属的事都没抽到 —— 身份池没接上")
	}
}

// ——— 建表 / 账本 ———

func TestEventGen_SchemaIdempotentAndNilStore(t *testing.T) {
	s := evgTestStore(t)
	for i := 0; i < 3; i++ {
		if err := EnsureEventGenSchema(s); err != nil {
			t.Fatalf("EnsureEventGenSchema 幂等失败 (第 %d 次): %v", i+1, err)
		}
	}
	if err := EnsureEventGenSchema(nil); err == nil {
		t.Error("nil store 必须报错 —— 静默成功会让配额形同虚设")
	}
	if _, _, err := s.ReserveEventGenCall("2026-05-01", "2026-05-01 23:00:00", 0, 0); err != nil {
		t.Errorf("cap=0 应折成默认而不是报错: %v", err)
	}
	if _, err := (&Store{}).EventGenCallsOn("2026-05-01"); err == nil {
		t.Error("未初始化的 store 读账本必须报错")
	}
}

// 预留的"检查 + 插入"必须在一个事务里。
// 反面: 拆成两次独立语句 → 并发下会把配额撑破 → 这条会红。
func TestEventGen_ReserveIsAtomicUnderConcurrency(t *testing.T) {
	s := evgTestStore(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	granted := 0
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok, err := s.ReserveEventGenCall("2026-05-01", "2026-05-01 23:00:00", 3, 0)
			if err != nil {
				t.Errorf("预留报错: %v", err)
				return
			}
			if ok {
				mu.Lock()
				granted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if granted != 3 {
		t.Fatalf("并发下配额必须恰好放行 3 次, 实得 %d", granted)
	}
	if got, _ := s.EventGenCallsOn("2026-05-01"); got != 3 {
		t.Fatalf("账本应有 3 行, 实得 %d", got)
	}
}

// ——— §3.6.2 的输入真的进了提示词 ———

func TestEventGen_PromptCarriesDesignInputs(t *testing.T) {
	evgTestIsolate(t)
	st := evgTestStore(t)
	now := evgTestNow()
	if err := st.SaveSchedule(evgTestWeek(t)); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveMoodState(MoodState{Valence: 0.45, Arousal: 0.6}); err != nil {
		t.Fatal(err)
	}
	// 两天前的事 → 必须进 (避免重复); 十天前的事 → 不许进 (超出回看窗口)。
	ts := func(d int) string { return now.AddDate(0, 0, -d).Format("2006-01-02 15:04:05") }
	if _, err := st.IngestWorldEvents([]WorldEvent{
		{TS: ts(2), Kind: "social", Source: "朋友", Text: "老朋友约了顿饭", Intensity: 0.5, Weight: 1, Duration: "pulse"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.IngestWorldEvents([]WorldEvent{
		{TS: ts(10), Kind: "social", Source: "朋友", Text: "十天前的那件事", Intensity: 0.5, Weight: 1, Duration: "pulse"},
	}); err != nil {
		t.Fatal(err)
	}

	c := &evgTestChatter{reply: evgTestFrameHappy}
	if _, err := GenerateNightPool(context.Background(), c, st, evgTestEnv(), evgTestProfile(), now); err != nil {
		t.Fatal(err)
	}
	user := c.lastUser()
	for _, want := range []string{"老朋友约了顿饭", "挺好的", "07:30-12:00", "上课", "小雨", "小夜"} {
		if !strings.Contains(user, want) {
			t.Errorf("提示词少了设计稿要求的输入 %q:\n%s", want, user)
		}
	}
	// 反面: 回看窗口失效 (把整张表拉出来) → 十天前那条会进提示词 → 红。
	if strings.Contains(user, "十天前的那件事") {
		t.Errorf("超出回看窗口的事件不许进提示词:\n%s", user)
	}
	if !strings.Contains(c.system[0], "不许生成公共可证伪") {
		t.Errorf("系统提示词里的红线丢了:\n%s", c.system[0])
	}
}

// 读数快照必须是深拷贝 —— 调用方长期持有它, 包级状态还会继续被写。
func TestEventGen_StatsSnapshotIsDeepCopy(t *testing.T) {
	evgTestIsolate(t)
	noteEventGenDrop("x")
	s := EventGenStatsSnapshot()
	s.Dropped["x"] = 999
	s.Reasons["y"] = 1
	again := EventGenStatsSnapshot()
	if again.Dropped["x"] != 1 {
		t.Fatalf("快照的 map 被外部改动了: %+v", again.Dropped)
	}
	if _, ok := again.Reasons["y"]; ok {
		t.Fatalf("快照的 Reasons 被外部改动了: %+v", again.Reasons)
	}
}

// FallbackRate / AvgProduced 的口径 —— 报告里那一行"降级率"就是这么来的。
func TestEventGen_StatsDerivedRates(t *testing.T) {
	// 反面: 分母改成 Calls → 一个"从没成功过"的部署被成功调用稀释, 显示得像健康。
	var idle EventGenStats
	if got := idle.FallbackRate(); got != 0 {
		t.Fatalf("没跑过 → 0 (不是 NaN, 也不是 1), 实得 %v", got)
	}
	if got := (EventGenStats{Calls: 5, Success: 1, Fallbacks: 1}).FallbackRate(); got != 0.5 {
		t.Fatalf("降级率只按 (成功+退回) 算, 实得 %v", got)
	}
	if got := (EventGenStats{Success: 2, Produced: 8}).AvgProduced(); got != 4 {
		t.Fatalf("成功时平均收下 4 条, 实得 %v", got)
	}
	if got := (EventGenStats{Success: 0, Produced: 9}).AvgProduced(); got != 0 {
		t.Fatalf("没成功过 → 0 (不是 9), 实得 %v", got)
	}
}
