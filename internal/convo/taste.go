package convo

import (
	"database/sql"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────
// §6.3 判据① 品味轨迹 / 判据④ 冷场
//
// 判据①: 「它对什么话题有话想说」。
// ⚠️ 未决问题: embedding 能不能上裁决路径。规格倾向**不允许** ——
// 决策器不接模型。所以这里用**共现计数**: 某话题出现 N 次, 它接了几次。
// 可数、可回归、能在 trace 里读出来 —— 这三条比「更准」重要。
//
// 判据④: 冷场。它需要「这个场景上一条消息是什么时候」, 也就是**时钟**。
// 而 DecideValue 是纯函数, 不许碰时钟 —— 所以差值在引擎侧算好注入。
// 本文件把那次计算抽成 SceneColdGap, 好让「新场景不是冷场」这种边界可测。
// ─────────────────────────────────────────────────────────────

// TasteHit 一个话题的接话统计。
type TasteHit struct {
	Topic string
	Seen  int
	Spoke int
}

// TasteMinSeen 样本下限。
// ⚠️ 又是「零值 = 未提供」: 看过 1 次就断言「它爱聊这个」是噪声,
// 而噪声驱动判据①会让它每轮都对同一个话题兴奋。
const TasteMinSeen = 3

// TasteOf 接话率 (0..1)。样本不足 → ok=false。
func TasteOf(hits []TasteHit, topic string) (float64, bool) {
	for _, h := range hits {
		if h.Topic != topic {
			continue
		}
		if h.Seen < TasteMinSeen {
			return 0, false
		}
		return float64(h.Spoke) / float64(h.Seen), true
	}
	return 0, false
}

// TasteHitThreshold 接话率超过它才算「有品味」。
const TasteHitThreshold = 0.4

// SceneColdGap 判据④ 的读数。
//
//	has     = 这个场景**有过**上一条消息 (没有 = 新场景, 不是冷场)
//	valid   = 落在 [min,max] 之间, 可以算「冷场」命中
//	ceiling = 超过 max —— 已经凉透了, 不是「刚好冷场」
//
// ⚠️ 三个量必须分开: 把「没上一条」和「上一条很久以前」合成一个 0 值,
// 会让每一个新群的第一条消息都触发「冷场救场」。
func SceneColdGap(last time.Time, hasLast bool, now time.Time, min, max time.Duration) (gap time.Duration, has, valid, ceiling bool) {
	if !hasLast || last.IsZero() {
		return 0, false, false, false
	}
	gap = now.Sub(last)
	if gap < 0 {
		gap = 0
	}
	if max > 0 && gap > max {
		return gap, true, false, true
	}
	if gap < min {
		return gap, true, false, false
	}
	return gap, true, true, false
}

// ParseMsgTS 消息时间戳解析。
//
// ⚠️ **生产写的是 bodyTSLayout ("2006-01-02 15:04:05"), 不是 RFC3339。**
// 只按 RFC3339 解析会**静默失败** → 返回 has=false → 判据④ 永不触发,
// 而它编译过、测试绿 (测试自己插的是 RFC3339 才绿)。
// 两种都认, 是为了兼容测试夹具与任何历史行。
func ParseMsgTS(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.ParseInLocation(bodyTSLayout, s, time.Local); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// EnsureTasteSchema 建表。
func EnsureTasteSchema(db *sql.DB) error {
	_, err := db.Exec("CREATE TABLE IF NOT EXISTS convo_taste (" +
		"owner TEXT NOT NULL, topic TEXT NOT NULL, seen INTEGER NOT NULL DEFAULT 0, " +
		"spoke INTEGER NOT NULL DEFAULT 0, " +
		"PRIMARY KEY (owner, topic))")
	return err
}

// RecordTaste 记一次话题出现 (spoke = 它这一轮是否接了话)。
func RecordTaste(db *sql.DB, owner, topic string, spoke bool) error {
	if topic == "" {
		return nil
	}
	inc := 0
	if spoke {
		inc = 1
	}
	_, err := db.Exec("INSERT INTO convo_taste (owner, topic, seen, spoke) VALUES (?,?,1,?) "+
		"ON CONFLICT(owner, topic) DO UPDATE SET seen = seen + 1, spoke = spoke + excluded.spoke",
		owner, topic, inc)
	return err
}

// LoadTaste 读某人的品味表。
func LoadTaste(db *sql.DB, owner string) ([]TasteHit, error) {
	rows, err := db.Query("SELECT topic, seen, spoke FROM convo_taste WHERE owner = ?", owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TasteHit
	for rows.Next() {
		var h TasteHit
		if err := rows.Scan(&h.Topic, &h.Seen, &h.Spoke); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// ColdGapMin / ColdGapMax 判据④ 的窗口。
// 下限是"还没到冷场", 上限是"已经凉透了" —— 三段语义必须分开 (见 SceneColdGap)。
const (
	ColdGapMin = time.Hour
	ColdGapMax = 8 * time.Hour
)

// LastSceneMessageAtExcluding 与 LastSceneMessageAt 相同, 但**跳过"刚刚这一条"**。
//
// ⚠️ 为什么非要有它: 调用方是先落库再裁决的 (InsertMessageFull → GateGroupMessage)。
// 直接取最新一条, 差值恒为 0, 判据④**永远不会命中** ——
// 而它看起来完全正常: 编译过、测试绿、就是从不触发。
// 「留着一个永不触发的闸门比不写更糟」, 这里是同一个病的读数版。
//
// 排除规则是**精确**的 (文本相同 **且** 10 秒内), 不是"跳过最新一条":
// 后者在连续两条同文本、或调用方改了顺序时会静默错位。
func LastSceneMessageAtExcluding(db *sql.DB, sceneKey, selfText string, now time.Time) (time.Time, bool, error) {
	if sceneKey == "" {
		return time.Time{}, false, nil
	}
	rows, err := db.Query("SELECT ts, payload_json FROM convo_messages "+
		"WHERE scene_key = ? AND origin <> 'ai_to_ai' ORDER BY id DESC LIMIT 10", sceneKey)
	if err != nil {
		return time.Time{}, false, err
	}
	defer rows.Close()
	self := strings.TrimSpace(selfText)
	for rows.Next() {
		var ts, payload string
		if err := rows.Scan(&ts, &payload); err != nil {
			return time.Time{}, false, err
		}
		t, ok := ParseMsgTS(ts)
		if !ok {
			continue
		}
		if self != "" && strings.TrimSpace(payload) == self && now.Sub(t) < 10*time.Second {
			continue // 就是刚插进来的这一条
		}
		return t, true, nil
	}
	return time.Time{}, false, rows.Err()
}
