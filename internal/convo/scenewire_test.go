package convo

import (
	"testing"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
)

// ─────────────────────────────────────────────────────────────
// 四张新表的**写入口**
//
// ⚠️ 这一片原来"只有读侧、没有写侧":
//
//   · `convo_scene`        0 行 → `convo.go` 的 `SelfRoleLine` 段永远不注入,
//                                    `conflictwire.go` 的 stage 永远读空;
//   · `convo_scene_member` 0 行 → `IsNewMember` 恒 true (所有人永远是新人)。
//
// 而那两个读侧**已经接好了** —— 这一种失效棘轮扫不到:
// 代码看起来完全正常, 只是它读的东西没人往里写。
// ─────────────────────────────────────────────────────────────

const sceneWireKey = "group:1034712035"

func sceneWireCfg() GroupConfig {
	return GroupConfig{Enabled: true, Whitelist: []string{"1034712035"}}
}

func sceneWireSay(e *Engine, uin, text string) {
	e.GateGroupMessage(SceneMsg{
		SceneKey: sceneWireKey, UIN: uin, Text: text, TS: clock.Now(),
	}, SelfIdentity{UIN: "3487037837"}, sceneWireCfg())
}

// TestSceneWire_GroupMessageRegistersTheScene
//
// ⚠️ **接线尺子**: 一条群消息走完之后, 那张表必须有行。
func TestSceneWire_GroupMessageRegistersTheScene(t *testing.T) {
	e := gateEngine(t)
	if err := EnsureSceneSchema(e.Store); err != nil {
		t.Fatal(err)
	}
	if sc, _ := e.Store.LoadScene(sceneWireKey); sc != nil {
		t.Fatal("夹具坏了: 一开始就有场景行")
	}
	sceneWireSay(e, "111", "今天天气不错")
	sc, err := e.Store.LoadScene(sceneWireKey)
	if err != nil {
		t.Fatal(err)
	}
	if sc == nil {
		t.Fatal("**群消息走完了, 场景表还是一行都没有** —— 那条读侧永远读到 nil")
	}
	if sc.Kind != SceneKindGroup {
		t.Fatalf("kind 推错了: %q", sc.Kind)
	}
	// ⚠️ 新行的 self_role 必须是**空**(留空 → EffectiveSelfRole 落 member 档),

	// 而 stage 落在最保守档。
	if sc.SelfRole != "" {
		t.Fatalf("新场景不该有 self_role, 实得 %q", sc.SelfRole)
	}
	if sc.Stage != StageStranger {
		t.Fatalf("新场景应当落在陌生人档, 实得 %q", sc.Stage)
	}
}

// TestSceneWire_EnsureSceneNeverClobbers (⚠️ 这条比上面重要)
//
// ⚠️ 群消息那条路是**每一轮**都要登记的。所以它用的原语必须是
// **只插入、绝不覆盖** —— 否则:
//
//	· `stage` 每轮被重置回"陌生人"(而 conflictwire 正读着它);
//	· `self_role` 每轮被抹成 "", 谁在管理页设了角色下一秒就没了。
func TestSceneWire_EnsureSceneNeverClobbers(t *testing.T) {
	e := gateEngine(t)
	if err := EnsureSceneSchema(e.Store); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.EnsureScene(sceneWireKey); err != nil {
		t.Fatal(err)
	}
	first, _ := e.Store.LoadScene(sceneWireKey)
	// 模拟"有人在管理页设过" + "阶段推进过"。
	if _, err := e.Store.DB.Exec(
		"UPDATE convo_scene SET self_role=?, stage=? WHERE scene_key=?", "admin", StageFamiliar, sceneWireKey); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := e.Store.EnsureScene(sceneWireKey); err != nil {
			t.Fatal(err)
		}
	}
	after, _ := e.Store.LoadScene(sceneWireKey)
	if after.SelfRole != "admin" {
		t.Fatalf("**self_role 被每轮登记抹掉了**: %q —— 那是纪律第一条的反面", after.SelfRole)
	}
	if after.Stage != StageFamiliar {
		t.Fatalf("**stage 被每轮登记重置了**: %q —— 而冲突判定正读着它", after.Stage)
	}
	if after.FirstSeenTS != first.FirstSeenTS {
		t.Fatalf("first_seen_ts 被改动了: %q → %q", first.FirstSeenTS, after.FirstSeenTS)
	}
}

// TestSceneWire_GroupMessageRegistersTheMember
//
// ⚠️ 没有它 `IsNewMember` 恒 true —— **所有人永远是新人**。
//
// ⚠️ **`IsNewMember` 的语义是"首次出现距今在窗口内"**, 不是"有没有见过"。
// 我刚登记完一个人在 1 小时窗口里它**仍然算新人** —— 那是对的(新人档要持续一会儿)。
// 我第一版把它读成"见过就不是新人", 于是断言反了。这一条改成直接钉**那一行事实**:
// 「他到底有没有被登记」。
func TestSceneWire_GroupMessageRegistersTheMember(t *testing.T) {
	e := gateEngine(t)
	if err := EnsureSceneSchema(e.Store); err != nil {
		t.Fatal(err)
	}
	if m, _ := e.Store.SceneMemberByUIN(sceneWireKey, "111"); m != nil {
		t.Fatal("夹具坏了: 一开始就有成员行")
	}
	sceneWireSay(e, "111", "在吗")
	m, err := e.Store.SceneMemberByUIN(sceneWireKey, "111")
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		t.Fatal("**群里说过话之后成员表还是一行都没有** —— 新人判定会永远是新人")
	}
	if m.Stage != StageStranger {
		t.Fatalf("新成员应当落在陌生人档, 实得 %q", m.Stage)
	}
	// ⚠️ 而**没说过话的**必须仍然没有行 —— 不许"见过一个人就当全群都认识"。
	if other, _ := e.Store.SceneMemberByUIN(sceneWireKey, "999"); other != nil {
		t.Fatal("没发过言的 uin 也被登记了")
	}
	// ⚠️ 窗口归零 → 窗口内"新人"这条判据不再命中 (逐字节钉住语义)。
	if isNew, _ := e.Store.IsNewMember(sceneWireKey, "111", time.Now(), 0); isNew {
		t.Fatal("零窗口下已登记的成员仍被判成新人 —— 语义变了")
	}
	if isNew, _ := e.Store.IsNewMember(sceneWireKey, "999", time.Now(), 0); !isNew {
		t.Fatal("没有行的 uin 应当恒为新人")
	}
}

// TestSceneWire_WhitelistStillGuardsTheTables (反面)
//
// ⚠️ 不在白名单的群**不许**在表里留下任何痕迹 ——
// 登记写在白名单**之后**, 那是有意的。
func TestSceneWire_WhitelistStillGuardsTheTables(t *testing.T) {
	e := gateEngine(t)
	if err := EnsureSceneSchema(e.Store); err != nil {
		t.Fatal(err)
	}
	e.GateGroupMessage(SceneMsg{SceneKey: "group:999", UIN: "111", Text: "喂", TS: clock.Now()},
		SelfIdentity{UIN: "3487037837"}, sceneWireCfg())
	if sc, _ := e.Store.LoadScene("group:999"); sc != nil {
		t.Fatal("**不在白名单的群也被登记了** —— 表被不服务的群污染")
	}
	if isNew, _ := e.Store.IsNewMember("group:999", "111", time.Now(), time.Hour); !isNew {
		t.Fatal("不在白名单的群也写了成员行")
	}
}
