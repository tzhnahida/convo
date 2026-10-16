package convo

import (
	"database/sql"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────
// §6.4.3 社交反馈闭环
//
// 闭环 = 捕获 → 归因 → 泛化成**边界记忆**。
// 现状只有 ask_share_reject 一个计数 —— 那不是闭环, 是一个开关。
//
// ⚠️ 归因的红线: 边界指向**行为**, 不指向**人**。
// 「别提工资」是边界; 「它不喜欢我提工资」是揣测。
// 揣测会变成内耗, 而内耗是陪伴体最不该有的东西 ——
// 所以 FeedbackToBoundary 生成的文本必须能通过 feedbackPersonWords 检查。
// ─────────────────────────────────────────────────────────────

// FeedbackKind 负反馈三类。
type FeedbackKind string

const (
	FeedbackNone    FeedbackKind = ""
	FeedbackScold   FeedbackKind = "scold"   // 骂
	FeedbackCorrect FeedbackKind = "correct" // 纠正
	FeedbackCold    FeedbackKind = "cold"    // 冷场
)

// FeedbackEvent 一次负反馈。
type FeedbackEvent struct {
	Kind     FeedbackKind `json:"kind"`
	Text     string       `json:"text"`
	Topic    string       `json:"topic"`
	SceneKey string       `json:"scene_key"`
	TS       string       `json:"ts"`
}

var scoldWords = []string{"闭嘴", "烦不烦", "别说了", "你懂什么", "少管闲事", "神经病", "滚"}
var correctWords = []string{"不是这样", "搞错了", "你说反了", "记错了", "不对吧", "不是这个"}

// feedbackColdMinutes 冷场阈值: 它说了话, 但这么久没人接。
const feedbackColdMinutes = 30

// DetectFeedback 捕获。宁可漏报: 把正常对话误判成「被骂」会让它变得畏缩。
func DetectFeedback(userText string, gotReply bool, minutesSilent int) (FeedbackKind, bool) {
	t := strings.TrimSpace(userText)
	for _, wd := range scoldWords {
		if strings.Contains(t, wd) {
			return FeedbackScold, true
		}
	}
	for _, wd := range correctWords {
		if strings.Contains(t, wd) {
			return FeedbackCorrect, true
		}
	}
	if !gotReply && minutesSilent >= feedbackColdMinutes {
		return FeedbackCold, true
	}
	return FeedbackNone, false
}

// feedbackPersonWords 归因红线词。边界文本命中即丢弃。
var feedbackPersonWords = []string{"他不喜欢", "讨厌我", "针对我", "嫌我", "不待见我", "他烦我", "它不喜欢"}

// FeedbackToBoundary 泛化成边界记忆。
// topic 可以为空 —— 空的时候给一条**通用**边界, 而不是猜一个话题。
func FeedbackToBoundary(kind FeedbackKind, topic string) (string, bool) {
	t := strings.TrimSpace(topic)
	var b string
	switch kind {
	case FeedbackScold:
		if t != "" {
			b = "别再主动聊「" + t + "」"
		} else {
			b = "刚那个话头不对, 别再提"
		}
	case FeedbackCorrect:
		if t != "" {
			b = "「" + t + "」这件事我说错过, 以后先确认再说"
		} else {
			b = "不确定的事先别下结论"
		}
	case FeedbackCold:
		if t != "" {
			b = "「" + t + "」这个话题接不住, 换个方向"
		} else {
			b = "没人接的话头就放下, 别追"
		}
	default:
		return "", false
	}
	for _, wd := range feedbackPersonWords {
		if strings.Contains(b, wd) {
			return "", false
		}
	}
	return b, true
}

// FeedbackPromoteThreshold 同类反馈累计到这个次数才升格成**边界**。
// ⚠️ 一次就升格等于「它被说一句就再也不提了」—— 那是畏缩, 不是长记性。
const FeedbackPromoteThreshold = 3

// EnsureFeedbackSchema 建表。计数与边界同表: 边界是计数的**终态**, 分开存会有一天不一致。
func EnsureFeedbackSchema(db *sql.DB) error {
	_, err := db.Exec("CREATE TABLE IF NOT EXISTS convo_feedback (" +
		"owner TEXT NOT NULL, scene_key TEXT NOT NULL, kind TEXT NOT NULL, " +
		"topic TEXT NOT NULL DEFAULT '', n INTEGER NOT NULL DEFAULT 0, " +
		"promoted INTEGER NOT NULL DEFAULT 0, updated_ts TEXT NOT NULL DEFAULT '', " +
		"PRIMARY KEY (owner, scene_key, kind, topic))")
	return err
}

// RecordFeedback 记一次并返回累计次数与是否已升格。
func RecordFeedback(db *sql.DB, owner, sceneKey string, kind FeedbackKind, topic string) (int, error) {
	topic = strings.TrimSpace(topic)
	_, err := db.Exec("INSERT INTO convo_feedback (owner, scene_key, kind, topic, n, promoted, updated_ts) "+
		"VALUES (?,?,?,?,1,0,?) ON CONFLICT(owner, scene_key, kind, topic) DO UPDATE SET n = n + 1, updated_ts = excluded.updated_ts",
		owner, sceneKey, string(kind), topic, time.Now().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	var n int
	if err := db.QueryRow("SELECT n FROM convo_feedback WHERE owner=? AND scene_key=? AND kind=? AND topic=?",
		owner, sceneKey, string(kind), topic).Scan(&n); err != nil {
		return 0, err
	}
	if n >= FeedbackPromoteThreshold {
		_, _ = db.Exec("UPDATE convo_feedback SET promoted = 1 WHERE owner=? AND scene_key=? AND kind=? AND topic=?",
			owner, sceneKey, string(kind), topic)
	}
	return n, nil
}

// BoundaryStrengthAt §7.4: 边界**降权而非永久禁止** —— 见 forget.go。
//
// ⚠️ 没有这一层, 一条边界会永久生效, 而设计稿明说「否则越骂越畏缩」。
// 软化的边界照样注入, 只是不再是硬线。
func FeedbackBoundaryLineAt(boundary, strength string) (string, bool) {
	b := strings.TrimSpace(boundary)
	if b == "" {
		return "", false
	}
	switch strength {
	case "hard":
		return "【边界】" + b + "。**这是一条硬线**, 不是建议。", true
	case "soft":
		return "【边界·旧】" + b + "。(以前定过, 现在还作数, 但不必如临大敌)", true
	}
	return "", false // none: 已经不算边界了
}

// LoadBoundaryLines 读回边界并**按 §7.4 的衰减档渲染**。
//
// ⚠️ 这是提示词实际吃的那一份 (`value.go` → `DialogInput.Boundaries`)。
// 它比"读原始边界"多一层衰减: hard 是硬线, soft 是旧边界, none 干脆不注入。
func (s *Store) LoadBoundaryLines(owner, sceneKey string, now time.Time) []string {
	rows, err := s.DB.Query("SELECT kind, topic, COALESCE(updated_ts,'') FROM convo_feedback "+
		"WHERE owner=? AND scene_key=? AND promoted = 1", owner, sceneKey)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var kind, topic, at string
		if err := rows.Scan(&kind, &topic, &at); err != nil {
			continue
		}
		b, ok := FeedbackToBoundary(FeedbackKind(kind), topic)
		if !ok {
			continue
		}
		line, ok := FeedbackBoundaryLineAt(b, BoundaryStrengthAt(at, now))
		if !ok {
			continue // none: 过期了, 一条永久硬线会让它在这个话题上永远闭嘴
		}
		out = append(out, line)
	}
	return out
}
