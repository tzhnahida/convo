package convo

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tzhnahida/VocabLock/internal/clock"
)

// ─────────────────────────────────────────────────────────────
// §6.8 管理页: 偏向可见 / 它会说的 / 学过的 / 边界 / 夜伴 / 观察台
//
// 设计稿 L978:「**一个看不见偏向的群友, 比一个没有偏向的群友更危险**」。
//
// ⚠️ 这一层的存在理由不是"给用户看数据", 而是**让偏向可归因**:
// 决策层已经落了 speaker_uin / scene_key / stance 三列 (§6.10 护栏 3),
// 但**没有人读它**就等于没落 —— 偏向仍然是隐形的, 只是多了一张表。
// 所以这里的每一个接口都必须能回答「为什么是这个结果」, 而不只是给出数字。
// ─────────────────────────────────────────────────────────────

// biasWindowDays 偏向统计的窗口。
const biasWindowDays = 14

// ColdShoulderMinSamples 判定「冷落」的最少发言数。
// ⚠️ 少于这个数**不下结论**: 说了一次没被接, 那不叫冷落, 叫样本不足。
const ColdShoulderMinSamples = 5

// GET /api/v1/convo/bias — §6.8 偏向可见
//
// 返回三块:
//
//	① speakers —— 谁的话被接得多 / 谁被冷落 (按发言人聚合)
//	② scenes   —— 每个场景的三态分布 (沉默率是 §12 的核心读数)
//	③ explain  —— **为什么**: 当前的偏向阈值与关系量, 让数字可归因
func (s *Server) handleBiasGet(w http.ResponseWriter, r *http.Request) {
	since := time.Now().AddDate(0, 0, -biasWindowDays).Format(bodyTSLayout)
	rows, err := s.Store.DB.Query("SELECT speaker_uin, scene_key, stance, COALESCE(reason,''), COUNT(*) "+
		"FROM convo_decision_trace "+
		"WHERE ts >= ? AND speaker_uin <> '' GROUP BY speaker_uin, scene_key, stance, reason", since)
	if err != nil {
		http.Error(w, `{"error":"query"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type agg struct {
		spoke, reacted, silent, total int
	}
	bySpeaker := map[string]*agg{}
	byScene := map[string]*agg{}
	// reasons: 「为什么」的直方图 —— 这是本接口从"读数"变成"可归因"的那一步。
	reasons := map[string]int{}
	for rows.Next() {
		var uin, scene, stance, reason string
		var n int
		if err := rows.Scan(&uin, &scene, &stance, &reason, &n); err != nil {
			continue
		}
		if reason != "" {
			reasons[reason] += n
		}
		if bySpeaker[uin] == nil {
			bySpeaker[uin] = &agg{}
		}
		if byScene[scene] == nil {
			byScene[scene] = &agg{}
		}
		for _, a := range []*agg{bySpeaker[uin], byScene[scene]} {
			a.total += n
			switch stance {
			case string(StanceSpeak):
				a.spoke += n
			case string(StanceReact):
				a.reacted += n
			default:
				a.silent += n
			}
		}
	}
	if err := rows.Err(); err != nil {
		http.Error(w, `{"error":"scan"}`, http.StatusInternalServerError)
		return
	}

	speakers := make([]map[string]interface{}, 0, len(bySpeaker))
	for uin, a := range bySpeaker {
		m := map[string]interface{}{
			"uin": uin, "total": a.total, "spoke": a.spoke,
			"reacted": a.reacted, "silent": a.silent,
			"speak_rate":    rate(a.spoke, a.total),
			"silent_rate":   rate(a.silent, a.total),
			"cold_shoulder": a.total >= ColdShoulderMinSamples && a.spoke == 0,
			// favored「更愿意接谁」—— 偏向是**双向**的,
			// 只报"从不被接"会把一半的图景藏起来。
			"favored":           a.total >= ColdShoulderMinSamples && rate(a.spoke, a.total) >= 0.5,
			"sample_sufficient": a.total >= ColdShoulderMinSamples,
		}
		speakers = append(speakers, m)
	}
	sort.Slice(speakers, func(i, j int) bool {
		ti, tj := speakers[i]["total"].(int), speakers[j]["total"].(int)
		if ti != tj {
			return ti > tj
		}
		return speakers[i]["uin"].(string) < speakers[j]["uin"].(string)
	})

	scenes := make([]map[string]interface{}, 0, len(byScene))
	for k, a := range byScene {
		scenes = append(scenes, map[string]interface{}{
			"scene_key": k, "total": a.total, "spoke": a.spoke,
			"reacted": a.reacted, "silent": a.silent, "silent_rate": rate(a.silent, a.total),
		})
	}
	sort.Slice(scenes, func(i, j int) bool {
		return scenes[i]["scene_key"].(string) < scenes[j]["scene_key"].(string)
	})

	cfg := DefaultBiasConfig()
	type rc struct {
		Reason string `json:"reason"`
		N      int    `json:"n"`
	}
	reasonList := make([]rc, 0, len(reasons))
	for k, v := range reasons {
		reasonList = append(reasonList, rc{k, v})
	}
	sort.Slice(reasonList, func(i, j int) bool {
		if reasonList[i].N != reasonList[j].N {
			return reasonList[i].N > reasonList[j].N
		}
		return reasonList[i].Reason < reasonList[j].Reason
	})
	writeJSON(w, map[string]interface{}{
		"window_days": biasWindowDays,
		"speakers":    speakers,
		"scenes":      scenes,
		"reasons":     reasonList,
		// explain: **为什么是这个结果** —— 没有这一块, 上面的数字仍然不可归因。
		"explain": map[string]interface{}{
			"closeness_high":        cfg.ClosenessHigh,
			"regard_high":           cfg.RegardHigh,
			"floor_missed":          cfg.FloorMissed,
			"cold_shoulder_samples": ColdShoulderMinSamples,
			"note": "偏向 = 命中数 + Δ, 且不低于底线; 群里 multiplier 恒为 1.0 (§6.8)。" +
				"这里只显示**读数**, 不改行为 —— 改行为请调 bias 配置并重启。",
		},
	})
}

// pageParams 解析 limit/offset。**坏参数退回默认值, 不报错** ——
// 一个观测接口不该因为 ?limit=abc 就 500。
func pageParams(r *http.Request, defLimit, maxLimit int) (int, int) {
	limit, offset := defLimit, 0
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	if v := strings.TrimSpace(r.URL.Query().Get("offset")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	return limit, offset
}

// rate 比率。⚠️ 分母为 0 时返回 **0 而不是 100%** —— 与 SilentRate 同一条纪律。
func rate(n, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(n) / float64(total)
}

// GET /api/v1/convo/observation — §12 观察台
//
// 三态分布 / 沉默率 / 夜批状态 / 话题词表 / 事件池 —— 一眼看它今天在干什么。
func (s *Server) handleObservation(w http.ResponseWriter, r *http.Request) {
	fs := FrameStatsSnapshot()
	day := clock.Today()
	night := NightKeyOf(clock.Now())
	nightRuns, _ := s.Store.CounterGet("night_batch", night)
	learnRuns, _ := s.Store.CounterGet("learn_runs", day)
	shareWeek, _ := s.Store.CounterGet("share_week", WeekStartOf(day))
	topicKeys, _ := s.Store.RecentTopicKeys(ownerKeyOf(s), 7)
	// ⚠️ #11: 这里原来读的是**手工表** (convo_night_peer), 而生产库那张表是空的 ——
	// 于是控制台永远显示"缺名册 / ready:false", 而它昨晚**实跑了 71 轮**。
	// 读数撒谎比没有读数更糟: 你会照着它去修一个根本没坏的东西。
	// 现在与夜间批用**同一个口径** (NightRosterPlan), 只读、不生成世界观。
	peers, peerSrc := NightRosterPlan(s.Store, ownerKeyOf(s), nil, nil)
	sparks, _ := s.Store.SharableSparks(clock.Now(), 20)
	learned, _ := s.Store.RecentLearned(200)
	unsaid := 0
	for _, it := range learned {
		if it.SaidCount == 0 {
			unsaid++
		}
	}
	writeJSON(w, map[string]interface{}{
		"day": day,
		"stance": map[string]interface{}{
			"silent": fs.StanceSilent, "react": fs.StanceReact, "speak": fs.StanceSpeak,
			"silent_rate": fs.SilentRate(),
		},
		"night": map[string]interface{}{
			"key": night, "runs": nightRuns, "peers": len(peers),
			// #11: 名册**从哪来**必须一起说 —— 只报个数的话,
			// "手工表空但世界观 cast 有 5 人"和"真的没有人"长得一模一样。
			"peers_source": peerSrc, "peers_names": nightPeerNames(peers),
			// ⚠️ 把「没配名册」显式说出来 —— §3.7 会整批跳过, 而跳过是静默的。
			"ready": len(peers) > 0,
		},
		"learn": map[string]interface{}{
			"runs_today":   learnRuns,
			"weekdays":     learnWeekdayNames(),
			"is_learn_day": shouldLearnToday(clock.Now()),
			"state_now":    learnStateNow(s),
			"eligible_now": learnEligibleNow(s),
			// ⚠️ 「为什么是 0」必须能**自解释** —— 一个永远显示 0 的卡片
			// 和一条坏掉的链路长得一模一样。
			"why_not": learnWhyNot(shouldLearnToday(clock.Now()), learnEligibleNow(s), learnRuns, learnStateNow(s)),
		},
		"share_week": map[string]interface{}{"used": shareWeek, "cap": ShareWeeklyCap},
		"topic_keys": topicKeys,
		"sparks": map[string]interface{}{
			"sharable": len(sparks), "daily_max": SparkDailyMax,
		},
		"learned": map[string]interface{}{
			"total": len(learned), "never_said": unsaid,
			// 设计稿 L153: 「学到的 90% 永远不说出来」—— 这个比例是**读数**, 不是指标。
			"unsaid_ratio": rate(unsaid, len(learned)),
		},
	})
}

func learnWeekdayNames() []string {
	var out []string
	for _, d := range LearnWeekdays {
		out = append(out, d.String())
	}
	return out
}

// ownerKeyOf 管理页用的数据分区键 (与 Engine.ownerKey 同一条约定)。
func ownerKeyOf(s *Server) string {
	if s == nil || s.Store == nil {
		return "default"
	}
	return s.Store.OwnerKey()
}

// learnStateNow 当前槽位 (给"为什么没学"用)。
func learnStateNow(s *Server) string {
	ws, err := s.Store.LoadSchedule()
	if err != nil || ws == nil {
		return ""
	}
	return CurrentState(ws, clock.Now())
}

// learnEligibleNow 现在能不能学 (与 Engine.learnEligible 同一条判据)。
func learnEligibleNow(s *Server) bool {
	ws, err := s.Store.LoadSchedule()
	if err != nil || ws == nil {
		return false
	}
	cur := CurrentState(ws, clock.Now())
	if cur != StateFree && cur != StateSlack {
		return false
	}
	return !inSleepWindow(ws, clock.Now())
}

// learnWhyNot 「今天为什么没学」的人话。返回 "" = 现在就该学。
func learnWhyNot(isDay, eligible bool, runs int, cur string) string {
	switch {
	case runs > 0:
		return "" // 今天学过了
	case !isDay:
		return "今天不是学习日 (只在 周二/周四/周日)"
	case !eligible:
		return "现在不在 free/slack 槽 (当前 " + cur + ")"
	}
	return ""
}

// GET /api/v1/convo/sparks — §2.6 它会说的灵光
//
// ⚠️ 含**已沉降的** —— 「不是删除, 是沉降」是设计稿的硬要求,
// 一个只显示"可说"的页面会让人以为没被接住的灵光消失了。
func (s *Server) handleSparksList(w http.ResponseWriter, r *http.Request) {
	limit, offset := pageParams(r, 50, 500)
	all, err := s.Store.AllSparks(offset + limit)
	if err != nil {
		http.Error(w, `{"error":"query"}`, http.StatusInternalServerError)
		return
	}
	now := clock.Now()
	cfg := DefaultSparkStatusConfig()
	// ⚠️ 分页在**算完档位之后**截取 —— 先截再算会让同一页在不同时刻顺序不同。
	if offset > len(all) {
		offset = len(all)
	}
	all = all[offset:]
	if len(all) > limit {
		all = all[:limit]
	}
	out := make([]map[string]interface{}, 0, len(all))
	for _, sp := range all {
		in := SparkHeatInput{HeatDelta: sp.HeatDelta, LastTouchTS: sp.LastTouchTS, Now: now}
		out = append(out, map[string]interface{}{
			"id": sp.ID, "kind": string(sp.Kind), "content": sp.Content,
			"born": sp.BornTS, "talk_key": sp.TalkKey,
			"source_kind": sp.Source.Kind, "has_ref": sp.Source.HasRef,
			"heat":   SparkHeat(in),
			"status": string(SparkStatusAt(in, sp.SaidTS != "", cfg)),
			"said":   sp.SaidTS != "",
		})
	}
	writeJSON(w, map[string]interface{}{
		"sparks": out,
		"thresholds": map[string]interface{}{
			"fresh_at": cfg.FreshAt, "warm_at": cfg.WarmAt, "drop_below": cfg.DropBelow,
			"half_life_h": SparkHeatHalfLifeH,
			"note":        "status 是 heat + 时间的**现算**值, 不落库。热度的半衰期就是它衰减的速度。",
		},
	})
}

// GET /api/v1/convo/learned — §2.5 它学过什么
//
// ⚠️ Origin 是**给它看的来历**, 页面展示它是为了让人能审:「这条它到底从哪弄明白的」。
// 它自己**永远看不到** Origin (不进 prompt)。
func (s *Server) handleLearnedList(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.RecentLearned(200)
	if err != nil {
		http.Error(w, `{"error":"query"}`, http.StatusInternalServerError)
		return
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]interface{}{
			"id": it.ID, "kind": string(it.Kind), "topic_key": it.TopicKey,
			"content": it.Content, "origin": it.Origin, "born": it.BornTS,
			"said_count": it.SaidCount, "last_said": it.LastSaidTS,
		})
	}
	writeJSON(w, map[string]interface{}{
		"learned": out,
		"policy": map[string]interface{}{
			"reveal_min_roll":  RevealMinRoll,
			"cooldown_days":    RevealCooldownDays,
			"share_weekly_cap": ShareWeeklyCap,
			"learn_weekdays":   learnWeekdayNames(),
		},
	})
}

// POST /api/v1/convo/forget — §7.4 **遗忘权**。
//
// ⚠️ 这是唯一一个会**物理删除**的接口, 所以它必须回一份删除清单 ——
// 一次静默的删除和一次没发生过的删除长得一模一样, 而这件事上"以为删了"最坏。
func (s *Server) handleForget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method"}`, http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		UIN string `json:"uin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.UIN) == "" {
		http.Error(w, `{"error":"uin_required"}`, http.StatusBadRequest)
		return
	}
	rep, err := s.Store.ForgetSubject(body.UIN, clock.Now())
	if err != nil {
		http.Error(w, `{"error":"forget"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "report": rep})
}

// GET / PUT /api/v1/convo/night-peers — §3.7 同伴名册
//
// ⚠️ **没有这个入口, §3.7 等于永远不会跑**: 夜间社交在名册为空时整批跳过。
// 手工 SQL 也能写, 但"要会用 sqlite3 才能开的功能"等于没开。
// nightlyPeersJSON 名册的展示形态 (两个接口共用, 免得字段名漂开)。
func nightlyPeersJSON(ps []NightPeer) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(ps))
	for _, p := range ps {
		out = append(out, map[string]interface{}{
			"uin": p.UIN, "name": p.Name, "brief": p.Brief, "weight": p.Weight,
		})
	}
	return out
}

