package convo

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// ——— 心情与世界事件的持久化 ———

// LoadMood 读当前心情 (没有则给人格底色, **不写库** —— 读操作不该有副作用)。
func (s *Store) LoadMood() (MoodState, error) {
	var v, a, bgV float64
	var causes, since, low, upd, bgText, bgUntil string
	err := s.DB.QueryRow(`SELECT valence, arousal, COALESCE(causes_json,'[]'),
		COALESCE(since_ts,''), COALESCE(low_until,''), COALESCE(updated_ts,''),
		COALESCE(bg_valence,0), COALESCE(bg_text,''), COALESCE(bg_until,'')
		FROM convo_mood_state WHERE id=1`).Scan(&v, &a, &causes, &since, &low, &upd, &bgV, &bgText, &bgUntil)
	if err == sql.ErrNoRows {
		return NewMoodState(), nil
	}
	if err != nil {
		return NewMoodState(), err
	}
	m := MoodState{Valence: v, Arousal: a, Since: since, LowUntil: low, Updated: upd,
		BgValence: bgV, BgText: bgText, BgUntil: bgUntil}
	_ = json.Unmarshal([]byte(causes), &m.Causes)
	return m, nil
}

func (s *Store) SaveMoodState(m MoodState) error {
	b, err := json.Marshal(m.Causes)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`INSERT INTO convo_mood_state(id, valence, arousal, causes_json, since_ts, low_until, updated_ts, bg_valence, bg_text, bg_until)
		VALUES(1,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET valence=excluded.valence, arousal=excluded.arousal,
			causes_json=excluded.causes_json, since_ts=excluded.since_ts,
			low_until=excluded.low_until, updated_ts=excluded.updated_ts,
			bg_valence=excluded.bg_valence, bg_text=excluded.bg_text, bg_until=excluded.bg_until`,
		m.Valence, m.Arousal, string(b), m.Since, m.LowUntil, m.Updated,
		m.BgValence, m.BgText, m.BgUntil)
	return err
}

