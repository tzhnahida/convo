package convo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tzhnahida/convo/internal/log"
)

// ═══════════════════════════════════════════════════════════════════════════
// §3.6 世界模型的 API 生成 —— **生成器 + 预算闸门**
//
// 这一批的第一等公民不是"能生成", 而是**闸门**: 用户批准了这条新增的模型调用
// 路径 (L2), 但在这之前它没有任何预算约束 —— 一条没有闸门的新路径, 出问题的
// 方式不是"生成得丑", 而是"某天账单翻十倍"或"主路径被一次网络往返卡住"。
//
// ——— 为什么必须是**夜间批处理**, 不能是 tick 的活 ———
//
// 设计稿 §3.6.2 把时机钉死在"每夜一次 (与 §4.4.2 夜间整合同批)"。三个理由缺一不可:
//
//	① 成本:  1 次/天 vs 1440 次/天。设计稿实算的年成本 ¥1.6 (§3.6.6) **建立在每夜 1 次上**;
//	② 质量:  一天内碎成多次生成, 事件就退化成"每小时一条小噪音", 而不是"今天这一天"
//	         (§3.6.1 要的语境自洽, 前提是**一次看全**这一天的输入);
//	③ 主路径: tick 要在秒级给出回复, 它**不能等一次网络往返** —— 把 LLM 调用塞进 tick,
//	         等于让"它在不在线"取决于第三方 API 的抖动。
//
// 所以本模块只对外给**同步函数**, 由主控决定何时调。但它不指望调用方自觉:
// **预算闸门就是防接线错误的第二道保险** —— 就算被接到 tick 上, 也只会烧掉 3 次调用,
// 之后全天走模板, 世界照常运转。
//
// ——— 与现行代码同一套纪律 ———
//
//	零值 = 未提供 ≠ 极值: 预算零值 → **默认**(不是"不限制"); p_trigger 用指针区分
//	                      "没给"(→ 默认概率) 与 "给了 0"(→ 模型明说不会发生 → 丢);
//	失败往"正常"倒:      任何失败 (网络/解析/超时/超预算) → 退回确定性模板,
//	                     **返回的池子永远非空** —— 世界停摆比事件平庸糟得多;
//	派生量不落库:        概率、掷骰结果、池子内容都不存; 库里只存**事实**
//	                     (什么时候调过、成没成、为什么没成)。
// ═══════════════════════════════════════════════════════════════════════════

// ——— 预算闸门 ———

// 闸门默认值。**都是硬数字, 不是"看着办"**。
const (
	// EventGenDefaultCallsPerDay 每天最多几次生成调用。
	//
	// 3 = 每夜 1 次 + 最多 1 次重试 + 1 次人工补跑。它同时是"最坏情况成本上限":
	// 设计稿的 ¥1.6/年是按 365 次算的, 闸门把它钉在 365×3 次以内 (仍是个位数人民币),
	// 而**没有闸门时的最坏情况是无限**。
	EventGenDefaultCallsPerDay = 3
	// EventGenDefaultCandidates 一次最多收几条候选 (§3.6.3: 池子 3-5 条)。
	EventGenDefaultCandidates = 5
	// EventGenHardMaxCandidates 候选条数的**硬上限** —— 配置写多大都不许越过它。
	EventGenHardMaxCandidates = 8
	// EventGenDefaultAttempts 一次生成最多调几次模型 (1 首投 + 1 重试)。
	EventGenDefaultAttempts = 2
	// EventGenCallTimeout 单次调用的超时。
	//
	// 超时也是闸门的一部分: 没有它, 一次挂死的请求会让夜间批处理永远不返回,
	// 而"失败"最终会以"今天没有世界事件"的形式出现 —— 恰恰是本模块最不能接受的结果。
	EventGenCallTimeout = 25 * time.Second
	// EventGenDefaultLookback 提示词里回看几天的世界事件 (§3.6.2: 最近 7 天, 避免重复)。
	EventGenDefaultLookback = 7
	// EventGenDefaultPTrigger 模型没给 p_trigger 时的默认发生概率。
	// 0.5 = 中庸: 既不是"必然发生"(生活变连续剧), 也不是"基本不发生"(池子形同虚设)。
	EventGenDefaultPTrigger = 0.5
	// EventGenMaxTextRunes 单条事件描述的字数上限。
	// §3.6.3 要的是"一句话"; 超了说明模型没按契约写, 与时刻解析不出同一条纪律 —— 丢。
	EventGenMaxTextRunes = 60
	// EventGenFallbackCount 模板兜底取几条。
	EventGenFallbackCount = 3
)

// 失败/降级原因 (进 EventGenStats.Reasons 与调用账本, 也是报告里"为什么退回"的答案)。
const (
	EventGenReasonNoChatter = "no_chatter" // 没有模型入口 → 直接用模板
	EventGenReasonBudget    = "budget"     // 日配额用尽, **调用根本没发出去**
	EventGenReasonChatError = "chat_error" // 网络/服务错误
	EventGenReasonTimeout   = "timeout"    // 超时
	EventGenReasonBadJSON   = "bad_json"   // 解析不出约定形状
	EventGenReasonNoEvents  = "no_events"  // 解析出来了, 但一条都没过硬校验
)

// EventGenBudget 是这条新增模型调用路径的预算闸门配置。
//
// ⚠️ **零值 = 用默认, 不是"不限制"**。这是本模块最容易做歪、后果最贵的一处:
// 一个 int 零值如果被解释成 0 次, 是"生成从不发生"; 被解释成无限, 是"账单无限"。
// 两者都错 —— 所以零值一律折成 EventGenDefaultCallsPerDay。
type EventGenBudget struct {
	CallsPerDay  int           // ≤0 → EventGenDefaultCallsPerDay
	Candidates   int           // ≤0 → EventGenDefaultCandidates; 硬夹到 [1, EventGenHardMaxCandidates]
	Attempts     int           // ≤0 → EventGenDefaultAttempts (含首投)
	Timeout      time.Duration // ≤0 → EventGenCallTimeout
	LookbackDays int           // ≤0 → EventGenDefaultLookback
}

// withDefaults 把零值折成默认 —— **唯一**一处解释预算的地方。
func (b EventGenBudget) withDefaults() EventGenBudget {
	if b.CallsPerDay <= 0 {
		b.CallsPerDay = EventGenDefaultCallsPerDay
	}
	if b.Candidates <= 0 {
		b.Candidates = EventGenDefaultCandidates
	}
	if b.Candidates > EventGenHardMaxCandidates {
		b.Candidates = EventGenHardMaxCandidates
	}
	if b.Candidates < 1 {
		b.Candidates = 1
	}
	if b.Attempts <= 0 {
		b.Attempts = EventGenDefaultAttempts
	}
	if b.Timeout <= 0 {
		b.Timeout = EventGenCallTimeout
	}
	if b.LookbackDays <= 0 {
		b.LookbackDays = EventGenDefaultLookback
	}
	return b
}

var (
	eventGenBudMu sync.Mutex
	eventGenBud   EventGenBudget // 零值 —— 由 withDefaults 折成默认
)

// SetEventGenBudget 设置预算闸门 (主控从 convo.yaml 读出来时调一次)。
// 传零值 = 回到默认, 与"不设置"完全等价。
func SetEventGenBudget(b EventGenBudget) {
	eventGenBudMu.Lock()
	eventGenBud = b
	eventGenBudMu.Unlock()
}

// EffectiveEventGenBudget 读**生效值** (零值已折成默认)。
// 报告与管理页应该打印它, 而不是打印配置原值 —— 否则"没配"和"配成默认"看起来不一样。
func EffectiveEventGenBudget() EventGenBudget {
	eventGenBudMu.Lock()
	b := eventGenBud
	eventGenBudMu.Unlock()
	return b.withDefaults()
}

