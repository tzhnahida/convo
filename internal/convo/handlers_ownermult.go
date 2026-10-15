package convo

import (
	"encoding/json"
	"net/http"
	"strings"
)

// ——— §5.7 owner 的手动关系倍率: 看 / 设 ———
//
// ⚠️ 为什么必须有一个能设的口子: 设计稿把它写成「管理页」的一条,
// 而它是一条**真实的杠杆** —— 私聊的关系量从 0 出发,
// 而"不向陌生人掏心窝子"把热力 cap 压在 20, 买不起一条 25 的主动消息。
// 没有这个口子, owner 只能**等**它自然涨上去。

// routeOwnerMult GET = 看, POST = 设。
func (s *Server) routeOwnerMult(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleOwnerMultGet(w, r)
	case http.MethodPost:
		s.handleOwnerMultSet(w, r)
	default:
		writeJSON(w, map[string]interface{}{"error": "method"})
	}
}

// handleOwnerMultGet 列出设过的倍率。**没设过的不列** —— 列一个 1.0 只会让人以为设过。
func (s *Server) handleOwnerMultGet(w http.ResponseWriter, r *http.Request) {
	owner := ownerKeyOf(s)
	q := `SELECT uin, mult, COALESCE(updated_ts,'') FROM convo_owner_mult WHERE owner=? ORDER BY mult DESC, uin`
	rows, err := s.Store.DB.Query(q, owner)
	if err != nil {
		writeJSON(w, map[string]interface{}{"error": "query"})
		return
	}
	defer rows.Close()
	out := []map[string]interface{}{}
	for rows.Next() {
		var uin, ts string
		var m float64
		if err := rows.Scan(&uin, &m, &ts); err != nil {
			continue
		}
		out = append(out, map[string]interface{}{"uin": uin, "mult": m, "updated": ts})
	}
	writeJSON(w, map[string]interface{}{
		"mults": out,
		// ⚠️ 把上限一起给出去 —— 页面上写死一个 5 会与代码里的常量分叉。
		"cap":     OwnerMultCap,
		"default": OwnerMultDefault,
	})
}

// handleOwnerMultSet 设一个 uin 的倍率。
//
// ⚠️ **回显 clamp 之后的那个数** —— 回显请求值会让"我设了 8×"与
// "实际按 5× 算"看起来一致, 而它们不是。
func (s *Server) handleOwnerMultSet(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UIN  string  `json:"uin"`
		Mult float64 `json:"mult"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, map[string]interface{}{"error": "bad json"})
		return
	}
	if strings.TrimSpace(body.UIN) == "" {
		writeJSON(w, map[string]interface{}{"error": "uin required", "code": "uin_required"})
		return
	}
	got, err := s.Store.SetOwnerMult(ownerKeyOf(s), body.UIN, body.Mult)
	if err != nil {
		writeJSON(w, map[string]interface{}{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{
		"uin": body.UIN,
		// 回显**生效值**
		"mult":      got,
		"requested": body.Mult,
		"clamped":   got != body.Mult,
		"cap":       OwnerMultCap,
	})
}
