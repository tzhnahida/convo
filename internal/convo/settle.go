package convo

import (
	"strings"
	"time"

	"github.com/tzhnahida/VocabLock/internal/clock"
)

// ——— §3.6.8 事件沉降规则 ———
//
// 它解的是设计稿自己指出的一处对撞: §2.3 要求"现编必须落库"(否则会自相矛盾),
// §3.6.7.6 又要求"不许无缘无故反复讲述"(否则成了道具依赖)。
//
// **一致 ≠ 反复主动提。** 它必须记得, 但不主动复述; 被问起时按沉降阶段给**不同粒度**的回答。
//
// ⚠️ 时刻以**最后一次被提及**为准, 不是生成时刻 —— 这条是整个规则的关键:
// "我爸住院了"三天后被问 → 答得具体(不穿帮); 一个月后再被问 → "好多了"或"记不太清"
// (**真人也不会记得那么细**)。若按生成时刻算, 一个被反复聊起的事会莫名其妙地"变模糊"。
const (
	// StageDetail 可说细节: 7 天内被提及过。
	StageDetail = "detail"
	// StageVague 模糊态: 7-30 天未被提及 —— **细节不可用, 只保留"发生过"**。
	StageVague = "vague"
	// StageArchived 归档: 30 天且无人问 —— 只被**直接问到**时给模糊回答, 绝不主动提。
	StageArchived = "archived"
	// StageToday 脉冲型: 只在当天上下文里存在, 用完即弃。
	StageToday = "today"
	// StageGone 已经不该出现 (脉冲过期)。
	StageGone = "gone"
)

// 背景型三段沉降的窗口 (小时)。
const (
	StageDetailHours = 7 * 24
	StageVagueHours  = 30 * 24
)

// RepeatedUpgradeAt 被提及多少次之后升级为"稳定事实"(反复型)。
// 设计稿: "越被聊越实 —— 这才对"。
const RepeatedUpgradeAt = 3

// SettleStage 按事件类型与"最后一次被提及"算它现在处在哪一阶段。
//
// ⚠️ 纯函数: 只依赖 (duration, lastMentioned, now)。**阶段不落库** ——
// 落库就要有"谁在什么时候重算它"的问题, 而这个东西随时可算, 存了只会不一致。
func SettleStage(duration, lastMentioned string, now time.Time) string {
	base := parseTS(lastMentioned)
	if base.IsZero() {
		return StageGone
	}
	h := now.Sub(base).Hours()
	switch duration {
	case "pulse":
		// 用完即弃: 只在当天存在
		if now.Format("2006-01-02") == base.Format("2006-01-02") {
			return StageToday
		}
		return StageGone
	case "stable":
		// 反复型: 升级为稳定事实, 不再沉降
		return StageDetail
	default: // background
		switch {
		case h < StageDetailHours:
			return StageDetail
		case h < StageVagueHours:
			return StageVague
		default:
			return StageArchived
		}
	}
}

func parseTS(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.Local)
	if err != nil {
		return time.Time{}
	}
	return t
}

// StageLine 把一条事件按阶段渲染成注入行。
//
// ⚠️ 每个阶段的行必须**把"能说到什么程度"写清楚** —— 只说"这是你的过去"没有用,
// 模型会照样给细节。设计稿要的正是"一个月后答'记不太清'"。
func StageLine(stage, text string) string {
	switch stage {
	case StageToday:
		// 脉冲型"只在当天上下文存在" —— 所以当天它是**可用**的, 只是不进自传体。
		// ⚠️ 第一版这里返回空串, 后果是脉冲事件**占着注入位却不渲染**, 把背景型挤掉了。
		return "- 今天: " + text + " (刚发生的, 可以自然提到; 但别反复说)"
	case StageDetail:
		return "- " + text + " (可以给具体细节)"
	case StageVague:
		return "- " + text + " (⚠️ **细节已经记不清了**, 只保留\"发生过\"这件事; 被问到就答\"记不太清了\"\"就那样\")"
	case StageArchived:
		return "- " + text + " (⚠️ **绝不主动提**; 只有当 ta 直接问到这件事时, 才用模糊的说法回应)"
	default:
		return ""
	}
}

// MentionedIn 判断一段回复是否**真的提到了**这条事件。
//
// 判据是廉价的字面重叠(事件文本里的 2 字以上中文片段是否出现在回复里), 与 §6.3 的
// 廉价判据同源 —— **不需要语义模型**。
//
// ⚠️ 宁可漏判也不误判: 误判会把一条正在沉降的事件**重置回"可说细节"**,
// 而漏判的后果只是它多模糊一会儿 —— 后者明显更轻。
func MentionedIn(reply, eventText string) bool {
	for _, frag := range hanFragments(eventText, 2) {
		if strings.Contains(reply, frag) {
			return true
		}
	}
	return false
}

// hanFragments 抽中文 2 字片段 (去重, 跳过纯虚词片段太短的情况由 n 保证)。
func hanFragments(s string, n int) []string {
	rs := []rune(s)
	var out []string
	seen := map[string]bool{}
	for i := 0; i+n <= len(rs); i++ {
		frag := string(rs[i : i+n])
		if !isHan2(frag) || seen[frag] {
			continue
		}
		seen[frag] = true
		out = append(out, frag)
	}
	return out
}

func isHan2(s string) bool {
	for _, r := range s {
		if r < 0x4E00 || r > 0x9FFF {
			return false
		}
	}
	return len([]rune(s)) == 2
}

// EventNote 注入用的一条事件。
type EventNote struct {
	Stage string
	Text  string
}

// EventNotesLine 渲染【你最近的事】注入段 (空 = 不注入)。
//
// ⚠️ 段里必须有一句"有外部输入才可以提" —— 否则它会拿这条去主动开场,
// 那就从"记得"变成了"复述", 正是 §3.6.7.6 要防的。
func EventNotesLine(notes []EventNote) string {
	if len(notes) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n【你最近遇到的事】(这些是**发生过的**, 必须记得 —— 自相矛盾才是崩塌)\n")
	for _, n := range notes {
		if line := StageLine(n.Stage, n.Text); line != "" {
			b.WriteString(line + "\n")
		}
	}
	b.WriteString("⚠️ **不许无缘无故主动提** —— 只有 ta 问起或聊到相关话题时才可以接。无缘无故重提与道具依赖同源。\n")
	return b.String()
}

// nextMentionStamp 便于测试注入固定时刻。
var nextMentionStamp = func() string { return clock.TodayTS() }
