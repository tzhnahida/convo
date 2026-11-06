package convo

import (
	"database/sql"
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// D / E (2026-11-06 用户批准 "A-E"): 表情节奏 与 反问率。
//
// 体检证据 (11-06, Pi 上 convo_messages):
//   · 12:54:46 文字与表情**同一秒**发出; 19:26:15 连发两条一样的表情;
//     19:26:29 文字 + 19:26:30 表情 —— 真人不会这样: 表情是**隔一会儿**
//     单独补的那一下, 不是跟正文捆在一起发出去的第二段;
//   · 11-05 的 45 条文字里约 12 个问句 (27%), 真人基线 6.2%,
//     而且多是反射式追问「这啥」「笑啥呢」「st 是啥」。
//
// 两条都做成**出口闸门**: 能机器判定的, 就别指望模型自觉 (10-24 的教训:
// 提示词写了「别问这表情什么意思」, 它照问)。
// ─────────────────────────────────────────────────────────────

const (
	// StickerAfterTextGap 刚说完一句话, 多久之内**不补**表情。
	//
	// 真人语料: 表情紧跟在文字后面, 多半不是"补一个", 而是手滑 / 连点 /
	// 两件事挤在一起 —— 用户看到的就是"这傻 AI 怎么又跑去发表情包了"。
	StickerAfterTextGap = 20 * time.Second
	// StickerMinGap 两张表情之间至少隔多久 (连发两张一样的是最刺眼的一种)。
	StickerMinGap = 60 * time.Second
	// StickerMaxPerTurn 一条回复里最多几张表情。
	StickerMaxPerTurn = 1
	// QuestionFatigueWindow / QuestionFatigueHits: 这个窗口里它已经问了
	// 这么多句 → 本轮**问号清零** (只留陈述)。
	QuestionFatigueWindow = 10 * time.Minute
	QuestionFatigueHits   = 2
	// questionScanMsgs 往前看几条自己说过的话。
	questionScanMsgs = 6
)

// aiBeat 场景里它自己最近的一次发言 (说话或发表情)。
type aiBeat struct {
	at   time.Time
	kind string
	text string
}

// recentAIBeats 取场景内它自己最近 n 条消息 (最新在前)。
//
// ⚠️ **按 scene_key 查, 不按全局** —— 它在群里刚说完话, 不该拦住私聊里的表情;
// 反过来私聊里连发两张, 群里那张也该拦。
// ⚠️ 读不到 (表还没建 / 测试里的空库) → 返回 nil, 由调用方按"没有记录 = 放行"处理:
// 一次读库抖动不该让它从此一个字都说不出。
func (e *Engine) recentAIBeats(sceneKey string, n int) []aiBeat {
	if e.Store == nil || e.Store.DB == nil || n <= 0 {
		return nil
	}
	rows, err := e.Store.DB.Query(`SELECT ts, kind, payload_json FROM convo_messages
		WHERE dir='ai' AND scene_key = ? ORDER BY id DESC LIMIT ?`, sceneKey, n)
	if err != nil {
		if err != sql.ErrNoRows {
			log.Debug("convo:engine", "表情节奏读库失败: %v", err)
		}
		return nil
	}
	defer rows.Close()
	var out []aiBeat
	for rows.Next() {
		var ts, kind, payload string
		if serr := rows.Scan(&ts, &kind, &payload); serr != nil {
			continue
		}
		// ⚠️ 必须 ParseInLocation(本地): 落库的 ts 是本地时间字符串,
		// 用 time.Parse 会当成 UTC —— 于是 age 恒少 8 小时 (负的), 闸门永远拦。
		at, perr := time.ParseInLocation(historyTSLayout, strings.TrimSpace(ts), time.Local)
		if perr != nil {
			at = time.Time{} // 时间读不出来: 这条不参与节奏判断, 但不阻断后面的
		}
		out = append(out, aiBeat{at: at, kind: kind, text: payload})
	}
	return out
}

// StickerPaceOK 这张表情**现在**能不能发。
//
// 三条规矩 (全部只看自己在这个场景里说过的话):
//   - 最近一条是文字/追问, 且不到 StickerAfterTextGap → 不发;
//   - 最近一条是表情, 且不到 StickerMinGap → 不发;
//   - 再往前不用看: 一条最新的发言就足以定节奏。
//
// 纯表情回复 (没文字) 不受第一条限制 —— 那是"只发个表情", 本来就该发。
func (e *Engine) StickerPaceOK(sceneKey string) bool {
	if e.Store == nil {
		return true
	}
	beats := e.recentAIBeats(sceneKey, 1)
	if len(beats) == 0 {
		return true
	}
	b := beats[0]
	if b.at.IsZero() {
		return true
	}
	age := clock.Now().Sub(b.at)
	switch b.kind {
	case "text", "ask", "fallback":
		if age < StickerAfterTextGap {
			log.Info("convo:engine", "表情拦截: 刚发过文字 (%s 前)", age.Round(time.Second))
			return false
		}
	case "sticker":
		if age < StickerMinGap {
			log.Info("convo:engine", "表情拦截: 上一张才 %s 前", age.Round(time.Second))
			return false
		}
	}
	return true
}

// QuestionCapFor 本轮允许几个问句 (0 = 本轮只许说陈述)。
//
// 判据是**它自己的历史**: 10 分钟内已经问过 2 句以上, 这一轮就别再问了 ——
// 追问率是跨轮次累积出来的, 单条上限 (BubbleMaxQuestions=1) 拦不住
// 「每隔一条问一句」那种节奏。
//
// ⚠️ 返回 0 不等于"这条不许发": 调用方整形后若什么都不剩, 会退回 1 (见 normalizeWithQuestionCap)。
// 静默比多问一句糟 —— 那是"叫了它不应"。
func (e *Engine) QuestionCapFor(sceneKey string) int {
	if e.Store == nil {
		return BubbleMaxQuestions
	}
	now := clock.Now()
	hits := 0
	for _, b := range e.recentAIBeats(sceneKey, questionScanMsgs) {
		if b.kind != "text" && b.kind != "ask" {
			continue
		}
		if b.at.IsZero() || now.Sub(b.at) > QuestionFatigueWindow {
			continue
		}
		if strings.ContainsAny(b.text, "？?") {
			hits++
		}
	}
	if hits >= QuestionFatigueHits {
		return 0
	}
	return BubbleMaxQuestions
}

// normalizeWithQuestionCap 出口整形 + 问句额度。
//
// ⚠️ 余额为 0 时先按 0 整形; 整条被削空 (= 这一句本来就是个问句) → 退回 1。
// **宁可多问一句, 也不能把"叫了它不应"制造出来。**
func normalizeWithQuestionCap(text string, cap int) string {
	if cap >= 0 {
		if s := NormalizeBubble(text, cap); s != "" {
			return s
		}
	}
	return NormalizeBubble(text, BubbleMaxQuestions)
}