func (s *Server) routeNightPeers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		peers, err := s.Store.LoadNightPeers()
		if err != nil {
			http.Error(w, `{"error":"query"}`, http.StatusInternalServerError)
			return
		}
		out := make([]map[string]interface{}, 0, len(peers))
		for _, p := range peers {
			out = append(out, map[string]interface{}{
				"uin": p.UIN, "name": p.Name, "brief": p.Brief, "weight": p.Weight,
			})
		}
		// ⚠️ #11: 手工表**不是**"夜里会跟谁聊"。它只是**兜底名单** ——
		// 夜里优先用的是世界观 cast。原来这个接口只报手工表, 于是界面上写着
		// 「名册为空 → 整批跳过」, 而那一夜它实跑了 71 轮。
		effective, src := NightRosterPlan(s.Store, ownerKeyOf(s), nil, nil)
		writeJSON(w, map[string]interface{}{
			"peers": out, "budget": NightSocialBudget, "group_share_pct": NightGroupSharePct,
			// ⚠️ ready 判的是**夜里到底跑不跑得起来**, 不是"手工表里有没有人"。
			"ready": len(effective) > 0,
			// 真正会用的那份 + 它从哪来。
			"effective": nightlyPeersJSON(effective), "effective_source": src,
			"manual_count": len(peers),
			"note":         "夜里优先用**世界观 cast**; 手工表是兜底名单。两者都空才会整批跳过。",
		})
	case http.MethodPut:
		var body struct {
			UIN    int64   `json:"uin"`
			Name   string  `json:"name"`
			Brief  string  `json:"brief"`
			Weight float64 `json:"weight"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"bad"}`, http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(body.Name) == "" || body.UIN == 0 {
			// ⚠️ 空名字 / 无 uin 直接 400, 不静默丢弃 —— 静默丢弃会让"保存成功但没生效"。
			http.Error(w, `{"error":"name_and_uin_required"}`, http.StatusBadRequest)
			return
		}
		if err := s.Store.SaveNightPeer(NightPeer{UIN: body.UIN, Name: body.Name, Brief: body.Brief, Weight: body.Weight}); err != nil {
			http.Error(w, `{"error":"save"}`, http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true})
	case http.MethodDelete:
		uin, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("uin")), 10, 64)
		if err != nil || uin == 0 {
			http.Error(w, `{"error":"bad_uin"}`, http.StatusBadRequest)
			return
		}
		if _, err := s.Store.DB.Exec("DELETE FROM convo_night_peer WHERE uin = ?", uin); err != nil {
			http.Error(w, `{"error":"delete"}`, http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true})
	default:
		http.Error(w, `{"error":"method"}`, http.StatusMethodNotAllowed)
	}
}

