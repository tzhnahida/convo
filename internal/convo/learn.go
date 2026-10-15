package convo

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/tzhnahida/VocabLock/internal/clock"
	"github.com/tzhnahida/VocabLock/internal/log"
)

// ─────────────────────────────────────────────────────────────
// §2.5 自主学习与不经意透露
//
// 「它有自己关心的事, 而且会自己去弄明白 —— 这件事大多不告诉任何人。」
// 这是「它有生活」最便宜的一种实现: **不需要对话, 也能有内容**。
//
// ⚠️ 三条最要紧的纪律:
//   1. 学习**不吃任何共享资源** (L105/L146: 它消耗的是它自己的时间) ——
//      不吃主动配额、不吃热力。这是它唯一的「免费」机制。
//   2. 学到的 **90% 永远不说出来** (L153) —— 所以库存(段4b)与这一次带出(段7.4)
//      **必须分开**: 把碎片清单常驻注入 = 模型会全部说出来 = 每日汇报。
//   3. 硬线: **学来的东西不许当自己的经历** (L116)。
//      「我最近在看爵士乐历史」可以; 「我上周去了一个爵士酒吧」不行。
// ─────────────────────────────────────────────────────────────

// LearnKind 学习产物的三档 (L110-114)。零值 "" = 未提供 → 不入库。
type LearnKind string

const (
	LearnInterest LearnKind = "interest" // 兴趣点: 可透露
	LearnFact     LearnKind = "fact"     // 知识碎片: 可自然带出
	LearnOpinion  LearnKind = "opinion"  // 观点: 可争论 —— 最人味的一档
)

// Valid 白名单 (同 memSource 的严格模式: 未知值一律拒收, 不走 default)。
func (k LearnKind) Valid() bool {
	switch k {
	case LearnInterest, LearnFact, LearnOpinion:
		return true
	}
	return false
}

// LearnedItem 一条学习产物。**只存事实与来历**; 可透露性/冷却全部现算。
type LearnedItem struct {
	ID         int64
	Kind       LearnKind
	Content    string // 一句话
	TopicKey   string // 品味轨迹上的主题键 (小写词根, 匹配用; 不是自由文本)
	Origin     string // 来历的人话 (owner 审查用; **不进 prompt**)
	BornTS     string
	LastSaidTS string // 事实: 最后一次被带出的时刻 ("" = 从未)
	SaidCount  int    // 事实: 累计带出次数
}

// ── 品味轨迹 (派生量, **不落库**) ──────────────────────────

// TasteTopic 轨迹上的一个主题。
type TasteTopic struct {
	Key    string
	Weight float64
	Source string // persona | memory | learned | mixed (归因用)
}

// TasteTrail 按 Weight 降序。
type TasteTrail struct{ Topics []TasteTopic }

// TasteInput 引擎侧组装好的快照, 纯函数消费。
type TasteInput struct {
	PersonaInterests []string
	Memories         []Memory
	Learned          []LearnedItem
	// HasLearned ⚠️ 与 HasImpulse 同一条约定: **空切片 ≠ 没学过**。
	// 少了它, 忘了填的调用方会得到一条空的「你琢磨过的方向」标题, 然后**自己编一个兴趣出来**。
	HasLearned bool
}

