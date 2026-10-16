package convo

import (
	"database/sql"
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// §7.4 遗忘与权利
//
// 三条:
//   · 衰减: 久不用的记忆降权, 但**边界类降权而非永久禁止** (否则越骂越畏缩);
//   · 可撤回: 边界/梗/偏好/阈值变化全部可见可撤;
//   · **遗忘权: 任何人说「别记我」→ 物理删除该主体的全部记忆,
//     且不可被它的社会学习逆转。**
//
// ⚠️ 第三条里「不可被逆转」是**承重的**, 也是最容易漏的一半:
// 只删一次的话, 下一轮会话提取会**再学一遍**, 而且它学得更安静 ——
// 用户看到的是「我明明说过别记我」。所以删除必须配一块**墓碑**。
// ─────────────────────────────────────────────────────────────

// forgetPhrases 遗忘请求的判据词。
//
// ⚠️ **宁可宽**: 误判的代价是"少记了一点", 漏判的代价是"用户说过别记我而它还在记"。
// 这两件事不对称, 所以这张表刻意宽松。
// ⚠️ **句式表分成两栏 —— 因为这两栏的动作完全不同。**
//
// 设计稿 L1582/L1859 只给了一句话:
//
//	> **遗忘权**: 任何人说"**别记我**" → **物理删除**该主体的全部记忆,
//	> 且不可被它的社会学习逆转。
//
// ⚠️ 而 `ForgetSubject` 做的是**物理删除 + 立碑**(碑一立, 这个人**从此**不再被记)。
// 于是原来那句免责说明 ——「误判的代价是"少记了一点"」—— **在删除面前是错的**:
//
//	误判一次 = 这个人**永久地**从它的记忆里消失, 而且不会恢复。
//
// ⇒ 所以分两栏:
//
//	· `forgetErasePhrases` —— **明确指向"记"**的说法(设计稿那一句及其同族)
//		  → 走**删除 + 立碑**;
//	· `forgetSoftPhrases`  —— **可能指别的事**的说法(「忘了吧」可以是在说某件具体的事)
//		  → **只记日志, 不删**。
//
// ⚠️ 这个分栏**是我的判断, 不是设计稿写的** —— 设计稿只规定了「别记我」那一条。
// 我选了**保守**那一侧(少删), 并把它记进排障记录等 owner 拍板。
var forgetErasePhrases = []string{
	"别记我", "别记住我", "不要记我", "别记了",
	"忘了我", "把我忘掉", "删掉我的", "删除我的记忆", "别存我的", "不要存我",
}

// forgetSoftPhrases 含糊的 —— 只记日志, **绝不删**。
var forgetSoftPhrases = []string{
	"忘了吧", "忘掉吧", "忘掉这些", "别记这些", "不要记这些",
}

// forgetPhrases 两栏合起来 —— `ForgetRequested` 用它(语义不变)。
var forgetPhrases = append(append([]string{}, forgetErasePhrases...), forgetSoftPhrases...)

// ForgetEraseRequested 这句话是不是**明确指向"记"**的遗忘请求 ——
// 也就是**唯一**该走"物理删除 + 立碑"那一档。
//
// ⚠️ 它与 `ForgetRequested` 的差别就是两栏的差别(见上面的说明):
// 后者含含糊糊的那些也认, 而**含糊的话不该触发不可逆的删除**。
func ForgetEraseRequested(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	for _, p := range forgetErasePhrases {
		if strings.Contains(t, p) {
			return true
		}
	}
	return false
}

// ForgetRequested 这句话是不是在行使遗忘权 (两栏都算)。
//
// ⚠️ 判据刻意宽松**是对的**: 漏判的代价是"用户说过别记我而它还在记"。
// 而**动作**那一侧才需要收窄 —— 那是 `ForgetEraseRequested` 的事。
func ForgetRequested(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	for _, p := range forgetPhrases {
		if strings.Contains(t, p) {
			return true
		}
	}
	return false
}

// EnsureForgetSchema 建墓碑表。
func EnsureForgetSchema(db *sql.DB) error {
	_, err := db.Exec("CREATE TABLE IF NOT EXISTS convo_forget (" +
		"uin TEXT PRIMARY KEY, ts TEXT NOT NULL DEFAULT '')")
	return err
}

// ForgetReport 删了什么 —— **必须能被看见**: 一次静默的删除和一次没发生过的删除
// 长得一模一样, 而这件事上"以为删了"是最坏的结果。
type ForgetReport struct {
	UIN      string         `json:"uin"`
	Deleted  map[string]int `json:"deleted"`
	Total    int            `json:"total"`
	Tombston string         `json:"tombstone_ts"`
}

// ForgetSubject 物理删除一个主体的全部记忆, 并立碑。
//
// 「主体」= 一个 uin。私聊场景键是 private:<uin>, 场景级表按它删;
// 人物级表 (convo_memory_person) 按 uin 列删。
func (s *Store) ForgetSubject(uin string, at time.Time) (ForgetReport, error) {
	u := strings.TrimSpace(uin)
	rep := ForgetReport{UIN: u, Deleted: map[string]int{}, Tombston: at.Format(bodyTSLayout)}
	if u == "" {
		return rep, nil
	}
	scene := SceneKeyPrivate(u)
	type job struct {
		table string
		query string
		args  []interface{}
	}
	jobs := []job{
		{"convo_memory_person", "DELETE FROM convo_memory_person WHERE uin = ?", []interface{}{u}},
		{"convo_memory_scene", "DELETE FROM convo_memory_scene WHERE scene_key = ?", []interface{}{scene}},
		{"convo_memory", "DELETE FROM convo_memory WHERE scene_key = ?", []interface{}{scene}},
		{"convo_feedback", "DELETE FROM convo_feedback WHERE scene_key = ?", []interface{}{scene}},
		{"convo_bond", "DELETE FROM convo_bond WHERE scene_key = ?", []interface{}{scene}},
		{"convo_intent", "DELETE FROM convo_intent WHERE scene_key = ?", []interface{}{scene}},
		{"convo_challenge", "DELETE FROM convo_challenge WHERE text <> '' AND 1=0", nil}, // 占位: 该表无主体列
		// ⚠️ 原始对白也算记忆。留着它等于「删了索引但留着正文」。
		{"convo_messages", "DELETE FROM convo_messages WHERE scene_key = ?", []interface{}{scene}},
	}
	for _, j := range jobs {
		res, err := s.DB.Exec(j.query, j.args...)
		if err != nil {
			// 表可能不存在 (老库 / 未迁移) —— 记下来, 不中断其余删除。
			// 中断会让"删了一半"变成常态, 而半删是最难发现的状态。

			log.Warn("convo:forget", "遗忘权: %s 删除失败 (%v)", j.table, err)
			continue
		}
		n, _ := res.RowsAffected()
		rep.Deleted[j.table] = int(n)
		rep.Total += int(n)
	}
	// §7.5 **血缘 → 撤回**: 群公共层里由这个主体说的那些条也要删。
	//
	// ⚠️ 上面那条 convo_memory_scene 只按**私聊**场景键删; 而群公共层是
	// **大家看得到**的那一侧 —— 遗忘权漏掉它 = 「删了私聊, 留下满群都在说的那句」。
	//
	// ⚠️ 逐条读血缘 (SceneMemoryOrigin) 而不是 WHERE origin_uin=?:
	// 注入路径里根本没有 uin (scene.go 开头第 1 条), 血缘是**审计专用**的读数 ——
	// 走审计口, 「谁能读它」就只剩一个入口。
	// ⚠️ 代价: 每个群最多 200 条。遗忘权是人手触发的, 这条代价可以接受。
	if rows, qerr := s.DB.Query(`SELECT scene_key FROM convo_scene WHERE kind = 'group'`); qerr == nil {
		var sceneKeys []string
		for rows.Next() {
			var sk string
			if rows.Scan(&sk) == nil && strings.TrimSpace(sk) != "" {
				sceneKeys = append(sceneKeys, sk)
			}
		}
		_ = rows.Close()
		pulled := 0
		for _, sk := range sceneKeys {
			ms, merr := s.SceneMemories(sk, 200)
			if merr != nil {
				continue
			}
			for _, m := range ms {
				if origin, oerr := s.SceneMemoryOrigin(m.MemID); oerr != nil || origin != u {
					continue
				}
				if _, derr := s.DB.Exec(`DELETE FROM convo_memory_scene WHERE mem_id = ? AND scene_key = ?`, m.MemID, sk); derr == nil {
					pulled++
				}
			}
		}
		if pulled > 0 {
			rep.Deleted["convo_memory_scene(群公共层血缘)"] = pulled
			rep.Total += pulled
			log.Warn("convo:forget", "§7.5 群公共层按血缘撤回 %d 条 (origin_uin=%s)", pulled, u)
		}
	}
	// ⚠️ **墓碑**: 没有它, 下一轮学回来 —— 而那正是「不可被逆转」要防的。
	if _, err := s.DB.Exec("INSERT OR REPLACE INTO convo_forget (uin, ts) VALUES (?,?)", u, rep.Tombston); err != nil {
		return rep, err
	}
	s.InvalidateMemoryBatch()
	log.Warn("convo:forget", "§7.4 遗忘权: uin=%s 物理删除 %d 行并立碑", u, rep.Total)
	return rep, nil
}

// SubjectForgotten 该主体是否已行使过遗忘权。
func (s *Store) SubjectForgotten(uin string) bool {
	u := strings.TrimSpace(uin)
	if u == "" {
		return false
	}
	var ts string
	if err := s.DB.QueryRow("SELECT ts FROM convo_forget WHERE uin = ?", u).Scan(&ts); err != nil {
		return false
	}
	return true
}

// ForgetScene 从场景键反推主体是否已被遗忘 (给写入闸门用)。
func (s *Store) ForgetScene(sceneKey string) bool {
	if SceneKindOf(sceneKey) != SceneKindPrivate {
		return false
	}
	return s.SubjectForgotten(strings.TrimPrefix(sceneKey, SceneKindPrivate+":"))
}

// ── §7.4 第一条: 边界**降权而非永久禁止** ─────────────────

const (
	// BoundaryHardDays 边界作为**硬线**的时长。
	BoundaryHardDays = 14
	// BoundarySoftDays 之后软化; 超过它不再注入。
	BoundarySoftDays = 60
)

// BoundaryStrengthAt 边界此刻的强度档: hard | soft | none。
//
// ⚠️ 设计稿专门写了这一条的理由: 「**否则越骂越畏缩**」。
// 一条永久硬线会让它在这个话题上永远闭嘴 —— 那不是长记性, 是创伤。
func BoundaryStrengthAt(promotedAt string, now time.Time) string {
	t, ok := ParseMsgTS(promotedAt)
	if !ok {
		return "hard" // 读不出来 → 按还在保护期处理 (保守: 少犯错)
	}
	age := now.Sub(t)
	switch {
	case age < BoundaryHardDays*24*time.Hour:
		return "hard"
	case age < BoundarySoftDays*24*time.Hour:
		return "soft"
	}
	return "none"
}
