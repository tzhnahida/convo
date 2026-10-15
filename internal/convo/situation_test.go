package convo

import (
	"context"
	"strings"
	"testing"
	"time"
)

// ——— §4.3 日级处境段: 内容契约 + 接线 ———

// ——— 真路径接线: dialogReply 里那一行 ———
//
// ⚠️ 上面 TestDayNote_EngineEndToEnd 是**直接调 dayNote()** 的 ——
// 它证明函数对, 但证明不了 dialogReply 里那句 in.DayNote = e.dayNote(...) 还在。
// 删掉那一行, 那条测试照样绿 (BodyLine 就是这么当了一整轮死代码的)。
// 这条走 **dialogReply 全程**: 假 LLM 收到的 system 就是真的提示词。

type sitCaptureChatter struct {
	lastSystem string
	n          int
}

func (f *sitCaptureChatter) Chat(_ context.Context, system, _ string) (string, error) {
	f.lastSystem = system
	f.n++
	return `{"reply":[{"kind":"text","text":"嗯 在的"}],"mood":"ok"}`, nil
}

func sitEngine(t *testing.T, c Chatter) (*Engine, *Store) {
	t.Helper()
	s := newTestStore(t)
	return &Engine{
		Store:   s,
		Cfg:     DefaultConfig(),
		Dialog:  &DialogEngine{Store: s},
		Chatter: c,
		UserID:  1,
		Lang:    "ja",
	}, s
}

// 正面: 心情低 + 高唤醒 → dialogReply 真的把处境段送进了 system。
// 改坏成: 删掉 dialogReply 里那句 in.DayNote = e.dayNote(...) → 红。
func TestDialogReply_DayNoteReachesSystemPrompt(t *testing.T) {
	c := &sitCaptureChatter{}
	e, s := sitEngine(t, c)
	if err := s.SaveMoodState(MoodState{Valence: -0.9, Arousal: 0.95}); err != nil {
		t.Fatal(err)
	}
	p := &PersonaProfile{Name: "小夜", Template: "bestie"}
	if _, err := e.dialogReply(context.Background(), DialogInput{UserText: "在吗", State: StateFree}, p); err != nil {
		t.Fatalf("dialogReply: %v", err)
	}
	if c.n == 0 {
		t.Fatal("假 LLM 一次都没被调用 —— 这条测试没走到真路径")
	}
	if !strings.Contains(c.lastSystem, "今天不太想聊") {
		t.Fatalf("处境段没进 system —— dialogReply 里那行接线断了。system 前 400 字:\n%.400s", c.lastSystem)
	}
}

// 反面: 心情中性 → system 里**不许**出现处境段。
// 改坏成: 无条件算 DayNote (例如忘了 DeclinePermit 的门) → 红。
func TestDialogReply_NoDayNoteWhenNeutral(t *testing.T) {
	c := &sitCaptureChatter{}
	e, _ := sitEngine(t, c)
	p := &PersonaProfile{Name: "小夜", Template: "bestie"}
	if _, err := e.dialogReply(context.Background(), DialogInput{UserText: "在吗", State: StateFree}, p); err != nil {
		t.Fatalf("dialogReply: %v", err)
	}
	if strings.Contains(c.lastSystem, "【今天的处境】") {
		t.Fatalf("心情中性却注入了处境段:\n%.400s", c.lastSystem)
	}
}

// TestSituationLine_EmptyByDefault 反面: 零值 = 什么都不给 = **整段消失**。
// 改坏成: 无条件写段头 → 红。常驻的处境段等于天天在暗示"我今天状态不好"。
func TestSituationLine_EmptyByDefault(t *testing.T) {
	if got := SituationLine(SituationInput{}); got != "" {
		t.Fatalf("零值必须不注入, 得到:\n%s", got)
	}
	if got := SituationLine(SituationInput{Now: time.Now()}); got != "" {
		t.Fatalf("只有时间也必须不注入, 得到:\n%s", got)
	}
}

