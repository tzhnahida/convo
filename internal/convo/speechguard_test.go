package convo

import (
	"strings"
	"testing"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
)

func TestIsStopRequest(t *testing.T) {
	yes := []string{"闭嘴", " 闭嘴 ", "别说了", "安静点", "停一下", "烦死了", "别刷屏了"}
	for _, s := range yes {
		if !IsStopRequest(s) {
			t.Errorf("%q 应当算叫停", s)
		}
	}
	no := []string{"", "今天天气不错", "他说闭嘴了我才停的（这里在转述一件很长的事情，不该当叫停指令）"}
	for _, s := range no {
		if IsStopRequest(s) {
			t.Errorf("%q 不该算叫停", s)
		}
	}
}

// 叫停 → 静默 + 记 mute; 而且**不解释**。
func TestGate_StopRequestSilences(t *testing.T) {
	e := gateEngine(t)
	if err := EnsureSpeechGuardSchema(e.Store.DB); err != nil {
		t.Fatal(err)
	}
	res := e.GateGroupMessage(SceneMsg{SceneKey: sceneWireKey, UIN: "111", Text: "闭嘴", TS: clock.Now()},
		SelfIdentity{UIN: "3487037837"}, sceneWireCfg())
	if res.Stance != StanceSilent {
		t.Fatalf("被叫停却也接话: %s (%s)", res.Stance, res.Reason)
	}
	if until, _, muted := e.Store.SceneMute(sceneWireKey, clock.Now()); !muted || until.Before(clock.Now()) {
		t.Fatal("叫停之后没有静默期")
	}
}

// 静默期内普通消息一律不接。
func TestGate_MutedSceneStaysSilent(t *testing.T) {
	e := gateEngine(t)
	if err := EnsureSpeechGuardSchema(e.Store.DB); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.MuteScene(sceneWireKey, clock.Now().Add(30*time.Minute).Format(bodyTSLayout), "闭嘴"); err != nil {
		t.Fatal(err)
	}
	res := e.GateGroupMessage(SceneMsg{SceneKey: sceneWireKey, UIN: "111", Text: "哈哈说到哪了", TS: clock.Now()},
		SelfIdentity{UIN: "3487037837"}, sceneWireCfg())
	if res.Stance != StanceSilent || !strings.Contains(res.Reason, "被叫停中") {
		t.Fatalf("静默期里接了话: %s (%s)", res.Stance, res.Reason)
	}
}

// 静默期内被 @ = 明确叫它 → 解除静默并回应。
func TestGate_MuteLiftedByAtSelf(t *testing.T) {
	e := gateEngine(t)
	if err := EnsureSpeechGuardSchema(e.Store.DB); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.MuteScene(sceneWireKey, clock.Now().Add(30*time.Minute).Format(bodyTSLayout), "闭嘴"); err != nil {
		t.Fatal(err)
	}
	res := e.GateGroupMessage(SceneMsg{SceneKey: sceneWireKey, UIN: "111", Text: "在吗",
		AtUINs: []string{"3487037837"}, TS: clock.Now()},
		SelfIdentity{UIN: "3487037837"}, sceneWireCfg())
	if res.Stance != StanceSpeak {
		t.Fatalf("被 @ 时静默没有解除: %s (%s)", res.Stance, res.Reason)
	}
	if _, _, muted := e.Store.SceneMute(sceneWireKey, clock.Now()); muted {
		t.Fatal("被 @ 之后静默应当解除")
	}
}

// 预算: 10 分钟上限 4 条 —— 超了就不再开口 (线程延续也一样)。
func TestGate_SpeechBudgetCapsBurst(t *testing.T) {
	e := gateEngine(t)
	if err := EnsureSpeechGuardSchema(e.Store.DB); err != nil {
		t.Fatal(err)
	}
	now := clock.Now()
	fiveAgo := now.Add(-5 * time.Minute).Format(bodyTSLayout)
	for i := 0; i < 4; i++ {
		id, err := e.Store.InsertMessageFull("ai", "qq", "text", "它说的一句", MsgMeta{SceneKey: sceneWireKey})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.Store.DB.Exec("UPDATE convo_messages SET ts=? WHERE id=?", fiveAgo, id); err != nil {
			t.Fatal(err)
		}
	}
	res := e.GateGroupMessage(SceneMsg{
		SceneKey: sceneWireKey, UIN: "111", Text: "然后呢",
		HasPrev: true, PrevUIN: "3487037837", PrevTS: now.Add(-5 * time.Minute), TS: now,
	}, SelfIdentity{UIN: "3487037837"}, sceneWireCfg())
	if res.Stance != StanceSilent || !strings.Contains(res.Reason, "话太密") {
		t.Fatalf("超预算还在说: %s (%s)", res.Stance, res.Reason)
	}
}

// 反面: 预算没超时, 线程延续照常接。
func TestGate_ThreadSpeaksWhenBudgetOk(t *testing.T) {
	e := gateEngine(t)
	if err := EnsureSpeechGuardSchema(e.Store.DB); err != nil {
		t.Fatal(err)
	}
	now := clock.Now()
	id, err := e.Store.InsertMessageFull("ai", "qq", "text", "它说的一句", MsgMeta{SceneKey: sceneWireKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.DB.Exec("UPDATE convo_messages SET ts=? WHERE id=?", now.Add(-5*time.Minute).Format(bodyTSLayout), id); err != nil {
		t.Fatal(err)
	}
	res := e.GateGroupMessage(SceneMsg{
		SceneKey: sceneWireKey, UIN: "111", Text: "然后呢",
		HasPrev: true, PrevUIN: "3487037837", PrevTS: now.Add(-5 * time.Minute), TS: now,
	}, SelfIdentity{UIN: "3487037837"}, sceneWireCfg())
	if res.Stance != StanceSpeak {
		t.Fatalf("预算正常却不敢接: %s (%s)", res.Stance, res.Reason)
	}
}
