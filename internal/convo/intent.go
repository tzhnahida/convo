package convo

import (
	"database/sql"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────
// §7.5 双轨存储
//
// fallback 轨: 拿不到用户意图时它自己编一个 (已有, kind=fallback)。
// intent 轨:  用户**明确说了**意图时留下的规范文本。
//
// ⚠️ 「双轨」的全部内容在于两条轨的**去向不同**:
//   intent   → 只喂回上下文 (下一轮的「它之前说过要…」)
//   fallback → 参与检索排序
// 如果 intent 也参与检索, 它就会和 fallback 抢同一个位置, 双轨退化成单轨 ——
// 而且是更糟的单轨: 用户的原话会被当成它的推测一样对待。
// 所以本文件导出 IntentRetrievable() 作为一个**常量式的分界线**,
// 让「不许检索」这件事有一个可以被测试、也可以被 grep 到的落点。
// ─────────────────────────────────────────────────────────────

// IntentTrack 一条意图轨记录。
type IntentTrack struct {
	Raw       string `json:"raw"`       // 用户原话
	Canonical string `json:"canonical"` // 规范文本 (去口语噪声)
	Kind      string `json:"kind"`      // plan | preference | fact | boundary
	TS        string `json:"ts"`
}

// IntentKind 四类。
const (
	IntentPlan       = "plan"
	IntentPreference = "preference"
	IntentFact       = "fact"
	IntentBoundary   = "boundary"
)

// IntentCanonicalMaxRunes 规范文本上限。超长的一律截断 ——
// 它是喂回上下文的, 一条顶别人三条会把 L1 挤爆。
const IntentCanonicalMaxRunes = 60

var intentPlanWords = []string{"明天", "后天", "下周", "下个月", "打算", "准备", "计划", "要去", "约了"}
var intentPrefWords = []string{"我喜欢", "我不喜欢", "我讨厌", "我爱", "我最怕", "我受不了", "我习惯"}

// ⚠️ **裸「别」不许进表**: 「别急」「别客气」「别忘了带伞」都会命中,
// 于是一句日常寒暄会被升格成「硬线, 不许试探」。边界词必须是**祈使 + 对象**的形状。
var intentBoundWords = []string{"不要", "不许", "不准", "别再", "别在", "以后别", "别再提", "别提"}

// ClassifyIntentKind 规则分类。默认 fact —— 认不出来就当事实记着,
// 宁可少一层语义, 不可猜。
func ClassifyIntentKind(raw string) string {
	t := strings.TrimSpace(raw)
	if t == "" {
		return ""
	}
	// 长度闸门: 5 个字符以下的一律当不了意图 —— 认不出来就当事实记着 (宁可少一层语义)。
	if len([]rune(t)) < 5 {
		return IntentFact
	}
	for _, wd := range intentBoundWords {
		if strings.Contains(t, wd) {
			return IntentBoundary
		}
	}
	for _, wd := range intentPlanWords {
		if strings.Contains(t, wd) {
			return IntentPlan
		}
	}
	for _, wd := range intentPrefWords {
		if strings.Contains(t, wd) {
			return IntentPreference
		}
	}
	return IntentFact
}

// intentFillers 口语噪声。去掉它们才算「规范」。
var intentFillers = []string{"那个", "就是", "然后", "其实", "反正", "你知道吧", "怎么说呢"}

// CanonicalIntent 原话 → 规范文本。
// ⚠️ 空结果返回 false: 一条去掉噪声后什么都不剩的话, 不是意图, 是口气。
func CanonicalIntent(raw, kind string) (string, bool) {
	t := strings.TrimSpace(raw)
	if t == "" {
		return "", false
	}
	for _, f := range intentFillers {
		t = strings.ReplaceAll(t, f, "")
	}
	t = strings.TrimSpace(t)
	if t == "" {
		return "", false
	}
	r := []rune(t)
	if len(r) > IntentCanonicalMaxRunes {
		t = string(r[:IntentCanonicalMaxRunes])
	}
	if kind == IntentPlan || kind == IntentBoundary {
		return t, true
	}
	return t, true
}

// IntentRetrievable 双轨的分界线: intent 轨**永远不参与检索**。
// 返回常量而不是字段 —— 这条不许被配置化, 配置化就是给它留后门。
func IntentRetrievable() bool { return false }

// IntentLine 意图 → 注入段。
// ⚠️ 措辞里点明「是 TA 说过的」: 用户原话必须与它自己的推测在提示里可区分,
// 否则模型会把两者混成一回事, 而这条正是双轨存在的理由。
func IntentLine(it IntentTrack) (string, bool) {
	c := strings.TrimSpace(it.Canonical)
	if c == "" {
		return "", false
	}
	switch it.Kind {
	case IntentPlan:
		return "【TA 说过的·打算】" + c + "。可以自然地问一句进展, **不要催**。", true
	case IntentPreference:
		return "【TA 说过的·偏好】" + c + "。记住它, 别踩。", true
	case IntentBoundary:
		return "【TA 说过的·边界】" + c + "。**这是硬线, 不许试探**。", true
	}
	return "【TA 说过的】" + c + "。", true
}

// ── 持久化 ──────────────────────────────────────────────

// EnsureIntentSchema 建表。
// ⚠️ 与 convo_memory **刻意不同表**: 双轨如果共用一张表, 总有一天有人会给它加一个
// 检索用的 score 列, 于是 intent 悄悄变成了可检索 —— 分表是这条约定的物理形态。
func EnsureIntentSchema(db *sql.DB) error {
	_, err := db.Exec("CREATE TABLE IF NOT EXISTS convo_intent (" +
		"id INTEGER PRIMARY KEY AUTOINCREMENT, " +
		"owner TEXT NOT NULL, scene_key TEXT NOT NULL, " +
		"raw TEXT NOT NULL DEFAULT '', canonical TEXT NOT NULL DEFAULT '', " +
		"kind TEXT NOT NULL DEFAULT '', ts TEXT NOT NULL DEFAULT '')")
	return err
}

// SaveIntent 落一条意图轨。空 canonical 直接丢 (它不是意图, 是口气)。
func SaveIntent(db *sql.DB, owner, sceneKey string, it IntentTrack) error {
	c := strings.TrimSpace(it.Canonical)
	if c == "" {
		return nil
	}
	if it.TS == "" {
		it.TS = time.Now().Format(time.RFC3339)
	}
	_, err := db.Exec("INSERT INTO convo_intent (owner, scene_key, raw, canonical, kind, ts) VALUES (?,?,?,?,?,?)",
		owner, sceneKey, it.Raw, c, it.Kind, it.TS)
	return err
}

// LoadIntents 读最近的意图轨 (喂回上下文用)。
func LoadIntents(db *sql.DB, owner, sceneKey string, limit int) ([]IntentTrack, error) {
	if limit <= 0 {
		limit = 5
	}
	rows, err := db.Query("SELECT raw, canonical, kind, ts FROM convo_intent "+
		"WHERE owner = ? AND scene_key = ? ORDER BY id DESC LIMIT ?", owner, sceneKey, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IntentTrack
	for rows.Next() {
		var it IntentTrack
		if err := rows.Scan(&it.Raw, &it.Canonical, &it.Kind, &it.TS); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}
