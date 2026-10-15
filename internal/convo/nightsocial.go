package convo

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────
// §3.7 夜间多 AI 社会模拟 (加速时钟)
//
// 一句话: **它同时在「两个群 + 多个好友」里活动, 而它和别的 AI 的那些对话,
// 是在夜里一次性算出来的。**
//
// ⚠️ 四条最容易写错的地方:
//   1. **不许「预演明天」** —— 只补算刚过去的那一天。用户的现实 > 它的虚构。
//   2. **预算耗尽就直接散会** —— 「话没说完就散了」本身就是真实的。
//   3. **来源归属硬卡 ai_said** —— 否则它会把自己和别的 AI 的经历说成「你上次说…」。
//   4. **失败 / 超时 → 当晚整批丢弃** —— 不留半天的记录, 错位的时间线不可修复。
// ─────────────────────────────────────────────────────────────

const (
	NightSocialBudget      = 100
	NightGroupSharePct     = 65
	NightWindowStartH      = 8
	NightWindowEndH        = 23
	NightMaxRoundsPerScene = 40
)

// NightSessionKind 会话类型。可见性由它决定: 群 = 公开层, 私聊 = 成对私密。
type NightSessionKind string

const (
	NightSessionGroup NightSessionKind = "group"
	NightSessionDM    NightSessionKind = "dm"
)

// IsPublic 这一层的产物能不能被**别的 AI 看到、互相引用**。
//
// ⚠️ **层决定归属** (2026-10-14 定):
//
//	· 群 (公开层) —— 说过的话大家都听见了, 所以**可以互相说、互相引用**;
//	· 私聊 (成对私密) —— 只进**当事双方的私事**, 别人看不见, 也不许被引用。
//
// 与 §3.4 一致: 每个 AI 只能通过**看到的公开行为**推测同伴状态, 不读内部状态;
// 私聊内容出现在公开场合 = 全知叙事。
func (k NightSessionKind) IsPublic() bool { return k == NightSessionGroup }

// NightPeer 夜间会话里的另一个 AI。
type NightPeer struct {
	UIN    int64
	Name   string
	Brief  string
	Weight float64
}

// NightSession 一个会话通道。
type NightSession struct {
	Kind  NightSessionKind
	Scene string
	Peers []NightPeer
}

// NightBudgetLine 一条预算分配。
type NightBudgetLine struct {
	Scene  string
	Kind   NightSessionKind
	Rounds int
}

// AllocateNightBudget 把总预算分到各会话。
//
// ⚠️ **总和不得超过 total**。但一侧没有任何会话时, 整份预算归另一侧 ——
// 那不是「把没说完的话补完」(那仍然禁止), 而是: 没有人接收的份额如果扔掉,
// 只有一个群的夜晚就只会用掉 65% 的预算, 那是分配表的 bug, 不是设计意图。
func AllocateNightBudget(total int, sessions []NightSession) []NightBudgetLine {
	if total <= 0 {
		total = NightSocialBudget
	}
	if len(sessions) == 0 {
		return nil
	}
	group := total * NightGroupSharePct / 100
	dm := total - group
	var groups, dms []int
	for i, s := range sessions {
		if s.Kind == NightSessionGroup {
			groups = append(groups, i)
		} else {
			dms = append(dms, i)
		}
	}
	if len(groups) > 0 && len(dms) == 0 {
		group, dm = total, 0
	}
	if len(dms) > 0 && len(groups) == 0 {
		group, dm = 0, total
	}
	alloc := make([]int, len(sessions))
	if len(groups) > 0 {
		per := group / len(groups)
		for _, i := range groups {
			alloc[i] = per
		}
	}
	if len(dms) > 0 {
		totalW := 0.0
		for _, i := range dms {
			w := 0.0
			for _, p := range sessions[i].Peers {
				w += p.Weight
			}
			if w <= 0 {
				w = 1
			}
			totalW += w
		}
		for _, i := range dms {
			w := 0.0
			for _, p := range sessions[i].Peers {
				w += p.Weight
			}
			if w <= 0 {
				w = 1
			}
			alloc[i] = int(float64(dm) * w / totalW)
		}
	}
	var out []NightBudgetLine
	for i, s := range sessions {
		n := alloc[i]
		if n > NightMaxRoundsPerScene {
			n = NightMaxRoundsPerScene
		}
		if n <= 0 {
			continue // 分不到就不开会, 不四舍五入凑一个
		}
		out = append(out, NightBudgetLine{Scene: s.Scene, Kind: s.Kind, Rounds: n})
	}
	return out
}

