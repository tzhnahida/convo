package convo

import (
	"fmt"
	"time"

	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// §3.5 **事件来源与预算** (2026-10-15)
//
// 「①频率预算: 大事件 ≤1/周、小事件 ≤3/周(**超了生活就变连续剧**) ②同类冷却 30 天」
//
// ⚠️ 没有这一层会怎样: 事件池每天都在生成, 而**没有东西记得昨天发生了什么** ——
// 于是一周里能出现三场"家人住院"。那不是"生活丰富", 那是**连续剧**。
// 而连续剧最要命的不是夸张, 是**它把每一天都变成有事的**, 于是"平平常常的一天"消失了。
//
// ⚠️ 判在**入池**这一步(事件变成事实的地方), 不是判在生成那一步:
// 生成器给的是**候选**, 候选可以是十条; 而"这个月已经有过一场大事了"只有库知道。
// ─────────────────────────────────────────────────────────────

const (
	// EventMajorPerWeek 大事件每周上限。
	EventMajorPerWeek = 1
	// EventMinorPerWeek 小事件每周上限。
	EventMinorPerWeek = 3
	// EventMajorIntensity 强度到多少算"大事件"。
	//
	// ⚠️ 阈值不是随便定的: eventGenKinds 给的缺省强度里, accident=0.8 / family=0.7,
	// 而 social=0.5 / stranger=0.3 / weather=0.4 / drink=0.3 —— 0.7 正好把它们分开。
	EventMajorIntensity = 0.7
	// EventSameClassCooldownDays 同类大事件的冷却。
	EventSameClassCooldownDays = 30
)

// EventBudgetVerdict §3.5 的裁决。
type EventBudgetVerdict struct {
	Allow bool
	// Reason 只给日志与 owner。
	Reason string
}

// EventClass 事件的"类" —— 冷却按它判。
//
// ⚠️ 用 Kind|Source 是**粗**的, 但方向是对的: accident 与 drink 虽然都落到
// downstream kind `self`, Source 却不同(自己 / 自己)…… 所以这里更依赖 Source。
// 真正要挡的是"同一类大事一个月里来两次"(家人住院 → 家人重病)。
//
// ⚠️ 而它**只对大事件生效** —— 见 CheckEventBudget 的注释。
func EventClass(ev WorldEvent) string {
	return ev.Kind + "|" + ev.Source
}

// CheckEventBudget §3.5 的三条: 大事件/周 · 小事件/周 · 同类冷却。
func (s *Store) CheckEventBudget(ev WorldEvent, now time.Time) EventBudgetVerdict {
	week := now.AddDate(0, 0, -7)
	major, minor, err := s.countEventsSince(week)
	if err != nil {
		// ⚠️ 读不到库 → **放行**。失败往"正常说话"倒: 一次读库失败不该让它那天什么都没发生。
		log.Warn("convo:budget", "事件预算读库失败(本次放行): %v", err)
		return EventBudgetVerdict{Allow: true}
	}
	if ev.Intensity >= EventMajorIntensity {
		if major >= EventMajorPerWeek {
			return EventBudgetVerdict{Reason: fmt.Sprintf("这周已经有过 %d 件大事", major)}
		}
		// 同类冷却**只对大事件生效**。
		// ⚠️ 这一条是刻意的收窄: 照字面把冷却套在**所有**事件上,
		// "天气"这个 Source 会把一个月里所有的天气事件全挡掉 —— 那是把池子掏空, 不是防重复。
		// 设计稿给冷却举的例子是「家人重病」, 而它显然是大事。
		if last, ok, lerr := s.lastEventOfClass(EventClass(ev)); lerr == nil && ok {
			if now.Sub(last) < EventSameClassCooldownDays*24*time.Hour {
				return EventBudgetVerdict{Reason: "同一类大事上个月刚发生过"}
			}
		}
	} else if minor >= EventMinorPerWeek {
		return EventBudgetVerdict{Reason: fmt.Sprintf("这周已经有 %d 件小事了", minor)}
	}
	return EventBudgetVerdict{Allow: true}
}

// countEventsSince 窗口内的大/小事件条数。
func (s *Store) countEventsSince(since time.Time) (major, minor int, err error) {
	rows, err := s.DB.Query(`SELECT COALESCE(intensity,0) FROM convo_world_event WHERE ts >= ?`,
		since.Format(bodyTSLayout))
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var v float64
		if serr := rows.Scan(&v); serr != nil {
			return 0, 0, serr
		}
		if v >= EventMajorIntensity {
			major++
		} else {
			minor++
		}
	}
	return major, minor, rows.Err()
}

// lastEventOfClass 这一"类"上一次是什么时候。
func (s *Store) lastEventOfClass(class string) (time.Time, bool, error) {
	parts := splitClass(class)
	var ts string
	err := s.DB.QueryRow(`SELECT COALESCE(MAX(ts),'') FROM convo_world_event WHERE kind=? AND source=?`,
		parts[0], parts[1]).Scan(&ts)
	if err != nil || ts == "" {
		return time.Time{}, false, err
	}
	t, perr := time.ParseInLocation(bodyTSLayout, ts, time.Local)
	if perr != nil {
		// ⚠️ 时刻解析不出 → 当作**没有过** (失败往"能发生"倒, 不往"永远不发生"倒)。
		return time.Time{}, false, nil
	}
	return t, true, nil
}

func splitClass(class string) [2]string {
	for i := 0; i < len(class); i++ {
		if class[i] == '|' {
			return [2]string{class[:i], class[i+1:]}
		}
	}
	return [2]string{class, ""}
}
