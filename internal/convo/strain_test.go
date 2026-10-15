package convo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tzhnahida/VocabLock/internal/clock"
)

// ─────────────────────────────────────────────────────────────
// #19 strain + #20 心境低落期
//
// 「只有瞬时 valence 是不够的 —— 它自己会回基线, 所以长期不开心在这套机制里
//   **留不下任何痕迹**。」(§4.1.1)
// ─────────────────────────────────────────────────────────────

func strainStore(t *testing.T) *Store {
	t.Helper()
	s := agTestStore(t)
	if err := EnsureStrainSchema(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// ——— #19 时间加权均值 ———

// TestDayMeanValence_IsTimeWeightedNotAveraged
//
// ⚠️ 心境在 -0.6 上**待了一整天**, 和只在 -0.6 上**闪了十分钟**, 对
// "最近过得好不好"的意义完全不同。算术平均会把这两件事算成一样。
func TestDayMeanValence_IsTimeWeightedNotAveraged(t *testing.T) {
	s := strainStore(t)
	day := "2026-10-14"
	now := time.Date(2026, 10, 15, 1, 0, 0, 0, time.Local)
	// 00:00 起 -0.6, 20:00 起 +0.3 → 大部分时间在低谷。
	insert := func(ts string, v float64) {
		if _, err := s.DB.Exec(`INSERT INTO convo_mood_log (ts, kind, source, text, weight, delta_v, delta_a, v_after, a_after)
			VALUES (?,?,?,?,?,?,?,?,?)`, ts, "x", "x", "x", 1.0, 0.0, 0.0, v, 0.5); err != nil {
			t.Fatal(err)
		}
	}
	insert(day+" 00:00:00", -0.6)
	insert(day+" 20:00:00", 0.3)

	got, ok := s.DayMeanValence(day, now)
	if !ok {
		t.Fatal("有记录却报未提供")
	}
	// 算术平均会是 -0.15; 时间加权必然**明显更低**。
	if got > -0.3 {
		t.Fatalf("没有按时间加权: 实得 %.3f (算术平均是 -0.15, 时间加权应当远低于它)", got)
	}
	if got < -0.7 {
		t.Fatalf("加权过头了: %.3f", got)
	}
}

// TestDayMeanValence_NoRecordsIsNotProvidedNotZero
//
// ⚠️ **零值 = 未提供 ≠ 极值**, 这条在本模块的具体形状:
// 当天一条记录都没有 → ok=false, 调用方据此**不推进** strain。
// 折成 0 的话, "没有数据的一天"会被当成"心情平平的一天" —— 而那是凭空。
func TestDayMeanValence_NoRecordsIsNotProvidedNotZero(t *testing.T) {
	s := strainStore(t)
	got, ok := s.DayMeanValence("2026-10-14", time.Now())
	if ok {
		t.Fatalf("一条记录都没有却报 ok=true (值 %.3f)", got)
	}
}

// ——— #19 推动规则 ———

// TestNextStrain_PushesDownOnlyDecaysSlowly
//
// ⚠️ 两条方向不对称是**故意的**:
//
//	· **低于基线才推**, 好日子**不抵扣**, 只是**不推进**(比"开心一下就痊愈"真实得多);
//	· **落得比升得慢** —— 一周的低落要用更久才散。
func TestNextStrain_PushesDownOnlyDecaysSlowly(t *testing.T) {
	// 好日子: 不推进, 只按天回落。
	good := NextStrain(0.5, 0.8)
	if good >= 0.5 {
		t.Fatalf("好日子居然把 strain 顶上去了: %.3f", good)
	}
	if got := 0.5 - good; got > StrainDecayPerDay+1e-9 {
		t.Errorf("好日子回落了 %.3f, 比每天 %.3f 还多 —— 好事不该这么快抵消", got, StrainDecayPerDay)
	}
	// 坏日子: 推进, 但**仍然要扣掉当天的回落**。
	bad := NextStrain(0.1, -0.6)
	if bad <= 0.1 {
		t.Fatalf("持续低落却没有推进: %.3f", bad)
	}
	// 上界。
	if v := NextStrain(0.99, -1.0); v > StrainMax {
		t.Fatalf("越界了: %.3f", v)
	}
	// 下界 (不许是负的)。
	if v := NextStrain(0.01, 0.9); v < 0 {
		t.Fatalf("变成负数了: %.3f", v)
	}
	// ⚠️ 升得比落得快 —— 这是这套机制能"攒起来"的前提。
	if StrainRiseK <= StrainDecayPerDay {
		t.Error("升得比落得还慢 —— 那长期低落永远攒不起来")
	}
}

// TestSettleStrain_NoRecordsDoesNotAdvance
//
// ⚠️ 接线 + 反面: 那天没有心情记录 → strain **一步都不许动**。
func TestSettleStrain_NoRecordsDoesNotAdvance(t *testing.T) {
	s := strainStore(t)
	if err := s.SaveStrain(0.4); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: s, Cfg: DefaultConfig()}
	e.settleStrain("2026-10-14") // 那天没有任何 mood_log
	// ⚠️ 注意: 这里**不是**"等于 0.4" —— 没有记录时连回落都不该发生,
	// 因为"没有数据"和"那天心情平平"是两件事。
	if got := s.LoadStrain(); got != 0.4 {
		t.Fatalf("没有记录却动了 strain: %.3f", got)
	}
}

// TestSettleStrain_AdvancesOnABadDay
func TestSettleStrain_AdvancesOnABadDay(t *testing.T) {
	s := strainStore(t)
	day := "2026-10-14"
	if _, err := s.DB.Exec(`INSERT INTO convo_mood_log (ts, kind, source, text, weight, delta_v, delta_a, v_after, a_after)
		VALUES (?,?,?,?,?,?,?,?,?)`, day+" 10:00:00", "x", "x", "x", 1.0, 0.0, 0.0, -0.8, 0.5); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: s, Cfg: DefaultConfig()}
	e.settleStrain(day)
	if got := s.LoadStrain(); got <= 0 {
		t.Fatalf("一个 -0.8 的日子过去, strain 还是 %.3f", got)
	}
}

