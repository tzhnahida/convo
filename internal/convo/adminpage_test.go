package convo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ——— §6.8 管理页 ———

func adminServer(t *testing.T) *Server {
	t.Helper()
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	return &Server{Store: e.Store}
}

func traceRow(t *testing.T, s *Store, uin, scene, stance string) {
	t.Helper()
	_, err := s.DB.Exec("INSERT INTO convo_decision_trace (ts, speaker_uin, scene_key, stance) VALUES (?,?,?,?)",
		time.Now().Format(bodyTSLayout), uin, scene, stance)
	if err != nil {
		t.Fatal(err)
	}
}

func getJSON(t *testing.T, h http.HandlerFunc, target string) map[string]interface{} {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	w := httptest.NewRecorder()
	h(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("%s → %d %s", target, w.Code, w.Body.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("坏 JSON: %v (%s)", err, w.Body.String())
	}
	return out
}

// ⚠️ 少于样本下限**不下结论** —— 说了一次没被接, 那叫样本不足, 不叫冷落。
func TestBias_ColdShoulderNeedsSamples(t *testing.T) {
	s := adminServer(t)
	for i := 0; i < ColdShoulderMinSamples-1; i++ {
		traceRow(t, s.Store, "111", "group:1", "silent")
	}
	out := getJSON(t, s.handleBiasGet, "/api/v1/convo/bias")
	speakers := out["speakers"].([]interface{})
	if len(speakers) != 1 {
		t.Fatalf("want 1 speaker, got %v", speakers)
	}
	m := speakers[0].(map[string]interface{})
	if m["cold_shoulder"].(bool) {
		t.Fatalf("只说了 %d 次就判冷落 —— 样本不足不许下结论", ColdShoulderMinSamples-1)
	}
	if m["sample_sufficient"].(bool) {
		t.Fatal("样本不足必须显式标出来, 否则页面上看不出这个结论不可信")
	}
	// 补齐样本 → 仍然一次没被接 → 这时才算冷落。
	traceRow(t, s.Store, "111", "group:1", "silent")
	out = getJSON(t, s.handleBiasGet, "/api/v1/convo/bias")
	m = out["speakers"].([]interface{})[0].(map[string]interface{})
	if !m["cold_shoulder"].(bool) {
		t.Fatalf("样本够了且一次没被接, 应当判冷落: %v", m)
	}
}

// ⚠️ 分母为 0 时返回 0 而不是 100% —— 与 SilentRate 同一条纪律。
func TestBias_EmptyIsZeroNotHundred(t *testing.T) {
	s := adminServer(t)
	out := getJSON(t, s.handleBiasGet, "/api/v1/convo/bias")
	if _, ok := out["explain"]; !ok {
		t.Fatal("缺 explain —— 没有它, 上面的数字不可归因, 偏向仍然是隐形的")
	}
	if int(out["window_days"].(float64)) != biasWindowDays {
		t.Fatal("窗口不对")
	}
	if out["speakers"].([]interface{}) == nil {
		t.Fatal("空数据也要返回空数组, 不能是 null")
	}
}

func TestBias_StanceBreakdown(t *testing.T) {
	s := adminServer(t)
	for _, st := range []string{"speak", "speak", "react", "silent"} {
		traceRow(t, s.Store, "222", "group:9", st)
	}
	out := getJSON(t, s.handleBiasGet, "/api/v1/convo/bias")
	m := out["speakers"].([]interface{})[0].(map[string]interface{})
	if int(m["spoke"].(float64)) != 2 || int(m["reacted"].(float64)) != 1 || int(m["silent"].(float64)) != 1 {
		t.Fatalf("三态拆错了: %v", m)
	}
	if m["speak_rate"].(float64) != 0.5 {
		t.Fatalf("接话率算错了: %v", m["speak_rate"])
	}
	scenes := out["scenes"].([]interface{})
	if len(scenes) != 1 {
		t.Fatalf("场景聚合丢了: %v", scenes)
	}
}

// ⚠️ 这条是**回归尺子**: 引擎与后台各算一遍 owner key, 表现是"管理页永远是空的"。
func TestOwnerKey_EngineAndAdminAgree(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	// ⚠️ **必须真的设一个 SelfQQ**: 默认配置里它是空的, 两边都会算出 "default",
	// 于是这条测试**恒真** —— 把后台改成永远返回 "default" 也照样绿。
	// 这是变异测试抓出来的构造缺陷, 不是实现缺陷。
	e.Cfg.Channel.SelfQQ = "12345"
	k := e.ownerKey()
	if k != "12345" {
		t.Fatalf("引擎没按 SelfQQ 分区: %q", k)
	}
	if e.Store.OwnerKey() != k {
		t.Fatalf("引擎算出来 %q, 库里是 %q —— 后台会读到另一个分区", k, e.Store.OwnerKey())
	}
	srv := &Server{Store: e.Store}
	if ownerKeyOf(srv) != k {
		t.Fatalf("后台读到 %q, 引擎写的是 %q —— 表现是「管理页永远是空的」", ownerKeyOf(srv), k)
	}
}

// ⚠️ §3.7 没有这个入口就等于永远不会跑。
func TestNightPeers_AdminRoundTripAndValidation(t *testing.T) {
	s := adminServer(t)
	put := func(body string) int {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/convo/night-peers", strings.NewReader(body))
		w := httptest.NewRecorder()
		s.routeNightPeers(w, req)
		return w.Code
	}
	if code := put("{\"uin\":0,\"name\":\"无uin\"}"); code != http.StatusBadRequest {
		t.Fatalf("缺 uin 必须 400 (静默丢弃会让「保存成功但没生效」), got %d", code)
	}
	if code := put("{\"uin\":7,\"name\":\"  \"}"); code != http.StatusBadRequest {
		t.Fatalf("空名字必须 400, got %d", code)
	}
	if code := put("{\"uin\":7,\"name\":\"阿七\",\"brief\":\"话少\",\"weight\":2}"); code != http.StatusOK {
		t.Fatalf("合规条目应当写入, got %d", code)
	}
	out := getJSON(t, s.routeNightPeers, "/api/v1/convo/night-peers")
	if !out["ready"].(bool) {
		t.Fatal("写完名册应当 ready=true")
	}
	if len(out["peers"].([]interface{})) != 1 {
		t.Fatalf("名册条数不对: %v", out["peers"])
	}
	// 删除
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/convo/night-peers?uin=7", nil)
	w := httptest.NewRecorder()
	s.routeNightPeers(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("删除失败: %d", w.Code)
	}
	out = getJSON(t, s.routeNightPeers, "/api/v1/convo/night-peers")
	if out["ready"].(bool) {
		t.Fatal("删空之后应当 ready=false")
	}
}

// ⚠️ 撤销升格**不删计数** —— 删了它下次还要重新挨三遍。
func TestFeedbackBoundaries_ToggleKeepsCount(t *testing.T) {
	s := adminServer(t)
	owner := ownerKeyOf(s)
	for i := 0; i < FeedbackPromoteThreshold; i++ {
		if _, err := RecordFeedback(s.Store.DB, owner, "group:1", FeedbackScold, "工资"); err != nil {
			t.Fatal(err)
		}
	}
	out := getJSON(t, s.routeFeedbackBoundaries, "/api/v1/convo/feedback-boundaries")
	fb := out["feedback"].([]interface{})
	if len(fb) != 1 {
		t.Fatalf("want 1, got %v", fb)
	}
	m := fb[0].(map[string]interface{})
	if !m["promoted"].(bool) || m["boundary"] == nil {
		t.Fatalf("应当已升格并给出边界文本: %v", m)
	}
	body := "{\"scene_key\":\"group:1\",\"kind\":\"scold\",\"topic\":\"工资\",\"promoted\":false}"
	req := httptest.NewRequest(http.MethodPut, "/api/v1/convo/feedback-boundaries", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.routeFeedbackBoundaries(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("撤销失败: %d", w.Code)
	}
	out = getJSON(t, s.routeFeedbackBoundaries, "/api/v1/convo/feedback-boundaries")
	m = out["feedback"].([]interface{})[0].(map[string]interface{})
	if m["promoted"].(bool) {
		t.Fatal("撤销没生效")
	}
	if int(m["count"].(float64)) != FeedbackPromoteThreshold {
		t.Fatalf("撤销把计数也清了 (%v) —— 它下次还要重新挨三遍", m["count"])
	}
}

// ⚠️ 「不是删除, 是沉降」—— 只显示"可说"的页面会让人以为没被接住的灵光消失了。
func TestSparks_IncludesSettled(t *testing.T) {
	s := adminServer(t)
	old := time.Now().Add(-2000 * time.Hour).Format(bodyTSLayout)
	if _, err := s.Store.SaveSpark(Spark{Kind: SparkIdea, Content: "老想法", TalkKey: "k",
		Source: SparkSource{Kind: "dream", RefID: "m", HasRef: true}, HeatDelta: 0.5, LastTouchTS: old}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.SaveSpark(Spark{Kind: SparkIdea, Content: "新想法", TalkKey: "k2",
		Source: SparkSource{Kind: "dream", RefID: "m2", HasRef: true}, HeatDelta: 0.9,
		LastTouchTS: time.Now().Format(bodyTSLayout)}); err != nil {
		t.Fatal(err)
	}
	out := getJSON(t, s.handleSparksList, "/api/v1/convo/sparks")
	sp := out["sparks"].([]interface{})
	if len(sp) != 2 {
		t.Fatalf("沉降的灵光必须仍然可见 (不是删除): %v", sp)
	}
	statuses := map[string]bool{}
	for _, x := range sp {
		statuses[x.(map[string]interface{})["status"].(string)] = true
	}
	if !statuses["dropped"] && !statuses["settled"] {
		t.Fatalf("2000 小时前的那条应当是沉降档, got %v", statuses)
	}
	if _, ok := out["thresholds"]; !ok {
		t.Fatal("要给出阈值 —— 否则页面上的档位不可解释")
	}
}

func TestObservation_ReportsReadiness(t *testing.T) {
	s := adminServer(t)
	out := getJSON(t, s.handleObservation, "/api/v1/convo/observation")
	night := out["night"].(map[string]interface{})
	if night["ready"].(bool) {
		t.Fatal("没配名册时 ready 必须是 false —— 跳过是静默的, 页面必须说出来")
	}
	if err := s.Store.SaveNightPeer(NightPeer{UIN: 5, Name: "阿七"}); err != nil {
		t.Fatal(err)
	}
	out = getJSON(t, s.handleObservation, "/api/v1/convo/observation")
	if !out["night"].(map[string]interface{})["ready"].(bool) {
		t.Fatal("配了名册应当 ready=true")
	}
	sh := out["share_week"].(map[string]interface{})
	if int(sh["cap"].(float64)) != ShareWeeklyCap {
		t.Fatal("周分享上限要露出来")
	}
	if _, ok := out["learned"].(map[string]interface{})["unsaid_ratio"]; !ok {
		t.Fatal("「学到的 90% 永远不说出来」是个读数, 页面上必须能看到")
	}
}

func TestLearned_ExposesOriginToOwnerOnly(t *testing.T) {
	s := adminServer(t)
	if _, err := s.Store.SaveLearned(LearnedItem{Kind: LearnFact, TopicKey: "合成器",
		Content: "滤波器是个会唱歌的电阻", Origin: "看了篇科普", BornTS: clockTodayStr()}); err != nil {
		t.Fatal(err)
	}
	out := getJSON(t, s.handleLearnedList, "/api/v1/convo/learned")
	l := out["learned"].([]interface{})
	if len(l) != 1 {
		t.Fatalf("want 1, got %v", l)
	}
	if l[0].(map[string]interface{})["origin"] != "看了篇科普" {
		t.Fatal("来历必须给 owner 看 —— 看不到就无从审「它从哪弄明白的」")
	}
	if _, ok := out["policy"]; !ok {
		t.Fatal("要给出策略 (冷却/上限/星期), 否则读数不可解释")
	}
}
