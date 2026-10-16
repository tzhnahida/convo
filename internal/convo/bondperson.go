package convo

import (
	"database/sql"
	"strconv"
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// §5.1 **人物层**关系量 —— 按 (owner, uin) 存, 跨场景累积。
//
// ⚠️ 为什么要有它 (2026-10-16 owner 定规):
// convo_bond 是**按场景**存的: "group:1034712035" 一行, "private:<uin>" 一行。
// 于是「群里聊得火热、私聊没聊过」长成这样:
//
//	群那一行 closeness 0.37 → 群阶段 acquaint
//	私聊那一行 closeness 0    → 私聊阶段 **stranger**
//	⇒ 私聊热力 cap 20 < 每条主动消息的代价 25 ⇒ **私聊主动配额恒 0** ⇒ 疏离
//
// 而群与私聊**是同一个人** —— 那不是「关系还没到」, 是**记错了地方**。
//
// ⚠️ 方向规则 (owner 逐字给的, 四条一条都不能反):
//
//	① 群与私聊**共用同一个 uin 的量** —— 群里聊得多, 私聊不该疏离;
//	② A 群的互动只影响 **A 群** 与 **该用户的私聊**;
//	③ B 群同理 —— **群之间互不串**;
//	④ **私聊永不进群** —— 私聊里说的话不许让它在群里更熟。
//
// ⚠️ 落点是一张**独立的表**, 不是合成场景键:
// SceneKindOf 对未知前缀**默认当群**, 拿 "person:<uin>" 混进 convo_bond
// 会被所有按场景键判断的地方读成「一个群」—— 那是最难查的一类错。
//
// ⚠️ 方向规则由**读侧**保证, 不靠调用方自觉:
//
//	私聊场景 → 读**人物层**;  群场景 → 只读**本群那一行**。
//
// 于是 ②③④ 是结构性的: 群那一行只被自己那一个群读写, 私聊那行不进群。
// ─────────────────────────────────────────────────────────────

// EnsureBondPersonSchema 建表。
func EnsureBondPersonSchema(db *sql.DB) error {
	_, err := db.Exec("CREATE TABLE IF NOT EXISTS convo_bond_person (" +
		"owner TEXT NOT NULL, uin TEXT NOT NULL, " +
		"closeness REAL NOT NULL DEFAULT 0, regard REAL NOT NULL DEFAULT 0, " +
		"familiarity REAL NOT NULL DEFAULT 0, updated_ts TEXT NOT NULL DEFAULT '', " +
		"PRIMARY KEY (owner, uin))")
	return err
}

// LoadBondPerson 读一个人的人物层关系量。没有记录 → Loaded=false 的零值。
func LoadBondPerson(db *sql.DB, owner, uin string) (BondRecord, error) {
	var r BondRecord
	uin = strings.TrimSpace(uin)
	if db == nil || uin == "" {
		return r, nil
	}
	row := db.QueryRow("SELECT closeness, regard, familiarity FROM convo_bond_person "+
		"WHERE owner = ? AND uin = ?", owner, uin)
	if err := row.Scan(&r.Closeness, &r.Regard, &r.Familiarity); err != nil {
		if err == sql.ErrNoRows {
			return r, nil
		}
		return r, err
	}
	r.Loaded = true
	return r, nil
}

// SaveBondPerson 落一个人的人物层关系量。
func SaveBondPerson(db *sql.DB, owner, uin string, b Bond) error {
	if db == nil || strings.TrimSpace(uin) == "" {
		return nil
	}
	_, err := db.Exec("INSERT OR REPLACE INTO convo_bond_person "+
		"(owner, uin, closeness, regard, familiarity, updated_ts) VALUES (?,?,?,?,?,?)",
		owner, uin, b.Closeness, b.Regard, b.Familiarity, time.Now().Format(time.RFC3339))
	return err
}

// PersonUINOfScene 场景键 → 对面**那个人**。群场景没有单一个人 → 空串。
//
// ⚠️ 群场景返回空串是**方向规则③的入口**: 调用方拿到空串就什么都写不了,
// 于是「把群的关系算到某个人头上」在类型上就不可能发生。
func PersonUINOfScene(sceneKey string) string {
	if SceneKindOf(sceneKey) != SceneKindPrivate {
		return ""
	}
	return strings.TrimSpace(ScenePeerOf(sceneKey))
}

// BackfillBondPerson 老数据回填: "private:<uin>" 那一行 → 人物层。
//
// ⚠️ **只能从私聊回填**。群那一行是**这个群的总量**, 它没有 uin 维度 ——
// 从群回填等于把这个群所有成员的亲密度全算到某一个人头上, 而那是编的。
// 群互动从这一刻起按发言人记账 (见 NoteSceneBond 与 recordGateOutcome)。
//
// 幂等: 逐字段取**较大的那个** —— 重复跑不会把已经累积起来的人物层打回去。
// ⚠️ `ownerUIN` 是**默认私聊的真实 uin** (配置里的 owner_qq)。
// 为什么必须显式给: 默认私聊的场景键是**字面量** private:owner,
// 它**不含真实 uin** —— 拿它当人物层的键, 群里的那个号与私聊会变成两笔账,
// 而「两笔账」正是这一层要消灭的东西
// (2026-10-16 在仿真库上实测到: 它写出了 uin=owner 那一行)。
// 拿不到真实 uin → **跳过**那个场景: 键错了比不写更糟。
func BackfillBondPerson(db *sql.DB, owner, ownerUIN string) (int, error) {
	if db == nil {
		return 0, nil
	}
	rows, err := db.Query("SELECT scene_key, closeness, regard, familiarity FROM convo_bond "+
		"WHERE owner = ? AND scene_key LIKE ?", owner, SceneKindPrivate+":%")
	if err != nil {
		return 0, err
	}
	type row struct {
		uin string
		b   Bond
	}
	var got []row
	for rows.Next() {
		var sceneKey string
		var b Bond
		if err := rows.Scan(&sceneKey, &b.Closeness, &b.Regard, &b.Familiarity); err != nil {
			rows.Close()
			return 0, err
		}
		uin := PersonUINOfScene(sceneKey)
		if uin == "owner" {
			// 字面量场景键 (默认私聊) —— 只有调用方知道真实 uin 是谁。
			uin = strings.TrimSpace(ownerUIN)
		}
		if uin != "" {
			got = append(got, row{uin: uin, b: b})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	n := 0
	for _, g := range got {
		cur, err := LoadBondPerson(db, owner, g.uin)
		if err != nil {
			return n, err
		}
		merged := Bond{
			Closeness:   max(cur.Closeness, g.b.Closeness),
			Regard:      max(cur.Regard, g.b.Regard),
			Familiarity: max(cur.Familiarity, g.b.Familiarity),
		}
		if cur.Loaded && merged == cur.Bond {
			continue // 已经不比私聊小 —— 不必写
		}
		if err := SaveBondPerson(db, owner, g.uin, merged); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// ——— 写侧 / 读侧的唯一入口 ———

// personUINForScene 场景键 → 人物层的键。
//
// ⚠️ **私聊场景键是字面量 "private:owner"**(见 store.go 的 DefaultSceneKey 与
// sceneasym.go 的注释), 它**不含真实 uin** —— 拿它当人物层的键, 群里的那个号
// 与私聊**对不上账**, 而"对得上账"正是这一层存在的全部理由。
// 所以默认私聊一律用 owner 的真实 uin (e.UserID)。
//
// 拿不到真实 uin → 返回空串 = **不写**。键错了比不写更糟:
// 它会把同一个人分裂成两笔永远不再合并的账。
func (e *Engine) personUINForScene(sceneKey string) string {
	if SceneKindOf(sceneKey) != SceneKindPrivate {
		return "" // 群场景没有单一个人 (方向规则③的入口)
	}
	if sceneKey == DefaultSceneKey {
		if e.UserID > 0 {
			return strconv.FormatInt(e.UserID, 10)
		}
		return ""
	}
	return PersonUINOfScene(sceneKey)
}

// noteBondPerson 只写**人物层**那一行 (场景那一行由调用方写, 两者的增量同源)。
//
// ⚠️ 熟悉度照旧不乘倍率 (ApplyBondScaled 的注释): 它是计数性事实。
func (e *Engine) noteBondPerson(uin string, sig BondSignal, mult float64) {
	uin = strings.TrimSpace(uin)
	if e.Store == nil || e.Store.DB == nil || uin == "" {
		return
	}
	sig.Msgs = 1
	cur, err := LoadBondPerson(e.Store.DB, e.ownerKey(), uin)
	if err != nil {
		log.Warn("convo:value", "人物层读取失败(群与私聊会各算各的): %v", err)
		return
	}
	next := ApplyBondScaled(cur.Bond, sig, mult)
	if cur.Loaded && next == cur.Bond {
		return
	}
	if err := SaveBondPerson(e.Store.DB, e.ownerKey(), uin, next); err != nil {
		log.Warn("convo:value", "人物层落库失败(群与私聊会各算各的): %v", err)
	}
}

// bondNoteForScene §5.1 **关系注入行** —— 私聊注入的是人物层那一份 (owner 定规 2026-10-16)。
//
// ⚠️ 空串 = **不注入**: `Loaded=false` 的语义是「没有记录」, 不是「陌生人」——
// 与 value.go 那条注释同规 (凭空注入会让它对新来的人莫名冷淡/莫名熟络)。
func (e *Engine) bondNoteForScene(sceneKey string) string {
	br, err := e.loadBondForScene(sceneKey)
	if err != nil || !br.Loaded {
		return ""
	}
	line, ok := BondLine(br.Bond)
	if !ok {
		return ""
	}
	return line
}

// loadBondForScene §5.1 **读侧的唯一入口**: 私聊读人物层, 群读本群那一行。
//
// ⚠️ 方向规则在这里落地, 而不是每个调用点各写一遍 ——
// 各写一遍必然有人写漏, 而写漏的表现是「私聊内容让它在群里更熟」(规则④被破),
// 那种错从日志上完全看不出来。
//
// ⚠️ 回退: 人物层还没有记录时 (老库没回填 / 刚升级) 用私聊那一行 ——
// 不假装"没有关系"。
func (e *Engine) loadBondForScene(sceneKey string) (BondRecord, error) {
	if e.Store == nil || e.Store.DB == nil {
		return BondRecord{}, nil
	}
	if uin := e.personUINForScene(sceneKey); uin != "" {
		pr, err := LoadBondPerson(e.Store.DB, e.ownerKey(), uin)
		if err != nil {
			return BondRecord{}, err
		}
		if pr.Loaded {
			return pr, nil
		}
	}
	return LoadBond(e.Store.DB, e.ownerKey(), sceneKey)
}

// BondStageSharedWithMult 与 BondStageWithMult 同规, 但**互动量那一半不乘倍率**。
//
// ⚠️ ApplyBondScaled 的注释: 熟悉度是计数性事实, 而倍率说的是「我偏不偏心你」。
// 把它也乘上去会让「主人永远第一」变成「主人的群永远最熟」—— 那是两件事。
func BondStageSharedWithMult(b Bond, mult float64) BondStage {
	byQuality := BondStageWithMult(b, mult)
	byVolume := bondStageOfValue(b.Familiarity)
	if bondStageRank(byVolume) > bondStageRank(byQuality) {
		return byVolume
	}
	return byQuality
}

// ——— 阶段: 互动量也算 ———

// BondStageShared 不带 owner 倍率的那一档判据 (= mult 1.0)。
//
// ⚠️ 它**只是同一份实现的入口**: 另写一遍必然与它分叉 (阈值 / 取高档的规则各一份)。
func BondStageShared(b Bond) BondStage { return BondStageSharedWithMult(b, 1.0) }

// 关系阶段判据的两条口径 (写在这里, 免得分叉):
//
// ⚠️ 为什么熟悉度也算 (owner 定规 2026-10-16「互动量按 uin 通用」):
// closeness 是**质** (分享私事 / 深夜 / 被冷落), familiarity 是**量** (聊过多少条)。
// 只看质的话, 一个群里聊了 400 句的人**仍然是陌生** —— 而那与「熟不熟」是反的。
//
// ⚠️ 两者共用**同一组**阈值 (0.15 / 0.40 / 0.70): 换的是**来源**, 不是尺子。
// 另起一套阈值会让「熟人」这个词有两个互相矛盾的定义。
//
// ⚠️ 判据与读侧分开: 谁读它由读侧决定 (私聊读人物层, 群读本群那一行)。

// bondStageOfValue 把一个 0..1 的量按 §5.1 的阈值折成档 (阈值只在这里出现一次)。
func bondStageOfValue(v float64) BondStage {
	switch {
	case v >= BondIntimateAt:
		return BondStageIntimate
	case v >= BondFamiliarAt:
		return BondStageFamiliar
	case v >= BondAcquaintAt:
		return BondStageAcquaint
	}
	return BondStageStranger
}

// bondStageRank 阶段的**序** (取较高档要用它 —— 阶段名不是可比较的)。
func bondStageRank(s BondStage) int {
	switch s {
	case BondStageIntimate:
		return 3
	case BondStageFamiliar:
		return 2
	case BondStageAcquaint:
		return 1
	}
	return 0
}
