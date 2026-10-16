package convo

import (
	"fmt"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// §4.4.6 **失眠成因要的日级读数** (2026-10-15)
//
// 设计稿 L687: 失眠「**不是随机数, 是当天发生的事的结果**」。
// 而 InsomniaInput 有八个字段, 夜里那批**只填了三个**
// (LateMsg / MoodLow / Dream) —— 冲突次数、加班时长、热力峰值三项一直是 0。
//
// ⚠️ 三项为 0 的后果不是"少几分", 而是**判据塌了**:
// 阈值 4 分, 而剩下三项满分 2+2+2 —— 也就是说
// **一个白天吵了两架、加班四小时、群里热到爆的日子, 依然判不出失眠**。
// 而日志里只会看到"成因分 2", 看不出少了什么。
//
// ⚠️ 这三项的性质**不一样**, 所以做法也不一样:
//
//	加班时长 —— **能重算**(日程留着)          → 现算, 不落库
//	冲突次数 —— **不能重算**(决策表没按月归档) → 发生时累加
//	热力峰值 —— **不能重算**(热力只存上次结算那一刻的值) → 每次读到就抬一次顶
//
// 「峰值」这类量本质上**不可重算** —— 它必须在发生的当下被记下来,
// 否则两次结算之间的那个尖峰就永远丢了。这与"沉降阶段现算"是**相反**的取舍,
// 而区别在于: 沉降阶段是时间的纯函数, 峰值不是。
// ─────────────────────────────────────────────────────────────

// dayMetricConflicts / dayMetricHeatPeak 两个不可重算的日级读数。
const (
	dayMetricConflicts = "conflicts"
	dayMetricHeatPeak  = "heat_peak"
)

// OverworkBaseMin 一天的"正常忙碌"上限 —— 超过的部分才算加班。
//
// ⚠️ 8 小时是**基线**不是硬指标: 它是"一个正常工作日有多长",
// 而不是"不许超过 8 小时"。超过的**小时数**才是失眠的成因。
const OverworkBaseMin = 8 * 60

// EnsureDayMetricSchema 幂等建表。
func EnsureDayMetricSchema(s *Store) error {
	_, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS convo_day_metric (
		day TEXT NOT NULL, name TEXT NOT NULL, v REAL NOT NULL DEFAULT 0,
		updated_ts TEXT DEFAULT '', PRIMARY KEY (day, name)
	)`)
	return err
}

// BumpDayMax 把某个日级读数抬到 max(旧值, v)。
func (s *Store) BumpDayMax(day, name string, v float64) error {
	if s == nil || s.DB == nil {
		return fmt.Errorf("daymetric: store 未初始化")
	}
	// ⚠️ 用 SQL 的 CASE 比较而不是"先读再比再写": 后者在两次调用之间有一个窗口,
	// 而热力是每分钟都可能被读到的 —— 那正是峰值最容易丢的地方。
	_, err := s.DB.Exec(`INSERT INTO convo_day_metric (day, name, v, updated_ts) VALUES (?,?,?,?)
		ON CONFLICT(day, name) DO UPDATE SET
			v = CASE WHEN excluded.v > convo_day_metric.v THEN excluded.v ELSE convo_day_metric.v END,
			updated_ts = excluded.updated_ts`,
		day, name, v, clock.TodayTS())
	return err
}

// AddDayMetric 日级累加。
func (s *Store) AddDayMetric(day, name string, delta float64) error {
	if s == nil || s.DB == nil {
		return fmt.Errorf("daymetric: store 未初始化")
	}
	_, err := s.DB.Exec(`INSERT INTO convo_day_metric (day, name, v, updated_ts) VALUES (?,?,?,?)
		ON CONFLICT(day, name) DO UPDATE SET v = convo_day_metric.v + excluded.v, updated_ts = excluded.updated_ts`,
		day, name, delta, clock.TodayTS())
	return err
}

// DayMetric 读一个日级读数。**没记过 → ok=false**(零值 = 未提供 ≠ 0)。
func (s *Store) DayMetric(day, name string) (float64, bool) {
	if s == nil || s.DB == nil {
		return 0, false
	}
	var v float64
	if err := s.DB.QueryRow(`SELECT v FROM convo_day_metric WHERE day=? AND name=?`, day, name).Scan(&v); err != nil {
		return 0, false
	}
	return v, true
}

// OverworkHours 从今天的日程算加班小时数 (向下取整, 不超的部分算 0)。
//
// ⚠️ 这个量**可以重算**, 所以**不落库** —— 与 §4.1「热度现算不落库」同一条纪律。
func OverworkHours(ws *WeekSchedule, now time.Time) int {
	busy := BusyMinutesToday(ws, now)
	if busy <= OverworkBaseMin {
		return 0
	}
	return (busy - OverworkBaseMin) / 60
}

// ConflictsToday 今天记下的冲突次数。没记过 → 0 (它本来就是"今天吵了几次")。
func (s *Store) ConflictsToday(now time.Time) int {
	v, _ := s.DayMetric(now.Format("2006-01-02"), dayMetricConflicts)
	return int(v)
}

// HeatPeakToday 今天的热力峰值。没记过 → ok=false。
func (s *Store) HeatPeakToday(now time.Time) (float64, bool) {
	return s.DayMetric(now.Format("2006-01-02"), dayMetricHeatPeak)
}

// NoteHeatPeak 每次读到热力就抬一次当天的顶。
//
// ⚠️ 挂在**读**上而不是"结算"上: 热力的互动入账那条路现在**没有接线**
// (RecordHeatInteraction 全库零调用), 挂在结算上的话这个峰值永远是空的。
// 而"读到的最大值"与"真实峰值"在**它每分钟都被读到**的前提下是等价的。
func (e *Engine) NoteHeatPeak(v float64, now time.Time) {
	if e.Store == nil || v <= 0 {
		return
	}
	if err := e.Store.BumpDayMax(now.Format("2006-01-02"), dayMetricHeatPeak, v); err != nil {
		log.Warn("convo:heat", "热力峰值记账失败(当日峰值会偏低): %v", err)
	}
}

// InsomniaReadings 把三项读数填进失眠输入。
//
// ⚠️ 三项都**读不到就不填**(保持 0), 而不是填一个猜的数 ——
// 填了猜的数会让判据看起来在工作, 而它推的是空气。
// ⚠️ 取的是**刚过去的那一天**(传进来的 day), 不是"现在" ——
// 夜里跑的时候已经是第二天凌晨了, 拿 time.Now() 去数会数到新的一天。
func (e *Engine) InsomniaReadings(in *InsomniaInput, day string) {
	if e.Store == nil || in == nil {
		return
	}
	// 用那一天的**末尾**去数: BusyMinutesToday 只算到"现在", 而我们要整天的。
	at := clock.Now()
	if t, ok := ParseDay(day); ok {
		at = t.Add(24*time.Hour - time.Minute)
	}
	ws, _ := e.Store.LoadSchedule()
	in.Overwork = OverworkHours(ws, at)
	in.Conflict = e.Store.ConflictsToday(at)
	if peak, ok := e.Store.HeatPeakToday(at); ok {
		in.Heat = peak
	}
}
