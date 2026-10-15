package convo

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ⚠️ **这条是「复读机」的根因尺子** (2026-10-14)。
//
// `call` 是双轨的: 轨 1 打 `{base}/{action}` + 裸 payload, 失败就退轨 2
// 打 `{base}` + 包装体。**兜底本身是对的** —— NapCat 的形态差异确实存在。
//
// 问题在于"失败"两个字被当成了同一件事。传输层失败 (超时/连接被掐)
// 的真实含义是**不知道对方到底执行了没有** —— 那时再打轨 2,
// 就是让同一条消息发两遍。群友看到的就是它复读。
//
// 对 `get_image` 无所谓 (幂等), 对 `send_group_msg` / `send_private_msg`
// 就是实打实的重发。
func TestCall_TransportFailureMustNotFallThroughToTrack2(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			// 轨 1: **收下了请求** (记录命中), 然后掐断连接 —— 模拟"对方可能已经做了"。
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Skip("该测试服务器不支持 hijack")
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Fatalf("hijack: %v", err)
			}
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{}}`))
	}))
	defer srv.Close()

	c := NewOneBotClient(srv.URL, "")
	if err := c.call("send_group_msg", map[string]interface{}{"group_id": 1, "message": "在吗"}, nil); err == nil {
		t.Fatal("连接被掐断却报成功")
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("轨 1 传输失败后又打了 %d 次 —— 群里会看到它复读", n-1)
	}
}

// 反面: **服务端明确回了错误** 时, 兜底必须照旧 —— 那是双轨存在的理由。
func TestCall_HttpErrorStillFallsThroughToTrack2(t *testing.T) {
	var track2 int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/send_group_msg") {
			w.WriteHeader(http.StatusNotFound) // 这个子路径不存在 → 兜底
			return
		}
		atomic.AddInt32(&track2, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{}}`))
	}))
	defer srv.Close()

	c := NewOneBotClient(srv.URL, "")
	if err := c.call("send_group_msg", map[string]interface{}{"group_id": 1}, nil); err != nil {
		t.Fatalf("404 之后兜底应当成功: %v", err)
	}
	if atomic.LoadInt32(&track2) != 1 {
		t.Fatal("404 之后没有走兜底轨 —— 把双轨设计一起废掉了")
	}
}

// 兜底轨自己**传输失败**时 → 到此为止, 不许再有任何第三次。
func TestCall_Track2TransportFailureDoesNotLoop(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if strings.HasSuffix(r.URL.Path, "/send_group_msg") {
			w.WriteHeader(http.StatusNotFound) // 轨 1 被明确拒绝 → 允许退轨 2
			return
		}
		// 轨 2: 收下请求后掐断 —— 结局未知。
		hj := w.(http.Hijacker)
		conn, _, _ := hj.Hijack()
		_ = conn.Close()
	}))
	defer srv.Close()

	c := NewOneBotClient(srv.URL, "")
	if err := c.call("send_group_msg", map[string]interface{}{"group_id": 1}, nil); err == nil {
		t.Fatal("兜底轨断了却报成功")
	}
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Fatalf("两轨各试一次应命中 2 次, 实际 %d (不能更多)", n)
	}
}

// 正常成功路径一个字都不许变。
func TestCall_HappyPathSingleHit(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"message_id":7}}`))
	}))
	defer srv.Close()

	c := NewOneBotClient(srv.URL, "")
	var out struct {
		MessageID int32 `json:"message_id"`
	}
	if err := c.call("send_group_msg", map[string]interface{}{"group_id": 1}, &out); err != nil {
		t.Fatal(err)
	}
	if out.MessageID != 7 {
		t.Fatalf("data 没解出来: %+v", out)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("成功路径命中 %d 次, 应为 1", n)
	}
}

// 超时同理: 请求**可能已经送达**。
func TestCall_TimeoutIsUnknownOutcome(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		time.Sleep(400 * time.Millisecond) // 比客户端超时长
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0}`))
	}))
	defer srv.Close()

	c := NewOneBotClient(srv.URL, "")
	c.http.Timeout = 80 * time.Millisecond
	_ = c.call("send_group_msg", map[string]interface{}{"group_id": 1}, nil)
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("超时后又打了 %d 次 —— 同一条消息会发两遍", n-1)
	}
}
