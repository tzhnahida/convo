package convo

import (
	"encoding/json"
	"net/http"
	"strings"
)

// ─────────────────────────────────────────────────────────────
// §6.x 插话收敛的**安全阀**: 看 / 放行 (2026-10-16)
//
// `chime.go` 自己写着:
//
//   > **被群友说了就稳定在 1%**…**不自动恢复**…
//   > 要放开只能 owner 手动 `ClearChimeComplaint`。
//   > **这是这个功能唯一的安全阀。**
//
// ⚠️ 而它**从来没有调用者** —— 也就是说这个阀门**只出不进**:
// 一旦某个群被判成"被抱怨过", 它就**永久**收敛在 1%。
//
// ⚠️ 这**不是假想的风险**: 2026-10-16 00:52 生产上真的落了一条
// (`group:1034712035`, 群友 @ 着它说「别刷屏闭嘴」), 于是那个群的插话率
// 就永久钉在 1%, **而没有任何办法撤销**。
//
// ⚠️ 而误判是**可能**的: 判定只要求「命中抱怨词 + 这条在跟它说话」——
// 一句"别刷屏"如果是**对着别人**说的、或者只是引用, 也会落库。
// 收敛本身是对的(宁可少说也别惹人烦), 但**必须有一个能撤销的口子**。
// ─────────────────────────────────────────────────────────────

// routeChime GET = 看, POST = 放行。
func (s *Server) routeChime(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleChimeGet(w, r)
	case http.MethodPost:
		s.handleChimeClear(w, r)
	default:
		writeJSON(w, map[string]interface{}{"error": "method"})
	}
}

// handleChimeGet 列出**被抱怨过**的群。
//
// ⚠️ **没被抱怨过的不列** —— 列一堆"正常"只会让真正收敛的那些看不出来。
func (s *Server) handleChimeGet(w http.ResponseWriter, r *http.Request) {
	owner := ownerKeyOf(s)
	q := `SELECT scene_key, COALESCE(complained_ts,''), COALESCE(updated_ts,'') FROM convo_chime_state WHERE owner=? AND complained_ts <> '' ORDER BY complained_ts DESC`
	rows, err := s.Store.DB.Query(q, owner)
	if err != nil {
		writeJSON(w, map[string]interface{}{"error": "query"})
		return
	}
	defer rows.Close()
	out := []map[string]interface{}{}
	for rows.Next() {
		var scene, ts, upd string
		if err := rows.Scan(&scene, &ts, &upd); err != nil {
			continue
		}
		out = append(out, map[string]interface{}{"scene_key": scene, "complained_ts": ts, "updated": upd})
	}
	writeJSON(w, map[string]interface{}{
		"converged": out,
		// ⚠️ 把两个概率一起给出去 —— 在页面上写死 10/1 会与代码里的常量分叉。
		"rate_pct":       ChimeRollPct,
		"rate_after_pct": ChimeRollPctAfterComplaint,
		"clear_endpoint": "/api/v1/convo/chime",
	})
}

// handleChimeClear 放行一个群 —— owner 手动撤销收敛。
//
// ⚠️ 它是**幂等**的: 没被抱怨过的群调它**什么都不改**
// (`UPDATE` 影响 0 行), **不会插一行 `complained_ts=”` 的假记录** ——
// 那会让"从没被说过"与"被说过又放行了"长得一模一样。
func (s *Server) handleChimeClear(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SceneKey string `json:"scene_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, map[string]interface{}{"error": "bad json"})
		return
	}
	scene := strings.TrimSpace(body.SceneKey)
	if scene == "" {
		writeJSON(w, map[string]interface{}{"error": "scene_key required", "code": "scene_key_required"})
		return
	}
	// ⚠️ **先看有没有** —— 不然"放行了"和"本来就没有"分不出来。
	before, err := s.Store.ChimeComplained(ownerKeyOf(s), scene)
	if err != nil {
		writeJSON(w, map[string]interface{}{"error": "query"})
		return
	}
	if err := s.Store.ClearChimeComplaint(ownerKeyOf(s), scene); err != nil {
		writeJSON(w, map[string]interface{}{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{
		"scene_key": scene,
		"cleared":   before,
		// ⚠️ 回显**现在的概率** —— 让页面不必自己推。
		"rate_pct": ChimeRollPct,
	})
}
