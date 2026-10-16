package convo

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
)

var hmRE = regexp.MustCompile(`\d{1,2}[:：]\d{2}`)

// TestVagueNowLine_GivesFeelingNotReading (§4.6.3)
//
// ⚠️ 对话路径的默认时间锚**不许出现分钟**。
// 原实现无条件打印 `2006-01-02 15:04:05` —— 那等于给它装了一个秒表。
func TestVagueNowLine_GivesFeelingNotReading(t *testing.T) {
	l := VagueNowLine()
	if !strings.Contains(l, "【大概什么时候】") {
		t.Fatalf("模糊时段段头不见了: %q", l)
	}
	if hmRE.MatchString(l) {
		t.Errorf("模糊时段里出现了分钟读数 —— §4.6.3 要求的是「晚上十点多」不是「22:13」: %q", l)
	}
	// 正面: 日期 / 星期 / 时段都要在(它得知道今天周几, 日程按星期分档)。
	for _, must := range []string{"月", "周"} {
		if !strings.Contains(l, must) {
			t.Errorf("模糊时段缺了 %q —— 它得知道今天是哪天: %q", must, l)
		}
	}
	// 对照: 精确那一行**必须**有分钟 —— 否则这条测试只是在验证"字符串很短"。
	if !hmRE.MatchString(PreciseNowLine()) {
		t.Fatal("精确时间行里没有分钟 —— 那 look_time 那条路就没有意义了")
	}
}

// TestVagueSlot_CoversWholeDay
//
// 反面: 任何一小时都不许返回空 —— 空时段会让段 9 变成「【大概什么时候】。」
func TestVagueSlot_CoversWholeDay(t *testing.T) {
	base := time.Date(2026, 10, 15, 0, 0, 0, 0, time.Local)
	for h := 0; h < 24; h++ {
		s := VagueSlot(base.Add(time.Duration(h) * time.Hour))
		if strings.TrimSpace(s) == "" {
			t.Errorf("%d 点没有时段词", h)
		}
	}
	// 晚上 22 点应当落进"点多"那一档 —— 那是"能感觉到的细度", 不是读数。
	if got := VagueSlot(base.Add(22 * time.Hour)); !strings.Contains(got, "点多") {
		t.Errorf("22 点的措辞不对: %q", got)
	}
}

// TestBuildSystem_VagueByDefault_PreciseOnDemand
//
// 接线: 段 9 必须**默认模糊**、`TimePrecise` 时才精确。
func TestBuildSystem_VagueByDefault_PreciseOnDemand(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜", Template: "bestie"}
	vague := d.buildSystem(DialogInput{UserText: "在吗"}, p, "ja")
	if !strings.Contains(vague, "【大概什么时候】") {
		t.Fatal("默认没走模糊时段 —— 段 9 的接线断了")
	}
	// ⚠️ 这里**故意不写 "整份提示词不许出现 HH:MM"** —— 那条断言把两件事混成了一件:
	//
	//   (a) 它**知道现在几点**      ← §4.6.3 要挡的就是这个
	//   (b) 它知道 `at` 字段长什么样 ← 帧字段的格式示例, 必须给, 否则它填出来的时刻全是"下午"
	//
	// 例子 `"at":"15:00"` 属于 (b)。原来那条宽断言会把它一起误伤, 而"把测试改宽以迁就代码"
	// 正是这个项目最反对的事 —— 所以要改的不是放宽, 而是**换成直接测 (a)**。
	// (这个坑踩了两次: 第一次是段 8 里写「真人说不出"现在 22:13"」, 那确实属于 (a)。)
	now := clock.Now()
	for _, leak := range []string{now.Format("15:04"), now.Format("15:04:05"), now.Format("2006-01-02 15:04")} {
		if strings.Contains(vague, leak) {
			t.Errorf("默认提示词里泄漏了**当前时刻** %q —— 那等于给它装了秒表", leak)
		}
	}
	// 段 9 那一段本身必须是模糊的(它是最接近"报价"的地方)。
	if i := strings.Index(vague, "【大概什么时候】"); i >= 0 {
		if m := hmRE.FindString(vague[i:]); m != "" {
			t.Errorf("时间那一段里出现了分钟读数 %q —— 那一段必须只有「点多」这种粗粒度", m)
		}
	}
	precise := d.buildSystem(DialogInput{UserText: "在吗", TimePrecise: true}, p, "ja")
	if !strings.Contains(precise, "【当前时间】") {
		t.Fatal("TimePrecise 时应当给精确时刻")
	}
	if !hmRE.MatchString(precise) {
		t.Error("TimePrecise 的提示词里没有分钟读数 —— 那「看了一下」什么也没看到")
	}
	if strings.Contains(precise, "【大概什么时候】") {
		t.Error("精确与模糊同时在 —— 时间变成了两段")
	}
}

// TestValidateFrame_RejectsPreciseClockWithoutLook
//
// ⚠️ 两面都要测, 因为**收窄**才是这条的重点:
// 分钟级要拦; 小时级的估算(「都十一点了」)必须放行 —— 那是要**发生**的话, 不是违规。
func TestValidateFrame_RejectsPreciseClockWithoutLook(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜", Template: "bestie"}
	frame := func(text string) OutputFrame {
		return OutputFrame{Reply: []FrameItem{{Kind: "text", Text: text}}}
	}
	// 反面 1: 没 look_time 就报分钟 → 违规
	if v := d.ValidateFrame(frame("现在22:13了, 你怎么还没睡"), DialogInput{UserText: "在吗"}, p); len(v) == 0 {
		t.Error("没看一下就报出 22:13 —— 必须违规")
	}
	if v := d.ValidateFrame(frame("都十一点零五分了"), DialogInput{UserText: "在吗"}, p); len(v) == 0 {
		t.Error("没看一下就报出「十一点零五分」—— 必须违规")
	}
	// 正面 1: 看过之后可以报
	if v := d.ValidateFrame(frame("现在22:13了, 你怎么还没睡"), DialogInput{UserText: "在吗", TimePrecise: true}, p); len(v) != 0 {
		t.Errorf("看过时间之后仍然拦 —— 那条路就白开了: %v", v)
	}
	// 正面 2: 小时级的估算**必须放行** —— 那是 §4.6.3 要它说的话
	for _, ok := range []string{"啊都这个点了", "都十一点了 你怎么还不睡", "我以为才过了十分钟"} {
		if v := d.ValidateFrame(frame(ok), DialogInput{UserText: "在吗"}, p); len(v) != 0 {
			t.Errorf("误伤了小时级估算 %q: %v", ok, v)
		}
	}
}
