package convo

import (
	"strings"
	"testing"

	"github.com/tzhnahida/convo/internal/clock"
)

// ─────────────────────────────────────────────────────────────
// §6.1「故障与判不出来必须分得开」—— **冲突那一侧**
//
// ⚠️ 与 §2.45(`ResolveAddressee`) 是**同一个洞的第二个出口**:
//
//   · `conflictContextOf` 里 `LoadScene` 的错误被**静默吞掉**
//     (「读不到的场景信息一律留空」);
//   · 于是 `ctx.Stage` 留空 → A1「阶段=陌生 → 不掺和」**不触发** →
//     一次读库失败把它推向**更容易介入**的那一侧;
//   · 而故障与"未提供"在日志里**长得一模一样**。
//
// ⚠️ 而 `ResolveConflict` 早就写好了那条分支, 注释里还写着:
//
//   > `ctxErr != nil`(读库失败) → ConflictNormal
//   > `ctx.Undecidable` → ConflictSilent
//   > **把两者并成一条, 就是 R28 写错的样子。**
//
// 也就是说: 分派那一侧**又**是早就写好的 —— 缺的只是把错误传进去。
// ─────────────────────────────────────────────────────────────

// conflictScene 造一个已经开过口的群场景(带冲突素材)。
func conflictFaultEngine(t *testing.T) (*Engine, string) {
	t.Helper()
	e := gateEngine(t)
	scene := "group:5001"
	if err := EnsureSceneSchema(e.Store); err != nil {
		t.Fatal(err)
	}
	// 场景登记好 —— 这样"读失败"测的是**失败**, 不是"未登记"。
	if err := e.Store.EnsureScene(scene); err != nil {
		t.Fatal(err)
	}
	// 攒够冲突素材(两条互喷), 否则 `conflictWindowOf` 直接 ok=false 走不到这里。
	for i := 0; i < 4; i++ {
		who := "111"
		if i%2 == 1 {
			who = "222"
		}
		if _, err := e.Store.InsertMessageFull("user", "qq", "text", "你有病吧", MsgMeta{
			Origin: OriginUser, SceneKey: scene, SenderUIN: who,
			AtUINs: []string{"3487037837"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	return e, scene
}

// TestConflictFault_ReadFailureGoesNormal (本条最重要)
//
// ⚠️ **故障必须走"正常说话"** —— 既不沉默, 也不介入。
//
// 做法是**真把库关掉**(不是打桩): 那样测到的是"读库失败"这条路,
// 而不是"我以为读库会失败"那条路。
func TestConflictFault_ReadFailureGoesNormal(t *testing.T) {
	e, scene := conflictFaultEngine(t)
	ctx, err := e.conflictContextOf(scene, nil, e.Cfg.Channel.SelfQQ)
	if err != nil {
		t.Fatalf("夹具坏了: 库还好的时候不该报错: %v", err)
	}
	_ = ctx

	// 关库 → 下一次读场景必然失败。
	if cerr := e.Store.DB.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	_, ferr := e.conflictContextOf(scene, nil, e.Cfg.Channel.SelfQQ)
	if ferr == nil {
		t.Fatal("**读库失败没有被报出来** —— 那它就会被「未提供」吞掉, 而这正是 R28 写错的样子")
	}

	// ⚠️ 而给 `ResolveConflict` 一个 ctxErr → 它必须判 ConflictNormal。
	dec := ResolveConflict(nil, ConflictBaseline{}, ConflictContext{}, ferr, ConflictPolicy{})
	if dec.Verdict != ConflictNormal {
		t.Fatalf("**故障没有走正常说话**: %s (%s)", dec.Verdict, dec.Why)
	}
	if !strings.Contains(dec.Why, "故障") {
		t.Fatalf("理由里没说清这是故障: %q", dec.Why)
	}
}

// TestConflictFault_MissingSceneIsNotAFault (反面, 与上一条成对)
//
// ⚠️ **"没有那一行"与"读不到"是两件事** ——
// 前者是"未提供"(照常走判据), 后者是故障(往说话倒)。
// 把两者并成一条就是 R28 写错的样子。
func TestConflictFault_MissingSceneIsNotAFault(t *testing.T) {
	e := gateEngine(t)
	if err := EnsureSceneSchema(e.Store); err != nil {
		t.Fatal(err)
	}
	// 一个**没登记过**的场景: 读得到结果, 只是没有那一行。
	ctx, err := e.conflictContextOf("group:4040404", nil, e.Cfg.Channel.SelfQQ)
	if err != nil {
		t.Fatalf("**没那一行被当成了故障**: %v —— 两者必须分得开", err)
	}
	if ctx.Stage != "" {
		t.Fatalf("没登记的阶段应当是空(未提供), 实得 %q", ctx.Stage)
	}
}

// TestConflictFault_StanceIsUntouchedOnFault
//
// ⚠️ 端到端那一面: 读库失败时, §6.9 **不许改动**裁决(不沉默也不介入)。
func TestConflictFault_StanceIsUntouchedOnFault(t *testing.T) {
	e, scene := conflictFaultEngine(t)
	if cerr := e.Store.DB.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	res := GroupGateResult{Stance: StanceSilent, Reason: "进来之前的样子"}
	e.applyConflictGate(SceneMsg{SceneKey: scene, UIN: "111", Text: "你有病吧", TS: clock.Now()}, &res)
	if res.Stance != StanceSilent || res.Reason != "进来之前的样子" {
		t.Fatalf("**故障时 §6.9 改动了裁决**: %s / %q", res.Stance, res.Reason)
	}
}
