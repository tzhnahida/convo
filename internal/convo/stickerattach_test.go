package convo

import (
	"os"
	"path/filepath"
	"testing"
)

func stickerAttachEngine(t *testing.T) *Engine {
	t.Helper()
	s := newTestStore(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "s.gif")
	if err := os.WriteFile(p, []byte("GIF89a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertSticker("https://fake/x.gif", p, "ja", "builtin", 0.9, ""); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	return &Engine{Store: s, Cfg: cfg}
}

func TestMaybeAttachSticker_Hits(t *testing.T) {
	e := stickerAttachEngine(t)
	if e.Cfg.Stickers.AttachPct != DefaultStickerAttachPct {
		t.Fatalf("默认概率不是 %d: %d", DefaultStickerAttachPct, e.Cfg.Stickers.AttachPct)
	}
	e.StickerRollFn = func(n int) int { return 0 } // 0 < 40 → 命中
	frame := OutputFrame{Reply: []FrameItem{{Kind: "text", Text: "嗯 在的"}}}
	got := e.maybeAttachSticker(frame)
	if len(got.Reply) != 2 || got.Reply[1].Kind != "sticker" {
		t.Fatalf("命中了却没附表情: %+v", got.Reply)
	}
	if got.Reply[1].StickerID == 0 {
		t.Fatal("附的表情没带 id —— 发送时会被当成'系统代选'再抽一次")
	}
}

func TestMaybeAttachSticker_MissRoll(t *testing.T) {
	e := stickerAttachEngine(t)
	e.StickerRollFn = func(n int) int { return 99 } // ≥ 40 → 不中
	frame := OutputFrame{Reply: []FrameItem{{Kind: "text", Text: "嗯"}}}
	if got := e.maybeAttachSticker(frame); len(got.Reply) != 1 {
		t.Fatalf("没中却附了: %+v", got.Reply)
	}
}

// 模型自己已经选了表情 → 不许再塞一张。
func TestMaybeAttachSticker_RespectsModelsOwnSticker(t *testing.T) {
	e := stickerAttachEngine(t)
	e.StickerRollFn = func(int) int { return 0 }
	frame := OutputFrame{Reply: []FrameItem{
		{Kind: "text", Text: "笑死"},
		{Kind: "sticker", StickerID: 1},
	}}
	if got := e.maybeAttachSticker(frame); len(got.Reply) != 2 {
		t.Fatalf("重复加了表情: %+v", got.Reply)
	}
}

// 纯表情帧 / 空帧不附。
func TestMaybeAttachSticker_RequiresText(t *testing.T) {
	e := stickerAttachEngine(t)
	e.StickerRollFn = func(int) int { return 0 }
	if got := e.maybeAttachSticker(OutputFrame{}); len(got.Reply) != 0 {
		t.Fatalf("空帧被加了东西: %+v", got.Reply)
	}
	if got := e.maybeAttachSticker(OutputFrame{Reply: []FrameItem{{Kind: "text", Text: "  "}}}); len(got.Reply) != 1 {
		t.Fatalf("纯空白文字被当成有文字: %+v", got.Reply)
	}
}
