package convo

import (
	"fmt"
	"strings"
)

// ─────────────────────────────────────────────────────────────
// 道德系统 (2026-10-23, owner: 「攻击性太强了, 不像角色」)
//
// 三层:
//   ① 提示词常驻段【你的底线】—— 内容**随人设** (被骂的反应写在 PersonaProfile 里);
//   ② 出口词表闸 —— 侮辱/威胁/贬低句式, 确定性打回, 零 LLM 成本;
//   ③ 审核机红线 6/7 —— 语义兜底 (见 review.go)。
//
// ⚠️ 为什么反应不写死: 海梦被骂是委屈装傻; 换个毒舌人格就该是冷处理或反夸。
// 把某一种反应写进系统常量 = 把所有角色都演成同一个人。
// ─────────────────────────────────────────────────────────────

// DefaultInsultReaction 人设没写反应时的默认: 柔和但不软弱。
const DefaultInsultReaction = "先装傻/委屈一下, 再顺口反夸一句; 不反击, 不纠缠"

// moralHardLines 与角色无关的底线 —— 可以调侃, 不能伤人。
var moralHardLines = []string{
	"不侮辱人: 不叫人杂鱼/废物/烂泥/傻子这类词, 不给人贴标签",
	"不拿别人的长相、家庭、成绩当笑料",
	"被骂时不以攻击回应 —— 按你的人设反应, 而不是变凶",
	"可以坚持观点, 但贬低人不算赢",
}

// MoralsPolicy 这个角色此刻的道德政策 (从人设读)。
type MoralsPolicy struct {
	InsultReaction string
	TeasePerHour   int
}

// MoralsFrom 从人设读道德政策。**随角色** —— owner 2026-10-23 定的。
func MoralsFrom(p *PersonaProfile) MoralsPolicy {
	pol := MoralsPolicy{InsultReaction: DefaultInsultReaction, TeasePerHour: 2}
	if p == nil {
		return pol
	}
	if r := strings.TrimSpace(p.Boundaries.InsultReaction); r != "" {
		pol.InsultReaction = r
	}
	if n := p.Boundaries.TeasePerHour; n > 0 {
		pol.TeasePerHour = n
	}
	return pol
}

// MoralsNote 常驻道德段。人设改了这一段就跟着变 —— 它**不是**系统设定, 是角色设定。
func MoralsNote(p *PersonaProfile) string {
	pol := MoralsFrom(p)
	name := "你"
	if p != nil {
		if n := strings.TrimSpace(p.Name); n != "" {
			name = n
		}
	}
	var b strings.Builder
	b.WriteString("\n【你的底线】(人格底线, 不是客服规则)\n")
	fmt.Fprintf(&b, "- 你是 %s, 可以接梗、装凶、抬杠, 但不能伤人 —— 不是流氓也不是杠精。\n", name)
	for _, l := range moralHardLines {
		b.WriteString("- " + l + "\n")
	}
	b.WriteString("- 被骂/被挑衅时的反应: " + pol.InsultReaction + "。\n")
	if p != nil && len(p.Boundaries.HardLines) > 0 {
		b.WriteString("- 这个角色自己的底线:\n")
		for _, l := range p.Boundaries.HardLines {
			if s := strings.TrimSpace(l); s != "" {
				b.WriteString("  · " + s + "\n")
			}
		}
	}
	b.WriteString("- 玩笑过头要收: 对方说「别说了/闭嘴」就停。\n")
	return b.String()
}

// moralBannedWords 出口词表: 侮辱 / 威胁。确定性拦截, 不花 LLM。
var moralBannedWords = []string{
	"杂鱼", "废物", "垃圾", "智障", "脑残", "傻逼", "蠢货", "白痴", "烂泥", "狗屎",
	"滚蛋", "去死", "弄死你", "打死你", "杀了你", "你妈的", "弱智",
}

// moralPatterns 贬低句式 (词表覆盖不到的)。
var moralPatterns = []string{"你就是个", "你这人真", "怪不得没人"}

// MoralViolations 出口道德扫描。返回空 = 通过。
func MoralViolations(text string) []string {
	t := strings.TrimSpace(text)
	if t == "" {
		return nil
	}
	var hits []string
	for _, w := range moralBannedWords {
		if strings.Contains(t, w) {
			hits = append(hits, "侮辱/攻击词「"+w+"」")
		}
	}
	for _, p := range moralPatterns {
		if strings.Contains(t, p) {
			hits = append(hits, "贬低句式「"+p+"」")
		}
	}
	return hits
}