// BuildTasteTrail 从三路来源合成品味轨迹。纯函数, 确定性排序。
func BuildTasteTrail(in TasteInput) TasteTrail {
	weights := map[string]float64{}
	sources := map[string]map[string]bool{}
	add := func(key, src string, w float64) {
		key = strings.ToLower(strings.TrimSpace(key))
		if len([]rune(key)) < 2 {
			return
		}
		weights[key] += w
		if sources[key] == nil {
			sources[key] = map[string]bool{}
		}
		sources[key][src] = true
	}
	for _, s := range in.PersonaInterests {
		add(s, "persona", 1.0)
	}
	for _, m := range in.Memories {
		if m.Source != "user_said" {
			continue // 只有「关于 ta 的事」才算 ta 的兴趣
		}
		add(m.Label, "memory", 0.6)
	}
	for _, l := range in.Learned {
		add(l.TopicKey, "learned", 0.8)
	}
	var t TasteTrail
	for k, wt := range weights {
		src := "mixed"
		if len(sources[k]) == 1 {
			for s := range sources[k] {
				src = s
			}
		}
		t.Topics = append(t.Topics, TasteTopic{Key: k, Weight: wt, Source: src})
	}
	sort.Slice(t.Topics, func(i, j int) bool {
		if t.Topics[i].Weight != t.Topics[j].Weight {
			return t.Topics[i].Weight > t.Topics[j].Weight
		}
		return t.Topics[i].Key < t.Topics[j].Key
	})
	return t
}

// TasteTrailLine 段 4b (慢 / 周档)。
//
// ⚠️ **只给方向键, 不给内容** —— L153 说「学到的 90% 永远不说出来」。
// 把碎片清单常驻注入, 模型会全部说出来, 那就是每日汇报 (L138 硬线③)。
// ⚠️ HasLearned=false 时**整段不注入**: 一个空的「你琢磨过的方向」标题
// 会直接诱发模型编一个兴趣出来。
func TasteTrailLine(in TasteInput, max int) (string, bool) {
	if !in.HasLearned && len(in.PersonaInterests) == 0 && len(in.Memories) == 0 {
		return "", false
	}
	if max <= 0 {
		max = 4
	}
	t := BuildTasteTrail(in)
	if len(t.Topics) == 0 {
		return "", false
	}
	var b strings.Builder
	b.WriteString("\n【你自己最近在琢磨的方向】(这是你自己的生活, **不要主动汇报**; 聊到了才自然说)\n")
	for i := 0; i < len(t.Topics) && i < max; i++ {
		b.WriteString("- " + t.Topics[i].Key + "\n")
	}
	return b.String(), true
}

// ── §2.5.1 自主学习 ────────────────────────────────────────

// LearnWeekdays 固定天: 周二 / 周四 / 周日 = 每周 3 次。
//
// ⚠️ 设计稿只说「每周 2-3 次」, 没说排布。我选**固定天**而不是概率:
// 理由与 §6.3 的纪律同源 —— 拿概率驱动「一周内的哪一天会发生」,
// 实测看不出效果, 而固定天可测、可回归。
var LearnWeekdays = []time.Weekday{time.Tuesday, time.Thursday, time.Sunday}

// shouldLearnToday 今天该不该学习。纯函数 (时钟由调用方注入)。
func shouldLearnToday(now time.Time) bool {
	for _, d := range LearnWeekdays {
		if now.Weekday() == d {
			return true
		}
	}
	return false
}

// learnEligible 学习时机的**硬闸门** (L105): 只在 free/slack 槽, 不在睡眠窗。
// 语义与 DecideFatigue 的睡眠闸门同一条: 睡眠是**最前面**的门, 不是在别处减一点。
func (e *Engine) learnEligible(now time.Time) bool {
	st := e.currentState()
	if st != StateFree && st != StateSlack {
		return false
	}
	if ws, err := e.Store.LoadSchedule(); err == nil && ws != nil {
		if inSleepWindow(ws, now) {
			return false
		}
	}
	return true
}

// learnQueryPrompt 第一步: 你今天想弄清楚什么。
//
// ⚠️ 只让它出一个**搜索词**, 不让它出结论 —— 结论必须来自搜到的东西。
func learnQueryPrompt(p *PersonaProfile, trail TasteTrail) string {
	var topics []string
	for i := 0; i < len(trail.Topics) && i < 6; i++ {
		topics = append(topics, trail.Topics[i].Key)
	}
	topicStr := strings.Join(topics, "、")
	if topicStr == "" {
		topicStr = "你自己最近关心的事"
	}
	return "你是 " + p.Name + "。现在是你的私人时间, 没人跟你说话。\n" +
		"你最近在琢磨: " + topicStr + "。\n" +
		"你想去网上弄清楚一件**具体**的事。给一个搜索词。\n" +
		"严格输出单行 JSON: {\"query\":\"搜索词\"}\n" +
		"要求: 搜索词 4-15 字, 是一个能被搜到的**名词性问题**, 不要写句子。" + NowLine()
}