// ——— 可观测 (§12: 机制有没有生效, 要看数字) ———
//
// ⚠️ 这张表的存在理由是**一个具体的误判**:
// "AI 生成从没成功过" 与 "世界很平静" 在报告里长得一模一样 —— 都是"今天没几条事件"。
// 没有 Fallbacks / Reasons / LastReason, 退回到模板这件事就是**静默**的,
// 而静默的降级等于没有降级 (P7 的教训: 静默的不是数据库, 是调用方)。
type EventGenStats struct {
	Calls        int            `json:"calls"`         // 真正发给模型的调用次数 (被闸门挡下的不算)
	Success      int            `json:"success"`       // 产出可用池、**没有降级**的次数
	ModelFail    int            `json:"model_fail"`    // 模型/解析/协议失败次数
	BudgetDenied int            `json:"budget_denied"` // 被日配额挡下、调用根本没发出去的次数
	Fallbacks    int            `json:"fallbacks"`     // 退回确定性模板的次数
	Produced     int            `json:"produced"`      // 模型产出并**收下**的候选条数
	RolledOut    int            `json:"rolled_out"`    // 按 p_trigger 判定"今天没发生"的条数
	TemplateEvts int            `json:"template_evts"` // 模板兜底产出的条数
	Dropped      map[string]int `json:"dropped"`       // 逐条丢弃原因分布 (bad_time / kind_unknown / …)
	Reasons      map[string]int `json:"reasons"`       // 整次降级原因分布
	LastReason   string         `json:"last_reason"`   // 最近一次降级原因 —— 一眼看到"为什么退回"
	Samples      []string       `json:"samples"`       // 头几条失败的原始输出 (截断), 供人眼判读
}

// EventGenMaxSamples 报告里保留的失败样本条数 (与 FrameStats 同规)。
const EventGenMaxSamples = 4

var (
	eventGenMu    sync.Mutex
	eventGenStats = EventGenStats{Dropped: map[string]int{}, Reasons: map[string]int{}}
)

func resetEventGenStatsLocked() {
	eventGenStats = EventGenStats{Dropped: map[string]int{}, Reasons: map[string]int{}}
}

// ResetEventGenStats 清零 (仿真每次运行开始时调用; 正式进程可周期性滚动)。
func ResetEventGenStats() {
	eventGenMu.Lock()
	resetEventGenStatsLocked()
	eventGenMu.Unlock()
}

// EventGenStatsSnapshot 读一份快照 (深拷贝两个 map 与样本, 调用方可安全长期持有)。
func EventGenStatsSnapshot() EventGenStats {
	eventGenMu.Lock()
	defer eventGenMu.Unlock()
	s := eventGenStats
	s.Dropped = make(map[string]int, len(eventGenStats.Dropped))
	for k, v := range eventGenStats.Dropped {
		s.Dropped[k] = v
	}
	s.Reasons = make(map[string]int, len(eventGenStats.Reasons))
	for k, v := range eventGenStats.Reasons {
		s.Reasons[k] = v
	}
	s.Samples = append([]string(nil), eventGenStats.Samples...)
	return s
}

// FallbackRate 降级率 = 退回模板 / (成功 + 退回)。
//
// ⚠️ 它**只统计"生成过"的那些天** —— 没调用的日子不该稀释它, 否则一个从不调用的
// 部署会显示"降级率 0%", 看起来健康得不得了。
func (s EventGenStats) FallbackRate() float64 {
	total := s.Success + s.Fallbacks
	if total == 0 {
		return 0
	}
	return float64(s.Fallbacks) / float64(total)
}

// AvgProduced 平均每次成功收下几条候选 (对照 §3.6.3 的 3-5 条)。
func (s EventGenStats) AvgProduced() float64 {
	if s.Success == 0 {
		return 0
	}
	return float64(s.Produced) / float64(s.Success)
}

func noteEventGen(f func(*EventGenStats)) {
	eventGenMu.Lock()
	if eventGenStats.Dropped == nil {
		eventGenStats.Dropped = map[string]int{}
	}
	if eventGenStats.Reasons == nil {
		eventGenStats.Reasons = map[string]int{}
	}
	f(&eventGenStats)
	eventGenMu.Unlock()
}

func noteEventGenCall() { noteEventGen(func(s *EventGenStats) { s.Calls++ }) }

func noteEventGenSuccess(n int) {
	noteEventGen(func(s *EventGenStats) {
		s.Success++
		s.Produced += n
	})
}

// noteEventGenModelFail 记一次模型侧失败**并留样本** (raw = 这一轮的真实输出, 可能为空)。
func noteEventGenModelFail(reason, raw string) {
	noteEventGen(func(s *EventGenStats) {
		s.ModelFail++
		s.Reasons[reason]++
		s.LastReason = reason
		if len(s.Samples) < EventGenMaxSamples {
			s.Samples = append(s.Samples, reason+": "+truncRunes(raw, 160))
		}
	})
}

func noteEventGenDenied(reason string) {
	noteEventGen(func(s *EventGenStats) {
		s.BudgetDenied++
		s.Reasons[reason]++
		s.LastReason = reason
	})
}

func noteEventGenFallback(reason string, n int) {
	noteEventGen(func(s *EventGenStats) {
		s.Fallbacks++
		s.TemplateEvts += n
		if s.LastReason == "" {
			s.LastReason = reason
		}
	})
}

func noteEventGenDrop(reason string) {
	noteEventGen(func(s *EventGenStats) { s.Dropped[reason]++ })
}

func noteEventGenRolledOut(n int) {
	if n <= 0 {
		return
	}
	noteEventGen(func(s *EventGenStats) { s.RolledOut += n })
}

// ——— 调用账本: 配额落在库里, 不在内存里 ———
//
// 为什么配额必须落库: 夜间批处理可能崩在调用**之前**或**之后**。进程内计数器一重启
// 就归零, 于是"崩一次重跑一次"就是无限预算 —— 那不是闸门, 是装饰。
//
// 库里只存**事实**: 什么时候预留了一次调用、成没成、为什么没成。概率、池子内容都不存
// (派生量不落库 —— 存了就要回答"谁在什么时候重算它")。
const eventGenSchema = `
CREATE TABLE IF NOT EXISTS convo_eventgen_call (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    day TEXT DEFAULT '',
    ts TEXT DEFAULT '',
    attempt INTEGER DEFAULT 0,
    state TEXT DEFAULT 'open',
    reason TEXT DEFAULT '',
    events INTEGER DEFAULT 0,
    source TEXT DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_convo_eventgen_day ON convo_eventgen_call(day, id);
`

// EnsureEventGenSchema 幂等建表 (老库补表, 重复调用无副作用)。
// 与 agency / fatigue 同规: store.go 是既有文件, 不塞进它的 migrate(), 由主控在迁移链里调一次。
func EnsureEventGenSchema(s *Store) error {
	if s == nil || s.DB == nil {
		return fmt.Errorf("eventgen: store 未初始化")
	}
	_, err := s.DB.Exec(eventGenSchema)
	return err
}

// EventGenCall 是一次生成调用的事实记录。
type EventGenCall struct {
	ID      int64  `json:"id"`
	Day     string `json:"day"`
	TS      string `json:"ts"`
	Attempt int    `json:"attempt"`
	State   string `json:"state"` // open | ok | fail
	Reason  string `json:"reason"`
	Events  int    `json:"events"`
	Source  string `json:"source"` // llm | template
}