// SpreadNightTurns 把 n 轮散布到当天 08:00-23:00。
//
// ⚠️ 用 DayRoll (确定性) 而不是 Dice: 同一批对话每次重算必须落在同一时刻,
// 否则「补算昨日记忆」会得到与昨天写入的不一致的时间线。
func SpreadNightTurns(day string, n int, seedKey string) []time.Time {
	if n <= 0 {
		return nil
	}
	start, ok := ParseDay(day)
	if !ok {
		return nil
	}
	winMin := (NightWindowEndH - NightWindowStartH) * 60
	slot := winMin / n
	if slot < 1 {
		slot = 1
	}
	out := make([]time.Time, 0, n)
	for i := 0; i < n; i++ {
		jitter := DayRoll(fmt.Sprintf("night:%s:%s:%d", seedKey, day, i), slot)
		m := NightWindowStartH*60 + i*slot + jitter
		if m >= NightWindowEndH*60 {
			m = NightWindowEndH*60 - 1
		}
		out = append(out, start.Add(time.Duration(m)*time.Minute))
	}
	return out
}

// NightTurn 一轮夜间发言。
type NightTurn struct {
	SceneKey string
	Kind     NightSessionKind
	Sender   string
	Text     string
	TS       time.Time
}

// NightSocialInput 一次夜间批算的输入。
type NightSocialInput struct {
	Night    string
	SelfName string
	Sessions []NightSession
	Budget   int
	Now      time.Time
	// ForbiddenNames 用户的名字。护栏 2: **不许编造用户参与过的对话**。
	ForbiddenNames []string
}

// NightSocialResult 批算结果。
type NightSocialResult struct {
	Turns   []NightTurn
	Spent   int
	Budget  int
	Skipped bool
	Reason  string
}

// NightGenerate 单轮生成函数 (测试可注入)。
type NightGenerate func(ctx context.Context, system, user string) (string, error)

// NightTurnSystemPrompt 对面那一句的系统提示。
func NightTurnSystemPrompt(self string, sess NightSession, peer NightPeer) string {
	where := "群里"
	if sess.Kind == NightSessionDM {
		where = "私聊里"
	}
	return "你是 " + self + "。现在是深夜, 你在" + where + "和别的 AI 朋友聊天, 这些对话发生在今天白天。\n" +
		"对面是 " + peer.Name + ":" + peer.Brief + "\n" +
		"严格输出一句话 (1-20 字), 像发微信。不要旁白, 不要括号, 不要解释。\n" +
		"硬线: ①**不许提到任何真人**, 尤其不许说「你说过」「你上次」—— 真人不在场;" +
		"②不许把梦当事实; ③不要问问题求回应。" + NowLine()
}

// nightSelfSystemPrompt 轮到**它自己**说话时的系统提示。
func nightSelfSystemPrompt(self string, sess NightSession, peer NightPeer) string {
	where := "群里"
	if sess.Kind == NightSessionDM {
		where = "私聊里"
	}
	return "你是 " + self + "。现在是深夜, 你在" + where + "和 AI 朋友聊天, 这些对话发生在今天白天。\n" +
		"对面是 " + peer.Name + ":" + peer.Brief + "\n" +
		"接住上面最后一句, 说一句话 (1-20 字), 像发微信。不要旁白, 不要括号。\n" +
		"硬线: ①**不许提到任何真人**; ②不许把梦当事实; ③不要问问题求回应。" + NowLine()
}

