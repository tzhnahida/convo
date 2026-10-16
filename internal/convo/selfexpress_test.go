package convo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tzhnahida/convo/internal/clock"
)

// ——— 替身 ———

// fakeSelfSender 同时满足 Sender 与 SelfExpressSender ——
// 前者是私聊出口 (提议/确认语), 后者是那五个对外动作。
type fakeSelfSender struct {
	mu        sync.Mutex
	card      string
	avatar    string
	profile   string
	qzoneText string
	qzoneMsg  string
	dms       []string
	imgs      []string
}

func (f *fakeSelfSender) SetGroupCard(gid int64, card string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.card = card
	return nil
}

func (f *fakeSelfSender) SetProfile(nickname, note string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.profile = nickname
	return nil
}

func (f *fakeSelfSender) SetAvatar(p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.avatar = p
	return nil
}

func (f *fakeSelfSender) PostQzone(text string, imgs []string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.qzoneText, f.qzoneMsg = text, "999"
	return "999", nil
}

func (f *fakeSelfSender) DeleteQzone(msgID string) error { return nil }

func (f *fakeSelfSender) SendText(uid int64, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dms = append(f.dms, text)
	return nil
}

func (f *fakeSelfSender) SendImage(uid int64, p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.imgs = append(f.imgs, p)
	return nil
}

func (f *fakeSelfSender) gotAvatar() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.avatar
}

func (f *fakeSelfSender) gotCard() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.card
}

func (f *fakeSelfSender) dmCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.dms)
}

type fakeScorer struct {
	score float64
	nsfw  bool
	note  string
	err   error
}

func (f fakeScorer) Score(ctx context.Context, imagePath string) (float64, bool, string, error) {
	return f.score, f.nsfw, f.note, f.err
}

type fakeJudger struct {
	reply string
	err   error
}

func (f fakeJudger) Chat(ctx context.Context, system, user string) (string, error) {
	return f.reply, f.err
}

// ——— ① 通道层: 五个动作打出去的是什么 ———

func TestSelfExpressChannel_ActionsAndPayloads(t *testing.T) {
	type oneCall struct {
		path string
		body map[string]interface{}
	}
	var mu sync.Mutex
	var calls []oneCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		calls = append(calls, oneCall{path: r.URL.Path, body: body})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"status\":\"ok\",\"retcode\":0,\"data\":{\"message_id\":12345}}"))
	}))
	defer srv.Close()
	c := NewOneBotClient(srv.URL, "")
	if err := c.SetGroupCard(1034712035, "小夜"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetProfile("小夜", "在"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetAvatar("/tmp/a.jpg"); err != nil {
		t.Fatal(err)
	}
	msgID, err := c.PostQzone("今天天气不错", []string{"/tmp/a.jpg"})
	if err != nil {
		t.Fatal(err)
	}
	if msgID != "12345" {
		t.Fatalf("说说应当带回 msg id, 实为 %q", msgID)
	}
	if err := c.DeleteQzone(msgID); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	byPath := map[string]map[string]interface{}{}
	for _, cl := range calls {
		byPath[cl.path] = cl.body
	}
	if b := byPath["/set_group_card"]; b == nil || b["card"] != "小夜" {
		t.Fatalf("set_group_card body: %+v", b)
	}
	if b := byPath["/set_qq_profile"]; b == nil || b["nickname"] != "小夜" {
		t.Fatalf("set_qq_profile body: %+v", b)
	}
	if b := byPath["/set_qq_avatar"]; b == nil || !strings.HasSuffix(str(b["file"]), "a.jpg") {
		t.Fatalf("set_qq_avatar body: %+v", b)
	}
	if b := byPath["/send_qzone_msg"]; b == nil {
		t.Fatal("send_qzone_msg 没被调用")
	} else if segs, ok := b["message"].([]interface{}); !ok || len(segs) != 2 {
		t.Fatalf("说说应当是 文字+图 两段: %+v", b["message"])
	}
	if b := byPath["/delete_qzone_msg"]; b == nil || str(b["message_id"]) != "12345" {
		t.Fatalf("delete_qzone_msg body: %+v", b)
	}
}

// str 把 interface{} 当字符串读 (测试断言用, 容忍数字/字符串两种回法)。
func str(v interface{}) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return strings.Trim(string(b), "\"")
}

// ——— ② 图源: pixiv 的源头 R18 关 ———

