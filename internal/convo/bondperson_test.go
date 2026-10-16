package convo

import "testing"

// TestBondStageShared_VolumeMovesTheStage
//
// ⚠️ owner 定规 (2026-10-16): 「互动量按 uin 通用」——
// 群里聊了几百句的人, 不该因为「没分享过私事」而被判成**陌生**。
func TestBondStageShared_VolumeMovesTheStage(t *testing.T) {
	// 不带倍率 = mult 1.0 (倍率那一半与互动量无关, 见 BondStageSharedWithMult)。
	shared := func(b Bond) BondStage { return BondStageSharedWithMult(b, 1.0) }
	cases := []struct {
		name string
		b    Bond
		want BondStage
	}{
		{"量小 → 陌生", Bond{Familiarity: 0.10}, BondStageStranger},
		{"量到熟人线", Bond{Familiarity: 0.20}, BondStageAcquaint},
		{"量到熟人", Bond{Familiarity: 0.45}, BondStageFamiliar},
		{"量到亲密", Bond{Familiarity: 0.80}, BondStageIntimate},
		{"质更高时取质", Bond{Closeness: 0.75, Familiarity: 0.20}, BondStageIntimate},
		{"量更高时取量", Bond{Closeness: 0.16, Familiarity: 0.45}, BondStageFamiliar},
		{"零值仍是陌生", Bond{}, BondStageStranger},
	}
	for _, c := range cases {
		if got := shared(c.b); got != c.want {
			t.Errorf("%s: BondStageShared(%+v) = %q, 想要 %q", c.name, c.b, got, c.want)
		}
	}
	// 反面: 阈值是**共用的那一组** —— 换来源不许换尺子。
	if shared(Bond{Familiarity: BondAcquaintAt}) != BondStageAcquaint {
		t.Fatal("熟悉度刚好到熟人阈值却不是熟人 —— 两把尺子分叉了")
	}
}

// TestPersonUINOfScene_GroupHasNoPerson
//
// ⚠️ 方向规则③的**入口**: 群场景没有单一个人。
// 返回空串 = 调用方什么都写不了, 于是「把群的关系算到某个人头上」在类型上就不可能发生。
func TestPersonUINOfScene_GroupHasNoPerson(t *testing.T) {
	if got := PersonUINOfScene("private:2735604537"); got != "2735604537" {
		t.Fatalf("私聊应当给出对面那个人, 实得 %q", got)
	}
	for _, key := range []string{"group:1034712035", "", "weird:key", "person:123"} {
		if got := PersonUINOfScene(key); got != "" {
			t.Fatalf("%q 不是私聊, 不该给出一个人 (实得 %q) —— 那会把群的关系算到某人头上", key, got)
		}
	}
}

// TestBackfillBondPerson_PrivateOnly
//
// ⚠️ **群那一行不能回填**: 它是这个群的总量, 没有 uin 维度 ——
// 从群回填等于把这个群所有成员的亲密度算到某一个人头上。
func TestBackfillBondPerson_PrivateOnly(t *testing.T) {
	s := newTestStore(t)
	if err := EnsureBondSchema(s.DB); err != nil {
		t.Fatal(err)
	}
	if err := EnsureBondPersonSchema(s.DB); err != nil {
		t.Fatal(err)
	}
	owner := s.OwnerKey()
	if err := SaveBond(s.DB, owner, SceneKeyPrivate("2735604537"),
		Bond{Closeness: 0.37, Regard: 0.2, Familiarity: 0.9}); err != nil {
		t.Fatal(err)
	}
	if err := SaveBond(s.DB, owner, "group:1034712035",
		Bond{Closeness: 0.99, Regard: 0.99, Familiarity: 0.99}); err != nil {
		t.Fatal(err)
	}

	n, err := BackfillBondPerson(s.DB, owner)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应当只回填一个人, 实得 %d", n)
	}
	rec, err := LoadBondPerson(s.DB, owner, "2735604537")
	if err != nil || !rec.Loaded {
		t.Fatalf("回填没落库: %+v err=%v", rec, err)
	}
	if rec.Closeness != 0.37 || rec.Familiarity != 0.9 {
		t.Fatalf("回填取错了来源 (是不是把群的数拿来了?): %+v", rec.Bond)
	}
	// ⚠️ 群号不是人 —— 不许为它建人物层记录。
	if g, err := LoadBondPerson(s.DB, owner, "1034712035"); err != nil || g.Loaded {
		t.Fatalf("群号被当成了一个人: %+v err=%v", g, err)
	}
	// 幂等: 再跑一遍不重复写。
	if n2, err := BackfillBondPerson(s.DB, owner); err != nil || n2 != 0 {
		t.Fatalf("回填不幂等: n=%d err=%v", n2, err)
	}
}
