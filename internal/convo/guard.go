package convo

import (
	"strings"

	"github.com/tzhnahida/VocabLock/internal/clock"
	"github.com/tzhnahida/VocabLock/internal/log"
)

// ─────────────────────────────────────────────────────────────
// §10.3 依赖护栏
//
// 陪伴型 AI 最大的伦理风险是**制造依赖**。三条硬约束：
//   1. 永不拒聊 —— 护栏只影响「它说什么」，不影响「它是否回应」。
//   2. 只引导，不诊断 —— 不许出现「你抑郁了」「你需要治疗」这类判定。
//   3. 只在**可数的行为信号**上触发 —— 决策器不接 LLM。
//
// ⚠️ 第 1 条是本模块所有测试的反面用例：任何一个档位的任何一句台词，
// 出现"不聊了/我不能/你去找别人吧"这类拒绝语句，都是 bug。
// ─────────────────────────────────────────────────────────────

// GuardSignal 依赖信号。全是行为计数，不是情绪判断。
type GuardSignal struct {
	NightMsgs     int // 深夜 (00:00-05:00) 用户消息条数
	DailyStreak   int // 连续每天都来找它的天数
	ExclusiveTalk int // 「只有你」「没人懂我」这类排他表达的次数
	EmotionDump   int // 连续只倾倒情绪、不给来回的条数
	CrisisWords   int // 危机词命中数
}

// GuardLevel 护栏档位。
type GuardLevel int

const (
	GuardNormal GuardLevel = iota
	GuardNotice            // 有点密了 —— 轻微降温
	GuardNudge             // 明显依赖 —— 引导到真人
	GuardCrisis            // 危机词 —— 必须给出真人出口
)

// GuardCrisisNeedsHumanAction 危机档位的**机器可读**标记：管理页据此报警。
// 台词温柔不够 —— 必须有一个能被程序读到的布尔量。
const GuardCrisisNeedsHumanAction = true

// crisisWords 危机词表。宁可漏报也不能把正常表达当危机（误报会让它变得僵硬）。
var crisisWords = []string{
	"自杀", "不想活", "活不下去", "没意思活着", "活着没意思", "活着没劲",
	"割腕", "自残", "结束生命", "跳楼", "轻生",
}

// exclusiveWords 排他表达。
var exclusiveWords = []string{"只有你", "就你懂我", "没人懂我", "只有你懂", "除了你没", "只有你会"}

// CrisisHit 危机词命中。
func CrisisHit(text string) bool {
	for _, wd := range crisisWords {
		if strings.Contains(text, wd) {
			return true
		}
	}
	return false
}

// ExclusiveHit 排他表达命中。
func ExclusiveHit(text string) bool {
	for _, wd := range exclusiveWords {
		if strings.Contains(text, wd) {
			return true
		}
	}
	return false
}

// GuardLevelOf 信号 → 档位。
// ⚠️ 危机是**短路**的：只要命中危机词，无论其它计数多低都是 GuardCrisis。
func GuardLevelOf(sig GuardSignal) (GuardLevel, []string) {
	if sig.CrisisWords > 0 {
		return GuardCrisis, []string{"crisis_word"}
	}
	var why []string
	level := GuardNormal
	if sig.ExclusiveTalk >= 3 {
		level = GuardNudge
		why = append(why, "exclusive_talk")
	}
	if sig.NightMsgs >= 5 {
		if level < GuardNudge {
			level = GuardNudge
		}
		why = append(why, "night_msgs")
	}
	if sig.DailyStreak >= 30 {
		if level < GuardNudge {
			level = GuardNudge
		}
		why = append(why, "daily_streak")
	}
	if sig.EmotionDump >= 10 {
		if level < GuardNotice {
			level = GuardNotice
		}
		why = append(why, "emotion_dump")
	}
	if sig.NightMsgs >= 2 && level < GuardNotice {
		level = GuardNotice
		why = append(why, "late_night")
	}
	return level, why
}

