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
