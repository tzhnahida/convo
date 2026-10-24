package convo

import (
	"strings"
	"testing"
)

// 用户实测 (2026-10-24): 随手发的表情包被它当成有意义的话读了。
func TestIsStickerOnlyMessage(t *testing.T) {
	yes := []string{
		"(表情 — 嘲讽) [图: 表情 — 嘲讽 厌]",
		"[QQ表情178]",
		"[QQ表情178][QQ表情178][QQ表情178]",
	}
	for _, s := range yes {
		if !isStickerOnlyMessage(s) {
			t.Errorf("%q 应当被认成只发了表情", s)
		}
	}
	no := []string{
		"(照片) [图: 展示一张电源树框图]",
		"在吗",
		"(表情 — 嘲讽) 你行不行啊", // 表情+文字 = 有话说
		"",
	}
	for _, s := range no {
		if isStickerOnlyMessage(s) {
			t.Errorf("%q 不该被当成纯表情", s)
		}
	}
}

// 只发表情的那一轮, system 里必须有一句说明书: 表情包没含义, 别解读。
func TestBuildSystem_InjectsStickerOnlyNote(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "海梦"}
	sys := d.buildSystem(DialogInput{
		UserText: "(表情 — 嘲讽) [图: 表情 — 嘲讽 厌]",
	}, p, "ja")
	if !strings.Contains(sys, "只发了个表情包") {
		t.Fatal("只发表情的那一轮没有说明书 —— 它又该过度解读了")
	}
	if !strings.Contains(sys, "没有具体意思") {
		t.Fatal("说明书里没有点明「表情包没有具体意思」")
	}

	plain := d.buildSystem(DialogInput{UserText: "在吗"}, p, "ja")
	if strings.Contains(plain, "只发了个表情包") {
		t.Fatal("普通文字消息不该出现表情说明书")
	}
}

// 表情包的渲染里不许再出现「含义」—— 那是它脑补的依据。
func TestStickerRender_DropsMeaning(t *testing.T) {
	l := ImageLabel{Kind: "sticker", Act: "嘲讽", Emotion: "厌", Meaning: "看不起你"}
	got := l.Render()
	if strings.Contains(got, "看不起你") {
		t.Fatalf("表情包渲染里仍有含义: %q", got)
	}
	if !strings.Contains(got, "嘲讽") {
		t.Fatalf("行为 (act) 必须保留: %q", got)
	}
}
