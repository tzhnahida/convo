package convo

import (
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────
// §5.6 **场景阶段的写入侧** —— `convo_scene.stage`
//
// ⚠️ 它补的是"消费者接上了、生产者不存在"那一类:
// `SelfRoleLine`(群里"能不能怼")与 `conflictwire` 的 A1("陌生 → 不掺和")
// 都在读那个字段, 而在它之前**没有任何东西升级过它** ——
// 于是无论私聊多熟, 它在群里**永远按"陌生"那一档说话**。
//
// ⚠️ 而**棘轮看不见这一类**: `SelfRoleLine` 有调用者 ✓, 缺的是写那个字段的人。
// ─────────────────────────────────────────────────────────────

func stageEngine(t *testing.T) *Engine {
	t.Helper()
	e := gateEngine(t)
	if err := EnsureSceneSchema(e.Store); err != nil {
		t.Fatal(err)
	}
	if err := EnsureBondSchema(e.Store.DB); err != nil {
		t.Fatal(err)
	}
	return e
}

// sceneStage 读回场景行里的 stage。
func sceneStage(t *testing.T, e *Engine, scene string) string {
	t.Helper()
	var s string
	if err := e.Store.DB.QueryRow("SELECT COALESCE(stage,'') FROM convo_scene WHERE scene_key=?",
		scene).Scan(&s); err != nil {
		t.Fatalf("场景行读不到: %v", err)
	}
	return s
}

// TestSceneStage_AcquaintSpelling (⚠️ 本条钉的是一个**拼法陷阱**)
//
// ⚠️ `bond.go` 写的是 `"acquaint"`, 而 `scene.go` 写的是 `"acquaintance"` ——
// **同一个概念两种拼法**。直接把 `BondStage` 强转成 string 写进场景行,
// 会得到一个**谁也认不出来的值**: 读者的 switch 静默落进 `default`,
// 而 default 的表现**恰好和"陌生"一样** ⇒ 那个 bug 看起来"一切正常"。
func TestSceneStage_AcquaintSpelling(t *testing.T) {
	if got := SceneStageFromBond(BondStageAcquaint); got != StageAcquaint {
		t.Fatalf("acquaint 档映射成了 %q, 而读者认的是 %q", got, StageAcquaint)
	}
	if SceneStageFromBond(BondStageAcquaint) == string(BondStageAcquaint) {
		t.Fatal("**映射退化成了强转** —— 那正是这个陷阱")
	}
	// 另外三档也要对得上。
	for want, in := range map[string]BondStage{
		StageStranger: BondStageStranger,
		StageFamiliar: BondStageFamiliar,
		StageIntimate: BondStageIntimate,
	} {
		if got := SceneStageFromBond(in); got != want {
			t.Fatalf("%s → %q, 期望 %q", in, got, want)
		}
	}
}

// TestSceneStage_ReachesSelfRoleLine (端到端, 本条最有价值)
//
// ⚠️ 光把字段写上不算数 —— **读它的那个人必须因此改变行为**。
// `SelfRoleLine` 是群里"能不能怼"的判据, 设计稿说它「比人设厚度重要」。
func TestSceneStage_ReachesSelfRoleLine(t *testing.T) {
	e := stageEngine(t)
	scene := "group:8801"
	if err := e.Store.EnsureScene(scene); err != nil {
		t.Fatal(err)
	}
	// 插入时是陌生 —— 先钉住基线(否则下面那句"变了"没有对照)。
	if got := sceneStage(t, e, scene); got != StageStranger {
		t.Fatalf("新场景应当是 stranger, 实得 %q", got)
	}
	// 把关系量抬到"熟悉"。
	if err := SaveBond(e.Store.DB, e.ownerKey(), scene, Bond{
		// ⚠️ 熟悉度种 5 (= 5000 条消息) 会让互动量那一半把它顶到亲密档,
		// 而这条用例要测的是**亲密度**那半边。两种来源各有自己的用例, 别互相盖住。
		Closeness: BondFamiliarAt + 0.05, Regard: 0.5, Familiarity: 0.1,
	}); err != nil {
		t.Fatal(err)
	}
	rec, err := LoadBond(e.Store.DB, e.ownerKey(), scene)
	if err != nil {
		t.Fatal(err)
	}
	e.NoteSceneStage(scene, rec)

	if got := sceneStage(t, e, scene); got != StageFamiliar {
		t.Fatalf("**关系变熟了, 场景阶段却没跟上**: %q", got)
	}
	// ⚠️ **而读它的那条路必须因此改口** —— 这才是这条接线的意义。
	sc, err := e.Store.LoadScene(scene)
	if err != nil || sc == nil {
		t.Fatalf("场景读不回: %v", err)
	}
	line := SelfRoleLine(sc, DefaultConfig().Group)
	if !strings.Contains(line, "熟悉") {
		t.Fatalf("`SelfRoleLine` 没有跟着变 —— 那条接线是空的:\n%s", line)
	}
}

// TestSceneStage_NoBondLeavesItAlone (反面)
//
// ⚠️ **还没有关系量 → 什么都不做**, 不是"降级成陌生"。
func TestSceneStage_NoBondLeavesItAlone(t *testing.T) {
	e := stageEngine(t)
	scene := "group:8802"
	if err := e.Store.EnsureScene(scene); err != nil {
		t.Fatal(err)
	}
	// 手工把它抬成亲密, 然后拿一条**没有关系量**的记录去调 —— 不许被改回去。
	if _, err := e.Store.DB.Exec("UPDATE convo_scene SET stage=? WHERE scene_key=?",
		StageIntimate, scene); err != nil {
		t.Fatal(err)
	}
	e.NoteSceneStage(scene, BondRecord{Loaded: false})
	if got := sceneStage(t, e, scene); got != StageIntimate {
		t.Fatalf("**没有关系量却把它改了**: %q —— 失败必须往「不动」倒", got)
	}
}

// TestSceneStage_DoesNotWriteWhenUnchanged (反面)
//
// ⚠️ 每一轮都 UPDATE 会让 `updated_ts` 失去意义 —— 而它是"这个场景最后动过"的读数。
func TestSceneStage_DoesNotWriteWhenUnchanged(t *testing.T) {
	e := stageEngine(t)
	scene := "group:8803"
	if err := e.Store.EnsureScene(scene); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.DB.Exec("UPDATE convo_scene SET updated_ts=? WHERE scene_key=?",
		"2026-01-01 00:00:00", scene); err != nil {
		t.Fatal(err)
	}
	// 关系量是空的(Loaded=false) → 一行都不该动。
	e.NoteSceneStage(scene, BondRecord{Loaded: false})
	var ts string
	if err := e.Store.DB.QueryRow("SELECT updated_ts FROM convo_scene WHERE scene_key=?",
		scene).Scan(&ts); err != nil {
		t.Fatal(err)
	}
	if ts != "2026-01-01 00:00:00" {
		t.Fatalf("没变却写了库(updated_ts 被改动): %q", ts)
	}
}
