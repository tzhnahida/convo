package convo

import "strings"

// ─────────────────────────────────────────────────────────────
// §6.10.4 称呼闸 —— 「别拿别人的外号叫这个人」(2026-11-03 串人事故)
//
// 事故原文: 「这不就是你想参加的那个嘛 **老缇** / 人家还让你别外传 我就当没看见」
//   · 当轮说话人 uin 2735604537, address_as 是**空**;
//   · 「老缇」是同群 uin 351390754 的 address_as;
//   · 那张图的标注只有「一张图, 没细看」——「人家还让你别外传」是**编的**。
//
// 提示词里早就写着「绝不猜外号」, 但那条纪律**没有闸门**就成了建议。
// 这里补的是出口硬闸: 只在**本轮说话人自己没被起过称呼**时才查 ——
// 因为他自己若有称呼, 用到别人的词也可能只是在说别人。
// ─────────────────────────────────────────────────────────────

// AddressViolations 回复里有没有**别人的**称呼。
//
// selfAddress = 本轮说话人在这个场景的 address_as (空 = 没起过)。
// others = 同一场景里**别人**的 address_as: 只放称呼, 不放显示名 ——
// 显示名可以出现在「谁说了什么」里, 称呼只能指向那个人。
func AddressViolations(text, selfAddress string, others []string) []string {
	if strings.TrimSpace(selfAddress) != "" {
		return nil
	}
	t := strings.TrimSpace(text)
	if t == "" || len(others) == 0 {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, o := range others {
		o = strings.TrimSpace(o)
		// ⚠️ 单字称呼不查: 「阿」「小」这种会命中正常词, 误杀一个真回复比漏一个更贵。
		if len([]rune(o)) < 2 || seen[o] {
			continue
		}
		seen[o] = true
		if strings.Contains(t, o) {
			out = append(out, "叫错了人: 「"+o+"」是这个群里**别人**的称呼, 这轮说话的人你没给他起过称呼")
		}
	}
	return out
}

// addressSplit 把本场景的称呼分成两份: 这一轮说话人的, 和别人的。
//
// ⚠️ **一次 `SceneMembers(sceneKey)` 取全** —— 称呼是「按场景各存一份」,
// 跨场景同名不算串 (查询本身已经保证), 这里只区分"这轮是谁"和"其余是谁"。
//
// ⚠️ 私聊只有一个对象, 没有"别人"可言 → 直接返回空, 不做闸。
func (e *Engine) addressSplit(sceneKey, speakerUIN string) (self string, others []string) {
	if e.Store == nil || SceneKindOf(sceneKey) != SceneKindGroup {
		return "", nil
	}
	members, err := e.Store.SceneMembers(sceneKey)
	if err != nil {
		return "", nil
	}
	speaker := strings.TrimSpace(speakerUIN)
	for _, mb := range members {
		a := strings.TrimSpace(mb.AddressAs)
		if a == "" {
			continue
		}
		if speaker != "" && strings.TrimSpace(mb.UIN) == speaker {
			self = a
			continue
		}
		others = append(others, a)
	}
	return self, others
}
