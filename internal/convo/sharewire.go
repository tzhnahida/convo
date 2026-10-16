package convo

import (
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// §2.5.4 有条件分享的**生产者**
//
// ⚠️ DecideShare 的判据一直在 (四条件 + 两道周上限), 而**管线**从来没建过:
// share_week 计数器从上线起**没有被 +1 过一次**。
// 于是设计稿 L153 那句「学到的 90% 永远不说出来」不是行为选择, 是**结构性**的 ——
// 观察台上那个 unsaid_ratio 一直贴着 100%, 而它看起来像「她保守」。
//
// 落点照 §2.6 灵光的先例: **段 7.3 的话题位** —— 不新增段, 段序结构完全不动。
// ─────────────────────────────────────────────────────────────

// learnShareTopic 把一条**从没带出去过**的学习内容变成这一轮的话题。
//
// 返回的第二个值是它的 id —— 调用方在**真的发出去之后**用它记账 (与灵光同规)。
func (e *Engine) learnShareTopic(now time.Time, state string) (Topic, int64, bool) {
	if e.Store == nil || e.UserID <= 0 {
		return Topic{}, 0, false
	}
	items, err := e.Store.RecentLearned(50)
	if err != nil {
		return Topic{}, 0, false
	}
	it, ok := pickUnsaidLearned(items)
	if !ok {
		return Topic{}, 0, false // 没有没说过的东西 → 没什么可分享
	}
	// 候选接收者 = owner (私聊出口只有他)。Match = **他有多爱聊这个话题** ——
	// 接话率, 与 §2.5.3 透露那条用的是同一把尺子。
	hits, _ := LoadTaste(e.Store.DB, e.ownerKey())
	match, hasMatch := TasteOf(hits, it.TopicKey)
	if !hasMatch {
		// ⚠️ 失败的落点与 HasHeat=false 同规: **往「不做」倒** ——
		// 样本不足时硬推一条, 表现是她突然讲一件对方完全不接的事。
		return Topic{}, 0, false
	}
	// 热力: 未提供 ≠ 零 (DecideShare 的注释: 未提供一律不分享)。
	heat, hasHeat := 0.0, false
	if hs, hok, herr := e.Store.LoadHeat(DefaultSceneKey); herr == nil && hok {
		heat, hasHeat = hs.Value, true
	}
	if _, ok := DecideShare(ShareInput{
		Kind: ShareLearn,
		Candidates: []ShareCandidate{{
			Who:       e.UserID,
			Scene:     DefaultSceneKey,
			Match:     match,
			Reachable: e.shareReachable(now),
		}},
		Heat: heat, HasHeat: hasHeat,
		Slot:    state,
		Roll:    Dice(100),
		HasRoll: true,
		// ⚠️ 两道周上限**共用一个 week 键**: 合计 ≤2/周是硬约束 (契约 S11),
		// 拆成两个计数器会叠加成 3/周。
		WeeklyUsed: counterOrZero(e.Store, "share_week", WeekStartOf(clock.Today())),
		LearnUsed:  counterOrZero(e.Store, "learn_share_week", WeekStartOf(clock.Today())),
	}); !ok {
		return Topic{}, 0, false
	}
	line := LearnShareLine(it)
	if line == "" {
		return Topic{}, 0, false
	}
	return Topic{Kind: "learn", Text: line}, it.ID, true
}

// shareReachable §2.5.4 的「够得到的那一个」: **最近有互动**才算够得到。
//
// ⚠️ 判据用「场景内最后一条消息」而不是「她上次主动开场」:
// 够不够得到说的是**对方在不在**, 不是她说过没有。
func (e *Engine) shareReachable(now time.Time) bool {
	last, has, err := LastSceneMessageAtExcluding(e.Store.DB, DefaultSceneKey, "", now)
	if err != nil || !has || last.IsZero() {
		return false // 读不到 / 从来没有过 → 够不到 (失败往「不分享」倒)
	}
	return now.Sub(last) <= ShareReachableWindow
}

// ShareReachableWindow 「够得到」的时间窗。
//
// ⚠️ 它是**判据**而不是配置项: 三天没说过话的人, 你突然跟他分享一件事,
// 那不是分享, 是搭讪。
const ShareReachableWindow = 72 * time.Hour

// pickUnsaidLearned 挑一条**从没带出去过**的 (SaidCount==0)。
//
// ⚠️ 顺序是「最近学到优先」—— RecentLearned 已经按时间倒序,
// 而分享一条刚学到的东西比翻出一条上个月的更自然。
func pickUnsaidLearned(items []LearnedItem) (LearnedItem, bool) {
	for _, it := range items {
		if it.SaidCount == 0 && strings.TrimSpace(it.Content) != "" {
			return it, true
		}
	}
	return LearnedItem{}, false
}

// LearnShareLine §2.5.4 分享学到的东西 → 段 7.3 的一句话。
//
// ⚠️ 三条禁止条款与 SparkLine 同规 (缺一条它就会变成播报):
//  1. **不许解释** —— 解释就把「想起一件事」变成「给你讲个知识点」;
//  2. **不许当事实断言** —— 它是「我最近看到的」, 不是「我知道的」;
//  3. **不许说「我学到」「我最近在研究」** —— 那几个字立刻暴露它在上课。
func LearnShareLine(it LearnedItem) string {
	c := strings.TrimSpace(it.Content)
	if c == "" {
		return ""
	}
	return "【本轮话题】你最近看到的一件事: " + c +
		" —— 像想起什么似的自然说出来, **不要解释**它、**不要当事实断言**," +
		"也不要说「我学到」「我最近在研究」。说完就停, 别引申。"
}

// MarkLearnSharedAfter 真的发出去了才记 (§2.5.4)。
//
// ⚠️ 记在**发出去之后** (与 MarkSparkSaidAfter / NoteCastMentions 同规):
// 提前记的话, 一次生成失败就会让这条内容**永远不再被说**, 而它其实谁都没听过。
func (e *Engine) MarkLearnSharedAfter(id int64, now time.Time) {
	if e.Store == nil {
		return
	}
	if err := e.Store.MarkRevealed(id, now); err != nil {
		log.Warn("convo:learn", "带出记账失败 (下次还会挑到它): %v", err)
	}
	week := WeekStartOf(clock.Today())
	_, _ = e.Store.CounterInc("share_week", week)
	_, _ = e.Store.CounterInc("learn_share_week", week)
}
