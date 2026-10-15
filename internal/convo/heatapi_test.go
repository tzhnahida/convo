package convo

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────
// §12 热力流水的展示面
//
// §2.68 写过: 热力链有三段, 而每一段失效时**现象都只是"它不主动开口"** ——
// 从外面看不出是哪一段断的。
//
// 而流水表每一笔带 before / after / delta / seq, 所以:
//
//   有没有入账  → 看有没有行;
//   序号推没推  → 看 seq;
//   是不是新会话 → 看 seq 有没有回退 (它是按会话重计的)。
// ─────────────────────────────────────────────────────────────

func heatTestServer(t *testing.T) *Server {
	t.Helper()
	e := gateEngine(t)
	return &Server{Store: e.Store}
}

// TestHeatAPI_ReturnsALogArray
//
// 这条只钉**形状**: 它必须回一个数组, 而不是 null ——
// 否则调用方(控制台)拿到 null 会渲染成空白, 而那与"没有入账"长得一样。
func TestHeatAPI_ReturnsALogArray(t *testing.T) {
	srv := heatTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/convo/heat", nil)
	w := httptest.NewRecorder()
	srv.routeHeat(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("状态 %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"log":[`) {
		t.Fatalf("没有回数组 (空表也必须是 [] 而不是 null): %s", body)
	}
	if !strings.Contains(body, `"limit":20`) {
		t.Fatalf("缺省 limit 应当折成 20: %s", body)
	}
}

// TestHeatAPI_BadLimitFallsBack (反面, 零值纪律)
//
// limit 乱填时**折回缺省**, 而不是报错也不是零 ——
// "没提供/填错"与"要零条"是两件事。
func TestHeatAPI_BadLimitFallsBack(t *testing.T) {
	srv := heatTestServer(t)
	for _, bad := range []string{"0", "-5", "abc", "99999"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/convo/heat?limit="+bad, nil)
		w := httptest.NewRecorder()
		srv.routeHeat(w, req)
		if !strings.Contains(w.Body.String(), `"limit":20`) {
			t.Fatalf("limit=%s 应当折回 20, 实得: %s", bad, w.Body.String())
		}
	}
}
