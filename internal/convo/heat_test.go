package convo

import (
	"fmt"
	"math"
	"testing"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════════
// §6.7 热力 —— 叶子模块的测试。
//
// 主体是**反面用例** (§7.2 R11–R20): 每条断言旁边写清"把实现改坏成 X, 这条会红"。
// 正面用例只负责证明"机制确实会动" —— 没有反面用例的正面用例测的是同义反复。
//
// 辅助函数一律带 heatTest 前缀 (合并到主分支时不会与别的模块撞名)。
// ═══════════════════════════════════════════════════════════════════════════

// heatTestStore 开一个临时库。**故意不建热力表** —— 建表要走 EnsureHeatSchema,
// 而"没建表 → 读失败 → 退回常数配额"这条路本身也要测 (R13)。
func heatTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(t.TempDir(), "heat-test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// heatTestEnsured 开库 + 建表 (模拟主控已在 migrate 里接过一次之后的库)。
func heatTestEnsured(t *testing.T) *Store {
	t.Helper()
	s := heatTestStore(t)
	if err := EnsureHeatSchema(s); err != nil {
		t.Fatalf("EnsureHeatSchema: %v", err)
	}
	return s
}

// heatTestBase 全部用例共用的一个固定时刻 (不依赖 clock.Now, 免得测试跟着系统时间飘)。
func heatTestBase() time.Time { return time.Date(2026, 3, 1, 9, 0, 0, 0, time.Local) }

// heatTestFold 把一串发言人按 1 秒一条喂进场景, 返回每次的读数。
// **注意签名里只有发言人列表, 没有任何"按人分组"的入口** —— 这正是要测的东西。
func heatTestFold(t *testing.T, s *Store, scene string, speakers []string, base time.Time, cfg HeatConfig) []HeatSettle {
	t.Helper()
	out := make([]HeatSettle, 0, len(speakers))
	for i, sp := range speakers {
		res, err := s.RecordHeatInteraction(HeatInteraction{
			SceneKey:  scene,
			Speaker:   sp,
			Stage:     HeatStageFamiliar,
			SlotState: StateBusy, // 权重 1, 数字好读
			Now:       base.Add(time.Duration(i) * time.Second),
		}, cfg)
		if err != nil {
			t.Fatalf("第 %d 条入账失败: %v", i, err)
		}
		out = append(out, res)
	}
	return out
}

// ───────────────────────────────────────────────────────────────────────────
// 档位表与衰减
// ───────────────────────────────────────────────────────────────────────────

func TestHeat_GainLadderIsATable(t *testing.T) {
	cfg := HeatConfig{}.Normalized()
	if g := HeatGain(1, StateBusy, HeatMods{}, cfg); math.Abs(g-5.0) > 1e-9 {
		t.Fatalf("本会话第 1 条 = %.3f, 期望 gain_open 5.0", g)
	}
	if g := HeatGain(10, StateBusy, HeatMods{}, cfg); math.Abs(g-0.5) > 1e-9 {
		t.Fatalf("第 10 条 = %.3f, 期望 gain_floor 0.5", g)
	}
	if g := HeatGain(999, StateBusy, HeatMods{}, cfg); math.Abs(g-0.5) > 1e-9 {
		t.Fatalf("第 999 条 = %.3f, 期望仍是 gain_floor", g)
	}
	prev := math.Inf(1)
	for n := 1; n <= 9; n++ {
		g := HeatGain(n, StateBusy, HeatMods{}, cfg)
		if !(g < prev) {
			t.Fatalf("档位表第 %d 条 = %.3f, 不比上一条 %.3f 低 —— 收益递减断了", n, g, prev)
		}
		prev = g
	}
	// 反面: 把"第 10 条 = gain_floor"改成"gain_open × 第 10 档系数" ——
	// 换个 gain_open 之后第 10 条就不再是 gain_floor, 下面这条会红。
	// (形状表只定中间 8 档, 两头由 gain_open / gain_floor 给, 就是为了挡住这个。)
	cfg2 := cfg
	cfg2.GainOpen = 20
	if g := HeatGain(10, StateBusy, HeatMods{}, cfg2); math.Abs(g-0.5) > 1e-9 {
		t.Fatalf("gain_open=20 时第 10 条 = %.3f, 仍必须是 gain_floor 0.5", g)
	}
	if g := HeatGain(1, StateBusy, HeatMods{}, cfg2); math.Abs(g-20) > 1e-9 {
		t.Fatalf("gain_open=20 时第 1 条 = %.3f, 期望 20", g)
	}
	// 退化配置 (open < floor) 压成平档, 不许出现"越聊越凉"
	cfg3 := HeatConfig{GainOpen: 0.2, GainFloor: 2}
	if g := HeatGain(1, StateBusy, HeatMods{}, cfg3); g != 2 {
		t.Fatalf("退化配置下第 1 条 = %.3f, 期望压成 gain_floor 2", g)
	}
}

func TestHeat_DecaysOverTime(t *testing.T) {
	base := heatTestBase()
	st := HeatState{Value: 80, SettledTS: base.Format(bodyTSLayout)}
	// 反面 (R14): 把衰减删掉 (HeatAt 直接 return st.Value) → 这里得到 80, 红。
	got := HeatAt(st, base.Add(3*36*time.Hour), 36)
	if math.Abs(got-10.0) > 0.01 {
		t.Fatalf("3 个半衰期后 = %.3f, 期望 10 (80/8)", got)
	}
	// 现值必须**现算**: 同一个事实在同一个时刻读两次, 结果一样; 时间不同, 结果不同
	if a, b := HeatAt(st, base.Add(time.Hour), 36), HeatAt(st, base.Add(time.Hour), 36); a != b {
		t.Fatalf("同一时刻两次读到不同的现值: %.6f / %.6f", a, b)
	}
	// 时钟回拨不许反向放大 —— 反面: 改成 math.Pow(2, -d) 不判符号 → 这里会 > 80, 红
	if v := HeatAt(st, base.Add(-time.Hour), 36); v != 80 {
		t.Fatalf("时钟回拨后 = %.3f, 期望原样 80 (绝不放大)", v)
	}
	// 没有结算时刻 = 没有衰减锚点 → 原样返回, 不凭空归零 (失败不往沉默倒)
	if v := HeatAt(HeatState{Value: 80}, base, 36); v != 80 {
		t.Fatalf("结算时刻缺失 → %.3f, 期望原样 80", v)
	}
	// 半衰期零值 = 未提供 → 内置 36h —— 反面: 零值当"瞬间衰减到 0" → 这里得 0, 红
	if v := HeatAt(st, base.Add(36*time.Hour), 0); math.Abs(v-40) > 0.01 {
		t.Fatalf("半衰期未提供 → %.3f, 期望按内置 36h 得 40", v)
	}
}

func TestHeat_SessionGapStartsNewWindow(t *testing.T) {
	base := heatTestBase()
	last := base.Format(bodyTSLayout)
	if seq, isNew := HeatSessionSeq("", 0, base, 30); seq != 1 || !isNew {
		t.Fatalf("从未记过 → seq=%d new=%v, 期望 1/true", seq, isNew)
	}
	if seq, isNew := HeatSessionSeq(last, 4, base.Add(10*time.Minute), 30); seq != 5 || isNew {
		t.Fatalf("10 分钟后 → seq=%d new=%v, 期望 5/false", seq, isNew)
	}
	// 恰好等于 gap **不算**新会话 (边界取 >): 反面 —— 把 > 改成 >= → 这里得 1, 红
	if seq, _ := HeatSessionSeq(last, 4, base.Add(30*time.Minute), 30); seq != 5 {
		t.Fatalf("恰好 30 分钟 → seq=%d, 期望仍是同一会话的 5", seq)
	}
	// 反面: 去掉 gap 判定 (永远 prevSeq+1) → 这里得到 5, 红
	if seq, isNew := HeatSessionSeq(last, 4, base.Add(31*time.Minute), 30); seq != 1 || !isNew {
		t.Fatalf("31 分钟后 → seq=%d new=%v, 期望重新起会话 1/true", seq, isNew)
	}
	// 反面: 把"时钟回拨"也当成新会话 → 这里得 1, 红 (忙群每 tick 重吃顶格)
	if seq, _ := HeatSessionSeq(last, 4, base.Add(-time.Minute), 30); seq != 5 {
		t.Fatalf("时钟回拨 → seq=%d, 期望不重开窗口 (5)", seq)
	}
	// 时刻读不出 → 当新会话 (往"加温最大"倒, 不往沉默倒)
	if seq, isNew := HeatSessionSeq("not-a-time", 9, base, 30); seq != 1 || !isNew {
		t.Fatalf("时刻读不出 → seq=%d new=%v, 期望 1/true", seq, isNew)
	}
}

// ───────────────────────────────────────────────────────────────────────────
// R15 —— 本模块最承重的一条
// ───────────────────────────────────────────────────────────────────────────

func TestHeatGain_IsSceneSessionKeyed(t *testing.T) {
	base := heatTestBase()
	cfg := HeatConfig{}.Normalized()
	const n = 500

	// 两段**时间线逐秒相同**的消息流, 只有发言人不同:
	//   A: 500 个不同发言人各 1 条    B: 同一个人 500 条
	speakers := make([]string, n)
	for i := range speakers {
		speakers[i] = fmt.Sprintf("uin%d", 10000+i)
	}
	oneMan := make([]string, n)
	for i := range oneMan {
		oneMan[i] = "uin10000"
	}

	sA := heatTestEnsured(t)
	sB := heatTestEnsured(t)
	a := heatTestFold(t, sA, "group:1", speakers, base, cfg)
	b := heatTestFold(t, sB, "group:1", oneMan, base, cfg)

	// 反面 (R15): 把递增键改成「人 × 场景」(按发言人各记一份序号, 或按人各存一份热力)
	//   → A 里每个人永远都是"第 1 条"(Δ 恒 5.0), B 里逐条递减 → 下面第一条断言立刻红。
	for i := 0; i < n; i++ {
		if a[i].Seq != b[i].Seq {
			t.Fatalf("第 %d 条: 多发言人 seq=%d, 同一人 seq=%d —— 序号跟人有关了 (键必须是场景×会话)",
				i, a[i].Seq, b[i].Seq)
		}
		if math.Abs(a[i].Delta-b[i].Delta) > 1e-12 {
			t.Fatalf("第 %d 条: 多发言人 Δ=%.6f, 同一人 Δ=%.6f —— gain 的键含了人", i, a[i].Delta, b[i].Delta)
		}
	}
	// 再钉一层: 两边都不是"一起坏成常数" —— 递推真的在走
	if math.Abs(a[0].Delta-5.0) > 1e-9 || math.Abs(a[9].Delta-0.5) > 1e-9 {
		t.Fatalf("递减没走起来: 第 1 条 Δ=%.3f, 第 10 条 Δ=%.3f (期望 5 / 0.5)", a[0].Delta, a[9].Delta)
	}
	if a[0].Seq != 1 || a[9].Seq != 10 || a[499].Seq != 500 {
		t.Fatalf("序号不是场景内累计: %d/%d/%d", a[0].Seq, a[9].Seq, a[499].Seq)
	}
	// 发言人只在库里留审计痕迹 (heat_log.ref_uin), **不进梯度**
	rows, err := sA.RecentHeatLog("group:1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].RefUIN != "uin10499" {
		t.Fatalf("最近一条流水的 ref_uin = %q, 期望 uin10499 (发言人只该出现在审计里)", rows[0].RefUIN)
	}
}

// ───────────────────────────────────────────────────────────────────────────
// 上限 / 零值 / 槽位权重
// ───────────────────────────────────────────────────────────────────────────

func TestHeat_ClampedAtStageCap(t *testing.T) {
	base := heatTestBase()
	cfg := HeatConfig{}.Normalized()
	s := heatTestEnsured(t)
	capFamiliar := (HeatConfig{}).CapOf(HeatStageFamiliar)
	if capFamiliar != 60 {
		t.Fatalf("familiar 上限 = %.0f, 期望 60", capFamiliar)
	}
	var maxV float64
	clamped := 0
	for i := 0; i < 200; i++ {
		res, err := s.RecordHeatInteraction(HeatInteraction{
			SceneKey: "group:2", Speaker: "u1", Stage: HeatStageFamiliar,
			SlotState: StateFree, // 权重 3 → 单条就能加 15, 更快撞上限
			Now:       base.Add(time.Duration(i) * time.Second),
		}, cfg)
		if err != nil {
			t.Fatal(err)
		}
		maxV = math.Max(maxV, res.After)
		if res.Clamped {
			clamped++
		}
	}
	// 反面 (R17): 把 HeatClamp 删掉 → 存量会一路加到 300+, 这里红
	if maxV > capFamiliar+1e-9 {
		t.Fatalf("热力冲到 %.3f, 超过场景阶段上限 %.0f", maxV, capFamiliar)
	}
	if clamped == 0 {
		t.Fatalf("200 条互动一次都没被上限截断 —— 要么上限没生效, 要么样本不够")
	}
	// 落库的**存量**同样不许超上限 (现值 = 存量 × 衰减 ≤ 存量)
	st, ok, err := s.LoadHeat("group:2")
	if err != nil || !ok {
		t.Fatalf("读回存量: ok=%v err=%v", ok, err)
	}
	if st.Value > capFamiliar+1e-9 {
		t.Fatalf("落库存量 %.3f 超上限 %.0f", st.Value, capFamiliar)
	}
	// 上限是**场景级**的: 同一个库、同一段时间线, intimate 场景该能更热
	// (这不是"给大群加戏", 是同一张档位表在不同 stage 下取值 —— A3 允许的规模差异)
	if (HeatConfig{}).CapOf(HeatStageIntimate) <= (HeatConfig{}).CapOf(HeatStageStranger) {
		t.Fatalf("阶段档位表不是单调的: stranger=%.0f intimate=%.0f",
			(HeatConfig{}).CapOf(HeatStageStranger), (HeatConfig{}).CapOf(HeatStageIntimate))
	}
}

func TestHeat_CapTable(t *testing.T) {
	if (HeatConfig{}).CapOf(HeatStageStranger) != 20 || (HeatConfig{}).CapOf(HeatStageFamiliar) != 60 || (HeatConfig{}).CapOf(HeatStageIntimate) != 100 {
		t.Fatalf("档位表 = %.0f/%.0f/%.0f, 期望 20/60/100",
			(HeatConfig{}).CapOf(HeatStageStranger), (HeatConfig{}).CapOf(HeatStageFamiliar), (HeatConfig{}).CapOf(HeatStageIntimate))
	}
	// 未知 / 空白阶段 → 关系阶梯的下界 (与 convo_scene.stage DEFAULT 'stranger' 一致)
	if (HeatConfig{}).CapOf("") != 20 || (HeatConfig{}).CapOf("nonsense") != 20 {
		t.Fatalf("未知阶段没有落到 stranger: ''=%.0f nonsense=%.0f", (HeatConfig{}).CapOf(""), (HeatConfig{}).CapOf("nonsense"))
	}
	if (HeatConfig{}).CapOf("Familiar") != 60 || (HeatConfig{}).CapOf(" familiar ") != 60 {
		t.Fatalf("大小写/空白敏感: Familiar=%.0f", (HeatConfig{}).CapOf("Familiar"))
	}
	// 配置覆盖 + 零值兜底: 反面 —— 把 ≤0 的 cap 当成"上限为 0" → 这里得 0, 红
	cfg := HeatConfig{CapStranger: 7}
	if got := cfg.CapOf(HeatStageStranger); got != 7 {
		t.Fatalf("配置覆盖失效: %.0f", got)
	}
	if got := (HeatConfig{}).CapOf(HeatStageFamiliar); got != 60 {
		t.Fatalf("零值配置没兜成默认: %.0f", got)
	}
}

func TestHeat_ClampZeroCapMeansUnset(t *testing.T) {
	// 反面 (纪律 #1): 把 cap ≤ 0 当成"上限为零" → 这里得 0, 红。
	// 零值的含义是"未提供上限", 不是"一点热力都不许有"。
	if v := HeatClamp(50, 0); v != 50 {
		t.Fatalf("cap 未提供 (0) 时被压到 %.0f, 期望原样 50", v)
	}
	if v := HeatClamp(50, 20); v != 20 {
		t.Fatalf("cap=20 时 = %.0f, 期望 20", v)
	}
	if v := HeatClamp(-3, 20); v != 0 {
		t.Fatalf("负存量 = %.0f, 期望压到 0", v)
	}
	if v := HeatClamp(7, 20); v != 7 {
		t.Fatalf("未触顶时被改动: %.0f", v)
	}
}

func TestHeat_SlotStateWeights(t *testing.T) {
	cfg := HeatConfig{}.Normalized()
	busy := HeatGain(1, StateBusy, HeatMods{}, cfg)
	slack := HeatGain(1, StateSlack, HeatMods{}, cfg)
	free := HeatGain(1, StateFree, HeatMods{}, cfg)
	focus := HeatGain(1, StateFocus, HeatMods{}, cfg)
	unknown := HeatGain(1, "nonsense", HeatMods{}, cfg)
	if busy != 5 || slack != 10 || free != 15 || focus != 0 || unknown != 0 {
		t.Fatalf("槽位权重 = busy %.1f / slack %.1f / free %.1f / focus %.1f / 未知 %.1f, 期望 5/10/15/0/0",
			busy, slack, free, focus, unknown)
	}
	// §2.4 的精化: **零值语义按"整个键是否存在"判定, 不按单个条目的数值**。
	// 反面 A: 把"表里某项为 0"当成"未提供" → free 会回落内置 3 → 得 15, 红。
	// 反面 B: 把"缺键"直接当 0 → busy 得 0 (忙的时候热力永远不涨), 红。
	cfg2 := HeatConfig{GainByState: map[string]float64{StateFree: 0}}
	if g := HeatGain(1, StateFree, HeatMods{}, cfg2); g != 0 {
		t.Fatalf("显式写 0 的槽位 = %.3f, 期望 0 (合法取值, 不是未提供)", g)
	}
	if g := HeatGain(1, StateBusy, HeatMods{}, cfg2); g != 5 {
		t.Fatalf("表里缺的键 = %.3f, 期望回落内置 busy 权重 (5)", g)
	}
	// 负权重没有"降温通道"的含义 → 夹到 0 (热力只由时间衰减来降)
	cfg3 := HeatConfig{GainByState: map[string]float64{StateFree: -3}}
	if g := HeatGain(1, StateFree, HeatMods{}, cfg3); g != 0 {
		t.Fatalf("负权重 = %.3f, 期望夹到 0", g)
	}
}

func TestHeat_ModsAndOwnerCap(t *testing.T) {
	cfg := HeatConfig{}.Normalized()
	base := HeatGain(1, StateBusy, HeatMods{}, cfg) // 5.0
	if g := HeatGain(1, StateBusy, HeatMods{MoodLow: true}, cfg); math.Abs(g-base*0.5) > 1e-9 {
		t.Fatalf("心情低 = %.3f, 期望减半 %.3f", g, base*0.5)
	}
	if g := HeatGain(1, StateBusy, HeatMods{BodyUnwell: true}, cfg); math.Abs(g-base*0.5) > 1e-9 {
		t.Fatalf("身体不适 = %.3f, 期望减半 %.3f", g, base*0.5)
	}
	if g := HeatGain(1, StateBusy, HeatMods{MoodLow: true, BodyUnwell: true}, cfg); math.Abs(g-base*0.25) > 1e-9 {
		t.Fatalf("心情差+生病 = %.3f, 期望 %.3f", g, base*0.25)
	}
	if g := HeatGain(1, StateBusy, HeatMods{EnvGood: true}, cfg); math.Abs(g-base*1.1) > 1e-9 {
		t.Fatalf("环境好 = %.3f, 期望 %.3f", g, base*1.1)
	}
	// 反面 (纪律 #1): OwnerMult 零值当成"0 倍" → 这里得 0 (热力永远攒不起来), 红
	if g := HeatGain(1, StateBusy, HeatMods{OwnerMult: 0}, cfg); math.Abs(g-base) > 1e-9 {
		t.Fatalf("倍率未提供 = %.3f, 期望无倍率 %.3f", g, base)
	}
	if g := HeatGain(1, StateBusy, HeatMods{OwnerMult: 3}, cfg); math.Abs(g-base*3) > 1e-9 {
		t.Fatalf("倍率 3 = %.3f, 期望 %.3f", g, base*3)
	}
	// 反面 (§5.6 硬上限 5×): 去掉 clamp → 这里得 495, 红
	if g := HeatGain(1, StateBusy, HeatMods{OwnerMult: 99}, cfg); math.Abs(g-base*5) > 1e-9 {
		t.Fatalf("倍率 99 = %.3f, 期望被 §5.6 硬上限压到 %.3f", g, base*5)
	}
	// 序号未提供 (0) → 按第 1 条 (顶格): 反面 —— 按 0 或负数算 → 这里不是 5, 红
	if g := HeatGain(0, StateBusy, HeatMods{}, cfg); math.Abs(g-5.0) > 1e-9 {
		t.Fatalf("序号未提供 = %.3f, 期望顶格 5.0", g)
	}
}

// ───────────────────────────────────────────────────────────────────────────
// 落点: 配额 (R11 / R12 / R18 / R19 / R20)
// ───────────────────────────────────────────────────────────────────────────

func TestHeat_LandsOnQuotaNotJitter(t *testing.T) {
	// 热力的唯一出口是**条数** (整数硬约束)。
	//
	// 反面 (R11): 把落点从配额挪到 OpenJitterPct(概率缩放) —— 那需要本模块
	// 收/发一个概率, 而本模块签名里**没有任何概率**; 同时"热力 → 条数"这张
	// 阶跃表不再成立 (抖动必过时条数不随热力变), 下面逐格对不上 → 红。
	//
	// ⚠️ 真正的 jitter 耦合要在 DecideTick 里测 (roll=0、热力从满到 0、条数从 N 到 0),
	// 那属于主控接线的范围, 本叶子模块只能钉住"出口是条数"这一半。
	cases := []struct {
		heat float64
		want int
	}{
		{0, 0}, {10, 0}, {24.9, 0}, {25, 1}, {49.9, 1}, {50, 2}, {100, 2}, {1e9, 2},
	}
	for _, c := range cases {
		if q := HeatQuota(c.heat, 25, 2); q != c.want {
			t.Errorf("热力 %.1f → %d 条, 期望 %d", c.heat, q, c.want)
		}
	}
	// 热力拉满也不许越红线: 反面 (R19) —— 去掉与 dailyCap 的 min → 这里得 1e6, 红
	if q := HeatQuota(1e9, 25, 2); q != 2 {
		t.Fatalf("满热 → %d 条, 期望被日上限 2 压住", q)
	}
	if q := HeatQuotaFrom(HeatQuotaInput{DailyCap: 2, Heat: 1e9, Provided: true, CostPerMsg: 25}); q != 2 {
		t.Fatalf("满热 (走完整落点) → %d 条, 期望 2", q)
	}
	// 反面: 极大热力下 float→int 越界会给出负数 (= 她一整天不说话) → 这里红
	if q := HeatQuota(math.MaxFloat64, 25, 3); q != 3 {
		t.Fatalf("极大热力 → %d 条, 期望 3", q)
	}
}

func TestHeat_AbsentFallsBackToConstantQuota(t *testing.T) {
	// 反面 (R12): 把"未提供热力"当成 heat=0 → 返回 0 → 她会整天不说话, 红。
	// §2.5: HasHeat=false 必须与升级前逐字节相同 (走 cfg.ActiveQuotaDay 路径)。
	if q := HeatQuotaFrom(HeatQuotaInput{DailyCap: 2, Heat: 0, Provided: false}); q != 2 {
		t.Fatalf("未提供热力 → %d 条, 期望常数配额 2", q)
	}
	// 而"提供过、且真的是 0"是**另一条路**: 预算 0 → 不主动 (设计稿的意图, 不是故障)
	if q := HeatQuotaFrom(HeatQuotaInput{DailyCap: 2, Heat: 0, Provided: true}); q != 0 {
		t.Fatalf("热力真的是 0 → %d 条, 期望 0 (陌生 cap 20 → 0 条)", q)
	}
	// 未提供时, 心情/身体的减法照常生效 (常数配额路径不是"绕过减法")
	if q := HeatQuotaFrom(HeatQuotaInput{DailyCap: 2, MoodCut: 1, Provided: false}); q != 1 {
		t.Fatalf("未提供热力 + 心情差 → %d 条, 期望 1", q)
	}
}

func TestHeat_ReadFailureFallsBackToConstantQuota(t *testing.T) {
	base := heatTestBase()
	s := heatTestStore(t)
	// ⚠️ 这里**显式把表删掉**, 而不是指望"OpenStore 还没建表" ——
	// 主控把 EnsureHeatSchema 接进 migrate 之后, 那个前提就没了 (实测: 接线后本用例变红)。
	// 显式造出"表不存在"这个局面, 测的东西才和接线状态无关。
	if _, err := s.DB.Exec("DROP TABLE IF EXISTS convo_heat"); err != nil {
		t.Fatalf("造局面失败: %v", err)
	}
	if _, err := s.DB.Exec("DROP TABLE IF EXISTS convo_heat_log"); err != nil {
		t.Fatalf("造局面失败: %v", err)
	}

	// 先钉住"读失败必须出声": 不是静默的 ok=false。
	// 反面: 把表不存在也当成"没结算过"(ok=false, err=nil) → 接线漏了永远看不出来, 红。
	if _, ok, err := s.LoadHeat("group:1"); err == nil {
		t.Fatalf("表不存在时 LoadHeat 竟然不报错 (ok=%v) —— 接线漏了会看不出", ok)
	}

	// 反面 (R13): 读库失败时 return 0 (当成没热力) → 这里得到 0 条, 红。
	// 失败必须往"正常说话"倒: 退回常数配额。
	in := HeatQuotaInput{DailyCap: 2, Provided: true} // 就算调用方声称有热力, 读不出来也不许沉默
	if q := s.HeatQuotaAt("group:1", base, HeatConfig{}.Normalized(), in); q != 2 {
		t.Fatalf("读库失败 → %d 条, 期望退回常数配额 2", q)
	}
	// 对照组: 表建好、没记录 = 热力 0 = 预算 0 (这不是失败, 与上面那条必须分开)
	s2 := heatTestEnsured(t)
	if q := s2.HeatQuotaAt("group:1", base, HeatConfig{}.Normalized(), in); q != 0 {
		t.Fatalf("库里没有该场景 → %d 条, 期望 0 (从未互动过 = 没有热力)", q)
	}
}

func TestHeat_DailyCapStillBinds(t *testing.T) {
	// 反面 (R19): 把 min(dailyCap, heatBudget) 改成只取 heatBudget →
	// 满热会给出 40000000 条, 下面两条都红 (§6.7:943「打扰红线不因热力高而放开」)。
	if q := HeatQuota(1e9, 25, 2); q != 2 {
		t.Fatalf("满热 → %d 条, 期望 2", q)
	}
	if q := HeatQuotaFrom(HeatQuotaInput{DailyCap: 1, Heat: 1e6, Provided: true, CostPerMsg: 25}); q != 1 {
		t.Fatalf("日上限 1 + 满热 → %d 条, 期望 1", q)
	}
	// 热力不够时, 热力那一侧照样能压住
	if q := HeatQuota(30, 25, 5); q != 1 {
		t.Fatalf("热力 30 → %d 条, 期望 1 (⌊30/25⌋)", q)
	}
}

func TestHeat_MoodCutStillBindsAtFullHeat(t *testing.T) {
	// 反面 (R20): 把 min 挪到心情/身体减法**之前** → 满热会把减掉的 1 顶回来,
	// 下面第一条得 2 而不是 1, 红。这就是「打扰红线不因热力高而放开」。
	full := HeatQuotaInput{DailyCap: 2, MoodCut: 1, Heat: 1e6, Provided: true}
	if q := HeatQuotaFrom(full); q != 1 {
		t.Fatalf("满热 + 心情差 → %d 条, 期望 1 (减法必须在取 min 之前)", q)
	}
	// 身体档位 (-2) 同理, 且与心情叠加
	if q := HeatQuotaFrom(HeatQuotaInput{DailyCap: 2, BodyCut: -2, Heat: 1e6, Provided: true}); q != 0 {
		t.Fatalf("满热 + 生病 -2 → %d 条, 期望 0", q)
	}
	if q := HeatQuotaFrom(HeatQuotaInput{DailyCap: 2, MoodCut: 1, BodyCut: -2, Heat: 1e6, Provided: true}); q != 0 {
		t.Fatalf("满热 + 心情差 + 生病 → %d 条, 期望 0", q)
	}
	// 决定性的一格: 热力预算**落在"减法前"和"减法后"的日上限之间**。
	// 正确顺序 → 4 减 1 = 3, 取 min(3, 3) = 3; 把 min 挪到减法之前 → min(5,3) = 3, 再减 1 = 2。
	// 上面那几条"满热"的用例两序同值, 分不出实现 —— **只有这一格能**。
	if q := HeatQuotaFrom(HeatQuotaInput{DailyCap: 5, MoodCut: 1, Heat: 75, Provided: true, CostPerMsg: 25}); q != 3 {
		t.Fatalf("热力预算 3、日上限 5、心情减 1 → %d 条, 期望 3 (先减法)", q)
	}
	// 减法把配额压到负数时夹到 0, 不许出现"负条数"
	if q := HeatQuotaFrom(HeatQuotaInput{DailyCap: 1, MoodCut: 1, BodyCut: -3, Provided: false}); q != 0 {
		t.Fatalf("减法过头 → %d 条, 期望 0", q)
	}
}

func TestHeat_CostZeroMeansDefault(t *testing.T) {
	// 反面 (R18): 把 cost ≤ 0 处理成"0 = 免费" → 这里得 10 条, 红。
	// 一行配置漏写就等于把热力闸门拆了, 而现象只是"她忽然话变多", 看不出是配置坏了。
	if q := HeatQuota(100, 0, 10); q != 4 {
		t.Fatalf("cost 未配置 → %d 条, 期望 4 (= ⌊100/25⌋)", q)
	}
	if q := HeatQuota(100, -1, 10); q != 4 {
		t.Fatalf("cost 为负 → %d 条, 期望 4", q)
	}
	if q := HeatQuota(100, 25, 10); q != 4 {
		t.Fatalf("cost=25 → %d 条, 期望 4", q)
	}
	if cfg := (HeatConfig{}).Normalized(); cfg.CostPerMsg != HeatDefaultCostPerMsg {
		t.Fatalf("零值配置的 cost = %.1f, 期望 %.1f", cfg.CostPerMsg, HeatDefaultCostPerMsg)
	}
	// 走完整落点同样按 25 算 (60/25 = 2)
	if q := HeatQuotaFrom(HeatQuotaInput{DailyCap: 5, Heat: 60, Provided: true}); q != 2 {
		t.Fatalf("cost 未配置时满落点 → %d 条, 期望 2", q)
	}
}

// ───────────────────────────────────────────────────────────────────────────
// 回话不花热力 / 持久化 / 审计流水
// ───────────────────────────────────────────────────────────────────────────

func TestHeat_ReplyNeverSpendsHeat(t *testing.T) {
	base := heatTestBase()
	cfg := HeatConfig{}.Normalized()
	s := heatTestEnsured(t)
	// ⚠️ 用 intimate (cap 100) 而不是 stranger (cap 20): 后者会被上限压住,
	// 于是"每条回话扣 25"也会被 0 地板吞掉 —— 测试就分辨不出来了。
	prev := 0.0
	for i := 0; i < 5; i++ {
		// 回话 = 场景内的一次互动 → 走 RecordHeatInteraction (只加不减)
		res, err := s.RecordHeatInteraction(HeatInteraction{
			SceneKey: "private:1", Speaker: "owner", Stage: HeatStageIntimate,
			SlotState: StateFree, Now: base.Add(time.Duration(i) * time.Minute),
		}, cfg)
		if err != nil {
			t.Fatal(err)
		}
		// 反面 (R16 的叶子级等价物): 把回话也接进 SpendHeat (即 delta 里减 cost) →
		// 第 2 条就会从 15 掉到 2, 这里红。
		// (§4.4: 被 @ 必答是硬门槛; 回话若也扣, 忙群永远攒不出 25 → 大群必然变哑巴。)
		if res.After < prev-1e-9 {
			t.Fatalf("第 %d 条回话把热力从 %.3f 降到 %.3f —— 回话不许扣热力", i, prev, res.After)
		}
		prev = res.After
	}
	if prev < 40 {
		t.Fatalf("五条互动之后热力只有 %.3f —— 加温没走起来, 这条测试就分辨不出'回话扣费'了", prev)
	}
	// 只有**主动发起**才减, 且减的就是 cost 那个量级 (不是"随便掉一点")
	sp, err := s.SpendHeat("private:1", base.Add(time.Hour), HeatStageIntimate, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !(sp.After < sp.Before) {
		t.Fatalf("主动发起没有扣热力: before=%.3f after=%.3f", sp.Before, sp.After)
	}
	if d := sp.Before - sp.After; math.Abs(d-25) > 0.5 {
		t.Fatalf("主动发起扣了 %.3f, 期望 ≈25", d)
	}
	if sp.After <= 0 {
		t.Fatalf("攒起来的热力被扣到了地板: %.3f (扣费不该越界)", sp.After)
	}
}

func TestHeat_StoreRoundTripAndIdempotentSchema(t *testing.T) {
	base := heatTestBase()
	s := heatTestStore(t)
	if err := EnsureHeatSchema(s); err != nil {
		t.Fatal(err)
	}
	// 幂等: 重复调用无副作用 (老库升级路径会走两遍)
	if err := EnsureHeatSchema(s); err != nil {
		t.Fatalf("重复建表不幂等: %v", err)
	}
	// 没有记录 = 从未结算过, 且**读操作不写库**
	if _, ok, err := s.LoadHeat("group:9"); err != nil || ok {
		t.Fatalf("空库读回: ok=%v err=%v", ok, err)
	}

	// 落库的只有事实: 结算值 + 结算时刻 + 会话计数器
	ts := base.Format(bodyTSLayout)
	if err := s.SaveHeat(HeatState{SceneKey: "group:9", Value: 42, SettledTS: ts,
		CapAtSettle: 60, SessionSeq: 7, LastMsgTS: ts}); err != nil {
		t.Fatal(err)
	}
	st, ok, err := s.LoadHeat("group:9")
	if err != nil || !ok {
		t.Fatalf("读回: ok=%v err=%v", ok, err)
	}
	if st.Value != 42 || st.SettledTS != ts || st.SessionSeq != 7 || st.LastMsgTS != ts {
		t.Fatalf("读回的事实不一致: %+v", st)
	}
	// **读出来的不是现值**: 现值要现算。反面 —— 把衰减结果也落库 (存派生量) →
	// 这里读回的值就不再是当初写下去的 42, 红。
	if v := HeatAt(st, base.Add(72*time.Hour), 36); math.Abs(v-10.5) > 0.01 {
		t.Fatalf("2 个半衰期后现值 = %.3f, 期望 10.5 (42/4)", v)
	}
	// ⚠️ 这里原来还有一段「惰性结算」(调 `SettleHeat` 把现值写回事实) —— 已删。
	//
	// 上面那条「**读出来的不是现值**: 现值要现算」已经覆盖了它的实质;
}

func TestHeat_LogRecordsActualDelta(t *testing.T) {
	base := heatTestBase()
	cfg := HeatConfig{}.Normalized()
	s := heatTestEnsured(t)

	// 场景 A: 一次互动 (stranger cap 20, free 权重 3 → 15), 再一次 → 名义 11.7 但被上限截断
	for i := 0; i < 2; i++ {
		if _, err := s.RecordHeatInteraction(HeatInteraction{
			SceneKey: "group:8", Speaker: fmt.Sprintf("u%d", i), Stage: HeatStageStranger,
			SlotState: StateFree, Now: base.Add(time.Duration(i) * time.Second),
		}, cfg); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.RecentHeatLog("group:8", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("流水 %d 条, 期望 2", len(rows))
	}
	// 最新一条 = 被上限截断的那次: reason 换成 cap_clamp, delta 记**实际变化**不是名义增益
	// 反面: 把 delta 记成名义值 (11.7) 或 reason 永远是 gain_interaction → 这里红
	if rows[0].Reason != HeatReasonCapClamp {
		t.Fatalf("被截断那次的 reason = %q, 期望 %q", rows[0].Reason, HeatReasonCapClamp)
	}
	if math.Abs(rows[0].Delta-5.0) > 0.01 || math.Abs(rows[0].After-20.0) > 0.01 {
		t.Fatalf("截断那次的 delta/after = %.3f/%.3f, 期望 5/20", rows[0].Delta, rows[0].After)
	}
	if rows[1].Reason != HeatReasonGain || math.Abs(rows[1].Delta-15.0) > 0.01 {
		t.Fatalf("首条互动的流水 = %q %.3f, 期望 gain_interaction 15", rows[1].Reason, rows[1].Delta)
	}
	// 审计里能看到是谁说的话 (只进流水, 不进梯度)
	if rows[0].RefUIN != "u1" || rows[1].RefUIN != "u0" {
		t.Fatalf("流水的 ref_uin = %q/%q, 期望 u1/u0 (最新在前)", rows[0].RefUIN, rows[1].RefUIN)
	}
	if rows[0].Seq != 2 || rows[1].Seq != 1 {
		t.Fatalf("流水的会话序号 = %d/%d, 期望 2/1", rows[0].Seq, rows[1].Seq)
	}

	// 扣费那条: delta 是实际的 (15 → 0 只掉 15, 不是名义的 25)
	if _, err := s.SpendHeat("group:8", base.Add(time.Hour), HeatStageStranger, cfg); err != nil {
		t.Fatal(err)
	}
	rows2, err := s.RecentHeatLog("group:8", 1)
	if err != nil {
		t.Fatal(err)
	}
	// 现值 ≈ 20 × 2^(-1h/36h) ≈ 19.62, 扣 25 会被 0 地板兜住 → delta 是实际的 -19.62
	if rows2[0].Reason != HeatReasonCost || math.Abs(rows2[0].Delta+19.62) > 0.05 {
		t.Fatalf("扣费流水 = %q %.3f, 期望 cost_initiate ≈ -19.62 (地板兜住, 不是名义 -25)",
			rows2[0].Reason, rows2[0].Delta)
	}
}