// learnDigestPrompt 第二步: 把搜到的**真实结果**嚼成一条自己的话。
//
// ⚠️ 来历**不由模型写** —— 见 runLearning。模型只会把 URL 抄错或编一个。
func learnDigestPrompt(p *PersonaProfile, query, hits string) string {
	return "你是 " + p.Name + "。你刚搜了「" + query + "」, 下面是搜到的东西:\n\n" + hits + "\n" +
		"从里面挑一个**你真心觉得有意思**的点, 用你自己的话讲一句。\n" +
		"严格输出单行 JSON: {\"kind\":\"interest|fact|opinion\",\"topic\":\"主题词\",\"content\":\"一句话\"}\n" +
		"硬线: ①content 必须是**上面材料里有的**, 不许加材料外的内容;" +
		"②**不许编造亲身经历** (不许出现「我去了」「我见过」);" +
		"③一句话, 不是一段; ④topic 是一个 2-6 字的名词。" + NowLine()
}

// learnSystemPrompt 旧版单步提示词 (保留给测试与回读)。
//
// ⚠️ 措辞里写死了两条硬线: 不许编经历、不许汇报。
// 学习 Job 是整个系统里唯一「无对话产出内容」的地方, 少了这两句,
// 模型的第一反应就是写一段「今天学到了…」。
func learnSystemPrompt(p *PersonaProfile, trail TasteTrail) string {
	var topics []string
	for i := 0; i < len(trail.Topics) && i < 6; i++ {
		topics = append(topics, trail.Topics[i].Key)
	}
	topicStr := strings.Join(topics, "、")
	if topicStr == "" {
		topicStr = "你自己最近关心的事"
	}
	return "你是 " + p.Name + "。现在是你的私人时间, 没人跟你说话。\n" +
		"你最近在琢磨: " + topicStr + "。\n" +
		"去找一个你**真的会想知道**的具体东西, 然后给出一句话结论。\n" +
		"严格输出单行 JSON: {\"kind\":\"interest|fact|opinion\",\"topic\":\"主题词\",\"content\":\"一句话\",\"origin\":\"你从哪弄明白的(人话, 20字内)\"}\n" +
		"硬线: ①**不许编造亲身经历** —— 不许出现「我去了」「我见过」「上周我」这类说法;" +
		"②你有的是**知识**, 不是经历; ③content 必须是**一句话**, 不是一段。" + NowLine()
}

