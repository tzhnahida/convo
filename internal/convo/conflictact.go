package convo

import (
	"strings"

	"github.com/tzhnahida/VocabLock/internal/clock"
	"github.com/tzhnahida/VocabLock/internal/log"
)

// ─────────────────────────────────────────────────────────────
// §6.9 冲突介入的**执行**
//
// conflict.go 判的是「要不要介入」，本文件判的是「怎么劝」。
// 三式：
//   Divert 转移 —— 把话题掰开，不评理
//   Absorb 吸收 —— 自嘲/卖萌，把火气引到自己身上
//   DM     私下 —— 单独找被夹的那个人
//
// ⚠️ 头号红线：**它绝不评理**。任何一式的执行指令里出现「你是对的」「他错了」
// 这类判定，都是 bug —— 一个会站队的群友不是和事佬，是添柴的。
// ─────────────────────────────────────────────────────────────

// ConflictAct 介入方式。
type ConflictAct string

const (
	ConflictActNone   ConflictAct = "none"
	ConflictActDivert ConflictAct = "divert"
	ConflictActAbsorb ConflictAct = "absorb"
	ConflictActDM     ConflictAct = "dm"
)

// ConflictActInput 执行层的输入。
type ConflictActInput struct {
	Grade           int    // 冲突强度 1..3 (来自 conflict.go 的判定)
	MediationHit    bool   // 已经有人劝过了
	SceneKind       string // group | private
	IsOwnerInvolved bool   // 群主在其中一方
	ActedToday      int    // 今天已经介入过几次
	CoolingDown     bool   // 上一次介入的冷却还没过
}

// ConflictActDailyCap 一天最多介入两次。
// ⚠️ 与 §4.3 一致：介入是**例外**，例外的失败方向是"不做"。
const ConflictActDailyCap = 2

// DecideConflictAct 选一式。返回 (方式, 理由) —— 理由进 trace，可回归。
func DecideConflictAct(in ConflictActInput) (ConflictAct, string) {
	if in.CoolingDown {
		return ConflictActNone, "cooldown"
	}
	if in.ActedToday >= ConflictActDailyCap {
		return ConflictActNone, "daily_cap"
	}
	if SceneKindOf(in.SceneKind) == SceneKindPrivate {
		return ConflictActNone, "private_no_mediation"
	}
	if in.IsOwnerInvolved && in.Grade >= 2 {
		return ConflictActDM, "owner_involved_no_public_side"
	}
	if in.Grade >= 3 {
		if in.MediationHit {
			return ConflictActAbsorb, "already_mediated_hard"
		}
		return ConflictActDM, "too_hot_to_mediate_publicly"
	}
	if in.MediationHit {
		return ConflictActAbsorb, "already_mediated"
	}
	if in.Grade == 2 {
		return ConflictActDivert, "medium_divert"
	}
	return ConflictActAbsorb, "mild_absorb"
}

// conflictJudgeWords 评理词。执行指令命中即 bug。
// 注意它同时包含"护着甲"和"护着乙"两类 —— 站哪边都是站边。
var conflictJudgeWords = []string{
	"你是对的", "你没错", "你有理", "他错了", "是他不对", "怪他", "都怪",
	"你不对", "你也有错", "谁对谁错", "谁错了", "该道歉的是",
}

// ConflictActFrame 把招式翻成**可执行的指令**（不是硬编码台词 —— 台词由模型说）。
// 返回 false = 这一式没有可执行内容（None）。
func ConflictActFrame(act ConflictAct, name, topic string) (string, bool) {
	n := strings.TrimSpace(name)
	if n == "" {
		n = "对方"
	}
	t := strings.TrimSpace(topic)
	if t == "" {
		t = "刚才那事"
	}
	switch act {
	case ConflictActDivert:
		return "【冲突·转移】群里有点火药味了。做法：**把话题掰开** —— " +
			"提一件完全无关的具体小事（吃饭/天气/某个梗），把注意力引走。" +
			"一句话就够，不要评论刚才的争论，不要提「别吵了」这种话。", true
	case ConflictActAbsorb:
		return "【冲突·吸收】气氛有点紧。做法：**把火气引到自己身上** —— " +
			"自嘲一句 / 发个表情 / 说一句自己更糗的事。" +
			"不要站队，不要总结，不要当和事佬。", true
	case ConflictActDM:
		return "【冲突·私下】刚才" + t + "闹得不太好看。" + n + "可能正憋着火。" +
			"做法：**私聊** " + n + " 单独说一句，只问一句「你还好吧」，" +
			"**不要复述争论内容、不要分析、不要替另一边说话**。", true
	}
	return "", false
}

// ConflictActFrameIsClean 执行指令是否干净（无评理词）。测试与运行期都在用。
func ConflictActFrameIsClean(frame string) bool {
	for _, wd := range conflictJudgeWords {
		if strings.Contains(frame, wd) {
			return false
		}
	}
	return true
}

// conflictActNote 判"这一轮该怎么劝" (§6.9 的执行层)。
//
// 判据层 (GateConflict / DetectConflict) 只说"要不要介入"; 这里把结果翻成
// **可执行的指令** —— 三式 (转移/吸收/私下) 与每日上限都在 DecideConflictAct 里。
//
// ⚠️ 介入是**例外**, 例外的失败方向是"不做": 任何一步读不出来都返回 false。
func (e *Engine) conflictActNote(sceneKey string) (string, bool) {
	if e.Store == nil || strings.TrimSpace(sceneKey) == "" {
		return "", false
	}
	if SceneKindOf(sceneKey) != SceneKindGroup {
		return "", false // 私聊里没有"劝架"这回事
	}
	// ⚠️ 窗口构造走**共享的那个函数** (conflictWindowOf) ——
	// 判定侧 (applyConflictGate) 用的是同一份, 两处不许各写一遍。
	cmsgs, pol, ok := e.conflictWindowOf(sceneKey, 20)
	if !ok {
		return "", false
	}
	// heated 是"**窗口里已经有人劝过**" —— 它在共享函数之外, 因为只有这一侧要它。
	heated := false
	for _, m := range cmsgs {
		if hit, _ := ConflictMediationHit(m.Text, pol.MediationWords); hit {
			heated = true
		}
	}
	base := ConflictBaselineFrom(cmsgs, pol)
	if hit, _ := DetectConflict(cmsgs, base, pol); !hit {
		return "", false
	}
	// grade 是**档位**, 不是连续公式: 已经有人劝过 = 吵得更凶 (劝不住才需要第二次)。
	grade := 2
	if heated {
		grade = 3
	}
	today := clock.Today()
	acted, _ := e.Store.CounterGet("conflict_act", today)
	act, reason := DecideConflictAct(ConflictActInput{
		Grade: grade, MediationHit: heated, SceneKind: sceneKey, ActedToday: acted,
	})
	if act == ConflictActNone {
		return "", false
	}
	frame, ok := ConflictActFrame(act, "", "")
	if !ok {
		return "", false
	}
	if _, err := e.Store.CounterInc("conflict_act", today); err != nil {
		log.Warn("convo:engine", "冲突介入计数失败: %v", err)
	}
	log.Info("convo:engine", "§6.9 冲突介入: %s (%s)", act, reason)
	return frame, true
}
