package convo

import (
	"database/sql"
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// 角色位**长出来** (取代原来的 DayRoll 分配)

// 「真人的位置是处出来的, 不是分出来的。」
//
// 原来的做法: `AssignGroupRole` 用 DayRoll(persona|scene) 一次算出一个常量。
// 它确定性、可测 —— 但它永远不会变, 所以它不是"位", 是"标签"。
//
// ⚠️ 三条不许破:
//   1. **样本不足不下结论** —— 新群里的位先给种子值, 处久了才长出来。
//      这条与"冷落判定"用的是同一个纪律: 说了一次没被接, 那不叫冷落。
//   2. **档位表, 不是连续漂移** —— 位是可数的几档, 不是一个小数。
//   3. **变得慢** —— 阈值判据, 不是滑动的权重。一次被反驳不该改位。
// ─────────────────────────────────────────────────────────────

const (
	// RoleMinSpoke 长出位所需的最少开口次数。
	// ⚠️ 低于它一律回落到**种子位** —— 不是"沉默位", 是"还没处出来"。
	RoleMinSpoke = 8
	// RoleFollowUpWindow 它说完之后多久内收到的消息算"接它的话"。
	RoleFollowUpWindow = 30 * time.Minute
)

// RoleStats 一个场景里它**实际做过什么**。全是事实计数, 没有派生量。
type RoleStats struct {
	Spoke        int
	Reacted      int
	Silent       int
	RepliedTo    int    // 说完之后有人接
	Contradicted int    // 说完之后被顶
	LastSpokeTS  string // 用来判定"下一条是不是接它的"
}

// EnsureRoleSchema 建表。
func EnsureRoleSchema(db *sql.DB) error {
	_, err := db.Exec("CREATE TABLE IF NOT EXISTS convo_role (" +
		"owner TEXT NOT NULL, scene_key TEXT NOT NULL, " +
		"spoke INTEGER NOT NULL DEFAULT 0, reacted INTEGER NOT NULL DEFAULT 0, " +
		"silent INTEGER NOT NULL DEFAULT 0, replied_to INTEGER NOT NULL DEFAULT 0, " +
		"contradicted INTEGER NOT NULL DEFAULT 0, " +
		"last_spoke_ts TEXT NOT NULL DEFAULT '', updated_ts TEXT NOT NULL DEFAULT '', " +
		"PRIMARY KEY (owner, scene_key))")
	return err
}

// BumpRoleStat 记一笔。字段名由**白名单**决定 —— 拼字符串进 SQL 是注入面。
func (s *Store) BumpRoleStat(owner, sceneKey, field string, delta int) error {
	col := ""
	switch field {
	case "spoke", "reacted", "silent", "replied_to", "contradicted":
		col = field
	default:
		return nil // 未知字段一律不写, 不报错 (调用方的笔误不该炸掉一条群消息)
	}
	_, err := s.DB.Exec("INSERT INTO convo_role (owner, scene_key, "+col+", updated_ts) VALUES (?,?,?,?) "+
		"ON CONFLICT(owner, scene_key) DO UPDATE SET "+col+" = "+col+" + excluded."+col+", updated_ts = excluded.updated_ts",
		owner, sceneKey, delta, clock.TodayTS())
	return err
}

// MarkRoleSpoke 记下"它刚说完"的时刻 —— 下一条消息据此判"接没接"。
func (s *Store) MarkRoleSpoke(owner, sceneKey string, at time.Time) error {
	_, err := s.DB.Exec("INSERT INTO convo_role (owner, scene_key, last_spoke_ts, updated_ts) VALUES (?,?,?,?) "+
		"ON CONFLICT(owner, scene_key) DO UPDATE SET last_spoke_ts = excluded.last_spoke_ts, updated_ts = excluded.updated_ts",
		owner, sceneKey, at.Format(bodyTSLayout), clock.TodayTS())
	return err
}

// ClearRoleSpoke 清掉标记 (一轮窗口只用一次, 否则一次开口会被反复算成"被接")。
func (s *Store) ClearRoleSpoke(owner, sceneKey string) error {
	_, err := s.DB.Exec("UPDATE convo_role SET last_spoke_ts = '' WHERE owner = ? AND scene_key = ?", owner, sceneKey)
	return err
}

// LoadRoleStats 读账本。第二个返回值 = **有没有记录** (零值 ≠ 全零统计)。
func (s *Store) LoadRoleStats(owner, sceneKey string) (RoleStats, bool, error) {
	var st RoleStats
	row := s.DB.QueryRow("SELECT spoke, reacted, silent, COALESCE(replied_to,0), COALESCE(contradicted,0), "+
		"COALESCE(last_spoke_ts,'') FROM convo_role WHERE owner = ? AND scene_key = ?", owner, sceneKey)
	if err := row.Scan(&st.Spoke, &st.Reacted, &st.Silent, &st.RepliedTo, &st.Contradicted, &st.LastSpokeTS); err != nil {
		if err == sql.ErrNoRows {
			return st, false, nil
		}
		return st, false, err
	}
	return st, true, nil
}

// DeriveRole 从账本长出位。**纯函数** —— 阈值表, 不是连续漂移。
//
// seed = 种子位 (新群/样本不足时用它)。
// ⚠️ 返回 seed 的条件是**样本不足**, 不是"看不出倾向" —— 这两件事不一样:
// 前者是"还没处出来", 后者是"处出来了, 就是个普通人"。
func DeriveRole(st RoleStats, seed GroupRole) GroupRole {
	if st.Spoke < RoleMinSpoke {
		return seed
	}
	speakRate := float64(st.Spoke) / float64(st.Spoke+st.Silent+st.Reacted+1)
	replyRate := float64(st.RepliedTo) / float64(st.Spoke)
	contraRate := float64(st.Contradicted) / float64(st.Spoke)
	switch {
	case contraRate >= 0.40 && speakRate >= 0.15:
		return GroupRoleContrarian // 开口不少、且常被顶 → 杠精
	case replyRate >= 0.50 && speakRate >= 0.25:
		return GroupRoleAnchor // 开口有人接 → 组织者
	case speakRate < 0.15:
		return GroupRoleLurker // 基本不说话 → 潜水
	case st.RepliedTo == 0:
		return GroupRoleLurker // 说了没人接 → 也是潜水 (它自己会收敛)
	}
	return GroupRoleFoiler // 说、也有人接, 但不多 → 捧哏
}

// GroupRoleOfStore 与 Engine.RoleOf **同一条判据**, 但只依赖 Store ——
// 给没有引擎实例的读数面用 (观察台/管理页)。顺序逐字一致: 账本处出来的优先, 没有就用种子。
func GroupRoleOfStore(s *Store, sceneKey string) GroupRole {
	if s == nil || strings.TrimSpace(sceneKey) == "" {
		return GroupRoleNone
	}
	p, err := s.LoadProfile()
	if err != nil || p == nil {
		return GroupRoleNone
	}
	seed := AssignGroupRole(p.Name, sceneKey)
	st, ok, err := s.LoadRoleStats(s.OwnerKey(), sceneKey)
	if err != nil || !ok {
		return seed
	}
	return DeriveRole(st, seed)
}

// RoleOf 它在**这个群**里的位: 处出来的优先, 没处出来就用种子。
func (e *Engine) RoleOf(personaKey, sceneKey string) GroupRole {
	seed := AssignGroupRole(personaKey, sceneKey)
	if e.Store == nil {
		return seed
	}
	st, ok, err := e.Store.LoadRoleStats(e.ownerKey(), sceneKey)
	if err != nil {
		// 静默的不是数据库, 是调用方: 读失败会让位**永远停在种子**, 而看不出是坏了。
		log.Warn("convo:role", "角色账本读取失败, 本轮用种子位: %v", err)
		return seed
	}
	if !ok {
		return seed
	}
	return DeriveRole(st, seed)
}
