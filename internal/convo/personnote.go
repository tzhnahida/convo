package convo

import (
	"fmt"
	"strings"
)

// ─────────────────────────────────────────────────────────────
// §6.10.3 **记忆注入的主体选择(关键)**
//
// 设计稿原话:
//
//   > 只注入 ① **当前发言人的**关系记忆 + ② **群公共记忆**
//
//   > A 在群里说话 → 只注入 A 的记忆; **B 的私事在上下文里根本不存在**
//
// ⚠️ 而生产路径上 `DialogInput.SpeakerUIN` **一路传到注入层就蒸发了**:
// 它在 `value.go` 被填上, 而 `buildSystem` **一个字节都没读它**。
// 于是"谁在说"这件事在注入层是**死的** ——
// 段 5 装的是**按场景**取的记忆(整个群的), 不是"关于这个人"的。
//
// ⚠️ 这一条是 **§5.4 跨人转述红线的注入侧** ——
// 存储侧由 `SavePersonMemory` 分区保证, 而"取的时候只取对的人"要靠这里。
// ─────────────────────────────────────────────────────────────

// displayNameOf 取某人在某场景的显示名 —— 取不到就返回空串。
//
// ⚠️ **取不到 = 未提供**, 不是"他叫空名字":
// `PersonNoteLine` 会据此退回"这个人", 而**不会**在提示词里留个空括号。
//
// ⚠️ 走 `SceneMemberByUIN`(唯一的身份查询入口), **没有"按名字查"那条路** ——
// 那正是 §6.10.4「绝不猜外号」的机器化形态。
func (e *Engine) displayNameOf(sceneKey, uin string) string {
	if e.Store == nil {
		return ""
	}
	m, err := e.Store.SceneMemberByUIN(sceneKey, uin)
	if err != nil || m == nil {
		return ""
	}
	return strings.TrimSpace(m.DisplayName)
}

// PersonNoteLine 渲染「这一轮是谁在说话 + 关于他我知道什么」。
//
// ⚠️ **零值 = 未提供**: 没有记忆、没有稳定事实、或没有 uin → 返回空串,
// 一个字都不进提示词(空段会白占 token, 而且会让模型以为"关于他什么都不知道"是条信息)。
//
// ⚠️ **§6.10.7: 注入里绝不许出现 owner 标记** —— 这里只出现 uin 尾号与名字,
// 与段 7 的发言流用同一套锚(尾号), 不引入任何特权标记。
//
// ⚠️ **名字与 uin 都可为空** —— 名字为空时退回"这个人",
// 而 uin 为空时**不带括号**(与 `SceneHistoryLine` 同一条纪律:
// "有个人"和"uin 是问号"是两件事)。
func PersonNoteLine(displayName, uin string, mems []PersonMemory, facts []PersonMemory) string {
	if len(mems) == 0 && len(facts) == 0 {
		return ""
	}
	name := strings.TrimSpace(displayName)
	if name == "" {
		name = "这个人"
	}
	who := name
	if strings.TrimSpace(uin) != "" {
		who += "(" + UinTail(uin) + ")"
	}
	var b strings.Builder
	// ⚠️ 标题里点明"**这一轮**在说话的那个人" ——
	// 不点明的话, 群里的"关于 ta 的事"会被模型当成"关于所有人"。
	fmt.Fprintf(&b, "\n【关于**这一轮说话的人** %s】", who)
	if len(mems) > 0 {
		b.WriteString("\n（他的事, 只有他在场时才用得上; 别人在这个群里的事不在你的上下文里）\n")
		for _, m := range mems {
			fmt.Fprintf(&b, "- [%s] %s: %s\n", m.MemID, m.Label, m.V)
		}
	}
	if len(facts) > 0 {
		// §5.6「全局级只放**这个人的稳定事实**」—— 它们与关系记忆分开放,
		// 因为取舍不同: 前者是"关于他的事", 后者是"他是个什么样的人"。
		b.WriteString("（他的稳定事实 —— 只在他本人在场时可用）\n")
		for _, m := range facts {
			fmt.Fprintf(&b, "- %s: %s\n", m.Label, m.V)
		}
	}
	return b.String()
}
