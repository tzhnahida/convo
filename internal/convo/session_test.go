package convo

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
)

func TestParseSessionVerdict(t *testing.T) {
	cases := []struct {
		raw  string
		same bool
		bad  bool
	}{
		{"{\"same\":true}", true, false},
		{"{\"same\":false}", false, false},
		{"好的: {\"same\":true} 以上", true, false},
		{"我觉得还在聊", false, true},
		{"{\"x\":1}", false, true},
	}
	for _, c := range cases {
		got, err := ParseSessionVerdict(c.raw)
		if c.bad {
			if err == nil {
				t.Errorf("%q 应当报错, 得到 %v", c.raw, got)
			}
			continue
		}
		if err != nil || got != c.same {
			t.Errorf("%q → (%v, %v), 期望 %v", c.raw, got, err, c.same)
		}
	}
}

func TestSession_TouchAndExpire(t *testing.T) {
	s := newTestStore(t)
	if err := EnsureSessionSchema(s.DB); err != nil {
		t.Fatal(err)
	}
	now := clock.Now()
	if err := s.TouchSession("group:1", "甲: 晚上吃啥 / 乙: 火锅", now); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.SessionActive("group:1", 30*time.Minute, now); err != nil || !ok {
		t.Fatalf("刚刷新的会话应当是活跃的: ok=%v err=%v", ok, err)
	}
	// 31 分钟后 → 过期
	if _, ok, _ := s.SessionActive("group:1", 30*time.Minute, now.Add(31*time.Minute)); ok {
		t.Fatal("超过 30 分钟仍然活跃 —— 窗口没生效")
	}
}

// ⚠️ 核心行为: 5 分钟判定"还是同一话题" → 重置 30 分钟窗口。
func TestSessionPoll_SameResetsWindow(t *testing.T) {
	s := newTestStore(t)
	if err := EnsureSessionSchema(s.DB); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: s, Cfg: DefaultConfig()} // Session 默认开
	now := clock.Now()
	if err := s.TouchSession("group:1", "在聊火锅", now); err != nil {
		t.Fatal(err)
	}
	// 把轮询时间拨到 6 分钟前, last_ts 拨到 10 分钟前
	oldPoll := now.Add(-6 * time.Minute).Format(bodyTSLayout)
	oldLast := now.Add(-10 * time.Minute).Format(bodyTSLayout)
	if _, err := s.DB.Exec("UPDATE convo_session SET poll_ts=?, last_ts=? WHERE scene_key=?", oldPoll, oldLast, "group:1"); err != nil {
		t.Fatal(err)
	}
	// 这 5 分钟里别人又说了话
	if _, err := s.InsertMessageFull("user", "qq", "text", "那就火锅吧", MsgMeta{SceneKey: "group:1", SenderUIN: "111"}); err != nil {
		t.Fatal(err)
	}
	e.Chatter = &fakeChatter{outputs: []string{"{\"same\":true}"}}
	e.sessionPollTick(context.Background())

	var last string
	if err := s.DB.QueryRow("SELECT COALESCE(last_ts,'') FROM convo_session WHERE scene_key='group:1'").Scan(&last); err != nil {
		t.Fatal(err)
	}
	if last == oldLast {
		t.Fatal("判定为同一话题, 但没有重置 30 分钟窗口")
	}
}

// 判定"已经在聊别的事" → 关会话。
func TestSessionPoll_NewTopicCloses(t *testing.T) {
	s := newTestStore(t)
	if err := EnsureSessionSchema(s.DB); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: s, Cfg: DefaultConfig()}
	now := clock.Now()
	if err := s.TouchSession("group:1", "在聊火锅", now); err != nil {
		t.Fatal(err)
	}
	oldPoll := now.Add(-6 * time.Minute).Format(bodyTSLayout)
	if _, err := s.DB.Exec("UPDATE convo_session SET poll_ts=? WHERE scene_key=?", oldPoll, "group:1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertMessageFull("user", "qq", "text", "话说昨天的比赛", MsgMeta{SceneKey: "group:1", SenderUIN: "111"}); err != nil {
		t.Fatal(err)
	}
	e.Chatter = &fakeChatter{outputs: []string{"{\"same\":false}"}}
	e.sessionPollTick(context.Background())

	var open int
	if err := s.DB.QueryRow("SELECT open FROM convo_session WHERE scene_key='group:1'").Scan(&open); err != nil {
		t.Fatal(err)
	}
	if open != 0 {
		t.Fatal("判定换了话题, 但会话没有结束")
	}
}

