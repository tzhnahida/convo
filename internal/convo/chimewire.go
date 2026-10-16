package convo

import (
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// 插话闸门的**接线** —— 把 DecideChime 挂进群裁决。
//
// 在它之前, 非 @ 的群消息一律停在 `silent (undecidable)` ——
// 它**只有被叫才存在**, 而它的灵光 / 学过的碎片全都烂在库里。
// ─────────────────────────────────────────────────────────────

// ChimeRollFn 掷骰函数。nil = Dice。
//
// ⚠️ 它是插话判定里**唯一的随机源** —— 不注入的话, "10% 真的生效了"
// 和"配额真的封住了"这两件事都没法验, 只能靠肉眼盯日志。
func (e *Engine) chimeRoll(n int) int {
	if e.ChimeRollFn != nil {
		return e.ChimeRollFn(n)
	}
	return Dice(n)
}

// chimeAt 群裁决落到 undecidable 时的插话判定。
//
// ⚠️ 读失败一律**退回沉默** —— 插话是"打扰"那一侧的例外,
// 而例外的失败方向只能是"不做"。反过来会让一次数据库抖动变成刷屏。
func (e *Engine) chimeAt(sc SceneMsg) ChimeDecision {
	no := ChimeDecision{}
	if e.Store == nil || strings.TrimSpace(sc.SceneKey) == "" {
		return no
	}
	owner := e.ownerKey()
	complained, err := e.Store.ChimeComplained(owner, sc.SceneKey)
	if err != nil {
		log.Warn("convo:chime", "插话状态读取失败, 本次不插话: %v", err)
		return no
	}
	used, topicUsed, sinceLast, err := e.Store.ChimeUsage(owner, sc.SceneKey)
	if err != nil {
		log.Warn("convo:chime", "插话计数读取失败, 本次不插话: %v", err)
		return no
	}
	dec := DecideChime(ChimeInput{
		TopicMatch:     e.chimeTopicMatch(sc),
		Roll:           e.chimeRoll(100),
		UsedToday:      used,
		TopicUsedToday: topicUsed,
		GroupMsgsToday: e.Store.GroupMsgsToday(sc.SceneKey),
		WakingElapsed:  e.wakingElapsed(),
		SinceLast:      sinceLast,
		Complained:     complained,
	})
	return dec
}

// chimeTopicMatch 群友聊的这件事, 它是不是**正好知道点什么**。
//
// ⚠️ 信号源是**它自己攒的东西** (灵光 + 学过的), 不是通用百科 ——
// 这才是"插话"和"抢答"的区别: 前者是"我想起一件事", 后者是"我知道答案"。
//
// ⚠️ 用 2-gram 线索重合 (与 §7.2 联想同一把尺子), 不用语义模型:
// 这条判据要在**每一条**群消息上跑, 一次 LLM 调用会把成本打穿。
func (e *Engine) chimeTopicMatch(sc SceneMsg) bool {
	if e.Store == nil {
		return false
	}
	mine := map[string]bool{}
	if sparks, err := e.Store.SharableSparks(clock.Now(), 8); err == nil {
		for _, sp := range sparks {
			for _, cu := range BigramCues(sp.Content, 0) {
				mine[cu.Token] = true
			}
		}
	}
	if learned, err := e.Store.RecentLearned(30); err == nil {
		for _, it := range learned {
			for _, cu := range BigramCues(it.TopicKey+" "+it.Content, 0) {
				mine[cu.Token] = true
			}
		}
	}
	if len(mine) == 0 {
		return false // 它自己还什么都没攒下 —— 没有"正好知道"可言
	}
	// 比的是**这条消息 + 最近几句**: 一个话题很少在一句里说全。
	for _, txt := range append([]string{sc.Text}, e.recentSceneTexts(sc.SceneKey, 4)...) {
		for _, cu := range BigramCues(txt, 0) {
			if mine[cu.Token] {
				return true
			}
		}
	}
	return false
}

// recentSceneTexts 这个场景最近几条**别人**说的话。
func (e *Engine) recentSceneTexts(sceneKey string, n int) []string {
	if n <= 0 {
		return nil
	}
	rows, err := e.Store.DB.Query("SELECT payload_json FROM convo_messages WHERE scene_key = ? "+
		"AND dir = 'user' ORDER BY id DESC LIMIT ?", sceneKey, n)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var raw string
		if rows.Scan(&raw) == nil {
			out = append(out, raw)
		}
	}
	return out
}

