package convo

import (
	"encoding/json"
	"net/http"
)

// ─────────────────────────────────────────────────────────────
// §教练机 **撤销一条教错的规则** —— owner 的出口 (2026-10-16 接线)
//
// ⚠️ 教练机本身**是接了的** ✓: owner 在对话里发「教练: <规则>」,
// `onUserText` 顶部短路 → `AddCoachRule` ✓, 然后规则进 `coachNote` → 提示词 ✓。
//
// ⚠️ 而 `ClearCoachRules`(「**owner 想重新开始教**」) **一个调用者都没有** ✗:

//   生产调用: 0 · 测试调用: 0
//
// ⇒ 于是**教错一条规则没法撤销** ✗ —— 而它每一轮都会进提示词 ✓。
//
// ⚠️ 这与 §2.58 那条(**插话收敛的安全阀**)是**同一个形状**:
// 一个写着"owner 手动"的出口, 写好了, 而没有入口 ✓。
// ⇒ 而处置也一样: **给它一个入口**, 而不是删掉它 ✓。
// ─────────────────────────────────────────────────────────────

// routeCoach GET = 看当前生效的规则, POST = 全部停用。
func (s *Server) routeCoach(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleCoachGet(w, r)
	case http.MethodPost:
		s.handleCoachClear(w, r)
	default:
		writeJSON(w, map[string]interface{}{"error": "method"})
	}
}

// handleCoachGet 列出**生效中**的规则。
//
// ⚠️ 只列 active 的 —— 列一堆已停用的会让"现在到底在被什么约束"看不出来 ✓
// (`ClearCoachRules` 是 `UPDATE ... SET active=0`, **不删行** ✓)。
func (s *Server) handleCoachGet(w http.ResponseWriter, r *http.Request) {
	owner := ownerKeyOf(s)
	rows, err := s.Store.DB.Query(
		`SELECT id, text, COALESCE(scope,'') FROM convo_coach WHERE owner=? AND active=1 ORDER BY id`, owner)
	if err != nil {
		writeJSON(w, map[string]interface{}{"error": "query"})
		return
	}
	defer rows.Close()
	out := []map[string]interface{}{}
	for rows.Next() {
		var id int64
		var text, scope string
		if err := rows.Scan(&id, &text, &scope); err != nil {
			continue
		}
		out = append(out, map[string]interface{}{"id": id, "text": text, "scope": scope})
	}
	writeJSON(w, map[string]interface{}{
		"rules":          out,
		"clear_endpoint": "/api/v1/convo/coach",
	})
}

// handleCoachClear 全部停用 —— owner 想重新开始教。
//
// ⚠️ **是"停用"不是"删除"** ✓: `ClearCoachRules` 只把 `active` 置 0 ✓,
// 所以历史还在, 而一个人**可以回头查"它以前被教过什么"** ✓。
// ⚠️ 而返回值必须带上**到底停用了几条** —— 否则"本来就没有"与"刚停用了三条"
// 在调用方看起来一模一样 ✓ (与 §2.58 那条同一个理由)。
func (s *Server) handleCoachClear(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Confirmed bool `json:"confirmed"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	// ⚠️ 这是一次**批量停用** —— 要一个明确的确认位。
	// (它不像放行一个群那样局部: 一停就是**所有**规则 ✓。)
	if !body.Confirmed {
		writeJSON(w, map[string]interface{}{
			"error": "confirmed required", "code": "need_confirm",
			"hint": "这会停用全部正在生效的教练规则; 确认请在请求体里传 confirmed=true",
		})
		return
	}
	n, err := s.Store.ClearCoachRules(ownerKeyOf(s))
	if err != nil {
		writeJSON(w, map[string]interface{}{"error": "clear failed", "detail": err.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{"cleared": n})
}
