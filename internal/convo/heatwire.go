package convo

import (
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// §6.7 **热力通道的接线** (2026-10-15)
//
// 热力这一节代码早就写完了、测试也全绿 —— 而 `RecordHeatInteraction` / `SpendHeat`
// 的**生产调用者一直是零**。后果不是"少一个功能":
//
//   `heatNow` 在**没有热力行**时返回 `(0, true)`(「新场景 → 热力 0 → 它一开始就少说话」),
//   而 `HeatQuota(h=0)` 返回 **0**, 于是 `in.ActiveToday < quota` 永远为假 ——
//   **它再也不会主动开口。**
//
// ⚠️ 生产实测坐实: `ai_opens` 在 10-12 / 10-13 各 2 次, **10-14 起彻底消失**。
// 而它的表现只是"它变安静了"—— 一个**看起来像性格**的故障。
//
// ⚠️ 这条通道分两半, 缺一不可:
//   · **入账** (互动 → 热力涨)  ← 原来两半都缺, 所以热力行根本不存在;
//   · **扣费** (主动发起 → 25/条) ← §6.7「每条主动消息 = 25 热力」。
// 只接扣费会让它立刻哑掉; 只接入账会让它**越聊越话多**, 没有代价。
//
// ⚠️ 而**回话不走扣费** (§4.4): 被 @ 必答是硬门槛。若回话也扣,
// 忙群会因为递减退化永远攒不出 25 → 大群必然变哑巴。
// ─────────────────────────────────────────────────────────────

// heatStageOf 场景阶段 → 热力上限档 (§5.6: cap 取场景级)。
//
// ⚠️ 读不到关系量 → **stranger** (最小的那个 cap): 失败往"少说"倒,
// 不往"放开"倒 —— 与 `heatNow` 的两条路分开同一条纪律。
func (e *Engine) heatStageOf(sceneKey string) string {
	if e.Store == nil || e.Store.DB == nil {
		return HeatStageStranger
	}
	rec, err := LoadBond(e.Store.DB, e.ownerKey(), sceneKey)
	if err != nil {
		return HeatStageStranger
	}
	// §5.7 作用 ②: **热力上限按倍率后的阶段算**, 而倍率只在私聊生效(护栏 5)。
	//
	// ⚠️ 这里只改**判阶段用的那个数**, 不改关系量本身 ——
	// "把亲密度乘上去存起来"会让"昨天还陌生今天很热"变成真的(护栏 1 禁止的正是那个)。
	switch BondStageWithMult(rec.Bond, e.OwnerMultForScene(sceneKey)) {
	case BondStageIntimate:
		return HeatStageIntimate
	case BondStageFamiliar:
		return HeatStageFamiliar
	default:
		// Acquaint 与 Stranger 共用最小 cap —— 内置档位表只有三档。
		return HeatStageStranger
	}
}

// heatMods 本次互动的倍率 (§4.1): 全是有则有、无则不加分的开关。
//
// ⚠️ 三个开关**默认全关** —— 它们都是"加成", 关着等于不加, 是安全的那一侧。
func (e *Engine) heatMods(now time.Time) HeatMods {
	var m HeatMods
	if e.Store == nil {
		return m
	}
	// 心情低 → 减半。与失眠那条同一口径 (amp < 0.3)。
	if e.Store.MoodAmpOf(clock.Today()) < 0.3 {
		m.MoodLow = true
	}
	// 身体不适 → 减半。读不到就是"没病"(失败往正常说话倒)。
	if b, err := e.Store.LoadBody(); err == nil {
		b.Refresh(now)
		m.BodyUnwell = b.Active
	}
	return m
}

// NoteSceneHeat **入账一次场景互动** —— 场景里**任何**一条消息都算。
//
// ⚠️ 这是"热力会涨"的唯一来源。不接它, 热力恒为 0, 而后果是**它不再主动开口**。
func (e *Engine) NoteSceneHeat(sceneKey, speaker string, now time.Time) {
	if e.Store == nil || strings.TrimSpace(sceneKey) == "" {
		return
	}
	slot := ""
	if ws, err := e.Store.LoadSchedule(); err == nil {
		slot = string(CurrentState(ws, now))
	}
	if _, err := e.Store.RecordHeatInteraction(HeatInteraction{
		SceneKey:  sceneKey,
		Speaker:   speaker,
		Stage:     e.heatStageOf(sceneKey),
		SlotState: slot,
		Mods:      e.heatMods(now),
		Now:       now,
	}, e.heatCfg()); err != nil {
		// ⚠️ **必须出声**: 入账失败的表现是"它越来越不爱主动说话",
		// 而那看起来像"它变安静了"—— 一个像性格的故障。
		log.Warn("convo:heat", "热力入账失败(主动配额会被压到 0): %v", err)
	}
}

// SpendSceneHeat 一次**主动发起**的扣费 (§6.7: 每条主动消息 = 25 热力)。
//
// ⚠️ **回话不走这里** —— 被 @ 必答是硬门槛, 扣了它大群必然变哑巴。
func (e *Engine) SpendSceneHeat(sceneKey string, now time.Time) {
	if e.Store == nil || strings.TrimSpace(sceneKey) == "" {
		return
	}
	if _, err := e.Store.SpendHeat(sceneKey, now, e.heatStageOf(sceneKey), e.heatCfg()); err != nil {
		log.Warn("convo:heat", "热力扣费失败(它会更频繁地开口): %v", err)
	}
}
