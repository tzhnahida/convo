package convo

import (
	"testing"
	"time"
)

// ——— 接线测试: 新模块是不是真的接上了 ———
//
// ⚠️ 这一组测的**不是"函数对不对"**, 而是"它有没有被调用"。
// BodyLine 当了一整轮死代码这件事说明: 单测全绿与"接上了"是两回事。

// TestDecideTick_HeatBudgetBinds §6.7 热力真的落进了配额。
//
// 反面: 把 DecideTick 里的 HeatQuotaFrom 换回 `quota := cfg.ActiveQuotaDay` 的旧算法 → 红。
func TestDecideTick_HeatBudgetBinds(t *testing.T) {
	cfg := SchedulerConfig{ActiveQuotaDay: 2, MoodLowWillingness: 0.45,
		OpenMinGapMin: 240, OpenJitterPct: 10, HeatCostPerMsg: 25}
	base := TickInput{Now: time.Now(), HadSlot: true, State: StateFree,
		HasImpulse: true, OpenImpulse: 0.9, OpenJitterRoll: 0}

	// ① 热力为 0 → 预算 0 → 不主动开口。
	cold := base
	cold.HasHeat, cold.Heat = true, 0
	if got := DecideTick(cold, cfg); got.Kind == ActActiveOpen {
		t.Fatal("热力为 0 却开口了 —— 热力没落进配额")
	}
	// ② 热力够 → 开口。
	hot := base
	hot.HasHeat, hot.Heat = true, 100
	if got := DecideTick(hot, cfg); got.Kind != ActActiveOpen {
		t.Fatalf("热力充足却没开口: %s (%s)", got.Kind, got.Reason)
	}
	// ③ **未提供热力 → 走常数配额** (与升级前逐字节相同)。
	//    ⚠️ 这一条是"失败往正常说话倒"的证明: 一次读库失败不许让它变哑巴。
	none := base
	none.HasHeat = false
	if got := DecideTick(none, cfg); got.Kind != ActActiveOpen {
		t.Fatal("未提供热力时没走常数配额 —— 读库失败会让它忽然变安静")
	}
}

// TestValidateFrame_MediationRefused §6.9 硬线 5 真的进了校验链。
//
// 反面: 删掉 dialog.go 里那段 ConflictMediationHit → 红。
func TestValidateFrame_MediationRefused(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜"}
	bad := OutputFrame{Reply: []FrameItem{{Kind: "text", Text: "大家都冷静一下 别吵了"}}}
	if v := d.ValidateFrame(bad, DialogInput{}, p); len(v) == 0 {
		t.Fatal("劝和话术外露没被拦 —— 硬线 5 要求静默丢弃 (说出来那一刻就从群友变成客服)")
	}
	// 反面: 普通话**不许**被误拦 —— 过滤太宽的代价是它不敢说话。
	ok := OutputFrame{Reply: []FrameItem{{Kind: "text", Text: "今天天气不错"}}}
	if v := d.ValidateFrame(ok, DialogInput{}, p); len(v) != 0 {
		t.Fatalf("普通话被误拦: %v", v)
	}
}

// TestPrepareDailyJobs_GeneratesEventPool §4.4.2 第 0 步真的接上了。
//
// 反面: 删掉 prepareDailyJobs 里那段 GenerateNightPool → 红。
// 没有它, 次日的事件池是空的 —— 而"明天没事件"与"生成坏了"在报告里长得一样。
//
// ⚠️ 这里**故意**让假 LLM 返回一段不是 JSON 的东西:
// 它同时验证了另一条契约 —— **失败必须退回确定性模板, 池子永远非空**。
// 如果实现写成"生成失败 → 返回空池", 这条会红。
func TestPrepareDailyJobs_GeneratesEventPool(t *testing.T) {
	s := newTestStore(t)
	// 环境源: 固定一天, 保证池子的生成有输入。
	oldEnv := EnvSource
	EnvSource = func(time.Time) (EnvDay, bool) {
		return EnvDay{Date: "2026-03-01", City: "东京", Weather: "小雨", TempC: 6}, true
	}
	defer func() { EnvSource = oldEnv }()

	if err := s.SaveProfile(&PersonaProfile{Name: "小夜", Template: "bestie",
		CreatedTS: "2026-02-01 10:00:00"}); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: s, Cfg: DefaultConfig(),
		Chatter: &awChatter{out: "这不是 JSON, 模型抽风了"}, UserID: 1, Lang: "ja"}

	e.prepareDailyJobs(time.Date(2026, 3, 1, 23, 30, 0, 0, time.Local))

	var n int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM convo_world_event_draft").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("第 0 步没生成事件池 —— 明天的世界是空的 (接上了没有?)")
	}
	// 反面: 池子必须在**模型失败**时仍然非空 —— 退回模板不是"记成今天没事发生"。
	if n < 1 {
		t.Fatal("模型抽风导致池子为空 —— 失败必须退回模板, 世界不许停摆")
	}
}

// TestDecideGroupIngest_FiveGates 群消息接收入口的三道门 + 两个零值陷阱。
//
// 反面: 删掉任意一道门 → 对应的那一种输入会被放进来 → 红。
func TestDecideGroupIngest_FiveGates(t *testing.T) {
	on := NormalizeGroupConfig(GroupConfig{Enabled: true, Whitelist: []string{"12345"}})

	// ① **私聊消息不许走群入口** —— 私聊是升级前就有的通路, 给它加群的门槛是加新门槛。
	if g := DecideGroupIngest(on, "private", 12345); g.Record {
		t.Fatal("私聊消息走了群入口 —— 那会给旧通路加一道它本来没有的门")
	}
	// ② **群号零值 = 未提供, 不是"群 0"** —— 拿不到群号就构造不出分区键。
	if g := DecideGroupIngest(on, "group", 0); g.Record {
		t.Fatal("群号缺失却记了 —— 这条会掉进私聊场景, 污染 L1")
	}
	if g := DecideGroupIngest(on, "group", -1); g.Record {
		t.Fatal("负数群号被当成有效群")
	}
	// ③ 三道门全过: 记, 且**分区键必须正确** (错了就是跨场景污染)。
	g := DecideGroupIngest(on, "group", 12345)
	if !g.Record {
		t.Fatalf("白名单里的群没被记: %s", g.Reason)
	}
	if g.SceneKey != "group:12345" {
		t.Fatalf("分区键不对: %q — 分区键错了比不记更糟 (它会把群消息记成私聊)", g.SceneKey)
	}
	// ④ 白名单外 → 不记。
	if g := DecideGroupIngest(on, "group", 99999); g.Record {
		t.Fatal("白名单外的群被记了")
	}
	// ⑤ 群聊功能没开 → 不记 (即使白名单里有)。
	off := NormalizeGroupConfig(GroupConfig{Enabled: false, Whitelist: []string{"12345"}})
	if g := DecideGroupIngest(off, "group", 12345); g.Record {
		t.Fatal("群聊功能没开却记了")
	}
	// ⑥ 空白名单 → 一个都不记 (fail-closed)。
	empty := NormalizeGroupConfig(GroupConfig{Enabled: true})
	if g := DecideGroupIngest(empty, "group", 12345); g.Record {
		t.Fatal("空白名单放行了 —— 被谁拉进群它都会开始记录")
	}
}
