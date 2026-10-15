package convo

import (
	"context"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────
// §5.6 **护栏 5**: owner 倍率**不进群**
//
// 设计稿的理由值得逐字抄:
//
//	> §6.10.7 要求不许在群里暴露 owner 身份, 而"**有 owner 的群它更话多**"
//	> 是一个**可观测的统计差别** —— 那等于把关系用一个行为量公示出去。
//
// ⚠️ 所以最强的不变量**不是**"倍率函数在群里返回 1.0"
// (那是实现, 不是性质), 而是:
//
//	**把倍率从 1 调到 5, 群场景那段提示词必须逐字节相同。**
//
// ⚠️ 因为群友能观测到的正是**那段文字产生的行为** ——
// 提示词相同 ⇒ 行为分布相同 ⇒ 公示不了任何东西。
// ─────────────────────────────────────────────────────────────

// TestOwnerMult_GroupPromptIsInvariant (本条最重要)
func TestOwnerMult_GroupPromptIsInvariant(t *testing.T) {
	// ⚠️ **两个独立的库** —— 这是这个夹具的关键。
	//
	// 第一版我在**同一个库**上跑了两遍, 于是第二遍的历史里多了第一遍那句回话,
	// 而 `AllShort(history)` 因此变了 ⇒ 提示词当然不同。
	// **那是我夹具的错, 不是泄漏** —— 差一点就报了一个假泄漏。
	//
	// 现在: 两个库的场景/成员/关系量逐字节相同, 唯一差别只剩倍率。
	type run struct {
		sys   string
		stage string
	}
	do := func(mult float64) run {
		e, cap, scene := crossPersonEngine(t)
		if err := SaveBond(e.Store.DB, e.ownerKey(), scene, Bond{
			Closeness: 0.4, Regard: 0.3, Familiarity: 10,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Store.SetOwnerMult(e.ownerKey(), xbUIN, mult); err != nil {
			t.Fatal(err)
		}
		if err := e.SpeakInGroupSeeing(context.Background(), scene, 9901, xaUIN, "你们在聊什么", ""); err != nil {
			t.Fatal(err)
		}
		return run{sys: cap.lastSystem, stage: e.heatStageOf(scene)}
	}
	one := do(OwnerMultDefault)
	five := do(OwnerMultCap)

	// ⚠️ 空转自检: 提示词得真的造出来了。
	if len(one.sys) < 500 || len(five.sys) < 500 {
		t.Fatalf("提示词太短(%d / %d) —— 夹具没造起来, 比较是空转", len(one.sys), len(five.sys))
	}
	if one.sys != five.sys {
		t.Errorf("**倍率改变了群提示词** —— 那是一个可观测的差别(护栏 5): mult=1 长 %d / mult=5 长 %d",
			len(one.sys), len(five.sys))
		diffPromptLines(t, one.sys, five.sys)
	}
	if one.stage != five.stage {
		t.Errorf("**倍率改变了群的阶段**(%s → %s) —— 它会连带改变热力上限与群里的说话档",
			one.stage, five.stage)
	}
}

// TestOwnerMult_PrivateIsAffected (对照, 与上一条成对)
//
// ⚠️ **没有这一条, 上面那条可能是空的**:
// 如果倍率整个坏掉(比如读取恒返 1.0), "群里不变"**依然成立** ——
// 而它什么都没守住。所以必须证明**倍率在私聊里真的起作用**。
func TestOwnerMult_PrivateIsAffected(t *testing.T) {
	e, _, _ := crossPersonEngine(t)
	priv := SceneKeyPrivate(xbUIN)
	// 关系量卡在"认识"的门槛附近 —— 倍率翻五倍应当把它抬上去。
	if err := SaveBond(e.Store.DB, e.ownerKey(), priv, Bond{
		Closeness: BondAcquaintAt + 0.01, Regard: 0.2, Familiarity: 5,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.SetOwnerMult(e.ownerKey(), xbUIN, OwnerMultDefault); err != nil {
		t.Fatal(err)
	}
	low := e.heatStageOf(priv)
	if _, err := e.Store.SetOwnerMult(e.ownerKey(), xbUIN, OwnerMultCap); err != nil {
		t.Fatal(err)
	}
	high := e.heatStageOf(priv)
	if low == high {
		t.Fatalf("**倍率在私聊里没起作用**(两档都是 %s) —— 那「群里不变」就没证明任何东西", low)
	}
}

// diffPromptLines 把两段提示词按行比对, 打出不同的那些行。
func diffPromptLines(t *testing.T, a, b string) {
	t.Helper()
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	n := len(al)
	if len(bl) < n {
		n = len(bl)
	}
	shown := 0
	for i := 0; i < n && shown < 6; i++ {
		if al[i] != bl[i] {
			t.Logf("行 %d 不同 | mult=1: %s | mult=5: %s", i+1, al[i], bl[i])
			shown++
		}
	}
	if len(al) != len(bl) {
		t.Logf("行数不同: %d vs %d", len(al), len(bl))
	}
}