// runLearning 学习 Job。**只用自己的时间** —— 不吃热力、不吃主动配额。
//
// ⚠️ **失败只 warn, 不产生任何碎片** —— 绝不「编一个凑数」。
// 编出来的碎片次日会以「它最近在看 X」的形态说出口, 而 X 是假的。
func (e *Engine) runLearning(ctx context.Context, p *PersonaProfile) error {
	if e.Chatter == nil {
		return nil
	}
	mem, _ := e.Store.TopMemories(20, clock.Now())
	learned, _ := e.Store.RecentLearned(20)
	trail := BuildTasteTrail(TasteInput{
		PersonaInterests: splitInterests(p.Attrs["interests"]),
		Memories:         mem,
		Learned:          learned,
		HasLearned:       true,
	})
	// ——— 第 1 步: 它想弄清楚什么 ———
	out, err := e.Chatter.Chat(ctx, learnQueryPrompt(p, trail), "开始。")
	if err != nil {
		log.Warn("convo:learn", "学习 Job 失败 (今夜不产出碎片): %v", err)
		return nil
	}
	query, ok := ParseLearnQuery(out)
	if !ok {
		log.Warn("convo:learn", "第一步没给出可用的搜索词, 今夜不学")
		return nil
	}
	// ——— 第 2 步: **真去搜** ———
	//
	// ⚠️ 这里是整条链的要害。搜不到 / 搜挂了 → **今天不学**。
	// 退回"让模型自己编一条"就是把它变成一个会撒谎的东西 ——
	// 而且它会在某天的群里把那条编的东西当谈资说出来。
	searcher := e.learnSearcher()
	hits, serr := searcher.SearchWeb(ctx, query, 5)
	if serr != nil {
		log.Warn("convo:learn", "搜索失败 (%v) —— 今天不学, 绝不退回编造", serr)
		return nil
	}
	if len(hits) == 0 {
		log.Info("convo:learn", "「%s」没搜到东西 —— 今天不学", query)
		return nil
	}
	// ——— 第 3 步: 嚼成一句自己的话 ———
	out2, derr := e.Chatter.Chat(ctx, learnDigestPrompt(p, query, FormatHits(hits, 5)), "开始。")
	if derr != nil {
		log.Warn("convo:learn", "消化失败 (今夜不产出碎片): %v", derr)
		return nil
	}
	item, ok := ParseLearnedItem(out2)
	if !ok {
		log.Warn("convo:learn", "学习产出不合规, 丢弃")
		return nil
	}
	// ⚠️ **来历由代码写死**, 不信模型。
	// 模型给的 origin 永远是「看了篇科普」这种无法核验的话;
	// 而这里写的是**真实搜到的那一条** —— 以后出问题能查回去。
	item.Origin = strings.TrimSpace(hits[0].Title) + " · " + hits[0].URL
	if _, err := e.Store.SaveLearned(item); err != nil {
		log.Warn("convo:learn", "学习碎片落库失败: %v", err)
		return err
	}
	if _, err := e.Store.CounterInc("learn_runs", clock.Today()); err != nil {
		log.Warn("convo:learn", "学习计数失败: %v", err)
	}
	log.Info("convo:learn", "§2.5 学到一条 [%s] %s  ← %s", item.Kind, item.Content, hits[0].URL)
	return nil
}

