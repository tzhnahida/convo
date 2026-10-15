package convo

import (
	"encoding/json"
	"net/http"
	"strings"
)

// routeMember GET = 看某个场景的成员与称呼, POST = 设一个人的称呼。
//
// 设计稿 L1298:
//
//	称呼与禁区 | 按场景: 在 A 群叫外号, 在 B 群叫全名, 两边不许串
//
// 而这一条链在 2026-10-16 之前是断的, 而且是两处都断:
//
//   - address_as 那一列被 Scan 进结构体就丢掉了 (见 §2.81 接上的读者);
//   - SetMemberAddress (写者) 一个生产调用者都没有 —— 也就是没入口。
//
// 这个文件补的是后半: 一个真的能设它的地方。
// 形状与 handlers_coach.go 一样 —— 它是"写着 owner 手动"的那类出口。
func (s *Server) routeMember(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleMemberGet(w, r)
	case http.MethodPost:
		s.handleMemberSet(w, r)
	default:
		writeJSON(w, map[string]interface{}{"error": "method"})
	}
}

// handleMemberGet 列一个场景的成员。scene 查询参数必填。
//
// 带上 address_as 与 display_name 两个 ——
// "它平时怎么叫他"与"他群里叫什么"是两件事, 而设称呼时要看着后者决定。
func (s *Server) handleMemberGet(w http.ResponseWriter, r *http.Request) {
	scene := strings.TrimSpace(r.URL.Query().Get("scene"))
	if scene == "" {
		writeJSON(w, map[string]interface{}{
			"error": "scene required",
			"code":  "need_scene",
			"hint":  "例如 ?scene=group:1034712035",
		})
		return
	}
	rows, err := s.Store.DB.Query(
		`SELECT uin, COALESCE(display_name,''), COALESCE(address_as,''), COALESCE(stage,'') `+
			`FROM convo_scene_member WHERE scene_key=? ORDER BY uin`, scene)
	if err != nil {
		writeJSON(w, map[string]interface{}{"error": "query"})
		return
	}
	defer rows.Close()
	out := []map[string]interface{}{}
	for rows.Next() {
		var uin, name, addr, stage string
		if err := rows.Scan(&uin, &name, &addr, &stage); err != nil {
			continue
		}
		out = append(out, map[string]interface{}{
			"uin":          uin,
			"display_name": name,
			"address_as":   addr,
			"stage":        stage,
		})
	}
	writeJSON(w, map[string]interface{}{"scene": scene, "members": out})
}

// handleMemberSet 设一个称呼。
//
// 请求体: {scene, uin, address}
//
// 空 address 是"清掉", 不是错误 —— 与 SetMemberAddress 的语义一致。
// 而没有确认位: 它不像"停用全部教练规则"那样是批量的, 一次只动一个人、一个场景。
func (s *Server) handleMemberSet(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Scene   string `json:"scene"`
		UIN     string `json:"uin"`
		Address string `json:"address"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if strings.TrimSpace(body.Scene) == "" || strings.TrimSpace(body.UIN) == "" {
		writeJSON(w, map[string]interface{}{
			"error": "scene and uin required",
			"code":  "need_scene_and_uin",
		})
		return
	}
	if err := s.Store.SetMemberAddress(body.Scene, body.UIN, body.Address); err != nil {
		// 最常见的那种失败是"这个成员还没登记" ——
		// 而 SetMemberAddress 报错而不是静默不写 (它的错误信息自己说了怎么修)。
		writeJSON(w, map[string]interface{}{
			"error":  "set failed",
			"detail": err.Error(),
		})
		return
	}
	writeJSON(w, map[string]interface{}{
		"ok":         true,
		"scene":      body.Scene,
		"uin":        body.UIN,
		"address_as": body.Address,
	})
}
