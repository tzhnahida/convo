package convo

import (
	"strings"
	"testing"
	"time"
)

// 2026-10-30 用户: 「问候类呢?！」—— 预告必须是问候, 不能是评论。
func TestLooksLikeGreeting(t *testing.T) {
	yes := []string{"早 起了没", "早啊", "在吗 今天忙不忙", "昨晚睡得怎么样", "哈喽"}
	for _, s := range yes {
		if !looksLikeGreeting(s) {
			t.Errorf("%q 应当算问候", s)
		}
	}
	no := []string{
		"这么早就坐着看书了", // 评论 (实测中招)
		"周末赶什么衣服",   // 关于旧话题的提问 (实测中招)
		"我这边刚起 脑子还没转",
		"",
	}
	for _, s := range no {
		if looksLikeGreeting(s) {
			t.Errorf("%q 不该算问候", s)
		}
	}
}

// 兜底问候必须按小时给出非空句子。
func TestFallbackGreeting_NonEmpty(t *testing.T) {
	for h := 0; h < 24; h++ {
		now := time.Date(2026, 10, 30, h, 0, 0, 0, time.Local)
		got := fallbackGreeting(now)
		if strings.TrimSpace(got) == "" {
			t.Fatalf("%d 点的兜底问候为空", h)
		}
		if !looksLikeGreeting(got) {
			t.Fatalf("%d 点的兜底句自己都不像问候: %q", h, got)
		}
	}
}

// 提示词里必须写明「开头必须是一句问候」, 否则模型又会写评论。
func TestPreviewPrompt_RequiresGreetingOpening(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "海梦"}
	sys := d.buildSystem(DialogInput{ActionKind: ActPreview, State: StateFree, PreviewNext: StateBusy, PreviewAt: "07:30"}, p, "ja")
	for _, want := range []string{"开头必须是一句问候", "不许用评论开头", "周末赶什么衣服"} {
		if !strings.Contains(sys, want) {
			t.Fatalf("提示词缺少 %q", want)
		}
	}
}