// RunNightSocial 跑一次夜间多 AI 社交模拟。
//
// ⚠️ **任何一步失败 → 整批丢弃** (Skipped=true, Turns=nil)。
// 留半天记录比不留更糟: 时间线会错位, 而错位不可修复。
func RunNightSocial(ctx context.Context, gen NightGenerate, in NightSocialInput) NightSocialResult {
	if gen == nil || len(in.Sessions) == 0 {
		return NightSocialResult{Skipped: true, Reason: "no_session"}
	}
	day := in.Night
	if strings.TrimSpace(day) == "" {
		return NightSocialResult{Skipped: true, Reason: "no_night_key"}
	}
	lines := AllocateNightBudget(in.Budget, in.Sessions)
	if len(lines) == 0 {
		return NightSocialResult{Skipped: true, Reason: "no_budget"}
	}
	budget := in.Budget
	if budget <= 0 {
		budget = NightSocialBudget
	}
	var turns []NightTurn
	spent := 0
	for _, ln := range lines {
		if spent >= budget {
			break // 跑到即停 —— **不许把没说完的话补完**
		}
		sess := findSession(in.Sessions, ln.Scene)
		if sess == nil || len(sess.Peers) == 0 {
			continue
		}
		stamps := SpreadNightTurns(day, ln.Rounds, ln.Scene)
		for r := 0; r < ln.Rounds; r++ {
			if spent >= budget {
				break
			}
			peer := sess.Peers[r%len(sess.Peers)]
			hist := nightHistory(turns, ln.Scene, 6)
			// ⚠️ **必须轮替说话人**。只生成对面那一句的话:
			//   ① 写回的判据是「它有没有接话」→ 永远拿不到东西 → 一条记忆都写不回;
			//   ② 那也不是对话, 是它单方面挨了一百句。
			speaker := peer.Name
			sys := NightTurnSystemPrompt(in.SelfName, *sess, peer)
			if r%2 == 1 {
				speaker = in.SelfName
				sys = nightSelfSystemPrompt(in.SelfName, *sess, peer)
			}
			out, err := gen(ctx, sys, hist)
			if err != nil {
				return NightSocialResult{Skipped: true, Reason: err.Error(), Budget: budget}
			}
			txt, ok := CleanNightTurn(out)
			if !ok {
				continue // 空/越界的一轮直接不要, 不占预算
			}
			if NightMentionsForbidden(txt, in.ForbiddenNames) {
				continue // §3.7.5 护栏 2
			}
			ts := time.Time{}
			if r < len(stamps) {
				ts = stamps[r]
			}
			turns = append(turns, NightTurn{SceneKey: ln.Scene, Kind: ln.Kind, Sender: speaker, Text: txt, TS: ts})
			spent++
		}
	}
	return NightSocialResult{Turns: turns, Spent: spent, Budget: budget}
}

func findSession(all []NightSession, scene string) *NightSession {
	for i := range all {
		if all[i].Scene == scene {
			return &all[i]
		}
	}
	return nil
}

func nightHistory(turns []NightTurn, scene string, n int) string {
	var b strings.Builder
	cnt := 0
	for i := len(turns) - 1; i >= 0 && cnt < n; i-- {
		if turns[i].SceneKey != scene {
			continue
		}
		b.WriteString(turns[i].Sender + ": " + turns[i].Text + "\n")
		cnt++
	}
	return b.String()
}

// nightForbiddenPhrases 夜间发言的禁区句式。
var nightForbiddenPhrases = []string{"我梦到", "我梦见", "你上次说", "你说过", "我跟你说过"}

// CleanNightTurn 清洗一轮产出。空 / 过长 / 多行 / 命中禁区 → 丢弃。
func CleanNightTurn(raw string) (string, bool) {
	t := strings.TrimSpace(raw)
	t = strings.Trim(t, "\"「」")
	if t == "" {
		return "", false
	}
	if len([]rune(t)) > 60 {
		return "", false // 一句话的上限 —— 超了就是论述, 那是报告不是聊天
	}
	if strings.Contains(t, "\n") {
		return "", false
	}
	for _, wd := range nightForbiddenPhrases {
		if strings.Contains(t, wd) {
			return "", false
		}
	}
	return t, true
}

// NightMentionsForbidden 护栏 2: 不许编造用户参与过的对话。
func NightMentionsForbidden(text string, names []string) bool {
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if strings.Contains(text, n) {
			return true
		}
	}
	return false
}

