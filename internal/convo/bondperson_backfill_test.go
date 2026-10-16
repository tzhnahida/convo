package convo

import "testing"

// TestBackfillBondPerson_DoesNotInventTheLiteralOwner
//
// ⚠️ 默认私聊的场景键是**字面量** private:owner, 它不含真实 uin。
// 拿它当人物层的键, 群里的那个号与私聊会变成两笔账 ——
// 而两笔账正是这一层要消灭的东西。
//
// 这不是假想: 2026-10-16 在仿真库上实测到它写出了 uin=owner 那一行。
// 所以「拿不到真实 uin」的答案不是猜一个, 而是**跳过**。
func TestBackfillBondPerson_DoesNotInventTheLiteralOwner(t *testing.T) {
	s := newTestStore(t)
	if err := EnsureBondSchema(s.DB); err != nil {
		t.Fatal(err)
	}
	if err := EnsureBondPersonSchema(s.DB); err != nil {
		t.Fatal(err)
	}
	owner := s.OwnerKey()
	if err := SaveBond(s.DB, owner, DefaultSceneKey, Bond{Closeness: 0.5, Familiarity: 0.3}); err != nil {
		t.Fatal(err)
	}
	// ① 没有真实 uin -> 跳过: 不写任何人物层记录。
	if n, err := BackfillBondPerson(s.DB, owner, ""); err != nil || n != 0 {
		t.Fatalf("没有真实 uin 时不该回填: n=%d err=%v", n, err)
	}
	if rec, err := LoadBondPerson(s.DB, owner, "owner"); err != nil || rec.Loaded {
		t.Fatal("凭空造了一个叫 owner 的人 —— 那会与真实 uin 那笔账永远分家")
	}
	// ② 给了真实 uin -> 按它建键, 历史不丢。
	if n, err := BackfillBondPerson(s.DB, owner, "2735604537"); err != nil || n != 1 {
		t.Fatalf("带真实 uin 时应当回填 1 人: n=%d err=%v", n, err)
	}
	rec, err := LoadBondPerson(s.DB, owner, "2735604537")
	if err != nil || !rec.Loaded {
		t.Fatalf("真实 uin 那一行没建出来: %+v err=%v", rec, err)
	}
	if rec.Closeness != 0.5 || rec.Familiarity != 0.3 {
		t.Fatalf("历史没搬过来: %+v", rec.Bond)
	}
}
