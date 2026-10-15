package convo

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/tzhnahida/VocabLock/internal/log"
)

// ——— 管理 Web API: 创建朋友向导 (§8.2) ———

// handleRolesSearch 双轨 A 路线: 用户提交名(可选) → LLM 列名/类似名 → 搜索抓取提炼 → 候选卡 (失败→内置包)。
func (s *Server) handleRolesSearch(w http.ResponseWriter, r *http.Request) {
	if s.Chatter == nil {
		writeJSON(w, map[string]interface{}{"error": "ai not configured", "code": "ai_unconfigured"})
		return
	}
	var req struct {
		Name string `json:"name"` // 用户提交的角色名/关键词 (可空)
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	ctx, cancel := contextWithTimeout(r, 180*time.Second)
	defer cancel()
	names, err := RoleSearchPlan(ctx, s.Chatter, req.Name)
	if err != nil {
		log.Warn("convo:role", "names: %v", err)
		names = []string{"初音ミク", "千反田愛瑠", "后藤ひとり"}
	}
	roles, err := SearchRoles(ctx, s.Chatter, s.webSearch(), names)
	mode := "search"
	if err != nil || len(roles) == 0 {
		log.Warn("convo:role", "search chain failed — builtin pack: %v", err)
		roles = BuiltinRolePack()
		mode = "builtin"
	}
	writeJSON(w, map[string]interface{}{"roles": roles, "mode": mode})
}

// handleProfilePreview 三问+模板(+可选角色卡) → 规则打分 → LLM①日程 + LLM②开场白 → 预览。
// 不落库 — 用户确认后才 PUT。
func (s *Server) handleProfilePreview(w http.ResponseWriter, r *http.Request) {
	if s.Chatter == nil {
		writeJSON(w, map[string]interface{}{"error": "ai not configured", "code": "ai_unconfigured"})
		return
	}
	var body struct {
		Answers ProfileAnswers `json:"answers"`
		Role    *RoleCard      `json:"role"` // nil = 原创模式
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
		return
	}
	ctx, cancel := contextWithTimeout(r, 240*time.Second)
	defer cancel()

	var profile *PersonaProfile
	if body.Role != nil {
		profile = BuildRoleProfile(&body.Answers, *body.Role)
	} else {
		profile = BuildProfile(&body.Answers)
	}
	ws, err := GenerateSchedule(ctx, s.Chatter, profile)
	if err != nil {
		http.Error(w, `{"error":"schedule generation failed"}`, http.StatusInternalServerError)
		return
	}
	demo, err := GenerateDemo(ctx, s.Chatter, profile)
	if err != nil {
		http.Error(w, `{"error":"demo generation failed"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{
		"profile":  profile,
		"schedule": ws,
		"demo":     demo,
	})
}

// webSearch: 萌娘百科 MediaWiki API (零认证直连, 2026-09-06 实测可用)。
func (s *Server) webSearch() WebSearcher {
	return &MoegirlWeb{Base: "https://zh.moegirl.org.cn", HTTP: &http.Client{Timeout: 30 * time.Second}}
}

func (s *Server) Lang() string {
	if s.LangCode != "" {
		return s.LangCode
	}
	return "ja"
}

// handleProfileConfirm 用户确认预览 → 落库 profile+schedule + 种子记忆 + 落盘备份。
func (s *Server) handleProfileConfirm(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Profile  *PersonaProfile `json:"profile"`
		Schedule *WeekSchedule   `json:"schedule"`
		Demo     *OutputFrame    `json:"demo"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Profile == nil || body.Schedule == nil {
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
		return
	}
	if err := ValidateWeekSchedule(body.Schedule); err != nil {
		http.Error(w, `{"error":"schedule invalid: `+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	// demo.first_mes 落入 profile (Character Card 兼容)
	if body.Demo != nil && len(body.Demo.Reply) > 0 && body.Demo.Reply[0].Text != "" {
		body.Profile.FirstMes = body.Demo.Reply[0].Text
	}
	if err := s.Store.SaveProfile(body.Profile); err != nil {
		http.Error(w, `{"error":"save profile"}`, http.StatusInternalServerError)
		return
	}
	if err := s.Store.SaveSchedule(body.Schedule); err != nil {
		http.Error(w, `{"error":"save schedule"}`, http.StatusInternalServerError)
		return
	}
	if err := SeedMemories(r.Context(), s.Store, body.Profile); err != nil {
		log.Warn("convo:profile", "seed memory: %v", err)
	}
	// 落盘备份 (试跑人格不随 DB 作废 — §6.3 模块 2)
	s.backupProfile(body.Profile, body.Schedule)
	writeJSON(w, map[string]interface{}{"ok": true})
}

func (s *Server) handleProfileGet(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.LoadProfile()
	if err != nil {
		http.Error(w, `{"error":"load profile"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{"profile": p})
}

func (s *Server) backupProfile(p *PersonaProfile, ws *WeekSchedule) {
	dir := filepath.Join(s.DataDir, "convo")
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Warn("convo:profile", "backup mkdir: %v", err)
		return
	}
	pb, _ := json.MarshalIndent(p, "", "  ")
	sb, _ := json.MarshalIndent(ws, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, "profile-backup.json"), pb, 0644)
	_ = os.WriteFile(filepath.Join(dir, "schedule-backup.json"), sb, 0644)
}

func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}
