package convo

import (
	"context"
	"strings"
	"testing"
)

// ⚠️ **必须带冒号** —— 不带的话「教练机真好玩」会被当成指令。
func TestParseCoachCommand(t *testing.T) {
	yes := map[string]string{
		"教练:别说你刷屏这种话":          "别说你刷屏这种话",
		"教练：不要连着发三条":           "不要连着发三条",
		"coach: keep it short": "keep it short",
		"  教练: 前面有空格":          "前面有空格",
	}
	for in, want := range yes {
		got, ok := ParseCoachCommand(in)
		if !ok || got != want {
			t.Fatalf("%q → %q ok=%v, want %q", in, got, ok, want)
		}
	}
	// 反面: 不带冒号的正常说话**不许**被当成指令。
	for _, bad := range []string{"教练机真好玩", "这个教练怎么样", "coach", "教练:", "教练：", "", "普通消息"} {
		if _, ok := ParseCoachCommand(bad); ok {
			t.Fatalf("%q 不该被当成教练指令", bad)
		}
	}
}

func TestCoachRules_RoundTripAndCap(t *testing.T) {
	s := newTestStore(t)
	owner := "u1"
	if rs, err := s.CoachRules(owner, ""); err != nil || len(rs) != 0 {
		t.Fatalf("没人教过时应当是空: %v %v", rs, err)
	}
	for i := 0; i < CoachMaxRules+5; i++ {
		if _, err := s.AddCoachRule(owner, "规则"+string(rune('a'+i)), ""); err != nil {
			t.Fatal(err)
		}
	}
	rs, err := s.CoachRules(owner, "")
	if err != nil {
		t.Fatal(err)
	}
	// ⚠️ 超出上限时**丢最旧的** —— 最近被纠正的才是它现在最该记住的。
	if len(rs) != CoachMaxRules {
		t.Fatalf("应当裁到 %d 条, got %d", CoachMaxRules, len(rs))
	}
	if rs[0].Text == "规则a" {
		t.Fatal("留下的应当是最新的, 不是最旧的")
	}
}

// ⚠️ 场景隔离: 在 A 群教的规矩不该管到 B 群。
func TestCoachRules_ScopeIsolation(t *testing.T) {
	s := newTestStore(t)
	owner := "u1"
	if _, err := s.AddCoachRule(owner, "全局规矩", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddCoachRule(owner, "只在A群", "group:A"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.CoachRules(owner, "group:B")
	if len(got) != 1 || got[0].Text != "全局规矩" {
		t.Fatalf("B 群不该看到 A 群的规矩: %+v", got)
	}
	gotA, _ := s.CoachRules(owner, "group:A")
	if len(gotA) != 2 {
		t.Fatalf("A 群应当看到两条: %+v", gotA)
	}
}

func TestCoachNote(t *testing.T) {
	if CoachNote(nil) != "" {
		t.Fatal("没有规则时不该注入任何东西 —— 空串会占位")
	}
	got := CoachNote([]CoachRule{{Text: "新的"}, {Text: "旧的"}})
	if !strings.Contains(got, "新的") || !strings.Contains(got, "旧的") {
		t.Fatalf("规则没进去: %q", got)
	}
	// ⚠️ 措辞是"它对你说过", 不是"系统要求" —— 后者会让它变成在念条款。
	if !strings.Contains(got, "亲口") {
		t.Fatalf("措辞太系统了: %q", got)
	}
	// 人读起来从早到晚更顺。
	if strings.Index(got, "旧的") > strings.Index(got, "新的") {
		t.Fatalf("顺序应当是旧的在前: %q", got)
	}
}

// ⚠️ 确认语要是**人话**, 不是"已记录" —— 后者立刻暴露这是个系统。
func TestCoachAck_IsHuman(t *testing.T) {
	for _, in := range []string{"别说你刷屏", "不要连着发", "别叫我杂鱼"} {
		got := CoachAck(in)
		if strings.Contains(got, "记录") || strings.Contains(got, "收到指令") {
			t.Fatalf("确认语太系统: %q", got)
		}
		if got == "" {
			t.Fatal("确认语不能是空的")
		}
	}
}

// 端到端: owner 说「教练: ...」 → 落库 → 下一次注入里能看到它。
func TestCoach_EndToEndThroughEngine(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	e.Cfg.Channel.OwnerQQ = "10001"
	if err := e.onUserText(context.Background(), "教练:别说你刷屏这种话", ""); err != nil {
		t.Fatal(err)
	}
	got := e.coachNote("")
	if !strings.Contains(got, "别说你刷屏这种话") {
		t.Fatalf("教了却没注入 —— 存了不用等于白存: %q", got)
	}
}
