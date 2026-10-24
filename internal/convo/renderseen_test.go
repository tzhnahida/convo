package convo

import (
	"strings"
	"testing"
)

// ⚠️ **本批的头号契约** (用户 2026-10-14 定的口径):
//
//	第一次出现:  [图: 忍不住多咬一口, 撒娇式道歉](照片)
//	之后出现:    [图: 这张图出现过多次, 内容往上找](照片)
//
// 为什么: 群里同一张贴纸会发几十次。每次都把完整描述铺一遍,
// 上下文会被同一份内容撑爆 —— 模型看到的是一堆一模一样的行。
func TestRenderSeen_FirstFullThenReference(t *testing.T) {
	l := ImageLabel{Kind: "photo", Meaning: "忍不住多咬一口, 撒娇式道歉"}
	first := l.RenderSeen(1)
	if !strings.Contains(first, "忍不住多咬一口") {
		t.Fatalf("第一次必须给完整内容: %q", first)
	}
	if !strings.Contains(first, "照片") {
		t.Fatalf("第一次也要带短标, 否则后面「往上找」对不上: %q", first)
	}
	for _, n := range []int{2, 3, 20} {
		got := l.RenderSeen(n)
		if strings.Contains(got, "忍不住多咬一口") {
			t.Fatalf("第 %d 次不该再铺一遍内容: %q", n, got)
		}
		if !strings.Contains(got, "往上找") {
			t.Fatalf("第 %d 次该指向前面: %q", n, got)
		}
		// ⚠️ 短标必须**和第一次那份对得上** —— 否则"往上找"找不到东西。
		if !strings.Contains(got, "照片") {
			t.Fatalf("第 %d 次丢了短标, 对不上第一次: %q", n, got)
		}
	}
}

// 表情包同理, 而且它的短标是**行为** (那是它最需要的信息)。
func TestRenderSeen_StickerKeepsActInTag(t *testing.T) {
	l := ImageLabel{Kind: "sticker", Act: "嘲讽", Emotion: "厌", Meaning: "看不起你"}
	first := l.RenderSeen(1)
	if !strings.Contains(first, "嘲讽") {
		t.Fatalf("第一次该有行为: %q", first)
	}
	// ⚠️ 2026-10-24: 表情包**不再渲染含义** —— 那是过度解读的源头。
	if strings.Contains(first, "看不起你") {
		t.Fatalf("表情包不该再把含义铺进上下文: %q", first)
	}
	repeat := l.RenderSeen(5)
	if !strings.Contains(repeat, "嘲讽") {
		t.Fatalf("重复时**行为必须留着** —— 那正是它要读的东西: %q", repeat)
	}
	if strings.Contains(repeat, "看不起你") {
		t.Fatalf("重复时不该再铺含义: %q", repeat)
	}
}

// Render() 是 RenderSeen(1) 的别名 —— 老调用点行为不变。
func TestRender_EqualsFirstSeen(t *testing.T) {
	l := ImageLabel{Kind: "sticker", Act: "附和", Meaning: "同意"}
	if l.Render() != l.RenderSeen(1) {
		t.Fatal("Render 应当等于首次渲染")
	}
}

// ⚠️ 图上的字是**锚** (图文双构) —— 首次必须带上。
func TestRenderSeen_FirstKeepsImageText(t *testing.T) {
	l := ImageLabel{Kind: "sticker", Act: "阴阳怪气", Text: "你说得对", Meaning: "嘴上同意"}
	if got := l.RenderSeen(1); !strings.Contains(got, "你说得对") {
		t.Fatalf("首次丢了图上的字 (那是解读方向的锚): %q", got)
	}
}

// 没读到 ≠ 看到。两者措辞必须一眼分得开 —— 混了它就会编「图裂了」。
func TestImageMarks_AreDistinguishable(t *testing.T) {
	if ImageUnreadMark == ImagePhotoMark {
		t.Fatal("「没读到」和「看到了」不许是同一个标记")
	}
	if !strings.Contains(ImageUnreadMark, "没读到") {
		t.Fatalf("没读到的标记要明说: %q", ImageUnreadMark)
	}
	if !strings.Contains(ImageUnreadMark, "别猜") {
		t.Fatalf("没读到的标记要禁止猜: %q", ImageUnreadMark)
	}
}
