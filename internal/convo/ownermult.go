package convo

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/tzhnahida/VocabLock/internal/clock"
	"github.com/tzhnahida/VocabLock/internal/log"
)

// ─────────────────────────────────────────────────────────────
// §5.7 **owner 的手动关系倍率** (设计稿 L1301, 2026-10-13 定)
//
// 「owner 可在管理页给任一 uin 设**关系倍率**(默认 1.0, **上限 5×**)——
// 用于保证"**主人永远第一**"。」
//
// 作用有三: ①亲密度/敬意的**积累速度** ×M ②**热力上限**按倍率后的阶段算
// (**仅私聊场景**) ③偏向权重 ×M。
//
// ⚠️ 它存在的理由是**一条真实的生产症状**: 三层接线都修好之后,
// ai_opens 仍然是 0 —— 因为私聊场景的 closeness 从 0 出发,
// 而"不向陌生人掏心窝子"把 cap 压在 20, 买不起一条 25 的主动消息。
// 设计稿留的杠杆就是这个倍率; 没有它, owner 只能**等**关系量自然涨上去。
//
// ⚠️ **五条护栏**(设计稿原话), 每一条都落在下面的代码里:
//
//	1. 不改变即时一致性 —— 关系仍靠互动积累, 倍率只让它更快;
//	2. 对外不可见 —— 不许在群里表现为特殊待遇;
//	3. 脆弱展现不受倍率影响 —— 噩梦/深夜求助硬绑 owner(那一条不在本文件范围内);
//	4. 上限 5×;
//	5. **倍率不进群场景** —— 上面 ② 只作用于私聊。
// ─────────────────────────────────────────────────────────────

// OwnerMultDefault 没设过时的倍率。**1.0, 不是 0** ——
// 零值当成 0 倍会让"没配置"变成"把关系积累关掉"。
const OwnerMultDefault = 1.0

// OwnerMultCap 护栏 4: 上限 5×。
//
// ⚠️ 理由写在设计稿里, 值得抄一遍: 「防止调成**舔狗模式**
// (那不叫偏爱, 叫**没有人格**)」。
const OwnerMultCap = 5.0

// EnsureOwnerMultSchema 建表。
func EnsureOwnerMultSchema(s *Store) error {
	if s == nil || s.DB == nil {
		return fmt.Errorf("ownermult: store 未初始化")
	}
	_, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS convo_owner_mult (
		owner TEXT NOT NULL, uin TEXT NOT NULL, mult REAL NOT NULL DEFAULT 1,
		updated_ts TEXT DEFAULT '', PRIMARY KEY (owner, uin)
	)`)
	return err
}

// SetOwnerMult 设一个 uin 的关系倍率。
//
// ⚠️ **在这里 clamp, 不在读的时候 clamp**: 存进去的就是生效的那个数,
// 否则"管理页显示 8×, 实际按 5× 算"—— 一个只有对着两处看才发现的差。
// ⚠️ 下限也是 1.0: 倍率只会**加快**, 不许拿来打压某个号。
func (s *Store) SetOwnerMult(owner, uin string, m float64) (float64, error) {
	if s == nil || s.DB == nil {
		return 0, fmt.Errorf("ownermult: store 未初始化")
	}
	uin = strings.TrimSpace(uin)
	if uin == "" {
		return 0, fmt.Errorf("ownermult: uin 为空")
	}
	if m < OwnerMultDefault {
		m = OwnerMultDefault
	}
	if m > OwnerMultCap {
		m = OwnerMultCap
	}
	_, err := s.DB.Exec(`INSERT INTO convo_owner_mult (owner, uin, mult, updated_ts)
		VALUES (?,?,?,?)
		ON CONFLICT(owner, uin) DO UPDATE SET mult=excluded.mult, updated_ts=excluded.updated_ts`,
		owner, uin, m, clock.TodayTS())
	return m, err
}

// OwnerMult 读一个 uin 的倍率。**没设过 / 读不到 → 1.0**。
//
// ⚠️ 失败往 1.0 倒(不是往 0 倒, 也不是往 5 倒): 它是"额外加速",
// 读不到就该回到"没有加速"的基线 —— 那正是升级前的行为。
func (s *Store) OwnerMult(owner, uin string) float64 {
	if s == nil || s.DB == nil {
		return OwnerMultDefault
	}
	var m float64
	if err := s.DB.QueryRow(`SELECT mult FROM convo_owner_mult WHERE owner=? AND uin=?`,
		owner, strings.TrimSpace(uin)).Scan(&m); err != nil {
		if err != sql.ErrNoRows {
			log.Warn("convo:ownermult", "倍率读库失败(按 1.0 处理): %v", err)
		}
		return OwnerMultDefault
	}
	if m < OwnerMultDefault {
		return OwnerMultDefault
	}
	if m > OwnerMultCap {
		return OwnerMultCap
	}
	return m
}

// OwnerMultForScene 一个场景适用的倍率。**群场景恒 1.0**(护栏 5)。
//
// ⚠️ 护栏 5 的理由值得抄: 「§6.10.7 要求不许在群里暴露 owner 身份,
// 而"有 owner 的群它更话多"是一个**可观测的统计差别** ——
// 那等于把关系用一个行为量公示出去」。
func (e *Engine) OwnerMultForScene(sceneKey string) float64 {
	if e.Store == nil || SceneKindOf(sceneKey) != SceneKindPrivate {
		return OwnerMultDefault
	}
	return e.Store.OwnerMult(e.ownerKey(), peerUINOf(sceneKey))
}

// peerUINOf 私聊场景键里的那个 uin。
func peerUINOf(sceneKey string) string {
	return strings.TrimPrefix(strings.TrimSpace(sceneKey), SceneKindPrivate+":")
}

// ScaledBondDelta 把一次关系量的变化按倍率放大 (**护栏 1: 只改速度, 不改方向**)。
//
// ⚠️ 负向信号**同样被放大** —— "只放大好事"等于给它开了一个不会记仇的外挂,
// 而那不是"更亲近", 是"没有人格"的另一面。
func ScaledBondDelta(dc, dr, mult float64) (float64, float64) {
	if mult <= 0 {
		mult = OwnerMultDefault
	}
	if mult > OwnerMultCap {
		mult = OwnerMultCap
	}
	return dc * mult, dr * mult
}

// BondStageWithMult 按倍率后的亲密度判阶段 (设计稿作用 ②)。
//
// ⚠️ 它**只用来定热力上限**, 不改关系量本身 —— 也就是
// 「按倍率后的**阶段**算 cap」, 而不是"把亲密度乘上去存起来"。
// 后者会让"昨天还陌生今天很热"变成真的 —— 那正是护栏 1 禁止的。
func BondStageWithMult(b Bond, mult float64) BondStage {
	if mult <= 0 {
		mult = OwnerMultDefault
	}
	if mult > OwnerMultCap {
		mult = OwnerMultCap
	}
	scaled := b
	scaled.Closeness = clamp01(b.Closeness * mult)
	return BondStageOf(scaled)
}
