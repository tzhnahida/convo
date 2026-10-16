package convo

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/tzhnahida/convo/internal/log"
)

// ——— 管理 Web API: 梗库 (模块 11) ———
//
// GET    /api/v1/convo/slang          — 全部词条 (管理页)
// POST   /api/v1/convo/slang          — 手动添加 (用户确认过 → active)
// PUT    /api/v1/convo/slang/{id}     — 编辑词条内容
// POST   /api/v1/convo/slang/{id}/state — 转状态 active/candidate/retired
// DELETE /api/v1/convo/slang/{id}     — 删除

func (s *Server) routeSlang(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/api/v1/convo/slang" {
		switch r.Method {
		case http.MethodGet:
			s.handleSlangList(w, r)
		case http.MethodPost:
			s.handleSlangAdd(w, r)
		default:
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		}
		return
	}
	rest := strings.TrimPrefix(path, "/api/v1/convo/slang/")
	parts := strings.Split(rest, "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, `{"error":"bad id"}`, http.StatusBadRequest)
		return
	}
	switch {
	case r.Method == http.MethodPut && len(parts) == 1:
		s.handleSlangUpdate(w, r, id)
	case r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "state":
		s.handleSlangState(w, r, id)
	case r.Method == http.MethodDelete && len(parts) == 1:
		if err := s.Store.DeleteSlang(id); err != nil {
			http.Error(w, `{"error":"delete failed"}`, http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true})
	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleSlangList(w http.ResponseWriter, r *http.Request) {
	slangs, err := s.Store.SlangAll()
	if err != nil {
		http.Error(w, `{"error":"list failed"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{"slangs": slangs})
}

func (s *Server) handleSlangAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Phrase  string `json:"phrase"`
		Meaning string `json:"meaning"`
		Example string `json:"example"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Phrase) == "" {
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
		return
	}
	id, err := s.Store.UpsertSlang(SlangDelta{
		Phrase: strings.TrimSpace(req.Phrase), Meaning: req.Meaning, Example: req.Example,
		Confidence: 0.9,
	}, "user") // 手动添加 = 用户确认过 → active 高置信
	if err != nil {
		http.Error(w, `{"error":"add failed"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "id": id})
}

func (s *Server) handleSlangUpdate(w http.ResponseWriter, r *http.Request, id int64) {
	var req struct {
		Phrase  string `json:"phrase"`
		Meaning string `json:"meaning"`
		Example string `json:"example"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
		return
	}
	if err := s.Store.UpdateSlang(id, req.Phrase, req.Meaning, req.Example); err != nil {
		http.Error(w, `{"error":"update failed"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true})
}

func (s *Server) handleSlangState(w http.ResponseWriter, r *http.Request, id int64) {
	var req struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
		return
	}
	if req.State != SlangActive && req.State != SlangCandidate && req.State != SlangRetired {
		http.Error(w, `{"error":"state must be active|candidate|retired"}`, http.StatusBadRequest)
		return
	}
	if err := s.Store.SetSlangState(id, req.State); err != nil {
		http.Error(w, `{"error":"state failed"}`, http.StatusInternalServerError)
		return
	}
	log.Info("convo:slang", "admin: slang #%d → %s", id, req.State)
	writeJSON(w, map[string]interface{}{"ok": true})
}
