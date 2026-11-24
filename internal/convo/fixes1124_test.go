package convo

import (
	"fmt"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────
// 2026-11-24 用户批准 1–4 (串事件修复) 的回归用例。
//
// 起因: 12:36 有人抱怨「材料力学花了 6 块钱的 token」, 12:38 同一个人发图说
// 「wc域名回来了」, 它回「回来了就好 别再花几块钱token了」—— A 事件的词接进 B 事件。
//
// 1 线程化上下文 · 2 判不出时只给锚点+前一条 · 3 提示词标题 · 4 群里降级只留表情
// ─────────────────────────────────────────────────────────────

func threadRows(t *testing.T, got string) int {
	t.Helper()
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(got), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "【群1=") {
			continue
		}
		n++
	}
	return n
}

func seedThread(t *testing.T, s *Store, scene string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := s.InsertMessageFull("user", "qq", "text",
			fmt.Sprintf("第%d句闲聊", i), MsgMeta{SceneKey: scene, SenderUIN: "111"}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
}

// 1+2: 插话档(narrow) 只给锚点 + 前一条 —— 没有线索支撑更长的上下文。
func TestThreadHistory_NarrowIsAnchorPlusOne(t *testing.T) {
	s, err := OpenStore(t.TempDir(), "thread.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer s.Close()
	e := &Engine{Store: s, Cfg: DefaultConfig()}
	scene := "group:999"
	seedThread(t, s, scene, 6)

	got := e.recentThreadIn(scene, 12, true)
	if rows := threadRows(t, got); rows != 2 {
		t.Fatalf("narrow 应给 2 行 (锚点+前一条), 实得 %d: %q", rows, got)
	}
	if !strings.Contains(got, "第5句闲聊") {
		t.Fatalf("最新那条必须在窗口里: %q", got)
	}
	if strings.Contains(got, "第0句闲聊") {
		t.Fatalf("更早的话题不该进来: %q", got)
	}
}

// 1: 正常档最多锚点 + 6 条, 且最新那句在最后一行 (渲染方向)。
func TestThreadHistory_TextAnchorCapsAt7(t *testing.T) {
	s, err := OpenStore(t.TempDir(), "thread2.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer s.Close()
	e := &Engine{Store: s, Cfg: DefaultConfig()}
	scene := "group:998"
	seedThread(t, s, scene, 12)

	got := e.recentThreadIn(scene, 12, false)
	if rows := threadRows(t, got); rows > 7 {
		t.Fatalf("纯文字锚点最多 7 行, 实得 %d: %q", rows, got)
	}
	lines := strings.Split(strings.TrimSpace(got), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, "第11句闲聊") {
		t.Fatalf("最新一条应在最后一行 (它是锚点), 实际最后一行: %q", last)
	}
}

// 1: 锚点自带图 → 只给锚点 (看见图就直接对图说话, 不翻上一条)。
func TestThreadHistory_PictureAnchorAlone(t *testing.T) {
	s, err := OpenStore(t.TempDir(), "thread3.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer s.Close()
	e := &Engine{Store: s, Cfg: DefaultConfig()}
	scene := "group:997"
	seedThread(t, s, scene, 4)
	if _, err := s.InsertMessageFull("user", "qq", "text",
		"(照片) [图: Cadence 登录界面]", MsgMeta{SceneKey: scene, SenderUIN: "222"}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got := e.recentThreadIn(scene, 12, false)
	if rows := threadRows(t, got); rows != 1 {
		t.Fatalf("带图锚点只给 1 行, 实得 %d: %q", rows, got)
	}
	if strings.Contains(got, "第3句闲聊") {
		t.Fatalf("图之前的话题不该进来: %q", got)
	}
}

// 3: 提示词标题改掉了 —— 不再要求"与上下句连贯回应"(那等于让它把几条线串起来)。
func TestPromptHeader_ThreadScoped(t *testing.T) {
	d := &DialogEngine{}
	got := d.buildSystem(DialogInput{History: "1 我: 在吗\n2 我: 在"}, &PersonaProfile{Name: "海梦"}, "zh")
	if !strings.Contains(got, "【现在这条线】") {
		t.Fatalf("缺【现在这条线】标题: %q", got)
	}
	if strings.Contains(got, "【最近对话】") {
		t.Fatalf("旧标题应已移除: %q", got)
	}
	if !strings.Contains(got, "别的话题不要接进来") {
		t.Fatalf("缺少「别接别的线」的硬约束: %q", got)
	}
}

// 4: 群里降级产物只留表情; 没有表情就沉默 (兜底句会把别的事接进来)。
func TestGroupDegrade_StickerOnly(t *testing.T) {
	withSticker := OutputFrame{Reply: []FrameItem{
		{Kind: "text", Text: "回来了就好 别再花几块钱token了"},
		{Kind: "sticker", StickerID: 7},
	}}
	got := groupDegradeReaction(withSticker)
	if len(got.Reply) != 1 || got.Reply[0].Kind != "sticker" {
		t.Fatalf("降级后应只剩表情, 实得 %+v", got.Reply)
	}

	textOnly := OutputFrame{Reply: []FrameItem{{Kind: "text", Text: "串台的那句话"}}}
	if got := groupDegradeReaction(textOnly); len(got.Reply) != 0 {
		t.Fatalf("没有表情时应沉默, 实得 %+v", got.Reply)
	}
}
