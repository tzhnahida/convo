package convo

import (
	"time"

	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// §4.4.4 **惊醒与求助** (2026-10-16 接线)
//
// 设计稿 L844-856:
//
//   > **噩梦**(约 15%)触发"**惊醒**":
//   > · **睡眠窗内绝不发消息**(打扰红线, **优先级高于一切**)
//   > · **可以求助**: 若惊醒且**不在睡眠窗内**, 可发一条**极短**的消息("睡不着")
//
// 以及:
//
//   > ⚠️ **接收对象硬绑定 owner**(2026-10-13 定):
//   > **脆弱只对主人展现**, 即使某个群友的好感度更高也不给他看。
//
// ⚠️ 梦本身早就接好了(`MakeDream` 在夜间批里, 噩梦已落库) ——
// 而**惊醒之后三道闸一道都没有**: `StartleMaySend` / `StartlePermit` /
// `StartleTarget` 全是零调用。
// ─────────────────────────────────────────────────────────────

// StartleAskLine 那条求助消息的**唯一正文**。
//
// ⚠️ 设计稿要的是「**极短**的一条」—— 所以它是一个常量, 不是一个生成器。
// 让它由模型自由发挥, 那句"睡不着"就会变成一段需要被安慰的独白,
// 而那是**情绪勒索**, 正是这一节要避免的东西。
const StartleAskLine = "睡不着"

// startleAsk §4.4.4 的**唯一出口**: 惊醒之后要不要开口求助。
//
// ⚠️ **三道闸的顺序是设计稿给的, 不许重排**:
//
//	① 打扰红线(睡眠窗内绝不发) → ② 频率(≤1 次/月) → ③ 对象(硬绑定 owner)
//
// ⚠️ **①的失败方向是"不发"**:
// 「睡眠窗内绝不发」是**打扰那一侧**的红线, 与插话同族 ——
// 项目在插话那一处已经定过规矩: 「插话是"打扰"那一侧的例外,
// **例外的失败只能往"不做"倒**」。所以**日程读不出来时也不发**(不是"判不出就发")。
//
// ⚠️ 而"没有日程"(`ws == nil`, **未提供**)是另一回事:
// 那就不存在睡眠窗可违反, `StartleMaySend` 返回 true 是对的。
// 两者在这一层分开, 而不是在那个函数里各自解释。
func (e *Engine) startleAsk(now time.Time) {
	if e.Sender == nil || e.Store == nil {
		return
	}
	// ① 打扰红线。
	ws, werr := e.Store.LoadSchedule()
	if werr != nil {
		log.Warn("convo:dream", "§4.4.4 惊醒求助: 日程读不到 → **不发** (打扰侧失败往「不说」倒): %v", werr)
		return
	}
	if !StartleMaySend(now, ws) {
		log.Info("convo:dream", "§4.4.4 惊醒求助: 睡眠窗内 → 不发 (打扰红线)")
		return
	}
	// ② 频率 ≤1 次/月。
	//
	// ⚠️ 它是**生活上限** —— 只判在这里, **绝不告诉模型数字**(§4.6.1)。
	// 提示词里写"你一个月只能求助一次", 它就会开始数着求助。
	permit, perr := e.Store.StartlePermit(now)
	if perr != nil {
		log.Warn("convo:dream", "§4.4.4 惊醒求助: 额度读不到 → 不发: %v", perr)
		return
	}
	if !permit {
		log.Info("convo:dream", "§4.4.4 惊醒求助: 本月已经用过 → 不发")
		return
	}
	// ③ 对象硬绑定 owner。
	//
	// ⚠️ `StartleTarget` 的签名**本身就是护栏** —— 它不接受候选列表,
	// 所以"挑一个好感度高的群友"这件事在这一行上**做不到**。
	to := StartleTarget(e.UserID)
	if err := e.Sender.SendText(to, StartleAskLine); err != nil {
		log.Warn("convo:dream", "§4.4.4 惊醒求助发送失败: %v", err)
		return
	}
	// ⚠️ **发出去之后才记账** —— 与 `MarkSparkSaidAfter` 同规:
	// 先记的话, 一次发送失败就会吃掉整月额度, 而它其实谁都没说过。
	if _, err := e.Store.CounterInc("nightmare_ask", now.Format("2006-01")); err != nil {
		log.Warn("convo:dream", "惊醒求助记账失败(月度额度会算不准): %v", err)
	}
	log.Info("convo:dream", "§4.4.4 惊醒求助: 已发给 owner")
}
