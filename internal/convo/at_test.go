package convo

import (
	"encoding/json"
	"testing"
)

// TestOneBotEvent_AtUINs §6.10.2 ② 的输入。
//
// 反面: 把 @全体 在这里过滤掉 → 红。它算不算"叫我"是 IsAddressedToMe 的裁定
// (scene.go 已定: **@全体不算叫我**), IO 层先替它决定, 那条裁定以后就改不动了。
func TestOneBotEvent_AtUINs(t *testing.T) {
	// 原生数组形态: 取出 at 段。
	ev := OneBotEvent{Message: json.RawMessage(
		`[{"type":"at","data":{"qq":"12345"}},{"type":"text","data":{"text":" 你好"}}]`)}
	got := ev.AtUINs()
	if len(got) != 1 || got[0] != "12345" {
		t.Fatalf("at 段没解析出来: %v", got)
	}
	// **@全体原样返回** —— 由判据裁定, 不在 IO 层过滤。
	all := OneBotEvent{Message: json.RawMessage(`[{"type":"at","data":{"qq":"all"}}]`)}
	if g := all.AtUINs(); len(g) != 1 || g[0] != "all" {
		t.Fatalf("@全体必须在 IO 层原样返回 (由判据裁定它算不算叫我), 得到 %v", g)
	}
	// 文本里的 "@12345" 不是 at 段 —— 靠人眼认 @ 是错的, 靠段类型才对。
	text := OneBotEvent{Message: json.RawMessage(`[{"type":"text","data":{"text":"@12345 在吗"}}]`)}
	if g := text.AtUINs(); len(g) != 0 {
		t.Fatalf("文本里的 @ 被当成了 at 段: %v", g)
	}
	// 多个 at 全取。
	multi := OneBotEvent{Message: json.RawMessage(
		`[{"type":"at","data":{"qq":"1"}},{"type":"at","data":{"qq":"2"}}]`)}
	if g := multi.AtUINs(); len(g) != 2 {
		t.Fatalf("多个 at 只取到一个: %v", g)
	}
}
