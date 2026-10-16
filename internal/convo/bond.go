package convo

import (
	"database/sql"
	"time"
)

// ─────────────────────────────────────────────────────────────
// §5.1 三个量 (亲密度 / 敬意 / 熟悉度)
//
// bias.go 早就有 HasCloseness / HasRegard 两个输入字段, 但它们**永远是 false**：
// 没有数据源。后果是 §6.8 的关系权重恒不生效, 而现象只是「她对谁都一样」——
// 看不出是坏了。本文件补上数据源。
//
// ⚠️ 三条设计约束:
//   1. 驱动量必须是**可数的行为** (分享了私事/道了谢/被冷落), 不是情绪判断。
//   2. 好事慢、坏事快 (负面偏好)。对称增减 = 一个没有记忆的量。
//   3. 熟悉度**只增不减** —— 它衡量的是「熟不熟」, 不是「好不好」。
//      「今天吵架所以没那么熟了」是把两个量揉成一个。
// ─────────────────────────────────────────────────────────────

// Bond 三个慢量, 值域 [0,1]。
type Bond struct {
	Closeness   float64 `json:"closeness"`
	Regard      float64 `json:"regard"`
	Familiarity float64 `json:"familiarity"`
}

// BondFamiliarityPerMsg 熟悉度按「见过多少条」增长, 1000 条封顶。
const BondFamiliarityPerMsg = 1.0 / 1000.0

// BondSignal 一轮对话里**真实发生过**的行为。
type BondSignal struct {
	SharedSecret  bool // 说了私事
	AskedAdvice   bool // 向它请教
	Thanked       bool // 道谢
	LateNightTalk bool // 深夜还在聊
	Rebuffed      bool // 被冷落 / 被打断
	Corrected     bool // 被纠正
	Msgs          int  // 本轮消息条数
}

// 幅度。坏事(|0.05|) > 好事(0.04) —— 这是刻意的。
const (
	bondShareCloseness  = 0.04
	bondNightCloseness  = 0.03
	bondRebuffCloseness = -0.05
	bondAdviceRegard    = 0.03
	bondThankRegard     = 0.04
	bondCorrectedRegard = -0.03
)

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// BondDelta 一个信号带来的增量。
func BondDelta(sig BondSignal) (dCloseness, dRegard float64) {
	if sig.SharedSecret {
		dCloseness += bondShareCloseness
	}
	if sig.LateNightTalk {
		dCloseness += bondNightCloseness
	}
	if sig.Rebuffed {
		dCloseness += bondRebuffCloseness
	}
	if sig.AskedAdvice {
		dRegard += bondAdviceRegard
	}
	if sig.Thanked {
		dRegard += bondThankRegard
	}
	if sig.Corrected {
		dRegard += bondCorrectedRegard
	}
	return dCloseness, dRegard
}

// ApplyBond 一次更新。熟悉度单独一条路, 不受好坏影响。
func ApplyBond(cur Bond, sig BondSignal) Bond {
	return ApplyBondScaled(cur, sig, OwnerMultDefault)
}

// ApplyBondScaled 与 ApplyBond 同款, 但把亲密度/敬意的**变化量**按倍率放大
// (§5.7 owner 手动倍率, 设计稿作用 ①)。
//
// ⚠️ **熟悉度不乘倍率**: 它是"聊过多少"的计数性事实, 而倍率说的是"我偏不偏心你"。
// 把熟悉度也乘上去, 会让"主人永远第一"变成"主人的群永远最熟"—— 那是两件事。
//
// ⚠️ 正负都乘: 只放大好事等于开了一个不会记仇的外挂。
func ApplyBondScaled(cur Bond, sig BondSignal, mult float64) Bond {
	dc, dr := BondDelta(sig)
	dc, dr = ScaledBondDelta(dc, dr, mult)
	cur.Closeness = clamp01(cur.Closeness + dc)
	cur.Regard = clamp01(cur.Regard + dr)
	if sig.Msgs > 0 {
		cur.Familiarity = clamp01(cur.Familiarity + float64(sig.Msgs)*BondFamiliarityPerMsg)
	}
	return cur
}

// 阶段阈值。
const (
	BondAcquaintAt = 0.15
	BondFamiliarAt = 0.40
	BondIntimateAt = 0.70
)

// BondStage 关系阶段。
type BondStage string

const (
	BondStageStranger BondStage = "stranger"
	BondStageAcquaint BondStage = "acquaint"
	BondStageFamiliar BondStage = "familiar"
	BondStageIntimate BondStage = "intimate"
)

// BondStageOf 亲密度 → 阶段。
func BondStageOf(b Bond) BondStage {
	// ⚠️ 阈值只在 bondStageOfValue 里写一次 —— 这里再抄一遍必然与它分叉。
	return bondStageOfValue(b.Closeness)
}

// BondLine 注入段。
// ⚠️ 只有**亲近**才给提示。生疏时什么都不说 —— 「我们还不熟」这种台词
// 会把一个性格设定变成一份关系说明书。
func BondLine(b Bond) (string, bool) {
	// ⚠️ 用**质∪量**那一条判据 (owner 定规 2026-10-16「互动量按 uin 通用」):
	// 聊过几百条的人当然算熟 —— 只认亲密度会让「熟」这个词在群里与私聊各有一个定义。
	if BondStageShared(b) != BondStageIntimate {
		return "", false
	}
	if b.Regard >= 0.5 {
		return "【关系】你和对方已经很熟了, 而且你挺服它。可以更随意: 开玩笑、" +
			"直接说想法、不用每句都客气。", true
	}
	return "【关系】你和对方已经很熟了。可以更随意一点, 不用每句都客气。", true
}

// BondRecord 带「有没有这一行」的量。
// ⚠️ 与 bias.go 的 HasCloseness 对齐: 零值与「没有记录」不可区分,
// 少了 Loaded, 忘了填的调用方会静默得到「对所有人都是陌生人」。
type BondRecord struct {
	Bond
	Loaded bool
}

// EnsureBondSchema 建表。
func EnsureBondSchema(db *sql.DB) error {
	_, err := db.Exec("CREATE TABLE IF NOT EXISTS convo_bond (" +
		"owner TEXT NOT NULL, scene_key TEXT NOT NULL, " +
		"closeness REAL NOT NULL DEFAULT 0, regard REAL NOT NULL DEFAULT 0, " +
		"familiarity REAL NOT NULL DEFAULT 0, updated_ts TEXT NOT NULL DEFAULT '', " +
		"PRIMARY KEY (owner, scene_key))")
	return err
}

// LoadBond 读一个场景的关系量。没有记录 → Loaded=false 的零值。
func LoadBond(db *sql.DB, owner, sceneKey string) (BondRecord, error) {
	var r BondRecord
	row := db.QueryRow("SELECT closeness, regard, familiarity FROM convo_bond "+
		"WHERE owner = ? AND scene_key = ?", owner, sceneKey)
	if err := row.Scan(&r.Closeness, &r.Regard, &r.Familiarity); err != nil {
		if err == sql.ErrNoRows {
			return r, nil
		}
		return r, err
	}
	r.Loaded = true
	return r, nil
}

// SaveBond 落一个场景的关系量。
func SaveBond(db *sql.DB, owner, sceneKey string, b Bond) error {
	_, err := db.Exec("INSERT OR REPLACE INTO convo_bond "+
		"(owner, scene_key, closeness, regard, familiarity, updated_ts) VALUES (?,?,?,?,?,?)",
		owner, sceneKey, b.Closeness, b.Regard, b.Familiarity, time.Now().Format(time.RFC3339))
	return err
}
