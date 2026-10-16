package convo

import (
	"net/http"
	"time"

	"github.com/tzhnahida/convo/internal/log"
)

// ——— §2.3 世界观的读写口 ———
//
// ⚠️ 这三个接口补的是一个**看不见的东西**:
//
//   世界观是它「一戳就破」的那个东西, 但在此之前**只能进库看**。
//   owner 看不到它写了什么, 也就无法判断它是不是在胡说;
//   而换角色之后旧世界观还挂在库里 (须弥 vs 明日方舟), 没有入口重生成 ——
//   结果是新角色顶着一份别人的来处说话, 那比没有更糟。
//
// 三个动词各司其职: GET 看, POST 重生, DELETE 清空 (留着让夜间路径自己重生)。

// handleWorldGet 看当前世界观。没有 → 200 + ok:false (不是 404: 这不是错误)。
func (s *Server) handleWorldGet(w http.ResponseWriter, r *http.Request) {
	b, ok, err := s.Store.LoadWorldBible(ownerKeyOf(s))
	if err != nil {
		http.Error(w, `{"error":"load world"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{"ok": ok, "world": b, "prompt": WorldPrompt(b)})
}

// handleWorldRegen 用当前人格**重新生成**一份世界观并落库。
//
// ⚠️ 校验不过**不落库** (与 GenerateWorldBible 同一条纪律): 半个世界观比没有更糟。
func (s *Server) handleWorldRegen(w http.ResponseWriter, r *http.Request) {
	if s.Chatter == nil {
		writeJSON(w, map[string]interface{}{"error": "ai not configured", "code": "ai_unconfigured"})
		return
	}
	p, err := s.Store.LoadProfile()
	if err != nil || p == nil {
		http.Error(w, `{"error":"load profile"}`, http.StatusInternalServerError)
		return
	}
	ctx, cancel := contextWithTimeout(r, 180*time.Second)
	defer cancel()
	b, gerr := GenerateWorldBible(ctx, s.Chatter, p)
	if gerr != nil {
		log.Warn("convo:world", "重生成失败: %v", gerr)
		writeJSON(w, map[string]interface{}{"ok": false, "error": gerr.Error()})
		return
	}
	if serr := s.Store.SaveWorldBible(ownerKeyOf(s), b); serr != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "save: " + serr.Error()})
		return
	}
	log.Info("convo:world", "世界观重生成: 定稿配角 %d 人 / 动态配角 %d 人", len(b.Fixed()), len(b.Dynamic()))
	writeJSON(w, map[string]interface{}{"ok": true, "world": b, "prompt": WorldPrompt(b)})
}

// handleWorldClear 清空世界观 —— 下一次夜间路径会自己重生一份。
func (s *Server) handleWorldClear(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Store.DB.Exec("DELETE FROM convo_world_bible WHERE owner = ?", ownerKeyOf(s)); err != nil {
		http.Error(w, `{"error":"clear world"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true})
}

// handleWorld 路由: GET 看 / POST 重生 / DELETE 清空。
func (s *Server) handleWorld(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleWorldGet(w, r)
	case http.MethodPost:
		s.handleWorldRegen(w, r)
	case http.MethodDelete:
		s.handleWorldClear(w, r)
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}
