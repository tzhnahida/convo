package convo

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tzhnahida/VocabLock/internal/clock"
	"github.com/tzhnahida/VocabLock/internal/log"
)

//go:embed admin_dist
var adminFS embed.FS

// Server is the convo HTTP endpoint: management Web + /api/v1/convo/*.
// Unlike the main app (random port + random token for a local Flutter UI),
// the convo server needs a STABLE endpoint on the Pi (PC browser bookmarks
// it) — so port comes from config and the token is persisted across restarts.
type Server struct {
	http          *http.Server
	Port          int
	Token         string
	Store         *Store
	Sched         *Scheduler
	Chatter       Chatter // AI 调用 (nil = 未配置, 预览/对话接口 503)
	DataDir       string
	LangCode      string // 目标语言 (表情包/角色搜索), 默认 ja
	FirecrawlBase string
	AdminDisk     string // dev override: serve admin UI from this directory instead of embed
}

// LoadOrCreateToken returns the persisted token (data/convo-token.txt) or
// generates + persists one.
func LoadOrCreateToken(dataDir string, cfgToken string) (string, error) {
	if cfgToken != "" {
		return cfgToken, nil
	}
	path := filepath.Join(dataDir, "convo-token.txt")
	if b, err := os.ReadFile(path); err == nil {
		if t := strings.TrimSpace(string(b)); t != "" {
			return t, nil
		}
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(token+"\n"), 0600); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Server) Start(listen string) error {
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("bind %s: %w", listen, err)
	}
	addr := listener.Addr().(*net.TCPAddr)
	s.Port = addr.Port

	s.http = &http.Server{
		Handler:      withConvoMiddleware(mux, s.Token),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 300 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	go func() {
		if err := s.http.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Error("convo:http", "serve: %v", err)
		}
	}()
	return nil
}

func (s *Server) Shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.http.Shutdown(ctx)
}

