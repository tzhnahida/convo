package convo

import (
	"net/http"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// ——— §4.5 层 2 行程: 看 / 重排 ———
//
// ⚠️ 为什么要开这个口:
// ① **它只在夜里跑一次** —— 出问题时白天没有任何补救手段, 而"今天没有安排"
//    和"这一步根本没接线"在观测面上长得一模一样;
// ② owner 得**看得到明天排了什么** —— 一份排歪的行程会污染一整天的对话,
//    而它是"系统事实", 进提示词之后它就会照着说。

// handleDayPlanGet 今天的与明天的各一份。
func (s *Server) handleDayPlanGet(w http.ResponseWriter, r *http.Request) {
	owner := ownerKeyOf(s)
	today := clock.Today()
	tomorrow := TomorrowKey(clock.Now())
	tp, tok, _ := s.Store.LoadDayPlan(owner, today)
	mp, mok, _ := s.Store.LoadDayPlan(owner, tomorrow)
	writeJSON(w, map[string]interface{}{
		"today":    map[string]interface{}{"ok": tok, "day": today, "plan": tp},
		"tomorrow": map[string]interface{}{"ok": mok, "day": tomorrow, "plan": mp},
	})
}

// handleDayPlanRegen 用当前人格重排**明天**的行程。
//
// ⚠️ 校验不过**不落库** —— 一份坏行程比没有更糟(SaveDayPlan 会再拦一道)。
func (s *Server) handleDayPlanRegen(w http.ResponseWriter, r *http.Request) {
	if s.Chatter == nil {
		writeJSON(w, map[string]interface{}{"error": "ai not configured", "code": "ai_unconfigured"})
		return
	}
	p, err := s.Store.LoadProfile()
	if err != nil || p == nil {
		http.Error(w, `{"error":"load profile"}`, http.StatusInternalServerError)
		return
	}
	ws, werr := s.Store.LoadSchedule()
	if werr != nil || ws == nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "读不到周日程, 判不出睡眠窗"})
		return
	}
	ctx, cancel := contextWithTimeout(r, 180*time.Second)
	defer cancel()
	day := TomorrowKey(clock.Now())
	in := buildDayPlanInput(s.Store, ownerKeyOf(s), day, p, nil, ws)
	plan, gerr := GenerateDayPlan(ctx, s.Chatter, in, ws)
	if gerr != nil {
		// ⚠️ 失败是**正常结局**(宁可没有不可现编), 所以回 200 + ok:false, 不是 5xx。
		log.Warn("convo:dayplan", "手动重排失败: %v", gerr)
		writeJSON(w, map[string]interface{}{"ok": false, "error": gerr.Error()})
		return
	}
	if serr := s.Store.SaveDayPlan(ownerKeyOf(s), plan, ws); serr != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "save: " + serr.Error()})
		return
	}
	log.Info("convo:dayplan", "手动重排: %s · %d 条", day, len(plan.Items))
	writeJSON(w, map[string]interface{}{"ok": true, "day": day, "plan": plan})
}

// handleDayPlan 路由: GET 看 / POST 重排。
func (s *Server) handleDayPlan(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleDayPlanGet(w, r)
	case http.MethodPost:
		s.handleDayPlanRegen(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}
