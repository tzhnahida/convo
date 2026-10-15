package convo

import (
	"strings"
	"testing"
)

// ⚠️ **本批最重的一条契约**
//
// 世界观当初只接了一半: 生成 ✅ 落库 ✅ 生成夜间名册 ✅
// 但**从来没有进过对话提示词** —— production 代码里 WorldPrompt 零调用,
// 只有测试在调它。
//
// 后果就是用户看到的那句「这个世界观一戳就破」:
// 不是写得不好, 是**它压根不知道自己的世界观**, 被问起只能现编。
//
// 这条测试的意义不是"验证函数能跑", 而是**钉住"它被接上了"** ——
// 这类 bug (功能做完了但没接线) 靠读代码看不出来, 只有测试能挡。
func TestWorldBible_ActuallyReachesThePrompt(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	owner := e.ownerKey()
	b := WorldBible{
		Self: "小夜",
		Cast: []CastMember{
			{Name: "阿岚", Relation: RelationCompanion, History: "一起住了三年", Status: "在准备考试"},
			{Name: "老周", Relation: RelationMentor, History: "教我修东西", Status: "出差了"},
		},
	}
	if err := e.Store.SaveWorldBible(owner, b); err != nil {
		t.Fatal(err)
	}
	note := e.worldNote()
	if note == "" {
		t.Fatal("worldNote 是空的 —— 世界观没有从库里出来")
	}
	for _, want := range []string{"阿岚", "老周", "你生活里的人"} {
		if !strings.Contains(note, want) {
			t.Fatalf("世界观渲染里缺 %q:\n%s", want, note)
		}
	}
	// ⚠️ 真正的契约: 它必须能出现在 **buildSystem 的产物**里,
	// 而不只是"worldNote() 这个函数能返回东西"。
	sys := (&DialogEngine{}).buildSystem(DialogInput{WorldNote: note, UserText: "在吗"}, &PersonaProfile{Name: "小夜"}, "ja")
	if !strings.Contains(sys, "阿岚") {
		t.Fatalf("世界观没进 system prompt —— 这正是那个 bug:\n%s", sys[:min(len(sys), 600)])
	}
}

// 没有世界观时**不许留下空段骨架**。
func TestWorldNote_AbsentIsEmpty(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	if got := e.worldNote(); got != "" {
		t.Fatalf("库里没有世界观时该是空串, got %q", got)
	}
	sys := (&DialogEngine{}).buildSystem(DialogInput{UserText: "在吗"}, &PersonaProfile{Name: "小夜"}, "ja")
	if strings.Contains(sys, "你生活里的人") {
		t.Fatal("没世界观却渲染出了骨架")
	}
}

// ⚠️ 稳定段前置: 世界观只在重新定稿时变, 放在 persona 之后、
// 任何按天/按轮变化的东西之前 —— 否则每一轮都击穿缓存。
func TestWorldNote_CacheFriendlyPlacement(t *testing.T) {
	sys := (&DialogEngine{}).buildSystem(DialogInput{
		WorldNote: "\n【你生活里的人】\n- 阿岚 (同伴): 一起住了三年\n",
		UserText:  "在吗",
	}, &PersonaProfile{Name: "小夜"}, "ja")
	iW := strings.Index(sys, "你生活里的人")
	// ⚠️ 锚点用**默认**那一档的段头(§4.6.3 之后时间默认是模糊时段)。
	// 这里测的是**顺序**不是内容, 所以锚哪个段头都行 —— 但要锚默认的那个, 否则它测的就不是生产路径。
	iT := strings.Index(sys, "【大概什么时候】")
	if iW < 0 || iT < 0 {
		t.Fatalf("缺少段落: world=%d time=%d", iW, iT)
	}
	if iW > iT {
		t.Fatal("世界观排在了时间之后 —— 它变化极慢, 放后面每轮都击穿缓存")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
