package convo

import (
	"strings"
	"testing"
)

// ⚠️ 2026-10-19 生产事故: 07:25 的预告说「我这就去睡了」, 而 07:30 是该起床去上学。
// 根因: 预告拿**当前槽**(睡)去拼提示词, 不是下一个槽(忙)。
func TestPreview_NamesTheNextSlotNotTheCurrent(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜"}
	sys := d.buildSystem(DialogInput{
		ActionKind:  ActPreview,
		State:       StateSleep, // 现在在睡
		SlotLabel:   "23:30-07:30",
		PreviewNext: StateBusy, // 07:30 起要忙
		PreviewAt:   "07:30",
	}, p, "ja")
	if !strings.Contains(sys, "07:30") || !strings.Contains(sys, "忙正事") {
		t.Fatalf("预告没说清下一格是什么: %s", sys)
	}
	if strings.Contains(sys, "马上要去「") {
		t.Fatalf("还在用旧措辞 (把时间区间当目的地): %s", sys)
	}

	// ⚠️ 2026-10-19: 预告曾把最近三句旧话递进提示词, 于是它开口就翻昨天的账。
	if strings.Contains(sys, "别再用这种说法") {
		t.Fatalf("预告又把旧话题递到嘴边了: %s", sys)
	}
	if !strings.Contains(sys, "别接着之前的旧话题") {
		t.Fatalf("预告没有禁止翻旧账: %s", sys)
	}

	// 反方向: 下一个槽是睡 → 就该说睡。
	sys2 := d.buildSystem(DialogInput{ActionKind: ActPreview, State: StateFree, PreviewNext: StateSleep}, p, "ja")
	if !strings.Contains(sys2, "该睡了") {
		t.Fatalf("睡前预告没说该睡了: %s", sys2)
	}
}

// 裁决层必须把"下一格"随动作带下来, 否则下游无从得知方向。
func TestDecideTick_PreviewCarriesNextSlot(t *testing.T) {
	got := DecideTick(base(tueAt(11, 56), StateFree), testCfg())
	if got.Kind != ActPreview {
		t.Fatalf("kind=%q, 期望 preview", got.Kind)
	}
	if got.SlotState != StateFocus || got.SlotStart != "12:00" {
		t.Fatalf("预告没带上下一格: state=%q start=%q", got.SlotState, got.SlotStart)
	}
}

// 2026-10-27 用户实测: 「人与人之间聊天不会直接说在干什么啊, 要么问好, 要么关照对方」。
// 预告只说「待会儿要干什么」= 行程播报; 新提示必须先看对方。
func TestPreview_AsksThemInsteadOfReportingSchedule(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜"}
	sys := d.buildSystem(DialogInput{
		ActionKind:  ActPreview,
		State:       StateFree,
		PreviewNext: StateBusy,
		PreviewAt:   "09:00",
	}, p, "ja")
	for _, want := range []string{"别报行程", "问一句 ta", "只发一条"} {
		if !strings.Contains(sys, want) {
			t.Fatalf("预告提示缺少 %q: %s", want, sys)
		}
	}
	if strings.Contains(sys, "只说眼前这件事") {
		t.Fatalf("旧的行程播报提示还在: %s", sys)
	}
}

// 预告只许留一条气泡 —— 连发就成了自说自话。
func TestOnlyFirstBubble(t *testing.T) {
	items := []FrameItem{
		{Kind: "text", Text: "嗯 起来了"},
		{Kind: "text", Text: "待会儿九点得去洗杯子晾衣服"},
		{Kind: "text", Text: "你周末打算干嘛"},
	}
	got := onlyFirstBubble(items)
	if len(got) != 1 || got[0].Text != "嗯 起来了" {
		t.Fatalf("应当只留第一条: %+v", got)
	}
	// 表情在前、文本在后 → 取文本 (纯表情预告没意义)。
	got2 := onlyFirstBubble([]FrameItem{{Kind: "sticker"}, {Kind: "text", Text: "早"}})
	if len(got2) != 1 || got2[0].Kind != "text" || got2[0].Text != "早" {
		t.Fatalf("应当优先保留文本: %+v", got2)
	}
}
