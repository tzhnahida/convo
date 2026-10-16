package convo

import (
	"time"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// noteRoleOutcome 角色账本的写入点 —— **每次群裁决都记一笔**。
//
// ⚠️ 它和 recordGateOutcome 同一个位置, 理由也一样:
// 只记"它说了话"的那些, 开口率就永远是 100%, 于是位**永远长不出来**。
// 沉默也必须记 —— 位是从"它多少次忍住没说"里长出来的。
func (e *Engine) noteRoleOutcome(sc SceneMsg, out GroupGateResult) {
	if e.Store == nil || sc.SceneKey == "" {
		return
	}
	owner := e.ownerKey()
	// ① 上一条是它说的吗 —— 有人接 / 被顶。窗口只用一次。
	if st, ok, err := e.Store.LoadRoleStats(owner, sc.SceneKey); err == nil && ok && st.LastSpokeTS != "" {
		if t, ok2 := ParseMsgTS(st.LastSpokeTS); ok2 && clock.Now().Sub(t) < RoleFollowUpWindow {
			last := e.lastAIText(2)
			// ⚠️ **不许要求"复述了它的词"才算被接。**
			// 严格 2-gram 匹配会让"被接住"系统性偏 0 (别人回一句「胖了」并不复用它的字),
			// 于是每一个群都会漂向潜水 —— 那是个安静的、方向一致的错。
			// 群里的"被接住"就是: 它说完之后**还有人说话**, 而且那句话不是在顶它。
			if last != "" {
				field := "replied_to"
				if _, hit := ChallengedOf(sc.Text, last); hit {
					field = "contradicted"
				}
				if err := e.Store.BumpRoleStat(owner, sc.SceneKey, field, 1); err != nil {
					log.Warn("convo:role", "角色账本写入失败: %v", err)
				}
			}
			if err := e.Store.ClearRoleSpoke(owner, sc.SceneKey); err != nil {
				log.Warn("convo:role", "角色账本清理失败: %v", err)
			}
		}
	}
	// ② 这一次它做了什么。**沉默也记。**
	switch out.Stance {
	case StanceSpeak:
		if err := e.Store.BumpRoleStat(owner, sc.SceneKey, "spoke", 1); err != nil {
			log.Warn("convo:role", "角色账本写入失败: %v", err)
		}
		if err := e.Store.MarkRoleSpoke(owner, sc.SceneKey, clock.Now()); err != nil {
			log.Warn("convo:role", "角色账本标记失败: %v", err)
		}
	case StanceReact:
		if err := e.Store.BumpRoleStat(owner, sc.SceneKey, "reacted", 1); err != nil {
			log.Warn("convo:role", "角色账本写入失败: %v", err)
		}
	default:
		if err := e.Store.BumpRoleStat(owner, sc.SceneKey, "silent", 1); err != nil {
			log.Warn("convo:role", "角色账本写入失败: %v", err)
		}
	}
}

var _ = time.Now