// ReserveEventGenCall 预留一次模型调用 —— **先扣预算, 再发请求**。
//
// 顺序不能反。先调用后计数的话, 一次卡死/崩溃的调用不计费, 调用方重试就是无限次;
// 先扣后调, 崩溃也照样占额度, 这才叫硬闸门。
//
// 检查与插入在**同一个事务**里 —— 否则并发下"读到 2、各插一条"就会把配额撑破。
//
// cap ≤ 0 → 用默认 (不是"不限制")。返回 ok=false = 今天已经用满, 调用**不许发出去**。
func (s *Store) ReserveEventGenCall(day, ts string, cap int, attempt int) (id int64, ok bool, err error) {
	if s == nil || s.DB == nil {
		return 0, false, errors.New("eventgen: store 未初始化")
	}
	if cap <= 0 {
		cap = EventGenDefaultCallsPerDay
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM convo_eventgen_call WHERE day=?`, day).Scan(&n); err != nil {
		return 0, false, err
	}
	if n >= cap {
		return 0, false, nil
	}
	res, err := tx.Exec(`INSERT INTO convo_eventgen_call(day, ts, attempt, state) VALUES(?,?,?,'open')`,
		day, ts, attempt)
	if err != nil {
		return 0, false, err
	}
	id, _ = res.LastInsertId()
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// FinishEventGenCall 给一次预留写上终态。写不上**不阻断主路径** (只出声)。
func (s *Store) FinishEventGenCall(id int64, ok bool, reason string, events int, source string) error {
	if s == nil || s.DB == nil || id == 0 {
		return nil
	}
	state := "fail"
	if ok {
		state = "ok"
	}
	_, err := s.DB.Exec(`UPDATE convo_eventgen_call SET state=?, reason=?, events=?, source=? WHERE id=?`,
		state, reason, events, source, id)
	return err
}

// EventGenCallsOn 某天已经用掉几次生成调用 (报告/管理页用)。
func (s *Store) EventGenCallsOn(day string) (int, error) {
	if s == nil || s.DB == nil {
		return 0, errors.New("eventgen: store 未初始化")
	}
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM convo_eventgen_call WHERE day=?`, day).Scan(&n)
	return n, err
}

// EventGenDaySummary 某天的 (调用次数, 降级次数, 收下条数) —— "AI 生成到底跑没跑通"的一行答案。
func (s *Store) EventGenDaySummary(day string) (calls, fails, events int, err error) {
	if s == nil || s.DB == nil {
		return 0, 0, 0, errors.New("eventgen: store 未初始化")
	}
	err = s.DB.QueryRow(`SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN state='fail' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(events),0) FROM convo_eventgen_call WHERE day=?`, day).Scan(&calls, &fails, &events)
	return calls, fails, events, err
}

