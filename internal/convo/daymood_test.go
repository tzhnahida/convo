package convo

import (
	"strings"
	"testing"
)

func TestDayMoodOf_IsDeterministic(t *testing.T) {
	for _, day := range []string{"2026-03-01", "2026-03-02", "2026-03-03"} {
		first := DayMoodOf(day)
		for i := 0; i < 50; i++ {
			if got := DayMoodOf(day); got != first {
				t.Fatalf("%s 的值不稳定: %v vs %v", day, got, first)
			}
		}
	}
}

func TestDayMoodOf_CoversAllThree(t *testing.T) {
	seen := map[DayMood]int{}
	for i := 0; i < 400; i++ {
		seen[DayMoodOf("2026-"+itoa(i))]++
	}
	for _, m := range []DayMood{DayMoodFlat, DayMoodLow, DayMoodBright} {
		if seen[m] == 0 {
			t.Fatalf("400 天里没有出现过 %v: %v", m, seen)
		}
	}
	if seen[DayMoodLow] > 120 {
		t.Fatalf("低意愿日太多了 (%d/400) —— 那不是一个偶尔不太想说话的人", seen[DayMoodLow])
	}
}

func TestDayMood_HasNoCause(t *testing.T) {
	if _, ok := DayMoodLine(DayMoodFlat); ok {
		t.Fatal("平常日不该注入任何东西 —— 天天说一句本身就是一种解释")
	}
	line, ok := DayMoodLine(DayMoodLow)
	if !ok {
		t.Fatal("低意愿日应当有注入文本")
	}
	if !strings.Contains(line, "不要解释") {
		t.Fatalf("必须写死禁止解释 —— 否则模型会编一个理由出来: %q", line)
	}
	for _, wd := range []string{"因为", "都怪", "你让我", "所以今天", "心情不好"} {
		if strings.Contains(line, wd) {
			t.Fatalf("注入文本里出现了归因词 %q —— 它的定义就是没有原因: %q", wd, line)
		}
	}
}

func TestDayMoodBypass(t *testing.T) {
	if !DayMoodBypass("救命 我不想活了", false, false) {
		t.Fatal("危机必须绕过 —— 「今天不想说话」不能变成「我最需要的时候它不理我」")
	}
	if !DayMoodBypass("帮我看看这个", true, false) {
		t.Fatal("求助必须绕过")
	}
	if !DayMoodBypass("急事", false, true) {
		t.Fatal("紧急必须绕过")
	}
	if DayMoodBypass("今天天气不错", false, false) {
		t.Fatal("普通消息不该绕过")
	}
}

func TestDayMoodDelayFactor(t *testing.T) {
	if DayMoodDelayFactor(DayMoodLow) <= 1 {
		t.Fatal("低意愿日应当更慢")
	}
	if DayMoodDelayFactor(DayMoodBright) >= 1 {
		t.Fatal("高意愿日应当更快")
	}
	if DayMoodDelayFactor(DayMoodFlat) != 1 {
		t.Fatal("平常日不许动延迟 —— 恒等是这套倍率的基线")
	}
}

func TestDayMoodBlocksAction(t *testing.T) {
	if !DayMoodBlocksAction(ActActiveOpen, DayMoodLow) {
		t.Fatal("低意愿日必须拦住主动开场 —— 那是「今天它没来找我」的全部内容")
	}
	if DayMoodBlocksAction(ActResume, DayMoodLow) {
		t.Fatal("中断-恢复被拦下了 —— 那不是话少, 那是又一次消失")
	}
	if DayMoodBlocksAction(ActActiveOpen, DayMoodFlat) || DayMoodBlocksAction(ActActiveOpen, DayMoodBright) {
		t.Fatal("平常日与高意愿日不该被拦")
	}
}

// 「漏」的唯一形态: 低意愿日连 @ 都可能不回。
func TestDayMoodSilencesAtReply(t *testing.T) {
	if !DayMoodSilencesAtReply(DayMoodLow) {
		t.Fatal("低意愿日应当允许「@ 了也不回」—— 真人被 @ 也会漏")
	}
	if DayMoodSilencesAtReply(DayMoodFlat) || DayMoodSilencesAtReply(DayMoodBright) {
		t.Fatal("平常日不许漏 @ —— 那会变成「它经常看不见我」")
	}
}

func TestBuildSystem_DayMoodNoteReachesPrompt(t *testing.T) {
	d := &DialogEngine{}
	line, ok := DayMoodLine(DayMoodLow)
	if !ok {
		t.Fatal("低意愿日应当有文本")
	}
	got := d.buildSystem(DialogInput{DayMoodNote: line, History: "你: 在吗"}, &PersonaProfile{Name: "小夜"}, "ja")
	if !strings.Contains(got, "你今天不太想说话") {
		t.Fatal("低意愿日的行为约束没有进提示词")
	}
	if strings.Index(got, "你今天不太想说话") > strings.Index(got, "【最近对话】") {
		t.Fatal("它是**天档**, 必须排在每轮变化段之前 (段序即缓存策略)")
	}
}
