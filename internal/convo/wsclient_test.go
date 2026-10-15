package convo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// ⚠️ token 不许进日志 —— 日志是最容易被随手贴出去的东西。
func TestRedactWSToken(t *testing.T) {
	got := RedactWSToken("ws://127.0.0.1:3102/ws?access_token=SECRET123&x=1")
	if strings.Contains(got, "SECRET123") {
		t.Fatalf("token 泄漏进日志: %q", got)
	}
	if !strings.Contains(got, "x=1") {
		t.Fatalf("后面的参数被吃掉了: %q", got)
	}
	if RedactWSToken("ws://a/ws") != "ws://a/ws" {
		t.Fatal("没有 token 时不该改动")
	}
	if !strings.Contains(RedactWSToken("ws://a/ws?access_token=S"), "***") {
		t.Fatal("末尾的 token 也要抹")
	}
}

// ⚠️ 双问号是上一批在 dashboard 上踩过的坑 —— 同一个错误不该犯两次。
func TestWSURLWithToken(t *testing.T) {
	c := ChannelConfig{WSURL: "ws://127.0.0.1:3102/ws", AccessToken: "T"}
	if got := c.WSURLWithToken(); got != "ws://127.0.0.1:3102/ws?access_token=T" {
		t.Fatalf("got %q", got)
	}
	c2 := ChannelConfig{WSURL: "ws://h/ws?a=1", AccessToken: "T"}
	if got := c2.WSURLWithToken(); got != "ws://h/ws?a=1&access_token=T" {
		t.Fatalf("已有 query 时必须用 &, got %q", got)
	}
	c3 := ChannelConfig{WSURL: "ws://h/ws?access_token=X", AccessToken: "T"}
	if got := c3.WSURLWithToken(); got != "ws://h/ws?access_token=X" {
		t.Fatalf("已带 token 时不许再拼一个, got %q", got)
	}
	if (ChannelConfig{WSURL: "ws://h/ws"}).WSURLWithToken() != "ws://h/ws" {
		t.Fatal("没有 token 时不该加参数")
	}
	if (ChannelConfig{}).WSURLWithToken() != "" {
		t.Fatal("空地址应当返回空")
	}
}

// ⚠️ **两个朝向共用这一份** —— 抄第二份的话, 过滤规则迟早分叉。
func TestHandleRaw_FiltersMetaAndBadJSON(t *testing.T) {
	got := make(chan OneBotEvent, 4)
	ws := &OneBotWS{OnEvent: func(ev OneBotEvent) { got <- ev }}
	ws.HandleRaw([]byte("{ bad json"))
	ws.HandleRaw([]byte("{\"post_type\":\"meta_event\",\"meta_event_type\":\"heartbeat\"}"))
	ws.HandleRaw([]byte("{\"post_type\":\"message\",\"message_type\":\"group\"}"))
	select {
	case ev := <-got:
		if ev.PostType != "message" {
			t.Fatalf("应当只放行 message, got %q", ev.PostType)
		}
	case <-time.After(time.Second):
		t.Fatal("合法事件被吞了")
	}
	select {
	case ev := <-got:
		t.Fatalf("多放了一条: %+v", ev)
	default:
	}
}

func TestHandleRaw_NilOnEvent(t *testing.T) {
	ws := &OneBotWS{}
	ws.HandleRaw([]byte("{\"post_type\":\"message\"}")) // 不许 panic
}

func TestStartOneBotWSClient_EmptyURLIsError(t *testing.T) {
	if err := StartOneBotWSClient(context.Background(), "  ", &OneBotWS{}); err == nil {
		t.Fatal("空地址必须报错, 而不是静默什么都不做")
	}
}

// 端到端: 我们**主动连出去**, 收到事件。这正是 nimbus 那个朝向。
func TestStartOneBotWSClient_ReceivesEvents(t *testing.T) {
	var up websocket.Upgrader
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("access_token") != "T" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.WriteJSON(map[string]interface{}{"post_type": "meta_event"})
		_ = c.WriteJSON(map[string]interface{}{"post_type": "message", "message_type": "group", "raw_message": "zaiza"})
		time.Sleep(300 * time.Millisecond)
	}))
	defer srv.Close()

	got := make(chan OneBotEvent, 2)
	ws := &OneBotWS{OnEvent: func(ev OneBotEvent) { got <- ev }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "?access_token=T"
	if err := StartOneBotWSClient(ctx, url, ws); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-got:
		if ev.RawMessage != "zaiza" {
			t.Fatalf("事件内容不对: %+v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("连出去之后没收到事件 —— 正向 WS 没工作")
	}
}

// 断开之后必须自己接回来 —— 否则桥接方重启一次它就永久哑了。
func TestStartOneBotWSClient_ReconnectsAfterClose(t *testing.T) {
	var up websocket.Upgrader
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = c.WriteJSON(map[string]interface{}{"post_type": "message"})
		c.Close() // 立刻断开 → 客户端必须重连
	}))
	defer srv.Close()
	got := make(chan OneBotEvent, 8)
	ws := &OneBotWS{OnEvent: func(ev OneBotEvent) { got <- ev }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := StartOneBotWSClient(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), ws); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(6 * time.Second)
	count := 0
	for count < 2 {
		select {
		case <-got:
			count++
		case <-deadline:
			t.Fatalf("断开后没有重连 (只收到 %d 次)", count)
		}
	}
}