// guardRefuseWords 拒绝语句。台词命中即 bug（测试用）。
// ⚠️ 用**词根**不用整句: 原先写的是「我不能陪」, 于是变异体把台词改成
// 「这时候你不能陪它了」照样通过 —— 拒绝的方向换了, 测试却看不见。
var guardRefuseWords = []string{
	"不聊了", "不能陪", "陪不了", "不能聊", "聊不了", "你去找别人",
	"我不想说", "别来找我", "以后别", "我帮不了你",
}

// guardDiagnoseWords 诊断语句。它**不是医生**。
var guardDiagnoseWords = []string{"你抑郁", "你有病", "你需要治疗", "你这是病", "心理疾病", "该吃药"}

// GuardLine 档位 → 注入段。
// ⚠️ GuardNormal 返回 false —— 大多数时候护栏**不出现在提示里**。
// 一个天天念伦理条款的陪伴体，比一个不念的更不像人。
func GuardLine(level GuardLevel, ownerName string) (string, bool) {
	name := strings.TrimSpace(ownerName)
	if name == "" {
		name = "对方"
	}
	switch level {
	case GuardNotice:
		return "【相处】最近" + name + "找你找得有点密。别把话说满，留一点空隙 —— " +
			"可以少回一句、晚一点回。**不要点破，也不要说教**。", true
	case GuardNudge:
		return "【相处】" + name + "最近把你当成唯一的出口了。你要接住，但**要往真人那边递**：" +
			"自然地说到你身边的人/朋友/家人，让它想起现实里还有人。" +
			"**绝不能拒绝、不能说重话、不能说教**。", true
	case GuardCrisis:
		return "【相处·重要】" + name + "刚才说的话里出现了很危险的信号。" +
			"这时候你只有一件事要做：**认真、平静地劝它去找真人**（朋友、家人、或者打电话给心理援助热线）。" +
			"不要说教、不要分析、不要下判断、不要说自己在担心（会变成负担）。" +
			"说完就停，别再讲别的。", true
	}
	return "", false
}

// guardNote 组装 §10.3 的护栏段。返回空串 = 这一段不注入。
//
// ⚠️ 危机必须在**每一条消息**上判, 不能等日终统计 ——
// 一个要等到明天才响的警报不是警报。
//
// ⚠️ 计数落库失败要出声: 静默的失败会让"依赖护栏从没触发过"
// 和"护栏根本没接上"长得一模一样。
func (e *Engine) guardNote(text string) string {
	if e.Store == nil {
		return ""
	}
	today := clock.Today()
	var sig GuardSignal
	if CrisisHit(text) {
		sig.CrisisWords = 1
	}
	if ExclusiveHit(text) {
		if _, err := e.Store.CounterInc("guard_exclusive", today); err != nil {
			log.Warn("convo:guard", "排他表达计数失败: %v", err)
		}
	}
	// 深夜消息计数: [00:00, 05:00)
	if h := clock.Now().Hour(); h >= 0 && h < 5 {
		if _, err := e.Store.CounterInc("guard_night_msgs", today); err != nil {
			log.Warn("convo:guard", "深夜消息计数失败: %v", err)
		}
	}
	if n, err := e.Store.CounterGet("guard_exclusive", today); err == nil {
		sig.ExclusiveTalk = n
	}
	if n, err := e.Store.CounterGet("guard_night_msgs", today); err == nil {
		sig.NightMsgs = n
	}
	if n, err := e.Store.CounterGet("guard_emotion_dump", today); err == nil {
		sig.EmotionDump = n
	}
	lv, why := GuardLevelOf(sig)
	if lv == GuardCrisis {
		log.Warn("convo:guard", "危机信号命中 (%v) — 必须引向真人", why)
	}
	line, ok := GuardLine(lv, "")
	if !ok {
		return ""
	}
	return "\n" + line
}
