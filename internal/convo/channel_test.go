package convo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockOneBotAPI 轨1 (标准): 期望 POST /{action} + 裸 payload 并回应 data。
func mockOneBotAPI(t *testing.T, respond func(action string, body []byte) (interface{}, int)) (*OneBotClient, *string) {
	t.Helper()
	var lastAction string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastAction = r.URL.Path
		var payload interface{}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		data, code := respond(r.URL.Path, nil)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "ok", "retcode": 0, "data": data,
		})
	}))
	return NewOneBotClient(srv.URL, ""), &lastAction
}

func TestOneBot_SendText_standardTrack(t *testing.T) {
	// v2.3.4 语义: 轨1 = POST /send_private_msg + 裸 payload {user_id, message}
	var gotPath string
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","retcode":0,"data":{"message_id":888}}`))
	}))
	defer srv.Close()
	c := NewOneBotClient(srv.URL, "")
	if err := c.SendText(10001, "你好呀"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/send_private_msg" {
		t.Fatalf("path = %q, want /send_private_msg", gotPath)
	}
	if _, wrapped := gotBody["action"]; wrapped {
		t.Fatal("body must be RAW payload, not {action,params} wrapper")
	}
	msgs, _ := gotBody["message"].([]interface{})
	if len(msgs) != 1 {
		t.Fatalf("message = %v", gotBody["message"])
	}
	if u, ok := gotBody["user_id"].(float64); !ok || int(u) != 10001 {
		t.Fatalf("user_id = %v", gotBody["user_id"])
	}
}

func TestOneBot_SendImage(t *testing.T) {
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","retcode":0,"data":{"message_id":1}}`))
	}))
	defer srv.Close()
	c := NewOneBotClient(srv.URL, "")
	if err := c.SendImage(10001, "/data/convo/stickers/ja/cat.png"); err != nil {
		t.Fatal(err)
	}
	msgs, _ := gotBody["message"].([]interface{})
	if len(msgs) != 1 {
		t.Fatalf("message = %v", gotBody["message"])
	}
	seg := msgs[0].(map[string]interface{})
	if seg["type"] != "image" || !strings.HasSuffix(seg["data"].(map[string]interface{})["file"].(string), "cat.png") {
		t.Fatalf("image segment = %v", seg)
	}
}

func TestOneBot_LoginInfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","retcode":0,"data":{"user_id":3934134900,"nickname":"纳西妲"},"echo":""}`))
	}))
	defer srv.Close()
	c := NewOneBotClient(srv.URL, "")
	id, name, err := c.LoginInfo()
	if err != nil {
		t.Fatal(err)
	}
	if id != 3934134900 || name != "纳西妲" {
		t.Fatalf("login = %d %q", id, name)
	}
	if c.selfName != "纳西妲" {
		t.Fatalf("selfName cache = %q", c.selfName)
	}
}

// 双轨: 轨1 404 → 轨2 (action-in-body 包装) 成功
func TestOneBot_call_dualTrack(t *testing.T) {
	var paths, bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 512)
		n, _ := r.Body.Read(b)
		paths = append(paths, r.URL.Path)
		bodies = append(bodies, string(b[:n]))
		if r.URL.Path == "/send_private_msg" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","retcode":0,"data":{"message_id":1}}`))
	}))
	defer srv.Close()
	c := NewOneBotClient(srv.URL, "")
	if err := c.SendText(10001, "hello"); err != nil {
		t.Fatalf("dual track failed: %v", err)
	}
	if len(paths) != 2 || paths[0] != "/send_private_msg" || paths[1] != "/" {
		t.Fatalf("paths = %v", paths)
	}
	// 轨1 裸 payload; 轨2 包装
	if !strings.Contains(bodies[0], `"user_id"`) || strings.Contains(bodies[0], `"action"`) {
		t.Fatalf("track1 body = %s (want raw payload)", bodies[0])
	}
	if !strings.Contains(bodies[1], `"action":"send_private_msg"`) {
		t.Fatalf("track2 body = %s (want wrapper)", bodies[1])
	}
}

func TestOneBot_SetSignature_unsupported(t *testing.T) {
	// 非 200 → 应返回错误 (调用方降级, 不中断)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 轨1 也 400 → 整体失败
		w.WriteHeader(400)
	}))
	defer srv.Close()
	c := NewOneBotClient(srv.URL, "")
	c.selfName = "纳西妲" // 已缓存昵称
	if err := c.SetSignature("摸鱼中"); err == nil {
		t.Fatal("expected error on 400")
	}
}

func TestOneBotEvent_PlainText(t *testing.T) {
	ev := OneBotEvent{
		Message: json.RawMessage(`[
			{"type":"text","data":{"text":"测试 "}},
			{"type":"face","data":{"id":"14"}},
			{"type":"image","data":{"file":"x.png"}}
		]`),
	}
	got := ev.PlainText()
	if got != "测试 [QQ表情14]" {
		t.Fatalf("PlainText = %q", got)
	}
	segs, err := ev.Segments()
	if err != nil || len(segs) != 3 {
		t.Fatalf("segments: %v %v", segs, err)
	}
}
