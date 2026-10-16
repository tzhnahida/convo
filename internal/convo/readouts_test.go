package convo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ——— 读数面: 时序 / 关系两层 / 内部状态 ———
//
// ⚠️ 这一组测的是**契约**, 不是数字对不对:
//
//	空库**不许报错** —— 页面上一张永远红的卡, 和「这个机制没跑」长得一模一样;
//	横轴必须**连续** —— 没数据的那天补 0, 否则「那天什么都没有」与「数据丢了」不可分辨;
//	阈值来自**代码** —— 页面不该另抄一份 0.15/0.40/0.70。

func readoutServer(t *testing.T) *http.ServeMux {
	t.Helper()
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	s := &Server{Store: e.Store}
	for _, ensure := range []func() error{
		func() error { return EnsureBondSchema(s.Store.DB) },
		func() error { return EnsureBondPersonSchema(s.Store.DB) },
		func() error { return EnsureHeatSchema(s.Store) },
		func() error { return EnsureSceneSchema(s.Store) },
	} {
		if err := ensure(); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	return mux
}

func readoutJSON(t *testing.T, mux *http.ServeMux, target string) map[string]interface{} {
	t.Helper()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("%s -> %d %s", target, w.Code, w.Body.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s 不是 JSON: %v", target, err)
	}
	return out
}

// TestReadouts_EmptyStoreIsNotAnError
//
// ⚠️ 表建好了但一条记录都没有 → 返回**空数组**。
// 空数组在页面上是「还没有数据」, 500 是「这个读数坏了」—— 两者不能混。
func TestReadouts_EmptyStoreIsNotAnError(t *testing.T) {
	mux := readoutServer(t)
	for _, target := range []string{
		"/api/v1/convo/series?kind=msgs",
		"/api/v1/convo/series?kind=counters",
		"/api/v1/convo/series?kind=heat&scene=private:owner",
		"/api/v1/convo/series?kind=mood",
		"/api/v1/convo/bonds",
		"/api/v1/convo/state",
	} {
		got := readoutJSON(t, mux, target)
		if e, bad := got["error"]; bad {
			t.Errorf("%s 在空库上报错: %v", target, e)
		}
	}
}

// TestSeries_MsgsFillsEveryDay
//
// ⚠️ 横轴连续: 要 30 天就得到 30 个点, 没数据的那天是 0 ——
// 折线直接跨过去会把「那天什么都没有」画成「一切正常」。
func TestSeries_MsgsFillsEveryDay(t *testing.T) {
	mux := readoutServer(t)
	got := readoutJSON(t, mux, "/api/v1/convo/series?kind=msgs&days=30")
	series, ok := got["series"].([]interface{})
	if !ok {
		t.Fatalf("series 不是数组: %T", got["series"])
	}
	if len(series) != 30 {
		t.Fatalf("要 30 天却得到 %d 个点 —— 缺口没补 0", len(series))
	}
	first, _ := series[0].(map[string]interface{})
	if _, has := first["user"]; !has {
		t.Fatal("点里没有 user 线 —— 一条线看不出谁在说")
	}
}

// TestSeries_CounterSeriesIsContinuous: 计数器那条同样要连续。
func TestSeries_CounterSeriesIsContinuous(t *testing.T) {
	mux := readoutServer(t)
	got := readoutJSON(t, mux, "/api/v1/convo/series?kind=counters&name=ai_opens&days=7")
	cs, ok := got["counters"].(map[string]interface{})
	if !ok {
		t.Fatalf("counters 不是对象: %T", got["counters"])
	}
	rows, _ := cs["ai_opens"].([]interface{})
	if len(rows) != 7 {
		t.Fatalf("要 7 天却得到 %d 个点", len(rows))
	}
}

// TestBonds_TwoLayersAndThresholdsFromCode
//
// ⚠️ 阈值必须**来自代码** —— 页面另抄一份 0.15/0.40/0.70 就等于第二个真源,
// 而它一定会与这里分叉。
func TestBonds_TwoLayersAndThresholdsFromCode(t *testing.T) {
	mux := readoutServer(t)
	got := readoutJSON(t, mux, "/api/v1/convo/bonds")
	for _, k := range []string{"scene", "person", "thresholds"} {
		if _, has := got[k]; !has {
			t.Errorf("bonds 少了 %q —— 关系有两层, 少一层就看不出群里热/私聊冷", k)
		}
	}
	th, _ := got["thresholds"].(map[string]interface{})
	if th["acquaint"] != BondAcquaintAt || th["intimate"] != BondIntimateAt {
		t.Fatalf("阈值不是代码里那几个: %v", th)
	}
}

// TestBonds_PersonLayerShowsUp
//
// ⚠️ 人物层这一层**必须能看见** —— 它是「群与私聊是同一个人」的唯一证据。
func TestBonds_PersonLayerShowsUp(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	if err := EnsureBondPersonSchema(e.Store.DB); err != nil {
		t.Fatal(err)
	}
	if err := SaveBondPerson(e.Store.DB, e.Store.OwnerKey(), "10001",
		Bond{Closeness: 0.42, Familiarity: 0.5}); err != nil {
		t.Fatal(err)
	}
	srv := &Server{Store: e.Store}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	got := readoutJSON(t, mux, "/api/v1/convo/bonds")
	rows, _ := got["person"].([]interface{})
	if len(rows) != 1 {
		t.Fatalf("人物层应当有 1 行, 实得 %d", len(rows))
	}
	row, _ := rows[0].(map[string]interface{})
	if row["stage"] != string(BondStageFamiliar) {
		t.Fatalf("阶段算错了: %v (0.42 应当是熟人)", row["stage"])
	}
}

// TestState_CarriesTheInternalReadings
//
// ⚠️ 这一屏是「它现在怎么样」—— 心情/身体/疲劳/热力四样缺一样, 那一屏就答不上来。
func TestState_CarriesTheInternalReadings(t *testing.T) {
	mux := readoutServer(t)
	got := readoutJSON(t, mux, "/api/v1/convo/state")
	for _, k := range []string{"mood", "body", "fatigue", "heat", "now"} {
		if _, has := got[k]; !has {
			t.Errorf("state 少了 %q", k)
		}
	}
	f, _ := got["fatigue"].(map[string]interface{})
	if _, has := f["tier"]; !has {
		t.Fatal("疲劳没给档位 —— 页面这一格就失去意义")
	}
}
