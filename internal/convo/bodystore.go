package convo

import (
	"database/sql"
	"sync"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// ——— §4.2 身体的持久化与入账 ———

// 性别取值 (§4.2: 周期型按人格性别决定)。
const (
	GenderFemale = "female"
	GenderMale   = "male"
)

// LoadBody 读当前身体状态。**没有记录 = 没病** —— 读操作不写库, 与 LoadMood 同规。
func (s *Store) LoadBody() (BodyState, error) {
	var b BodyState
	err := s.DB.QueryRow("SELECT kind, COALESCE(cause,''), COALESCE(note,''), COALESCE(onset_ts,''), COALESCE(course_h,0) FROM convo_body_state WHERE id=1").
		Scan(&b.Kind, &b.Cause, &b.Note, &b.OnsetTS, &b.CourseH)
	if err == sql.ErrNoRows {
		return BodyState{}, nil
	}
	if err != nil {
		return BodyState{}, err
	}
	return b, nil
}

// SaveBodyEpisode 记一次发作。**只存事实**(发作时刻 + 病程), 严重度永远现算。
//
// ⚠️ 列名 7 个、占位符 7 个 —— P7 那次静默失败就是这两半对不上,
// 而且当时调用方还把 SQL 错误吞了。这里两个数都数得出来, 别再让它对不上。
func (s *Store) SaveBodyEpisode(kind, cause, note string, onset time.Time, course time.Duration) error {
	_, err := s.DB.Exec(
		"INSERT INTO convo_body_state(id, kind, cause, note, onset_ts, course_h, updated_ts) VALUES(1,?,?,?,?,?,?) "+
			"ON CONFLICT(id) DO UPDATE SET kind=excluded.kind, cause=excluded.cause, note=excluded.note, "+
			"onset_ts=excluded.onset_ts, course_h=excluded.course_h, updated_ts=excluded.updated_ts",
		kind, cause, note, onset.Format(bodyTSLayout), course.Hours(), clock.Now().Format(bodyTSLayout))
	return err
}

// ——— 可观测读数 (§12: 机制有没有生效, 要看数字) ———

// BodyStats 身体层的读数。
//
// ⚠️ **SickRunes / WellRunes 是这一批最要紧的一对数** ——
// §4.2 要求"消息长度 ×0.6", 而长度**不是系统能直接控制的**(它由模型生成)。
// 与其在提示词里写一句"短一点"就宣称做完了, 不如把病中与平时的句长**并排记下来**:
// 数字分不开 ⇒ 说明这条只能靠系统侧后处理, 那是 §8.4 的活, 要另外记账。
type BodyStats struct {
	Episodes    map[string]int // 型别 → 发作次数
	SickReplies int            // 身体不适期间发出的消息条数
	WellReplies int
	SickRunes   int // 对应消息的字符数合计
	WellRunes   int
	Solicits    int // §4.2 硬线命中: 索取安慰 (只记不拦, 见 BodySolicitHits)

	// 延迟系数样本。**仿真里唯一真正可归因于身体的那个数** ——
	// 延迟由系统算 (scaleSeconds), 不经过模型的嘴。
	SickFactorSum float64
	SickFactorN   int
}

var (
	bodyMu    sync.Mutex
	bodyStats = BodyStats{Episodes: map[string]int{}}
)

func ResetBodyStats() {
	bodyMu.Lock()
	bodyStats = BodyStats{Episodes: map[string]int{}}
	bodyMu.Unlock()
}

func BodyStatsSnapshot() BodyStats {
	bodyMu.Lock()
	defer bodyMu.Unlock()
	cp := bodyStats
	cp.Episodes = make(map[string]int, len(bodyStats.Episodes))
	for k, v := range bodyStats.Episodes {
		cp.Episodes[k] = v
	}
	return cp
}

func noteBodyEpisode(kind string) {
	bodyMu.Lock()
	bodyStats.Episodes[kind]++
	bodyMu.Unlock()
}

func noteBodyReply(sick bool, runes int) {
	bodyMu.Lock()
	if sick {
		bodyStats.SickReplies++
		bodyStats.SickRunes += runes
	} else {
		bodyStats.WellReplies++
		bodyStats.WellRunes += runes
	}
	bodyMu.Unlock()
}

// noteBodyDelay 记一次"病中最终延迟系数" (含心情那一档, 因为它俩是相乘的)。
func noteBodyDelay(sick bool, factor float64) {
	if !sick {
		return
	}
	bodyMu.Lock()
	bodyStats.SickFactorSum += factor
	bodyStats.SickFactorN++
	bodyMu.Unlock()
}

// AvgSickFactor 病中的平均延迟系数 (无样本 → 0)。
func (b BodyStats) AvgSickFactor() float64 {
	if b.SickFactorN == 0 {
		return 0
	}
	return b.SickFactorSum / float64(b.SickFactorN)
}

func noteBodySolicit() {
	bodyMu.Lock()
	bodyStats.Solicits++
	bodyMu.Unlock()
}

// AvgSickRunes / AvgWellRunes 病中与平时的平均句长 (无样本 → 0)。
func (b BodyStats) AvgSickRunes() float64 {
	if b.SickReplies == 0 {
		return 0
	}
	return float64(b.SickRunes) / float64(b.SickReplies)
}

func (b BodyStats) AvgWellRunes() float64 {
	if b.WellReplies == 0 {
		return 0
	}
	return float64(b.WellRunes) / float64(b.WellReplies)
}

// ——— 引擎侧: 读效果 / 每日入账 ———

// bodyEffect 当前身体 → 行为参数。**读不到 = 没病 = 照常** ——
// 与 moodWillingness() 读不到返回 1.0 同一条原则: 失败往"正常说话"倒。
func (e *Engine) bodyEffect(now time.Time) BodyEffects {
	b, err := e.Store.LoadBody()
	if err != nil {
		return BodyEffects{DelayFactor: 1.0, LengthFactor: 1.0}
	}
	return b.Effect(now)
}

// ensureBody 每次 tick 路过: 该发作就发作。
// "该好了"不用做任何事 —— 好是现算出来的 (BodyState.Refresh)。
func (e *Engine) ensureBody(now time.Time) {
	cur, err := e.Store.LoadBody()
	if err != nil {
		e.worldWarn("身体状态读取", err)
		return
	}
	cur.Refresh(now)
	if cur.Active {
		return // 病程或尾巴期内不会再生一场病
	}
	// 周期型优先: 它有**绝对相位**, 不该被事件型的冷却挤掉。
	if ep, ok := e.cycleEpisode(now); ok {
		e.startBodyEpisode(ep)
		return
	}
	if !cur.Resting(now) {
		return // 上一场刚走完, 冷却期内
	}
	env, ok := TodayEnv()
	if !ok {
		return // 没有环境数据源 = 没有"天气骤变"这个因, 就不硬造病
	}
	// ⚠️ 骰子用**确定性日骰**而不是 Dice —— 这个函数每分钟都会被路过, 每 tick 掷三次
	// 会把整条随机流搅烂 (A/B 对照就没法归因了); 而"今天会不会病"本来就该**一天之内恒定**。
	// 顺带: 确定性日骰让仿真完全可复现, 也让这一层不消耗引擎的随机预算。
	day := clock.Today()
	ep, sick := BodyOnsetFromEnv(env, BodyRoll{
		Cold:     DayRoll(day+"|cold", 100),
		Insomnia: DayRoll(day+"|insomnia", 100),
		Booze:    DayRoll(day+"|booze", 100),
	}, now)
	if !sick {
		return
	}
	e.startBodyEpisode(ep)
}

func (e *Engine) startBodyEpisode(ep BodyState) {
	onset, ok := ep.Onset()
	if !ok {
		return
	}
	if err := e.Store.SaveBodyEpisode(ep.Kind, ep.Cause, ep.Note, onset, ep.Course()); err != nil {
		e.worldWarn("身体入账", err) // 这一场不会发生 —— 必须出声, 否则"她从不生病"看不出是坏了
		return
	}
	noteBodyEpisode(ep.Kind)
	log.Info("convo:engine", "身体: %s 发作 (病程 %.0fh · 尾巴: %s)", BodyLabel(ep.Kind), ep.Course().Hours(), ep.Note)
}

// cycleEpisode 周期型 (§4.2: 女性人格默认开启, 男性人格天然无此项)。
//
// ⚠️ 两处"未提供就加戏"的闸门:
//   - 性别未指定 → 不产生周期 (零值 = 未提供 != 女性);
//   - 人格没有创建时刻 → 也**不产生** (拿 now 当锚点会让新人格当天就来经期)。
func (e *Engine) cycleEpisode(now time.Time) (BodyState, bool) {
	p, err := e.Store.LoadProfile()
	if err != nil || p == nil {
		return BodyState{}, false
	}
	c := BodyCycle{Enabled: p.Gender == GenderFemale && !p.BodyCycleOff}
	if !c.Enabled {
		return BodyState{}, false
	}
	anchor, perr := time.ParseInLocation(bodyTSLayout, p.CreatedTS, time.Local)
	if perr != nil {
		if d, derr := time.ParseInLocation("2006-01-02", p.CreatedTS, time.Local); derr == nil {
			anchor = d
		} else {
			return BodyState{}, false
		}
	}
	// 相位: 由人格名派生, 免得每个人格的经期都恰好从创建那天算起 (确定性, 可复现)。
	anchor = anchor.AddDate(0, 0, -DayRoll(p.Name, BodyCycleDays))
	return BodyCycleEpisode(c, anchor, now)
}
