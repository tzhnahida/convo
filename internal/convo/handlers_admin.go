package convo

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tzhnahida/VocabLock/internal/clock"
	"github.com/tzhnahida/VocabLock/internal/log"
)

// ——— 管理 Web API (模块 9): 它记得的 / 表情包仓库 / 边界 / 日程 ———

// routeMemoryItem: GET(列表, 精确路径) / PUT(修正, /memories/{id}) / DELETE(删除)
func (s *Server) routeMemoryItem(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet:
		s.handleMemoriesList(w, r)
	case r.Method == http.MethodPut:
		s.handleMemoryEdit(w, r)
	case r.Method == http.MethodDelete:
		s.handleMemoryDelete(w, r)
	default:
		http.Error(w, `{"error":"method"}`, http.StatusMethodNotAllowed)
	}
}

// routeBoundaries: GET/PUT
func (s *Server) routeBoundaries(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleBoundariesGet(w, r)
	case http.MethodPut:
		s.handleBoundariesPut(w, r)
	default:
		http.Error(w, `{"error":"method"}`, http.StatusMethodNotAllowed)
	}
}

// GET /api/v1/convo/memories — 全部记忆 (active+pending), 可看可改可删=隐私出口
func (s *Server) handleMemoriesList(w http.ResponseWriter, r *http.Request) {
	// ⚠️ §7.3 **三个正交维度必须一起暴露** —— 它们是"这条记忆为什么没进来/为什么进来了"
	// 的**唯一**可查依据:
	//   · 敏感度(public/relation/private) 决定能不能跨人用;
	//   · 主体归属(scene_key/source)     决定对谁说;
	//   · 时间(slot_key/生效区间/被取代) 决定旧事实有没有被顶掉。
	//
	// 少任何一维, owner 看到的就是"这条记忆莫名其妙不见了"——
	// 而它其实是被场景闸门挡了、或者被同槽位的新事实取代了。
	rows, err := s.Store.DB.Query(`SELECT mem_id, k, v, label, kind, description, confidence, state,
		created_ts, updated_ts, access_count,
		COALESCE(source,''), COALESCE(sensitivity,''), COALESCE(scene_key,''),
		COALESCE(slot_key,''), COALESCE(valid_until,''), COALESCE(superseded_ts,''),
		COALESCE(inject_count,0), COALESCE(last_injected_ts,'')
		FROM convo_memory
		WHERE state IN ('active','pending') ORDER BY state DESC, confidence DESC`)
	if err != nil {
		http.Error(w, `{"error":"query"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := []map[string]interface{}{}
	for rows.Next() {
		var memID, k, v, label, kind, desc, state, created, updated string
		var source, sens, scene, slot, validUntil, superseded, lastInjected string
		var conf float64
		var acc, injectCount int
		if err := rows.Scan(&memID, &k, &v, &label, &kind, &desc, &conf, &state, &created, &updated, &acc,
			&source, &sens, &scene, &slot, &validUntil, &superseded, &injectCount, &lastInjected); err != nil {
			continue
		}
		out = append(out, map[string]interface{}{
			"mem_id": memID, "k": k, "v": v, "label": label, "kind": kind,
			"description": desc, "confidence": conf, "state": state,
			"created": created, "updated": updated, "access_count": acc,
			// §7.3 三维
			"source": source, "sensitivity": sens, "scene_key": scene,
			"slot_key": slot, "valid_until": validUntil, "superseded_ts": superseded,
			// 检索诱导遗忘 (它为什么"用过就不再出现")
			"inject_count": injectCount, "last_injected_ts": lastInjected,
		})
	}
	writeJSON(w, map[string]interface{}{"memories": out})
}

// DELETE /api/v1/convo/memories/{mem_id} — 删除一条 (用户不想被记的事)
func (s *Server) handleMemoryDelete(w http.ResponseWriter, r *http.Request) {
	memID := strings.TrimPrefix(r.URL.Path, "/api/v1/convo/memories/")
	if memID == "" {
		http.Error(w, `{"error":"bad"}`, http.StatusBadRequest)
		return
	}
	if _, err := s.Store.DB.Exec(`DELETE FROM convo_memory WHERE mem_id=?`, memID); err != nil {
		http.Error(w, `{"error":"delete"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true})
}

// PUT /api/v1/convo/memories/{mem_id} — 修正一条记忆内容
func (s *Server) handleMemoryEdit(w http.ResponseWriter, r *http.Request) {
	memID := strings.TrimPrefix(r.URL.Path, "/api/v1/convo/memories/")
	var body struct {
		V     string   `json:"v"`
		Label string   `json:"label"`
		Conf  *float64 `json:"confidence"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.V == "" {
		http.Error(w, `{"error":"bad"}`, http.StatusBadRequest)
		return
	}
	q := `UPDATE convo_memory SET v=?, updated_ts=?, state='active'`
	args := []interface{}{body.V, clock.TodayTS()}
	if body.Label != "" {
		q += `, label=?`
		args = append(args, body.Label)
	}
	if body.Conf != nil {
		q += `, confidence=?`
		args = append(args, *body.Conf)
	}
	q += ` WHERE mem_id=?`
	args = append(args, memID)
	if _, err := s.Store.DB.Exec(q, args...); err != nil {
		http.Error(w, `{"error":"update"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true})
}

// GET /api/v1/convo/stickers — 表情包仓库 (affinity 排序; 收藏/投喂/禁用)
func (s *Server) handleStickersList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.DB.Query(`SELECT id, local_path, lang, affinity, use_count, score, source, COALESCE(retired_ts,'')
		FROM stickers ORDER BY retired_ts IS NOT NULL, affinity DESC`)
	if err != nil {
		http.Error(w, `{"error":"query"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := []map[string]interface{}{}
	for rows.Next() {
		var id int64
		var path, lang, source, retired string
		var aff, score float64
		var use int
		if err := rows.Scan(&id, &path, &lang, &aff, &use, &score, &source, &retired); err != nil {
			continue
		}
		out = append(out, map[string]interface{}{
			"id": id, "path": path, "lang": lang, "affinity": aff, "use_count": use,
			"score": score, "source": source, "retired": retired != "",
		})
	}
	writeJSON(w, map[string]interface{}{"stickers": out})
}

// POST /api/v1/convo/stickers/feed — 投喂表情包 (multipart file) → 喜欢位 (affinity 0.7)
func (s *Server) handleStickerFeed(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		http.Error(w, `{"error":"multipart"}`, http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, `{"error":"no file"}`, http.StatusBadRequest)
		return
	}
	defer file.Close()
	dir := filepath.Join(s.DataDir, "convo", "stickers", "user")
	if err := os.MkdirAll(dir, 0755); err != nil {
		http.Error(w, `{"error":"mkdir"}`, http.StatusInternalServerError)
		return
	}
	name := time.Now().Format("20060102_150405") + strings.ToLower(filepath.Ext(header.Filename))
	path := filepath.Join(dir, name)
	out, err := os.Create(path)
	if err != nil {
		http.Error(w, `{"error":"save"}`, http.StatusInternalServerError)
		return
	}
	defer out.Close()
	if _, err := io.Copy(out, file); err != nil {
		http.Error(w, `{"error":"copy"}`, http.StatusInternalServerError)
		return
	}
	const updateQ = `INSERT INTO stickers(src_url, local_path, lang, affinity, score, source, born_ts)
		VALUES('user://', ?, '', 0.7, 1.0, 'feed', ?)`
	res, err := s.Store.DB.Exec(updateQ, path, clock.TodayTS())
	if err != nil {
		http.Error(w, `{"error":"insert"}`, http.StatusInternalServerError)
		return
	}
	id, _ := res.LastInsertId()
	log.Info("convo:sticker", "user feed #%d → %s", id, path)
	writeJSON(w, map[string]interface{}{"ok": true, "id": id})
}

// GET /api/v1/convo/stickers/file/{id} — 表情包图片代理 (浏览器 <img> 用)
func (s *Server) handleStickerFile(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/v1/convo/stickers/file/")
	var path string
	if err := s.Store.DB.QueryRow(`SELECT local_path FROM stickers WHERE id=?`, idStr).Scan(&path); err != nil || path == "" {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	clean := filepath.Clean(path)
	// 只开放 stickers 目录 (自搜下载 + 用户投喂两处)
	if !strings.HasPrefix(clean, filepath.Clean(filepath.Join(s.DataDir, "convo", "stickers"))) {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}
	w.Header().Set("Cache-Control", "max-age=3600")
	http.ServeFile(w, r, clean)
}

// DELETE /api/v1/convo/stickers/{id} — 禁用一个表情包 (直接退役)
func (s *Server) handleStickerDelete(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/convo/stickers/")
	if _, err := s.Store.DB.Exec(`UPDATE stickers SET retired_ts=? WHERE id=?`, clock.TodayTS(), id); err != nil {
		http.Error(w, `{"error":"update"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true})
}

// GET /api/v1/convo/boundaries — 边界开关 + 分享类别 + 黑名单
func (s *Server) handleBoundariesGet(w http.ResponseWriter, r *http.Request) {
	p, _ := s.Store.LoadProfile()
	if p == nil {
		writeJSON(w, map[string]interface{}{"boundaries": nil})
		return
	}
	writeJSON(w, map[string]interface{}{"boundaries": p.Boundaries})
}

// PUT /api/v1/convo/boundaries — 更新边界 (隐私开关)
func (s *Server) handleBoundariesPut(w http.ResponseWriter, r *http.Request) {
	var b Boundaries
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		http.Error(w, `{"error":"bad"}`, http.StatusBadRequest)
		return
	}
	p, _ := s.Store.LoadProfile()
	if p == nil {
		http.Error(w, `{"error":"no profile"}`, http.StatusBadRequest)
		return
	}
	p.Boundaries = b
	p.UpdatedTS = clock.TodayTS()
	if err := s.Store.SaveProfile(p); err != nil {
		http.Error(w, `{"error":"save"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true})
}

// GET /api/v1/convo/schedule — 周日程 (管理页可视化)
func (s *Server) handleScheduleGet(w http.ResponseWriter, r *http.Request) {
	ws, _ := s.Store.LoadSchedule()
	// §4.5.4-2 微调历史: 「它为什么改成这样」—— 日程开始被**程序**改写之后,
	// 没有这个 owner 就只能看到"睡点变了", 看不到是谁改的、为什么。
	revs, _ := s.Store.ScheduleRevisions(20)
	writeJSON(w, map[string]interface{}{"schedule": ws, "revisions": revs})
}
