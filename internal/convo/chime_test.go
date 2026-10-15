package convo

import (
	"testing"
	"time"
)

func chimeBase() ChimeInput {
	return ChimeInput{Roll: 99, UsedToday: 0, TopicUsedToday: 0, GroupMsgsToday: 600, WakingElapsed: 0.9, SinceLast: time.Hour}
}

// ⚠️ **核心主张**: 没理由也可以插一句 —— 真人天天这么干。
func TestDecideChime_RandomChannel(t *testing.T) {
	in := chimeBase()
	in.Roll = 5 // < 10
	if d := DecideChime(in); !d.Speak {
		t.Fatal("10% 通道没开 —— 它就只能靠话题命中说话了")
	}
	in.Roll = 10 // 边界: 10 不算命中
	if d := DecideChime(in); d.Speak {
		t.Fatal("边界应当是 [0,10), 10 不该命中")
	}
	in.Roll = 9
	if d := DecideChime(in); !d.Speak {
		t.Fatal("9 应当命中")
	}
}

func TestDecideChime_TopicChannelIgnoresRoll(t *testing.T) {
	in := chimeBase()
	in.Roll = 99
	in.TopicMatch = true
	d := DecideChime(in)
	if !d.Speak {
		t.Fatal("话题命中不该被骰子挡住")
	}
	if d.Reason == "" {
		t.Fatal("理由必须记下来 —— 否则日志里分不出是哪种插话")
	}
}

// ⚠️ **本批的新规矩**: 被群友说了 → 收敛到 1%。
// 这是这个功能唯一的安全阀 —— 插话是"打扰"那一侧的例外,
// 而例外的代价只有群里的人能判定, 它自己判不出来。
func TestDecideChime_ComplaintDropsTo1Pct(t *testing.T) {
	if ChimeRate(false) != ChimeRollPct {
		t.Fatalf("常态应当是 %d%%", ChimeRollPct)
	}
	if ChimeRate(true) != ChimeRollPctAfterComplaint {
		t.Fatalf("被说过之后应当是 %d%%", ChimeRollPctAfterComplaint)
	}
	in := chimeBase()
	in.Complained = true
	// 5% 在常态里会命中, 收敛后不该。
	in.Roll = 5
	if d := DecideChime(in); d.Speak {
		t.Fatal("被说过之后 5% 不该命中 —— 收敛没生效")
	}
	in.Roll = 0
	if d := DecideChime(in); !d.Speak {
		t.Fatal("1% 不是 0% —— 它只是收敛, 不是被禁言")
	}
	// ⚠️ 话题命中**不受收敛影响**: 收敛的是"没理由的插话",
	// 不是"有话说的时候也不许说"。
	in.Roll = 99
	in.TopicMatch = true
	if d := DecideChime(in); !d.Speak {
		t.Fatal("话题命中被收敛挡住了 —— 那它就成了哑巴")
	}
}

// ⚠️ 只看词的话, 群友 A 对群友 B 说「你话真多」会把它的插话率打到 1% ——
// 而它什么都没做错, 且没人会知道它为什么突然不说话了。
func TestChimeComplaintHit_RequiresAtSelf(t *testing.T) {
	if ChimeComplaintHit("你话真多", false) {
		t.Fatal("没点名却算到它头上 —— 群友互喷会误伤它")
	}
	if !ChimeComplaintHit("你话真多", true) {
		t.Fatal("点名了应当算")
	}
	if ChimeComplaintHit("今天天气不错", true) {
		t.Fatal("没抱怨不该算")
	}
	if ChimeComplaintHit("", true) {
		t.Fatal("空消息不该算")
	}
	for _, s := range []string{"别插嘴", "你能不能安静点", "少说两句吧", "又没问你"} {
		if !ChimeComplaintHit(s, true) {
			t.Fatalf("应当命中: %q", s)
		}
	}
}

// ⚠️ 20%×200 = 40 次刷屏; 10%×200 = 20 次机会 —— **随机必须被配额兜住**。
func TestDecideChime_QuotaCapsTheRandomness(t *testing.T) {
	in := chimeBase()
	in.Roll = 1
	in.UsedToday = ChimeQuotaPerDay
	if d := DecideChime(in); d.Speak {
		t.Fatal("配额用完了还在插话 —— 10% 会变成刷屏")
	}
}

// ⚠️ 配额**随时间释放**: 按先到先得, 一个热闹的早上会把当天 3 次全用光,
// 于是它上午话痨、下午到晚上完全消失 —— 那是明显的机器人特征。
func TestDecideChime_QuotaReleasesOverTheDay(t *testing.T) {
	in := chimeBase()
	in.Roll = 1
	in.UsedToday = 1
	in.WakingElapsed = 0.02
	if d := DecideChime(in); d.Speak {
		t.Fatal("刚醒就用掉了第二发 —— 配额没有随时间释放")
	}
	in.WakingElapsed = 0.5
	if d := DecideChime(in); !d.Speak {
		t.Fatal("到了下午该放出来的配额没放")
	}
	in.UsedToday = 0
	in.WakingElapsed = 0
	if d := DecideChime(in); d.Speak {
		t.Fatal("elapsed=0 时预算应当是 0")
	}
}

func TestDecideChime_CooldownBeatsEverything(t *testing.T) {
	in := chimeBase()
	in.Roll = 0
	in.TopicMatch = true
	in.SinceLast = time.Minute
	if d := DecideChime(in); d.Speak {
		t.Fatal("冷却期内不该插话 —— 连着插比不插更烦人")
	}
	in.SinceLast = 0
	if d := DecideChime(in); !d.Speak {
		t.Fatal("零值 = 从没插过, 不该被冷却挡住")
	}
}

