package convo

import (
	"strings"
	"time"

	"github.com/tzhnahida/VocabLock/internal/log"
)

// ─────────────────────────────────────────────────────────────
// §5.1 **关系量的接线** (2026-10-15)
//
// 它是「它再也不主动开口」那条链的**最后一环**, 也是最深的一环:
//
//   ① §6.7 热力通道没接线        → 没有热力行
//   ② heatNow 返回 (0,true)       → HeatQuota(0) = 0
//   ③ **即使接上线也一样**: `closeness` 恒为 0 → 永远判 stranger
//      → stranger 的热力 cap 是 20, 而每条主动消息的代价是 25
//      → `floor(20/25) = 0` → **主动配额还是 0**
//
// 而 ③ 的根因是: `BondSignal` 的四个**正向**字段
// (说了私事 / 向它请教 / 道谢 / 深夜还在聊)
// **全库没有一个生产者** —— `BondDelta` 里那四个加分分支是死代码,
// 于是 `closeness` 从 0 出发、只可能被 `Rebuffed` 扣下去。
//
// ⚠️ 关系量涨不动**不是"涨得慢"**: 它让一条**硬闸门**永远关着。
// 而那个闸门关着的表现, 只是**它比从前安静**。
// ─────────────────────────────────────────────────────────────

// bondThanksWords 道谢。
var bondThanksWords = []string{"谢谢", "多谢", "感谢", "谢啦", "谢了", "thx", "3q", "thank"}

// bondAdviceWords 请教 —— 问的是**它的看法**, 不是问事实。
var bondAdviceWords = []string{"你觉得", "你说呢", "怎么办", "该不该", "要不要", "请教", "帮我看看", "给点建议", "有什么建议"}

// bondSecretWords 说了私事 —— **只能靠明说的那几种措辞**。
//
// ⚠️ 刻意**不做内容推断**: "他今天没来上班" 是不是私事, 取决于说话人怎么想,
// 而猜错的代价是让关系量被无关的话推高 —— 那比涨得慢糟得多。
var bondSecretWords = []string{"只跟你说", "别告诉别人", "不要告诉别人", "我跟你说个事", "说个秘密", "心里话", "没人知道"}

// BondSignalsFrom 从一条消息里读出正向关系信号 (§5.1)。
//
// ⚠️ 全是**关键词**判据, 不接 LLM: 关系量是每轮都要算的,
// 而一次调用换 0.04 的亲密度是荒谬的性价比。
func BondSignalsFrom(text string, at time.Time) BondSignal {
	var sig BondSignal
	t := strings.ToLower(strings.TrimSpace(text))
	if t == "" {
		return sig
	}
	sig.Thanked = containsAny(t, bondThanksWords)
	sig.AskedAdvice = containsAny(t, bondAdviceWords)
	sig.SharedSecret = containsAny(t, bondSecretWords)
	// 深夜还在聊 = 凌晨 0-5 点**还说了话** (它是时间信号, 不是文本信号)。
	//
	// ⚠️ **`at` 为零值时必须判否。**
	//
	// 第一版写的是 `at.Hour() >= 0 && at.Hour() < 5` —— 而零值 `time.Time` 的 Hour() 就是 0,
	// 于是**每一条没带时刻的消息都被读成"凌晨还在聊"**, 亲密度 +0.03。
	//
	// 它不是"多涨一点": 零值 = **未提供**, 而"凌晨两点"是一个**极值**。
	// 实测量到两处连带故障 —— 一个群在第一条消息之后就带上了偏向,
	// 另一处三态计数直接错了一位。
	sig.LateNightTalk = !at.IsZero() && at.Hour() < 5
	return sig
}

// NoteSceneBond 更新一个场景的关系量 (§5.1)。
//
// ⚠️ **群与私聊都必须调它。** 私聊那条路原来是**完全没有**关系量生产者的 ——
// 而主动开口正是发在私聊场景上, 用的是 `heatStageOf(DefaultSceneKey)`。
func (e *Engine) NoteSceneBond(sceneKey, text string, at time.Time) {
	if e.Store == nil || e.Store.DB == nil || strings.TrimSpace(sceneKey) == "" {
		return
	}
	br, err := LoadBond(e.Store.DB, e.ownerKey(), sceneKey)
	if err != nil {
		return
	}
	sig := BondSignalsFrom(text, at)
	sig.Msgs = 1 // 熟悉度**每条都涨** (与好坏无关)
	// §5.7: owner 的关系倍率按**场景**取 —— 群场景恒 1.0(护栏 5)。
	b := ApplyBondScaled(br.Bond, sig, e.OwnerMultForScene(sceneKey))
	if b == br.Bond {
		return // 什么都没变, 不写库
	}
	if err := SaveBond(e.Store.DB, e.ownerKey(), sceneKey, b); err != nil {
		log.Warn("convo:value", "关系量落库失败(亲密度会涨不动, 而它挡着主动配额): %v", err)
	}
}
