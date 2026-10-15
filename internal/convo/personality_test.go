package convo

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBuiltinSchedules_valid(t *testing.T) {
	for _, kind := range []string{"student", "office_worker", "night_owl"} {
		ws := builtinSchedule(kind)
		if err := ValidateWeekSchedule(ws); err != nil {
			t.Fatalf("builtin %q invalid: %v", kind, err)
		}
		// ⚠️ **2026-10-15 改 (#12)**。这里原来写死 `SleepStart != "23:30" || SleepEnd != "07:30"` ——
		// 而那条断言**正是让 night_owl 的 90 分钟谎言漏过去的原因**:
		// 它核对的是**展示字段本身**, 而展示字段和槽表**可以不一致**
		// (night_owl 的槽表里它 06:00 就起, 展示字段却写着 07:30, 两边都"通过"了)。
		//
		// 现在断言的是**两者一致** —— 真相源是槽表。
		for _, d := range ws.Days {
			sleep, wake, ok := SleepWindowOf(ws, d.Day)
			if !ok {
				t.Fatalf("builtin %q day %s: 槽表里解不出睡眠窗", kind, d.Day)
			}
			if fmtHM(sleep) != ws.SleepStart || fmtHM(wake) != ws.SleepEnd {
				t.Fatalf("builtin %q day %s: 展示字段 %s-%s 与槽表 %s-%s 不一致",
					kind, d.Day, ws.SleepStart, ws.SleepEnd, fmtHM(sleep), fmtHM(wake))
			}
		}
	}
}

func TestBuiltinSchedule_sampling(t *testing.T) {
	ws := builtinSchedule("student")
	// 2026-09-07 = Mon: 09:00-17:30 focus
	mon := func(h, m int) time.Time { return time.Date(2026, 9, 7, h, m, 0, 0, time.Local) }
	cases := []struct {
		at   time.Time
		want string
	}{
		{mon(3, 0), StateSleep},
		{mon(10, 0), StateFocus},
		{mon(20, 0), StateFree},
		{time.Date(2026, 9, 6, 10, 0, 0, 0, time.Local), StateSlack}, // 周日摸鱼
	}
	for _, c := range cases {
		if got := CurrentState(ws, c.at); got != c.want {
			t.Fatalf("state at %v = %q, want %q", c.at, got, c.want)
		}
	}
}

func TestExtractJSON(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`{"a":1}`, `{"a":1}`},
		{"前缀说明\n```json\n{\"a\":1,\"b\":[2]}\n```", `{"a":1,"b":[2]}`},
		{"{}", `{}`},
		{"no json here", ""},
	}
	for _, c := range cases {
		out, err := extractJSON(c.in)
		if c.want == "" {
			if err == nil {
				t.Fatalf("extractJSON(%q) expected error, got %q", c.in, out)
			}
			continue
		}
		if err != nil || string(out) != c.want {
			t.Fatalf("extractJSON(%q) = %q, %v; want %q", c.in, out, err, c.want)
		}
	}
}

func TestRulesScore(t *testing.T) {
	a := &ProfileAnswers{Template: "mentor", Q1: "medium", Q2: "mentor", Q3: "daily"}
	cad, hb, em, nv := RulesScore(a)
	if cad != "medium" || hb != 0.95 || em != 0.35 || nv != 0.7 {
		t.Fatalf("RulesScore = %q %v %v %v", cad, hb, em, nv)
	}
	a = &ProfileAnswers{Template: "troll", Q3: "weekly"}
	_, hb, em, nv = RulesScore(a)
	if hb != 0.25 || em != 0.7 || nv != 0.3 {
		t.Fatalf("RulesScore troll = %v %v %v", hb, em, nv)
	}
}

// fakeChatter: 按调用次数返回预制输出 (测试 LLM 管线)
type fakeChatter struct {
	outputs []string
	err     error
	i       int
}

func (f *fakeChatter) Chat(ctx context.Context, system, user string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if f.i >= len(f.outputs) {
		return "", errors.New("fake: no more outputs")
	}
	out := f.outputs[f.i]
	f.i++
	return out, nil
}

func TestGenerateSchedule_ok(t *testing.T) {
	good := `{"sleep_start":"23:30","sleep_end":"07:30","days":[` +
		`{"day":"mon","slots":[{"start":"00:00","end":"07:30","state":"sleep"},{"start":"07:30","end":"23:30","state":"free"},{"start":"23:30","end":"24:00","state":"sleep"}]},` +
		repeatDay("tue") + `,` + repeatDay("wed") + `,` + repeatDay("thu") + `,` + repeatDay("fri") + `,` + repeatDay("sat") + `,` + repeatDay("sun") + `]}`
	p := &PersonaProfile{Identity: "student", Description: "在校大学生"}
	ws, err := GenerateSchedule(context.Background(), &fakeChatter{outputs: []string{"```json\n" + good + "\n```"}}, p)
	if err != nil {
		t.Fatal(err)
	}
	if ws.SleepStart != "23:30" || len(ws.Days) != 7 {
		t.Fatalf("ws = %+v", ws)
	}
}

func repeatDay(day string) string {
	return `{"day":"` + day + `","slots":[{"start":"00:00","end":"07:30","state":"sleep"},{"start":"07:30","end":"23:30","state":"free"},{"start":"23:30","end":"24:00","state":"sleep"}]}`
}

func TestGenerateSchedule_fallback(t *testing.T) {
	// 两次输出非法 → 内置模板兜底
	f := &fakeChatter{outputs: []string{"{bad json", `{"days":[]}`}}
	p := &PersonaProfile{Identity: "night_owl"}
	ws, err := GenerateSchedule(context.Background(), f, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateWeekSchedule(ws); err != nil {
		t.Fatalf("fallback invalid: %v", err)
	}
	if ws.Days[0].Day != "mon" {
		t.Fatalf("fallback schedule days = %+v", ws.Days)
	}
}

func TestGenerateSchedule_llmErr_fallback(t *testing.T) {
	p := &PersonaProfile{Identity: "office_worker"}
	ws, err := GenerateSchedule(context.Background(), &fakeChatter{err: errors.New("boom")}, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateWeekSchedule(ws); err != nil {
		t.Fatalf("fallback invalid: %v", err)
	}
}

func TestGenerateDemo_fallback(t *testing.T) {
	f := &fakeChatter{outputs: []string{"不是 JSON 的输出"}}
	frame, err := GenerateDemo(context.Background(), f, &PersonaProfile{Name: "小夜", Template: "bestie"})
	if err != nil {
		t.Fatal(err)
	}
	if len(frame.Reply) == 0 || frame.Reply[0].Text == "" {
		t.Fatalf("demo frame empty: %+v", frame)
	}
}
