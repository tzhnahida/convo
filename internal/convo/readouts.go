package convo

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/tzhnahida/convo/internal/clock"
)

// ─────────────────────────────────────────────────────────────
// 读数面: **时序 / 关系两层 / 内部状态**
//
// 这一组端点是为控制台存在的 (§12: 机制有没有生效, 要看数字)。三条口径:
//
//	① **只读**: 这些处理器一个字节都不写库。会改状态的观测台,
//	   会让"看见的现象"和"系统的行为"互相影响。
//	② **缺数据不是错**: 表没建 / 没有记录 → 返回**空数组**, 不是 500。
//	   页面上一张永远红的卡, 和"这个机制没跑"长得一模一样。
//	   而**真的**查询失败要说出来 (error 字段), 不许吞。
//	③ **镜像判据要注明**: Server 手上只有 Store, 拿不到 Engine 的实例方法 ——
//	   所以疲劳档位这类读数在这里是**重算**的, 每处都注明它镜像的是谁。
// ─────────────────────────────────────────────────────────────

// DayCount 一天的计数 (时序图的最小单元)。
type DayCount struct {
	Day string `json:"day"`
	N   int    `json:"n"`
}

// MessageDay 一天的消息量。**分两条线** —— 一条线看不出"谁在说"。
type MessageDay struct {
	Day  string `json:"day"`
	User int    `json:"user"`
	AI   int    `json:"ai"`
}

// dayRange 最近 n 天的日期列表 (含今天), 升序。
//
// ⚠️ 图表的横轴必须**连续** —— 没数据的那天要补 0, 否则折线会直接跨过去,
// 而"那天什么都没有"和"那天数据丢了"看起来一模一样。
func dayRange(days int) []string {
	days = clampDays(days)
	now := clock.Now()
	out := make([]string, 0, days)
	for i := days - 1; i >= 0; i-- {
		out = append(out, now.AddDate(0, 0, -i).Format("2006-01-02"))
	}
	return out
}

func clampDays(days int) int {
	if days <= 0 {
		return 30
	}
	if days > 365 {
		return 365
	}
	return days
}

// MessageSeries 每天的消息量 (user / ai 两条线)。
//
// ⚠️ 日期从 ts 的**前 10 个字符**取: convo_messages.ts 就是 "YYYY-MM-DD HH:MM:SS"
// (全库同一套格式)。用 date() 会在格式变化时静默给出另一种口径。
func (s *Store) MessageSeries(days int) ([]MessageDay, error) {
	days = clampDays(days)
	if s == nil || s.DB == nil {
		return nil, nil
	}
	byDay := map[string]MessageDay{}
	rows, err := s.DB.Query("SELECT substr(ts,1,10) AS d, "+
		"SUM(CASE WHEN dir='user' THEN 1 ELSE 0 END), "+
		"SUM(CASE WHEN dir='ai' THEN 1 ELSE 0 END) "+
		"FROM convo_messages WHERE substr(ts,1,10) >= ? GROUP BY d", dayRange(days)[0])
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d MessageDay
		if err := rows.Scan(&d.Day, &d.User, &d.AI); err != nil {
			return nil, err
		}
		byDay[d.Day] = d
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]MessageDay, 0, days)
	for _, day := range dayRange(days) {
		d := byDay[day]
		d.Day = day
		out = append(out, d)
	}
	return out, nil
}

// CounterSeries 一个计数器最近 days 天的值 (缺的那天补 0)。
func (s *Store) CounterSeries(name string, days int) ([]DayCount, error) {
	days = clampDays(days)
	if s == nil || s.DB == nil || strings.TrimSpace(name) == "" {
		return nil, nil
	}
	byDay := map[string]int{}
	rows, err := s.DB.Query("SELECT day, n FROM convo_counters WHERE name = ? AND day >= ?",
		strings.TrimSpace(name), dayRange(days)[0])
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var day string
		var n int
		if err := rows.Scan(&day, &n); err != nil {
			return nil, err
		}
		byDay[day] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]DayCount, 0, days)
	for _, day := range dayRange(days) {
		out = append(out, DayCount{Day: day, N: byDay[day]})
	}
	return out, nil
}

// ——— 关系两层 ———

// SceneBondRow 一个场景的关系量 + 它折出来的阶段。
//
// ⚠️ 阶段用 **BondStageShared**(质与量取较高档) —— 与 heatStageOf 同一个判据。
// 两处各判一次会让"我们是熟人吗"有两个互相矛盾的答案。
type SceneBondRow struct {
	SceneKey    string  `json:"scene_key"`
	Kind        string  `json:"kind"`
	Closeness   float64 `json:"closeness"`
	Regard      float64 `json:"regard"`
	Familiarity float64 `json:"familiarity"`
	Stage       string  `json:"stage"`
}

