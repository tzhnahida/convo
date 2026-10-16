package convo

import (
	"net/http"
	"strconv"
	"strings"
)

// routeHeat GET = 热力的当下值与最近流水。
//
// §12: 机制有没有生效, 要看数字。
//
// 而这一条读数的意义在 §2.68 里写清楚了: 热力链有三段, 而每一段失效时
// 现象都只是"它不主动开口" —— 从外面看不出是哪一段断的。
//
// 流水表 convo_heat_log 每一笔都带 before / after / delta / seq,
// 所以"有没有入账""序号有没有推进""是不是新会话"三件事一眼能分辨。
//
// 查询参数: scene (缺省 = 全部场景), limit (缺省 20)。
func (s *Server) routeHeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, map[string]interface{}{"error": "method"})
		return
	}
	scene := strings.TrimSpace(r.URL.Query().Get("scene"))
	limit := 20
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	out := map[string]interface{}{"scene": scene, "limit": limit}

	// 当下值那半**不在这里** —— 读它的是 Engine 侧的 `heatNow()` ✓,
	// 而 `Server` 手上只有 `Store` ✓。
	// ⇒ 这一条入口只给**流水**: 而流水自带 before / after / seq ✓,
	// 所以"现在是多少"从最后一笔的 after 就能读出来 ✓。

	rows, err := s.Store.RecentHeatLog(scene, limit)
	if err != nil {
		writeJSON(w, map[string]interface{}{"error": "log query", "detail": err.Error()})
		return
	}
	log := []map[string]interface{}{}
	for _, r := range rows {
		log = append(log, map[string]interface{}{
			"ts":      r.TS,
			"reason":  r.Reason,
			"ref_uin": r.RefUIN,
			"delta":   r.Delta,
			"before":  r.Before,
			"after":   r.After,
			"seq":     r.Seq,
			// slot_state 是"delta 为什么是 0"的直接答案: focus 的权重显式为 0,
			// sleep 根本不在 GainByState 里 (2026-10-16 加, 见 heat.go 的 HeatSettle)。
			"slot_state": r.SlotState,
		})
	}
	out["log"] = log
	writeJSON(w, out)
}