// ParseLearnQuery 解析第一步的搜索词。
func ParseLearnQuery(raw string) (string, bool) {
	b, err := extractJSON(raw)
	if err != nil {
		return "", false
	}
	var f struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return "", false
	}
	q := strings.TrimSpace(f.Query)
	if len([]rune(q)) < 2 || len([]rune(q)) > 40 {
		return "", false
	}
	return q, true
}
func splitInterests(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	f := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '、' || r == ';' || r == '；' || r == '/'
	})
	out := make([]string, 0, len(f))
	for _, x := range f {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// learnedForbidden 学习产出的红线词。
// ⚠️ 第一条是 L116 的硬线: 学来的东西不许当自己的经历。
var learnedForbidden = []string{
	"我上周去了", "我去了", "我见过", "我去过", "我参加", "我买了个", "我认识的一个人",
	"你应该知道", "这都不懂", "我研究了很多", "我最近学了一个",
}

// ParseLearnedItem 学习 Job 的结构化产出 → LearnedItem。
// 解析失败或触红线一律拒收 (失败往「正常说话」倒, 不产生碎片)。
func ParseLearnedItem(raw string) (LearnedItem, bool) {
	b, err := extractJSON(raw)
	if err != nil {
		return LearnedItem{}, false
	}
	var f struct {
		Kind    string `json:"kind"`
		Topic   string `json:"topic"`
		Content string `json:"content"`
		Origin  string `json:"origin"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return LearnedItem{}, false
	}
	kind := LearnKind(strings.TrimSpace(f.Kind))
	if !kind.Valid() {
		return LearnedItem{}, false // 零值/未知值一律拒收 (S7 同款)
	}
	content := strings.TrimSpace(f.Content)
	topic := strings.TrimSpace(f.Topic)
	if content == "" || topic == "" {
		return LearnedItem{}, false
	}
	for _, wd := range learnedForbidden {
		if strings.Contains(content, wd) || strings.Contains(topic, wd) {
			return LearnedItem{}, false
		}
	}
	r := []rune(content)
	if len(r) > 80 {
		content = string(r[:80])
	}
	return LearnedItem{
		Kind: kind, Content: content, TopicKey: strings.ToLower(topic),
		Origin: strings.TrimSpace(f.Origin),
		BornTS: clock.Now().Format(bodyTSLayout),
	}, true
}

// ── §2.5.3 不经意透露 ─────────────────────────────────────

// RevealMinRoll 带出概率: roll < 15 → 带出 (10-15% 的上沿)。
//
// ⚠️ 这里用骰子**不违反**「落点必须是硬约束」的纪律:
// B6 的教训是「拿一个日内几乎不变的信号去驱动日内时刻」;
// 而这里的频率上限由 **7 天硬冷却**承担, 骰子只决定「这次是不是那次」。
// 这是「硬约束兜底 + 骰子调稀疏度」, 不是「概率缩放」。
const RevealMinRoll = 15

// RevealCooldownDays 同一条知识 7 天内最多带出 1 次 (L122)。
const RevealCooldownDays = 7

// RevealInput 透露判定的输入。
type RevealInput struct {
	UserText   string
	TopicKey   string
	Candidates []LearnedItem // 已按品味命中过滤, 降序
	Roll       int
	// HasRoll ⚠️ 零值 0 = **未提供** → 不透露。
	// 少了它, 忘了掷的调用方会被读成「掷出 0」= **每次都透露** —— 与 HasImpulse 同一条理由。
	HasRoll      bool
	CooldownDays int

	Now time.Time
}

// RevealDecision 透露判定结果。
type RevealDecision struct {
	Item    LearnedItem
	Kind    LearnKind
	Matched bool
}

// RevealEligible 7 天硬冷却。
// ⚠️ 它是**独立函数**而不是内联在 DecideReveal 里:
// 契约 R3 说的是「即使骰子掷中也要硬拒」, 那要求冷却能被单独测量。
func RevealEligible(it LearnedItem, now time.Time, cooldownDays int) bool {
	if cooldownDays <= 0 {
		cooldownDays = RevealCooldownDays
	}
	if strings.TrimSpace(it.LastSaidTS) == "" {
		return true
	}
	t, ok := ParseMsgTS(it.LastSaidTS)
	if !ok {
		return false // 解析不了的时刻 → 宁可不说 (失败往不做倒)
	}
	return now.Sub(t) >= time.Duration(cooldownDays)*24*time.Hour
}

// DecideReveal 纯函数。
// ⚠️ **冷却在骰子之前判** —— 契约是「即使骰子掷中也要硬拒」,
// 反过来写的话, 冷却就只是「大多数时候有效」。
func DecideReveal(in RevealInput) (RevealDecision, bool) {
	if len(in.Candidates) == 0 {
		return RevealDecision{}, false
	}
	now := in.Now
	if now.IsZero() {
		now = clock.Now()
	}
	var pick LearnedItem
	var found bool
	for _, it := range in.Candidates {
		if !RevealEligible(it, now, in.CooldownDays) {
			continue
		}
		pick, found = it, true
		break
	}
	if !found {
		return RevealDecision{}, false
	}
	if !in.HasRoll {
		return RevealDecision{}, false // 忘了掷 = 未提供, 不是「掷出 0」
	}
	if in.Roll < 0 || in.Roll >= RevealMinRoll {
		return RevealDecision{}, false
	}
	return RevealDecision{Item: pick, Kind: pick.Kind, Matched: true}, true
}

// LearnedLine 段 7.4 (每轮档)。三档措辞分开 —— 形态不同, 不是同一句话换个词。
//
// ⚠️ 三条硬线写进措辞: 不许汇报 (L123) · 不许当经历 (L116) · 不许拿它压人 (L124)。
func LearnedLine(d RevealDecision) (string, bool) {
	if !d.Matched {
		return "", false
	}
	c := strings.TrimSpace(d.Item.Content)
	if c == "" {
		return "", false
	}
	switch d.Kind {
	case LearnInterest:
		return "\n（你最近自己在看这个 —— 聊到了就顺口提一句是什么让你感兴趣, 别汇报「我在学习」）", true
	case LearnOpinion:
		return "\n（这事你有立场:「" + c + "」。**可以说得不一样**, 甚至可以被反驳 —— 但别用它压人）", true
	default:
		return "\n（这个你刚好知道一个具体说法:「" + c + "」。**当谈资用**, 别说成经历, 别说「我最近学了」）", true
	}
}

// ── §2.5.4 有条件分享 ─────────────────────────────────────

// ShareKind 两类分享。**共享一个周计数器** (L210: 合计 ≤2/周, 取严, 不叠加)。
type ShareKind string

const (
	ShareLearn ShareKind = "learn"
	ShareSpark ShareKind = "spark"
)

const (
	// ShareWeeklyCap 合计上限。**一个计数器**, 不是两个 ——
	// 两个计数器会叠加成 3/周 (契约 S11)。
	ShareWeeklyCap = 2
	// LearnShareWeeklyCap §2.5.4 单独给的更严上限 (每周 ≤1 次)。
	LearnShareWeeklyCap = 1
	// ShareMinRoll 命中阈值: roll < 15 → 分享 (10-20% 的下沿)。
	ShareMinRoll = 15
	// ShareMinHeat 条件②「热力足够」。
	ShareMinHeat = 3.0
)

// ShareCandidate 一个候选接收者。
type ShareCandidate struct {
	Who       int64
	Scene     string  // "qq" | "group:<id>"
	Match     float64 // 现算: 品味匹配 × §6.8 偏向
	Reachable bool    // 「够得到的那一个」: 最近有互动
}

// ShareInput 分享判定的输入。
type ShareInput struct {
	Kind       ShareKind
	Candidates []ShareCandidate
	Heat       float64
	// HasHeat ⚠️ false → **不许分享**。
	// 这条与 HasImpulse 语义**相反**: 开口是基线(失败往做倒), 分享是例外(失败往不做倒)。
	HasHeat    bool
	Slot       string
	Roll       int
	HasRoll    bool
	WeeklyUsed int
	// LearnUsed 本周已经分享过几次学习内容 (仅 LearnShare 用)。
	LearnUsed int
}

// ShareDecision 分享判定结果。
type ShareDecision struct {
	Who           int64
	Scene         string
	WhetherToName bool
	Reason        string
}

// DecideShare 纯函数。四条件的**硬闸门全部前置**, 概率放最后。
func DecideShare(in ShareInput) (ShareDecision, bool) {
	if in.WeeklyUsed >= ShareWeeklyCap {
		return ShareDecision{}, false // 周上限是硬约束, 先于一切
	}
	if in.Kind == ShareLearn && in.LearnUsed >= LearnShareWeeklyCap {
		return ShareDecision{}, false
	}
	if !in.HasHeat {
		return ShareDecision{}, false // 未提供热力 ≠ 热力为零 → 一律不分享
	}
	if in.Heat < ShareMinHeat {
		return ShareDecision{}, false
	}
	slot := strings.TrimSpace(in.Slot)
	if slot != StateFree && slot != StateSlack {
		return ShareDecision{}, false // ② 在可发言槽
	}
	best := -1
	for i, c := range in.Candidates {
		if !c.Reachable {
			continue // 「够得到的那一个」: 够不到的不算候选
		}
		if best < 0 || c.Match > in.Candidates[best].Match ||
			(c.Match == in.Candidates[best].Match && c.Who < in.Candidates[best].Who) {
			best = i
		}
	}
	if best < 0 {
		return ShareDecision{}, false
	}
	if !in.HasRoll {
		return ShareDecision{}, false // 忘了掷 = 未提供
	}
	if in.Roll < 0 || in.Roll >= ShareMinRoll {
		return ShareDecision{}, false // ③ 概率掷中
	}
	c := in.Candidates[best]
	// ⚠️ 群里**默认不指名** (L136): 只对一个人分享会让旁观者尴尬。
	name := SceneKindOf(c.Scene) != SceneKindGroup
	return ShareDecision{Who: c.Who, Scene: c.Scene, WhetherToName: name, Reason: "share"}, true
}

// ── 持久化 ──────────────────────────────────────────────

// EnsureLearnSchema 建表。
// ⚠️ **独立表**, 不写进 convo_topics —— topic.go 的 SeedTopics 会 DELETE 全表 (契约 R11),
// 也不写进 convo_memory —— 那会掉进 dialog.go 归属 switch 的 default 分支,
// 被当成「你记得的关于 ta 的事」, 于是它会对用户说「你最近在看爵士乐历史」(契约 R10)。
func EnsureLearnSchema(db *sql.DB) error {
	_, err := db.Exec("CREATE TABLE IF NOT EXISTS convo_learned (" +
		"id INTEGER PRIMARY KEY AUTOINCREMENT, " +
		"kind TEXT NOT NULL, topic_key TEXT NOT NULL, content TEXT NOT NULL, " +
		"origin TEXT NOT NULL DEFAULT '', born_ts TEXT NOT NULL, " +
		"last_said_ts TEXT NOT NULL DEFAULT '', said_count INTEGER NOT NULL DEFAULT 0)")
	return err
}

// SaveLearned 落一条学习产物。kind 非法一律拒收。
func (s *Store) SaveLearned(it LearnedItem) (int64, error) {
	if !it.Kind.Valid() {
		return 0, nil
	}
	res, err := s.DB.Exec("INSERT INTO convo_learned (kind, topic_key, content, origin, born_ts) VALUES (?,?,?,?,?)",
		string(it.Kind), it.TopicKey, it.Content, it.Origin, it.BornTS)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// RecentLearned 最近的学习产物。
func (s *Store) RecentLearned(limit int) ([]LearnedItem, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.DB.Query("SELECT id, kind, topic_key, content, origin, born_ts, last_said_ts, said_count "+
		"FROM convo_learned ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LearnedItem
	for rows.Next() {
		var it LearnedItem
		var kind string
		if err := rows.Scan(&it.ID, &kind, &it.TopicKey, &it.Content, &it.Origin, &it.BornTS, &it.LastSaidTS, &it.SaidCount); err != nil {
			return nil, err
		}
		it.Kind = LearnKind(kind)
		out = append(out, it)
	}
	return out, rows.Err()
}

// MarkRevealed 记一次带出 (事实, 不是派生量)。
func (s *Store) MarkRevealed(id int64, at time.Time) error {
	_, err := s.DB.Exec("UPDATE convo_learned SET last_said_ts = ?, said_count = said_count + 1 WHERE id = ?",
		at.Format(bodyTSLayout), id)
	return err
}

// LearnedByTopic 按主题键取候选 (已按品味命中过滤)。
func (s *Store) LearnedByTopic(topicKey string, limit int) ([]LearnedItem, error) {
	if limit <= 0 {
		limit = 5
	}
	rows, err := s.DB.Query("SELECT id, kind, topic_key, content, origin, born_ts, last_said_ts, said_count "+
		"FROM convo_learned WHERE topic_key LIKE ? ORDER BY id DESC LIMIT ?", "%"+strings.ToLower(strings.TrimSpace(topicKey))+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LearnedItem
	for rows.Next() {
		var it LearnedItem
		var kind string
		if err := rows.Scan(&it.ID, &kind, &it.TopicKey, &it.Content, &it.Origin, &it.BornTS, &it.LastSaidTS, &it.SaidCount); err != nil {
			return nil, err
		}
		it.Kind = LearnKind(kind)
		out = append(out, it)
	}
	return out, rows.Err()
}
