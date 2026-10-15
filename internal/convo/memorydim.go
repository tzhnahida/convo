package convo

import (
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────
// §7.3 三个正交维度
//
// | 维度     | 取值            | 决定                     |
// | 敏感度   | 公共/关系/私域  | 能不能长期记、能不能跨人用 |
// | 主体归属 | 某人/某群/自己  | 对谁说                   |
// | 时间     | 生效/失效区间   | 旧事实被新事实取代        |
//
// 主体归属早就在 (source + scene_key)。本文件补的是另外两维。
//
// ⚠️ **未提供 ≠ 禁止**。历史行没有 sensitivity / scene_key,
// 把它们读成"关系级且不属于任何场景"→ 全部不可用, 表现是「它突然什么都不记得了」。
// 所以两处零值都取**允许**: 闸门只对**显式声明过**的记忆生效。
// ─────────────────────────────────────────────────────────────

// memoryNoSupersedeKinds 天然**并存**的类型, 不许互相取代。
//
// ⚠️ 这是本维最容易出事故的一条: 「下周三体检」和「月底交房租」都是 k=plan,
// 若自动按 k 派生槽位, 后写的那条会把前一条**静默作废** ——
// 而现象只是"它忘了我说过要体检"。
var memoryNoSupersedeKinds = map[string]bool{
	"plan": true, "event": true, "todo": true, "reminder": true, "schedule": true,
}

// MemorySlot 槽位键。返回 "" = **不参与取代**。
//
// 槽位的语义是「同一个问题只能有一个答案」: 猫叫什么、住哪、几点上班。
// 计划类不是这种问题 —— 它们可以有很多个, 所以不构成槽。
func MemorySlot(kind, k, label string) string {
	if memoryNoSupersedeKinds[strings.ToLower(strings.TrimSpace(kind))] {
		return ""
	}
	kk := strings.TrimSpace(k)
	ll := strings.TrimSpace(label)
	if kk == "" || ll == "" {
		return "" // 缺一半就不构成槽 —— 宁可不错杀
	}
	return kk + "|" + ll
}

// MemoryCrossScene 敏感度 → **能不能跨人用** (§7.3 维一)。
//
// 关系级和私域级都**不能**; 只有 public 可以。
func MemoryCrossScene(sens string) bool {
	return strings.TrimSpace(sens) == SensitivityPublic
}

// MemoryUsableIn 这条记忆**此刻、在这个场景**能不能被注入。
//
// 返回 false 的每一条都有独立的理由, 不要合并 —— 合并之后
// 「被新事实取代」和「从来没绑过场景」会表现成同一个东西, 无法归因。
func MemoryUsableIn(m Memory, sceneKey string, now time.Time) bool {
	// 维三: 失效区间左端 —— 被取代。
	if strings.TrimSpace(m.SupersededTS) != "" {
		return false
	}
	// 维三: 失效区间右端 —— 过期。
	if vu := strings.TrimSpace(m.ValidUntil); vu != "" {
		t, ok := ParseMsgTS(vu)
		if !ok {
			return false // 解析不了的期限 → 宁可不用 (失败往不做倒)
		}
		if !now.Before(t) {
			return false
		}
	}
	// 维一: 私域根本不该在这里 (写入口已丢), 读到就是脏数据 → 不用。
	if strings.TrimSpace(m.Sensitivity) == SensitivityPrivate {
		return false
	}
	// 维一: 显式 public → 任何场景可用。
	if MemoryCrossScene(m.Sensitivity) {
		return true
	}
	// ⚠️ 零值/未提供 → **允许** (零回归)。见文件头。
	sk := strings.TrimSpace(m.SceneKey)
	if sk == "" {
		return true
	}
	return sk == strings.TrimSpace(sceneKey)
}

// SupersedeSlot 把同槽位的**其它**活跃行标为失效 (§7.3 维三)。
//
// ⚠️ 它是 UPDATE 而不是 DELETE: 「旧事实被新事实取代」不等于「旧事实没发生过」。
// 删掉之后就无法回答「它以前以为猫叫什么」—— 而那正是判断它有没有"改口"的唯一依据。
func (s *Store) SupersedeSlot(slot, keepMemID, ts string) error {
	if strings.TrimSpace(slot) == "" {
		return nil
	}
	_, err := s.DB.Exec("UPDATE convo_memory SET superseded_ts = ? "+
		"WHERE slot_key = ? AND mem_id <> ? AND COALESCE(superseded_ts,'') = ''",
		ts, slot, keepMemID)
	return err
}
