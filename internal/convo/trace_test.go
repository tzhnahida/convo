package convo

import (
	"errors"
	"strings"
	"testing"
)

func TestClassifyOutcome(t *testing.T) {
	cases := []struct {
		name string
		f    OutputFrame
		err  error
		want string
	}{
		{"正常一次过", OutputFrame{}, nil, OutcomeFirstPass},
		{"降级放行", OutputFrame{Degraded: true}, nil, OutcomeDegraded},
		{"硬拒", OutputFrame{Refused: true}, nil, OutcomeRefused},
		{"报错", OutputFrame{}, errors.New("boom"), OutcomeError},
	}
	for _, c := range cases {
		if got := ClassifyOutcome(c.f, c.err); got != c.want {
			t.Errorf("%s: 得到 %q, 期望 %q", c.name, got, c.want)
		}
	}
}

// 🔴 反面: 硬拒**必须**优先于降级 —— 硬拒的帧 Degraded 是 false, 但语义上是「刻意不说」,
// 回放时绝不能与「故障降级」混为一谈。
func TestClassifyOutcome_RefusedBeatsDegraded(t *testing.T) {
	if got := ClassifyOutcome(OutputFrame{Refused: true, Degraded: true}, nil); got != OutcomeRefused {
		t.Fatalf("硬拒应优先, 得到 %q", got)
	}
}

func TestTraceRoundTrip(t *testing.T) {
	store, err := OpenStore(t.TempDir(), "test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	want := DecisionTrace{
		TS: "2026-05-01 10:00:00", Seq: 7, ActionKind: ActImmediate,
		UserText: "在吗", StateFact: "【你现在】闲着", MoodVal: -0.2, MoodAro: 0.4,
		MemIDs: []string{"m_a", "m_b"}, SlangHit: "绝了",
		SystemPrompt: "PROMPT-BODY", PromptBytes: 11,
		RawOutput: "raw", Outcome: OutcomeFirstPass,
		SentTexts: []string{"在的"},
	}
	if err := store.RecordTrace(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.RecentTraces(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("应有 1 条, 实得 %d", len(got))
	}
	g := got[0]
	if g.Seq != want.Seq || g.ActionKind != want.ActionKind || g.Outcome != want.Outcome {
		t.Errorf("基本字段没对上: %+v", g)
	}
	if g.MoodVal != want.MoodVal || g.PromptBytes != want.PromptBytes {
		t.Errorf("数值字段没对上: v=%v bytes=%d", g.MoodVal, g.PromptBytes)
	}
	if strings.Join(g.MemIDs, ",") != "m_a,m_b" {
		t.Errorf("记忆 id 没对上: %v", g.MemIDs)
	}
	if len(g.SentTexts) != 1 || g.SentTexts[0] != "在的" {
		t.Errorf("实发文本没对上: %v", g.SentTexts)
	}
}

// 🔴 反面: 记忆/实发为空时, JSON 往返不许 panic 或读不出来。
func TestTraceEmptySlicesSurviveRoundTrip(t *testing.T) {
	store, err := OpenStore(t.TempDir(), "test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.RecordTrace(DecisionTrace{TS: "2026-05-01 10:00:00", Outcome: OutcomeDegraded}); err != nil {
		t.Fatal(err)
	}
	got, err := store.RecentTraces(1)
	if err != nil {
		t.Fatal(err)
	}
	if s := RenderTrace(got[0]); !strings.Contains(s, "DEGRADED") {
		t.Errorf("渲染应含结局, 实得: %s", s)
	}
}

func TestRenderTraceShowsCausalChain(t *testing.T) {
	tr := DecisionTrace{
		ID: 42, TS: "2026-05-01 10:00:00", ActionKind: ActActiveOpen, Proactive: true,
		StateFact: "【你现在】闲着", MoodVal: 0.1, MoodAro: 0.5,
		MemIDs: []string{"m_a"}, SlangHit: "绝了", PromptBytes: 100,
		RawOutput: "raw", Outcome: OutcomeFirstPass, SentTexts: []string{"在的"},
	}
	s := RenderTrace(tr)
	for _, want := range []string{"#42", "主动", "FIRST_PASS", "m_a", "绝了", "实发[0]"} {
		if !strings.Contains(s, want) {
			t.Errorf("回放缺少 %q:\n%s", want, s)
		}
	}
}