// 判定失败 → **保持原状态** (fail-open): 不关会话, 窗口也不动。
func TestSessionPoll_FailureKeepsState(t *testing.T) {
	s := newTestStore(t)
	if err := EnsureSessionSchema(s.DB); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: s, Cfg: DefaultConfig()}
	now := clock.Now()
	if err := s.TouchSession("group:1", "在聊火锅", now); err != nil {
		t.Fatal(err)
	}
	oldPoll := now.Add(-6 * time.Minute).Format(bodyTSLayout)
	oldLast := now.Add(-10 * time.Minute).Format(bodyTSLayout)
	if _, err := s.DB.Exec("UPDATE convo_session SET poll_ts=?, last_ts=? WHERE scene_key=?", oldPoll, oldLast, "group:1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertMessageFull("user", "qq", "text", "嗯", MsgMeta{SceneKey: "group:1", SenderUIN: "111"}); err != nil {
		t.Fatal(err)
	}
	e.Chatter = &fakeChatter{err: errors.New("boom")}
	e.sessionPollTick(context.Background())

	var open int
	var last string
	if err := s.DB.QueryRow("SELECT open, COALESCE(last_ts,'') FROM convo_session WHERE scene_key='group:1'").Scan(&open, &last); err != nil {
		t.Fatal(err)
	}
	if open != 1 || last != oldLast {
		t.Fatalf("判定失败时状态被改了: open=%d last=%q (期望 1 / %q)", open, last, oldLast)
	}
}

// 会话中它**刚说完话的 90 秒内不接** —— 否则每条消息都接一句, 变成刷屏。
func TestGate_SessionJustSpokeStaysSilent(t *testing.T) {
	s := newTestStore(t)
	if err := EnsureSessionSchema(s.DB); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProfile(&PersonaProfile{Name: "小夜", CreatedTS: clock.Now().Format(bodyTSLayout)}); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: s, Cfg: DefaultConfig()}
	e.ChimeRollFn = func(int) int { return 99 } // 别让随机插话混进来
	e.DayMoodFn = func(string) DayMood { return DayMoodFlat }
	e.TouchSession(sceneWireKey)
	if _, err := s.InsertMessageFull("ai", "qq", "text", "刚说完一句", MsgMeta{SceneKey: sceneWireKey}); err != nil {
		t.Fatal(err)
	}
	res := e.GateGroupMessage(SceneMsg{
		SceneKey: sceneWireKey, UIN: "111", Text: "随便说点什么", TS: clock.Now(),
	}, SelfIdentity{UIN: "3487037837"}, sceneWireCfg())
	if res.Stance != StanceSilent || !strings.Contains(res.Reason, "缓一下") {
		t.Fatalf("刚说完话又接了: %s (%s)", res.Stance, res.Reason)
	}
}

// 会话中门槛降一档 (2 → 1); 不在会话中不动。
func TestEffectiveValueConfig_SessionLowersBar(t *testing.T) {
	s := newTestStore(t)
	if err := EnsureSessionSchema(s.DB); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProfile(&PersonaProfile{Name: "小夜", CreatedTS: clock.Now().Format(bodyTSLayout)}); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: s, Cfg: DefaultConfig()}
	sc := SceneMsg{SceneKey: "group:1"}
	base := e.effectiveValueConfig(sc, GroupConfig{})
	if err := s.TouchSession("group:1", "在聊火锅", clock.Now()); err != nil {
		t.Fatal(err)
	}
	inSession := e.effectiveValueConfig(sc, GroupConfig{})
	if inSession.SpeakMinHits != base.SpeakMinHits-1 {
		t.Fatalf("会话中没有降门槛: base=%d session=%d", base.SpeakMinHits, inSession.SpeakMinHits)
	}
	if inSession.SpeakMinHits < 1 {
		t.Fatal("门槛降到 0 以下 —— 那就变成\"命中就开口\"")
	}
}