func TestPixivSource_SafeModeAndR18Filter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/ajax/search/artworks/") {
			t.Errorf("路径不对: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("mode"); got != "safe" {
			t.Errorf("r18 关着时搜索必须走 safe 模式, 实为 %q", got)
		}
		if r.Header.Get("Referer") == "" {
			t.Error("pixiv 必须带 Referer, 否则 403")
		}
		_, _ = w.Write([]byte(`{"error":false,"message":"","body":{"illustManga":{"data":[` +
			`{"id":"1","title":"安静的风景","url":"https://i.pximg.net/c/360x360_70/img-master/img/2024/01/01/1_p0_master1200.jpg","userName":"A","tags":["風景"],"xRestrict":0},` +
			`{"id":"2","title":"R-18 那种","url":"https://i.pximg.net/c/360x360_70/img-master/img/2024/01/01/2_p0_master1200.jpg","userName":"B","tags":["R-18"],"xRestrict":0}` +
			`]}}}`))
	}))
	defer srv.Close()
	cfg := ImageSourceConfig{Source: "pixiv", PerRun: 5}
	cfg.Pixiv.Base = srv.URL
	src, err := NewImageSource(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := src.Fetch(context.Background(), "テスト", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("R18 那张该在源头就被拦掉, 实得 %d 张", len(got))
	}
	if strings.Contains(got[0].URL, "/c/360x360_70/") {
		t.Fatalf("缩略图尺寸段没去掉: %s", got[0].URL)
	}
	if !strings.Contains(got[0].PageURL, "/artworks/1") {
		t.Fatalf("出处链接不对: %s", got[0].PageURL)
	}
}

// ——— ③ 图源: json 可插拔 (点路径 + 字段名) ———

func TestJSONSource_DotPathAndFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":{"list":[{"pic":"https://x/1.jpg","t":"标题一"},{"pic":"https://x/2.jpg","t":"标题二"}]}}`))
	}))
	defer srv.Close()
	cfg := ImageSourceConfig{Source: "json", PerRun: 9}
	cfg.JSON = JSONSourceConfig{URL: srv.URL + "/s?q={query}", ArrayPath: "result.list", URLField: "pic", TitleField: "t"}
	src, err := NewImageSource(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := src.Fetch(context.Background(), "猫", 9)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Title != "标题二" {
		t.Fatalf("点路径/字段名没生效: %+v", got)
	}
}

// ——— ④ 视觉闸: fail-closed ———

func TestScreenCandidate_FailsClosed(t *testing.T) {
	ctx := context.Background()
	c := &ImageCandidate{LocalPath: "/tmp/x.jpg"}
	if ok, why := ScreenCandidate(ctx, nil, 0.6, true, c); ok {
		t.Fatal("没配视觉且 required → 必须拒绝: " + why)
	}
	if ok, why := ScreenCandidate(ctx, nil, 0.6, false, c); !ok {
		t.Fatal("显式放弃那道闸时应当放行: " + why)
	}
	if ok, why := ScreenCandidate(ctx, fakeScorer{err: errors.New("boom")}, 0.6, true, c); ok {
		t.Fatal("视觉报错 → 必须拒绝 (fail-closed): " + why)
	}
	if ok, why := ScreenCandidate(ctx, fakeScorer{score: 0.9, nsfw: true}, 0.6, true, c); ok {
		t.Fatal("NSFW → 必须拒绝: " + why)
	}
	if ok, why := ScreenCandidate(ctx, fakeScorer{score: 0.3}, 0.6, true, c); ok {
		t.Fatal("低分 → 必须拒绝: " + why)
	}
	ok, why := ScreenCandidate(ctx, fakeScorer{score: 0.8, note: "一只安静的猫"}, 0.6, true, c)
	if !ok {
		t.Fatalf("该放行: %s", why)
	}
	if c.Note != "一只安静的猫" || c.Score != 0.8 {
		t.Fatalf("结论要落到候选上: %+v", c)
	}
}

// ——— ⑤ 解析: 含糊一律当不要 ———

func TestParseSelfJudgment(t *testing.T) {
	cases := []struct {
		out  string
		yes  bool
		body string
	}{
		{"要\n这张看着挺像我", true, "这张看着挺像我"},
		{"不要\n太花哨了", false, "太花哨了"},
		{"YES\ndraft text", true, "draft text"},
		{"NO\nnot my thing", false, "not my thing"},
		{"嗯……再看看", false, ""},
		{"", false, ""},
	}
	for _, tc := range cases {
		yes, body := parseSelfJudgment(tc.out)
		if yes != tc.yes || body != tc.body {
			t.Errorf("%q → (%v,%q), 期望 (%v,%q)", tc.out, yes, body, tc.yes, tc.body)
		}
	}
}

