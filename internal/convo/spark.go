package convo

import (
	"database/sql"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/tzhnahida/VocabLock/internal/clock"
)

// ─────────────────────────────────────────────────────────────
// §2.6 灵光: 创意的产生、筛选与主动分享
//
// 「语言模型的创意本质是**重组**, 不是无中生有。」
// 所以本节只解决两件事: **留存** 与 **碰撞**。
//
// ⚠️ 三条写死的硬门槛:
//   1. **没有素材就没有灵光** (L197) —— 禁止「空转生成金句」。
//      三路素材 (夜间碎片/被顶/联想) 全空时, 骰子掷中也不生成。
//   2. **status 不落库** —— 它是 heat + 时间的纯函数。
//      落了库就必须回答「谁在什么时候重算它」(body.go 的教训原文)。
//   3. **沉降不是删除** (L224) —— 没人接的灵光 heat 衰减, 但行留在库里,
//      将来还能被联想触发。删除 = 永久失去那个方向。
// ─────────────────────────────────────────────────────────────

// SparkKind 五种。零值 "" = **未提供 → 不入库** (不许猜一个 kind)。
type SparkKind string

const (
	SparkObservation SparkKind = "observation"
	SparkConnection  SparkKind = "connection"
	SparkOpinion     SparkKind = "opinion"
	SparkJoke        SparkKind = "joke"
	SparkIdea        SparkKind = "idea"
)

// Valid 白名单 (同 memSource 的严格模式: 未知值拒收, 不走 default)。
func (k SparkKind) Valid() bool {
	switch k {
	case SparkObservation, SparkConnection, SparkOpinion, SparkJoke, SparkIdea:
		return true
	}
	return false
}

// SparkSource 素材来历。
// ⚠️ HasRef 零值 = **没有素材 → 禁止生成** (L197)。
// 它同时是硬线②「不许把组合当事实」的凭据: 只有 memory 来源才可以说「我看过」。
type SparkSource struct {
	Kind   string // memory | learned | world_event | dialog | dream
	RefID  string
	Quote  string // 素材原文片段 (owner 审查用; **不进 prompt**)
	HasRef bool
}

// Spark **只存事实**。heat 的档位与 status 全部现算。
type Spark struct {
	ID      int64
	BornTS  string
	Kind    SparkKind
	Content string
	Source  SparkSource
	// HeatDelta **只存累计增量** (事件驱动), 不存当前值。
	HeatDelta float64
	// LastTouchTS 最近一次被接住的时刻 —— heat 衰减的起点。
	LastTouchTS string
	// SaidTS 第一次说出去的时刻 ("" = 还没说)。
	SaidTS  string
	TalkKey string
}

// ── heat 的现算 ───────────────────────────────────────────

// SparkHeatHalfLifeH 热度的半衰期 (小时)。
// ⚠️ 用**时间衰减**, 不是每日清零 —— 每日清零 = 每天满血 = 每日金句播报 (S8)。
const SparkHeatHalfLifeH = 72

// SparkHeatInput heat 现算的输入。
type SparkHeatInput struct {
	HeatDelta   float64
	LastTouchTS string
	Now         time.Time
}

// SparkHeat 现算。**永不归零到 -∞** —— 沉降不是删除 (L224)。
func SparkHeat(in SparkHeatInput) float64 {
	h := in.HeatDelta
	if h < 0 {
		h = 0
	}
	if strings.TrimSpace(in.LastTouchTS) == "" {
		return h
	}
	t, ok := ParseMsgTS(in.LastTouchTS)
	if !ok {
		return h
	}
	now := in.Now
	if now.IsZero() {
		now = clock.Now()
	}
	hours := now.Sub(t).Hours()
	if hours <= 0 {
		return h // 时钟回拨不许把热度**加**上去
	}
	return h * math.Pow(0.5, hours/SparkHeatHalfLifeH)
}

// SparkStatus 生命周期档位。
type SparkStatus string