// NightMemory 一条要写回的记忆。
type NightMemory struct {
	SceneKey string
	Text     string
	// At 这一轮**发生在当天的什么时候** (§3.7.1 的时间散布)。
	//
	// ⚠️ **这一格原来是缺的, 而它让整条散布机制白算。**
	//
	// `SpreadNightTurns` 把 100 轮按合理时刻铺到 08:00-23:00, `NightTurn.TS` 也带着它 ——
	// 而 `NightWriteBack` 组装 `NightMemory` 时**把它丢了**。
	// 于是 §3.7.1 那句「次日看起来就是昨天白天聊的」**没有任何东西记着"什么时候"**:
	// 写回的记忆里既没有时刻, 也没有时段词。
	//
	// ⚠️ 零值 = **未提供**, 不是"午夜" —— 见 NightPeriodWord。
	At     time.Time
	Source string // **恒 ai_said** (来源归属硬性要求)
	// Public 这条产物来自公开层 (群) 吗。
	// ⚠️ 它决定这条**能不能被别的 AI 引用** —— 私聊内容进公开场合 = 全知叙事 (§3.4)。
	Public bool
}

// NightWriteBack 逐场景写回。
//
// 规则 (§3.7.4):
//   - 群聊里的公共内容 → 群公共记忆 (所有成员可见)
//   - 私聊内容 → 只有对话双方 (本系统只跑自己参与的那几个会话, 天然满足)
//   - **与它无关的对话 → 直接丢弃** (不是所有对话都值得记)
//
// ⚠️ 「与它无关」的判据是**它有没有接话**: 一个会话里它一句没说,
// 那段对话对它就只是背景音。所以只收「它说的话」+「它接话前的那一句」。
func NightWriteBack(res NightSocialResult, selfName string, maxPerScene int) []NightMemory {
	if res.Skipped || len(res.Turns) == 0 {
		return nil
	}
	if maxPerScene <= 0 {
		maxPerScene = 6
	}
	byScene := map[string][]NightTurn{}
	var order []string
	for _, t := range res.Turns {
		if _, ok := byScene[t.SceneKey]; !ok {
			order = append(order, t.SceneKey)
		}
		byScene[t.SceneKey] = append(byScene[t.SceneKey], t)
	}
	sort.Strings(order)
	var out []NightMemory
	for _, scene := range order {
		turns := byScene[scene]
		kept := 0
		for i := 0; i < len(turns) && kept < maxPerScene; i++ {
			if turns[i].Sender != selfName {
				continue
			}
			pub := turns[i].Kind.IsPublic()
			if i > 0 {
				prev := turns[i-1]
				out = append(out, NightMemory{SceneKey: scene, Text: prev.Sender + ": " + prev.Text,
					At: prev.TS, Source: "ai_said", Public: pub})
				kept++
			}
			out = append(out, NightMemory{SceneKey: scene, Text: selfName + ": " + turns[i].Text,
				At: turns[i].TS, Source: "ai_said", Public: pub})
			kept++
		}
	}
	return out
}

// NightPeriodWord 把散布出来的时刻说成一个**时段词**。
//
// ⚠️ 为什么要折成词而不是存时刻: 记忆注入那一侧只给一句话,
// 而「昨天下午」比「2026-10-15 14:23」更像人会说的东西 —— 也短得多。
//
// ⚠️ **零值必须返回空串**(零值 = 未提供 ≠ 午夜): 折成"凌晨"会让每一条
// 没有时刻的写回都谎报成深夜 —— 与 BondSignalsFrom 那个 `Hour() >= 0` 是同一个坑。
func NightPeriodWord(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	switch h := at.Hour(); {
	case h < 6:
		return "凌晨"
	case h < 9:
		return "早上"
	case h < 12:
		return "上午"
	case h < 14:
		return "中午"
	case h < 18:
		return "下午"
	case h < 21:
		return "傍晚"
	default:
		return "晚上"
	}
}

// NightMemoryLine 写回记忆时用的那一行 —— **带上时段**。
//
// ⚠️ 这就是 §3.7.1「次日看起来就是昨天白天聊的」的落点:
// 没有它, 那些记忆在提示词里是**没有时间**的一段对话。
func NightMemoryLine(m NightMemory) string {
	w := NightPeriodWord(m.At)
	if w == "" {
		return m.Text // 没有时刻就不编一个
	}
	return "昨天" + w + " — " + m.Text
}

