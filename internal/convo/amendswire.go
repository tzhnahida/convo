package convo

import (
	"strings"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// §5.5 **找补** (M10) 的写入侧 (2026-10-16 接线)
//
// 设计稿 L1182: 「**连续两次毁约后必须有一次补偿行为**」;
// L1180 ③「短期失去"主动约定权"」; 而 `agency.go` 写着:
//
//   > **恢复条件也是硬的: 找补一次 (MarkAmends) 或过了记仇期。**
//
// ⚠️ 而 `MarkAmends` **从来没有调用者** —— 于是:
//
//   · `MayCommit` ← `CompensationOwed` ← 最近那次毁约的 `amends_ts == ""`;
//   · `amends_ts` 全库无写入者 → 连续两次毁约之后**只能等 7 天记仇期**自动解除。
//
// ⚠️ 而**提示词那一侧早就在要求它找补**了 ——
// 于是这条链的形状是: **提示词叫它做一件事, 而那件事做了也没人记账。**
// 那是「提示词里的上限不算数」的**镜像**: 那一条是"说了不做", 这一条是"做了不算".
// ─────────────────────────────────────────────────────────────

// amendsMarkers 找补句式。
//
// ⚠️ 与 `BodySolicitHits` 同款: **只负责认, 不负责判** ——
// 认出来交给 `MarkAmends`, 而它自己还有一道闸(只给已毁约/已忘的找补)。
//
// ⚠️ 刻意收得**窄**: 它决定的是"解开主动约定权"这件事,
// 而误判的代价不对称 —— 认错了一次不该认的, 那份"欠着"就白欠了。
// 所以只收**明确指向补偿**的说法, 不收"不好意思""抱歉"这类通用客套。
var amendsMarkers = []string{
	"改天请你", "下次请你", "请你吃", "我请客", "我买单",
	"赔你", "补偿你", "补偿一下", "补给你", "给你补",
	"欠你一顿", "欠你一次", "算我欠你", "我欠你的",
}

// AmendsHits 这句话是不是在**找补**。
func AmendsHits(text string) []string {
	t := strings.TrimSpace(text)
	if t == "" {
		return nil
	}
	var out []string
	for _, m := range amendsMarkers {
		if strings.Contains(t, m) {
			out = append(out, m)
		}
	}
	return out
}

// noteAmendsIfAny §5.5 找补账本 —— **真的说出口了才记**。
//
// ⚠️ 调用点必须在**发送之后**那一段(与 `noteDeclineIfAny` 同区):
// 降级帧与硬拒帧里出现"改天请你"**不许**记账 ——
// 那些话**没有发出去**, 而"没发出去的话解开约定权"是白送。
//
// ⚠️ 读库失败 → **不记**(也**不解锁**): 失败往"保持欠着"倒 ——
// 那一侧更保守, 而"白欠一次"比"白送一次信用"轻得多。
func (e *Engine) noteAmendsIfAny(text string) {
	if e.Store == nil {
		return
	}
	// ⚠️ `hits` 要活到日志那一行, 所以**不能**在 if 的初始化语句里声明。
	hits := AmendsHits(text)
	if len(hits) == 0 {
		return
	}
	now := clock.Now()
	id, err := e.Store.PendingAmendsID(now)
	if err != nil {
		log.Warn("convo:agency", "找补: 欠账读不到 → 不记 (失败往「保持欠着」倒): %v", err)
		return
	}
	if id == 0 {
		return // 不欠 —— 说了"请你"也只是客气话
	}
	if err := e.Store.MarkAmends(id, now); err != nil {
		log.Warn("convo:agency", "找补记账失败(约定权不会解锁): %v", err)
		return
	}
	log.Info("convo:agency", "§5.5 找补已记账 (%v) —— 主动约定权解锁", hits)
}
