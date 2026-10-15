package convo

import (
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────
// §5.7 owner 的手动关系倍率 (设计稿 L1301)
//
// ⚠️ 它存在的理由是**一条真实的生产症状**: 三层接线都修好之后 ai_opens 仍然是 0 ——
// 因为私聊的 closeness 从 0 出发, 而"不向陌生人掏心窝子"把 cap 压在 20,
// 买不起一条 25 的主动消息。设计稿留的杠杆就是这个倍率。
// ─────────────────────────────────────────────────────────────

func ownerMultEngine(t *testing.T) (*Engine, *Store) {
	t.Helper()
	s := agTestStore(t)
	if err := EnsureOwnerMultSchema(s); err != nil {
		t.Fatal(err)
	}
	return &Engine{Store: s, Cfg: DefaultConfig()}, s
}

// TestOwnerMult_DefaultIsOne_NotZero
//
// ⚠️ **零值 = 未提供 = 1.0, 不是 0 倍。**
// 把"没配置"读成 0 倍, 等于一上线就把关系积累关掉 —— 而现象只是"它怎么忽然冷下来了"。
func TestOwnerMult_DefaultIsOne_NotZero(t *testing.T) {
	e, s := ownerMultEngine(t)
	if got := s.OwnerMult(e.ownerKey(), "999"); got != OwnerMultDefault {
		t.Fatalf("没设过应当是 %.1f, 实得 %.2f", OwnerMultDefault, got)
	}
	// 读库失败也往 1.0 倒(不是 0, 也不是 5)。
	if got := (&Store{}).OwnerMult("o", "999"); got != OwnerMultDefault {
		t.Fatalf("没有库时应当是 %.1f, 实得 %.2f", OwnerMultDefault, got)
	}
}

// TestOwnerMult_ClampedOnWrite (护栏 4 + 下限)
//
// ⚠️ **clamp 发生在写的那一刻**, 不是读的时候 ——
// 否则"管理页显示 8×"与"实际按 5× 算"会同时成立, 而那只有对着两处看才发现。
func TestOwnerMult_ClampedOnWrite(t *testing.T) {
	e, s := ownerMultEngine(t)
	owner := e.ownerKey()
	if got, err := s.SetOwnerMult(owner, "999", 8); err != nil || got != OwnerMultCap {
		t.Fatalf("8× 应当被夹到 %.1f, 实得 %.2f (err=%v)", OwnerMultCap, got, err)
	}
	// 存进去的就是夹过的数。
	if got := s.OwnerMult(owner, "999"); got != OwnerMultCap {
		t.Fatalf("读回来的应当是夹过的 %.1f, 实得 %.2f", OwnerMultCap, got)
	}
	// ⚠️ 下限也是 1.0: 倍率只用来**加快**, 不许拿来打压某个号。
	if got, _ := s.SetOwnerMult(owner, "888", 0.1); got != OwnerMultDefault {
		t.Fatalf("0.1× 应当被抬到 %.1f, 实得 %.2f", OwnerMultDefault, got)
	}
	if _, err := s.SetOwnerMult(owner, "  ", 2); err == nil {
		t.Fatal("空 uin 应当报错, 而不是写一行无主的倍率")
	}
}

// TestOwnerMult_SpeedsUpAccumulationButIsNotInstant (护栏 1)
//
// ⚠️ 「不改变即时一致性 —— 关系仍靠互动积累, 倍率只是让它更快;
// **不许出现"昨天还陌生, 今天突然很热"**」
func TestOwnerMult_SpeedsUpAccumulationButIsNotInstant(t *testing.T) {
	e, s := ownerMultEngine(t)
	owner := e.ownerKey()
	scene := DefaultSceneKey
	at := time.Date(2026, 10, 15, 2, 0, 0, 0, time.Local) // 深夜 → 同时命中"深夜还在聊"
	text := "这事我只跟你说, 你别告诉别人"

	// 1× 走 5 条。
	for i := 0; i < 5; i++ {
		e.NoteSceneBond(scene, text, at.Add(time.Duration(i)*time.Minute))
	}
	base, _ := LoadBond(s.DB, owner, scene)
	if base.Bond.Closeness <= 0 {
		t.Fatal("1× 时亲密度没涨 —— 前面那条接线坏了")
	}

	// 换成 5× 重来一遍 (换个场景键免得叠在上一份上)。
	if _, err := s.SetOwnerMult(owner, peerUINOf(SceneKeyPrivate("42")), 5); err != nil {
		t.Fatal(err)
	}
	s2 := SceneKeyPrivate("42")
	for i := 0; i < 5; i++ {
		e.NoteSceneBond(s2, text, at.Add(time.Duration(i)*time.Minute))
	}
	fast, _ := LoadBond(s.DB, owner, s2)
	if fast.Bond.Closeness <= base.Bond.Closeness {
		t.Fatalf("5× 没比 1× 快: %.3f vs %.3f", fast.Bond.Closeness, base.Bond.Closeness)
	}
	// ⚠️ 护栏 1「**不改变即时一致性**」的落点是: **一条消息不够**。
	//
	// 第一版我写的是"五条消息不该到 intimate" —— 那**比设计稿严**, 而且严错了地方:
	// 设计稿说的是"倍率只是让它更快", 没说"几条以内不许满"。
	// 用一段同时命中两个亲密信号的极端输入去卡条数, 卡住的其实是夹具。
	//
	// 真正被禁止的是"**忽然很热**" —— 那就该一条一条地测。
	one := SceneKeyPrivate("43")
	if _, err := s.SetOwnerMult(owner, peerUINOf(one), 5); err != nil {
		t.Fatal(err)
	}
	e.NoteSceneBond(one, text, at)
	first, _ := LoadBond(s.DB, owner, one)
	if first.Bond.Closeness >= BondIntimateAt {
		t.Fatalf("**一条**消息就推到了 %.3f —— 那才是「昨天还陌生今天很热」(护栏 1 禁止)", first.Bond.Closeness)
	}
	if first.Bond.Closeness <= 0 {
		t.Fatal("5× 下一条消息一点没涨 —— 那这个倍率是空转")
	}
	// ⚠️ 熟悉度**不乘倍率**: 它是"聊过多少"的计数性事实。
	if fast.Bond.Familiarity > base.Bond.Familiarity+0.001 {
		t.Fatalf("熟悉度被倍率放大了: %.4f vs %.4f —— 那会把「主人永远第一」变成「主人的群永远最熟」",
			fast.Bond.Familiarity, base.Bond.Familiarity)
	}
}

// TestOwnerMult_DoesNotEnterGroupScenes (护栏 5)
//
// ⚠️ 「§6.10.7 要求不许在群里暴露 owner 身份, 而"有 owner 的群它更话多"
// 是一个**可观测的统计差别** —— 那等于把关系用一个行为量公示出去」。
func TestOwnerMult_DoesNotEnterGroupScenes(t *testing.T) {
	e, s := ownerMultEngine(t)
	if _, err := s.SetOwnerMult(e.ownerKey(), "3487037837", 5); err != nil {
		t.Fatal(err)
	}
	// 群场景 → 恒 1.0, 哪怕这个 uin 的倍率是 5。
	if got := e.OwnerMultForScene("group:1"); got != OwnerMultDefault {
		t.Fatalf("倍率漏进了群场景: %.2f", got)
	}
	if got := e.OwnerMultForScene(SceneKeyPrivate("3487037837")); got != 5 {
		t.Fatalf("私聊场景没拿到倍率: %.2f", got)
	}

	// ⚠️ 而热力档位那一条也必须只在私聊生效。
	zero := Bond{Closeness: 0.2} // 本身是 acquaint, 乘 5 之后到 intimate
	if BondStageOf(zero) == BondStageWithMult(zero, 5) {
		t.Fatal("BondStageWithMult 根本没改阶段 —— 那作用 ② 就是空转")
	}
	if BondStageWithMult(zero, 1) != BondStageOf(zero) {
		t.Fatal("1× 时阶段应当与原来一致")
	}
}