// EventGenLedger 某天的调用账本 (新→旧), 供人事后翻。
func (s *Store) EventGenLedger(day string, limit int) ([]EventGenCall, error) {
	if s == nil || s.DB == nil {
		return nil, errors.New("eventgen: store 未初始化")
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.DB.Query(`SELECT id, day, ts, attempt, state, reason, events, source
		FROM convo_eventgen_call WHERE day=? ORDER BY id DESC LIMIT ?`, day, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventGenCall
	for rows.Next() {
		var c EventGenCall
		if err := rows.Scan(&c.ID, &c.Day, &c.TS, &c.Attempt, &c.State, &c.Reason, &c.Events, &c.Source); err != nil {
			continue
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RecentWorldEventLines 最近 N 天**已经入账**的世界事件 ("MM-DD 文本")。
//
// 这是 §3.6.2 要求的输入之一: "最近 7 天的世界事件(避免重复)"。
// 模板做不到的那件事 (不重复) 就靠这一段 —— 没有它, 模型只会换着说法把同一件事再讲一遍。
func (s *Store) RecentWorldEventLines(now time.Time, days, limit int) ([]string, error) {
	if s == nil || s.DB == nil {
		return nil, errors.New("eventgen: store 未初始化")
	}
	if days <= 0 {
		days = EventGenDefaultLookback
	}
	if limit <= 0 {
		limit = 12
	}
	from := now.AddDate(0, 0, -days).Format("2006-01-02 15:04:05")
	rows, err := s.DB.Query(`SELECT ts, text FROM convo_world_event WHERE ts >= ? ORDER BY id DESC LIMIT ?`, from, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var ts, text string
		if err := rows.Scan(&ts, &text); err != nil {
			continue
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		d := ts
		if len(ts) >= 10 {
			d = ts[5:10]
		}
		out = append(out, d+" "+text)
	}
	return out, rows.Err()
}

// ——— 进程内闸门 (无库路径) ———
//
// GenerateDayPool 的签名不带 store (设计稿给的形状就是这样), 所以那条路径的配额只能
// 记在进程内: 重启即忘。**这不是缺陷, 是分工** —— 正式路径走 GenerateNightPool, 配额在库里。
var (
	eventGenMemMu  sync.Mutex
	eventGenMemDay string
	eventGenMemN   int
)

func eventGenMemReserve(day string, cap int) bool {
	if cap <= 0 {
		cap = EventGenDefaultCallsPerDay
	}
	eventGenMemMu.Lock()
	defer eventGenMemMu.Unlock()
	if eventGenMemDay != day {
		eventGenMemDay, eventGenMemN = day, 0
	}
	if eventGenMemN >= cap {
		return false
	}
	eventGenMemN++
	return true
}

// eventGenReserve 预留一次调用: 有库走库 (跨重启硬约束), 无库/库坏走进程内闸门。
//
// 库坏的时候**不是放行**, 而是退回进程内闸门 + 出声 —— 静默放行会让配额形同虚设,
// 而静默拒绝又会让世界停摆。两条路都留着, 但都不能静默。
func eventGenReserve(st *Store, day string, now time.Time, capN, attempt int) (int64, bool) {
	if st == nil || st.DB == nil {
		return 0, eventGenMemReserve(day, capN)
	}
	id, ok, err := st.ReserveEventGenCall(day, now.Format("2006-01-02 15:04:05"), capN, attempt)
	if err != nil {
		log.Warn("convo:eventgen", "调用账本不可用 (%v) —— 本进程内继续封顶 %d 次/天", err, capN)
		return 0, eventGenMemReserve(day, capN)
	}
	return id, ok
}

func eventGenFinish(st *Store, id int64, ok bool, reason string, n int, source string) {
	if st == nil || st.DB == nil || id == 0 {
		return
	}
	if err := st.FinishEventGenCall(id, ok, reason, n, source); err != nil {
		log.Warn("convo:eventgen", "调用账本终态写不上 (%v) —— 配额计数会偏保守", err)
	}
}

// ——— 输出契约: 模型说什么形状, 我们怎么读懂 ———

// eventGenDraft 是设计稿 §3.6.3 的候选形状。
//
// ⚠️ 三个轴用指针, 是为了守住"**零值 = 未提供 ≠ 极值**":
// arousal=0 是"极度平静", intensity=0 是"毫无影响", p_trigger=0 是"不会发生" ——
// 它们都是**极值**, 而"模型没给这个字段"是完全不同的意思。用 float64 接的话,
// 这两种情况在 Go 里长得一模一样, 于是"没给"会被当成"模型说它不会发生"而丢掉。
//
// valence 用值类型: 0 在它上面本来就**是**中间 (中性), 未提供也落在中性, 两者等价。
type eventGenDraft struct {
	Kind      string   `json:"kind"`
	Desc      string   `json:"desc"`
	TS        string   `json:"ts"`
	Window    string   `json:"window"`
	Valence   float64  `json:"valence"`
	Arousal   *float64 `json:"arousal"`
	Intensity *float64 `json:"intensity"`
	Duration  string   `json:"duration"`
	PTrigger  *float64 `json:"p_trigger"`
	Source    string   `json:"source"`
}

type eventGenFrame struct {
	Events []eventGenDraft `json:"events"`
}

// eventGenKindRule 是 kind 的**档位表** (幅度小, 用档位; 不搞连续缩放)。
//
// 为什么要映射: 设计稿的 kind (social|stranger|weather|work|family|accident) 是**素材分类**,
// 而 WorldEvent.Kind 是**下游认识的枚举** (environment|calendar|social|relation|self)。
// 直接把素材分类写进 WorldEvent 会让下游的分支全部落空 —— 那是"看得见的错", 但一样是错。
//
// Intensity/Arousal 是**缺省档**: 模型没给强度时用它, 而不是用 0
// (一条强度 0 的事件在池子里是具尸体 —— 看得见, 但什么都不推动)。
type eventGenKindRule struct {
	Kind      string
	Source    string
	Intensity float64
	Arousal   float64
}

var eventGenKinds = map[string]eventGenKindRule{
	"social":   {Kind: "social", Source: "朋友", Intensity: 0.5, Arousal: 0.6},
	"stranger": {Kind: "social", Source: "陌生人", Intensity: 0.3, Arousal: 0.5},
	"weather":  {Kind: "environment", Source: "天气", Intensity: 0.4, Arousal: 0.5},
	"work":     {Kind: "calendar", Source: "工作", Intensity: 0.5, Arousal: 0.5},
	"family":   {Kind: "relation", Source: "家人", Intensity: 0.7, Arousal: 0.6},
	"accident": {Kind: "self", Source: "自己", Intensity: 0.8, Arousal: 0.7},
	// §4.2.4 **饮料事件** (2026-10-15)。
	//
	// ⚠️ 这是"间接影响心情"的落点: 它**不报心情**, 但你能从「今天买到一杯超难喝的」里听出来。
	// 强度刻意压低(0.3) —— 一杯饮料不该推动一整天的情绪。
	"drink": {Kind: "self", Source: "自己", Intensity: 0.3, Arousal: 0.4},
}

// 丢弃原因 (进 Dropped 分布)。
const (
	evgDropEmptyText = "empty_text"
	evgDropTooLong   = "too_long"
	evgDropKind      = "kind_unknown"
	evgDropBadTime   = "bad_time"
	evgDropWindow    = "out_of_window"
	evgDropPublic    = "public_fact"
	evgDropPZero     = "p_zero"
	evgDropDup       = "dup"
	evgNoteWinIgnore = "window_ignored" // 不是丢弃: 窗口认不出 → 忽略但留痕
)

// eventGenPublicFactWords 是 §3.6.4 红线的**兜底词表**。
//
// ⚠️ 判据不是这张表, 而是"**能不能被搜索引擎验证**": 可证伪的公共事件必须来自真实数据源。
// 词表永远不全 —— 所以它只做 fail-closed 的粗筛, 真正的防线是系统提示词里那条红线。
// 两个理由 (§3.6.4): ① 一查就露 = 拟人崩塌; ② **真有灾难时它在编造, 显得极其冷血。**
var eventGenPublicFactWords = []string{
	"地震", "台风", "海啸", "洪水", "火山", "疫情", "战争",
	"恐怖袭击", "爆炸", "失火", "火灾", "空难", "坠机", "核泄漏",
}

// ——— 入口 ———

// GenerateDayPool 生成"今天的事件池" —— **无库版** (仿真 / 测试 / 无 store 场景)。
//
// ⚠️ 两条返回值约定, 用错第二条就会让世界停摆:
//
//	events: **永远非空**。要么模型产出, 要么确定性模板兜底 —— 可以直接喂给 SeedDrafts。
//	err:    只在"连模板都给不出"时非 nil (理论上不会发生)。
//	        **降级到模板不是错误** —— 它走 log.Warn + EventGenStats + 调用账本三条痕迹,
//	        而不是一个调用方可能顺手 `return err` 掉的 error
//	        (与 GenerateSchedule 同规: 兜底路径返回可用结果, 痕迹留在读数里)。
//
// ⚠️ 这里原来有一个 `GenerateDayPool`(**无库版**) —— **已删**。
//
// 它自己的注释就写着「配额记在**进程内**, 重启即忘。**正式路径请用
// `GenerateNightPool`**」—— 也就是说它**自己声明了自己不是那条路**。
// 而它在生产与测试里的调用者都是 **0**。
//
// ⚠️ 无库版的配额闸门还有另一个毛病: 进程内计数**重启即忘**,
// 而那正是 §3.6.2 要防的("重启之后又能生成一次") —— 留着它等于留一条
// **绕过硬配额**的路, 而谁都不会注意到自己走错了那条。

// GenerateNightPool 是**唯一的入口** (夜间批处理调它, 每夜一次)。
//
// 那个 store 不是装饰, 它买来三样东西:
//   - §3.6.2 要求的输入: 最近 7 天的世界事件 / 今天的心情 / 今天的日程 —— 全在库里,
//     缺了它们, "语境自洽"和"不重复"两条就都只是提示词里的一句空话;
//   - 跨重启的**硬配额** (桩在 convo_eventgen_call 表里);
//   - 事后可翻的账本 (报告与管理页的那一行"生成跑没跑通")。
func GenerateNightPool(ctx context.Context, c Chatter, st *Store, env EnvDay, p *PersonaProfile, now time.Time) ([]WorldEvent, error) {
	return eventGenRun(ctx, c, st, env, p, now)
}

// eventGenRun 是两条入口共用的骨架: 时钟闸门 → 预算闸门 → 调用 → 失败退回。
func eventGenRun(ctx context.Context, c Chatter, st *Store, env EnvDay, p *PersonaProfile, now time.Time) ([]WorldEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	day := now.Format("2006-01-02")
	bud := EffectiveEventGenBudget()

	// ① 时钟闸门: 没有模型入口 → 直接模板。**仍然留痕** ——
	// 否则"这条路径从没接过线"会和"世界很平静"长得一样。
	if c == nil {
		pool := EventGenFallbackPool(env, p, now)
		noteEventGenDenied(EventGenReasonNoChatter)
		noteEventGenFallback(EventGenReasonNoChatter, len(pool))
		log.Warn("convo:eventgen", "没有模型入口 —— 今日事件池用确定性模板 (%d 条)", len(pool))
		return pool, nil
	}

	reason := ""
	var raw string
	for attempt := 0; attempt < bud.Attempts; attempt++ {
		// ② 预算闸门: **先扣后调**。拒绝 = 调用根本不发出去。
		callID, ok := eventGenReserve(st, day, now, bud.CallsPerDay, attempt)
		if !ok {
			// 预算拒绝只解释"为什么没再试一次"; 前面已经失败过的话, **降级原因仍是那次失败** ——
			// 否则报告里会显示成"是配额挡的", 而真正坏掉的那次调用被它盖住。
			if reason == "" {
				reason = EventGenReasonBudget
			}
			noteEventGenDenied(EventGenReasonBudget)
			break
		}
		noteEventGenCall()
		evs, why, out := eventGenOnce(ctx, c, st, env, p, now, bud)
		if why == "" && len(evs) > 0 {
			noteEventGenSuccess(len(evs))
			eventGenFinish(st, callID, true, "", len(evs), "llm")
			return evs, nil
		}
		reason, raw = why, out
		noteEventGenModelFail(reason, raw)
		eventGenFinish(st, callID, false, reason, 0, "llm")
	}
	if reason == "" {
		reason = EventGenReasonNoEvents
	}

	// ③ 失败退回: **世界照常运转**。池子非空是硬保证, 不是尽力而为。
	pool := EventGenFallbackPool(env, p, now)
	noteEventGenFallback(reason, len(pool))
	log.Warn("convo:eventgen", "今日事件池退回确定性模板 (%s, %d 条) —— 世界照常运转", reason, len(pool))
	return pool, nil
}

// eventGenOnce 一次模型调用 + 解析 + 校验。返回 (池子, 失败原因, 原始输出)。
// 失败原因 = "" 表示成功。原始输出无论如何都带回来 —— 失败样本是"该改提示词还是该改解析器"的唯一依据。
func eventGenOnce(ctx context.Context, c Chatter, st *Store, env EnvDay, p *PersonaProfile, now time.Time, bud EventGenBudget) ([]WorldEvent, string, string) {
	ins := eventGenGather(st, p, now, bud.LookbackDays)
	user := eventGenUserPrompt(env, p, ins, now, bud.Candidates)

	// 超时是闸门的一部分: 没有它, 一次挂死的请求会让夜间批处理永不返回。
	cctx, cancel := context.WithTimeout(ctx, bud.Timeout)
	defer cancel()
	out, err := c.Chat(cctx, eventGenSystemPrompt(), user)
	if err != nil {
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			return nil, EventGenReasonTimeout, out
		}
		return nil, EventGenReasonChatError, out
	}
	drafts, perr := eventGenParseDrafts(out)
	if perr != nil {
		return nil, EventGenReasonBadJSON, out
	}
	pool := eventGenBuild(drafts, now.Format("2006-01-02"), bud.Candidates)
	if len(pool) == 0 {
		// 解析成功但一条都没留下 —— 它**不是**"今天没事发生", 它是协议失败。
		// 这两种情况如果都缩成空池, 报告里就再也分不开了。
		return nil, EventGenReasonNoEvents, out
	}
	return pool, "", out
}

// eventGenParseDrafts 抽出候选列表。容忍两种常见形态: {"events":[…]} 与裸数组 […]。
//
// 为什么容忍裸数组: 模型漏掉外层对象是高频输出形态, 为它整批退回模板不划算
// (失败往"正常"倒)。但**只容忍这两种** —— 再多的宽容会让"解析成功"变成一个没有意义的词。
func eventGenParseDrafts(out string) ([]eventGenDraft, error) {
	if raw, err := extractJSON(out); err == nil {
		var f eventGenFrame
		if err := json.Unmarshal(raw, &f); err == nil && len(f.Events) > 0 {
			return f.Events, nil
		}
	}
	start := strings.IndexByte(out, '[')
	end := strings.LastIndexByte(out, ']')
	if start < 0 || end <= start {
		return nil, fmt.Errorf("eventgen: 没有 JSON 数组或对象")
	}
	var arr []eventGenDraft
	if err := json.Unmarshal([]byte(out[start:end+1]), &arr); err != nil {
		return nil, err
	}
	if len(arr) == 0 {
		return nil, fmt.Errorf("eventgen: 空数组")
	}
	return arr, nil
}

// eventGenCand 是过了硬校验、还没掷骰的候选。
type eventGenCand struct {
	ev WorldEvent
	p  float64
}

// eventGenBuild 把草稿折成事件池: 逐条硬校验 → 唯一化 → 按 p_trigger 掷骰 → 排序 → 封顶。
func eventGenBuild(drafts []eventGenDraft, day string, max int) []WorldEvent {
	var cands []eventGenCand
	seen := map[string]bool{}
	for _, d := range drafts {
		ev, p, why := eventGenValidate(d, day)
		if why != "" {
			noteEventGenDrop(why)
			continue
		}
		key := ev.Key()
		if seen[key] { // 模型复读自己是很常见的: 同一个池子里不该有两条一样的事
			noteEventGenDrop(evgDropDup)
			continue
		}
		seen[key] = true
		cands = append(cands, eventGenCand{ev: ev, p: p})
	}
	if len(cands) == 0 {
		return nil
	}

	// §3.6.3 的 p_trigger: "白天按此概率触发, 不是必然发生"。
	//
	// 为什么在这里掷 (而不是等到白天再掷): WorldEvent 没有 p 字段, 而取到点事件那条路
	// (DrawDueDrafts) 是既有文件 —— 不许改。所以概率在**生成时**掷: 入池的就是"今天就发生了"
	// 的那个子集, 对外表现与"白天按概率触发"完全一致 (生成 5 条, 当天只发生 1-2 条)。
	//
	// ⚠️ 概率可以**削减条数, 不能清零**: 全掷空就变成"今天没事发生",
	// 那正是本模块最不能接受的结果。池底硬保 1 条 (p 最高的那条)。
	var real []eventGenCand
	rolledOut := 0
	for _, c := range cands {
		if eventGenRoll(day, c.ev.Text) < c.p {
			real = append(real, c)
			continue
		}
		rolledOut++
	}
	if len(real) == 0 {
		best := cands[0]
		for _, c := range cands[1:] {
			if c.p > best.p {
				best = c
			}
		}
		real = append(real, best)
		rolledOut-- // 池底那一条不算"没发生"
	}
	noteEventGenRolledOut(rolledOut)

	sort.SliceStable(real, func(i, j int) bool { return real[i].ev.TS < real[j].ev.TS })
	if max > 0 && len(real) > max {
		real = real[:max]
	}
	out := make([]WorldEvent, 0, len(real))
	for _, c := range real {
		out = append(out, c.ev)
	}
	return out
}

// eventGenValidate 把一条草稿折成世界事件; 返回 (事件, 发生概率, 丢弃原因)。
//
// **时刻是硬约束** —— 与 convo 里 remember/promise 同一条纪律:
// 模型给的时刻解析不出, 就丢那一条。理由不是洁癖: 一条没有有效时刻的事件会被
// 塞进错误的时刻 (或当天零点), 于是"世界在日内会动"这件事会**假装**在动 ——
// 它成了参数缺失时最糟的那种失败: 有数据、有日志、但时间是假的。
func eventGenValidate(d eventGenDraft, day string) (WorldEvent, float64, string) {
	text := eventGenSanitizeText(d.Desc)
	if text == "" {
		return WorldEvent{}, 0, evgDropEmptyText
	}
	if len([]rune(text)) > EventGenMaxTextRunes {
		return WorldEvent{}, 0, evgDropTooLong
	}
	if w := eventGenPublicFactWord(text); w != "" {
		return WorldEvent{}, 0, evgDropPublic
	}
	rule, ok := eventGenKinds[strings.ToLower(strings.TrimSpace(d.Kind))]
	if !ok {
		return WorldEvent{}, 0, evgDropKind
	}
	hm, ok := eventGenParseHHMM(d.TS)
	if !ok {
		return WorldEvent{}, 0, evgDropBadTime
	}
	// window 是**可选**的软约束: 给了、认得出, 就要自洽 (时刻必须在窗口内);
	// 给了、认不出, 忽略它但留痕 —— 为一个可选字段的格式瑕疵丢掉一条好事件,
	// 是拿"严格"换"世界停摆", 不划算。
	if w := strings.TrimSpace(d.Window); w != "" {
		if ws, we, wok := eventGenParseWindow(w); wok {
			if hm < ws || hm > we {
				return WorldEvent{}, 0, evgDropWindow
			}
		} else {
			noteEventGenDrop(evgNoteWinIgnore)
		}
	}
	// p_trigger: 指针区分"没给"(→ 默认) 与 "给了 0"(→ 模型明说不会发生, 丢)。
	p := EventGenDefaultPTrigger
	if d.PTrigger != nil {
		v := eventGenAxis(*d.PTrigger)
		if v <= 0 {
			return WorldEvent{}, 0, evgDropPZero
		}
		p = clampF(v, 0, 1)
	}
	aro := rule.Arousal
	if d.Arousal != nil {
		aro = clampF(eventGenAxis(*d.Arousal), 0, 1)
	}
	inten := rule.Intensity
	if d.Intensity != nil && eventGenAxis(*d.Intensity) > 0 {
		inten = clampF(eventGenAxis(*d.Intensity), 0, 1)
	}
	src := eventGenSanitizeText(d.Source)
	if src == "" || len([]rune(src)) > 12 {
		src = rule.Source // 零值 = 未提供 → 档位默认
	}
	return WorldEvent{
		TS:        day + " " + hm + ":00",
		Kind:      rule.Kind,
		Source:    src,
		Text:      text,
		Valence:   clampF(eventGenAxis(d.Valence), -1, 1),
		Arousal:   aro,
		Intensity: inten,
		// 关系权重恒 1.0: 它是**关系层**的量 (§5.6)。在这里发明一个权重,
		// 就会和关系层将来的接线打架 —— 本模块不发明别人的量。
		Weight:   1.0,
		Duration: eventGenDuration(d.Duration),
	}, p, ""
}

// eventGenDuration 折成两种型 (§3.6.8)。
// 默认**脉冲**而不是背景: 背景型要进自传体记忆, 是个更重的承诺 ——
// 零值(未提供)时选轻的那一档, 是"失败往正常倒"在时长上的同一条规则。
func eventGenDuration(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "background", "背景", "持续", "长期", "week", "周":
		return "background"
	default:
		return "pulse"
	}
}

// eventGenAxis 把 NaN/Inf 压成 0 —— JSON 里它们不会出现, 但 Go 的 float 比较
// 对 NaN 全返回 false, 那会让 clampF 静默失效并让 NaN 一路流进心情。
func eventGenAxis(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

// eventGenSanitizeText 把空白压成单空格。
// 事件文本会被拼进对话的注入段 (它"今天发生的事"), 多行会破坏那一段的结构 ——
// 保持它是一句话。
func eventGenSanitizeText(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

// eventGenPublicFactWord 命中红线词表就返回那个词 (空串 = 没命中)。
func eventGenPublicFactWord(text string) string {
	for _, w := range eventGenPublicFactWords {
		if strings.Contains(text, w) {
			return w
		}
	}
	return ""
}

// eventGenParseHHMM 只认两种写法: "HH:MM" 与 "HH:MM:SS" (前后空白已去掉)。
// 其余 (空串 / "18:00-23:00" 这种区间 / "晚上八点") 一律**不认**。
func eventGenParseHHMM(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	var t time.Time
	var err error
	if t, err = time.Parse("15:04", s); err != nil {
		if t, err = time.Parse("15:04:05", s); err != nil {
			return "", false
		}
	}
	h, m := t.Hour(), t.Minute()
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return "", false
	}
	return fmt.Sprintf("%02d:%02d", h, m), true
}

// eventGenParseWindow 解析 "HH:MM-HH:MM"。
//
// 分隔符全角/波浪线/破折号一律折成 '-' —— 模型在这上面几乎必然会飘, 而这是**可选**字段,
// 为它丢掉一条好事件不划算 (所以返回 false 的处置是"忽略窗口", 不是"丢掉事件")。
// 跨午夜的窗口 (start > end) 在"当天 HH:MM"这个形状里表达不了 → 也算认不出。
func eventGenParseWindow(s string) (string, string, bool) {
	s = strings.TrimSpace(s)
	for _, sep := range []string{"～", "~", "—", "–", "－", "至"} {
		s = strings.ReplaceAll(s, sep, "-")
	}
	parts := strings.Split(s, "-")
	if len(parts) != 2 {
		return "", "", false
	}
	a, ok1 := eventGenParseHHMM(parts[0])
	b, ok2 := eventGenParseHHMM(parts[1])
	if !ok1 || !ok2 || a > b {
		return "", "", false
	}
	return a, b, true
}

// eventGenRoll 是**确定性**伪随机: 同一天同一条候选永远得到同一个数。
//
// 为什么不用全局 rng:
//
//	① 夜间批处理可能崩在调用之后、入池之前 —— 重跑时掷骰结果必须一致,
//	   否则"今天本来没有的事"会在第二次运行里凭空出现 (池子漂移);
//	② 全局 rng 是引擎的共享序列, 从外面消耗它会让仿真对照失真 (simEnv 踩过这条)。
func eventGenRoll(day, text string) float64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(day))
	_, _ = h.Write([]byte{'|'})
	_, _ = h.Write([]byte(text))
	return float64(h.Sum64()%10000) / 10000.0
}

// ——— 提示词 (§3.6.2 的输入 · §3.6.3 的输出契约 · §3.6.7 的护栏) ———

// eventGenInputs 是提示词要用的素材 (缺了就少一段, **不阻断生成**)。
type eventGenInputs struct {
	Name     string
	Identity string
	Relation string
	Persona  string
	Schedule string
	Mood     string
	Body     string
	Recent   []string
}

// eventGenGather 从库里取 §3.6.2 要求的输入。
//
// ⚠️ 每一样都是**尽力而为**: 读不到就少一段, 绝不让整次生成失败。
// 这是"素材生成"不是"决策" (§3.6 的交办): 素材少一点、生成的事件平庸一点都可以,
// 但世界不能因为一次 SELECT 失败就停摆。
func eventGenGather(st *Store, p *PersonaProfile, now time.Time, lookback int) eventGenInputs {
	var ins eventGenInputs
	if p != nil {
		ins.Name = p.Name
		ins.Identity = identityLabels[p.Identity]
		if ins.Identity == "" {
			ins.Identity = "未指定" // 零值 = 未指定 ≠ 某个具体身份 (别替它加戏)
		}
		ins.Relation = templateLabels[p.Template]
		ins.Persona = strings.TrimSpace(p.Description + " " + truncRunes(p.Personality, 80))
	}
	if st == nil || st.DB == nil {
		return ins
	}
	if ws, err := st.LoadSchedule(); err == nil && ws != nil {
		ins.Schedule = eventGenScheduleLine(ws, p, now)
	} else if err != nil {
		log.Warn("convo:eventgen", "日程读不到, 提示词少一段: %v", err)
	}
	if m, err := st.LoadMood(); err == nil {
		ins.Mood = fmt.Sprintf("%s (%.2f, 精神 %.2f)", m.Label(), m.Valence, m.Arousal)
	} else {
		log.Warn("convo:eventgen", "心情读不到, 提示词少一段: %v", err)
	}
	if b, err := st.LoadBody(); err == nil {
		b.Refresh(now)
		if b.Active {
			// 只给**事实**, 不给 §4.2 那套"不许要安慰"的约束文本 ——
			// 那是对话模型的规矩, 不是素材生成器的输入。
			ins.Body = BodyLabel(b.Kind)
		}
	}
	if rec, err := st.RecentWorldEventLines(now, lookback, 12); err == nil {
		ins.Recent = rec
	} else {
		log.Warn("convo:eventgen", "最近世界事件读不到, 提示词少一段: %v", err)
	}
	return ins
}

// eventGenScheduleLine 把今天的日程压成一行 ("07:30-12:00 上课; …")。
func eventGenScheduleLine(ws *WeekSchedule, p *PersonaProfile, now time.Time) string {
	if ws == nil {
		return ""
	}
	id := ""
	if p != nil {
		id = p.Identity
	}
	key := weekdayKey(now)
	var parts []string
	for _, d := range ws.Days {
		if d.Day != key {
			continue
		}
		for _, sl := range d.Slots {
			parts = append(parts, sl.Start+"-"+sl.End+" "+eventGenStateLabel(sl.State, id))
		}
		break
	}
	return strings.Join(parts, "; ")
}

// eventGenStateLabel 把日程状态折成一个词。focus 按身份分岔 (上课 / 上班)。
// 认不出的状态**原样返回**: 看得见的未知, 好过看不见的空白。
func eventGenStateLabel(state, identity string) string {
	switch state {
	case StateSleep:
		return "睡觉"
	case StateFocus:
		switch identity {
		case "office_worker":
			return "上班"
		case "night_owl":
			return "干活"
		case IdentityRole:
			// ⚠️ 角色档**不许**说成"上课/上班" —— 那正是 #13 的病。
			// 用最中性的一个词: 它具体在做什么, 由世界观与日程说了算。
			return "在忙"
		default:
			return "上课"
		}
	case StateBusy:
		return "忙"
	case StateSlack:
		return "摸鱼"
	case StateFree:
		return "空闲"
	}
	return state
}

func eventGenSystemPrompt() string {
	return `你是一个"生活事件生成器"。你在为一个有自己生活的虚拟角色生成**今天可能遇到的事**。
只输出 JSON, 不要解释, 不要 markdown 代码块。

输出形状:
{"events":[{"kind":"social","desc":"同事临时拉你去喝酒","ts":"18:30","window":"18:00-23:00","valence":-0.2,"arousal":0.6,"intensity":0.4,"duration":"pulse","p_trigger":0.5,"source":"同事"}]}

字段:
- kind: social | stranger | weather | work | family | accident 之一 (写成别的, 这一条作废)
- desc: 一句话, 中文, 40 字以内, 像"路上遇到一个遛狗的老太太"这种
- ts: 24 小时制 "HH:MM", **必填** —— 解析不出来的, 这一条整条作废
- window: 可选, "HH:MM-HH:MM"; 填了就必须包含 ts
- valence -1..1 方向 / arousal 0..1 唤起 / intensity 0..1 强度
- duration: pulse(当天就过去) | background(要持续好几天)
- p_trigger 0..1: 白天按这个概率真的发生 —— **没发生是常态**, 不要都填 1
- source: 可选, 涉及的人 ("同事" / "室友" / "妈妈" / "便利店店员")

硬规则 (违反整条作废):
1. **不许生成公共可证伪的事实**: 地震 / 台风 / 新闻 / 热搜 / 名人 / 赛事这类一查就露的, 一律不许。
   只能是不可证伪的私域小事。如果真有灾难, 你在编造会显得极其冷血。
2. 不许用高情绪钩子博同情 (家人重病这类, 一个月最多一次)。
3. 身份自洽: 学生不许"陪客户应酬", 上班族不许"上课点名"。
4. 不要重复最近几天已经发生过的事, 也别反复用同一类道具 (快递 / 饭团这种别天天来)。
5. 语境自洽: 结合今天的日程、心情、天气 —— 它今天在忙什么、心情如何, 决定了什么事"像会发生"。`
}

func eventGenUserPrompt(env EnvDay, p *PersonaProfile, ins eventGenInputs, now time.Time, n int) string {
	var b strings.Builder
	b.WriteString("【它是谁】")
	if ins.Name != "" {
		b.WriteString(ins.Name + ", ")
	}
	b.WriteString(ins.Identity)
	if ins.Relation != "" {
		b.WriteString(", 和用户是" + ins.Relation + "关系")
	}
	if ins.Persona != "" {
		b.WriteString("。性格: " + ins.Persona)
	}
	b.WriteString("\n")
	if ins.Schedule != "" {
		b.WriteString("【今天的日程】" + ins.Schedule + "\n")
	}
	if ins.Mood != "" || ins.Body != "" {
		b.WriteString("【当前状态】心情 " + ins.Mood)
		if ins.Body != "" {
			b.WriteString("; 身体 " + ins.Body)
		}
		b.WriteString("\n")
	}
	if len(ins.Recent) > 0 {
		b.WriteString("【最近已经发生过的 (别重复)】\n- " + strings.Join(ins.Recent, "\n- ") + "\n")
	}
	b.WriteString("【季节与天气】" + eventGenSeasonLine(env, now) + "\n")
	fmt.Fprintf(&b, "请给最多 %d 条候选 (少于这个数也行, 但至少 1 条)。", n)
	return b.String()
}

// eventGenSeasonLine 季节 + 天气 (设计稿 §3.6.2 的输入之一)。
// 天气是**它那边的今天**, 不是公共事实 —— 与真实数据源 (Open-Meteo) 同源, 可以直接写进素材。
func eventGenSeasonLine(env EnvDay, now time.Time) string {
	season := "冬"
	switch now.Month() {
	case 3, 4, 5:
		season = "春"
	case 6, 7, 8:
		season = "夏"
	case 9, 10, 11:
		season = "秋"
	}
	s := fmt.Sprintf("%s (%s)", season, now.Format("01-02"))
	if env.City != "" {
		s += " " + env.City
	}
	if env.Weather != "" {
		s += fmt.Sprintf(", %s %d°C", env.Weather, env.TempC)
	}
	return s
}

// ——— 确定性模板兜底 ———
//
// 这张表就是"退回现有确定性模板"里的**那个模板**。它必须满足三件事:
//
//	① **确定性**: 同一天同一个人格 → 同一张池子 (重跑不漂移, 仿真可对照);
//	② **非空**: 无论模型怎么坏、怎么超时、配额怎么用尽, 池子永远至少 1 条 ——
//	   世界停摆比事件平庸糟得多;
//	③ **不携带公共可证伪的事实**: 它进的是同一套注入段, 同样受 §3.6.4 红线约束。
type eventGenTemplate struct {
	TS        string
	Text      string
	Kind      string
	Source    string
	Valence   float64
	Arousal   float64
	Intensity float64
	Duration  string
}

// eventGenDrinkRoll 当天种子 → [0,1)。
//
// ⚠️ 就是个哈希, **不是随机数**: 同一天必须永远得到同一个结果。
func eventGenDrinkRoll(seed string) float64 {
	h := fnv.New32a()
	_, _ = h.Write([]byte("drink|" + seed))
	return float64(h.Sum32()%10000) / 10000.0
}

// eventGenDrinkPool §4.2.4 **饮料事件** (§3.6 事件池的一个新类)。
//
// ⚠️ 幅度**必须小**(±0.05~0.15): 一杯饮料不该推动一整天的情绪。
// 它的作用不是"让心情变好/变坏", 而是给"今天买到一杯超难喝的"这件事一个**成因链位置** ——
// 于是你能从它随口一句里听出来它今天不太顺, 而它**从没报过心情**。
var eventGenDrinkPool = []eventGenTemplate{
	{TS: "10:40", Text: "楼下买了一杯少冰的美式, 还行", Kind: "self", Source: "自己", Valence: 0.08, Arousal: 0.4, Intensity: 0.2},
	{TS: "15:20", Text: "试了便利店新出的那个茶, 出乎意料地好喝", Kind: "self", Source: "自己", Valence: 0.15, Arousal: 0.45, Intensity: 0.25},
	{TS: "14:10", Text: "外卖送来的咖啡洒了半杯", Kind: "self", Source: "自己", Valence: -0.12, Arousal: 0.45, Intensity: 0.2},
	{TS: "16:50", Text: "买的那瓶饮料甜得发齁, 喝了两口就放下了", Kind: "self", Source: "自己", Valence: -0.15, Arousal: 0.4, Intensity: 0.25},
}

// eventGenCommonPool 通用小事 (幅度小: 都是 -0.3..+0.3 的日常)。
var eventGenCommonPool = []eventGenTemplate{
	{TS: "10:20", Text: "快递到了, 但是放错了驿站", Kind: "self", Source: "生活", Valence: -0.20, Arousal: 0.5, Intensity: 0.3},
	{TS: "14:40", Text: "耳机一边没声了", Kind: "self", Source: "生活", Valence: -0.25, Arousal: 0.55, Intensity: 0.3},
	{TS: "19:10", Text: "便利店想买的那个饭团卖完了", Kind: "social", Source: "陌生人", Valence: -0.15, Arousal: 0.5, Intensity: 0.25},
	{TS: "11:05", Text: "楼下在装修, 一上午没安静过", Kind: "environment", Source: "生活", Valence: -0.30, Arousal: 0.6, Intensity: 0.35},
	{TS: "20:30", Text: "朋友临时约周末吃饭", Kind: "social", Source: "朋友", Valence: 0.30, Arousal: 0.65, Intensity: 0.4},
	{TS: "09:15", Text: "手机充电线接触不良, 充了一晚上只有 20%", Kind: "self", Source: "生活", Valence: -0.30, Arousal: 0.55, Intensity: 0.35},
	{TS: "16:20", Text: "洗完衣服发现口袋里有张纸, 全糊了", Kind: "self", Source: "生活", Valence: -0.25, Arousal: 0.55, Intensity: 0.3},
	{TS: "21:40", Text: "在旧外套里翻出二十块钱", Kind: "self", Source: "生活", Valence: 0.25, Arousal: 0.6, Intensity: 0.3},
	{TS: "12:50", Text: "想点的外卖今天停送了", Kind: "self", Source: "生活", Valence: -0.20, Arousal: 0.5, Intensity: 0.25},
	{TS: "17:30", Text: "排队排到一半发现走错了窗口", Kind: "self", Source: "生活", Valence: -0.20, Arousal: 0.55, Intensity: 0.3},
	{TS: "13:10", Text: "阳台那盆绿萝蔫了, 浇了点水", Kind: "self", Source: "生活", Valence: -0.10, Arousal: 0.45, Intensity: 0.2},
	{TS: "22:10", Text: "路过小区门口, 看见一只很凶的橘猫", Kind: "social", Source: "陌生人", Valence: 0.20, Arousal: 0.6, Intensity: 0.3},
}

// eventGenIdentityPools 身份专属 (§3.6.7.3 身份自洽: 学生不许陪客户应酬)。
var eventGenIdentityPools = map[string][]eventGenTemplate{
	// IdentityRole §3.6.7.3: **角色档**。
	//
	// ⚠️ 刻意全是**中性**素材(路人 / 天气 / 手边的事), 不预设宿舍、食堂、工位、教室 ——
	// 角色的具体日常来自世界观与它的日程, **不该由这一层替它编**。
	// 而"没编"与"编错"之间, 宁可没编: 编错了就是自相矛盾, 而不编只是少一层素材。
	IdentityRole: {
		{TS: "09:20", Text: "路上被人问了个不太好答的问题", Kind: "social", Source: "路人", Valence: -0.15, Arousal: 0.55, Intensity: 0.3},
		{TS: "14:30", Text: "手边那件事比想象中费时间", Kind: "self", Source: "自己", Valence: -0.20, Arousal: 0.5, Intensity: 0.3},
		{TS: "17:40", Text: "天阴下来了, 像是要下雨", Kind: "environment", Source: "天气", Valence: -0.10, Arousal: 0.45, Intensity: 0.25},
		{TS: "20:10", Text: "翻到一件很久以前留下的东西", Kind: "self", Source: "自己", Valence: 0.15, Arousal: 0.5, Intensity: 0.3},
	},
	"student": {
		{TS: "08:05", Text: "早课点名, 差点没赶上", Kind: "self", Source: "学校", Valence: -0.25, Arousal: 0.7, Intensity: 0.35},
		{TS: "15:40", Text: "小组作业有人放鸽子", Kind: "self", Source: "同学", Valence: -0.30, Arousal: 0.6, Intensity: 0.4},
		{TS: "18:20", Text: "图书馆想借的那本书刚被借走", Kind: "self", Source: "学校", Valence: -0.20, Arousal: 0.5, Intensity: 0.3},
		{TS: "23:10", Text: "室友打游戏声音有点大", Kind: "self", Source: "室友", Valence: -0.25, Arousal: 0.55, Intensity: 0.3},
	},
	"office_worker": {
		{TS: "18:40", Text: "临时被拉进一个会, 开到很晚", Kind: "calendar", Source: "工作", Valence: -0.30, Arousal: 0.6, Intensity: 0.4},
		{TS: "16:10", Text: "同事把活儿甩过来了", Kind: "social", Source: "同事", Valence: -0.30, Arousal: 0.65, Intensity: 0.4},
		{TS: "08:40", Text: "通勤地铁晚点, 站了四十分钟", Kind: "calendar", Source: "通勤", Valence: -0.25, Arousal: 0.6, Intensity: 0.35},
		{TS: "12:30", Text: "楼下咖啡店换老板了, 味道变了", Kind: "environment", Source: "生活", Valence: -0.10, Arousal: 0.5, Intensity: 0.25},
	},
	"night_owl": {
		{TS: "09:30", Text: "两点多才睡, 早上怎么都起不来", Kind: "self", Source: "自己", Valence: -0.25, Arousal: 0.45, Intensity: 0.35},
		{TS: "23:50", Text: "半夜刷到一个特别对胃口的视频", Kind: "self", Source: "自己", Valence: 0.25, Arousal: 0.6, Intensity: 0.3},
		{TS: "22:30", Text: "便利店的店员已经认得我了", Kind: "social", Source: "便利店店员", Valence: 0.20, Arousal: 0.55, Intensity: 0.3},
		{TS: "01:10", Text: "没忍住又点了一次宵夜", Kind: "self", Source: "自己", Valence: 0.15, Arousal: 0.5, Intensity: 0.25},
	},
}

// eventGenWeatherPool 天气相关 (模板也要看天气 —— 否则雨天和晴天退回的池子一模一样)。
func eventGenWeatherPool(weather string) []eventGenTemplate {
	switch {
	case strings.Contains(weather, "雨"):
		return []eventGenTemplate{
			{TS: "08:15", Text: "伞被风吹翻了, 到地方裤脚全湿", Kind: "environment", Source: "天气", Valence: -0.30, Arousal: 0.6, Intensity: 0.4},
		}
	case strings.Contains(weather, "雪"):
		return []eventGenTemplate{
			{TS: "07:50", Text: "出门踩到一层薄冰, 差点滑一跤", Kind: "environment", Source: "天气", Valence: -0.30, Arousal: 0.65, Intensity: 0.4},
		}
	case strings.Contains(weather, "晴"):
		return []eventGenTemplate{
			{TS: "12:40", Text: "太阳很好, 晒得人有点犯困", Kind: "environment", Source: "天气", Valence: 0.20, Arousal: 0.35, Intensity: 0.25},
		}
	case strings.Contains(weather, "阴"):
		return []eventGenTemplate{
			{TS: "15:30", Text: "一整天都是灰的, 说不清哪里不对劲", Kind: "environment", Source: "天气", Valence: -0.15, Arousal: 0.4, Intensity: 0.25},
		}
	}
	return nil
}

// EventGenFallbackPool 模板兜底池 —— **空池子是本模块唯一不允许产生的东西**。
func EventGenFallbackPool(env EnvDay, p *PersonaProfile, now time.Time) []WorldEvent {
	day := now.Format("2006-01-02")
	id := ""
	if p != nil {
		id = p.Identity
	}
	seed := day + "|" + id

	// 池子按"今天的贴合度"拼接 (档位表, 不是概率): 身份专属 → 天气专属 → 通用。
	pool := make([]eventGenTemplate, 0, 24)
	pool = append(pool, eventGenIdentityPools[id]...)
	pool = append(pool, eventGenWeatherPool(env.Weather)...)
	pool = append(pool, eventGenCommonPool...)
	// ⚠️ 兜底是**独占**的: 一旦用上它, 这一池就只有那一条。
	// (第一版把饮料池挂在兜底**之后**, 于是清空表的库会拿到 1 条中性 + 2 条饮料 ——
	//  而那条反面用例要的是"恰好一条"。)
	floored := false
	if len(pool) == 0 {
		pool = []eventGenTemplate{{
			TS: "12:00", Text: "今天过得平平常常, 没什么特别的",
			Kind: "self", Source: "生活", Valence: 0, Arousal: 0.5, Intensity: 0.2, Duration: "pulse",
		}}
		floored = true
	}
	// §4.2.4 饮料事件: **约三成的日子**会买点什么喝 (设计稿给的 p_trigger ≈ 0.3)。
	//
	// ⚠️ 判据用**当天种子**, 不用真随机 —— 同一天的池子必须逐字节一样,
	// 否则重跑一次就换了事件, 而那是"时间线不可修复"(与 §3.7 夜间社交同一条纪律)。
	//
	// ⚠️ 刻意排在**兜底之后**: 兜底管的是"所有内容表都空了", 而饮料池也是内容。
	// 排前面的话, 一个被清空的库在"饮料日"会拿到三条饮料事件而**永远走不到兜底** ——
	// 那正是 TestEventGen_FallbackPoolDeterministicAndNeverEmpty 那条反面用例挡的东西。
	if !floored && eventGenDrinkRoll(seed) < 0.3 {
		pool = append(pool, eventGenDrinkPool...)
	}

	n := EventGenFallbackCount
	if n > len(pool) {
		n = len(pool)
	}
	// 顺序取样 + 起点按 (日子|人格) 哈希 → 组合天天不同, 且**不会撞号**。
	// (随机步进会在步长与池长不互素时撞号, 于是"3 条"悄悄变成 2 条 —— 静默少一条。)
	start := eventGenPick(seed, len(pool))
	out := make([]WorldEvent, 0, n)
	for i := 0; i < len(pool) && len(out) < n; i++ {
		t := pool[(start+i)%len(pool)]
		// ⚠️ Duration 的零值是 "" —— 而 SettleStage 把**非 pulse**的一律当 background
		// (事件因此会留在自传体里 30 天)。表里没写就折成最轻的一档: 这是"零值必须落在
		// 安全的那一侧"在时长上的具体一条 —— 兜底事件不该悄悄变成长期背景。
		dur := t.Duration
		if dur == "" {
			dur = "pulse"
		}
		out = append(out, WorldEvent{
			TS:        day + " " + t.TS + ":00",
			Kind:      t.Kind,
			Source:    t.Source,
			Text:      t.Text,
			Valence:   t.Valence,
			Arousal:   t.Arousal,
			Intensity: t.Intensity,
			Weight:    1.0,
			Duration:  dur,
		})
	}
	return out
}

// eventGenPick 由种子决定一个 [0,n) 的起点。
//
// ⚠️ 是哈希不是随机数: 同一天必须永远从同一个位置开始, 否则池子会漂移。
func eventGenPick(seed string, n int) int {
	if n <= 0 {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte("pick|" + seed))
	return int(h.Sum64() % uint64(n))
}