// wakingElapsed 今天**醒着的时间**过了几分之几。
//
// ⚠️ 用它而不是"一天过了几分之几": 它凌晨 3 点本来就该在睡,
// 按整天算的话, 深夜会凭空多出一截预算。
func (e *Engine) wakingElapsed() float64 {
	ws, err := e.Store.LoadSchedule()
	if err != nil || ws == nil {
		return 1 // 读不到就当作"已经过完" —— 保守方向是让它**能**说话
	}
	// ⚠️ **从槽表解**, 不读展示字段 (#12): 展示字段只描述"一般情况",
	// 而真假由**那一天的槽表**决定 —— 生产上真出现过"周三整日没有 sleep 槽"的日程,
	// 那时按展示字段算出来的"深夜没预算"是**假的**。
	sleep, wake, ok := SleepWindowOf(ws, weekdayKey(clock.Now()))
	if !ok || sleep <= wake {
		return 1 // 那天没有睡眠段 → 它整天都是醒的 (槽表就是这么说的)
	}
	now := clock.Now()
	cur := now.Hour()*60 + now.Minute()
	switch {
	case cur <= wake:
		return 0
	case cur >= sleep:
		return 1
	}
	// ⚠️ 分母**必须**用 sleepWindowMinutes —— 写 `sleep-wake` 会得到 960 分钟,
	// 于是"醒着的比例"永远算小一半, 而它同样是静默的(读数看着很正常)。
	return float64(cur-wake) / float64(sleepWindowMinutes(sleep, wake))
}

// hhmm 解析 "HH:MM" → 一天中的第几分钟。
func hhmm(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	h := int(s[0]-'0')*10 + int(s[1]-'0')
	m := int(s[3]-'0')*10 + int(s[4]-'0')
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// ——— 计数落库 ———

// ChimeUsage 读今天的两笔账 + 距上次插话多久。
func (s *Store) ChimeUsage(owner, sceneKey string) (int, int, time.Duration, error) {
	var day, last string
	var used, topicUsed int
	err := s.DB.QueryRow("SELECT COALESCE(used_day,''), COALESCE(used_n,0), "+
		"COALESCE(topic_n,0), COALESCE(last_ts,'') FROM convo_chime_state "+
		"WHERE owner = ? AND scene_key = ?", owner, sceneKey).Scan(&day, &used, &topicUsed, &last)
	if err != nil {
		// 没记录 = 今天一次都没插过。**这不是错误。**
		return 0, 0, 0, nil
	}
	today := clock.Today()
	if day != today {
		// 跨天: 计数归零, 但 complained_ts 不归零 (收敛是永久的)。
		return 0, 0, 0, nil
	}
	var since time.Duration
	if t, ok := ParseMsgTS(last); ok {
		since = clock.Now().Sub(t)
	}
	return used, topicUsed, since, nil
}

// BumpChime 记一次插话。**跨天自动归零**。
func (s *Store) BumpChime(owner, sceneKey string, topic bool) error {
	col := "used_n"
	if topic {
		col = "topic_n"
	}
	day := clock.Today()
	now := clock.Now().Format(bodyTSLayout)
	// 先确保今天这一行存在且是今天的: 跨天时两笔账一起清零。
	_, err := s.DB.Exec("INSERT INTO convo_chime_state (owner, scene_key, used_day, used_n, topic_n, last_ts, updated_ts) "+
		"VALUES (?,?,?,0,0,?,?) ON CONFLICT(owner, scene_key) DO UPDATE SET "+
		"used_n = CASE WHEN convo_chime_state.used_day = excluded.used_day THEN convo_chime_state.used_n ELSE 0 END, "+
		"topic_n = CASE WHEN convo_chime_state.used_day = excluded.used_day THEN convo_chime_state.topic_n ELSE 0 END, "+
		"used_day = excluded.used_day, last_ts = excluded.last_ts, updated_ts = excluded.updated_ts",
		owner, sceneKey, day, now, clock.TodayTS())
	if err != nil {
		return err
	}
	_, err = s.DB.Exec("UPDATE convo_chime_state SET "+col+" = "+col+" + 1 WHERE owner = ? AND scene_key = ?", owner, sceneKey)
	return err
}

// GroupMsgsToday 这个群今天**别人**说了多少条。
//
// ⚠️ 只数 dir='user' —— 把 AI 自己插的话也数进去的话,
// 它插得越多额度越高, 是个正反馈。
func (s *Store) GroupMsgsToday(sceneKey string) int {
	var n int
	_ = s.DB.QueryRow("SELECT COUNT(*) FROM convo_messages WHERE scene_key = ? AND dir = 'user' "+
		"AND ts >= ?", sceneKey, clock.Today()+" 00:00:00").Scan(&n)
	return n
}
