package convo

import (
	"strings"
)

// ─────────────────────────────────────────────────────────────
// §5.7 **关系之间的不对称** (2026-10-15)
//
// 它可以同时对 A 和 B 都好 —— 但 **A 和 B 互相知道吗?**
// 真人的答案是: 不一定, 而且真人会**主动管理这件事**:
//   · 在 A 面前**少提** B(不是心虚, 是**读空气**);
//   · 话题撞上时**换个说法**(「一个朋友」而不是"德克萨斯");
//   · A 和 B 认识时, **可以一起提**。
//
// ⚠️ **这不是 §7.3「禁止跨人转述」的重复。**
// 那条防的是**泄露事实**(A 说过的话不能讲给 B);
// 本节说的是**话题选择策略** —— **事实没泄露, 但它选择在谁面前提谁。**
//
// 落点: 注入段的"关系记忆"**按当前发言人分区** ——
// §6.10.3 已经对**群聊**做了, 本节把它**推广到私聊**:
// **对某人说话时, 关于其他人的记忆降权, 而不是全量注入。**
//
// ⚠️ 是**降权**不是**过滤**: 真人在 A 面前也会提 B, 只是提得少。
// 硬切掉会让它变成一个"每换一个人就失忆一次"的角色 —— 那比说错话更假。
// ─────────────────────────────────────────────────────────────

const (
	// SceneAffinitySame 当前场景的记忆: 加权。
	SceneAffinitySame = 1.25
	// SceneAffinityOther **别的场景**的记忆: 降权 (§5.7 的核心)。
	SceneAffinityOther = 0.35
	// SceneAffinityGlobal 没有场景标签的记忆(**零值 = 不绑定**) → 中性。
	SceneAffinityGlobal = 1.0
)

// ⚠️ **私聊场景键不该在这里发明第二个。**
//
// 我第一版加了 `DmSceneKey(uin) = "dm:<uin>"`, 而 `DefaultSceneKey = "private:owner"`
// **本来就在**(消息入库、repeat 记账都用它)—— 再发明一套只会让两处慢慢漂开。
// 这一节要做的只是**把它传进 DialogInput**, 让取记忆的那一步知道自己在跟谁说话。
// sceneAffinity §5.7 的排序因子。
//
// ⚠️ 三种情况**必须分开**:
//   - 当前场景  → 加权(这正是你在跟谁说话);
//   - **别的场景** → 降权(在 A 面前少提 B);
//   - **没有场景标签** → **中性**。零值 = 未提供 ≠ "别的场景" ——
//     把全局记忆也降权的话, 它会在私聊里几乎想不起任何长期的事。
func sceneAffinity(m Memory, sceneKey string) float64 {
	cur := strings.TrimSpace(sceneKey)
	sk := strings.TrimSpace(m.SceneKey)
	if cur == "" || sk == "" {
		return SceneAffinityGlobal
	}
	if sk == cur {
		return SceneAffinitySame
	}
	return SceneAffinityOther
}