func TestDecideChime_WakingElapsedClamped(t *testing.T) {
	in := chimeBase()
	in.Roll = 1
	in.WakingElapsed = 5
	in.UsedToday = ChimeQuotaPerDay
	if d := DecideChime(in); d.Speak {
		t.Fatal("elapsed>1 被当成超额预算了")
	}
	in.WakingElapsed = -1
	in.UsedToday = 0
	if d := DecideChime(in); d.Speak {
		t.Fatal("负数 elapsed 不该给预算")
	}
}

func TestDecideChime_DefaultsToSilence(t *testing.T) {
	in := chimeBase()
	in.Roll = 50
	if d := DecideChime(in); d.Speak || d.Reason != "" {
		t.Fatalf("不该说话时理由也要是空的: %+v", d)
	}
}

// ⚠️ 抱怨状态**落库**: 重启会丢掉内存,
// 而"重启之后又变得话多"正是群友最会注意到的那种反复。
func TestChimeComplaint_RoundTrip(t *testing.T) {
	s := newTestStore(t)
	owner, scene := "u1", "group:1034712035"
	if ok, err := s.ChimeComplained(owner, scene); err != nil || ok {
		t.Fatalf("空库不该有抱怨: ok=%v err=%v", ok, err)
	}
	if err := s.MarkChimeComplaint(owner, scene); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.ChimeComplained(owner, scene); !ok {
		t.Fatal("抱怨没落库 —— 重启之后它又会变话多")
	}
	// 场景隔离: 别的群不该被连坐。
	if ok, _ := s.ChimeComplained(owner, "group:999"); ok {
		t.Fatal("抱怨串场景了 —— A 群被说, B 群也跟着闭嘴")
	}
	if err := s.ClearChimeComplaint(owner, scene); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.ChimeComplained(owner, scene); ok {
		t.Fatal("撤销没生效")
	}
}

// ⚠️ **本批的新规矩**: 话题是**群友引起的**, 不占随机那 3 次 (2026-10-14 定)。
func TestDecideChime_TopicDoesNotConsumeRandomQuota(t *testing.T) {
	in := chimeBase()
	in.TopicMatch = true
	in.Roll = 99                    // 随机通道用不上
	in.UsedToday = ChimeQuotaPerDay // 随机额度已满
	if d := DecideChime(in); !d.Speak {
		t.Fatal("随机额度满了就把话题通道也堵了 —— 两笔账没分开")
	}
}

// ⚠️ 但**独立预算 ≠ 无预算**: 只靠冷却的话, 热闹群里一天能插 40 次。
func TestDecideChime_TopicHasItsOwnCeiling(t *testing.T) {
	in := chimeBase()
	in.TopicMatch = true
	in.TopicUsedToday = ChimeTopicQuotaMax
	in.UsedToday = ChimeQuotaPerDay
	if d := DecideChime(in); d.Speak {
		t.Fatal("话题通道没有天花板 —— 它会住在群里")
	}
	// 但话题额度用完时, 它仍可以"没理由地想插一句"。
	in.Roll = 1
	in.UsedToday = 0
	if d := DecideChime(in); !d.Speak {
		t.Fatal("话题额度用完应当落到随机通道")
	}
}

// ⚠️ **按比例**: 额度随群里消息量长, 不随时钟长。
func TestTopicBudget_ScalesWithGroupActivity(t *testing.T) {
	if TopicBudgetFor(20) != 1 {
		t.Fatalf("安静群至少 1 次, got %d", TopicBudgetFor(20))
	}
	if TopicBudgetFor(100) != 2 {
		t.Fatalf("100 条应当 2 次, got %d", TopicBudgetFor(100))
	}
	if TopicBudgetFor(1000) != 15 {
		t.Fatalf("1000 条应当卡在硬顶 15, got %d", TopicBudgetFor(1000))
	}
	if TopicBudgetFor(100000) != ChimeTopicQuotaMax {
		t.Fatal("比例本身也要有顶 —— 10000 条的群会把它喂成话痨")
	}
	// 空群不该是 0: 0 = 永远不说话, 那和死掉没区别。
	if TopicBudgetFor(0) != 1 {
		t.Fatal("空群也必须给 1 次")
	}
}

func TestDecideChime_TopicBudgetFollowsGroupVolume(t *testing.T) {
	in := chimeBase()
	in.TopicMatch = true
	in.TopicUsedToday = 2
	in.UsedToday = ChimeQuotaPerDay
	in.GroupMsgsToday = 100 // 额度 2 → 已用 2, 不给
	if d := DecideChime(in); d.Speak {
		t.Fatal("群只聊了 100 条却还能插第 3 次")
	}
	in.GroupMsgsToday = 600 // 额度 10 → 给
	if d := DecideChime(in); !d.Speak {
		t.Fatal("群里聊了 600 条, 额度该涨上去了")
	}
}

// ⚠️ **不自动恢复** —— 一旦回涨, 群友会看到它又开始了,
// 而"被说过之后又犯"比一开始就话多更难受。
func TestChimeRate_IsStableAfterComplaint(t *testing.T) {
	for i := 0; i < 3; i++ {
		if ChimeRate(true) != ChimeRollPctAfterComplaint {
			t.Fatal("收敛率必须是稳定的常量, 不该随时间或次数变化")
		}
	}
	if ChimeRollPctAfterComplaint >= ChimeRollPct {
		t.Fatal("收敛率必须低于常态率")
	}
}
