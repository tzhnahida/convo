package convo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
)

// ——— 通道: 戳一戳的两个 payload ———

func TestPokeChannel_FriendAndGroupPayload(t *testing.T) {
	var mu = make(chan struct{}, 1)
	mu <- struct{}{}
	type oneCall struct {
		path string
		body map[string]interface{}
	}
	var calls []oneCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		<-mu
		calls = append(calls, oneCall{path: r.URL.Path, body: body})
		mu <- struct{}{}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"status\":\"ok\",\"retcode\":0,\"data\":{}}"))
	}))
	defer srv.Close()
	c := NewOneBotClient(srv.URL, "")
	if err := c.Poke(0, 2735604537); err != nil {
		t.Fatal(err)
	}
	if err := c.Poke(1034712035, 2735604537); err != nil {
		t.Fatal(err)
	}
	<-mu
	byPath := map[string]map[string]interface{}{}
	for _, cl := range calls {
		byPath[cl.path] = cl.body
	}
	if b := byPath["/friend_poke"]; b == nil || b["user_id"] == nil {
		t.Fatalf("私戳要走 friend_poke 且带 user_id: %+v", b)
	}
	if b := byPath["/group_poke"]; b == nil || b["group_id"] == nil || b["user_id"] == nil {
		t.Fatalf("群戳要走 group_poke 且带 group_id+user_id: %+v", b)
	}
	if b := byPath["/friend_poke"]; b != nil && b["group_id"] != nil {
		t.Fatal("私戳不该带 group_id")
	}
}

// ——— 裁决 ———

// pokeEngine 造一个只测戳一戳的引擎: 骰子与心情都钉住 (理由同 gateEngine)。
func pokeEngine(t *testing.T, cfg PokeConfig, roll int, line string) (*Engine, *Store, *fakeSelfSender) {
	t.Helper()
	st := newTestStore(t)
	if err := st.SaveProfile(&PersonaProfile{Name: "小夜", Template: "bestie", CreatedTS: clock.TodayTS()}); err != nil {
		t.Fatal(err)
	}
	fs := &fakeSelfSender{}
	e := &Engine{
		Store:   st,
		Sender:  fs,
		Chatter: fakeJudger{reply: line},
		UserID:  2735604537,
	}
	e.Cfg.Poke = cfg
	e.PokeRollFn = func(n int) int { return roll }
	e.DayMoodFn = func(string) DayMood { return DayMoodFlat }
	return e, st, fs
}

// sleepNow 造一张"现在正睡着"的作息表。
//
// ⚠️ 必须落到 **Days[].Slots** 上: `CurrentState` 只看那一层,
// 顶层的 SleepStart/SleepEnd 它**不看** (那是别的函数在用)。
// 我第一版就是只写顶层 → 睡着不成立 → 用例红了, 而代码是对的。
func sleepNow(t *testing.T, st *Store) {
	t.Helper()
	now := clock.Now()
	ws := &WeekSchedule{Days: []DaySchedule{{
		Day: weekdayKey(now),
		// ⚠️ 用**整天**覆盖, 不要写"现在前后半小时": CurrentState 的区间是
		// `min >= start && min < end`, **不支持跨午夜** —— 在 00:xx 造一个
		// 23:55→00:55 的窗口, 睡着根本不成立, 而用例会在凌晨无故变红
		// (这种坑这个仓库已经踩过一次, 见 valuegate_test.go 的 DayMoodFn)。
		Slots: []Slot{{
			Start: "00:00",
			End:   "23:59",
			State: StateSleep,
		}},
	}}}
	if err := st.SaveSchedule(ws); err != nil {
		t.Fatal(err)
	}
}