// TestSituationLine_NoMoodDisclosure 反面: **不许泄露心情数值或成因** (§4.1)。
//
// 这是本段最容易写歪的地方: 顺手把"你现在心情不好"写进去, 它就会当成台词说出来 ——
// 而设计稿要的是"表现得短/慢/少表情", 不是"报备心情"。
// 改坏成: 把 in.DeclineWhy 或 valence 拼进文本 → 红。
func TestSituationLine_NoMoodDisclosure(t *testing.T) {
	line := SituationLine(SituationInput{
		Now:        time.Now(),
		DeclineOK:  true,
		DeclineWhy: "心情低(-0.90<=-0.35)且高唤醒(0.95>=0.65) —— 是烦躁, 不是低落",
		Fatigue:    2,
	})
	if line == "" {
		t.Fatal("前置: 这一组应当产出内容")
	}
	for _, leak := range []string{"-0.90", "0.95", "-0.35", "0.65", "心情低", "高唤醒", "烦躁", "低落", "valence"} {
		if strings.Contains(line, leak) {
			t.Fatalf("处境段泄露了心情判据 %q —— 它只许写约束, 不许写状态:\n%s", leak, line)
		}
	}
	// 正面: 该有的约束必须在。
	// ⚠️ 锚点用**约束本身**, 不用计数 —— 计数在 §4.6.1 内在化时已经被删掉了。
	for _, must := range []string{"不想聊", "不要要求安慰"} {
		if !strings.Contains(line, must) {
			t.Fatalf("缺约束 %q:\n%s", must, line)
		}
	}
}

// ——— 接线: 段必须真的进提示词, 且必须在天档 ———

func TestBuildSystem_DayNoteReachesPrompt(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜", Template: "bestie"}
	note := SituationLine(SituationInput{DeclineOK: true})
	if note == "" {
		t.Fatal("前置: 应当产出一段")
	}
	sys := d.buildSystem(DialogInput{UserText: "在吗", DayNote: note}, p, "ja")
	if !strings.Contains(sys, "今天不太想聊") {
		t.Fatal("日级处境段没进提示词 —— 机制等于没接线")
	}
}

// 反面: 没有处境时提示词**逐字节不变**。
func TestBuildSystem_NoDayNoteNoSegment(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜", Template: "bestie"}
	withEmpty := d.buildSystem(DialogInput{UserText: "在吗", DayNote: ""}, p, "ja")
	plain := d.buildSystem(DialogInput{UserText: "在吗"}, p, "ja")
	if withEmpty != plain {
		t.Fatal("空处境改变了提示词 —— 段必须是「没有就一个字都不加」")
	}
	if strings.Contains(plain, "【今天的处境】") {
		t.Fatal("没许可却出现了处境段")
	}
}

// ——— 端到端: 引擎真的能拿到许可, 且**真的**写进提示词 ———
//
// 上面几条测的是"给了 DayNote 会不会注入"; 这条测的是 **dayNote() 会不会给出 DayNote** ——
// 两者缺一, 整条通道仍然是断的 (BodyLine 就是这么当了一轮死代码)。
func TestDayNote_EngineEndToEnd(t *testing.T) {
	s := newTestStore(t)
	e := &Engine{Store: s, Cfg: DefaultConfig()}
	now := time.Now()

	// 前置: 中性心情 → 不给许可。
	if got := e.dayNote(now, "在吗", nil); strings.Contains(got, "不想聊") {
		t.Fatalf("心情中性却给了拒绝许可:\n%s", got)
	}
	// 低心情 + 高唤醒 → 应当给许可。
	if err := s.SaveMoodState(MoodState{Valence: -0.9, Arousal: 0.95}); err != nil {
		t.Fatal(err)
	}
	got := e.dayNote(now, "在吗", nil)
	if !strings.Contains(got, "不想聊") {
		t.Fatalf("低心情+高唤醒应当拿到拒绝许可, 却得到:\n%q", got)
	}
	// 且**必须**真的进提示词。
	sys := (&DialogEngine{}).buildSystem(DialogInput{UserText: "在吗", DayNote: got}, &PersonaProfile{Name: "小夜"}, "ja")
	if !strings.Contains(sys, "不想聊") {
		t.Fatal("许可拿到了却没进提示词")
	}
	// 反面: C 层 (安全/求助) 一票否决 —— 再差的心情也不许拒绝。
	if got := e.dayNote(now, "救命 快回我", nil); strings.Contains(got, "不想聊") {
		t.Fatalf("C 层请求不该给拒绝许可:\n%s", got)
	}
}
