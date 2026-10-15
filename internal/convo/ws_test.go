package convo

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// ——— v2.4.3: WS 端到端 — 真实 WS 帧 → OneBotEvent → ImageFiles/PlainText ———

func wsRoundTrip(t *testing.T, payload string) OneBotEvent {
	t.Helper()
	got := make(chan OneBotEvent, 1)
	ws := &OneBotWS{OnEvent: func(ev OneBotEvent) { got <- ev }}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	srv := &http.Server{Handler: ws.Handler()}
	go srv.Serve(ln)
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws://"+ln.Addr().String()+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-got:
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("OnEvent 未收到事件")
		return OneBotEvent{}
	}
}

func TestWS_Event_CQStringForm_ImageFiles(t *testing.T) {
	// NapCat messagePostFormat=string: message 字段是含 CQ 码的 JSON 字符串
	payload := `{"post_type":"message","message_type":"private","user_id":2735604537,` +
		`"message":"[CQ:image,file=abc.jpg,url=https://multimedia.nt.qq.com.cn/download?fileid=1&amp;rkey=2,type=flash]",` +
		`"raw_message":"[CQ:image,file=abc.jpg,url=https://multimedia.nt.qq.com.cn/download?fileid=1&amp;rkey=2]",` +
		`"message_id":123,"time":1234567890}`
	ev := wsRoundTrip(t, payload)
	files := ev.ImageFiles()
	if len(files) != 1 {
		t.Fatalf("CQ string event: ImageFiles=%v want 1", files)
	}
	want := "https://multimedia.nt.qq.com.cn/download?fileid=1&rkey=2"
	if files[0] != want {
		t.Fatalf("ImageFiles[0]=%q want %q (url 优先+反转义)", files[0], want)
	}
}

func TestWS_Event_ArrayForm_ImageFiles(t *testing.T) {
	// ⚠️ 2026-09-07 msg-diag 实证: data 含数字 sub_type:1 — map[string]string 会整组炸
	payload := `{"post_type":"message","message_type":"private","user_id":2735604537,` +
		`"message":[{"type":"image","data":{"summary":"[动画表情]","file":"96D7D18E7A190419CC210CF653839079.jpg","sub_type":1,"url":"https://multimedia.nt.qq.com.cn/download?fileid=1&amp;rkey=2"}}],` +
		`"raw_message":"[CQ:image,summary=&#91;动画表情&#93;,file=96D7D18E7A1904]",` +
		`"message_id":124,"time":1234567891}`
	ev := wsRoundTrip(t, payload)
	files := ev.ImageFiles()
	if len(files) != 1 {
		t.Fatalf("array event (sub_type): ImageFiles=%v want 1", files)
	}
	if files[0] != "https://multimedia.nt.qq.com.cn/download?fileid=1&rkey=2" {
		t.Fatalf("ImageFiles[0]=%q (url 优先+反转义)", files[0])
	}
	// PlainText 不得是 raw_message 的 CQ (Segments 成功后应只有 text 段)
	if got := ev.PlainText(); strings.Contains(got, "[CQ:") {
		t.Fatalf("PlainText 泄漏 CQ: %q", got)
	}
}

func TestWS_Event_JSONArrayAsString_ImageFiles(t *testing.T) {
	// NapCat 序列化产物: message 是「JSON 数组的转义字符串」
	inner := `[{"type":"image","data":{"file":"a.jpg","sub_type":1,"url":"https://example.com/i.png"}}]`
	b, _ := json.Marshal(inner)
	payload := `{"post_type":"message","message_type":"private","user_id":2735604537,` +
		`"message":` + string(b) + `,"raw_message":"[图片]","message_id":125,"time":1234567892}`
	ev := wsRoundTrip(t, payload)
	files := ev.ImageFiles()
	if len(files) != 1 || files[0] != "https://example.com/i.png" {
		t.Fatalf("JSON-array-as-string: ImageFiles=%v", files)
	}
}

func TestWS_Event_MetaIgnored(t *testing.T) {
	// meta_event 不应触发 OnEvent
	payload := `{"post_type":"meta_event","meta_event_type":"heartbeat","time":1234567892,"status":{}}`
	ws := &OneBotWS{OnEvent: func(ev OneBotEvent) { t.Fatalf("meta_event 不应回调") }}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	srv := &http.Server{Handler: ws.Handler()}
	go srv.Serve(ln)
	defer srv.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws://"+ln.Addr().String()+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // 无回调即通过
}

func TestWS_Event_BadJSON_Skipped(t *testing.T) {
	got := make(chan OneBotEvent, 1)
	ws := &OneBotWS{OnEvent: func(ev OneBotEvent) { got <- ev }}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	srv := &http.Server{Handler: ws.Handler()}
	go srv.Serve(ln)
	defer srv.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws://"+ln.Addr().String()+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"post_type":"message","message":`))
	// 坏帧后发好帧, 坏帧应被跳过 (继续循环)
	good := `{"post_type":"message","message_type":"private","user_id":1,"message":"你好","raw_message":"你好"}`
	if err := conn.WriteMessage(websocket.TextMessage, []byte(good)); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-got:
		if ev.UserID != 1 {
			t.Fatalf("bad frame should be skipped, got %s", string(ev.Message))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("good event not delivered after bad frame")
	}
}

var _ = json.RawMessage(nil) // keep import if assertions change