// IngestWorldEvents 把事件汇入心情; 返回**真正新入账**的条数。
//
// 幂等靠 event_key 唯一约束: tick 每分钟都会路过, 同一场雨只该推动心情一次。
// 每推一条就在 `convo_mood_log` 留一行 —— 那是 §3.2 约束 4"对 owner 可查"的因果链,
// 也是"它今天怎么了"这个管理页问题的唯一数据来源。
func (s *Store) IngestWorldEvents(evs []WorldEvent) (int, error) {
	if len(evs) == 0 {
		return 0, nil
	}
	m, err := s.LoadMood()
	if err != nil {
		return 0, err
	}
	now := clock.Now()
	applied := 0
	for _, ev := range evs {
		// §3.5 频率预算: 大事件 ≤1/周 · 小事件 ≤3/周 · 同类大事冷却 30 天。
		//
		// ⚠️ 判在**这里**(事件变成事实的地方)而不是生成那一步:
		// 生成器给的是候选, 候选可以十条; 而"这个月已经有过一场大事了"**只有库知道**。
		// ⚠️ 被挡下要**出声** —— 静默丢弃会让"预算生效"和"生成器坏了"长得一模一样。
		if v := s.CheckEventBudget(ev, now); !v.Allow {
			log.Info("convo:budget", "§3.5 事件未入池(%s): %s", v.Reason, ev.Text)
			continue
		}
		res, err := s.DB.Exec(`INSERT OR IGNORE INTO convo_world_event
			(event_key, ts, kind, source, text, valence, arousal, intensity, weight, duration, last_mentioned_ts)
			VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			ev.Key(), ev.TS, ev.Kind, ev.Source, ev.Text,
			ev.Valence, ev.Arousal, ev.Intensity, ev.Weight, ev.Duration, ev.TS)
		if err != nil {
			return applied, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue // 已入过账
		}
		var dv, da float64
		if ev.Duration == "background" {
			// 背景压力 (§3.2 约束 2): 不是推一次就完, 而是**改当天的心情底色**,
			// 由 Decay 的回落目标承载 —— 于是它整天都在, 而不是早上响一下。
			//
			// ⚠️ 改完底色必须**立刻走一次衰减**: 否则 m.Valence 原地不动,
			// 报告里的"心情轨迹"会显示成一条直线 (实测踩过), 机制看起来像没生效。
			// 心情向新底色**移动**而不是瞬移 —— 人对"今天一直下雨"的反应本来就是渐进的。
			before := m.Valence
			m.BgValence = clampF(ev.Valence, -0.6, 0.6)
			m.BgText = ev.Text
			m.BgUntil = now.Format("2006-01-02") + " 23:59:59"
			m.Decay(now)
			m.Updated = now.Format("2006-01-02 15:04:05")
			if m.Since == "" {
				m.Since = clock.TodayTS()
			}
			dv = m.Valence - before
		} else {
			dv, da = m.Apply(ev, now)
		}
		m.Causes = append(m.Causes, MoodCause{
			TS: now.Format("2006-01-02 15:04:05"), Kind: ev.Kind, Source: ev.Source,
			Text: ev.Text, Weight: ev.Weight, DeltaV: dv, DeltaA: da,
		})
		if len(m.Causes) > MoodCauseCap {
			m.Causes = m.Causes[len(m.Causes)-MoodCauseCap:]
		}
		applied++
		_, _ = s.DB.Exec(`INSERT INTO convo_mood_log
			(ts, kind, source, text, weight, delta_v, delta_a, v_after, a_after)
			VALUES(?,?,?,?,?,?,?,?,?)`,
			now.Format("2006-01-02 15:04:05"), ev.Kind, ev.Source, ev.Text,
			ev.Weight, dv, da, m.Valence, m.Arousal)
	}
	if applied > 0 {
		if err := s.SaveMoodState(m); err != nil {
			return applied, err
		}
	}
	return applied, nil
}

// MoodLog 最近 N 条心情变化 (管理页"它今天怎么了"用)。
func (s *Store) MoodLog(limit int) ([]MoodCause, error) {
	rows, err := s.DB.Query(`SELECT ts, kind, source, text, weight, delta_v, delta_a
		FROM convo_mood_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MoodCause
	for rows.Next() {
		var c MoodCause
		if err := rows.Scan(&c.TS, &c.Kind, &c.Source, &c.Text, &c.Weight, &c.DeltaV, &c.DeltaA); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ——— §3.6 事件池: 到点才发生的世界 ———

const sqlSeedDraft = `INSERT OR IGNORE INTO convo_world_event_draft
	(event_key, fire_ts, kind, source, text, valence, arousal, intensity, weight, duration, fired)
	VALUES(?,?,?,?,?,?,?,?,?,?,0)`

// SeedDrafts 把当天的事件池入池。幂等 —— tick 每分钟路过也只入一次。
func (s *Store) SeedDrafts(evs []WorldEvent) (int, error) {
	n := 0
	for _, ev := range evs {
		res, err := s.DB.Exec(sqlSeedDraft, ev.Key(), ev.TS, ev.Kind, ev.Source, ev.Text,
			ev.Valence, ev.Arousal, ev.Intensity, ev.Weight, ev.Duration)
		if err != nil {
			return n, err
		}
		if c, _ := res.RowsAffected(); c > 0 {
			n++
		}
	}
	return n, nil
}

const sqlDrawDue = `SELECT id, fire_ts, kind, source, text, valence, arousal, intensity, weight, duration
	FROM convo_world_event_draft WHERE fired=0 AND fire_ts <= ? ORDER BY fire_ts`

// DrawDueDrafts 取出**到点且未触发**的草稿, 标记已触发并返回。
//
// ⚠️ **先标记后返回**: 万一后续入账失败, 宁可漏掉一条事件, 也不要每分钟重放一次 ——
// 重复入账会把心情反复推向同一个方向, 那比漏掉更假。
func (s *Store) DrawDueDrafts(now time.Time) ([]WorldEvent, error) {
	rows, err := s.DB.Query(sqlDrawDue, now.Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil, err
	}
	var ids []int64
	var out []WorldEvent
	for rows.Next() {
		var id int64
		var ev WorldEvent
		if err := rows.Scan(&id, &ev.TS, &ev.Kind, &ev.Source, &ev.Text,
			&ev.Valence, &ev.Arousal, &ev.Intensity, &ev.Weight, &ev.Duration); err != nil {
			continue
		}
		ids = append(ids, id)
		out = append(out, ev)
	}
	rows.Close()
	for _, id := range ids {
		_, _ = s.DB.Exec(`UPDATE convo_world_event_draft SET fired=1 WHERE id=?`, id)
	}
	return out, nil
}

const sqlLastEvent = `SELECT COALESCE(MAX(ts),'') FROM convo_world_event`

// LastWorldEventTS 最近一次世界事件的时刻 (B6 开口冲量的输入)。
//
// ⚠️ 这是**日内会变**的信号 —— 上一版 B6 用心情算冲量, 而心情在日内几乎不动,
// 于是冲量恒定、闸门形同虚设 (实测开关两态分布逐字相同)。
// §3.6 的事件池让事件在日内散开之后, 这个信号才真的有信息量。
func (s *Store) LastWorldEventTS() (time.Time, bool, error) {
	var ts string
	if err := s.DB.QueryRow(sqlLastEvent).Scan(&ts); err != nil {
		return time.Time{}, false, err
	}
	if ts == "" {
		return time.Time{}, false, nil
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", ts, time.Local)
	if err != nil {
		return time.Time{}, false, err
	}
	return t, true, nil
}

// ——— §3.6.8 事件沉降 ———

const sqlInPlay = `SELECT event_key, text, duration, COALESCE(last_mentioned_ts,'')
	FROM convo_world_event WHERE ts >= ? ORDER BY id DESC LIMIT ?`

// InPlayEvents 取"还在场上"的事件, 附上它当前的沉降阶段。
//
// ⚠️ 阶段是**算出来的**不是存的 —— 存了就要有"谁在什么时候重算它"的问题,
// 而这个东西随时可算, 存下来只会不一致。
func (s *Store) InPlayEvents(now time.Time, limit int) ([]EventNote, error) {
	// 只回看 40 天: 更早的早已归档, 拉出来只是白占注入位。
	from := now.AddDate(0, 0, -40).Format("2006-01-02 15:04:05")
	rows, err := s.DB.Query(sqlInPlay, from, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventNote
	for rows.Next() {
		var key, text, duration, lastMention string
		if err := rows.Scan(&key, &text, &duration, &lastMention); err != nil {
			continue
		}
		stage := SettleStage(duration, lastMention, now)
		if stage == StageGone {
			continue
		}
		out = append(out, EventNote{Stage: stage, Text: text})
	}
	return out, rows.Err()
}

// NoteEventMentions 生成之后调用: **回复里真的提到了**哪条, 就把它的时钟重置。
//
// ⚠️ 时钟以"最后一次被提及"为准 (§3.6.8)。按生成时刻算的话,
// 一件被反复聊起的事会莫名其妙地"变模糊" —— 而那恰恰是反的。
func (s *Store) NoteEventMentions(reply string, now time.Time, limit int) (int, error) {
	if strings.TrimSpace(reply) == "" {
		return 0, nil
	}
	from := now.AddDate(0, 0, -40).Format("2006-01-02 15:04:05")
	rows, err := s.DB.Query(sqlInPlay, from, limit)
	if err != nil {
		return 0, err
	}
	type hit struct {
		key   string
		count int
	}
	var hits []hit
	for rows.Next() {
		var key, text, duration, lastMention string
		if err := rows.Scan(&key, &text, &duration, &lastMention); err != nil {
			continue
		}
		if MentionedIn(reply, text) {
			hits = append(hits, hit{key, 0})
		}
	}
	rows.Close()
	for _, h := range hits {
		_, _ = s.DB.Exec(`UPDATE convo_world_event SET last_mentioned_ts=?,
			mention_count=COALESCE(mention_count,0)+1 WHERE event_key=?`,
			now.Format("2006-01-02 15:04:05"), h.key)
	}
	// 反复型升级: "越被聊越实 —— 这才对" (§3.6.8)
	_, _ = s.DB.Exec(`UPDATE convo_world_event SET duration='stable'
		WHERE duration='background' AND COALESCE(mention_count,0) >= ?`, RepeatedUpgradeAt)
	return len(hits), nil
}
