package convo

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────
// §6.10.4 称呼的 **owner 入口**
//
// 这一条链之前断在两处 (见 §2.81 与 §2.84):
//   - address_as 被 Scan 进结构体就丢掉 (读者);
//   - SetMemberAddress 没有生产调用者 (写者 = 没入口)。
//
// 读者上一轮接上了; 这条用例钉的是**后半**: 真的能设进去。
// ─────────────────────────────────────────────────────────────

func memberTestServer(t *testing.T) (*Server, *Store) {
	t.Helper()
	e := gateEngine(t)
	if err := EnsureSceneSchema(e.Store); err != nil {
		t.Fatal(err)
	}
	return &Server{Store: e.Store}, e.Store
}

// TestMemberAPI_SetsTheAddress
func TestMemberAPI_SetsTheAddress(t *testing.T) {
	srv, st := memberTestServer(t)
	scene := "group:8801"
	if err := st.EnsureScene(scene); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ObserveMember(scene, "10001", "缇", ""); err != nil {
		t.Fatal(err)
	}

	body := strings.NewReader(`{"scene":"group:8801","uin":"10001","address":"老缇"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/convo/member", body)
	w := httptest.NewRecorder()
	srv.routeMember(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("设置失败: %d %s", w.Code, w.Body.String())
	}

	// ⚠️ 光看响应不算数 —— **必须回去读库**。
	m, err := st.SceneMemberByUIN(scene, "10001")
	if err != nil || m == nil {
		t.Fatalf("读成员失败: %v %v", m, err)
	}
	if m.AddressAs != "老缇" {
		t.Fatalf("库里还是 %q —— 接口说 ok 而没写进去", m.AddressAs)
	}
}

// TestMemberAPI_MissingSceneIsRefused (反面)
//
// 没有 scene 时**必须报错**, 而不是默默列一堆东西或者静默成功。
func TestMemberAPI_MissingSceneIsRefused(t *testing.T) {
	srv, _ := memberTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/convo/member", nil)
	w := httptest.NewRecorder()
	srv.routeMember(w, req)
	if !strings.Contains(w.Body.String(), "need_scene") {
		t.Fatalf("没给 scene 却没报错: %s", w.Body.String())
	}
}
