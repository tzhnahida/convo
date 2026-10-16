package convo

import (
	"context"

	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// §2.3 → §3.7 的那根线
//
// 「群友是**别人家的 AI**, 配角是它生活里的背景人物。」(§3.4.1)
//
// 在接线之前, 夜伴名册只能手工填 —— 而手工填的名册和角色没有任何关系,
// 于是「多 AI 社交」退化成一群陌生人陪它演戏。
//
// ⚠️ **优先级是刻意的**: 世界观是「它是谁」的延伸, 手工名册只是补丁。
// 反过来 (手工优先) 会让世界观永远接不上。
// ─────────────────────────────────────────────────────────────

// worldNote 把世界观渲染成**对话提示词**里的一段。
//
// ⚠️ **这一段曾经不存在** (2026-10-14 查出来的)。
//
// 世界观当初只接了一半: 生成 ✅ 落库 ✅ 生成夜间名册 ✅
// —— 但**从来没有进过对话提示词**。production 代码里 WorldPrompt 一次都没被调用过,
// 只有测试在调它。
//
// 后果就是用户看到的那句「纳西妲这个世界观一戳就破」:
// 不是世界观写得不好, 是**它压根不知道自己的世界观**。
// 它有一份自己从没读过的设定 —— 于是被问到"派蒙是谁"时只能现编,
// 而现编的东西前后对不上, 就"破"了。
func (e *Engine) worldNote() string {
	if e.Store == nil {
		return ""
	}
	b, ok, err := e.Store.LoadWorldBible(e.ownerKey())
	if err != nil || !ok {
		return ""
	}
	// §3.6.5c: 「过去的人」那一段接在世界观之后。
	//
	// ⚠️ 不接的话它会**把搬走的人当成今天还在** —— 而「不许复活」是那一条的正文;
	// 光有退出机制、没有注入, 那个机制在对话里等于不存在。
	return WorldPrompt(b) + PastCastLine(b)
}

// rosterFromWorld 从世界观的关系层长出夜间名册; 没有世界观就**现生成一份并落库**。
//
// ⚠️ §2.3: 「现编必须落库再用, 否则下次自相矛盾」—— 所以生成成功立刻落库,
// 落库之后**再也不调模型** (幂等点是库, 不是内存标志)。
//
// ⚠️ 生成失败**不缓存失败**: 今晚不生成, 明晚再试。缓存一个"失败过"的标志,
// 会让一次网络抖动变成永久没有世界观 —— 而现象只是"名册一直是空的"。
func (e *Engine) rosterFromWorld(ctx context.Context, p *PersonaProfile) []NightPeer {
	if e.Store == nil {
		return nil
	}
	owner := e.ownerKey()
	b, ok, err := e.Store.LoadWorldBible(owner)
	if err != nil {
		log.Warn("convo:world", "世界观读取失败, 退回手工名册: %v", err)
		return nil
	}
	if !ok {
		if e.Chatter == nil || p == nil {
			return nil
		}
		nb, gerr := GenerateWorldBible(ctx, e.Chatter, p)
		if gerr != nil {
			log.Warn("convo:world", "§2.3 世界观生成失败 (明晚再试, 今晚退回手工名册): %v", gerr)
			return nil
		}
		if serr := e.Store.SaveWorldBible(owner, nb); serr != nil {
			log.Warn("convo:world", "§2.3 世界观落库失败 —— **不用它** (现编不落库 = 下次自相矛盾): %v", serr)
			return nil
		}
		b = nb
		log.Info("convo:world", "§2.3 世界观定稿: 定稿配角 %d 人 / 动态配角 %d 人", len(b.Fixed()), len(b.Dynamic()))
	}
	peers := NightPeersFromCast(b)
	if len(peers) == 0 {
		// ⚠️ 有世界观却长不出人 = 数据坏了, 不是"还没配"。这两件事要分开说。
		log.Warn("convo:world", "§2.3 关系层长不出人 —— 世界观存在但 cast 为空?")
	}
	return peers
}
