package convo

import (
	"io"
	"net/http"
	"strings"
)

// ─────────────────────────────────────────────────────────────
// 看板: 把这套系统的**全部数据**摊在一页上。
//
// ⚠️ 为什么是 Go 直接吐 HTML, 而不是改 admin/ 那套 Svelte:
// 那条链路要 npm + vite build 才能验证, 而"改了但没构建"和"没改"长得一模一样。
// 这一页零构建、零依赖, `go build` 之后就能看 —— 观测手段本身不该有构建风险。
// (admin/ 那套还在, 走 /assets/ + /, 这条只是新增。);
//
// ⚠️ 它**不含任何 token**: 页面从 URL 读 ?token= 再带在 fetch 上,
// 与既有 SPA 的做法一致 (convoAuthMiddleware 认 ?token=)。
// ─────────────────────────────────────────────────────────────

// handleDashboard 看板页。
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, dashboardHTML)
}

// handleTraces 决策轨迹 (「上下文浏览器」的数据源)。
//
// ⚠️ system_prompt **整段回传**并在前端切段位 —— 只回 hash 或只回长度,
// "它当时到底看到了什么"这个问题就永远答不上, 而那正是这张表的唯一理由。
func (s *Server) handleTraces(w http.ResponseWriter, r *http.Request) {
	limit, offset := pageParams(r, 30, 200)
	// q: 关键字搜索。
	//
	// ⚠️ 这不是"顺手加的筛选"。轨迹表是**只增**的, 几千条之后"翻页找那次判错"
	// 就不再可行 —— 而看不到它当时看到什么, 这张表就没有存在理由。
	// ⚠️ 没查到必须是 **0 条**, 不能退化成"搜索失败 → 全量返回":
	// 那会让"搜不到"看起来像"到处都是"。
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	where, qargs := "", []interface{}{}
	if q != "" {
		like := "%" + q + "%"
		where = " WHERE (COALESCE(reason,'') LIKE ? OR COALESCE(user_text,'') LIKE ?" +
			" OR COALESCE(raw_output,'') LIKE ? OR COALESCE(scene_key,'') LIKE ?" +
			" OR COALESCE(speaker_uin,'') LIKE ? OR COALESCE(action_kind,'') LIKE ?" +
			" OR COALESCE(stance,'') LIKE ?)"
		qargs = append(qargs, like, like, like, like, like, like, like)
	}
	var total int
	_ = s.Store.DB.QueryRow("SELECT COUNT(*) FROM convo_decision_trace"+where, qargs...).Scan(&total)
	args := append(append([]interface{}{}, qargs...), limit, offset)
	rows, err := s.Store.DB.Query("SELECT id, ts, COALESCE(action_kind,''), COALESCE(stance,''), "+
		"COALESCE(reason,''), COALESCE(speaker_uin,''), COALESCE(scene_key,''), "+
		"COALESCE(prompt_bytes,0), COALESCE(outcome,''), COALESCE(user_text,''), "+
		"COALESCE(raw_output,''), COALESCE(system_prompt,'') "+
		"FROM convo_decision_trace"+where+" ORDER BY id DESC LIMIT ? OFFSET ?", args...)
	if err != nil {
		http.Error(w, `{"error":"query"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := []map[string]interface{}{}
	for rows.Next() {
		var id int64
		var ts, kind, stance, reasonQ, uin, scene, outcome, userText, raw, prompt string
		var bytes int
		if err := rows.Scan(&id, &ts, &kind, &stance, &reasonQ, &uin, &scene, &bytes, &outcome, &userText, &raw, &prompt); err != nil {
			continue
		}
		segs := SegmentPrompt(prompt)
		segOut := make([]map[string]interface{}, 0, len(segs))
		for _, sg := range segs {
			segOut = append(segOut, map[string]interface{}{"name": sg.Name, "start": sg.Start, "end": sg.End})
		}
		out = append(out, map[string]interface{}{
			"id": id, "ts": ts, "action_kind": kind, "stance": stance, "reason": reasonQ,
			"speaker_uin": uin, "scene_key": scene, "prompt_bytes": bytes, "outcome": outcome,
			"user_text": truncRunes(userText, 200), "raw_output": truncRunes(raw, 600),
			"prompt": truncRunes(prompt, 6000), "segments": segOut,
		})
	}
	writeJSON(w, map[string]interface{}{"traces": out, "total": total, "limit": limit, "offset": offset, "q": q})
}
