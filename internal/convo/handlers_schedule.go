package convo

import (
	"net/http"
	"time"

	"github.com/tzhnahida/VocabLock/internal/log"
)

// ——— §4.5 周日程: 看 / 重生成 ———
//
// ⚠️ 为什么要开一个"重生成"的口:
//
// 校验的漏洞(2026-10-15 修)会让一份**坏的周表**顺利落库并长期使用 ——
// 生产库里那份"周三整日没有 sleep 槽"的日程就是这么来的。
// 而**漏洞修好之后, 坏表仍然躺在库里**: 校验只管生成路径, 不管存量。
// 没有这个口, 唯一能修它的办法是重建人格 —— 拿大炮打蚊子, 而且会顺手丢掉别的东西。
//
// ⚠️ 与世界观重生成(handlers_world.go)刻意同构: 看 / 重生 / 校验不过不落库。

// handleScheduleRegen 用当前人格重新生成周日程并落库。
func (s *Server) handleScheduleRegen(w http.ResponseWriter, r *http.Request) {
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
	ws, gerr := GenerateSchedule(ctx, s.Chatter, p)
	if gerr != nil {
		log.Warn("convo:schedule", "重生成失败: %v", gerr)
		writeJSON(w, map[string]interface{}{"ok": false, "error": gerr.Error()})
		return
	}
	// ⚠️ 落库**之前**再校验一次: GenerateSchedule 走内置兜底那条路时不经过上面的校验,
	// 而"兜底的也必须是好的"这件事不能靠假设 —— 这正是一次事故的教训(半个世界观比没有更糟)。
	if verr := ValidateWeekSchedule(ws); verr != nil {
		log.Error("convo:schedule", "重生成的日程本身不合法 (拒绝落库): %v", verr)
		writeJSON(w, map[string]interface{}{"ok": false, "error": "生成的日程不合规: " + verr.Error()})
		return
	}
	if serr := s.Store.SaveSchedule(ws); serr != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "save: " + serr.Error()})
		return
	}
	log.Info("convo:schedule", "周日程重生成: 睡点 %s / 起 %s", ws.SleepStart, ws.SleepEnd)
	writeJSON(w, map[string]interface{}{"ok": true, "schedule": ws})
}

// handleSchedule 路由: GET 看 / POST 重生成。
func (s *Server) handleSchedule(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleScheduleGet(w, r)
	case http.MethodPost:
		s.handleScheduleRegen(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}