func pokeRows(t *testing.T, st *Store) []PokeRecord {
	t.Helper()
	rows, err := st.LoadPokes(20)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// TestPoke_PokesBack 默认那一下: 戳回去。
func TestPoke_PokesBack(t *testing.T) {
	e, st, fs := pokeEngine(t, PokeConfig{Enabled: true, DelayMaxSec: 0}, 0, "干嘛")
	if err := e.HandlePoke(context.Background(), PokeEvent{SceneKey: "private:1", UIN: "1", Private: true}); err != nil {
		t.Fatal(err)
	}
	if fs.pokeCount() != 1 {
		t.Fatalf("应当戳回去一次, 实为 %d 次", fs.pokeCount())
	}
	if rows := pokeRows(t, st); len(rows) != 1 || rows[0].Replied != PokeReplyPoke {
		t.Fatalf("审计里应当记一次 poke: %+v", rows)
	}
}

// TestPoke_SaysALine 掷到搭话那一段: 用模型给的话, 不是写死的。
func TestPoke_SaysALine(t *testing.T) {
	e, st, fs := pokeEngine(t, PokeConfig{Enabled: true, DelayMaxSec: 0}, 80, "在的在的")
	if err := e.HandlePoke(context.Background(), PokeEvent{SceneKey: "group:1034712035", UIN: "1", GroupID: 1034712035}); err != nil {
		t.Fatal(err)
	}
	if fs.groupMsgCount() != 1 || fs.groupMsgs[0] != "在的在的" {
		t.Fatalf("群戳应当在群里搭一句话: %+v", fs.groupMsgs)
	}
	if rows := pokeRows(t, st); len(rows) != 1 || rows[0].Replied != PokeReplyLine {
		t.Fatalf("审计里应当记一次 line: %+v", rows)
	}
}

// TestPoke_AsleepUnlessPokedRepeatedly 睡着就装死; 连戳三次才算急事。
func TestPoke_AsleepUnlessPokedRepeatedly(t *testing.T) {
	e, st, fs := pokeEngine(t, PokeConfig{Enabled: true, DelayMaxSec: 0}, 0, "怎么了")
	sleepNow(t, st)
	ev := PokeEvent{SceneKey: "private:1", UIN: "1", Private: true}
	if err := e.HandlePoke(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if fs.pokeCount() != 0 {
		t.Fatal("睡着了不该回")
	}
	rows := pokeRows(t, st)
	if len(rows) != 1 || !strings.Contains(rows[0].Reason, "睡了") {
		t.Fatalf("审计要写清是「睡了」: %+v", rows)
	}
	// 第二下仍然不理 (默认 SleepUnlessTimes=3, 也就是"戳满三下"才当急事)。
	if err := e.HandlePoke(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if fs.pokeCount() != 0 {
		t.Fatal("睡着时第二下还不该理")
	}
	// 第三下: 连戳到门槛, 当急事 —— 回一下。
	if err := e.HandlePoke(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if fs.pokeCount() != 1 {
		t.Fatalf("睡着被连戳到门槛该回一下, 实为 %d 次", fs.pokeCount())
	}
}

// TestPoke_Cooldown 同一个人刚被回过 → 装死 (限流是必须的, 否则连戳变刷屏)。
func TestPoke_Cooldown(t *testing.T) {
	e, st, fs := pokeEngine(t, PokeConfig{Enabled: true, DelayMaxSec: 0}, 0, "干嘛")
	if err := st.InsertPoke(PokeRecord{UIN: "1", Replied: PokeReplyPoke, Reason: "戳回去",
		TS: clock.Now().Add(-1 * time.Minute).Format("2006-01-02 15:04:05")}); err != nil {
		t.Fatal(err)
	}
	if err := e.HandlePoke(context.Background(), PokeEvent{SceneKey: "private:1", UIN: "1", Private: true}); err != nil {
		t.Fatal(err)
	}
	if fs.pokeCount() != 0 {
		t.Fatal("一分钟前刚回过, 这次该装死")
	}
	rows := pokeRows(t, st)
	if len(rows) != 2 || !strings.Contains(rows[0].Reason, "刚回过") {
		t.Fatalf("审计要写清是冷却: %+v", rows)
	}
}

// TestPoke_DailyCap 今天回够了 → 装死。
func TestPoke_DailyCap(t *testing.T) {
	e, st, fs := pokeEngine(t, PokeConfig{Enabled: true, DelayMaxSec: 0, DailyCap: 2}, 0, "干嘛")
	for i := 0; i < 2; i++ {
		if err := st.InsertPoke(PokeRecord{UIN: "9", Replied: PokeReplyPoke,
			TS: clock.Now().Format("2006-01-02 15:04:05")}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.HandlePoke(context.Background(), PokeEvent{SceneKey: "private:1", UIN: "1", Private: true}); err != nil {
		t.Fatal(err)
	}
	if fs.pokeCount() != 0 {
		t.Fatal("今天已经回够了, 该装死")
	}
	// 前两行是夹具塞进去凑上限的, 新的那行在最前。
	rows := pokeRows(t, st)
	if len(rows) != 3 || !strings.Contains(rows[0].Reason, "戳够了") {
		t.Fatalf("审计要写清是上限: %+v", rows)
	}
}

// TestPoke_LowMoodDay 低意愿日: 第一下不理, 第二下才理。
func TestPoke_LowMoodDay(t *testing.T) {
	e, st, fs := pokeEngine(t, PokeConfig{Enabled: true, DelayMaxSec: 0}, 0, "干嘛")
	e.DayMoodFn = func(string) DayMood { return DayMoodLow }
	ev := PokeEvent{SceneKey: "private:1", UIN: "1", Private: true}
	if err := e.HandlePoke(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if fs.pokeCount() != 0 {
		t.Fatal("低意愿日的第一下该装死")
	}
	if err := e.HandlePoke(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if fs.pokeCount() != 1 {
		t.Fatal("第二下该理 (连戳算急事)")
	}
	_ = st
}

// TestPoke_DisabledDoesNothing 总开关关着: 连审计都不写。
func TestPoke_DisabledDoesNothing(t *testing.T) {
	e, st, fs := pokeEngine(t, PokeConfig{Enabled: false}, 0, "干嘛")
	if err := e.HandlePoke(context.Background(), PokeEvent{SceneKey: "private:1", UIN: "1", Private: true}); err != nil {
		t.Fatal(err)
	}
	if fs.pokeCount() != 0 {
		t.Fatal("关着还敢戳")
	}
	if rows := pokeRows(t, st); len(rows) != 0 {
		t.Fatalf("关着时不该有任何记录: %+v", rows)
	}
}
