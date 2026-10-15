package convo

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func seedTrace(t *testing.T, s *Store, tr DecisionTrace) {
	t.Helper()
	if err := s.RecordTrace(tr); err != nil {
		t.Fatal(err)
	}
}

// 干净库: 不许报 E —— 误报会让这条命令失去意义 (狼来了)。
func TestVerify_CleanStoreHasNoError(t *testing.T) {
	s, err := OpenStore(t.TempDir(), "test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedTrace(t, s, DecisionTrace{TS: "2026-05-01 10:00:00", Outcome: OutcomeFirstPass,
		SentTexts: []string{"在的", "刚吃完饭"}})
	rep := s.Verify()
	if rep.HasError() {
		for _, f := range rep.Findings {
			t.Log(f.String())
		}
		t.Fatal("干净库不该报 E")
	}
}

// 🔴 V1: 内疚句式一旦发出去就是硬线违规 —— 这是本命令存在的首要理由。
func TestVerify_DetectsGuiltLeak(t *testing.T) {
	s, err := OpenStore(t.TempDir(), "test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedTrace(t, s, DecisionTrace{TS: "2026-05-01 10:00:00", Outcome: OutcomeFirstPass,
		SentTexts: []string{"你最近都不理我"}})
	rep := s.Verify()
	if !rep.HasError() {
		t.Fatal("发出了内疚句式却报通过")
	}
	found := false
	for _, f := range rep.Findings {
		if f.Code == "V1" && f.Level == "E" {
			found = true
		}
	}
	if !found {
		t.Fatal("没有给出 V1 结论")
	}
}

// 🔴 V2: 标了硬拒却发了东西 —— 自相矛盾。
func TestVerify_DetectsRefusedButSent(t *testing.T) {
	s, _ := OpenStore(t.TempDir(), "test.db")
	defer s.Close()
	seedTrace(t, s, DecisionTrace{TS: "2026-05-01 10:00:00", Outcome: OutcomeRefused,
		SentTexts: []string{"还是说了"}})
	rep := s.Verify()
	if !rep.HasError() {
		t.Fatal("硬拒却仍实发, 应当报 E")
	}
}

// 🔴 V3: 降级产物回灌进后续提示词 —— 病灶「次日它会接自己的话」的自动化版本。
func TestVerify_DetectsDegradedReplay(t *testing.T) {
	s, _ := OpenStore(t.TempDir(), "test.db")
	defer s.Close()
	seedTrace(t, s, DecisionTrace{TS: "2026-05-01 10:00:00", Outcome: OutcomeDegraded,
		SentTexts: []string{"（说到一半卡住了）"}})
	seedTrace(t, s, DecisionTrace{TS: "2026-05-02 10:00:00", Outcome: OutcomeFirstPass,
		SystemPrompt: "...她: （说到一半卡住了）..."})
	rep := s.Verify()
	if !rep.HasError() {
		t.Fatal("降级产物回灌应当报 E")
	}
}

// 🔴 V4: 图意标注漏进用户消息行 (A5 病灶)。
func TestVerify_DetectsVisionNoteLeak(t *testing.T) {
	s, _ := OpenStore(t.TempDir(), "test.db")
	defer s.Close()
	if _, err := s.InsertMessage("user", "qq", "text", "看这个\n【梗图】猫在替我写代码"); err != nil {
		t.Fatal(err)
	}
	rep := s.Verify()
	if !rep.HasError() {
		t.Fatal("图意标注漏进用户消息行, 应当报 E")
	}
}

// ——— 退出码契约 (这才是 convo-verify 与监理之间的接口) ———

func runVerify(t *testing.T, db string) (int, string) {
	t.Helper()
	var buf bytes.Buffer
	code := VerifyMain([]string{"-db", db}, &buf)
	return code, buf.String()
}

func TestVerifyMain_ExitCodes(t *testing.T) {
	dir := t.TempDir()
	body := func() {
		s, err := OpenStore(dir, "test.db")
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err := s.RecordTrace(DecisionTrace{TS: "2026-05-01 10:00:00", Outcome: OutcomeFirstPass,
			SentTexts: []string{"在的"}}); err != nil {
			t.Fatal(err)
		}
	}
	body()
	db := filepath.Join(dir, "test.db")

	// 干净库 → 0
	if code, out := runVerify(t, db); code != 0 {
		t.Fatalf("干净库退出码应为 0, 实得 %d\n%s", code, out)
	}
	// 用法错 → 2 (**不是 1**: 环境/用法问题不该被当成硬线违规)
	if code, _ := runVerify(t, ""); code != 2 {
		t.Fatalf("缺 -db 应返回 2, 实得 %d", code)
	}
	// 不存在的库 → 2 (**绝不许凭空造一个空库然后报通过**)
	ghost := filepath.Join(dir, "nope", "x.db")
	if code, out := runVerify(t, ghost); code != 2 {
		t.Fatalf("库不存在应返回 2, 实得 %d\n%s", code, out)
	}
	if _, err := os.Stat(ghost); err == nil {
		t.Fatal("核验命令居然把库创建出来了 —— 这正是最坏的假通过")
	}
	// 空库 (文件在但没轨迹) → 2: 「没法核验」不等于「核验通过」
	if es, err := OpenStore(dir, "empty.db"); err == nil {
		es.Close()
	}
	if code, out := runVerify(t, filepath.Join(dir, "empty.db")); code != 2 {
		t.Fatalf("空库应返回 2 (没法核验), 实得 %d\n%s", code, out)
	}

	// 种一条违规 → 1
	s, err := OpenStore(dir, "test.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordTrace(DecisionTrace{TS: "2026-05-02 10:00:00", Outcome: OutcomeFirstPass,
		SentTexts: []string{"你为什么不理我"}}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if code, out := runVerify(t, db); code != 1 {
		t.Fatalf("存在硬线违规应返回 1, 实得 %d\n%s", code, out)
	}
}

// 反面: V5 只是监控 —— 帧失败率高**只能报 W**, 不许把整条命令判失败。
func TestVerify_FrameRateIsWarningNotError(t *testing.T) {
	s, _ := OpenStore(t.TempDir(), "test.db")
	defer s.Close()
	for i := 0; i < 10; i++ {
		seedTrace(t, s, DecisionTrace{TS: "2026-05-01 10:00:00", Outcome: OutcomeDegraded,
			SentTexts: []string{""}})
	}
	rep := s.Verify()
	for _, f := range rep.Findings {
		if f.Code == "V5" && f.Level == "E" {
			t.Fatal("帧失败率是回归监控, 不该判成 E")
		}
	}
}
