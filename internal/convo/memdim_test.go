package convo

import (
	"testing"
)

// ─────────────────────────────────────────────────────────────
// 三张新表的主键维度
//
// ⚠️ **`mem_id` 是全局 UNIQUE 的, 而 `scene_key` 是另一列** —— 于是:
//
//
//     A 群写 mem_id="m1" 内容="A 群的梗"   → 一行 (scene_key=A, v="A 群的梗")
//     B 群写 mem_id="m1" 内容="B 群的梗"   → ON CONFLICT(mem_id) 命中 A 那行,
//                                           **把 v 改成 "B 群的梗", 而 scene_key 仍是 A**
//
// 结果: **B 永远看不到自己的梗, 而 A 的梗被 B 的内容顶掉了** ——
// 静默跨群串, 而且两边都看不出是坏了。
//
// ⚠️ 一条记忆的身份是 **`(场景, mem_id)`**, 不是 `mem_id` ——
// 表的约束应当说同一件事。
// ─────────────────────────────────────────────────────────────

const memDimA = "group:8001"
const memDimB = "group:8002"

// TestSaveSceneMemory_SameMemIDInTwoScenesDoNotClobber (本条最重要)
func TestSaveSceneMemory_SameMemIDInTwoScenesDoNotClobber(t *testing.T) {
	s := newTestStore(t)
	if err := EnsureSceneSchema(s); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.SaveSceneMemory(SceneMemory{
		MemID: "m1", SceneKey: memDimA, K: "meme", V: "A 群的梗", Label: "a",
	}); err != nil || !ok {
		t.Fatalf("A 群写入失败: ok=%v err=%v", ok, err)
	}
	if ok, err := s.SaveSceneMemory(SceneMemory{
		MemID: "m1", SceneKey: memDimB, K: "meme", V: "B 群的梗", Label: "b",
	}); err != nil || !ok {
		t.Fatalf("B 群写入失败: ok=%v err=%v", ok, err)
	}

	a, err := s.SceneMemories(memDimA, 8)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.SceneMemories(memDimB, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 1 || a[0].V != "A 群的梗" {
		t.Fatalf("**A 群的梗被 B 群顶掉了**: %+v", a)
	}
	if len(b) != 1 || b[0].V != "B 群的梗" {
		t.Fatalf("**B 群看不到自己的梗**: %+v", b)
	}
}

// TestSaveSceneMemory_SameSceneStillUpserts (反面: 修维度不许把"同场景更新"也弄坏)
//
// ⚠️ 同一个场景里同一个 mem_id **仍然必须是 upsert** ——
// 那是"这条梗又有人提了一次, 内容更新了"的正常路径。
func TestSaveSceneMemory_SameSceneStillUpserts(t *testing.T) {
	s := newTestStore(t)
	if err := EnsureSceneSchema(s); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveSceneMemory(SceneMemory{MemID: "m2", SceneKey: memDimA, K: "meme", V: "初版"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveSceneMemory(SceneMemory{MemID: "m2", SceneKey: memDimA, K: "meme", V: "改过一版"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.SceneMemories(memDimA, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("同场景同 mem_id 应当只有一行, 实得 %d", len(got))
	}
	if got[0].V != "改过一版" {
		t.Fatalf("同场景更新没生效: %q", got[0].V)
	}
}

// TestSavePersonMemory_SameMemIDForTwoPeople (关系层的同一个洞)
//
// ⚠️ 关系层更糟: 那个洞会让**一条记忆从 A 改判给 B**(`DO UPDATE SET uin=excluded.uin`)。
// §5.4 说「跨人转述是**绝对红线**, 优先级高于一切隐私开关」。
func TestSavePersonMemory_SameMemIDForTwoPeople(t *testing.T) {
	s := newTestStore(t)
	if err := EnsureSceneSchema(s); err != nil {
		t.Fatal(err)
	}
	mk := func(uin, v string) {
		if ok, err := s.SavePersonMemory(PersonMemory{
			MemID: "p1", UIN: uin, SceneKey: "group:8001", K: "fact", V: v,
		}); err != nil || !ok {
			t.Fatalf("写入失败 uin=%s: ok=%v err=%v", uin, ok, err)
		}
	}
	mk("10001", "甲的牙医")
	mk("10002", "乙的护士")

	a, err := s.PersonMemories("group:8001", "10001")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.PersonMemories("group:8001", "10002")
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 1 || a[0].V != "甲的牙医" {
		t.Fatalf("**甲的记忆被改判给别人了**: %+v", a)
	}
	if len(b) != 1 || b[0].V != "乙的护士" {
		t.Fatalf("乙看不到自己的记忆: %+v", b)
	}
}
