package convo

import (
	"database/sql"
	"strings"

	"github.com/tzhnahida/convo/internal/clock"
)

// ─────────────────────────────────────────────────────────────
// 教练机: owner 直接教它
//
// 它是**唯一**能改写它行为的通道 —— 别的反馈都是统计 (被接话率、负反馈计数),
// 那些只能告诉它"少说点", 说不出「别说你刷屏这种话」。
//
// ⚠️ 四条纪律:
//   ① **只有 owner 能教** —— 群里任何人都能改写它 = 谁都能把它变成另一个人;
//   ② **纠正落库并注入** —— 存了不注入 = 白存 (与 §2.3「现编必须落库再用」同理);
//   ③ **有上限** —— 规则太多会撑爆提示词, 而且互相矛盾时它只能猜;
//   ④ **只改行为, 不改人格** —— 「别刷屏」是行为; 「你要温柔一点」是人格,
//      那条得走人格编辑, 走这里会让它精神分裂。
// ─────────────────────────────────────────────────────────────

const (
	// CoachMaxRules 注入的规则上限。
	// ⚠️ 超出时**丢最旧的** —— 最近被纠正的才是它现在最该记住的。
	CoachMaxRules = 12
	// coachPrefixes owner 下指令时用的前缀。
	CoachPrefixHits = "教练"
)

// coachPrefixes 触发前缀 (中英各一, 都要带冒号免得误伤正常说话)。
var coachPrefixes = []string{"教练:", "教练：", "coach:", "coach："}

// ParseCoachCommand 这句话是不是一条教练指令。返回指令本体。
//
// ⚠️ **必须带冒号** —— 不带的话「教练机真好玩」会被当成指令。
func ParseCoachCommand(text string) (string, bool) {
	t := strings.TrimSpace(text)
	for _, pre := range coachPrefixes {
		if strings.HasPrefix(strings.ToLower(t), pre) {
			body := strings.TrimSpace(t[len(pre):])
			if body == "" {
				return "", false // 空指令 = 没提供内容, 不是"提供了一条空规则"
			}
			return body, true
		}
	}
	return "", false
}

// CoachRule 一条纠正。
type CoachRule struct {
	ID    int64  `json:"id"`
	TS    string `json:"ts"`
	Text  string `json:"text"`  // owner 的原话
	Scope string `json:"scope"` // "" = 全局; 否则 scene_key
}

// EnsureCoachSchema 建表。
func EnsureCoachSchema(db *sql.DB) error {
	_, err := db.Exec("CREATE TABLE IF NOT EXISTS convo_coach (" +
		"id INTEGER PRIMARY KEY AUTOINCREMENT, owner TEXT NOT NULL DEFAULT '', " +
		"ts TEXT NOT NULL DEFAULT '', text TEXT NOT NULL DEFAULT '', " +
		"scope TEXT NOT NULL DEFAULT '', active INTEGER NOT NULL DEFAULT 1)")
	return err
}

// AddCoachRule 记一条纠正。
func (s *Store) AddCoachRule(owner, text, scope string) (int64, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, nil
	}
	res, err := s.DB.Exec("INSERT INTO convo_coach (owner, ts, text, scope, active) VALUES (?,?,?,?,1)",
		owner, clock.Now().Format(bodyTSLayout), text, scope)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// CoachRules 取生效的规则。**最近的在前, 超上限丢最旧的。**
func (s *Store) CoachRules(owner, sceneKey string) ([]CoachRule, error) {
	rows, err := s.DB.Query("SELECT id, COALESCE(ts,''), COALESCE(text,''), COALESCE(scope,'') "+
		"FROM convo_coach WHERE owner = ? AND active = 1 AND (scope = '' OR scope = ?) "+
		"ORDER BY id DESC LIMIT ?", owner, sceneKey, CoachMaxRules)
	if err != nil {
		// 表还没建 = 还没人教过它。**这不是错误。**
		return nil, nil
	}
	defer rows.Close()
	var out []CoachRule
	for rows.Next() {
		var r CoachRule
		if rows.Scan(&r.ID, &r.TS, &r.Text, &r.Scope) == nil {
			out = append(out, r)
		}
	}
	return out, nil
}

// ClearCoachRules 全部停用 (owner 想重新开始教)。
func (s *Store) ClearCoachRules(owner string) (int, error) {
	res, err := s.DB.Exec("UPDATE convo_coach SET active = 0 WHERE owner = ? AND active = 1", owner)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// coachNote 取这个场景生效的规则并渲染。读失败 → 空 (退回没被教过的状态, 不炸)。
func (e *Engine) coachNote(sceneKey string) string {
	if e.Store == nil {
		return ""
	}
	rules, err := e.Store.CoachRules(e.ownerKey(), sceneKey)
	if err != nil {
		return ""
	}
	return CoachNote(rules)
}

// CoachNote 把规则渲染成注入文本。
//
// ⚠️ 措辞是**"它对你说过"**, 不是"系统要求" —— 后者会让它变成客服 (在念条款)。
func CoachNote(rules []CoachRule) string {
	if len(rules) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n【ta 亲口纠正过你的】(按它自己说的照做, 别问为什么)\n")
	// 倒序: 库里是"最近在前", 但人读起来是从早到晚更顺。
	for i := len(rules) - 1; i >= 0; i-- {
		t := strings.TrimSpace(rules[i].Text)
		if t == "" {
			continue
		}
		b.WriteString("- ")
		b.WriteString(t)
		b.WriteString("\n")
	}
	return b.String()
}

// CoachAck owner 下完指令后它的回应。
//
// ⚠️ 是**人话确认**, 不是"已记录" —— 后者立刻暴露了这是个系统。
func CoachAck(text string) string {
	switch {
	case strings.Contains(text, "别说"), strings.Contains(text, "不要"), strings.Contains(text, "别"):
		return "知道了 不说了"
	}
	return "记住了"
}
