package convo

import "testing"

// ─────────────────────────────────────────────────────────────
// 教练机 **撤销规则** 的出口
//
// ⚠️ `ClearCoachRules`(「owner 想重新开始教」)**一个调用者都没有** ✗ ——
// 而教练机本身是接了的 ✓(「教练: <规则>」→ `AddCoachRule` → 进提示词 ✓)。
//
// ⇒ 教错一条规则**没法撤销** ✗ —— 与 §2.58(插话收敛的安全阀)同一个形状 ✓。
// ─────────────────────────────────────────────────────────────

// TestCoachClear_DeactivatesAllRules
//
// ⚠️ 两条不变量:
//
//	① 停用之后 `CoachRules` **取不到**它了(它不再进提示词 ✓);
//	② 而**行还在**(是"停用"不是"删除" ✓ —— 人能回头查"它以前被教过什么")。
func TestCoachClear_DeactivatesAllRules(t *testing.T) {
	e := gateEngine(t)
	if err := EnsureCoachSchema(e.Store.DB); err != nil {
		t.Fatal(err)
	}
	owner := e.ownerKey()
	for _, r := range []string{"别用感叹号", "别叫用户主人"} {
		if _, err := e.Store.AddCoachRule(owner, r, ""); err != nil {
			t.Fatal(err)
		}
	}
	// 先钉基线: 教过的规则**取得到**。
	if got, _ := e.Store.CoachRules(owner, DefaultSceneKey); len(got) != 2 {
		t.Fatalf("夹具坏了: 应当有 2 条生效规则, 实得 %d", len(got))
	}

	n, err := e.Store.ClearCoachRules(owner)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("应当停用 2 条, 实得 %d", n)
	}
	if got, _ := e.Store.CoachRules(owner, DefaultSceneKey); len(got) != 0 {
		t.Fatalf("**停用之后规则还在生效**(%d 条) —— 那它还会进提示词", len(got))
	}

	// ⚠️ 而**行必须还在** —— 它是"停用"不是"删除"。
	var rows int
	if err := e.Store.DB.QueryRow("SELECT COUNT(*) FROM convo_coach WHERE owner=?", owner).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("**行被删掉了**(剩 %d 行) —— 那历史就没了; 它是停用不是删除", rows)
	}
}
