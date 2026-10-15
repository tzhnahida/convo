package convo

import (
	"strings"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────
// #24 上行通道
//
// 「一个**永远在收着**的角色, 不是"有分寸的人", 是**抑郁的人**。」
// ─────────────────────────────────────────────────────────────

func moodWith(v float64, causes ...MoodCause) MoodState {
	return MoodState{Valence: v, Causes: causes}
}

func freshCause(dv float64) MoodCause {
	return MoodCause{TS: time.Now().Format(bodyTSLayout), Kind: "social", Source: "朋友", Text: "x", DeltaV: dv}
}

// TestUplift_RequiresAnExternalCause
//
// ⚠️ 四条护栏里最容易漏的一条: **必须能被外部解释**。
// 「不许无缘无故」与 §5.5 M7「不许随机掷骰子」同源 ——
// 一个没有来由就特别高兴的角色, 和一个没有来由就特别难过的角色**一样不可信**。
func TestUplift_RequiresAnExternalCause(t *testing.T) {
	now := time.Now()
	// 心情很高, 但**一条正成因都没有** → 不上行。
	if u := UpliftOf(moodWith(0.8), now); u.Active {
		t.Fatal("心情高却没有来由 —— 不许上行(无缘无故的高兴同样是假的)")
	}
	// 只有负成因也不行。
	if u := UpliftOf(moodWith(0.8, freshCause(-0.3)), now); u.Active {
		t.Fatal("成因是负的却当成上行")
	}
	// 有正成因 → 上行。
	if u := UpliftOf(moodWith(0.8, freshCause(0.3)), now); !u.Active {
		t.Fatal("有正成因、心情又高, 却不上行 —— 那这条通道等于不存在")
	}
	// 背景压力为正也算起因 (天气这类"整天都在"的底色)。
	bg := MoodState{Valence: 0.5, BgValence: 0.2}
	if u := UpliftOf(bg, now); !u.Active {
		t.Fatal("背景为正却不算起因")
	}
}

// TestUplift_ThresholdAndStaleness
func TestUplift_ThresholdAndStaleness(t *testing.T) {
	now := time.Now()
	// 差一点点 → 不上行。
	if u := UpliftOf(moodWith(UpliftThreshold-0.01, freshCause(0.3)), now); u.Active {
		t.Fatal("没到阈值却上行")
	}
	// 刚过阈值 → 上行。
	if u := UpliftOf(moodWith(UpliftThreshold+0.01, freshCause(0.3)), now); !u.Active {
		t.Fatal("过了阈值却不上行")
	}
	// ⚠️ 起因**过期** → 不上行: 三天前的一件好事不该让今天话特别多。
	old := MoodCause{TS: now.Add(-72 * time.Hour).Format(bodyTSLayout), Kind: "social", DeltaV: 0.4}
	if u := UpliftOf(moodWith(0.8, old), now); u.Active {
		t.Fatal("起因过期了却仍然上行")
	}
	// ⚠️ 坏时刻**不算起因** —— 折成"刚刚发生过"的话, 一条坏数据就能让它无缘无故高兴。
	bad := MoodCause{TS: "乱写", DeltaV: 0.4}
	if u := UpliftOf(moodWith(0.8, bad), now); u.Active {
		t.Fatal("时刻解析不出的成因被当成了真起因")
	}
}

// TestUplift_CapsAreHard
//
// ⚠️ 护栏一: 「上行**不是无限许可**」。不设上限的上行会变成**永动机**, 那比永远克制更假。
func TestUplift_CapsAreHard(t *testing.T) {
	u := UpliftOf(moodWith(1.0, freshCause(0.9)), time.Now())
	if !u.Active {
		t.Fatal("夹具前提不对")
	}
	if u.Quota > UpliftQuotaBonus {
		t.Errorf("配额加过头了: %d (上限 %d)", u.Quota, UpliftQuotaBonus)
	}
	if u.Delay > 1 || u.Delay <= 0 {
		t.Errorf("延迟系数必须在 (0,1]: %.2f", u.Delay)
	}
	if u.Length < 1 {
		t.Errorf("长度系数应当 ≥1: %.2f", u.Length)
	}
	// 节奏修正只能让它更快, 不能更慢 —— 方向错了就是把上行做成了下行。
	base := 1.0
	if got := upliftPacing(u, base); got > base {
		t.Errorf("上行把节奏改慢了: %.2f", got)
	}
	// 不上行 → 一个字都不改。
	if got := upliftPacing(Uplift{}, base); got != base {
		t.Errorf("不上行却动了节奏: %.2f", got)
	}
}

// TestUpliftLine_SilentWhenNormal
func TestUpliftLine_SilentWhenNormal(t *testing.T) {
	if got := UpliftLine(Uplift{}); got != "" {
		t.Fatalf("不上行却注入了: %q", got)
	}
	if got := UpliftLine(UpliftOf(moodWith(0.9), time.Now())); got != "" {
		t.Fatalf("没有起因却注入了: %q", got)
	}
}

// TestUpliftLine_NoSelfReportNoSolicitation
//
// ⚠️ 护栏二③: **不许自报**, **不许拿它索取**。
// 而"不许自报"最容易被写坏的方式, 就是**在提示词里举一个"不要这样说"的例子** ——
// 反例在提示词里也是范例(§2.12③ 的教训)。所以这条尺子查的是**规则在不在**,
// 而不是"有没有那个例句"。
func TestUpliftLine_NoSelfReportNoSolicitation(t *testing.T) {
	line := UpliftLine(UpliftOf(moodWith(0.8, freshCause(0.4)), time.Now()))
	if line == "" {
		t.Fatal("上行却不注入 —— 那四条护栏就没有落点")
	}
	if !strings.Contains(line, "不许自报") {
		t.Errorf("没有挡住「自报状态」:\n%s", line)
	}
	if !strings.Contains(line, "不许拿它索取") {
		t.Errorf("没有挡住「拿上行索取」:\n%s", line)
	}
	// ⚠️ 而且**不许把违禁的措辞摆出来当例子**。
	for _, bad := range []string{"我今天心情特别好", "你陪我聊", "我今天这么开心"} {
		if strings.Contains(line, bad) {
			t.Errorf("提示词里举了违禁例句 %q —— 反例也是范例:\n%s", bad, line)
		}
	}
	// 上行那一段**必须**落点明确: 行为侧(更快/更长/自己起话头), 而不是让它去报告心情。
	if !strings.Contains(line, "起个话头") {
		t.Errorf("没说清上行落在哪: \n%s", line)
	}
}

// TestBuildSystem_CarriesUpliftOnlyWhenItApplies
func TestBuildSystem_CarriesUpliftOnlyWhenItApplies(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜", Template: "bestie"}
	plain := d.buildSystem(DialogInput{UserText: "在吗"}, p, "ja")
	if strings.Contains(plain, "状态不错") {
		t.Fatal("没有心情状态却注入了上行")
	}
	m := moodWith(0.8, freshCause(0.4))
	with := d.buildSystem(DialogInput{UserText: "在吗", Mood: &m}, p, "ja")
	if !strings.Contains(with, "状态不错") {
		t.Fatal("该上行却没注入")
	}
}
