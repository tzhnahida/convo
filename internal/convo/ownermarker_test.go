package convo

import (
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────
// §6.10.7 **owner 标记绝不进注入** —— 一整段扫描
//
// 设计稿的要求是「**群里不许暴露 owner 身份**」。
//
// ⚠️ 而代码里的防线是**逐点布防**的(`SceneHistoryLine` / `PersonNoteLine` /
// `SelfRoleLine` / `heat` 的护栏 5 …各有注释说明"这里没有 owner 标记")。
//
// ⚠️ **逐点布防的问题是: 漏一个点就是漏。**
// 而它漏掉的表现是**在群里公示了主人** —— 一个不可撤销的隐私事故。
//
// ⇒ 所以这里要的是**一整段扫描**: 把群场景的提示词整段造出来,
// 断言它**认不出谁是 owner**。
// ─────────────────────────────────────────────────────────────

// ownerMarkers 一段群场景提示词里**不该出现**的东西。
//
// ⚠️ 刻意**不含** uin 尾号 —— 尾号是**所有人共用**的锚
// (段 7 的发言流对每个群友都用尾号), 它不区分 owner。
var ownerMarkers = []string{"主人", "owner", "Owner", "OWNER", "管理员", "群主", "3487037837"}

// TestOwnerMarker_NeverInGroupPrompt (一整段扫描)
func TestOwnerMarker_NeverInGroupPrompt(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜", Identity: "role",
		Personality: "话不多", Description: "朋友的妹妹"}
	scene := "group:1034712035"
	// ⚠️ **把所有能填的都填上** —— 扫描的价值就在于"覆盖到那个我没想到的段"。
	in := DialogInput{
		UserText:     "你们在聊什么",
		SceneKey:     scene,
		SpeakerUIN:   "2735604537",
		History:      "某人(4537): 在吗" + "\n" + "她: 在",
		SelfPlace:    SelfRoleLine(&Scene{SceneKey: scene, SelfRole: RoleMember, Stage: StageFamiliar}, DefaultConfig().Group),
		RoleNote:     "你在群里是普通群友",
		Memories:     []Memory{{MemID: "m1", Label: "工作", V: "在准备跳槽"}},
		PersonNote:   PersonNoteLine("某人", "2735604537", []PersonMemory{{MemID: "p1", Label: "职业", V: "牙医"}}, nil),
		BondNote:     "你们聊得挺多",
		RegisterNote: "最近的语气偏随意",
		GuardNote:    "别打听住址",
		Boundaries:   []string{"别问住址", "别提体重"},
		IntentNotes:  []string{"答应过周末一起打游戏"},
		DayMoodNote:  "今天心情一般",
		TasteNote:    "最近喜欢上柠檬茶",
		RevealNote:   "上次说过一件小事",
		ConflictNote: "群里刚有过一次口角",
		Association:  "柠檬茶 → 上次那家店",
		EventNotes:   []EventNote{{Text: "昨天楼下开了家新店"}},
	}
	sys := d.buildSystem(in, p, "ja")
	if len(sys) < 1000 {
		t.Fatalf("提示词只有 %d 字节 —— 夹具大概没造起来, 扫描是空的", len(sys))
	}
	// ⚠️ **先证明这把尺子不是空的** —— 夹具必须真的把那些段渲染进提示词了,
	// 否则"没扫到 owner 标记"只是因为"那一段根本没渲染",
	// 而那是一条**永远绿**的用例(比没有更糟: 它给人一种被守住了的错觉)。
	// ⚠️ `EventNotes` 那一段的正文要 `Stage` 非空才渲染, 所以哨兵取**标题**
	// (标题无论 Stage 是什么都会写出来)。
	for _, want := range []string{"牙医", "群里的位置", "柠檬茶", "别问住址", "你最近遇到的事"} {
		if !strings.Contains(sys, want) {
			t.Errorf("夹具没把这一段渲染出来(%q 不在提示词里) —— 扫描对它是空转", want)
		}
	}
	for _, m := range ownerMarkers {
		if strings.Contains(sys, m) {
			// ⚠️ 把那一行打出来 —— 只说"命中了"没法查。
			for _, ln := range strings.Split(sys, "\n") {
				if strings.Contains(ln, m) {
					t.Errorf("群提示词里出现了 owner 标记 %q:\n  %s", m, ln)
				}
			}
		}
	}
}

// TestOwnerMarker_PrivateIsAllowed (反面)
//
// ⚠️ 这条**不是**在说"私聊可以随便写主人" ——
// 而是说上面那条扫描**只对群成立**: 断言写宽了会变成一条永远红或永远绿的用例。
func TestOwnerMarker_PrivateIsAllowed(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜", Identity: "role"}
	sys := d.buildSystem(DialogInput{UserText: "在吗", SceneKey: DefaultSceneKey}, p, "ja")
	// 私聊里**不该**有群角色位那一档(没有群就没有群里的位置)。
	if strings.Contains(sys, "你在群里的位置") {
		t.Error("私聊提示词里出现了群角色位 —— 那是串台")
	}
}