func TestSelfActionApproval_OnlyShortClearWords(t *testing.T) {
	cases := []struct {
		in      string
		approve bool
		ok      bool
	}{
		{"准", true, true},
		{"准了。", true, true},
		{"不要", false, true},
		{"不换", false, true},
		{"嗯", false, false},
		{"那就这样吧我同意了", false, false},
		{"", false, false},
	}
	for _, tc := range cases {
		a, ok := SelfActionApproval(tc.in)
		if a != tc.approve || ok != tc.ok {
			t.Errorf("%q → (%v,%v), 期望 (%v,%v)", tc.in, a, ok, tc.approve, tc.ok)
		}
	}
}

// ——— ⑥ 限频 ———

func TestSelfKindDue_RespectsCaps(t *testing.T) {
	st := newTestStore(t)
	e := &Engine{Store: st}
	cfg := SelfExpressConfig{AvatarMinDays: 30, QzoneMinDays: 7}
	if !e.selfKindDue(SelfKindAvatar, cfg) {
		t.Fatal("从没做过 → 应当到期")
	}
	if _, err := st.InsertSelfAction(SelfAction{Kind: SelfKindAvatar, State: SelfStateApplied,
		AppliedTS: clock.Now().Format("2006-01-02 15:04:05")}); err != nil {
		t.Fatal(err)
	}
	if e.selfKindDue(SelfKindAvatar, cfg) {
		t.Fatal("刚换过头像 → 不该到期")
	}
	if !e.selfKindDue(SelfKindQzone, cfg) {
		t.Fatal("说说与头像是两套限频, 不该互相影响")
	}
}

func TestGroupCardText_PlainName(t *testing.T) {
	e := &Engine{}
	e.Cfg.Group.SelfAliases = []string{"小夜", "纳西妲"}
	if got := e.GroupCardText(SelfExpressConfig{}); got != "小夜" {
		t.Fatalf("空配置应当退回第一个别名, 实为 %q", got)
	}
	if got := e.GroupCardText(SelfExpressConfig{CardText: "草王"}); got != "草王" {
		t.Fatalf("配了固定文案就该用它, 实为 %q", got)
	}
	if got := (&Engine{}).GroupCardText(SelfExpressConfig{}); got != "小夜" {
		t.Fatalf("连别名都没有时应当兜到「小夜」, 实为 %q", got)
	}
}

// ——— ⑦ 端到端: 图源 → 视觉闸 → 它自己判 → 真的换 ———

