package convo

import (
	"strings"
	"testing"
)

// ——— §6.10.6「你在群里的位置」 ———

func placeScene(stage string) *Scene {
	return &Scene{SceneKey: "group:999", Kind: SceneKindGroup, Platform: "qq",
		GroupID: "999", SelfRole: RoleMember, Stage: stage}
}

// TestSelfRoleLine_NoOwnerMark **护栏 2 / §6.10.7**。
//
// 反面: 段里带出「谁是主人」→ 等于在群里公示关系 → 红。
//
// ⚠️ 但"**它自己**是群主"是**可以**说的 —— 那是它在群里的角色,
// 与"谁是它的主人"是两件事。这条界线最容易写糊。
func TestSelfRoleLine_NoOwnerMark(t *testing.T) {
	sc := placeScene(StageStranger)
	sc.SelfRole = RoleOwner // 它是这个群的群主
	line := SelfRoleLine(sc, GroupConfig{})
	if line == "" {
		t.Fatal("位置段应当产出内容")
	}
	// "群主"是它自己的角色 → 必须说。
	if !strings.Contains(line, "群主") {
		t.Fatalf("它自己的群角色没说:\n%s", line)
	}
	// 但**不许**出现把 owner 当成"主人"的措辞 —— 那是关系, 不是角色。
	for _, leak := range []string{"主人", "owner", "管理员是你"} {
		if strings.Contains(line, leak) {
			t.Fatalf("位置段泄露了关系标记 %q —— 等于在群里公示:\n%s", leak, line)
		}
	}
}

// TestSelfRoleLine_StageDrivesBehavior §6.10.5 三档。
// 反面: 三档给同一句 → "陌生期多听少说"这条约束形同虚设 → 红。
func TestSelfRoleLine_StageDrivesBehavior(t *testing.T) {
	stranger := SelfRoleLine(placeScene(StageStranger), GroupConfig{})
	familiar := SelfRoleLine(placeScene(StageFamiliar), GroupConfig{})
	intimate := SelfRoleLine(placeScene(StageIntimate), GroupConfig{})
	if stranger == familiar || familiar == intimate {
		t.Fatal("三档行为应当不同 —— 否则关系阶段对群里的表现没有任何影响")
	}
	if !strings.Contains(stranger, "不接梗") {
		t.Fatalf("陌生档必须明说「不接梗、不点名」:\n%s", stranger)
	}
	if !strings.Contains(intimate, "怼") {
		t.Fatalf("自己人档应当允许怼:\n%s", intimate)
	}
	// ⚠️ 零值 stage = **未知** → 按**最陌生**处理 (保守的一侧)。
	zero := SelfRoleLine(&Scene{GroupID: "999"}, GroupConfig{})
	if !strings.Contains(zero, "不接梗") {
		t.Fatalf("零值 stage 该按最陌生处理 (未知不等于自己人):\n%s", zero)
	}
}

// TestBuildSystem_SelfPlaceStaysInStableZone **段序契约**。
//
// 反面: 把位置段挪到段 2 之后 → 段 2/3 两个**稳定段**被推到新断点之后,
// 它们的前缀缓存**直接毁掉** → 红。
//
// 依据: L929「固定位 > 人设厚度」—— 位置是稳定档, 与 persona 同级;
// 段序表要求"逐字节稳定的段落全部前置"。
func TestBuildSystem_SelfPlaceStaysInStableZone(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜"}
	place := SelfRoleLine(placeScene(StageStranger), GroupConfig{})
	sys := d.buildSystem(DialogInput{SceneKey: "group:999", SelfPlace: place}, p, "ja")

	iPlace := strings.Index(sys, "【你在群里的位置】")
	if iPlace < 0 {
		t.Fatal("位置段没进提示词 —— 机制等于没接线")
	}
	iExample := strings.Index(sys, "【示例对话】")
	if iExample >= 0 && iPlace > iExample {
		t.Fatalf("位置段跑到了段 2 之后 (place=%d example=%d) —— 那会把两个稳定段推出前缀", iPlace, iExample)
	}
	// 反面: 没给 SelfPlace 时提示词里不许出现这一段。
	plain := d.buildSystem(DialogInput{SceneKey: "group:999"}, p, "ja")
	if strings.Contains(plain, "【你在群里的位置】") {
		t.Fatal("没给位置却注入了 —— 空串必须整段消失")
	}
}
