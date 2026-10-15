package convo

import (
	"strings"
)

// ─────────────────────────────────────────────────────────────
// §6.6 群聊角色位
//
// §1.3: 「群友的人味来自固定位, 不来自人设厚度」。
//
// ⚠️ 所以角色必须**按 (人格, 群) 确定性地固定下来**:
// 不能每天换, 不能每轮掷骰子, 也不能随重启改变 ——
// 一个今天捧哏、明天杠精的群友不是「多面」, 是没有位置。
// 因此这里是 DayRoll (确定性哈希) 而不是 Dice。
// ─────────────────────────────────────────────────────────────

// GroupRole 群里的位。
type GroupRole string

const (
	GroupRoleNone       GroupRole = ""
	GroupRoleFoiler     GroupRole = "foiler"     // 捧哏
	GroupRoleContrarian GroupRole = "contrarian" // 杠精
	GroupRoleLurker     GroupRole = "lurker"     // 潜水, 偶尔一针见血
	GroupRoleAnchor     GroupRole = "anchor"     // 组织者
)

// GroupRoleContrarianDailyMax 杠精一天最多杠两次。
// 位是固定的, **频次是配额** —— 位不等于纵容。
const GroupRoleContrarianDailyMax = 2

// AssignGroupRole 确定性分位。同一个人格在同一个群里永远同一个位。
func AssignGroupRole(personaKey, sceneKey string) GroupRole {
	k := strings.TrimSpace(personaKey) + "|" + strings.TrimSpace(sceneKey)
	switch v := DayRoll("grouprole:"+k, 100); {
	case v < 30:
		return GroupRoleFoiler
	case v < 50:
		return GroupRoleContrarian
	case v < 80:
		return GroupRoleLurker
	default:
		return GroupRoleAnchor
	}
}

// GroupRoleLabel 中文名。
func GroupRoleLabel(r GroupRole) string {
	switch r {
	case GroupRoleFoiler:
		return "捧哏"
	case GroupRoleContrarian:
		return "杠精"
	case GroupRoleLurker:
		return "潜水"
	case GroupRoleAnchor:
		return "组织者"
	}
	return ""
}

// GroupRoleNeedBonus 位的开口门槛修正。
// ⚠️ 这是本模块唯一与 §6.3 判据表直接相连的落点:
// 潜水位的「偶尔」必须落在**门槛**上, 不能落在「随机少说」上 ——
// 随机少说看不出效果, 门槛能。
func GroupRoleNeedBonus(r GroupRole) int {
	switch r {
	case GroupRoleLurker:
		return 2
	case GroupRoleFoiler:
		return -1
	}
	return 0
}

// GroupRoleLine 位 → 注入段。
// ⚠️ 空位返回 false: 没分到位就什么都不说, 不许给一段「你是个普通群友」。
func GroupRoleLine(r GroupRole, spokenToday int) (string, bool) {
	switch r {
	case GroupRoleFoiler:
		return "【群里·你的位】你是接话的那个人: 别人说完你搭一句, 把话头递回去。" +
			"不抢主角, 不主导话题。", true
	case GroupRoleContrarian:
		// §4.6.1: 这里是**结构上的内在化** —— 额度用完不报数, 而是换一副心情。
		// 原写法两头都占: 既宣告"一天最多两次", 用完还要再宣告一次"你今天已经抬过杠了"。
		// 真人不会数自己抬了几次杠, 但他会**觉得刚才那两句有点冲** —— 所以只说感觉。
		if spokenToday >= GroupRoleContrarianDailyMax {
			return "【群里·你的位】你刚才已经唱过一次反调了 —— 这一轮顺着说, 别硬拗。", true
		}
		return "【群里·你的位】你是爱唱反调的那个: 别人说好你就挑刺, 但**只挑事、不挑人**。" +
			"而且要有理由 —— 没理由的抬杠是烦人, 不是性格。", true
	case GroupRoleLurker:
		return "【群里·你的位】你平时潜水, 但只要开口就是**一句顶用**的:" +
			"要么是个别人没想到的点, 要么是个准的笑话。**不参与寒暄**。", true
	case GroupRoleAnchor:
		return "【群里·你的位】你是张罗事的那个人: 有人起头你就接住, 没人说话你也不硬撑。", true
	}
	return "", false
}
