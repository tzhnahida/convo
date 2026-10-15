package convo

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/tzhnahida/VocabLock/internal/clock"
)

// ——— v2.4.1 拟人升级包 ———

func TestReplyGapSeconds_rangeAndLongRatio(t *testing.T) {
	r := rand.New(rand.NewSource(42))
	old := Dice
	Dice = r.Intn // 抽样走骰子接缝, 测试自己钉种子
	defer func() { Dice = old }()
	const N = 2000
	long := 0
	for i := 0; i < N; i++ {
		g := replyGapSeconds(1.0)
		if g < 1 || g > 10 {
			t.Fatalf("gap out of range: %d", g)
		}
		if g >= 6 {
			long++
		}
	}
	ratio := float64(long) / N
	if ratio < 0.05 || ratio > 0.2 {
		t.Fatalf("long-gap ratio = %.3f, want ~0.10", ratio)
	}
}

func TestBuildSystem_v241_injections(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜", Description: "测试"}
	sys := d.buildSystem(DialogInput{
		UserText: "hi",
		Slangs:   []Slang{{Phrase: "麻了", Meaning: "累到无语"}},
		// ⚠️ 显式要精确那一档: 本测试断言的是"段 9 存在且在末尾"这个**位置**事实。
		// 默认那一档(模糊时段、没有分钟)由 TestVagueNowLine_GivesFeelingNotReading 覆盖 ——
		// 两边各测各的, 不许因为改默认值就把这条一起改松。
		TimePrecise: true,
	}, p, "ja")
	for _, want := range []string{"最高原则", "先像朋友、像真人", "【当前时间】", "UTC", "【示例对话】",
		"不要: ", "你可以: ", "出现即失败", "两个 text 项", "每句话最多 1 个梗", "1~15 字",
		// ⚠️ 2026-10-14 生产事故后新增的两条硬线:
		// ① 没有条数上限 → 对方发一句话, 它连回 5 条 (刷屏);
		// ② 没有反虚构约束 → 对方只发了一条, 它却说「你这一串刷屏」。
		"reply 最多 2 条", "不许凭空断言", "不许有无来由的反应",
		// ⚠️ Day-1 首轮审查的三条 (全是"防御性 AI 味"):
		"不许自证", "不许给对话画句号", "绝对不许论证",
		// ⚠️ 真实语料分析得出的两条 (112 条人话: 中位 5 字, 58% ≤5 字, 只有 8% 带问号):
		"单字/两三字回复是常态", "少用问句"} {
		if !strings.Contains(sys, want) {
			t.Fatalf("buildSystem missing %q", want)
		}
	}
	// 时间戳必须在最末 (缓存尾巴, 不破坏前缀); 时间行本身 ~45 字符
	if i := strings.LastIndex(sys, "【当前时间】"); i < len(sys)-70 {
		t.Fatalf("【当前时间】应在末尾: ...%s", sys[i:])
	}
}

func TestBuildSystem_v241_negativeExamples(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜", Description: "测试"}
	sys := d.buildSystem(DialogInput{UserText: "hi"}, p, "ja")
	// 无梗库 → 不得出现【你会的梗】段
	if strings.Contains(sys, "【你会的梗】") {
		t.Fatalf("no-slang case should not inject 黑话表")
	}
}

func TestSideEffects_rememberCapAndStore(t *testing.T) {
	s := newTestStore(t)
	d := &DialogEngine{Store: s}
	for i := 0; i < 4; i++ {
		d.SideEffects(OutputFrame{
			Remember: &FrameRemember{K: "plan", V: fmt.Sprintf("第%d件事", i), Label: "事"},
		}, DialogInput{}, nil)
	}
	n, _ := s.CounterGet("ai_remember", clock.Today())
	if n != 3 {
		t.Fatalf("ai_remember counter = %d, want 3 (cap)", n)
	}
	var cnt int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM convo_memory`).Scan(&cnt)
	if cnt != 3 {
		t.Fatalf("convo_memory rows = %d, want 3", cnt)
	}
	m, err := s.SlangByPhrase("__never__") // 无关查询仅确认 store 可用
	_ = m
	_ = err
	// 记忆应为 active 且带 ai_ 前缀 mem_id
	var state, memID string
	_ = s.DB.QueryRow(`SELECT state, mem_id FROM convo_memory LIMIT 1`).Scan(&state, &memID)
	if state != "active" || !strings.HasPrefix(memID, "ai_") {
		t.Fatalf("remember 落库异常: state=%s mem_id=%s", state, memID)
	}
}

func TestSideEffects_rememberEmptyIgnored(t *testing.T) {
	s := newTestStore(t)
	d := &DialogEngine{Store: s}
	d.SideEffects(OutputFrame{Remember: &FrameRemember{K: "x", V: "   "}}, DialogInput{}, nil)
	n, _ := s.CounterGet("ai_remember", clock.Today())
	if n != 0 {
		t.Fatalf("empty remember should not count, got %d", n)
	}
}

func TestValidateFrame_rememberEmptyNoViolation(t *testing.T) {
	d := &DialogEngine{}
	v := d.ValidateFrame(OutputFrame{
		Reply:    []FrameItem{{Kind: "text", Text: "好"}},
		Remember: &FrameRemember{K: "k", V: ""},
	}, DialogInput{}, nil)
	if len(v) != 0 {
		t.Fatalf("empty remember must not trigger retry: %v", v)
	}
}
