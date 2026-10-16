package convo

import (
	"time"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// §2.6 灵光的**"之后"** (2026-10-15 接线)
//
// 这一节原来只有**"产生"**:
//
//   · `runSparkBurst` 每晚按配额生成、落库 ✓
//   · `SparkLine` —— **生产调用者为零**
//   · `MarkSparkSaid` —— **零**
//   · `TouchSpark` —— **零**
//
// 也就是说: **它想出来的东西，说不出。**
//
// 而设计稿对"之后"的评价比"产生"高得多:
//
//   > **被接住之后(这才是"迸发"的真正机制)**
//   > 被接住 → heat 上升 → 这个方向被强化 → 它会在这个方向上想更多
//   > 没人接 → heat 衰减 → 放下(不是删除, 是沉降进长期记忆)
//
// 本文件接的是**前半段: 说出来**。「被接住」那一半见 §TouchSpark 的接线点。
// ─────────────────────────────────────────────────────────────

// SparkSharePerWeek 分享上限 (§2.6.6 硬线 5: 分享 ≤2/周)。
//
// ⚠️ 它是**生活上限** —— 只在这里判, **绝不告诉模型数字**(§4.6.1)。
// 提示词里写"你一周只能分享两次", 它就会开始数着分享。
const SparkSharePerWeek = 2

// SparksSaidSince 某个时刻之后说出去过几条。
func (s *Store) SparksSaidSince(since time.Time) int {
	if s == nil || s.DB == nil {
		return 0
	}
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM convo_spark WHERE said_ts <> '' AND said_ts >= ?`,
		since.Format(bodyTSLayout)).Scan(&n); err != nil {
		// ⚠️ 读不到 → **按已经用完处理**(返回上限): 失败往"这次不说"倒 ——
		// 说多了比少说一次糟得多, 而"少说一次"没有任何人会发现。
		return SparkSharePerWeek
	}
	return n
}

// ShareableSpark 挑一条**现在可以说**的灵光。
//
// 三个门, 顺序即优先级:
//
//	① 周配额(硬闸) → ② 说过的跳过 → ③ 档位必须是 fresh/warm
//
// ⚠️ ③ 那一档由 `SparkStatusAt` 现算: 新生是 warm(ParseSpark 给 0.5),
// 之后按 72 小时半衰期冷下去。冷掉的**不是删除**, 只是不再主动说 ——
// 它仍然躺在库里, 将来可能被联想触发 (那是 §7.2 的事)。
func (s *Store) ShareableSpark(now time.Time) (Spark, bool) {
	if s == nil || s.DB == nil {
		return Spark{}, false
	}
	if s.SparksSaidSince(now.AddDate(0, 0, -7)) >= SparkSharePerWeek {
		return Spark{}, false
	}
	all, err := s.AllSparks(50)
	if err != nil {
		return Spark{}, false
	}
	cfg := DefaultSparkStatusConfig()
	for _, sp := range all {
		if sp.SaidTS != "" {
			continue // 说过的不再说 —— 同一句"我突然想到"讲两遍就是念稿
		}
		st := SparkStatusAt(SparkHeatInput{HeatDelta: sp.HeatDelta, LastTouchTS: sp.LastTouchTS, Now: now},
			false, cfg)
		if st != SparkFresh && st != SparkWarm {
			continue
		}
		return sp, true
	}
	return Spark{}, false
}

// sparkShareTopic 把一条可分享的灵光变成**这一轮的话题** (§2.6 的唯一落点: 段 7.3)。
//
// 返回的第二个值是它的 id —— 调用方在**真的发出去之后**要用它记 `said_ts`。
func (e *Engine) sparkShareTopic(now time.Time, topicText string) (Topic, int64, bool) {
	if e.Store == nil {
		return Topic{}, 0, false
	}
	sp, ok := e.Store.ShareableSpark(now)
	if !ok {
		return Topic{}, 0, false
	}
	// §2.6 条件③「与当前语境有接口」: **灵光不能凭空抛出**。
	//
	// ⚠️ 判据的输入是**这一轮真的在聊的东西** (最近几句 + 本轮话题), 而不是
	// 「这条灵光本身好不好」—— 后者 SparkStatusAt 已经判过了 (档位 fresh/warm 才走到这里)。
	// 缺这一关的表现是: 它突然说一句「我前几天看到个事」, 而前文没有任何东西接得住它。
	if hist := e.recentDialogIn(DefaultSceneKey, 6); !SparkHasInterface(sp, hist, topicText) {
		log.Info("convo:spark", "§2.6 这条灵光与当前语境没有接口 —— 这一轮不抛出")
		return Topic{}, 0, false
	}
	line, ok := SparkLine(sp)
	if !ok {
		// SparkLine 自己会拦"内容不干净"的那些 (硬线②: 不许把组合当事实)。
		return Topic{}, 0, false
	}
	return Topic{Kind: "spark", Text: line}, sp.ID, true
}

// MarkSparkSaidAfter 真的发出去了才记 —— 与 §3.6.5c 的提及记账同规。
//
// ⚠️ 记在**发出去之后**: 提前记的话, 一次生成失败就会让那条灵光永远不再被说
// (它被标成"说过了"), 而它其实谁都没听过。
func (e *Engine) MarkSparkSaidAfter(id int64) {
	if e.Store == nil || id <= 0 {
		return
	}
	if err := e.Store.MarkSparkSaid(id, clock.Now()); err != nil {
		log.Warn("convo:spark", "灵光 said_ts 记不上(周配额会算不准): %v", err)
	}
}