const (
	SparkFresh   SparkStatus = "fresh"
	SparkWarm    SparkStatus = "warm"
	SparkSaid    SparkStatus = "said"
	SparkSettled SparkStatus = "settled"
	SparkDropped SparkStatus = "dropped"
)

// SparkStatusConfig 档位表 (档跃函数, 不是连续权重)。
type SparkStatusConfig struct {
	FreshAt   float64
	WarmAt    float64
	DropBelow float64
}

// DefaultSparkStatusConfig 默认档位。阈值需仿真定档 (设计稿只给了方向)。
func DefaultSparkStatusConfig() SparkStatusConfig {
	return SparkStatusConfig{FreshAt: 0.7, WarmAt: 0.4, DropBelow: 0.2}
}

// SparkStatusAt 现算档位。
func SparkStatusAt(in SparkHeatInput, said bool, cfg SparkStatusConfig) SparkStatus {
	if cfg.FreshAt <= 0 {
		cfg = DefaultSparkStatusConfig()
	}
	h := SparkHeat(in)
	switch {
	case h >= cfg.FreshAt:
		return SparkFresh
	case h >= cfg.WarmAt:
		return SparkWarm
	case said:
		return SparkSettled // 说过了, 冷掉了 → 沉降
	case h < cfg.DropBelow:
		return SparkDropped // 没说过, 冷掉了 → 放下 (行仍在库里)
	}
	return SparkWarm
}

// ── §2.6.3 什么时候会迸发 ─────────────────────────────────

// SparkDailyMax 日产上限 (L202: ≤2-3 个)。落在**夜间 Job 的循环上界**, 不是概率。
const SparkDailyMax = 3

// SparkBurstInput 三个时机 (L194-196)。
type SparkBurstInput struct {
	DreamMaterial []Memory // 夜间整合: 当天碎片
	// HasDream ⚠️ 少了它, 忘了填的调用方会**静默关掉整个主要产出口** (S2)。
	HasDream       bool
	Challenged     string   // 被顶: 用户反驳的那句话 ("" = 没被顶)
	Association    []Memory // 联想: 外部刺激 × 旧记忆
	HasAssociation bool
	Roll           int
	HasRoll        bool
}

// SparkBurstConfig 迸发参数。
type SparkBurstConfig struct {
	BurstBelow int // roll < BurstBelow → 迸发; <=0 → 45
}

// SparkBurstDecision 迸发判定结果。
type SparkBurstDecision struct {
	Kind   SparkKind
	Source SparkSource
	Reason string
}

// DecideBurst 纯函数。**没有素材 → 恒返回 false** (L197 硬线①)。
//
// ⚠️ 素材判定在骰子**之前**: 反过来写的话, 「空转生成金句」只会在**大多数时候**被挡住。
func DecideBurst(in SparkBurstInput, cfg SparkBurstConfig) (SparkBurstDecision, bool) {
	if cfg.BurstBelow <= 0 {
		cfg.BurstBelow = 45
	}
	hasDream := in.HasDream && len(in.DreamMaterial) > 0
	hasChallenged := strings.TrimSpace(in.Challenged) != ""
	hasAssoc := in.HasAssociation && len(in.Association) > 0
	if !hasDream && !hasChallenged && !hasAssoc {
		return SparkBurstDecision{}, false // 零值 nil 素材 ≠ 「有素材」(S1)
	}
	if !in.HasRoll {
		return SparkBurstDecision{}, false
	}
	if in.Roll < 0 || in.Roll >= cfg.BurstBelow {
		return SparkBurstDecision{}, false
	}
	// 主要产出口优先 (L194)。
	switch {
	case hasDream:
		m := in.DreamMaterial[0]
		return SparkBurstDecision{
			Kind:   SparkConnection,
			Source: SparkSource{Kind: "dream", RefID: m.MemID, Quote: m.V, HasRef: true},
			Reason: "night_integration",
		}, true
	case hasChallenged:
		return SparkBurstDecision{
			Kind:   SparkOpinion,
			Source: SparkSource{Kind: "dialog", RefID: "", Quote: strings.TrimSpace(in.Challenged), HasRef: true},
			Reason: "challenged",
		}, true
	default:
		m := in.Association[0]
		return SparkBurstDecision{
			Kind:   SparkConnection,
			Source: SparkSource{Kind: "memory", RefID: m.MemID, Quote: m.V, HasRef: true},
			Reason: "association",
		}, true
	}
}

