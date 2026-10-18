package convo

import (
	"database/sql"
	"math"
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// §6.10 插话: 「偶尔参与群聊」
//
// 在它之前, 非 @ 的群消息一律 `silent (undecidable)` —— 它**只有被叫才存在**。
// 而设计稿早就埋了燃料: §2.5 灵光、§2.6 学过的碎片, 它们存在的意义就是
// 「在合适的时机被说出来」。在那之前, 它自己弄明白的东西, 它自己永远不说。
//
// ⚠️ **两条理由通道, 都是合法的**:
//   ① **话题命中** —— 这个话题我正好有话说 (灵光/学过的 与当前话题重合);
//   ② **随机参与** —— 没理由, 就是想插一句。真人天天这么干。
//
// 把②当成「概率缩放」是错的: 它不是噪声, 它是**另一种真实的动机**。
//
// ⚠️ 但②**必须被配额兜住**: 10% × 200 条/天 = 20 次插话 ——
// 有配额在, 那只是"20 次机会里用掉 3 次"; 没有配额, 那就是刷屏。
// 随机是瞬时意愿, 配额是预算, 两者不是一回事, 缺一不可。
//
// ⚠️ 配额**随时间释放**, 不许先到先得:
//   按先到先得, 一个热闹的早上会把当天 3 次全用光 ——
//   于是它上午话痨、下午到晚上完全消失。那是明显的机器人特征。
//
// ⚠️ **被群友说了就稳定在 1%** (2026-10-14 定)。
// **不自动恢复** —— 没有"过几天就好了"这回事: 一旦回涨, 群友会看到它又开始了,
// 而"被说过之后又犯"比一开始就话多更难受。要放开只能 owner 手动 ClearChimeComplaint。
// 这是这个功能唯一的安全阀: 插话是"打扰"那一侧的例外, 而例外的代价
// 只有群里的人能判定 —— 它自己判不出来。所以**由他们说了算**。
// ─────────────────────────────────────────────────────────────

const (
	// ChimeRollPct 常态随机参与概率 (%)。
	ChimeRollPct = 10
	// ChimeRollPctAfterComplaint 被群友说过之后的概率。
	// ⚠️ 不是 0 —— 归零等于"永远闭嘴", 而它只是**收敛**, 不是禁言。
	// 留着 1% 是为了它还能偶尔冒个泡: 一个人被说了之后不会从此消失。
	ChimeRollPctAfterComplaint = 1
	// ChimeQuotaPerDay 每天插话上限。
	// ⚠️ 要**小** —— 多了就是「永远在找存在感」, 那是另一个极端, 一样不像人。
	ChimeQuotaPerDay = 3
	// ChimeTopicQuotaPerDay 话题通道**自己**的天花板。
	//
	// ⚠️ 话题命中**不占**上面那 3 次 —— 话题是**群友引起的**, 不该记在它自己头上
	// (2026-10-14 定)。它不是"我想说话", 是"你们聊的这个我正好知道"。
	//
	// ⚠️ 但**必须另有一个天花板**: 只靠 25 分钟冷却的话, 一个热闹的群里
	// 它一天能插到 40 次 —— 那不叫"偶尔参与", 那叫住在群里。
	// 独立预算 ≠ 无预算, 只是分开记账。
	ChimeTopicQuotaPerDay = 8
	// ChimeTopicPerMessages 每多少条**群消息**换一次话题插话额度。
	//
	// ⚠️ **按比例, 不按固定次数** (2026-10-14 定):
	// 固定 8 次在 1000 条/天的群里等于没有, 在 20 条/天的群里等于刷屏。
	// 群里聊得多, 它自然被带到的机会就多 —— 那才是真实的量。
	//
	// 60 条换 1 次: 1000 条 → 16 次 (卡在 15 上限); 100 条 → 2 次; 20 条 → 1 次。
	ChimeTopicPerMessages = 60
	// ChimeTopicQuotaMax 按比例算出来的**硬天花板**。
	// 比例本身也要有顶 —— 否则一个 10000 条的群会把它喂成话痨。
	ChimeTopicQuotaMax = 15
	// ChimeCooldown 两次插话之间的最小间隔。
	// 防的是"连着两条都命中" —— 连着插话比不插更烦人。
	ChimeCooldown = 25 * time.Minute
)

// ChimeInput 插话判定的全部输入。
type ChimeInput struct {
	// TopicMatch 话题命中: 它的灵光/学过的 与当前群里聊的重合。
	TopicMatch bool
	// Roll 0-99 的随机数 (调用方掷, 便于测)。
	Roll int
	// UsedToday 今天**随机通道**已经插过几次。
	UsedToday int
	// TopicUsedToday 今天**话题通道**已经说过几次。两笔账分开记。
	TopicUsedToday int
	// GroupMsgsToday 今天这个群里**别人说了多少条** —— 话题额度按它算。
	// ⚠️ 数别人的, 不数它自己的: 它自己插的话不该反过来给它涨额度。
	GroupMsgsToday int
	// WakingElapsed 今天**醒着的时间**过了几分之几 (0-1)。配额据此释放。
	WakingElapsed float64
	// SinceLast 距上次插话多久。零值 = 从没插过。
	SinceLast time.Duration
	// Complained 这个群里有没有人说过它插话太多。
	// ⚠️ 用 bool 而不是 `RatePct int` —— 那样零值会变成「0% = 永久禁言」,
	// 而"没抱怨过"和"抱怨到禁言"是两件完全不同的事。
	Complained bool
}

// ChimeDecision 插话裁决。
type ChimeDecision struct {
	Speak bool
	// Topic 走的是**话题通道**吗 —— 记账要用 (两笔账分开记)。
	// ⚠️ 不能靠 Reason 字符串前缀判 —— 文案会改, 而记账错了会让配额静默失效。
	Topic  bool
	Reason string
}

// ChimeRate 当前的随机参与概率 (%)。
func ChimeRate(complained bool) int {
	if complained {
		return ChimeRollPctAfterComplaint
	}
	return ChimeRollPct
}

// DecideChime 插话裁决。**纯函数**。
//
// ⚠️ 顺序是刻意的: **先算预算, 再掷骰子**。
// 反过来的话, 掷中了却因为没预算而不说 —— 那次掷骰子就白费了,
// 而"今天运气不好"和"今天不想说话"在日志里长得一模一样。
func DecideChime(in ChimeInput) ChimeDecision {
	no := ChimeDecision{Speak: false, Reason: ""}
	// ① 冷却 —— 连着插话比不插更烦人。
	if in.SinceLast > 0 && in.SinceLast < ChimeCooldown {
		return no
	}
	// ② 两条通道**各自记账**, 各自随时间释放。
	//
	// ⚠️ 话题通道先判: 它是**有理由**的那条, 而随机是"没理由"。
	// 反过来的话, 一次随机命中会把话题额度也吃掉 —— 而话题是群友给的。
	elapsed := clampf01(in.WakingElapsed)
	if in.TopicMatch && in.TopicUsedToday < TopicBudgetFor(in.GroupMsgsToday) {
		return ChimeDecision{Speak: true, Topic: true, Reason: "插话 — 话题命中 (群友聊到了它知道的事)"}
	}
	// ③ 随机通道 —— 这才占那 3 次。
	// 话题额度用完时**落到这里**: 它仍可以"没理由地想插一句"。
	budget := int(math.Ceil(float64(ChimeQuotaPerDay) * elapsed))
	if in.UsedToday >= budget {
		return no
	}
	rate := ChimeRate(in.Complained)
	if in.Roll >= 0 && in.Roll < rate {
		if in.Complained {
			return ChimeDecision{Speak: true, Reason: "插话 — 随机参与 (被说过, 已收敛到 1%)"}
		}
		return ChimeDecision{Speak: true, Reason: "插话 — 随机参与 (没理由, 就是想插一句)"}
	}
	return no
}

// TopicBudgetFor 今天话题通道的额度 —— **按群消息量按比例**, 且有硬天花板。
//
// ⚠️ 至少给 1 次: 一个安静的群里它也该有机会说一次"我知道这个"。
// 但**不给 0** —— 0 会让"安静群"变成"永远不说话", 而那和死掉没区别。
func clampf01(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

// ——— 「被群友说了」的检测与落库 ———

// chimeComplaintMarkers 群友嫌它插话太多时说的话。
//
// ⚠️ 只收**指向插话行为本身**的说法。收「闭嘴」这类通用粗口会把
// 群友之间互喷也算到它头上 —— 那会让它在没人说它的时候自我禁言。
var chimeComplaintMarkers = []string{
	"别插嘴", "插什么嘴", "插嘴", "别打岔", "打什么岔",
	"别刷屏", "刷屏了", "安静点", "安静一点", "少说两句", "少说点",
	"话真多", "话太多了", "话好多", "谁问你了", "没问你", "又没问你",
	"别接话", "别搭话", "你插什么",
}

const (
	// ChimeComplaintLookback "被说过"要回看多久。
	ChimeComplaintLookback = 10 * time.Minute
	// ChimeComplaintMinRecentSelf 回看窗口里它自己至少要说几条, 才算"话多"。
	//
	// ⚠️ 2026-10-18 的误判是这条常量存在的唯一理由:
	// 2026-10-16 00:52 用户 @它说了句「别刷屏闭嘴」, 而它在前两小时里
	// 在群里只发了 1 条 (用户发了 22 条)。词表命中 → 永久收敛到 1% ——
	// 它被扣了一顶自己没戴的帽子。**一句话的词不够, 还要看它当时到底在不在说。**
	ChimeComplaintMinRecentSelf = 3
)

// noteChimeComplaint 判定并落库"被抱怨过", 返回是否真的记了一笔。
//
// 两件事必须同时成立:
//
//	① 这条话是在说它 (词 + atSelf, 见 ChimeComplaintHit);
//	② 它**最近确实在说话** —— 否则「别刷屏」只可能是在说别的 (或者玩笑)。
//
// 只判①就是 2026-10-16 那次误判: 它在群里两小时只说了 1 条, 却被永久降级。
func (e *Engine) noteChimeComplaint(sc SceneMsg, addressed bool) bool {
	if e.Store == nil || SceneKindOf(sc.SceneKey) != SceneKindGroup {
		return false
	}
	if !ChimeComplaintHit(sc.Text, addressed) {
		return false
	}
	n := e.Store.RecentSelfMsgCount(sc.SceneKey, clock.Now().Add(-ChimeComplaintLookback))
	if n < ChimeComplaintMinRecentSelf {
		log.Info("convo:chime", "被抱怨但它最近没在说话 (%d 条/%.0f 分钟) → **不算收敛**: %q",
			n, ChimeComplaintLookback.Minutes(), truncateRunes(strings.TrimSpace(sc.Text), 40))
		return false
	}
	if err := e.Store.MarkChimeComplaint(e.ownerKey(), sc.SceneKey); err != nil {
		// ⚠️ 只 warn: 记不上是遗憾, 但**不许**因为它打断这一轮的群裁决。
		log.Warn("convo:chime", "被抱怨记不上(那条安全阀不生效): %v", err)
		return false
	}
	log.Info("convo:chime", "被说过插话太多 (%d 条/%.0f 分钟) → 随机通道收敛到 %d%%: %q",
		n, ChimeComplaintLookback.Minutes(), ChimeRollPctAfterComplaint, truncateRunes(strings.TrimSpace(sc.Text), 40))
	return true
}

// ChimeComplaintHit 这句话是不是在说它插话太多。
//
// ⚠️ 必须**同时**满足: ① 命中抱怨词; ② 这条消息在跟它说话 (被 @ 或点名)。
// 只看词的话, 群友 A 对群友 B 说「你话真多」会把它的插话率打到 1% ——
// 而它什么都没做错, 且没有人会知道为什么它突然不说话了。
func ChimeComplaintHit(text string, atSelf bool) bool {
	if !atSelf {
		return false
	}
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	for _, m := range chimeComplaintMarkers {
		if strings.Contains(t, m) {
			return true
		}
	}
	return false
}

// EnsureChimeSchema 建表。
func EnsureChimeSchema(db *sql.DB) error {
	_, err := db.Exec("CREATE TABLE IF NOT EXISTS convo_chime_state (" +
		"owner TEXT NOT NULL, scene_key TEXT NOT NULL, complained_ts TEXT NOT NULL DEFAULT '', " +
		"used_day TEXT NOT NULL DEFAULT '', used_n INTEGER NOT NULL DEFAULT 0, " +
		"topic_n INTEGER NOT NULL DEFAULT 0, " +
		"last_ts TEXT NOT NULL DEFAULT '', updated_ts TEXT NOT NULL DEFAULT '', " +
		"PRIMARY KEY (owner, scene_key))")
	// ⚠️ 老库补列 —— **无条件跑**, 不挂在任何别的检查下面。
	// (就在这个项目里, 我曾把一组 ALTER 挂在 `stickers.note` 的检查里,
	//  结果 Pi 上 convo_memory.source 一个月都没加上, 记忆写入全废。)
	_, _ = db.Exec("ALTER TABLE convo_chime_state ADD COLUMN topic_n INTEGER NOT NULL DEFAULT 0")
	return err
}

// MarkChimeComplaint 记下「这个群说过它插话太多」。**落库**, 不是内存。
//
// ⚠️ 落库的理由与其余状态一致: 重启会丢掉内存, 而"重启之后又变得话多"
// 正是群友最会注意到的那种反复。
func (s *Store) MarkChimeComplaint(owner, sceneKey string) error {
	_, err := s.DB.Exec("INSERT INTO convo_chime_state (owner, scene_key, complained_ts, updated_ts) VALUES (?,?,?,?) "+
		"ON CONFLICT(owner, scene_key) DO UPDATE SET complained_ts = excluded.complained_ts, updated_ts = excluded.updated_ts",
		owner, sceneKey, clock.Now().Format(bodyTSLayout), clock.TodayTS())
	return err
}

// ClearChimeComplaint 撤销收敛 (owner 手动放行时用)。
func (s *Store) ClearChimeComplaint(owner, sceneKey string) error {
	_, err := s.DB.Exec("UPDATE convo_chime_state SET complained_ts = '' WHERE owner = ? AND scene_key = ?", owner, sceneKey)
	return err
}

// RecentSelfMsgCount 这个场景里, 它自己在 since 之后说了几条。
//
// ts 在库里是本地时间字符串 (bodyTSLayout), 所以这里也按同一格式传参 ——
// 用 time.Time 直接比较会让"最近"变成"从 2026 年起"。
func (s *Store) RecentSelfMsgCount(sceneKey string, since time.Time) int {
	var n int
	if s == nil || s.DB == nil {
		return 0
	}
	_ = s.DB.QueryRow("SELECT count(*) FROM convo_messages WHERE scene_key = ? AND dir = 'ai' AND ts >= ?",
		sceneKey, since.Format(bodyTSLayout)).Scan(&n)
	return n
}

// ChimeComplained 这个群有没有抱怨过。
func (s *Store) ChimeComplained(owner, sceneKey string) (bool, error) {
	var ts string
	err := s.DB.QueryRow("SELECT COALESCE(complained_ts,'') FROM convo_chime_state WHERE owner = ? AND scene_key = ?", owner, sceneKey).Scan(&ts)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(ts) != "", nil
}

// TopicBudgetFor 今天话题通道的额度 —— **按群消息量按比例**, 且有硬天花板。
//
// ⚠️ **按比例, 不按固定次数** (2026-10-14 定):
// 固定 8 次在 1000 条/天的群里等于没有, 在 20 条/天的群里等于刷屏。
// 群里聊得多, 它自然被带到的机会就多 —— 那才是真实的量。
//
// 60 条换 1 次: 1000 条 → 16 → 卡在 15; 100 条 → 2; 20 条 → 1。
//
// ⚠️ **至少给 1 次**: 0 会让"安静的群"变成"永远不说话", 那和死掉没区别。
func TopicBudgetFor(groupMsgs int) int {
	if groupMsgs <= 0 {
		return 1
	}
	n := int(math.Ceil(float64(groupMsgs) / float64(ChimeTopicPerMessages)))
	if n < 1 {
		n = 1
	}
	if n > ChimeTopicQuotaMax {
		n = ChimeTopicQuotaMax
	}
	return n
}
