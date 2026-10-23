package convo

import (
	"strings"
	"testing"
)

// 被骂时的反应**随角色** —— owner 2026-10-23 专门纠正过的点。
func TestMoralsNote_ReactionComesFromPersona(t *testing.T) {
	marin := &PersonaProfile{Name: "喜多川海梦", Boundaries: Boundaries{
		InsultReaction: "先委屈装傻, 再反夸一句; 不反击",
	}}
	got := MoralsNote(marin)
	if !strings.Contains(got, "先委屈装傻") {
		t.Fatalf("人设写的反应没有进底线段: %q", got)
	}
	if !strings.Contains(got, "喜多川海梦") {
		t.Fatal("底线段里没有角色名")
	}

	other := &PersonaProfile{Name: "毒舌角色", Boundaries: Boundaries{
		InsultReaction: "冷处理, 直接不接",
	}}
	if s := MoralsNote(other); !strings.Contains(s, "冷处理") || strings.Contains(s, "委屈") {
		t.Fatalf("换角色后反应没跟着换: %q", s)
	}
}

// 人设没写反应 → 用通用柔和默认, 而不是空串。
func TestMoralsNote_DefaultReactionWhenUnset(t *testing.T) {
	got := MoralsNote(&PersonaProfile{Name: "小夜"})
	if !strings.Contains(got, DefaultInsultReaction) {
		t.Fatalf("缺少默认被骂反应: %q", got)
	}
	if !strings.Contains(got, "不侮辱人") {
		t.Fatal("缺少通用底线")
	}
}

// 角色自己的底线也要注进去。
func TestMoralsNote_PersonaHardLines(t *testing.T) {
	p := &PersonaProfile{Name: "海梦", Boundaries: Boundaries{
		HardLines: []string{"不拿朋友的梦想开玩笑"},
	}}
	if got := MoralsNote(p); !strings.Contains(got, "不拿朋友的梦想开玩笑") {
		t.Fatalf("角色底线没注入: %q", got)
	}
}

// 出口词表: 骂人话必须拦下, 正常聊天不许误伤。
func TestMoralViolations(t *testing.T) {
	bad := []string{"杂鱼杂鱼", "你就是个废物", "滚蛋吧你", "去死"}
	for _, s := range bad {
		if len(MoralViolations(s)) == 0 {
			t.Errorf("%q 应当被道德词表拦下", s)
		}
	}
	ok := []string{"今天好累啊", "笑死 这也太惨了", "你这个战术真能磨死人", ""}
	for _, s := range ok {
		if hits := MoralViolations(s); len(hits) > 0 {
			t.Errorf("%q 被误伤: %v", s, hits)
		}
	}
}

// 底线段要真的进 system prompt。
func TestBuildSystem_ContainsMoralsNote(t *testing.T) {
	d := &DialogEngine{}
	note := MoralsNote(&PersonaProfile{Name: "海梦", Boundaries: Boundaries{InsultReaction: "委屈装傻"}})
	sys := d.buildSystem(DialogInput{UserText: "滚", MoralsNote: note}, &PersonaProfile{Name: "海梦"}, "ja")
	if !strings.Contains(sys, "【你的底线】") || !strings.Contains(sys, "委屈装傻") {
		t.Fatal("道德底线段没有进系统提示词")
	}
}
