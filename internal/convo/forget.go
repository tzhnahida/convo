package convo

import (
	"database/sql"
	"strings"
	"time"

	"github.com/tzhnahida/VocabLock/internal/log"
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
var forgetPhrases = []string{
	"别记我", "别记住我", "不要记我", "忘了我", "把我忘掉", "别记了",
	// 「忘了吧 / 忘掉吧」这类**没有宾语**的说法也要收 —— 判据刻意宽松:
	// 误判的代价是"少记了一点", 漏判的代价是"用户说过别记我而它还在记"。
	"忘了吧", "忘掉吧", "忘掉这些", "别记这些", "不要记这些",
	"删掉我的", "删除我的记忆", "别存我的", "不要存我",
}

// ForgetRequested 这句话是不是在行使遗忘权。
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