// AllSceneBonds 全部场景的关系量 (读不出 → 空数组)。
func (s *Store) AllSceneBonds(owner string) ([]SceneBondRow, error) {
	if s == nil || s.DB == nil {
		return nil, nil
	}
	rows, err := s.DB.Query("SELECT scene_key, closeness, regard, familiarity "+
		"FROM convo_bond WHERE owner = ? ORDER BY closeness DESC, scene_key", owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SceneBondRow
	for rows.Next() {
		var r SceneBondRow
		var b Bond
		if err := rows.Scan(&r.SceneKey, &b.Closeness, &b.Regard, &b.Familiarity); err != nil {
			return nil, err
		}
		r.Closeness, r.Regard, r.Familiarity = b.Closeness, b.Regard, b.Familiarity
		r.Kind = SceneKindOf(r.SceneKey)
		r.Stage = string(BondStageShared(b))
		out = append(out, r)
	}
	return out, rows.Err()
}

// PersonBondRow 人物层的一行: **跨场景按 uin 累积**的那个人 (owner 定规 2026-10-16)。
type PersonBondRow struct {
	UIN         string  `json:"uin"`
	Closeness   float64 `json:"closeness"`
	Regard      float64 `json:"regard"`
	Familiarity float64 `json:"familiarity"`
	Stage       string  `json:"stage"`
}

// AllPersonBonds 人物层全部记录。
func (s *Store) AllPersonBonds(owner string) ([]PersonBondRow, error) {
	if s == nil || s.DB == nil {
		return nil, nil
	}
	rows, err := s.DB.Query("SELECT uin, closeness, regard, familiarity "+
		"FROM convo_bond_person WHERE owner = ? ORDER BY familiarity DESC, uin", owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PersonBondRow
	for rows.Next() {
		var r PersonBondRow
		var b Bond
		if err := rows.Scan(&r.UIN, &b.Closeness, &b.Regard, &b.Familiarity); err != nil {
			return nil, err
		}
		r.Closeness, r.Regard, r.Familiarity = b.Closeness, b.Regard, b.Familiarity
		r.Stage = string(BondStageShared(b))
		out = append(out, r)
	}
	return out, rows.Err()
}

// ——— 处理器 ———

// handleSeries 时序读数 (图表的数据源)。只读。
//
//	?kind=msgs              最近 ?days 天每天的消息量 (user/ai 两条线)
//	?kind=counters&name=... 一个计数器最近 ?days 天的值 (不带 name 就给一组常用的)
//	?kind=heat&scene=...    热力流水 (最近 ?limit 笔, 自带 before/after/delta)
//	?kind=mood              心情流水 (最近 ?limit 条)
func (s *Server) handleSeries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, map[string]interface{}{"error": "method"})
		return
	}
	q := r.URL.Query()
	kind := strings.TrimSpace(q.Get("kind"))
	days := clampDays(intParam(q.Get("days"), 30))
	limit := intParam(q.Get("limit"), 120)
	if limit <= 0 || limit > 500 {
		limit = 120
	}
	out := map[string]interface{}{"kind": kind, "days": days}
	switch kind {
	case "msgs":
		rows, err := s.Store.MessageSeries(days)
		if err != nil {
			out["error"] = err.Error()
		}
		out["series"] = rows
	case "counters":
		// 不带 name: 给一组"日子过成什么样"的常用计数 —— 一次请求画一张活动图。
		names := []string{"ai_opens", "rounds", "user_msgs", "night_batch", "learn_runs", "defiance"}
		if n := strings.TrimSpace(q.Get("name")); n != "" {
			names = []string{n}
		}
		got := map[string]interface{}{}
		for _, n := range names {
			rows, err := s.Store.CounterSeries(n, days)
			if err != nil {
				out["error"] = err.Error()
				break
			}
			got[n] = rows
		}
		out["counters"] = got
	case "heat":
		rows, err := s.Store.RecentHeatLog(strings.TrimSpace(q.Get("scene")), limit)
		if err != nil {
			out["error"] = err.Error()
		}
		pts := []map[string]interface{}{}
		for _, row := range rows {
			pts = append(pts, map[string]interface{}{
				"ts": row.TS, "after": row.After, "delta": row.Delta,
				"slot_state": row.SlotState, "reason": row.Reason,
			})
		}
		out["points"] = pts
	case "mood":
		causes, err := s.Store.MoodLog(limit)
		if err != nil {
			out["error"] = err.Error()
		}
		out["mood_log"] = causes
	default:
		out["error"] = "kind 只能是 msgs / counters / heat / mood"
	}
	writeJSON(w, out)
}