// newSelfExpressEngine 一台服务器同时当图源与图床, 引擎用假替身。
func newSelfExpressEngine(t *testing.T, cfg SelfExpressConfig, sc VisionScorer, judge Chatter) (*Engine, *Store, *fakeSelfSender, *httptest.Server) {
	t.Helper()
	st := newTestStore(t)
	if err := st.SaveProfile(&PersonaProfile{Name: "小夜", Template: "bestie", CreatedTS: clock.TodayTS()}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api"):
			_, _ = w.Write([]byte(`{"data":{"items":[{"img":"http://` + r.Host + `/a.jpg","title":"测试图"}]}}`))
		case strings.HasPrefix(r.URL.Path, "/a.jpg"):
			_, _ = w.Write([]byte("JPEGBYTES"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	fs := &fakeSelfSender{}
	e := &Engine{
		Store:   st,
		Sender:  fs,
		Chatter: judge,
		Cycle:   &StickerCycle{Scorer: sc},
	}
	e.Cfg.SelfExpress = cfg
	e.Cfg.SelfExpress.Images = ImageSourceConfig{
		Source: "json", Dir: t.TempDir(), PerRun: 3,
		// ⚠️ 关键词必须给: 没关键词 = 没得搜 = 这一类动作静默不发生 (设计如此, 不是 bug)。
		RoleQuery: "小夜",
		JSON: JSONSourceConfig{URL: srv.URL + "/api?q={query}", ArrayPath: "data.items",
			URLField: "img", TitleField: "title"},
	}
	e.Cfg.Group.Whitelist = []string{"1034712035"}
	return e, st, fs, srv
}

func TestSelfExpressTick_AutoAvatarGoesThroughGateAndJudgment(t *testing.T) {
	cfg := SelfExpressConfig{Enabled: true, Avatar: "auto", Card: "auto"}
	e, st, fs, srv := newSelfExpressEngine(t, cfg, fakeScorer{score: 0.9, note: "一张安静的测试图"}, fakeJudger{reply: "要\n这张看着挺像我"})
	defer srv.Close()
	if err := e.selfExpressTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fs.gotAvatar() == "" {
		t.Fatal("它说行 → 头像应当被换掉")
	}
	if fs.gotCard() != "小夜" {
		t.Fatalf("群名片应当设成固定名字, 实为 %q", fs.gotCard())
	}
	rows, err := st.LoadSelfActions(20)
	if err != nil || len(rows) == 0 {
		t.Fatalf("每一次判断都必须留痕: %v", err)
	}
	seenAvatar := false
	for _, r := range rows {
		if r.Kind == SelfKindAvatar && r.State == SelfStateApplied {
			seenAvatar = true
			if r.Score == 0 || r.Note == "" {
				t.Errorf("头像那行应当带分数与图意: %+v", r)
			}
			if !strings.Contains(r.Decision, "它说行") {
				t.Errorf("决策理由要落库: %q", r.Decision)
			}
		}
	}
	if !seenAvatar {
		t.Fatalf("没有 applied 的 avatar 行: %+v", rows)
	}
}

func TestSelfExpressTick_NSFWNeverReachesTheChannel(t *testing.T) {
	cfg := SelfExpressConfig{Enabled: true, Avatar: "auto"}
	e, _, fs, srv := newSelfExpressEngine(t, cfg, fakeScorer{score: 0.95, nsfw: true}, fakeJudger{reply: "要"})
	defer srv.Close()
	if err := e.selfExpressTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fs.gotAvatar() != "" {
		t.Fatal("NSFW 绝不能走到换头像 —— 闸在图源之后、判断之前")
	}
}

func TestSelfExpressTick_JudgmentNoMeansNo(t *testing.T) {
	cfg := SelfExpressConfig{Enabled: true, Avatar: "auto"}
	e, st, fs, srv := newSelfExpressEngine(t, cfg, fakeScorer{score: 0.9}, fakeJudger{reply: "不要\n这张不像我"})
	defer srv.Close()
	if err := e.selfExpressTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fs.gotAvatar() != "" {
		t.Fatal("它说了不要 → 一个字都不该动")
	}
	rows, _ := st.LoadSelfActions(10)
	found := false
	for _, r := range rows {
		if r.State == SelfStateRejected {
			found = true
		}
	}
	if !found {
		t.Fatal("它拒绝的那次也要留痕 (拒绝也算一次决定)")
	}
}

// ——— ⑧ propose 档: 先发图, 等一个字 ———

func TestSelfExpress_ProposeThenOwnerSaysYes(t *testing.T) {
	cfg := SelfExpressConfig{Enabled: true, Avatar: "propose"}
	e, st, fs, srv := newSelfExpressEngine(t, cfg, fakeScorer{score: 0.9}, fakeJudger{reply: "要\n这张好"})
	defer srv.Close()
	ctx := context.Background()
	if err := e.selfExpressTick(ctx); err != nil {
		t.Fatal(err)
	}
	if fs.dmCount() == 0 {
		t.Fatal("提议档必须先把图与问题发给你")
	}
	if fs.gotAvatar() != "" {
		t.Fatal("提议档不许自己动手")
	}
	if _, ok, _ := st.PendingSelfAction(); !ok {
		t.Fatal("应当留下一条待办")
	}
	if !e.trySelfActionReply(ctx, "准") {
		t.Fatal("「准」应当被认出来并处理掉")
	}
	if fs.gotAvatar() == "" {
		t.Fatal("批准之后应当真的换头像")
	}
	if _, ok, _ := st.PendingSelfAction(); ok {
		t.Fatal("待办应当已经结清")
	}
}

func TestSelfExpress_OwnerSaysNo(t *testing.T) {
	cfg := SelfExpressConfig{Enabled: true, Avatar: "propose"}
	e, st, fs, srv := newSelfExpressEngine(t, cfg, fakeScorer{score: 0.9}, fakeJudger{reply: "要\n想换"})
	defer srv.Close()
	ctx := context.Background()
	if err := e.selfExpressTick(ctx); err != nil {
		t.Fatal(err)
	}
	if !e.trySelfActionReply(ctx, "不要") {
		t.Fatal("「不要」应当被认出来")
	}
	if fs.gotAvatar() != "" {
		t.Fatal("说了不要还敢换")
	}
	if _, ok, _ := st.PendingSelfAction(); ok {
		t.Fatal("待办应当被结清 (记为 rejected)")
	}
}

// ——— ⑨ 总开关关着 = 一个动作都不发生 ———

func TestSelfExpressTick_DisabledMeansNothingHappens(t *testing.T) {
	cfg := SelfExpressConfig{Enabled: false, Avatar: "auto", Card: "auto"}
	e, st, fs, srv := newSelfExpressEngine(t, cfg, fakeScorer{score: 0.9}, fakeJudger{reply: "要"})
	defer srv.Close()
	if err := e.selfExpressTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fs.gotAvatar() != "" || fs.gotCard() != "" {
		t.Fatal("总开关关着还敢动")
	}
	if rows, _ := st.LoadSelfActions(5); len(rows) != 0 {
		t.Fatalf("关着的时候不该有任何记录: %+v", rows)
	}
}