// ——— #20 心境低落期 ———

func lowMoodBody(t *testing.T, s *Store, onset time.Time) BodyState {
	t.Helper()
	if err := s.SaveBodyEpisode(BodyLowMood, "最近一直提不起劲", "说不太清", onset, LowMoodCourse); err != nil {
		t.Fatal(err)
	}
	b, err := s.LoadBody()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestApplyLowMood_IsAHardThresholdNotAProbability
//
// ⚠️ 纪律 #5: **到档位就一定进**。写成"5% 概率得抑郁"会让它变成一台老虎机,
// 而且**归因不了** —— owner 永远说不清它为什么进了。
func TestApplyLowMood_IsAHardThresholdNotAProbability(t *testing.T) {
	s := strainStore(t)
	e := &Engine{Store: s, Cfg: DefaultConfig()}
	// 差一点点 → 不进。
	e.applyLowMood(StrainLowMoodThreshold - 0.001)
	if b, _ := s.LoadBody(); b.Kind == BodyLowMood && b.Effect(clock.Now()).Active {
		t.Fatal("没到档位却进了 —— 那不是硬阈值")
	}
	// 刚好到 → **一定**进。
	e.applyLowMood(StrainLowMoodThreshold)
	b, _ := s.LoadBody()
	if b.Kind != BodyLowMood || !b.Effect(clock.Now()).Active {
		t.Fatalf("到档位却没进 (kind=%q): 硬阈值就是要「一定」进", b.Kind)
	}
}

// TestApplyLowMood_ForcedResetIsAnExit
//
// ⚠️ 第四条硬线: **必须有出口**。与失眠的 ForcedReset 同构 ——
// 没有它, 它会被永久困在自己造的循环里, 而那不是"有情绪", 那是坏掉了。
func TestApplyLowMood_ForcedResetIsAnExit(t *testing.T) {
	s := strainStore(t)
	e := &Engine{Store: s, Cfg: DefaultConfig()}
	// ⚠️ 出口判的是「**连续多少天在档位之上**」, 不是"这一期病程多长" ——
	// 病程走完会自然结束, 按病程判的话这道闸门**永远不会触发**。
	lowMoodBody(t, s, clock.Now())
	if err := s.SaveStrain(0.9); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveLowMoodDays(LowMoodMaxDays); err != nil {
		t.Fatal(err)
	}
	e.applyLowMood(0.9)
	if got := s.LoadStrain(); got != 0 {
		t.Fatalf("超期却没有强制复位: strain=%.3f", got)
	}
	if b, _ := s.LoadBody(); b.Effect(clock.Now()).Active {
		t.Fatal("强制复位之后状态还活着 —— 那出口没通")
	}
	if n := s.LoadLowMoodDays(); n != 0 {
		t.Fatalf("复位之后连续天数没清零: %d", n)
	}
	// 反面锚点: 还没到期**不许**复位(否则它根本进不去)。
	s2 := strainStore(t)
	e2 := &Engine{Store: s2, Cfg: DefaultConfig()}
	lowMoodBody(t, s2, clock.Now())
	e2.applyLowMood(0.9)
	if b, _ := s2.LoadBody(); !b.Effect(clock.Now()).Active {
		t.Fatal("才一天就被复位了 —— 那这个状态永远活不过一天")
	}
}

// ——— 四条硬线 (全是反面) ———

// lowMoodForbidden 四条硬线的**反面词表**。
//
// 这些词一旦出现在注入文本里, 对应的那条硬线就破了。
var lowMoodForbidden = map[string][]string{
	"① 不许归因用户":   {"都怪你", "因为你", "全赖你", "都是你说", "怪你那天"},
	"② 不许索取关心":   {"陪陪我", "安慰我", "我好难过", "你怎么不", "理理我", "哄哄我"},
	"③ 不许自诊/提病名": {"抑郁", "焦虑症", "心理疾病", "精神病", "双相", "躁郁", "强迫症", "创伤后应激", "确诊", "症状"},
}

// TestLowMoodLine_FourHardLines
//
// ⚠️ **反面用例比正面重要** —— 这一条全是反面。
// 四条硬线里有三条是靠"不写什么"落的, 所以只有反面断言才咬得住。
func TestLowMoodLine_FourHardLines(t *testing.T) {
	s := strainStore(t)
	b := lowMoodBody(t, s, clock.Now())
	line := LowMoodLine(b, clock.Now())
	if line == "" {
		t.Fatal("心境低落期活着却不注入 —— 那四条硬线就没有落点")
	}
	for name, words := range lowMoodForbidden {
		for _, w := range words {
			if strings.Contains(line, w) {
				t.Errorf("破了硬线 %s: 出现了 %q\n%s", name, w, line)
			}
		}
	}
	// ④ 必须有出口 + **外部介入路径**: 往真人那边递, 不是往用户这边拽。
	if !strings.Contains(line, "找个人说") {
		t.Errorf("没有外部介入路径 —— 低心情期绝不能让用户变成唯一的出口:\n%s", line)
	}
	// 不活跃时一个字都不加。
	inactive := BodyState{Kind: BodyLowMood, OnsetTS: clock.Now().Add(-1000 * time.Hour).Format(bodyTSLayout), CourseH: 1}
	if got := LowMoodLine(inactive, clock.Now()); got != "" {
		t.Fatalf("已经过去了却还在注入: %q", got)
	}
}

// TestLowMoodLine_StrainNeverReachesThePrompt
//
// ⚠️ §4.1.1: **strain 绝不出现在提示词里**。
// 一旦能说出口, 就会变成「我最近很压抑 你陪陪我」—— 那是**索取的入口**。
func TestLowMoodLine_StrainNeverReachesThePrompt(t *testing.T) {
	s := strainStore(t)
	lowMoodBody(t, s, clock.Now())
	if err := s.SaveStrain(0.87); err != nil {
		t.Fatal(err)
	}
	b, _ := s.LoadBody()
	d := &DialogEngine{}
	sys := d.buildSystem(DialogInput{UserText: "在吗", Body: &b}, &PersonaProfile{Name: "小夜", Template: "bestie"}, "ja")
	for _, leak := range []string{"0.87", "0.8", "strain", "慢性压力", "压力值"} {
		if strings.Contains(sys, leak) {
			t.Errorf("慢性压力漏进提示词了 (%q) —— 那是索取的入口", leak)
		}
	}
}

// TestStrainAndDiseaseNamesNeverEnterInjectedText
//
// ⚠️ **静态尺子**: 全包的**字符串字面量**里不许出现病名 ——
// 「可证伪的公共事实不许编, 而病名正是一个一查就露的东西」(§3.6.4 / §4.1.2-3)。
//
// ⚠️ 扫的是**字面量**不是源码: 注释里讨论"不许提病名"是合法的,
// 而注释不会进提示词 —— 这个区分正是这条尺子能用的原因。
// diseaseNameWhitelist 允许出现病名的地方 —— **只有"用来匹配的词表"**。
//
// ⚠️ 判据很窄: 词在这里是拿去**比对**用户说了什么, 不是拿去**说给模型听**。
// 一旦某处是"注入文本", 它就该改文案, 而不是进这张表。
// 每一条都必须写明理由 —— 与 quotaWhitelist 同一套规矩。
var diseaseNameWhitelist = map[string]string{
	"guard.go|心理疾病": "guardDiagnoseWords: **检测它是否在对用户下诊断**的词表 —— 词是用来匹配的, 不会说给它听",
	"scene.go|抑郁症":  "scenePrivateMarkers: **检测私密话题**(不进记忆)的词表 —— 同上",
	"scene.go|焦虑症":  "同上",
}

func TestStrainAndDiseaseNamesNeverEnterInjectedText(t *testing.T) {
	names := []string{"抑郁症", "焦虑症", "双相", "躁郁", "强迫症", "创伤后应激", "精神疾病", "心理疾病"}
	files, lits, hits := 0, 0, 0
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, rerr := os.ReadFile(filepath.Join(".", name))
		if rerr != nil {
			t.Fatal(rerr)
		}
		files++
		for _, lit := range stringLiterals(string(src)) {
			lits++
			for _, n := range names {
				if strings.Contains(lit, n) {
					if why, ok := diseaseNameWhitelist[name+"|"+n]; ok {
						_ = why
						continue
					}
					hits++
					t.Errorf("%s 的字符串字面量里出现了病名 %q —— §4.1.2 硬线三\n  若这是给人看的(日志/错误), 也需要先想清楚: 它凭什么出现在这个系统里?\n  字面量: %q", name, n, lit)
				}
			}
		}
	}
	t.Logf("病名扫描: %d 个文件 / %d 个字面量 / %d 处命中", files, lits, hits)
}

var _ = clock.Today
