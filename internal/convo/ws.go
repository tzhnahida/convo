package convo

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/tzhnahida/convo/internal/log"
)

// OneBotWS serves the reverse-WebSocket endpoint that NapCat connects to
// (NapCat → our listener = outbound from Pi's perspective, per §8.1b
// network discipline; both processes live on the Pi so this is localhost).
type OneBotWS struct {
	AccessToken string
	OnEvent     func(ev OneBotEvent) // nil-safe
}

// HandleRaw 处理一条原始事件。
//
// ⚠️ **服务端与客户端共用这一份** —— 这是本文件唯一的事件入口。
// 抄第二份的话, 心跳过滤 / meta 过滤 / OnEvent 调用顺序迟早会在两边分叉,
// 而现象是"换个朝向就行为不一样", 极难查。
func (s *OneBotWS) HandleRaw(raw []byte) {
	var ev OneBotEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		log.Warn("convo:ws", "bad event json: %v", err)
		return
	}
	// meta_event (心跳/生命周期) 直接忽略
	if ev.PostType == "meta_event" {
		return
	}
	if s.OnEvent != nil {
		s.OnEvent(ev)
	}
}

func (s *OneBotWS) Handler() http.Handler {
	var upgrader = websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin:     func(r *http.Request) bool { return true }, // localhost-only listener
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.AccessToken != "" {
			auth := r.Header.Get("Authorization")
			// OneBot 惯例: Bearer token 或 query access_token
			if token := strings.TrimPrefix(auth, "Bearer "); token != s.AccessToken &&
				r.URL.Query().Get("access_token") != s.AccessToken {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Warn("convo:ws", "upgrade: %v", err)
			return
		}
		defer conn.Close()
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				log.Debug("convo:ws", "read: %v (disconnect)", err)
				return
			}
			s.HandleRaw(raw)
		}
	})
}

// StartOneBotWS serves the WS endpoint on its own listener (blocking until ctx done).
func StartOneBotWS(ctx context.Context, listen string, ws *OneBotWS) error {
	mux := http.NewServeMux()
	mux.Handle("/ws", ws.Handler())
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("ws listen %s: %w", listen, err)
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Error("convo:ws", "serve: %v", err)
		}
	}()
	log.Info("convo:ws", "listening %s (NapCat 反向 WS 接入点)", listen)
	return nil
}