func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"ts":%q}`, clock.TodayTS())
	})
	mux.HandleFunc("/api/v1/convo/status", s.handleStatus)
	mux.HandleFunc("/api/v1/convo/inbox", s.handleInbox)
	mux.HandleFunc("/api/v1/convo/profile", s.handleProfileGet)
	mux.HandleFunc("/api/v1/convo/profile/preview", s.handleProfilePreview)
	mux.HandleFunc("/api/v1/convo/profile/confirm", s.handleProfileConfirm)
	mux.HandleFunc("/api/v1/convo/profile/roles", s.handleRolesSearch)
	// §2.3 世界观: GET 看 / POST 重生 / DELETE 清空。
	mux.HandleFunc("/api/v1/convo/world", s.handleWorld)
	mux.HandleFunc("/api/v1/convo/memories", s.routeMemoryItem)
	mux.HandleFunc("/api/v1/convo/memories/", s.routeMemoryItem)
	mux.HandleFunc("/api/v1/convo/stickers", s.handleStickersList)
	mux.HandleFunc("/api/v1/convo/stickers/feed", s.handleStickerFeed)
	mux.HandleFunc("/api/v1/convo/stickers/file/", s.handleStickerFile)
	mux.HandleFunc("/api/v1/convo/stickers/", s.handleStickerDelete)
	mux.HandleFunc("/api/v1/convo/boundaries", s.routeBoundaries)
	// §4.5 周日程: GET 看 / POST 重生成 (校验漏洞修好后, 存量坏表需要一个出口)。
	mux.HandleFunc("/api/v1/convo/schedule", s.handleSchedule)
	// §4.5 层 2 行程: GET 看今明两天 / POST 重排明天 (它只在夜里跑一次, 白天没有补救手段)。
	mux.HandleFunc("/api/v1/convo/dayplan", s.handleDayPlan)
	// §5.7 owner 的手动关系倍率 (设计稿 L1301:「管理页给任一 uin 设」)。
	mux.HandleFunc("/api/v1/convo/owner_mult", s.routeOwnerMult)
	// §6.x 插话收敛的安全阀: GET 看哪些群被抱怨过 / POST 放行一个群。
	mux.HandleFunc("/api/v1/convo/chime", s.routeChime)
	// 教练机的出口: GET 看生效中的规则 / POST 全部停用 (owner 想重新开始教)。
	mux.HandleFunc("/api/v1/convo/coach", s.routeCoach)
	// §6.10.4 称呼的 owner 入口: GET 看成员 / POST 设一个人的称呼。
	mux.HandleFunc("/api/v1/convo/member", s.routeMember)
	// §12 热力流水: 机制有没有生效, 要看数字。
	mux.HandleFunc("/api/v1/convo/heat", s.routeHeat)
	mux.HandleFunc("/api/v1/convo/slang", s.routeSlang)
	// §6.8 偏向可见 (「一个看不见偏向的群友, 比一个没有偏向的群友更危险」)
	mux.HandleFunc("/api/v1/convo/bias", s.handleBiasGet)
	mux.HandleFunc("/api/v1/convo/observation", s.handleObservation)
	mux.HandleFunc("/api/v1/convo/sparks", s.handleSparksList)
	mux.HandleFunc("/api/v1/convo/learned", s.handleLearnedList)
	mux.HandleFunc("/api/v1/convo/night-peers", s.routeNightPeers)
	// §7.4 遗忘权 (物理删除 + 立碑)
	mux.HandleFunc("/api/v1/convo/forget", s.handleForget)
	// 看板 (零构建, Go 直接吐) + 决策轨迹
	mux.HandleFunc("/dashboard", s.handleDashboard)
	mux.HandleFunc("/api/v1/convo/traces", s.handleTraces)
	mux.HandleFunc("/api/v1/convo/feedback-boundaries", s.routeFeedbackBoundaries)
	mux.HandleFunc("/api/v1/convo/slang/", s.routeSlang)

	// Admin Web (SPA). Dev override: -admin-dir serves from disk for hot reload.
	var staticFS http.FileSystem
	if s.AdminDisk != "" {
		if st, err := os.Stat(s.AdminDisk); err == nil && st.IsDir() {
			staticFS = http.Dir(s.AdminDisk)
		}
	}
	if staticFS == nil {
		sub, err := fs.Sub(adminFS, "admin_dist")
		if err != nil {
			log.Error("convo:http", "embed sub: %v", err)
			return
		}
		staticFS = http.FS(sub)
	}
	mux.Handle("/", http.FileServer(staticFS))
}

// handleStatus: 状态台 — 当前状态 + 日程 + 最近消息数(管理 Web 首页数据源)
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	now := clock.Now()
	ws, _ := s.Store.LoadSchedule()
	state := CurrentState(ws, now)
	msgs, _ := s.Store.RecentMessages(20)
	profile, _ := s.Store.LoadProfile()
	// 观察台计数 (今日)
	counters := map[string]int{}
	for _, name := range []string{"ai_opens", "rounds", "ask_share_accept", "ask_share_reject", "user_msgs"} {
		n, _ := s.Store.CounterGet(name, clock.Today())
		counters[name] = n
	}
	writeJSON(w, map[string]interface{}{
		"state":       state,
		"now":         now.Format("2006-01-02 15:04:05"),
		"has_profile": s.hasProfile(),
		"profile":     profile,
		"schedule":    ws,
		"recent":      msgs,
		"counters":    counters,
	})
}

func (s *Server) hasProfile() bool {
	var n int
	_ = s.Store.DB.QueryRow(`SELECT COUNT(*) FROM convo_profile`).Scan(&n)
	return n > 0
}

// handleInbox: 待读消息 (轮询 — 管理 Web 观察台)
func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request) {
	msgs, _ := s.Store.RecentMessages(60)
	writeJSON(w, map[string]interface{}{"has_new": len(msgs) > 0, "msgs": msgs})
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ——— middleware (copy of main app's pattern, adapted) ———

type convoResponseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *convoResponseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

func withConvoMiddleware(next http.Handler, token string) http.Handler {
	var h http.Handler = next
	h = convoAuthMiddleware(h, token)
	h = convoRecoveryMiddleware(h)
	return h
}

func convoRecoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				log.Error("convo:http", "panic: %v", err)
				http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// convoAuthMiddleware: /health + static SPA shell 免认证; API 需 Bearer token
// 或 ?token= (浏览器书签带 token, SPA 存 localStorage 后走 Header)。
func convoAuthMiddleware(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/health" || p == "/" || p == "/index.html" || p == "/favicon.ico" || strings.HasPrefix(p, "/assets/") {
			next.ServeHTTP(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		if strings.HasPrefix(auth, "Bearer ") && strings.TrimPrefix(auth, "Bearer ") == token {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Query().Get("token") == token {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	})
}