// handleBonds 关系**两层**的读数: 场景层 (每个场景一行) + 人物层 (每个 uin 一行)。
//
// ⚠️ 人物层存在的理由是"群与私聊是同一个人": 只有场景层时,
// 群里聊 900 条而私聊没聊过, 私聊那边会恒为**陌生** —— 于是永远不开口。
// handlePokes 戳一戳的审计读入 —— 谁戳的、它回没回、为什么。
//
// ⚠️ 与自我表达那份审计同一个理由: 「装死」是设计的一部分,
// 那就必须能看见它**什么时候**装死、**为什么** —— 否则看起来就是"它坏了"。
func (s *Server) handlePokes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, map[string]interface{}{"error": "method"})
		return
	}
	rows, err := s.Store.LoadPokes(intParam(r.URL.Query().Get("limit"), 30))
	if err != nil {
		writeJSON(w, map[string]interface{}{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{"pokes": PokeStatsJSON(rows)})
}

// handleSelfActions 对外动作的**审计读入** —— 它自己做过什么, 连同理由。
//
// ⚠️ 这个端点存在的理由与控制权那一侧对偶: 控制权交给它之后,
// 唯一能让 owner 放心的东西就是「每一次决定都能回看」。
// 所以连它**拒绝**的那些也要出现在这里 —— 拒绝也是一次决定。
func (s *Server) handleSelfActions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, map[string]interface{}{"error": "method"})
		return
	}
	rows, err := s.Store.LoadSelfActions(intParam(r.URL.Query().Get("limit"), 50))
	if err != nil {
		writeJSON(w, map[string]interface{}{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{"actions": SelfActionsJSON(rows)})
}

func (s *Server) handleBonds(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, map[string]interface{}{"error": "method"})
		return
	}
	owner := s.Store.OwnerKey()
	out := map[string]interface{}{"owner": owner}
	if rows, err := s.Store.AllSceneBonds(owner); err != nil {
		out["error"] = err.Error()
	} else {
		out["scene"] = rows
	}
	if rows, err := s.Store.AllPersonBonds(owner); err != nil {
		out["error"] = err.Error()
	} else {
		out["person"] = rows
	}
	// 阈值一起给出: 页面要画"离下一档还差多少", 而阈值不该在页面上另抄一份。
	out["thresholds"] = map[string]interface{}{
		"acquaint": BondAcquaintAt, "familiar": BondFamiliarAt, "intimate": BondIntimateAt,
	}
	writeJSON(w, out)
}

// handleState 内部状态的**一屏读数**: 心情 / 身体 / 睡眠 / 疲劳 / 中断 / 热力 / 应变。
//
// ⚠️ 疲劳档位在这里是**重算**的 —— 镜像 `Engine.fatigueTier`,
// 输入逐字一致: 日程上忙了多久 (BusyMinutesToday) + 今天几轮 (rounds 计数)。
// 为什么重算而不是问引擎: Server 手上只有 Store; 而返回一个**空**读数
// 比返回一个算出来的数更坏 —— 页面会显示"不疲劳", 而那是另一回事。
func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, map[string]interface{}{"error": "method"})
		return
	}
	now := clock.Now()
	out := map[string]interface{}{"now": clock.TodayTS()}
	if m, err := s.Store.LoadMood(); err == nil {
		out["mood"] = m
	}
	if b, err := s.Store.LoadBody(); err == nil {
		out["body"] = b
		out["body_effect"] = b.Effect(now)
	}
	if n, ok, err := s.Store.LastNight(); err == nil && ok {
		out["sleep"] = n
	}
	ws, _ := s.Store.LoadSchedule()
	busy := BusyMinutesToday(ws, now)
	rounds := counterOrZero(s.Store, "rounds", clock.Today())
	tier, why := DecideFatigue(FatigueInput{BusyMinutes: busy, RoundsToday: rounds})
	out["fatigue"] = map[string]interface{}{
		"tier": tier, "reason": why, "busy_minutes": busy, "rounds_today": rounds,
	}
	if it, ok, err := s.Store.LoadInterruption(); err == nil && ok {
		out["interruption"] = map[string]interface{}{
			"anchor_ts": it.AnchorTS, "reason": it.Reason, "due_ts": it.DueTS,
			"suspended": it.SuspendedAt(now), "due": it.DueAt(now),
		}
	}
	out["strain"] = s.Store.LoadStrain()
	// 热力: 每个场景的当下值 + **上次结算时的上限** (审计用)。
	// ⚠️ cap 现算需要引擎的 HeatConfig (Server 没有), 所以给的是结算时那一个 ——
	// 它是真的、有出处的, 而不是照着默认表猜的。
	scenes := []string{DefaultSceneKey}
	if bonds, err := s.Store.AllSceneBonds(s.Store.OwnerKey()); err == nil {
		for _, b := range bonds {
			if b.SceneKey != DefaultSceneKey {
				scenes = append(scenes, b.SceneKey)
			}
		}
	}
	heat := []map[string]interface{}{}
	for _, sc := range scenes {
		st, ok, err := s.Store.LoadHeat(sc)
		if err != nil || !ok {
			continue
		}
		heat = append(heat, map[string]interface{}{
			"scene": sc, "value": st.Value, "cap_at_settle": st.CapAtSettle,
			"settled_ts": st.SettledTS, "session_seq": st.SessionSeq,
		})
	}
	out["heat"] = heat
	writeJSON(w, out)
}

// intParam 解析整数查询参数 (坏值 → 默认)。
//
// ⚠️ 坏值**不报错**: 观测台的一个参数写错不该让整页红掉 ——
// 那会把"我打错了"伪装成"服务坏了"。
func intParam(v string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
		return n
	}
	return def
}
