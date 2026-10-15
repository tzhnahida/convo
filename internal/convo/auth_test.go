package convo

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// 鉴权豁免名单是**封闭的显式五条** (/health, /, /index.html, /favicon.ico, /assets/ 前缀)。
// 所以"某条路由有没有挂上鉴权"没有可写错的接缝 —— 但有两件事是**真能坏**的:
//
//	① 包装器被摘掉 (重构 s.http.Handler = ... 时漏掉 withConvoMiddleware);
//	② 有人往豁免名单里加了一条 API 前缀。
//
// 这两条各自都能被下面钉住。
func TestAuth_APIRoutesRequireToken(t *testing.T) {
	mux := http.NewServeMux()
	s := adminServer(t)
	// 用本轮新增的观测接口当代表 —— 它们全是 API 路由。
	mux.HandleFunc("/api/v1/convo/bias", s.handleBiasGet)
	mux.HandleFunc("/api/v1/convo/observation", s.handleObservation)
	mux.HandleFunc("/api/v1/convo/night-peers", s.routeNightPeers)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := withConvoMiddleware(mux, "secret")

	for _, path := range []string{"/api/v1/convo/bias", "/api/v1/convo/observation", "/api/v1/convo/night-peers"} {
		// ① 无 token → 401
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s 无 token 返回 %d —— API 是敞开的", path, w.Code)
		}
		// ② 错 token → 401
		w = httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer wrong")
		h.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s 错 token 返回 %d", path, w.Code)
		}
		// ③ 对 token → 200
		w = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer secret")
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s 对 token 返回 %d: %s", path, w.Code, w.Body.String())
		}
	}

	// 豁免名单本身: 只有这几条可以免 token, **多一条都是漏洞**。
	exempt := []string{"/health"}
	for _, path := range exempt {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code == http.StatusUnauthorized {
			t.Fatalf("豁免名单被收紧了: %s 返回 401", path)
		}
	}
}

// ⚠️ 直接钉住豁免名单的**形状**: 任何 `/api/` 出现在中间件里都是漏洞。
//
// 这条不做 HTTP 调用, 而是读源码 —— 因为要断言的恰恰是"名单里有什么",
// 而那个名单是硬编码的。行为测试只能证明"当前这些路径要 token",
// 证明不了"以后不会有人往名单里加一条 API"。
func TestAuth_NoAPIPathIsExempt(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Skipf("读不到 server.go, 跳过: %v", err)
	}
	body := string(src)
	i := strings.Index(body, "func convoAuthMiddleware")
	if i < 0 {
		t.Fatal("鉴权中间件不见了 —— 包装链被重构过?")
	}
	seg := body[i:]
	if j := strings.Index(seg, "\n}"); j > 0 {
		seg = seg[:j]
	}
	if strings.Contains(seg, "/api/") {
		t.Fatalf("鉴权豁免名单里出现了 API 前缀 —— 那几条路由会敞开:\n%s", seg)
	}
}