// ── §2.6.4 筛选与分享 ─────────────────────────────────────

// SharableSparks 进「可说」池的灵光 (heat 够高且还没说过)。
// ⚠️ **不修正它自己的评估** (L201: 「它自己的评估会不准, 这正好」)。
// 这一层只做档位筛选, 不重排、不重打分。
func (s *Store) SharableSparks(now time.Time, limit int) ([]Spark, error) {
	if limit <= 0 {
		limit = 5
	}
	all, err := s.AllSparks(200)
	if err != nil {
		return nil, err
	}
	cfg := DefaultSparkStatusConfig()
	var out []Spark
	for _, sp := range all {
		if sp.SaidTS != "" {
			continue // 说过的不再主动说
		}
		st := SparkStatusAt(SparkHeatInput{HeatDelta: sp.HeatDelta, LastTouchTS: sp.LastTouchTS, Now: now}, false, cfg)
		if st == SparkFresh || st == SparkWarm {
			out = append(out, sp)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		hi := SparkHeat(SparkHeatInput{HeatDelta: out[i].HeatDelta, LastTouchTS: out[i].LastTouchTS, Now: now})
		hj := SparkHeat(SparkHeatInput{HeatDelta: out[j].HeatDelta, LastTouchTS: out[j].LastTouchTS, Now: now})
		if hi != hj {
			return hi > hj
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// SparkHasInterface 四条件③「与当前语境有接口」—— 不能凭空抛出。
// 判据: 灵光的 TalkKey 与当前对话/话题有**共享的表层线索** (与 §7.2 联想同一把尺子)。
func SparkHasInterface(sp Spark, history, topic string) bool {
	hay := history + " " + topic
	if strings.TrimSpace(hay) == "" {
		return false
	}
	keys := []string{sp.TalkKey}
	if sp.Source.HasRef {
		keys = append(keys, sp.Source.Quote)
	}
	set := map[string]bool{}
	for _, c := range BigramCues(hay, 0) {
		set[c.Token] = true
	}
	for _, k := range keys {
		for _, c := range BigramCues(k, 0) {
			if set[c.Token] {
				return true
			}
		}
	}
	return false
}

// sparkSeenPhrases 硬线②: 「不许把组合当事实」。
// ⚠️ 只有 memory 来源才**真的**是「看过的」; dream / dialog 来源的不是。
var sparkSeenPhrases = []string{"我在哪看过", "我记得在哪读到过", "我看过一篇", "我读过一篇", "之前看过"}

// sparkSeenAllowed 这一条灵光可不可以用「我看过」这种句式。
func sparkSeenAllowed(sp Spark) bool {
	return sp.Source.Kind == "memory" || sp.Source.Kind == "learned"
}

// sparkContentClean **内容**是否干净 (无禁区句式 / 无索评)。
//
// ⚠️ 检查对象是 **content 本身, 不是整段注入文本** ——
// 我们写的禁令句里天然含有被禁的那句话 (「不许问『你觉得怎么样』」),
// 拿整段去查会**自己把自己否掉**, 于是一条灵光都注入不进去。
// 这个 bug 在写出来的当场就被测试抓住了。
func sparkContentClean(content string, sp Spark) bool {
	if !sparkSeenAllowed(sp) {
		for _, wd := range sparkSeenPhrases {
			if strings.Contains(content, wd) {
				return false
			}
		}
	}
	for _, wd := range sparkAskPhrases {
		if strings.Contains(content, wd) {
			return false
		}
	}
	return true
}

// sparkAskPhrases 索要评价 (硬线③, 与 §2.5.4 同族)。
var sparkAskPhrases = []string{"你觉得怎么样", "你觉得呢", "怎么样?", "给点反馈", "你说说看"}

// SparkLine 段 7.3 的灵光分支 (唯一落点 —— 不新增段)。
//
// ⚠️ L216:「创意分享最忌讳配解释」。模型看到「我今天突然想到 X」的默认行为
// 就是**接着解释 X** —— 所以「不要解释」必须写成**禁止条款**, 与禁句表同族。
func SparkLine(sp Spark) (string, bool) {
	c := strings.TrimSpace(sp.Content)
	if c == "" {
		return "", false
	}
	if !sparkContentClean(c, sp) {
		return "", false
	}
	return "\n\n【本轮话题】(**只说这一句, 不要解释它从哪来**)\n「诶, 我今天突然想到 —— " + c + "」\n" +
		"⚠️ 发完就停: 不许问「你觉得怎么样」, 不许补一句解释, 不许说「我最近在想一个事」。\n" +
		"⚠️ 不许说「我梦到」「我在哪看过」这类把它当事实的话。", true
}

// ── 持久化 ──────────────────────────────────────────────

// EnsureSparkSchema 建表。**status 不在表里** —— 它是纯函数 (纪律 5)。
func EnsureSparkSchema(db *sql.DB) error {
	_, err := db.Exec("CREATE TABLE IF NOT EXISTS convo_spark (" +
		"id INTEGER PRIMARY KEY AUTOINCREMENT, born_ts TEXT NOT NULL, kind TEXT NOT NULL, " +
		"content TEXT NOT NULL, src_kind TEXT NOT NULL DEFAULT '', src_ref TEXT NOT NULL DEFAULT '', " +
		"src_quote TEXT NOT NULL DEFAULT '', has_ref INTEGER NOT NULL DEFAULT 0, " +
		"heat_delta REAL NOT NULL DEFAULT 0, last_touch_ts TEXT NOT NULL DEFAULT '', " +
		"said_ts TEXT NOT NULL DEFAULT '', talk_key TEXT NOT NULL DEFAULT '')")
	return err
}

// SaveSpark 落一条灵光。kind 非法 / 无素材一律拒收 (硬线①)。
func (s *Store) SaveSpark(sp Spark) (int64, error) {
	if !sp.Kind.Valid() {
		return 0, nil // 零值 kind 不许入库 (S7)
	}
	if !sp.Source.HasRef {
		return 0, nil // 没有素材就没有灵光 (S3)
	}
	if strings.TrimSpace(sp.Content) == "" {
		return 0, nil
	}
	hasRef := 0
	if sp.Source.HasRef {
		hasRef = 1
	}
	res, err := s.DB.Exec("INSERT INTO convo_spark (born_ts, kind, content, src_kind, src_ref, src_quote, has_ref, heat_delta, last_touch_ts, said_ts, talk_key) "+
		"VALUES (?,?,?,?,?,?,?,?,?,?,?)",
		sp.BornTS, string(sp.Kind), sp.Content, sp.Source.Kind, sp.Source.RefID, sp.Source.Quote,
		hasRef, sp.HeatDelta, sp.LastTouchTS, sp.SaidTS, sp.TalkKey)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// AllSparks 全部灵光 (含已沉降 —— **不删除**是 L224 的硬要求)。
func (s *Store) AllSparks(limit int) ([]Spark, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.DB.Query("SELECT id, born_ts, kind, content, src_kind, src_ref, src_quote, has_ref, heat_delta, last_touch_ts, said_ts, talk_key "+
		"FROM convo_spark ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Spark
	for rows.Next() {
		var sp Spark
		var kind string
		var hasRef int
		if err := rows.Scan(&sp.ID, &sp.BornTS, &kind, &sp.Content, &sp.Source.Kind, &sp.Source.RefID,
			&sp.Source.Quote, &hasRef, &sp.HeatDelta, &sp.LastTouchTS, &sp.SaidTS, &sp.TalkKey); err != nil {
			return nil, err
		}
		sp.Kind = SparkKind(kind)
		sp.Source.HasRef = hasRef != 0
		out = append(out, sp)
	}
	return out, rows.Err()
}

// TouchSpark 「被接住」→ heat 上升。
// ⚠️ 它是**状态更新**, 不是产生新 Spark —— 后者会无限繁殖 (S9)。
func (s *Store) TouchSpark(id int64, delta float64, at time.Time) error {
	_, err := s.DB.Exec("UPDATE convo_spark SET heat_delta = heat_delta + ?, last_touch_ts = ? WHERE id = ?",
		delta, at.Format(bodyTSLayout), id)
	return err
}

// MarkSparkSaid 记第一次说出去的时刻。
func (s *Store) MarkSparkSaid(id int64, at time.Time) error {
	_, err := s.DB.Exec("UPDATE convo_spark SET said_ts = ? WHERE id = ? AND said_ts = ''",
		at.Format(bodyTSLayout), id)
	return err
}

// sparkSystemPrompt 灵光产出的提示词。
// ⚠️ 硬线②「不许把组合当事实」: 只有 memory/learned 来源才允许「看过」句式,
// 所以这里按来源分支给出措辞。
// ⚠️ `reinforced` 是 §2.6.5「被接住 → 在这个方向上想更多」的**唯一落点**。
// 没有它, TouchSpark 加的那点热力只是**一个没人看的数字**。
//
// ⚠️ 它原来是"无参版 + With 版"两个函数, 而棘轮(TestNoUnwiredFunctions)当场指出
// 无参那个**变成了没人调的空壳** —— 于是删掉, 只留这一个。
func sparkSystemPrompt(p *PersonaProfile, d SparkBurstDecision, reinforced []string) string {
	base := "你是 " + p.Name + "。现在是你自己的时间, 没人跟你说话。\n"
	if d.Source.Quote != "" {
		base += "你脑子里有这么个东西: " + d.Source.Quote + "\n"
	}
	seen := "**不许说「我在哪看过」「我记得读到过」** —— 它不是你看来的, 是你自己想到的。"
	if sparkSeenAllowed(Spark{Source: d.Source}) {
		seen = "这是你确实接触过的东西, 可以说「看到过」, 但**不许编造具体出处**。"
	}
	if line := ReinforcedLine(reinforced); line != "" {
		base += line + "\n"
	}
	return base + "把它变成一句**你自己的**想法。\n" +
		"严格输出单行 JSON: {\"kind\":\"observation|connection|opinion|joke|idea\",\"content\":\"一句话\",\"talk_key\":\"主题词\"}\n" +
		"硬线: ①一句话, 不是一段, 不许配解释; ②**不许编造亲身经历**;" +
		"③**不许索要评价** (不许问「你觉得怎么样」); ④" + seen + NowLine()
}

// ParseSpark 灵光产出 → Spark。kind 非法 / 无素材一律拒收。
func ParseSpark(raw string, d SparkBurstDecision, bornTS string) (Spark, bool) {
	b, err := extractJSON(raw)
	if err != nil {
		return Spark{}, false
	}
	var f struct {
		Kind    string `json:"kind"`
		Content string `json:"content"`
		TalkKey string `json:"talk_key"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return Spark{}, false
	}
	kind := SparkKind(strings.TrimSpace(f.Kind))
	if !kind.Valid() {
		return Spark{}, false
	}
	content := strings.TrimSpace(f.Content)
	if content == "" {
		return Spark{}, false
	}
	for _, wd := range learnedForbidden {
		if strings.Contains(content, wd) {
			return Spark{}, false
		}
	}
	sp := Spark{
		BornTS: bornTS, Kind: kind, Content: content,
		Source: d.Source, HeatDelta: 0.5, // 新生落在 warm 档, 之后按时间衰减
		TalkKey: strings.TrimSpace(f.TalkKey),
	}
	return sp, true
}
