package convo

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestConsole_NoStoreHeaders 控制台与它的读数**都不许被缓存**。
//
// ⚠️ 2026-10-17: owner 问「控制页还是之前的那个吗」—— 线上明明是新的,
// 他看到的是浏览器给的一份缓存副本 (URL 每次一样, 而我们没给任何缓存指令,
// 浏览器就自己猜着缓)。所以这两行是**修一个真实故障**, 不是洁癖。
func TestConsole_NoStoreHeaders(t *testing.T) {
	s := &Server{}
	// 页面
	rr := httptest.NewRecorder()
	s.handleDashboard(rr, httptest.NewRequest("GET", "/dashboard", nil))
	if cc := rr.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Fatalf("控制台页面必须 no-store, 实为 %q", cc)
	}
	// 读数 (writeJSON 是所有端点的出口)
	rr2 := httptest.NewRecorder()
	writeJSON(rr2, map[string]interface{}{"ok": true})
	if cc := rr2.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Fatalf("读数必须 no-store, 实为 %q", cc)
	}
}
