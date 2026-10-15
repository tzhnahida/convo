package api

import (
	"net/http"
	"strconv"

	"github.com/tzhnahida/VocabLock/internal/api/mock"
	"github.com/tzhnahida/VocabLock/internal/db"
	"github.com/tzhnahida/VocabLock/internal/log"
)

// handleGrammarPointsList returns grammar points for the active language.
func (s *Server) handleGrammarPointsList(w http.ResponseWriter, r *http.Request) {
	if s.Mock {
		mock.HandleGrammarPointsList(w, r, s.Config.App.ActiveLang)
		return
	}
	if s.DB == nil {
		writeError(w, 500, "database not available")
		return
	}
	lang := r.URL.Query().Get("language")
	if lang == "" {
		lang = s.Config.App.ActiveLang
	}
	log.Debug("grammar", "list: lang=%s", lang)

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	points, total, err := s.DB.ListGrammarPoints(lang, limit, offset)
	if err != nil {
		writeError(w, 500, "list grammar points: "+err.Error())
		return
	}
	if points == nil {
		points = []*db.GrammarPoint{}
	}
	writeJSON(w, map[string]interface{}{
		"grammar_points": points,
		"total":          total,
	})
}

// handleGrammarPointCreate creates a new grammar point.
func (s *Server) handleGrammarPointCreate(w http.ResponseWriter, r *http.Request) {
	if s.DB == nil {
		writeError(w, 500, "database not available")
		return
	}
	var gp db.GrammarPoint
	if err := readJSON(r, &gp); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	if gp.Language == "" {
		gp.Language = s.Config.App.ActiveLang
	}
	log.Debug("grammar", "create: title=%s lang=%s", gp.Title, gp.Language)
	if exists, _ := s.DB.GrammarPointExists(gp.Language, gp.Title); exists {
		writeError(w, 409, "grammar point already exists")
		return
	}
	id, err := s.DB.InsertGrammarPoint(&gp)
	if err != nil {
		writeError(w, 500, "create grammar point: "+err.Error())
		return
	}
	gp.ID = id
	writeJSON(w, gp)
}

// handleGrammarPointUpdate updates an existing grammar point.
// Only user-editable fields are applied; SM-2 state is preserved.
func (s *Server) handleGrammarPointUpdate(w http.ResponseWriter, r *http.Request) {
	if s.DB == nil {
		writeError(w, 500, "database not available")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, 400, "invalid id")
		return
	}

	log.Debug("grammar", "update: id=%d", id)

	existing, err := s.DB.GetGrammarPoint(id)
	if err != nil {
		writeError(w, 404, "grammar point not found")
		return
	}

	var req struct {
		Title       *string `json:"title"`
		Explanation *string `json:"explanation"`
		Example     *string `json:"example"`
		Detail      *string `json:"detail"`
		Language    *string `json:"language"`
		Source      *string `json:"source"`
		Status      *string `json:"status"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}

	if req.Title != nil {
		existing.Title = *req.Title
	}
	if req.Explanation != nil {
		existing.Explanation = *req.Explanation
	}
	if req.Example != nil {
		existing.Example = *req.Example
	}
	if req.Detail != nil {
		existing.Detail = *req.Detail
	}
	if req.Language != nil {
		existing.Language = *req.Language
	}
	if req.Source != nil {
		existing.Source = *req.Source
	}
	if req.Status != nil {
		existing.Status = *req.Status
	}

	if err := s.DB.UpdateGrammarPoint(existing); err != nil {
		writeError(w, 500, "update grammar point: "+err.Error())
		return
	}
	writeJSON(w, map[string]interface{}{"updated": true, "id": id})
}

// handleGrammarPointDelete deletes a grammar point.
func (s *Server) handleGrammarPointDelete(w http.ResponseWriter, r *http.Request) {
	if s.DB == nil {
		writeError(w, 500, "database not available")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, 400, "invalid id")
		return
	}
	log.Debug("grammar", "delete: id=%d", id)
	if err := s.DB.DeleteGrammarPoint(id); err != nil {
		writeError(w, 500, "delete grammar point: "+err.Error())
		return
	}
	writeJSON(w, map[string]interface{}{"deleted": true, "id": id})
}
