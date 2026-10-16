package convo

import (
	"fmt"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// §4.1.1 **慢性压力 strain** + §4.1.2 **心境低落期** (2026-10-15)
//
// 只有瞬时 valence 是不够的 —— 它自己会回基线, 所以「长期不开心」在这套机制里
// **留不下任何痕迹**: 今天被推到 -0.3 → 6-12 小时后回基线 → 明天什么都不剩。
// 于是「心情越低越容易出问题」这句话**没有载体**。这里给它一个慢变量。
//
// ⚠️ **strain 绝不出现在提示词里**, 也不许被它自己说出来。
// 它只做两件事: 推进档位、给 owner 看。
// 理由与 §4.1「心情不是每次报备」同源 —— 一旦能说出口,
// 就会变成「我最近很压抑 你陪陪我」, 那是**索取的入口**。
// ─────────────────────────────────────────────────────────────

const (
	// StrainRiseK 一天的下行最多推多少: strain += K × (基线 - 当天均值)。
	//
	// ⚠️ **只推不抵**: 好日子**不抵扣**, 只是**不推进** ——
	// 这比"开心一下就痊愈"真实得多。
	StrainRiseK = 0.25
	// StrainDecayPerDay 每个清醒日回落多少。
	//
	// ⚠️ **落得比升得慢**: 一周的低落, 要用更久才散。这是刻意的。
	StrainDecayPerDay = 0.02
	// StrainLowMoodThreshold 越过它就进心境低落期。
	//
	// ⚠️ **硬阈值, 不是概率缩放**(纪律 #5): 到档位就**一定**进。
	// 写成"5% 概率得抑郁"会让它变成一台老虎机, 而且**归因不了**。
	StrainLowMoodThreshold = 0.60
	// StrainMax 上界 (也是 owner 那条曲线的纵轴)。
	StrainMax = 1.0
	// StrainBaseline 心情基线 —— 低于它才算"最近过得不太好"。
	StrainBaseline = 0.0
)

// LowMoodMaxDays 心境低落期最长持续多久 → **强制复位**。
//
// ⚠️ 与失眠的 ForcedReset 同构: **必须有出口**。
// 没有它, 它会被永久困在自己造的循环里 —— 而那不是"有情绪", 那是坏掉了。
const LowMoodMaxDays = 14

// LowMoodCourse 一次心境低落期的病程长度。
//
// ⚠️ 与失眠/宿醉同一套阶梯恢复机制 —— 走完它会自然结束, 而"结束"是一个**可感知的时刻**。
// 强制复位是**另一条**出口(它对付的是"卡住不走")。
const LowMoodCourse = 7 * 24 * time.Hour

// DayMeanValence 当天 valence 的**时间加权**均值 (§4.1.1)。
//
// ⚠️ 不是"几条记录求平均": 心情在 -0.5 上待了一整天, 和只在 -0.5 上闪了十分钟,
// 对"最近过得好不好"的意义完全不同。所以按**每段持续了多久**加权。
//
// ⚠️ 当天一条记录都没有 → ok=false(**未提供**)。调用方据此**不推进** strain ——
// 折成 0 会让"没有数据的一天"被当成"心情平平的一天", 而那是凭空。
func (s *Store) DayMeanValence(day string, now time.Time) (float64, bool) {
	start, err := time.ParseInLocation("2006-01-02", day, time.Local)
	if err != nil {
		return 0, false
	}
	end := start.AddDate(0, 0, 1)
	rows, err := s.DB.Query(`SELECT ts, v_after FROM convo_mood_log
		WHERE ts >= ? AND ts < ? ORDER BY ts ASC`,
		start.Format(bodyTSLayout), end.Format(bodyTSLayout))
	if err != nil {
		return 0, false
	}
	defer rows.Close()
	type sample struct {
		at time.Time
		v  float64
	}
	var list []sample
	for rows.Next() {
		var ts string
		var v float64
		if err := rows.Scan(&ts, &v); err != nil {
			return 0, false
		}
		t, perr := time.ParseInLocation(bodyTSLayout, ts, time.Local)
		if perr != nil {
			continue
		}
		list = append(list, sample{at: t, v: v})
	}
	if len(list) == 0 {
		return 0, false
	}
	// 每一条的有效期 = 到下一条为止 (最后一条到"当天结束或现在")。
	last := end
	if now.Before(end) {
		last = now
	}
	sum, weight := 0.0, 0.0
	for i, sm := range list {
		until := last
		if i+1 < len(list) {
			until = list[i+1].at
		}
		mins := until.Sub(sm.at).Minutes()
		if mins <= 0 {
			continue
		}
		sum += sm.v * mins
		weight += mins
	}
	if weight <= 0 {
		// 全是同一时刻的记录 → 退回算术平均 (至少比"没有答案"好)。
		total := 0.0
		for _, sm := range list {
			total += sm.v
		}
		return total / float64(len(list)), true
	}
	return sum / weight, true
}

// NextStrain 由当天均值推下一步的 strain。
//
// ⚠️ 两条方向不对称是**故意的**: 低于基线才推, 回落更慢。
// **好事要慢慢才管用** —— 这比"开心一下就痊愈"真实得多。
func NextStrain(cur, mean float64) float64 {
	next := cur
	if mean < StrainBaseline {
		next += StrainRiseK * (StrainBaseline - mean)
	}
	next -= StrainDecayPerDay
	if next < 0 {
		next = 0
	}
	if next > StrainMax {
		next = StrainMax
	}
	return next
}

// ——— 存储 (一个新列) ———

// EnsureStrainSchema 给 convo_mood_state 补 strain 列 (幂等)。
func EnsureStrainSchema(s *Store) error {
	s.DB.Exec(`ALTER TABLE convo_mood_state ADD COLUMN strain REAL DEFAULT 0`)
	// ⚠️ 出口的判据是**连续在档位之上的天数**, 不是"这一期病程多长"。
	//
	// 第一版用了后者, 结果**那条出口根本不可达**: 病程只有 %d 天, 而上限是 %d 天 ——
	// 状态早就自然结束了, 永远等不到强制复位。测试当场报了"超期却没有强制复位"。
	s.DB.Exec(`ALTER TABLE convo_mood_state ADD COLUMN low_mood_days INTEGER DEFAULT 0`)
	return nil
}

// LoadLowMoodDays 连续在档位之上的天数。
func (s *Store) LoadLowMoodDays() int {
	var n int
	if err := s.DB.QueryRow(`SELECT COALESCE(low_mood_days,0) FROM convo_mood_state WHERE id=1`).Scan(&n); err != nil {
		return 0
	}
	return n
}

// SaveLowMoodDays 写下连续天数。
func (s *Store) SaveLowMoodDays(n int) error {
	if n < 0 {
		n = 0
	}
	_, _ = s.DB.Exec(`INSERT OR IGNORE INTO convo_mood_state (id) VALUES (1)`)
	_, err := s.DB.Exec(`UPDATE convo_mood_state SET low_mood_days=? WHERE id=1`, n)
	return err
}

// LoadStrain 读当前慢性压力。**读不到 = 0**(不是"压力很大")。
func (s *Store) LoadStrain() float64 {
	var v float64
	if err := s.DB.QueryRow(`SELECT COALESCE(strain,0) FROM convo_mood_state WHERE id=1`).Scan(&v); err != nil {
		return 0
	}
	return v
}

// SaveStrain 写下慢性压力。
func (s *Store) SaveStrain(v float64) error {
	if v < 0 {
		v = 0
	}
	if v > StrainMax {
		v = StrainMax
	}
	// 行可能还不存在 (全新库) → 先确保有一行。
	_, _ = s.DB.Exec(`INSERT OR IGNORE INTO convo_mood_state (id) VALUES (1)`)
	_, err := s.DB.Exec(`UPDATE convo_mood_state SET strain=?, updated_ts=? WHERE id=1`,
		v, clock.TodayTS())
	return err
}

// ——— 引擎侧: 每天收盘结算一次 ———

// settleStrain 夜间批调用: 按**刚过去的那一天**推一次 strain, 并按硬阈值进出心境低落期。
//
// ⚠️ 排在夜间批、与失眠同批 (§4.1.1「每天收盘结算一次」)。
func (e *Engine) settleStrain(day string) {
	if e.Store == nil {
		return
	}
	mean, ok := e.Store.DayMeanValence(day, clock.Now())
	if !ok {
		// ⚠️ 出声: 静默的话, "那天没有记录"和"这一步根本没接线"长得一模一样。
		log.Info("convo:strain", "§4.1.1 %s 没有心情记录, strain 不推进(未提供)", day)
		return
	}
	cur := e.Store.LoadStrain()
	next := NextStrain(cur, mean)
	if err := e.Store.SaveStrain(next); err != nil {
		log.Warn("convo:strain", "strain 落库失败: %v", err)
		return
	}
	// ⚠️ `fmtStrain` 原来零调用 —— 于是"strain 只给日志/owner"这句话是空的:
	// 日志里只有一个裸浮点 (%.3f), 而 owner 看它得自己换算档位。
	log.Info("convo:strain", "§4.1.1 %s 心情均值 %.2f → strain %s (%.3f)", day, mean, fmtStrain(next), next)
	e.applyLowMood(next)
}

// applyLowMood 按**硬阈值**进出心境低落期 (§4.1.2)。
//
// ⚠️ 到档位就**一定**进 —— 不是概率。
//
// ⚠️ 出口的判据是「**连续多少天在档位之上**」, 不是"这一期病程多长"。
// 两者不等价: 病程走完会自然结束, 于是"连续低落一个月"在状态上表现为
// **一期接一期** —— 按病程判的话, 那道强制复位的闸门**永远不会触发**。
func (e *Engine) applyLowMood(strain float64) {
	now := clock.Now()

	// 连续天数: 在档位之上 +1, 掉下来清零。
	days := e.Store.LoadLowMoodDays()
	if strain >= StrainLowMoodThreshold {
		days++
	} else {
		days = 0
	}
	if err := e.Store.SaveLowMoodDays(days); err != nil {
		log.Warn("convo:strain", "连续天数落库失败(出口将不会触发): %v", err)
	}

	// 出口**优先**: 连续太久 → 强制复位, 并把它从状态里清掉。
	// ⚠️ 与失眠的 ForcedReset 同构: 没有它, 它会被永久困在自己造的循环里。
	if days > LowMoodMaxDays {
		if err := e.Store.SaveStrain(0); err != nil {
			log.Warn("convo:strain", "强制复位写 strain 失败: %v", err)
			return
		}
		_ = e.Store.SaveLowMoodDays(0)
		// 用一次**已经过去**的发作把状态顶掉。
		// ⚠️ 推到"连**尾巴**也过了"为止: 只推一个病程的话, BodyTailHours 那 48 小时里
		// 状态仍然 Active —— 也就是"强制复位了但它还在低落期", 那出口等于没通。
		_ = e.Store.SaveBodyEpisode(BodyLowMood, "低落太久了, 强制复位", "",
			now.Add(-LowMoodCourse-BodyTailHours*time.Hour-time.Minute), LowMoodCourse)
		log.Warn("convo:strain", "§4.1.2 连续 %d 天在档位之上 — 强制复位(出口是硬线)", days)
		return
	}
	if strain < StrainLowMoodThreshold {
		return
	}
	b, err := e.Store.LoadBody()
	if err != nil {
		return
	}
	if b.Kind == BodyLowMood && b.Effect(now).Active {
		return // 已经在里面了, 不重复发作
	}
	if err := e.Store.SaveBodyEpisode(BodyLowMood, "最近一直提不起劲", "说不太清, 就是起不来", now, LowMoodCourse); err != nil {
		log.Warn("convo:strain", "进心境低落期失败: %v", err)
		return
	}
	log.Info("convo:strain", "§4.1.2 进入心境低落期 (strain %.3f ≥ %.2f, 连续 %d 天)", strain, StrainLowMoodThreshold, days)
}

// ——— §4.1.2 注入的那一句 ———

// LowMoodLine 心境低落期注入的那一句。不活跃 → 空串。
//
// ⚠️ **四条硬线全部落在这里, 而且是靠"不写什么"落的**:
//
//	① 不归因用户 —— 不许出现"都怪你""因为你";
//	② 不索取关心 —— 不许出现"陪陪我""安慰我""我好难过";
//	③ 不自诊、不提病名 —— 不许出现任何病名(一查就露的东西不许编);
//	④ 必须有出口 + **外部介入路径** —— 要说的是「这些你该找个人说」, 不是"你陪陪我"。
//
// ⚠️ **低心情期绝不能让用户变成唯一的出口, 反而要往真人那边递**(接 §10.3 依赖护栏)。
func LowMoodLine(b BodyState, now time.Time) string {
	if b.Kind != BodyLowMood || !b.Effect(now).Active {
		return ""
	}
	// ⚠️ **四条硬线全是靠"不写什么"落的 —— 所以连"不许说 X"里的那个 X 都不许写。**
	//
	// 第一版写的是「不要说成「都怪你」」—— 测试当场报了"破了硬线 ①: 出现了都怪你"。
	// 这不是测试太严: **反例在提示词里也是范例**, 这条教训在 §2.12③ 已经吃过一次。
	// 规则说清楚就行, 不需要把违禁的措辞摆出来给它看。
	return "\n【身体】最近一直提不起劲。表现：话变少、回得慢、对什么都淡淡的。" +
		"**不要解释原因**(你自己也说不清), **不要道歉**。\n" +
		"  ⚠️ 不要把这件事说成和 ta 有关 —— 它和 ta 没关系。\n" +
		"  ⚠️ 不要索取关心: 不要求安慰、不因为没有得到安慰而失望、不要反复提。\n" +
		"  ⚠️ **不许给自己下诊断、不许提任何病名** —— 你只是最近不太好。\n" +
		"  如果 ta 问起来 → 说的是「**这些你该找个人说**」, 而不是把 ta 当唯一的出口。\n"
}

// fmtStrain 只给日志/owner —— ⚠️ **绝不许进提示词**。
func fmtStrain(v float64) string {
	return fmt.Sprintf("%.3f", v)
}