// ⚠️ 这里原来有一个 `NightDisablesNightSocial` —— **已删**。
//
// 它只是 `DriftDisablesNightSocial` 的**改名包装**, 而生产走的是后者
// (`registerDriftDir` + 夜间批里的那道闸门)。
//
// ⚠️ 留着它的坏处不是"多一个函数": 它让**同一个判据有两个名字**,
// 而下一个找"语域变硬时谁在挡"的人会 grep 到**没有调用者的那一个** ——
// 于是看起来像"闸门没接线"。

// ── 同伴名册 (手工维护, 落库以便管理页写入) ────────────────
//
// ⚠️ **裁定 (2026-10-13): 名册手填, 不派生。**
//
//	从别处「推导」出几个 AI 等于凭空造人 —— 名册是身份数据, 不是可算出来的量。
//	落库而不是放配置结构里, 是为了让管理页能写。
func EnsureNightPeerSchema(db *sql.DB) error {
	_, err := db.Exec("CREATE TABLE IF NOT EXISTS convo_night_peer (" +
		"uin INTEGER PRIMARY KEY, name TEXT NOT NULL, brief TEXT NOT NULL DEFAULT '', " +
		"weight REAL NOT NULL DEFAULT 1)")
	return err
}

// SaveNightPeer 写一个同伴。
func (s *Store) SaveNightPeer(p NightPeer) error {
	if p.UIN == 0 || strings.TrimSpace(p.Name) == "" {
		return nil
	}
	w := p.Weight
	if w <= 0 {
		w = 1
	}
	_, err := s.DB.Exec("INSERT OR REPLACE INTO convo_night_peer (uin, name, brief, weight) VALUES (?,?,?,?)",
		p.UIN, p.Name, p.Brief, w)
	return err
}

// NightRosterPlan 夜里**实际**会用的名册 + 它从哪来 —— **唯一口径**。
//
// ⚠️ 2026-10-15 修 (#11)。控制台原来只读手工表(convo_night_peer), 而生产库那张表是空的,
// 夜间批走的是**世界观 cast**(5 人)。于是控制台永远显示"缺名册", 而它昨晚**实跑了 71 轮**。
//
// **读数撒谎比没有读数更糟**: 你会照着它去修一个根本没坏的东西。
//
// ⚠️ 取用顺序必须与夜间批**完全一致**(世界观 cast → 配置 → 手工表)。
// 两处各写一遍就会漂开, 而漂开的症状正是这一条本身 —— 所以只有一个函数, 两边都调它。
//
// ⚠️ world 由调用方传入而不是这里生成: 生成世界观是**副作用**(会调模型、会落库),
// 而这个口子也要给**只读**的控制台用。控制台传 nil, 夜间批传 rosterFromWorld 的结果。
func NightRosterPlan(st *Store, owner string, world, configured []NightPeer) (peers []NightPeer, source string) {
	if st == nil {
		return nil, "none"
	}
	if len(world) > 0 {
		return world, "cast"
	}
	if b, ok, err := st.LoadWorldBible(owner); err == nil && ok {
		if r := NightPeersFromCast(b); len(r) > 0 {
			return r, "cast"
		}
	}
	if len(configured) > 0 {
		return configured, "configured"
	}
	if ps, err := st.LoadNightPeers(); err == nil && len(ps) > 0 {
		return ps, "manual"
	}
	return nil, "none"
}

// nightPeerNames 名册里的人名 (控制台用: 只看个数看不出"是不是真的换了人")。
func nightPeerNames(ps []NightPeer) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return out
}

// LoadNightPeers 读名册 (按权重降序, 权重相同按 uin)。
func (s *Store) LoadNightPeers() ([]NightPeer, error) {
	rows, err := s.DB.Query("SELECT uin, name, brief, weight FROM convo_night_peer ORDER BY weight DESC, uin ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NightPeer
	for rows.Next() {
		var p NightPeer
		if err := rows.Scan(&p.UIN, &p.Name, &p.Brief, &p.Weight); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