// GET / PUT /api/v1/convo/boundaries-feedback — §6.4.3 边界开关
//
// 边界是**累计三次同类负反馈**升格出来的。开关只做一件事: 撤销升格。
// ⚠️ 它**不删计数** —— 把计数清零等于让"它被说过三次"这件事消失, 下次又要重新挨三遍。
func (s *Server) routeFeedbackBoundaries(w http.ResponseWriter, r *http.Request) {
	owner := ownerKeyOf(s)
	switch r.Method {
	case http.MethodGet:
		rows, err := s.Store.DB.Query("SELECT scene_key, kind, topic, n, promoted FROM convo_feedback ORDER BY n DESC")
		if err != nil {
			http.Error(w, `{"error":"query"}`, http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		out := []map[string]interface{}{}
		for rows.Next() {
			var scene, kind, topic string
			var n, promoted int
			if err := rows.Scan(&scene, &kind, &topic, &n, &promoted); err != nil {
				continue
			}
			m := map[string]interface{}{
				"scene_key": scene, "kind": kind, "topic": topic,
				"count": n, "promoted": promoted != 0,
			}
			if b, ok := FeedbackToBoundary(FeedbackKind(kind), topic); ok {
				m["boundary"] = b
			}
			out = append(out, m)
		}
		writeJSON(w, map[string]interface{}{
			"feedback": out, "promote_threshold": FeedbackPromoteThreshold,
			"note": "开关 = 撤销升格, **不删计数**。删了计数它下次还要重新挨三遍。",
		})
	case http.MethodPut:
		var body struct {
			SceneKey string `json:"scene_key"`
			Kind     string `json:"kind"`
			Topic    string `json:"topic"`
			Promoted bool   `json:"promoted"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"bad"}`, http.StatusBadRequest)
			return
		}
		v := 0
		if body.Promoted {
			v = 1
		}
		if _, err := s.Store.DB.Exec("UPDATE convo_feedback SET promoted = ? WHERE owner = ? AND scene_key = ? AND kind = ? AND topic = ?",
			v, owner, body.SceneKey, body.Kind, body.Topic); err != nil {
			http.Error(w, `{"error":"update"}`, http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true})
	default:
		http.Error(w, `{"error":"method"}`, http.StatusMethodNotAllowed)
	}
}
