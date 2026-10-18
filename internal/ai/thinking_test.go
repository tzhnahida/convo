package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestThinkingSwitch 钉住两条相反的口径 —— 它们决定 temperature 到底生不生效。
//
// 背景 (2026-10-18): 服务的默认是**思考开启** (effort=high), 而思考模式下
// temperature 官方"设置不报错但不生效"。于是:
//   - 交互级 client 必须显式发 thinking.type=disabled, 否则整套拟人化抖动被静默吞掉;
//   - 后台级 client 必须**不发**这个字段, 否则思考就白开了。
//
// 两个方向都断死, 是因为"少发一个字段"和"多发一个字段"在日志里都看不出来。
func TestThinkingSwitch(t *testing.T) {
	var lastBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastBody, _ = io.ReadAll(r.Body)
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()

	do := func(disable bool) map[string]any {
		c := NewClient(Config{
			BaseURL:         srv.URL,
			APIKey:          "test-key",
			Model:           "deepseek-flash",
			DisableThinking: disable,
		})
		msgs := []Message{{Role: "system", Content: "系统"}, {Role: "user", Content: "用户"}}
		if _, err := c.Chat(context.Background(), msgs, 0.7); err != nil {
			t.Fatalf("chat: %v", err)
		}
		var req map[string]any
		if err := json.Unmarshal(lastBody, &req); err != nil {
			t.Fatalf("request 不是 JSON: %v (%s)", err, string(lastBody))
		}
		return req
	}

	// ① 交互级: 必须明确关掉思考。
	off := do(true)
	th, ok := off["thinking"].(map[string]any)
	if !ok || th["type"] != "disabled" {
		t.Fatalf("交互级请求必须带 thinking.type=disabled, 实际: %v", off["thinking"])
	}
	if _, has := off["temperature"]; !has {
		t.Fatalf("交互级请求必须带 temperature (拟人化抖动的落点)")
	}

	// ② 后台级: 不许带这个字段 (缺省 = 服务端默认思考)。
	on := do(false)
	if v, has := on["thinking"]; has {
		t.Fatalf("后台级请求不许带 thinking 字段, 实际: %v", v)
	}
}
